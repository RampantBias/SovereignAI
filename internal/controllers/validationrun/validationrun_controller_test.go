package validationrun

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

type kustomizationFixture struct {
	Resources []string `json:"resources"`
}

func TestValidationRunProviderRequestUsesCalculatorOverlay(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	projectPath := filepath.Join(repositoryRoot, "resources", "ex-project.yaml")

	var project v1alpha1.SovereignProject
	readYAMLFixture(t, projectPath, &project)

	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "workflow"},
		Spec: v1alpha1.SovereignWorkflowSpec{
			Project: v1alpha1.UIDReference{Name: project.Name},
		},
	}

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("c", 40)
	tree := strings.Repeat("d", 40)
	candidateDigest := "sha256:" + strings.Repeat("a", 64)
	ociDigest := "sha256:" + strings.Repeat("e", 64)
	artifacts := validationArtifactFixtures(workflow, &project, commit, tree, candidateDigest, ociDigest)
	objects := []runtime.Object{&project, workflow}
	for index := range artifacts {
		objects = append(objects, &artifacts[index])
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	reconciler := ValidationRunReconciler{Client: client}

	run := &v1alpha1.ValidationRun{}
	run.Name = "calculator-validation"
	run.Namespace = workflow.Namespace
	run.Spec.WorkflowRef = v1alpha1.UIDReference{Name: workflow.Name}
	run.Spec.Commit = commit
	run.Spec.ImageDigest = project.Spec.Validation.ImageName + "@" + ociDigest
	for index := range artifacts {
		run.Spec.Inputs = append(run.Spec.Inputs, v1alpha1.ArtifactReference{
			Name:   artifacts[index].Spec.Contract.Name,
			Digest: artifacts[index].Spec.Digest,
			ArtifactRef: &v1alpha1.UIDReference{
				Name: artifacts[index].Name,
				UID:  artifacts[index].UID,
			},
		})
	}

	request, resolution, message, err := reconciler.providerRequest(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if resolution != "ready" || message != "" {
		t.Fatalf("expected ready inputs, got %q: %s", resolution, message)
	}
	if request.InfrastructureRepo != project.Spec.Validation.InfrastructureRepo {
		t.Fatalf("infrastructure repository = %q, want %q", request.InfrastructureRepo, project.Spec.Validation.InfrastructureRepo)
	}
	if request.OverlayPath != project.Spec.Validation.OverlayPath {
		t.Fatalf("overlay path = %q, want %q", request.OverlayPath, project.Spec.Validation.OverlayPath)
	}
	if request.ImageName != project.Spec.Validation.ImageName {
		t.Fatalf("image name = %q, want %q", request.ImageName, project.Spec.Validation.ImageName)
	}
	if request.InfrastructureRevision != commit || request.Commit != commit {
		t.Fatalf("provider commit = %q, infrastructure revision = %q; want %q", request.Commit, request.InfrastructureRevision, commit)
	}
	if request.ImageDigest != project.Spec.Validation.ImageName+"@"+ociDigest {
		t.Fatalf("provider image = %q, want digest-pinned image", request.ImageDigest)
	}

	overlayDirectory := filepath.Join(repositoryRoot, "demo", "calculator", filepath.FromSlash(request.OverlayPath))
	var overlay kustomizationFixture
	readYAMLFixture(t, filepath.Join(overlayDirectory, "kustomization.yaml"), &overlay)
	if !slices.Contains(overlay.Resources, "../../base") {
		t.Fatalf("calculator validation overlay resources = %#v, want ../../base", overlay.Resources)
	}

	baseDirectory := filepath.Clean(filepath.Join(overlayDirectory, "..", "..", "base"))
	var base kustomizationFixture
	readYAMLFixture(t, filepath.Join(baseDirectory, "kustomization.yaml"), &base)
	if !slices.Contains(base.Resources, "deployment.yaml") {
		t.Fatalf("calculator base resources = %#v, want deployment.yaml", base.Resources)
	}

	var deployment appsv1.Deployment
	readYAMLFixture(t, filepath.Join(baseDirectory, "deployment.yaml"), &deployment)
	if len(deployment.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("calculator container count = %d, want 1", len(deployment.Spec.Template.Spec.Containers))
	}
	seedImageName, _, _ := strings.Cut(deployment.Spec.Template.Spec.Containers[0].Image, ":")
	if seedImageName != request.ImageName {
		t.Fatalf("calculator deployment image name = %q, provider override name = %q", seedImageName, request.ImageName)
	}
}

func TestValidationRunProviderRequestRejectsBrokenArtifactLineage(t *testing.T) {
	project := &v1alpha1.SovereignProject{
		ObjectMeta: metav1.ObjectMeta{Name: "project"},
		Spec: v1alpha1.SovereignProjectSpec{
			ApplicationRepository: v1alpha1.RepositorySpec{URL: "https://git.example.test/calculator.git"},
			Validation: v1alpha1.ProjectValidationSpec{
				InfrastructureRepo: "https://git.example.test/calculator.git",
				ImageName:          "registry.example.test/sovereign/calculator",
			},
		},
	}
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "workflow", UID: "workflow-uid"},
		Spec: v1alpha1.SovereignWorkflowSpec{
			Project: v1alpha1.UIDReference{Name: project.Name},
		},
	}
	commit := strings.Repeat("c", 40)
	artifacts := validationArtifactFixtures(
		workflow,
		project,
		commit,
		strings.Repeat("d", 40),
		"sha256:"+strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("e", 64),
	)
	artifacts[2].Spec.Claims.ImageDigest.CandidateCommit = strings.Repeat("f", 40)

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []runtime.Object{project, workflow}
	for index := range artifacts {
		objects = append(objects, &artifacts[index])
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	run := &v1alpha1.ValidationRun{
		ObjectMeta: metav1.ObjectMeta{Name: "validation", Namespace: workflow.Namespace},
		Spec:       v1alpha1.ValidationRunSpec{WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}},
	}
	for index := range artifacts {
		run.Spec.Inputs = append(run.Spec.Inputs, v1alpha1.ArtifactReference{
			Name: artifacts[index].Spec.Contract.Name, Digest: artifacts[index].Spec.Digest,
			ArtifactRef: &v1alpha1.UIDReference{Name: artifacts[index].Name, UID: artifacts[index].UID},
		})
	}

	_, resolution, message, err := (&ValidationRunReconciler{Client: kubeClient}).providerRequest(context.Background(), run)
	if err != nil || resolution != "invalid" || !strings.Contains(message, "candidate commit or tree") {
		t.Fatalf("expected invalid image lineage, got %q: %s (err=%v)", resolution, message, err)
	}
}

