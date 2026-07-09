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
	resultPath := flag.String("result", "", "agent result contract")
	stagingPath := flag.String("staging", "", "attempt staging directory")
	artifactPath := flag.String("artifact-store", "", "content-addressed artifact directory")
	sourceRevision := flag.String("source-revision", "", "source revision")
	flag.Parse()
	if *namespace == "" || *workflow == "" || *attempt == "" || *resultPath == "" || *stagingPath == "" || *artifactPath == "" {
		log.Fatal("namespace, workflow, attempt, result, staging, and artifact-store are required")
	}
	result, err := agentcontract.ReadResult(*resultPath, *stagingPath)
	if err != nil {
		log.Fatalf("validate result contract: %v", err)
	}
	collected, err := artifacts.Collect(*stagingPath, *artifactPath, *workflow, *attempt, *sourceRevision, result.Artifacts)
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

func artifactName(attempt, contract string, index int) string {
	name := strings.ToLower(strings.ReplaceAll(contract, "_", "-"))
	name = strings.ReplaceAll(name, "/", "-")
	if len(name) > 25 {
		name = name[:25]
	}
	return fmt.Sprintf("%s-%s-%02d", attempt, strings.Trim(name, "-"), index)
}
