package artifacts_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllers"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTypedArtifactPassesCollectorStoreAndReconciliation(t *testing.T) {
	staging := t.TempDir()
	store := t.TempDir()
	content, err := json.Marshal(artifactcontract.ChangeRequest{
		Summary: "Add divide support", Description: "Add calculator division.",
		AcceptanceCriteria: []string{"84 / 2 returns 42"},
		RepositoryURL:      "https://git.example.test/calculator.git",
		SourceCommit:       strings.Repeat("a", 40),
	})
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(staging, "change-request.json")
	if err := os.WriteFile(source, content, 0o440); err != nil {
		t.Fatal(err)
	}
	workflowRef := v1alpha1.UIDReference{Name: "workflow", UID: "workflow-uid"}
	collected, err := artifacts.Collect(staging, store, workflowRef, v1alpha1.TypedLocalReference{
		APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentRun", Name: "fixture",
	}, strings.Repeat("a", 40), []agentcontract.ArtifactOutput{{
		Contract: artifactcontract.ChangeRequestContract, Path: source, MediaType: "application/json",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(collected) != 1 {
		t.Fatalf("collected %d artifacts, want 1", len(collected))
	}

	artifact := &v1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{Name: "change-request", Namespace: "workflow", Generation: 1},
		Spec:       collected[0].Spec,
	}
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.Artifact{}).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "workflow"}}, artifact,
	).Build()
	reconciler := &controllers.ArtifactReconciler{Client: kubeClient, Audit: audit.NewMemoryRecorder()}
	request := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: artifact.Namespace, Name: artifact.Name}}
	if _, err := reconciler.Reconcile(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var accepted v1alpha1.Artifact
	if err := kubeClient.Get(context.Background(), request.NamespacedName, &accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Status.Phase != v1alpha1.PhaseSucceeded {
		t.Fatalf("phase = %s, want Succeeded: %#v", accepted.Status.Phase, accepted.Status.Conditions)
	}
	stored, err := os.ReadFile(accepted.Spec.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(content) || accepted.Spec.Digest != artifactcontract.DigestBytes(stored) {
		t.Fatal("accepted Artifact does not preserve exact stored bytes and digest")
	}
}
