package controllers

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"
)

type kustomizationFixture struct {
	Resources []string `json:"resources"`
}

func TestValidationRunProviderRequestUsesCalculatorOverlay(t *testing.T) {
	repositoryRoot := filepath.Clean(filepath.Join("..", ".."))
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
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&project, workflow).Build()
	reconciler := ValidationRunReconciler{Client: client}

	run := &v1alpha1.ValidationRun{}
	run.Name = "calculator-validation"
	run.Namespace = workflow.Namespace
	run.Spec.WorkflowRef = workflow.Name
	run.Spec.Commit = "candidate-commit"
	run.Spec.ImageDigest = "registry.example.test/calculator@sha256:abc"

	request, err := reconciler.providerRequest(context.Background(), run)
	if err != nil {
		t.Fatal(err)
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
