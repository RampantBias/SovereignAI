package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/inference"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type InferenceEndpointReconciler struct {
	client.Client
	Profile inference.Profile
	Scheme  *runtime.Scheme
	Audit   audit.Recorder
	Now     func() time.Time
}

func (r *InferenceEndpointReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.InferenceEndpoint{}).
		Owns(&corev1.Pod{}).
		Owns(&corev1.Service{}).
		Complete(r)
}

// Responsible for endpoint health/status, inference pod + service lifecycle, readiness
func (r *InferenceEndpointReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var endpoint v1alpha1.InferenceEndpoint
	if err := r.Get(ctx, request.NamespacedName, &endpoint); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Build inference workloads (inference pod + exposing service)
	pod, service := buildInferenceWorkloads(&endpoint)

	// Create or observe inference workloads in cluster
	if err := r.createInferenceWorkloads(ctx, &endpoint, pod, service); err != nil {
		return ctrl.Result{}, err
	}

	// Get pod & current phase
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

	// Update endpoint status with transition.
	if endpoint.Status.Phase != phase || endpoint.Status.PodRef == "" {
		return ctrl.Result{}, r.updateEndpointStatus(ctx, &endpoint, phase, pod.Name, service.Name, current.Spec.NodeName)
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *InferenceEndpointReconciler) appendEndpointEvent(ctx context.Context, endpoint *v1alpha1.InferenceEndpoint, eventType, action, outcome, reason string) error {
	return appendControllerEvent(ctx, r.Audit, "inferenceendpoint-controller", r.Now, audit.EventOptions{
		Type: eventType,
		Subject: audit.Subject{
			Project: endpoint.Labels["sovereign-ai.io/project"],
		},
		Action:  action,
		Target:  endpoint.Name,
		Outcome: outcome,
		Reason:  reason,
		References: map[string]string{
			"endpoint": endpoint.Name,
			"pod":      endpoint.Status.PodRef,
			"service":  endpoint.Status.ServiceRef,
			"node":     endpoint.Status.NodeName,
		},
		Data: map[string]any{
			"model":             endpoint.Spec.Model,
			"modelRevision":     endpoint.Spec.ModelRevision,
			"tenant":            endpoint.Spec.Tenant,
			"classification":    endpoint.Spec.Classification,
			"sharingScope":      endpoint.Spec.SharingScope,
			"allocatedKVRAMMiB": endpoint.Status.AllocatedKVRAMMiB,
			"activeLeaseCount":  endpoint.Status.ActiveLeaseCount,
		},
	})
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

func (r *InferenceEndpointReconciler) createInferenceWorkloads(
	ctx context.Context, endpoint *v1alpha1.InferenceEndpoint, pod *corev1.Pod, service *corev1.Service) error {
	if err := controllerutil.SetControllerReference(endpoint, pod, r.Scheme); err != nil {
		return err
	}
	if err := controllerutil.SetControllerReference(endpoint, service, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, service); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	if err := r.Create(ctx, pod); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *InferenceEndpointReconciler) updateEndpointStatus(ctx context.Context,
	endpoint *v1alpha1.InferenceEndpoint,
	phase v1alpha1.ResourcePhase,
	podName, serviceName, nodeName string) error {
	// Update endpoint status
	endpoint.Status.Phase = phase
	endpoint.Status.PodRef = podName
	endpoint.Status.ServiceRef = serviceName
	endpoint.Status.NodeName = nodeName
	endpoint.Status.ObservedGeneration = endpoint.Generation
	if err := r.Status().Update(ctx, endpoint); err != nil {
		return err
	}

	// Evaluate event type via phase
	eventType := "InferenceEndpointCreated"
	outcome := "created"
	if phase == v1alpha1.PhaseRunning {
		eventType = "InferenceEndpointReady"
		outcome = "ready"
	}
	if phase == v1alpha1.PhaseFailed {
		eventType = "InferenceEndpointLost"
		outcome = "lost"
	}
	return r.appendEndpointEvent(ctx, endpoint, eventType, "observe", outcome, string(phase))
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
