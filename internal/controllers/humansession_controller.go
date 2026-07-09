package controllers

import (
	"context"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// / Responsible for managing the lifecycle of the human workspace resources (pod/svc/svc-acc & ttl)
type HumanSessionReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	CodeServerImage string
	Now             func() time.Time
}

func (r *HumanSessionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(
			&v1alpha1.HumanSession{}).
		Owns(
			&corev1.Pod{}).
		Owns(
			&corev1.Service{}).
		Owns(&corev1.ServiceAccount{}).
		Complete(r)
}

// Assuming a 2 hour default TTL. I think this could be policy driven
// but I may need to consider classifications or other variables
var (
	defaultTTL = 2 * time.Hour
)

func (r *HumanSessionReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var session v1alpha1.HumanSession
	if err := r.Get(ctx, request.NamespacedName, &session); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Verify TTL by evaluating against the expiration time
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}

	if session.Status.ExpiresAt != nil && !now.Before(session.Status.ExpiresAt.Time) {
		pod, service, serviceAccount := buildHumanSessionWorkloads(&session, r.image())
		_ = client.IgnoreNotFound(r.Delete(ctx, pod))
		_ = client.IgnoreNotFound(r.Delete(ctx, service))
		_ = client.IgnoreNotFound(r.Delete(ctx, serviceAccount))
		if session.Status.Phase != v1alpha1.PhaseCancelled {
			session.Status.Phase = v1alpha1.PhaseCancelled
			return ctrl.Result{}, r.Status().Update(ctx, &session)
		}
		return ctrl.Result{}, nil
	}

	// Trigger new session
	if session.Status.Phase == "" {
		ttl := 2 * time.Hour
		if session.Spec.TTL != nil && session.Spec.TTL.Duration > 0 {
			ttl = session.Spec.TTL.Duration
		}
		expires := metav1.NewTime(now.Add(ttl))
		session.Status.Phase = v1alpha1.PhasePreparing
		session.Status.ExpiresAt = &expires
		session.Status.ObservedGeneration = session.Generation
		return ctrl.Result{}, r.Status().Update(ctx, &session)
	}

	// Build & Create human session pod and bind controller reference
	pod, service, serviceAccount := buildHumanSessionWorkloads(&session, r.image())
	for _, object := range []client.Object{serviceAccount, pod, service} {
		if err := controllerutil.SetControllerReference(&session, object, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, object); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
	}

	var current corev1.Pod
	if err := r.Get(ctx, request.NamespacedName, &current); err != nil {
		return ctrl.Result{}, err
	}

	// Convert pod phase to api phase
	phase := v1alpha1.PhasePreparing
	if current.Status.Phase == corev1.PodRunning && podReady(current) {
		phase = v1alpha1.PhaseRunning
	}

	// Align session phase with pod reference phase
	if session.Status.Phase != phase || session.Status.PodRef == "" {
		session.Status.Phase = phase
		session.Status.PodRef = pod.Name
		session.Status.ServiceRef = service.Name
		session.Status.AccessURL = "/sessions/" + session.Namespace + "/" + session.Name
		return ctrl.Result{}, r.Status().Update(ctx, &session)
	}

	// Requeue request to expiration
	return ctrl.Result{RequeueAfter: time.Until(session.Status.ExpiresAt.Time)}, nil
}

// For MVP we'll use a pre-defined image
func (r *HumanSessionReconciler) image() string {
	if r.CodeServerImage != "" {
		return r.CodeServerImage
	}
	return "ghcr.io/coder/code-server:4.99.4"
}

// Builds human session workload (pod, service, serviceAccount) primitives
func buildHumanSessionWorkloads(session *v1alpha1.HumanSession, image string) (*corev1.Pod, *corev1.Service, *corev1.ServiceAccount) {
	labels := map[string]string{"app.kubernetes.io/name": "sovereign-human-session", "sovereign-ai.io/human-session": session.Name, LabelWorkflow: session.Spec.WorkflowRef}
	automount := false
	nonRoot := true
	allowPrivilegeEscalation := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace, Labels: labels},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: session.Name, AutomountServiceAccountToken: &automount,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot},
			Containers: []corev1.Container{{Name: "code-server", Image: image, Args: []string{"--auth", "none", "--bind-addr", "0.0.0.0:8080", "/workspace"},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}}},
			Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: session.Spec.WorkflowRef + "-workspace"}}}},
		},
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace, Labels: labels}, Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Name: "http", Port: 8080}}}}
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace, Labels: labels}, AutomountServiceAccountToken: &automount}
	return pod, service, serviceAccount
}
