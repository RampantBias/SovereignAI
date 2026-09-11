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
		Name: "validation", WorkflowNamespace: "workflow", Project: "project",
		InfrastructureRepo:     "https://github.com/RampantBias/calculator-demo.git",
		InfrastructureRevision: "478daad19967dd08f13de44b6c7c8becb17d8c1e",
		OverlayPath:            "deploy/overlays/validation",
		ImageSelector:          "calculator", ImageDigest: "registry.example.test/calculator@sha256:abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	application, err := client.Resource(applicationGVR).Namespace("argocd").Get(context.Background(), reference, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sourceFields := map[string]string{
		"repoURL":        "https://github.com/RampantBias/calculator-demo.git",
		"targetRevision": "478daad19967dd08f13de44b6c7c8becb17d8c1e",
		"path":           "deploy/overlays/validation",
	}
	for field, want := range sourceFields {
		got, found, err := unstructured.NestedString(application.Object, "spec", "source", field)
		if err != nil || !found || got != want {
			t.Fatalf("application source %s = %q, found=%t, err=%v; want %q", field, got, found, err, want)
		}
	}
	images, found, err := unstructured.NestedStringSlice(application.Object, "spec", "source", "kustomize", "images")
	if err != nil || !found || len(images) != 1 || images[0] != "calculator=registry.example.test/calculator@sha256:abc" {
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

func TestArgoKustomizeStatusSurfacesComparisonError(t *testing.T) {
	ctx := context.Background()
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	provider := NewArgoKustomize(client, "argocd")
	reference, err := provider.Start(ctx, Request{Name: "validation", WorkflowNamespace: "workflow"})
	if err != nil {
		t.Fatal(err)
	}
	application, err := client.Resource(applicationGVR).Namespace("argocd").Get(ctx, reference, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	application.Object["status"] = map[string]any{
		"sync":   map[string]any{"status": "Unknown"},
		"health": map[string]any{"status": "Healthy"},
		"conditions": []any{
			map[string]any{"type": "ComparisonError", "message": "repository authentication failed"},
		},
	}
	if _, err := client.Resource(applicationGVR).Namespace("argocd").Update(ctx, application, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, err := provider.Status(ctx, reference)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Failed || status.Ready || status.Phase != "ComparisonError" || status.Message != "repository authentication failed" {
		t.Fatalf("comparison error was not surfaced: %#v", status)
	}
}

func TestArgoStatusExposesObservedValidationIdentity(t *testing.T) {
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "preview", "namespace": "argocd", "uid": "app-uid"},
		"spec":     map[string]any{"destination": map[string]any{"namespace": "workflow"}},
		"status":   map[string]any{"sync": map[string]any{"status": "Synced", "revision": "observed-commit"}, "health": map[string]any{"status": "Healthy"}, "summary": map[string]any{"images": []any{"registry/app@sha256:observed"}}},
	}}
	kube := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), app)
	provider := NewArgoKustomize(kube, "argocd")
	status, err := provider.Status(context.Background(), "preview")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.ApplicationUID != "app-uid" || status.ApplicationName != "preview" || status.ApplicationNamespace != "argocd" || status.DestinationNamespace != "workflow" || status.ObservedRevision != "observed-commit" || len(status.ObservedImages) != 1 || status.ObservedImages[0] != "registry/app@sha256:observed" {
		t.Fatalf("lost observed identity: %#v", status)
	}
}

func TestArgoStatusUsesCurrentSuccessfulSyncImagesWhenSummaryIsEmpty(t *testing.T) {
	for _, scenario := range []string{"empty summary", "missing summary", "stale sync", "running sync", "failed sync", "wrong namespace", "unsynced resource", "wrong kind", "summary takes precedence"} {
		t.Run(scenario, func(t *testing.T) {
			resource := map[string]any{"group": "apps", "kind": "Deployment", "name": "calculator", "namespace": "workflow", "status": "Synced", "images": []any{"registry/app@sha256:current"}}
			operation := map[string]any{"phase": "Succeeded", "syncResult": map[string]any{"revision": "current-commit", "resources": []any{resource}}}
			status := map[string]any{"sync": map[string]any{"status": "Synced", "revision": "current-commit"}, "health": map[string]any{"status": "Healthy"}, "summary": map[string]any{}, "operationState": operation}
			switch scenario {
			case "missing summary":
				delete(status, "summary")
			case "stale sync":
				operation["syncResult"].(map[string]any)["revision"] = "old-commit"
			case "running sync":
				operation["phase"] = "Running"
			case "failed sync":
				operation["phase"] = "Failed"
			case "wrong namespace":
				resource["namespace"] = "other"
			case "unsynced resource":
				resource["status"] = "SyncFailed"
			case "wrong kind":
				resource["kind"] = "Job"
			case "summary takes precedence":
				status["summary"] = map[string]any{"images": []any{"registry/app@sha256:summary"}}
			}
			app := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "preview", "namespace": "argocd", "uid": "app-uid"}, "spec": map[string]any{"destination": map[string]any{"namespace": "workflow"}}, "status": status}}
			provider := NewArgoKustomize(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), app), "argocd")
			got, err := provider.Status(context.Background(), "preview")
			if err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "empty summary", "missing summary":
				if len(got.ObservedImages) != 1 || got.ObservedImages[0] != "registry/app@sha256:current" {
					t.Fatalf("lost current sync evidence: %#v", got)
				}
			case "summary takes precedence":
				if len(got.ObservedImages) != 1 || got.ObservedImages[0] != "registry/app@sha256:summary" {
					t.Fatalf("overrode summary evidence: %#v", got)
				}
			default:
				if len(got.ObservedImages) != 0 {
					t.Fatalf("used ineligible sync evidence: %#v", got)
				}
			}
		})
	}
}
