package controllers

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/inference"
	admission "github.com/SovereignAI/internal/inference"
	policyengine "github.com/SovereignAI/internal/policy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const InferenceNamespace = "sovereign-inference"

type InferenceLeaseReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	DefaultStaticVRAMMiB int64
	DefaultMaxKVRAMMiB   int64
	SafetyHeadroomMiB    int64
	Profile              inference.Profile
	Policy               policyengine.Evaluator
	Audit                audit.Recorder
	Now                  func() time.Time
}

func (r *InferenceLeaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.InferenceLease{}).Complete(r)
}

// Responsible for admission, policy eval, capacity selection, binding, interruption, and lifecycle
func (r *InferenceLeaseReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	// Find lease
	var lease v1alpha1.InferenceLease
	if err := r.Get(ctx, request.NamespacedName, &lease); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if lease.Status.Phase == v1alpha1.PhaseRunning || lease.Status.Phase == v1alpha1.PhaseFailed {
		return ctrl.Result{}, nil
	}

	// Transition lease to pending
	if lease.Status.Phase == "" {
		lease.Status.Phase = v1alpha1.PhasePending
		lease.Status.ObservedGeneration = lease.Generation
		return ctrl.Result{}, r.Status().Update(ctx, &lease)
	}

	// Verify namespace for inference exists
	if err := r.ensureInferenceNamespace(ctx); err != nil {
		return ctrl.Result{}, err
	}

	// Get current inference endpoint & lease snapshots for decision making
	snapshots, err := r.snapshots(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Filter snapshots compatible with current policy & lease requirements
	filtered, policyDecision, err := r.admitSnapshots(ctx, lease, snapshots)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Admission/capacity decision
	decision := admission.Select(lease, filtered, true)

	// Handle decision action
	switch decision.Action {
	case admission.ActionReuse:
		return ctrl.Result{}, r.bind(ctx, &lease, decision.EndpointRef, policyDecision, decision.Reason)
	case admission.ActionEvict:
		for _, victim := range decision.EvictLeases {
			if err := r.interruptLease(ctx, victim, "EvictedByHigherPriorityLease"); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, r.bind(ctx, &lease, decision.EndpointRef, policyDecision, decision.Reason)
	case admission.ActionCreate:
		return r.handleActionCreate(ctx, &lease)
	default:
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
	}
}

func (r *InferenceLeaseReconciler) admitSnapshots(ctx context.Context, lease v1alpha1.InferenceLease, snapshots []admission.EndpointSnapshot) ([]admission.EndpointSnapshot, string, error) {
	if r.Policy == nil {
		return snapshots, "development-no-policy", nil
	}
	filtered := make([]admission.EndpointSnapshot, 0, len(snapshots))
	lastDecision := ""
	for _, snapshot := range snapshots {
		decision, err := r.Policy.Evaluate(ctx, map[string]any{
			"operation": "inference.share",
			"request":   inferencePolicyRequest(lease),
			"endpoint": map[string]any{
				"model": snapshot.Endpoint.Spec.Model, "modelRevision": snapshot.Endpoint.Spec.ModelRevision,
				"tenant": snapshot.Endpoint.Spec.Tenant, "classification": snapshot.Endpoint.Spec.Classification,
			},
		})
		if err != nil {
			return nil, "", err
		}
		lastDecision = decision.ID
		if decision.Allowed {
			filtered = append(filtered, snapshot)
		}
	}
	return filtered, lastDecision, nil
}

// Should really clean out ctrl.Result from this
func (r *InferenceLeaseReconciler) handleActionCreate(ctx context.Context, lease *v1alpha1.InferenceLease) (ctrl.Result, error) {
	// Evaluate whether we can create the inference based on policy established
	createDecision, err := r.evaluateCreate(ctx, lease)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Can't create inference, must report rejection event and fail lease
	if !createDecision.Allowed {
		lease.Status.Phase = v1alpha1.PhaseFailed
		lease.Status.DecisionID = createDecision.ID
		lease.Status.Reason = fmt.Sprint(createDecision.Reasons)
		if err := r.Status().Update(ctx, lease); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.appendLeaseEvent(
			ctx,
			lease,
			"InferenceLeaseRejected",
			"admit",
			lease.Name,
			"rejected",
			lease.Status.Reason,
			nil)
	}

	endpoint, err := r.ensureEndpoint(ctx, lease)
	if err != nil {
		return ctrl.Result{}, err
	}
	if endpoint.Status.Phase == v1alpha1.PhaseRunning {
		return ctrl.Result{}, r.bind(
			ctx,
			lease,
			v1alpha1.NamespacedReference{Namespace: endpoint.Namespace, Name: endpoint.Name},
			createDecision.ID,
			"new endpoint is ready")
	}
	return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
}

func (r *InferenceLeaseReconciler) evaluateCreate(ctx context.Context, lease *v1alpha1.InferenceLease) (policyengine.Decision, error) {
	if r.Policy == nil {
		return policyengine.Decision{ID: "development-no-policy", Allowed: true}, nil
	}
	return r.Policy.Evaluate(ctx, map[string]any{"operation": "inference.create", "request": inferencePolicyRequest(*lease)})
}

func inferencePolicyRequest(lease v1alpha1.InferenceLease) map[string]any {
	return map[string]any{
		"model": lease.Spec.Model, "modelRevision": lease.Spec.ModelRevision,
		"tenant": lease.Spec.Tenant, "classification": lease.Spec.Classification,
		"sharingScope": lease.Spec.SharingScope, "priority": lease.Spec.Priority,
	}
}

func (r *InferenceLeaseReconciler) ensureInferenceNamespace(ctx context.Context) error {
	var namespace corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: InferenceNamespace}, &namespace)
	if err == nil {
		return nil
	}
	if !apierrors.IsNotFound(err) {
		return err
	}
	namespace = corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: InferenceNamespace, Labels: map[string]string{"app.kubernetes.io/managed-by": "sovereign-orchestrator", "istio-injection": "enabled"}}}
	return client.IgnoreAlreadyExists(r.Create(ctx, &namespace))
}

