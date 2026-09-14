package agentrun

import (
	"context"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/domain/state"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *AgentRunReconciler) reconcileInferenceLeaseRelease(ctx context.Context, run *v1alpha1.AgentRun) (bool, error) {
	if run.Status.InferenceLeaseRef == "" {
		return true, nil
	}
	key := types.NamespacedName{Namespace: run.Namespace, Name: run.Status.InferenceLeaseRef}
	var lease v1alpha1.InferenceLease
	if err := r.Get(ctx, key, &lease); err != nil {
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		return false, err
	}
	if state.IsTerminal(lease.Status.Phase) {
		return true, nil
	}
	reason := "AgentRun" + string(run.Status.Phase)
	if !run.DeletionTimestamp.IsZero() {
		reason = "AgentRunDeleted"
	}
	requested := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.InferenceLease
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		if latest.Annotations != nil && latest.Annotations[controllermeta.InferenceLeaseReleaseRequestAnnotation] != "" {
			return nil
		}
		annotations := make(map[string]string, len(latest.Annotations)+1)
		for name, value := range latest.Annotations {
			annotations[name] = value
		}
		annotations[controllermeta.InferenceLeaseReleaseRequestAnnotation] = reason
		latest.Annotations = annotations
		if err := r.Update(ctx, &latest); err != nil {
			return err
		}
		requested = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if requested {
		if err := r.appendEvent(ctx, run, "InferenceLeaseReleaseRequested", "release", lease.Name, "requested", reason); err != nil {
			return false, err
		}
	}
	return false, nil
}

func (r *AgentRunReconciler) ensureLease(ctx context.Context, run *v1alpha1.AgentRun) (*v1alpha1.InferenceLease, error) {
	name := run.Name + "-inference"
	var lease v1alpha1.InferenceLease
	err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: name}, &lease)
	if err == nil {
		if run.Status.InferenceLeaseRef != name {
			run.Status.InferenceLeaseRef = name
			if err := r.updateStatusFields(ctx, run); err != nil {
				return nil, err
			}
		}
		return &lease, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return nil, err
	}
	workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.ObjectMeta.UID}
	lease = v1alpha1.InferenceLease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: run.Namespace}, Spec: v1alpha1.InferenceLeaseSpec{
		WorkflowRef: workflowRef, AttemptRef: run.Spec.AttemptRef, ProjectRef: workflow.Spec.Project.Name,
		Tenant: workflow.Spec.Project.Name, Classification: workflow.Spec.Classification,
		SharingScope: run.Spec.Inference.SharingScope, Model: run.Spec.Inference.Model,
		ModelRevision: run.Spec.Inference.ModelRevision, EstimatedKVRAMMiB: run.Spec.Inference.EstimatedKVRAMMiB,
		Priority: run.Spec.Inference.Priority, Evictable: run.Spec.Inference.Evictable,
	}}
	if err := controllerutil.SetControllerReference(run, &lease, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, &lease); err != nil {
		return nil, err
	}
	run.Status.InferenceLeaseRef = name
	if err := r.updateStatusFields(ctx, run); err != nil {
		return nil, err
	}
	if err := r.appendEvent(ctx, run, "InferenceLeaseRequested", "request", name, "requested", ""); err != nil {
		return nil, err
	}
	return &lease, nil
}
