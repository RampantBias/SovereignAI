package validation

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestApplicationNamingIsIsolated(t *testing.T) {
	name := ApplicationName("wf", "preview", "uid")
	if len(name) > 63 || name != ApplicationName("wf", "preview", "uid") || name == ApplicationName("other", "preview", "uid") || name == ApplicationName("wf", "preview", "recreated") {
		t.Fatal("unstable or colliding Application identity")
	}
}

func TestApplicationLifecycleSafety(t *testing.T) {
	ctx := context.Background()
	kube := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	p := NewArgoKustomize(kube, "argocd")
	request := Request{Name: "preview", WorkflowNamespace: "wf"}
	ref, err := p.Start(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	app, err := kube.Resource(applicationGVR).Namespace("argocd").Get(ctx, ref, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(app.GetFinalizers(), argoResourcesFinalizer) {
		t.Fatal("Application lacks resource deletion finalizer")
	}
	if err := unstructured.SetNestedField(app.Object, "Missing", "status", "health", "status"); err != nil {
		t.Fatal(err)
	}
	if _, err := kube.Resource(applicationGVR).Namespace("argocd").Update(ctx, app, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	status, err := p.Status(ctx, ref)
	if err != nil || status.Failed || status.Ready {
		t.Fatalf("initial Missing must remain pending: %#v %v", status, err)
	}
	if _, err := p.Start(ctx, request); err != nil {
		t.Fatal("idempotent start", err)
	}
	request.WorkflowNamespace = "another-workflow"
	if _, err := p.Start(ctx, request); err == nil {
		t.Fatal("must not adopt another workflow Application")
	}
	app.SetLabels(nil)
	if _, err := kube.Resource(applicationGVR).Namespace("argocd").Update(ctx, app, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := p.Destroy(ctx, ref); err == nil {
		t.Fatal("must not delete unmanaged Application")
	}
}
