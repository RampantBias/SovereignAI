package controllers

import (
	"context"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/validation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const ValidationFinalizer = "sovereign-ai.io/validation-cleanup"

type ValidationRunReconciler struct {
	client.Client
	Provider validation.Provider
	Audit    audit.Recorder
	Now      func() time.Time
}

func (r *ValidationRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.ValidationRun{}).Complete(r)
}

func (r *ValidationRunReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var run v1alpha1.ValidationRun
	if err := r.Get(ctx, request.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&run, ValidationFinalizer) {
			if run.Status.ProviderRef != "" {
				if err := r.Provider.Destroy(ctx, run.Status.ProviderRef); err != nil && !apierrors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
			}
			controllerutil.RemoveFinalizer(&run, ValidationFinalizer)
			return ctrl.Result{}, r.Update(ctx, &run)
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&run, ValidationFinalizer) {
		controllerutil.AddFinalizer(&run, ValidationFinalizer)
		return ctrl.Result{}, r.Update(ctx, &run)
	}
	if run.Status.ProviderRef == "" {
		request, err := r.providerRequest(ctx, &run)
		if err != nil {
			return ctrl.Result{}, err
		}
		reference, err := r.Provider.Start(ctx, request)
		if err != nil {
			return ctrl.Result{}, err
		}
		run.Status.ProviderRef = reference
		run.Status.Phase = v1alpha1.PhaseValidating
		run.Status.ObservedGeneration = run.Generation
		if err := r.Status().Update(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.appendValidationEvent(ctx, &run, "ValidationRunCreated", "create", "created", "")
	}
	status, err := r.Provider.Status(ctx, run.Status.ProviderRef)
	if err != nil {
		return ctrl.Result{}, err
	}
	run.Status.AccessURL = status.AccessURL
	if status.Ready {
		run.Status.Phase = v1alpha1.PhaseSucceeded
	} else if status.Failed {
		run.Status.Phase = v1alpha1.PhaseFailed
	} else {
		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}
	if err := r.Status().Update(ctx, &run); err != nil {
		return ctrl.Result{}, err
	}
	eventType := "ValidationRunReady"
	outcome := "ready"
	if run.Status.Phase == v1alpha1.PhaseFailed {
		eventType = "ValidationRunFailed"
		outcome = "failed"
	}
	return ctrl.Result{}, r.appendValidationEvent(ctx, &run, eventType, "observe", outcome, string(run.Status.Phase))
}

func (r *ValidationRunReconciler) appendValidationEvent(ctx context.Context, run *v1alpha1.ValidationRun, eventType, action, outcome, reason string) error {
	return appendControllerEvent(ctx, r.Audit, "validationrun-controller", r.Now, audit.EventOptions{
		Type: eventType,
		Subject: audit.Subject{
			Namespace: run.Namespace,
			Workflow:  run.Spec.WorkflowRef,
		},
		Action:  action,
		Target:  run.Name,
		Outcome: outcome,
		Reason:  reason,
		References: map[string]string{
			"validationRun": run.Name,
			"providerRef":   run.Status.ProviderRef,
			"validationURL": run.Status.AccessURL,
			"commit":        run.Spec.Commit,
			"imageDigest":   run.Spec.ImageDigest,
		},
		Data: map[string]any{
			"provider":    run.Spec.Provider,
			"overlayPath": run.Spec.OverlayPath,
			"destination": run.Spec.Destination,
		},
	})
}

func (r *ValidationRunReconciler) providerRequest(ctx context.Context, run *v1alpha1.ValidationRun) (validation.Request, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef}, &workflow); err != nil {
		return validation.Request{}, err
	}
	var project v1alpha1.SovereignProject
	if err := r.Get(ctx, types.NamespacedName{Name: workflow.Spec.ProjectName}, &project); err != nil {
		return validation.Request{}, err
	}
	return validation.Request{
		Name: run.Name, WorkflowNamespace: run.Namespace, Project: project.Name,
		InfrastructureRepo: project.Spec.Validation.InfrastructureRepo, InfrastructureRevision: "main",
		OverlayPath: project.Spec.Validation.OverlayPath, ImageName: project.Spec.Validation.ImageName,
		ImageDigest: run.Spec.ImageDigest, Commit: run.Spec.Commit,
	}, nil
}
