package validation

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestArgoKustomizeCreatesDigestPinnedApplication(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	provider := NewArgoKustomize(client, "argocd")
	reference, err := provider.Start(context.Background(), Request{
		Name: "validation-1", WorkflowNamespace: "workflow-1", Project: "project",
		InfrastructureRepo: "ssh://infra", InfrastructureRevision: "main", OverlayPath: "overlays/test",
		ImageName: "controller", ImageDigest: "registry/controller@sha256:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := client.Resource(applicationGVR).Namespace("argocd").Get(context.Background(), reference, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	images, found, err := unstructured.NestedStringSlice(application.Object, "spec", "source", "kustomize", "images")
	if err != nil || !found || len(images) != 1 || images[0] != "controller=registry/controller@sha256:abc" {
		t.Fatalf("unexpected image override: %#v found=%t err=%v", images, found, err)
	}
	if err := unstructured.SetNestedField(application.Object, map[string]any{"status": "Synced"}, "status", "sync"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedField(application.Object, map[string]any{"status": "Healthy"}, "status", "health"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Resource(applicationGVR).Namespace("argocd").Update(context.Background(), application, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, err := provider.Status(context.Background(), reference)
	if err != nil || !status.Ready {
		t.Fatalf("unexpected status: %#v err=%v", status, err)
	}
}