func (r *InferenceLeaseReconciler) snapshots(ctx context.Context) ([]admission.EndpointSnapshot, error) {
	var endpoints v1alpha1.InferenceEndpointList
	if err := r.List(ctx, &endpoints, client.InNamespace(InferenceNamespace)); err != nil {
		return nil, err
	}
	var leases v1alpha1.InferenceLeaseList
	if err := r.List(ctx, &leases); err != nil {
		return nil, err
	}
	result := make([]admission.EndpointSnapshot, 0, len(endpoints.Items))
	for _, endpoint := range endpoints.Items {
		snapshot := admission.EndpointSnapshot{Endpoint: endpoint}
		for _, lease := range leases.Items {
			if lease.Status.EndpointRef.Namespace == endpoint.Namespace && lease.Status.EndpointRef.Name == endpoint.Name {
				snapshot.Leases = append(snapshot.Leases, lease)
			}
		}
		result = append(result, snapshot)
	}
	return result, nil
}

func (r *InferenceLeaseReconciler) ensureEndpoint(ctx context.Context, lease *v1alpha1.InferenceLease) (*v1alpha1.InferenceEndpoint, error) {
	name := endpointName(lease.Spec.Model, lease.Spec.ModelRevision, lease.Spec.Tenant, lease.Spec.Classification)
	var endpoint v1alpha1.InferenceEndpoint
	err := r.Get(ctx, types.NamespacedName{Namespace: InferenceNamespace, Name: name}, &endpoint)
	if err == nil {
		return &endpoint, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	staticVRAM := r.DefaultStaticVRAMMiB
	if staticVRAM == 0 {
		staticVRAM = 4096
	}
	maxKV := r.DefaultMaxKVRAMMiB
	if maxKV == 0 {
		maxKV = 8192
	}
	endpoint = v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: InferenceNamespace, Labels: map[string]string{
			"sovereign-ai.io/project": lease.Spec.ProjectRef, "sovereign-ai.io/tenant": lease.Spec.Tenant,
			"sovereign-ai.io/classification": lease.Spec.Classification,
		}},
		Spec: v1alpha1.InferenceEndpointSpec{
			Provider:          "vllm",
			Model:             lease.Spec.Model,
			ModelRevision:     lease.Spec.ModelRevision,
			RuntimeImage:      r.Profile.RuntimeImage,
			Tenant:            lease.Spec.Tenant,
			Classification:    lease.Spec.Classification,
			SharingScope:      lease.Spec.SharingScope,
			StaticVRAMMiB:     staticVRAM,
			MaxKVRAMMiB:       maxKV,
			SafetyHeadroomMiB: r.SafetyHeadroomMiB,
		},
	}
	if err := r.Create(ctx, &endpoint); err != nil {
		return nil, err
	}
	if err := r.appendLeaseEvent(ctx, lease, "InferenceEndpointCreated", "create", endpoint.Name, "created", "", map[string]string{"endpoint": endpoint.Name}); err != nil {
		return nil, err
	}
	return &endpoint, nil
}

