package controllers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestArtifactReconcilerAcceptsExactStoredTypedContent(t *testing.T) {
	content := validChangeRequestContent(t)
	artifact := artifactFixture(t, content, artifactcontract.DigestBytes(content), v1alpha1.ContractReference{
		Name: "change-request", Version: "v1",
	})
	reconciled := reconcileArtifactFixture(t, artifact)
	if reconciled.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("phase = %s, want Succeeded: %#v", reconciled.Status.Phase, reconciled.Status.Conditions)
	}
	if conditionReason(reconciled.Status.Conditions) != "ContractAccepted" {
		t.Fatalf("unexpected condition: %#v", reconciled.Status.Conditions)
	}
}

func TestArtifactReconcilerAcceptsCollectorAttestationWithoutReadingContent(t *testing.T) {
	content := validChangeRequestContent(t)
	artifact := artifactFixture(t, content, artifactcontract.DigestBytes(content), v1alpha1.ContractReference{
		Name: "change-request", Version: "v1",
	})
	artifact.Spec.Path = filepath.Join(t.TempDir(), "collector-owned", "artifact.json")

	reconciled := reconcileArtifactFixture(t, artifact)
	if reconciled.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("phase = %s, want Succeeded: %#v", reconciled.Status.Phase, reconciled.Status.Conditions)
	}
}

func TestArtifactReconcilerRejectsIncompleteMetadata(t *testing.T) {
	content := validChangeRequestContent(t)
	artifact := artifactFixture(t, content, artifactcontract.DigestBytes(content), v1alpha1.ContractReference{
		Name: "change-request", Version: "v1",
	})
	artifact.Spec.Path = ""

	reconciled := reconcileArtifactFixture(t, artifact)
	if reconciled.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %s, want Failed", reconciled.Status.Phase)
	}
	if reason := conditionReason(reconciled.Status.Conditions); reason != "InvalidMetadata" {
		t.Fatalf("condition reason = %q, want InvalidMetadata", reason)
	}
}

func TestArtifactReconcilerRejectsMissingRequiredClaims(t *testing.T) {
	content := validChangeRequestContent(t)
	artifact := artifactFixture(t, content, artifactcontract.DigestBytes(content), v1alpha1.ContractReference{
		Name: "candidate-revision", Version: "v1",
	})

	reconciled := reconcileArtifactFixture(t, artifact)
	if reconciled.Status.Phase != v1alpha1.PhaseFailed {
		t.Fatalf("phase = %s, want Failed", reconciled.Status.Phase)
	}
	if reason := conditionReason(reconciled.Status.Conditions); reason != "InvalidClaims" {
		t.Fatalf("condition reason = %q, want InvalidClaims", reason)
	}
}

func validChangeRequestContent(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(artifactcontract.ChangeRequest{
		Summary: "Add divide support", Description: "Implement calculator division.",
		AcceptanceCriteria: []string{"84 / 2 returns 42"},
		RepositoryURL:      "https://git.example.test/calculator.git",
		SourceCommit:       strings.Repeat("a", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func artifactFixture(t *testing.T, content []byte, digest string, contract v1alpha1.ContractReference) *v1alpha1.Artifact {
	t.Helper()
	path := filepath.Join(t.TempDir(), "artifact.json")
	if err := os.WriteFile(path, content, 0o440); err != nil {
		t.Fatal(err)
	}
	return &v1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: "artifact", Namespace: "workflow", Generation: 1},
		Spec: v1alpha1.ArtifactSpec{
			WorkflowRef: v1alpha1.UIDReference{Name: "wf"}, ProducerRef: v1alpha1.TypedLocalReference{Kind: "AgentRun", Name: "architect"},
			Contract: contract, Digest: digest, Path: path,
		},
	}
}

func reconcileArtifactFixture(t *testing.T, artifact *v1alpha1.Artifact) *v1alpha1.Artifact {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: artifact.Namespace}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.Artifact{}).WithObjects(namespace, artifact).Build()
	reconciler := &ArtifactReconciler{Client: kubeClient, Audit: audit.NewMemoryRecorder()}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: artifact.Namespace, Name: artifact.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var result v1alpha1.Artifact
	if err := kubeClient.Get(context.Background(), request.NamespacedName, &result); err != nil {
		t.Fatal(err)
	}
	return &result
}

func conditionReason(conditions []metav1.Condition) string {
	if len(conditions) == 0 {
		return ""
	}
	return conditions[len(conditions)-1].Reason
}
