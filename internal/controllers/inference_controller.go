package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	admission "github.com/SovereignAI/internal/inference"
	policyengine "github.com/SovereignAI/internal/policy"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const InferenceNamespace = "sovereign-inference"

type InferenceLeaseReconciler struct {
	client.Client
	Scheme               *runtime.Scheme
	RuntimeImage         string
	DefaultStaticVRAMMiB int64
	DefaultMaxKVRAMMiB   int64
	SafetyHeadroomMiB    int64
	Policy               policyengine.Evaluator
}

func (r *InferenceLeaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.InferenceLease{}).Complete(r)
}

func (r *InferenceLeaseReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var lease v1alpha1.InferenceLease
	if err := r.Get(ctx, request.NamespacedName, &lease); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if lease.Status.Phase == v1alpha1.PhaseRunning || lease.Status.Phase == v1alpha1.PhaseFailed {
		return ctrl.Result{}, nil
	}
	if lease.Status.Phase == "" {
		lease.Status.Phase = v1alpha1.PhasePending
		lease.Status.ObservedGeneration = lease.Generation
		return ctrl.Result{}, r.Status().Update(ctx, &lease)
	}
	if err := r.ensureInferenceNamespace(ctx); err != nil {
		return ctrl.Result{}, err
	}
	snapshots, err := r.snapshots(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	filtered, policyDecision, err := r.admitSnapshots(ctx, lease, snapshots)
	if err != nil {
		return ctrl.Result{}, err
	}
	decision := admission.Select(lease, filtered, true)
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
		createDecision, err := r.evaluateCreate(ctx, &lease)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !createDecision.Allowed {
			lease.Status.Phase = v1alpha1.PhaseFailed
			lease.Status.DecisionID = createDecision.ID
			lease.Status.Reason = fmt.Sprint(createDecision.Reasons)
			return ctrl.Result{}, r.Status().Update(ctx, &lease)
		}
		endpoint, err := r.ensureEndpoint(ctx, &lease)
		if err != nil {
			return ctrl.Result{}, err
		}
		if endpoint.Status.Phase == v1alpha1.PhaseRunning {
			return ctrl.Result{}, r.bind(ctx, &lease, v1alpha1.NamespacedReference{Namespace: endpoint.Namespace, Name: endpoint.Name}, createDecision.ID, "new endpoint is ready")
		}
		return ctrl.Result{RequeueAfter: 3 * time.Second}, nil
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
	image := r.RuntimeImage
	if image == "" {
		image = "vllm/vllm-openai:v0.10.2"
	}
	endpoint = v1alpha1.InferenceEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: InferenceNamespace, Labels: map[string]string{
			"sovereign-ai.io/project": lease.Spec.ProjectRef, "sovereign-ai.io/tenant": lease.Spec.Tenant,
			"sovereign-ai.io/classification": lease.Spec.Classification,
		}},
		Spec: v1alpha1.InferenceEndpointSpec{
			Provider: "vllm", Model: lease.Spec.Model, ModelRevision: lease.Spec.ModelRevision, RuntimeImage: image,
			Tenant: lease.Spec.Tenant, Classification: lease.Spec.Classification, SharingScope: lease.Spec.SharingScope,
			StaticVRAMMiB: staticVRAM, MaxKVRAMMiB: maxKV, SafetyHeadroomMiB: r.SafetyHeadroomMiB,
		},
	}
	if err := r.Create(ctx, &endpoint); err != nil {
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
	return r.Status().Update(ctx, lease)
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
	return r.Status().Update(ctx, &lease)
}

type InferenceEndpointReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

func (r *InferenceEndpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&v1alpha1.InferenceEndpoint{}).Owns(&corev1.Pod{}).Owns(&corev1.Service{}).Complete(r)
}

func (r *InferenceEndpointReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var endpoint v1alpha1.InferenceEndpoint
	if err := r.Get(ctx, request.NamespacedName, &endpoint); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	pod, service := buildInferenceWorkloads(&endpoint)
	if err := controllerutil.SetControllerReference(&endpoint, pod, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := controllerutil.SetControllerReference(&endpoint, service, r.Scheme); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, service); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	if err := r.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	var current corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Namespace: endpoint.Namespace, Name: endpoint.Name}, &current); err != nil {
		return ctrl.Result{}, err
	}
	phase := v1alpha1.PhasePreparing
	if current.Status.Phase == corev1.PodRunning && podReady(current) {
		phase = v1alpha1.PhaseRunning
	} else if current.Status.Phase == corev1.PodFailed {
		phase = v1alpha1.PhaseFailed
	}
	if endpoint.Status.Phase != phase || endpoint.Status.PodRef == "" {
		endpoint.Status.Phase = phase
		endpoint.Status.PodRef = pod.Name
		endpoint.Status.ServiceRef = service.Name
		endpoint.Status.NodeName = current.Spec.NodeName
		endpoint.Status.ObservedGeneration = endpoint.Generation
		return ctrl.Result{}, r.Status().Update(ctx, &endpoint)
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func buildInferenceWorkloads(endpoint *v1alpha1.InferenceEndpoint) (*corev1.Pod, *corev1.Service) {
	labels := map[string]string{"app.kubernetes.io/name": "vllm-server", "sovereign-ai.io/endpoint-id": endpoint.Name, "sovereign-ai.io/model": endpoint.Spec.Model}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: endpoint.Name, Namespace: endpoint.Namespace, Labels: labels},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyAlways, Containers: []corev1.Container{{
			Name: "vllm", Image: endpoint.Spec.RuntimeImage,
			Args:           []string{"--model", endpoint.Spec.Model, "--revision", endpoint.Spec.ModelRevision, "--port", "8000"},
			Ports:          []corev1.ContainerPort{{Name: "http", ContainerPort: 8000}},
			Resources:      corev1.ResourceRequirements{Limits: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("1")}},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/health", Port: intstr.FromInt32(8000)}}, PeriodSeconds: 5},
		}}},
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: endpoint.Name, Namespace: endpoint.Namespace, Labels: labels}, Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Name: "http", Port: 8000, TargetPort: intstr.FromInt32(8000)}}}}
	return pod, service
}

func podReady(pod corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func endpointName(parts ...string) string {
	hash := sha256.Sum256([]byte(fmt.Sprint(parts)))
	return "vllm-" + hex.EncodeToString(hash[:])[:12]
}