func TestValidationRunProviderRequestWaitsForUnacceptedArtifact(t *testing.T) {
	project := &v1alpha1.SovereignProject{
		ObjectMeta: metav1.ObjectMeta{Name: "project"},
		Spec: v1alpha1.SovereignProjectSpec{
			ApplicationRepository: v1alpha1.RepositorySpec{URL: "https://git.example.test/calculator.git"},
			Validation: v1alpha1.ProjectValidationSpec{
				InfrastructureRepo: "https://git.example.test/calculator.git",
				ImageName:          "registry.example.test/sovereign/calculator",
			},
		},
	}
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "workflow", UID: "workflow-uid"},
		Spec: v1alpha1.SovereignWorkflowSpec{
			Project: v1alpha1.UIDReference{Name: project.Name},
		},
	}
	artifacts := validationArtifactFixtures(
		workflow,
		project,
		strings.Repeat("c", 40),
		strings.Repeat("d", 40),
		"sha256:"+strings.Repeat("a", 64),
		"sha256:"+strings.Repeat("e", 64),
	)
	artifacts[0].Status = v1alpha1.ArtifactStatus{}

	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []runtime.Object{project, workflow}
	for index := range artifacts {
		objects = append(objects, &artifacts[index])
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
	run := &v1alpha1.ValidationRun{
		ObjectMeta: metav1.ObjectMeta{Name: "validation", Namespace: workflow.Namespace},
		Spec:       v1alpha1.ValidationRunSpec{WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}},
	}
	for index := range artifacts {
		run.Spec.Inputs = append(run.Spec.Inputs, v1alpha1.ArtifactReference{
			Name: artifacts[index].Spec.Contract.Name, Digest: artifacts[index].Spec.Digest,
			ArtifactRef: &v1alpha1.UIDReference{Name: artifacts[index].Name, UID: artifacts[index].UID},
		})
	}

	_, resolution, message, err := (&ValidationRunReconciler{Client: kubeClient}).providerRequest(context.Background(), run)
	if err != nil || resolution != "pending" || !strings.Contains(message, "not accepted yet") {
		t.Fatalf("expected pending artifact, got %q: %s (err=%v)", resolution, message, err)
	}
}