func (r *InferenceLeaseReconciler) bind(ctx context.Context, lease *v1alpha1.InferenceLease, endpointRef v1alpha1.NamespacedReference, decisionID, reason string) error {
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var endpoint v1alpha1.InferenceEndpoint
		if err := r.Get(ctx, types.NamespacedName{Namespace: endpointRef.Namespace, Name: endpointRef.Name}, &endpoint); err != nil {
			return err
		}
		leaseRef := v1alpha1.NamespacedReference{Namespace: lease.Namespace, Name: lease.Name}
		if !slices.Contains(endpoint.Status.ActiveLeases, leaseRef) {
			if endpoint.Status.AllocatedKVRAMMiB+lease.Spec.EstimatedKVRAMMiB+endpoint.Spec.SafetyHeadroomMiB > endpoint.Spec.MaxKVRAMMiB {
				return fmt.Errorf("endpoint capacity changed before lease binding")
			}
			endpoint.Status.ActiveLeases = append(endpoint.Status.ActiveLeases, leaseRef)
			endpoint.Status.ActiveLeaseCount = int32(len(endpoint.Status.ActiveLeases))
			endpoint.Status.AllocatedKVRAMMiB += lease.Spec.EstimatedKVRAMMiB
			now := metav1.Now()
			endpoint.Status.LastUsedAt = &now
			if err := r.Status().Update(ctx, &endpoint); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	lease.Status.Phase = v1alpha1.PhaseRunning
	lease.Status.EndpointRef = endpointRef
	lease.Status.EndpointURL = fmt.Sprintf("http://%s.%s.svc:8000", endpointRef.Name, endpointRef.Namespace)
	lease.Status.DecisionID = decisionID
	lease.Status.Reason = reason
	lease.Status.ObservedGeneration = lease.Generation
	if err := r.Status().Update(ctx, lease); err != nil {
		return err
	}
	references := map[string]string{
		"endpoint":    endpointRef.Name,
		"endpointURL": lease.Status.EndpointURL,
	}
	if decisionID != "" {
		references["policyDecision"] = decisionID
	}
	if err := r.appendLeaseEvent(ctx, lease, "InferenceLeaseAdmitted", "admit", lease.Name, "admitted", reason, references); err != nil {
		return err
	}
	return r.appendLeaseEvent(ctx, lease, "InferenceLeaseBound", "bind", lease.Name, "bound", reason, references)
}

func (r *InferenceLeaseReconciler) interruptLease(ctx context.Context, ref v1alpha1.NamespacedReference, reason string) error {
	var lease v1alpha1.InferenceLease
	if err := r.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &lease); err != nil {
		return client.IgnoreNotFound(err)
	}
	if lease.Status.EndpointRef.Name != "" {
		if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			var endpoint v1alpha1.InferenceEndpoint
			key := types.NamespacedName{Namespace: lease.Status.EndpointRef.Namespace, Name: lease.Status.EndpointRef.Name}
			if err := r.Get(ctx, key, &endpoint); err != nil {
				return client.IgnoreNotFound(err)
			}
			leaseRef := v1alpha1.NamespacedReference{Namespace: lease.Namespace, Name: lease.Name}
			index := slices.Index(endpoint.Status.ActiveLeases, leaseRef)
			if index < 0 {
				return nil
			}
			endpoint.Status.ActiveLeases = slices.Delete(endpoint.Status.ActiveLeases, index, index+1)
			endpoint.Status.ActiveLeaseCount = int32(len(endpoint.Status.ActiveLeases))
			endpoint.Status.AllocatedKVRAMMiB = max(0, endpoint.Status.AllocatedKVRAMMiB-lease.Spec.EstimatedKVRAMMiB)
			return r.Status().Update(ctx, &endpoint)
		}); err != nil {
			return err
		}
	}
	lease.Status.Phase = v1alpha1.PhaseInterrupted
	lease.Status.Reason = reason
	if err := r.Status().Update(ctx, &lease); err != nil {
		return err
	}
	return r.appendLeaseEvent(ctx, &lease, "InferenceLeaseInterrupted", "interrupt", lease.Name, "interrupted", reason, map[string]string{"endpoint": lease.Status.EndpointRef.Name})
}

func (r *InferenceLeaseReconciler) appendLeaseEvent(ctx context.Context, lease *v1alpha1.InferenceLease, eventType, action, target, outcome, reason string, references map[string]string) error {
	if references == nil {
		references = make(map[string]string)
	}
	references["lease"] = lease.Name
	references["attempt"] = lease.Spec.AttemptRef
	return appendControllerEvent(ctx, r.Audit, "inferencelease-controller", r.Now, audit.EventOptions{
		Type: eventType,
		Subject: audit.Subject{
			Project:   lease.Spec.ProjectRef,
			Namespace: lease.Namespace,
			Workflow:  lease.Spec.WorkflowRef.Name,
			Attempt:   0,
		},
		Action:     action,
		Target:     target,
		Outcome:    outcome,
		Reason:     reason,
		DecisionID: lease.Status.DecisionID,
		References: references,
		Data: map[string]any{
			"model":             lease.Spec.Model,
			"modelRevision":     lease.Spec.ModelRevision,
			"tenant":            lease.Spec.Tenant,
			"classification":    lease.Spec.Classification,
			"sharingScope":      lease.Spec.SharingScope,
			"estimatedKVRAMMiB": lease.Spec.EstimatedKVRAMMiB,
			"priority":          lease.Spec.Priority,
			"evictable":         lease.Spec.Evictable,
		},
	})
}
