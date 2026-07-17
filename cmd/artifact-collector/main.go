package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/utilitycontract"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func main() {
	namespace := flag.String("namespace", "", "workflow namespace")
	workflow := flag.String("workflow", "", "workflow name")
	attempt := flag.String("attempt", "", "step attempt name")
	producerKind := flag.String("producer-kind", "", "authoritative producer resource kind")
	producerAPIVersion := flag.String("producer-api-version", v1alpha1.GroupVersion.String(), "authoritative producer API version")
	resultPath := flag.String("result", "", "agent result contract")
	stagingPath := flag.String("staging", "", "attempt staging directory")
	artifactPath := flag.String("artifact-store", "", "content-addressed artifact directory")
	auditEventsPath := flag.String("audit-events", "", "agent wrapper audit event JSONL path")
	sourceRevision := flag.String("source-revision", "", "source revision")
	flag.Parse()
	if *namespace == "" || *workflow == "" || *attempt == "" || *producerKind == "" || *resultPath == "" || *stagingPath == "" || *artifactPath == "" {
		log.Fatal("namespace, workflow, attempt, producer-kind, result, staging, and artifact-store are required")
	}
	if err := ingestRuntimeAudit(context.Background(), *auditEventsPath); err != nil {
		log.Fatalf("ingest runtime audit: %v", err)
	}
	result, err := readResult(*resultPath, *stagingPath)
	if err != nil {
		log.Fatalf("validate result contract: %v", err)
	}
	producer := v1alpha1.TypedLocalReference{APIVersion: *producerAPIVersion, Kind: *producerKind, Name: *attempt}
	collected, err := artifacts.Collect(*stagingPath, *artifactPath, *workflow, producer, *sourceRevision, result.Artifacts)
	if err != nil {
		log.Fatalf("collect artifacts: %v", err)
	}
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		log.Fatal(err)
	}
	kubernetes, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: scheme})
	if err != nil {
		log.Fatalf("create Kubernetes client: %v", err)
	}
	for index, item := range collected {
		name := artifactName(*attempt, item.Spec.Contract.Name, index)
		artifact := &v1alpha1.Artifact{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: *namespace, Labels: map[string]string{"sovereign-ai.io/workflow-id": *workflow, "sovereign-ai.io/attempt": *attempt}}, Spec: item.Spec}
		if err := kubernetes.Create(context.Background(), artifact); err != nil {
			if apierrors.IsAlreadyExists(err) {
				var existing v1alpha1.Artifact
				if getErr := kubernetes.Get(context.Background(), client.ObjectKeyFromObject(artifact), &existing); getErr == nil && existing.Spec.Digest == artifact.Spec.Digest {
					continue
				}
			}
			log.Fatalf("create Artifact %s: %v", name, err)
		}
	}
}

func readResult(path, stagingPath string) (agentcontract.Result, error) {
	result, err := agentcontract.ReadResult(path, stagingPath)
	if err == nil {
		return result, nil
	}
	utilityResult, utilityErr := utilitycontract.ReadResult(path, stagingPath)
	if utilityErr != nil {
		return agentcontract.Result{}, err
	}
	converted := agentcontract.Result{
		SchemaVersion: agentcontract.Version,
		Outcome:       utilityResult.Outcome,
		Message:       utilityResult.Message,
		Artifacts:     make([]agentcontract.ArtifactOutput, 0, len(utilityResult.Artifacts)),
	}
	if utilityResult.Error != nil {
		converted.Error = &agentcontract.ResultError{Code: utilityResult.Error.Code, Message: utilityResult.Error.Message}
	}
	for _, artifact := range utilityResult.Artifacts {
		converted.Artifacts = append(converted.Artifacts, agentcontract.ArtifactOutput{
			Contract:  artifact.Contract,
			Path:      artifact.Path,
			MediaType: artifact.MediaType,
		})
	}
	if validateErr := converted.Validate(stagingPath); validateErr != nil {
		return agentcontract.Result{}, validateErr
	}
	return converted, nil
}

func ingestRuntimeAudit(ctx context.Context, path string) error {
	if path == "" {
		return nil
	}
	config := audit.ConfigFromEnv()
	if config.DSN == "" && !config.Required {
		log.Print("SOVEREIGN_AUDIT_DSN is unset; skipping runtime audit ingestion")
		return nil
	}
	recorder, closeAudit, auditMode, err := audit.OpenConfigured(ctx, config)
	if err != nil {
		return err
	}
	defer closeAudit()
	count, err := audit.IngestEventFile(ctx, recorder, path)
	if err != nil {
		return err
	}
	log.Printf("ingested %d runtime audit events using %s backend", count, auditMode)
	return nil
}

func artifactName(attempt, contract string, index int) string {
	name := strings.ToLower(strings.ReplaceAll(contract, "_", "-"))
	name = strings.ReplaceAll(name, "/", "-")
	if len(name) > 25 {
		name = name[:25]
	}
	return fmt.Sprintf("%s-%s-%02d", attempt, strings.Trim(name, "-"), index)
}