func TestValidationRunReconcileInputResolution(t *testing.T) {
	for _, outcome := range []string{"pending", "invalid", "operational error"} {
		t.Run(outcome, func(t *testing.T) {
			project := &v1alpha1.SovereignProject{
				ObjectMeta: metav1.ObjectMeta{Name: "project"},
				Spec: v1alpha1.SovereignProjectSpec{
					ApplicationRepository: v1alpha1.RepositorySpec{URL: "https://git.example.test/calculator.git"},
					Validation: v1alpha1.ProjectValidationSpec{
						InfrastructureRepo: "https://git.example.test/calculator.git",
						ImageName:          "registry.example.test/sovereign/calculator",
					},
				},
			}
			workflow := &v1alpha1.SovereignWorkflow{
				ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "workflow", UID: "workflow-uid"},
				Spec:       v1alpha1.SovereignWorkflowSpec{Project: v1alpha1.UIDReference{Name: project.Name}},
			}
			workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}
			attempt := &v1alpha1.StepAttempt{
				ObjectMeta: metav1.ObjectMeta{Name: "validation", Namespace: workflow.Namespace, UID: "attempt-uid"},
				Spec: v1alpha1.StepAttemptSpec{
					WorkflowRef: workflowRef, StepName: "validation", RetryNumber: 1, Kind: v1alpha1.ExecutionKindValidation,
				},
				Status: v1alpha1.StepAttemptStatus{ExecutionRef: &v1alpha1.TypedLocalReference{
					APIVersion: v1alpha1.GroupVersion.String(), Kind: "ValidationRun", Name: "validation",
				}},
			}
			run := &v1alpha1.ValidationRun{
				ObjectMeta: metav1.ObjectMeta{
					Name: attempt.Name, Namespace: workflow.Namespace, Generation: 1,
					Finalizers:      []string{ValidationFinalizer},
					OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(attempt, v1alpha1.GroupVersion.WithKind("StepAttempt"))},
				},
				Spec: v1alpha1.ValidationRunSpec{AttemptRef: attempt.Name, WorkflowRef: workflowRef, StepName: "validation", Attempt: 1},
			}
			artifacts := validationArtifactFixtures(workflow, project, strings.Repeat("c", 40), strings.Repeat("d", 40), "sha256:"+strings.Repeat("a", 64), "sha256:"+strings.Repeat("e", 64))
			switch outcome {
			case "pending":
				artifacts[0].Status = v1alpha1.ArtifactStatus{}
			case "invalid":
				artifacts[2].Spec.Claims.ImageDigest.CandidateCommit = strings.Repeat("f", 40)
			}
			objects := []client.Object{project, attempt, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: workflow.Namespace}}}
			if outcome != "operational error" {
				objects = append(objects, workflow)
			}
			for index := range artifacts {
				artifact := &artifacts[index]
				objects = append(objects, artifact)
				run.Spec.Inputs = append(run.Spec.Inputs, v1alpha1.ArtifactReference{
					Name: artifact.Spec.Contract.Name, Digest: artifact.Spec.Digest,
					ArtifactRef: &v1alpha1.UIDReference{Name: artifact.Name, UID: artifact.UID},
				})
			}
			objects = append(objects, run)
			scheme := runtime.NewScheme()
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			if err := corev1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.ValidationRun{}).WithObjects(objects...).Build()
			// No provider is supplied: none of these outcomes may start a deployment.
			reconciler := &ValidationRunReconciler{Client: kubeClient}
			key := client.ObjectKeyFromObject(run)
			result, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key})
			if outcome == "operational error" {
				if !apierrors.IsNotFound(err) {
					t.Fatalf("expected Kubernetes read error, got %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if (result.RequeueAfter > 0) != (outcome == "pending") {
				t.Fatalf("unexpected requeue for %s: %#v", outcome, result)
			}
			var updated v1alpha1.ValidationRun
			if err := kubeClient.Get(context.Background(), key, &updated); err != nil {
				t.Fatal(err)
			}
			if outcome == "invalid" {
				if updated.Status.Phase != v1alpha1.PhaseFailed || updated.Status.Retryable || updated.Status.FailureReason != "InvalidValidationInputs" {
					t.Fatalf("invalid evidence must fail non-retryably: %#v", updated.Status)
				}
			} else if updated.Status.Phase == v1alpha1.PhaseFailed {
				t.Fatalf("%s must not fail validation: %#v", outcome, updated.Status)
			}
		})
	}
}

