package validationrun

import (
	"context"
	"fmt"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type cleanupProvider struct {
	gone      bool
	reference string
	statusErr error
}

func (p *cleanupProvider) Start(context.Context, validation.Request) (string, error) {
	return "", fmt.Errorf("unexpected Start")
}
func (p *cleanupProvider) Destroy(_ context.Context, reference string) error {
	p.reference = reference
	return nil
}
func (p *cleanupProvider) Status(context.Context, string) (validation.Status, error) {
	if p.gone {
		return validation.Status{}, apierrors.NewNotFound(schema.GroupResource{Group: "argoproj.io", Resource: "applications"}, p.reference)
	}
	return validation.Status{}, p.statusErr
}

func TestValidationDeletionWaitsForApplicationAbsence(t *testing.T) {
	for _, reference := range []string{"existing-provider-ref", ""} {
		t.Run("reference="+reference, func(t *testing.T) {
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			run := &v1alpha1.ValidationRun{ObjectMeta: metav1.ObjectMeta{Name: "preview", Namespace: "wf", UID: "run-uid", Finalizers: []string{ValidationFinalizer}}, Status: v1alpha1.ValidationRunStatus{ProviderRef: reference}}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(run).Build()
			if err := kube.Delete(ctx, run); err != nil {
				t.Fatal(err)
			}
			provider := &cleanupProvider{}
			r := ValidationRunReconciler{Client: kube, Provider: provider}
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
			result, err := r.Reconcile(ctx, req)
			if err != nil || result.RequeueAfter == 0 {
				t.Fatalf("must wait for absence: %#v %v", result, err)
			}
			if err := kube.Get(ctx, req.NamespacedName, run); err != nil {
				t.Fatal("finalizer removed before Argo completed", err)
			}
			want := reference
			if want == "" {
				want = validation.ApplicationName(run.Namespace, run.Name, string(run.UID))
			}
			if provider.reference != want {
				t.Fatalf("cleanup reference %s, want %s", provider.reference, want)
			}
			provider.statusErr = fmt.Errorf("transient API error")
			if _, err := r.Reconcile(ctx, req); err == nil {
				t.Fatal("API failure must not release finalizer")
			}
			provider.gone = true
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := kube.Get(ctx, req.NamespacedName, run); !apierrors.IsNotFound(err) {
				t.Fatalf("cleanup not completed: %v", err)
			}
		})
	}
}