func validationArtifactFixtures(
	workflow *v1alpha1.SovereignWorkflow,
	project *v1alpha1.SovereignProject,
	commit, tree, candidateDigest, ociDigest string,
) []v1alpha1.Artifact {
	workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}
	validStatus := v1alpha1.ArtifactStatus{
		ObservedGeneration: 1,
		Phase:              v1alpha1.PhaseSucceeded,
		Conditions: []metav1.Condition{{
			Type: "Valid", Status: metav1.ConditionTrue, Reason: "ContractAccepted", ObservedGeneration: 1,
		}},
	}
	return []v1alpha1.Artifact{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "candidate-revision", Namespace: workflow.Namespace, UID: "candidate-uid", Generation: 1},
			Spec: v1alpha1.ArtifactSpec{
				WorkflowRef: workflowRef, Contract: v1alpha1.ContractReference{Name: "candidate-revision", Version: "v1"},
				Digest: candidateDigest, Path: "/artifacts/candidate",
				Claims: &v1alpha1.ArtifactClaims{CandidateRevision: &v1alpha1.CandidateRevisionClaims{
					RepositoryURL: project.Spec.ApplicationRepository.URL, Branch: "sovereign/workflow",
					Commit: commit, Tree: tree,
				}},
			},
			Status: validStatus,
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "candidate-remote-proof", Namespace: workflow.Namespace, UID: "remote-uid", Generation: 1},
			Spec: v1alpha1.ArtifactSpec{
				WorkflowRef: workflowRef, Contract: v1alpha1.ContractReference{Name: "candidate-remote-proof", Version: "v1"},
				Digest: "sha256:" + strings.Repeat("b", 64), Path: "/artifacts/remote-proof",
				Claims: &v1alpha1.ArtifactClaims{CandidateRemoteProof: &v1alpha1.CandidateRemoteProofClaims{
					CandidateRevisionDigest: candidateDigest, RepositoryURL: project.Spec.ApplicationRepository.URL,
					Ref: "refs/heads/sovereign/workflow", ObservedCommit: commit, VerifiedAt: "2026-08-29T12:00:00Z",
				}},
			},
			Status: validStatus,
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "image-digest", Namespace: workflow.Namespace, UID: "image-uid", Generation: 1},
			Spec: v1alpha1.ArtifactSpec{
				WorkflowRef: workflowRef, Contract: v1alpha1.ContractReference{Name: "image-digest", Version: "v1"},
				Digest: "sha256:" + strings.Repeat("f", 64), Path: "/artifacts/image",
				Claims: &v1alpha1.ArtifactClaims{ImageDigest: &v1alpha1.ImageDigestClaims{
					CandidateRevisionDigest: candidateDigest, ImageRepository: project.Spec.Validation.ImageName,
					OCIDigest: ociDigest, CandidateCommit: commit, CandidateTree: tree,
				}},
			},
			Status: validStatus,
		},
	}
}

func readYAMLFixture(t *testing.T, path string, target any) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := yaml.Unmarshal(content, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}
