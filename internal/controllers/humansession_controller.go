package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
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
	Audit           audit.Recorder
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
	if !session.DeletionTimestamp.IsZero() {
		return r.finalizeWorkspaceWriter(ctx, &session)
	}
	if !controllerutil.ContainsFinalizer(&session, WorkspaceWriterFinalizer) {
		controllerutil.AddFinalizer(&session, WorkspaceWriterFinalizer)
		if err := r.Update(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Verify TTL by evaluating against the expiration time
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}

	if session.Status.ExpiresAt != nil && !now.Before(session.Status.ExpiresAt.Time) {
		if err := r.deleteWorkspaceWriterWorkloads(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		quiet, err := PodWriterQuiescent(ctx, r.Client, session.Namespace, session.Name)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !quiet {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		if session.Status.Phase != v1alpha1.PhaseCancelled {
			if err := r.releaseWorkspaceWriter(ctx, &session); err != nil {
				return ctrl.Result{}, err
			}
			session.Status.Phase = v1alpha1.PhaseCancelled
			if err := r.Status().Update(ctx, &session); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.appendHumanSessionEvent(ctx, &session, "HumanSessionExpired", "expire", "expired", "TTLExpired")
		}
		return ctrl.Result{}, nil
	}

	// Check for namespace termination
	terminating, err := NamespaceTerminating(ctx, r.Client, session.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if terminating {
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
		if err := r.Status().Update(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.appendHumanSessionEvent(ctx, &session, "HumanSessionCreated", "create", "created", "")
	}
	if terminalAttempt(session.Status.Phase) {
		return r.reconcileWorkspaceWriterRelease(ctx, &session)
	}

	grant, writerState, err := r.ensureWorkspaceWriter(ctx, &session)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch writerState {
	case WorkspaceWriterBlocked:
		if session.Status.PodRef != "" {
			if err := r.deleteWorkspaceWriterWorkloads(ctx, &session); err != nil {
				return ctrl.Result{}, err
			}
			session.Status.Phase = v1alpha1.PhaseInterrupted
			if err := r.Status().Update(ctx, &session); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.appendHumanSessionEvent(ctx, &session, "WorkspaceWriterAuthorityNotEstablished", "interrupt", "interrupted", "WorkspaceWriterAuthorityNotEstablished")
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	case WorkspaceWriterLost:
		if err := r.deleteWorkspaceWriterWorkloads(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		session.Status.Phase = v1alpha1.PhaseInterrupted
		if err := r.Status().Update(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.appendHumanSessionEvent(ctx, &session, "WorkspaceWriterAuthorityLost", "interrupt", "interrupted", "WorkspaceWriterAuthorityLost")
	}

	// Build & Create human session pod and bind controller reference
	pod, service, serviceAccount := buildHumanSessionWorkloads(&session, r.image(), grant)
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
		if err := r.Status().Update(ctx, &session); err != nil {
			return ctrl.Result{}, err
		}
		eventType := "HumanSessionWorkloadsCreated"
		outcome := "created"
		if phase == v1alpha1.PhaseRunning {
			eventType = "HumanSessionReady"
			outcome = "ready"
		}
		return ctrl.Result{}, r.appendHumanSessionEvent(ctx, &session, eventType, "reconcile", outcome, string(phase))
	}

	// Requeue request to expiration
	return ctrl.Result{RequeueAfter: time.Until(session.Status.ExpiresAt.Time)}, nil
}

func (r *HumanSessionReconciler) ensureWorkspaceWriter(ctx context.Context, session *v1alpha1.HumanSession) (WorkspaceWriterGrant, WorkspaceWriterState, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, client.ObjectKey{Namespace: session.Namespace, Name: session.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return WorkspaceWriterGrant{}, WorkspaceWriterBlocked, err
	}
	if workflow.Status.WorkspaceWriterLeaseRef == "" {
		return WorkspaceWriterGrant{}, WorkspaceWriterBlocked, nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	grant, state, err := AcquireWorkspaceWriter(ctx, r.Client, &workflow, workflow.Status.WorkspaceWriterLeaseRef, "HumanSession", session, session.Status.WorkspaceWriterEpoch, now)
	if err != nil || state != WorkspaceWriterGranted {
		return grant, state, err
	}
	newGrant := session.Status.WorkspaceWriterEpoch == 0
	session.Status.WorkspaceWriterLeaseRef = grant.LeaseName
	session.Status.WorkspaceWriterEpoch = grant.Epoch
	session.Status.WorkspaceWriterReleased = false
	session.Status.ObservedGeneration = session.Generation
	if err := r.Status().Update(ctx, session); err != nil {
		return WorkspaceWriterGrant{}, WorkspaceWriterBlocked, err
	}
	if newGrant {
		if err := r.appendHumanSessionEvent(ctx, session, "WorkspaceWriterAcquired", "acquire", "granted", fmt.Sprintf("WriterEpoch%d", grant.Epoch)); err != nil {
			return WorkspaceWriterGrant{}, WorkspaceWriterBlocked, err
		}
	}
	return grant, state, nil
}

func (r *HumanSessionReconciler) workspaceWriterGrant(session *v1alpha1.HumanSession) (WorkspaceWriterGrant, error) {
	holder, err := WorkspaceWriterIdentity("HumanSession", session)
	if err != nil {
		return WorkspaceWriterGrant{}, err
	}
	if session.Status.WorkspaceWriterLeaseRef == "" || session.Status.WorkspaceWriterEpoch < 1 {
		return WorkspaceWriterGrant{}, fmt.Errorf("HumanSession %s has no workspace writer grant", session.Name)
	}
	return WorkspaceWriterGrant{LeaseName: session.Status.WorkspaceWriterLeaseRef, HolderIdentity: holder, Epoch: session.Status.WorkspaceWriterEpoch}, nil
}

func (r *HumanSessionReconciler) releaseWorkspaceWriter(ctx context.Context, session *v1alpha1.HumanSession) error {
	if session.Status.WorkspaceWriterEpoch < 1 || session.Status.WorkspaceWriterReleased {
		return nil
	}
	grant, err := r.workspaceWriterGrant(session)
	if err != nil {
		return err
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	if err := ReleaseWorkspaceWriter(ctx, r.Client, session.Namespace, grant, now); err != nil {
		return err
	}
	session.Status.WorkspaceWriterReleased = true
	session.Status.ObservedGeneration = session.Generation
	return r.Status().Update(ctx, session)
}

func (r *HumanSessionReconciler) reconcileWorkspaceWriterRelease(ctx context.Context, session *v1alpha1.HumanSession) (ctrl.Result, error) {
	if session.Status.WorkspaceWriterEpoch < 1 || session.Status.WorkspaceWriterReleased {
		return ctrl.Result{}, nil
	}
	quiet, err := PodWriterQuiescent(ctx, r.Client, session.Namespace, session.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !quiet {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	grant, err := r.workspaceWriterGrant(session)
	if err != nil {
		return ctrl.Result{}, err
	}
	if err := r.releaseWorkspaceWriter(ctx, session); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.appendHumanSessionEvent(ctx, session, "WorkspaceWriterReleased", "release", "released", fmt.Sprintf("WriterEpoch%d", grant.Epoch))
}

func (r *HumanSessionReconciler) deleteWorkspaceWriterWorkloads(ctx context.Context, session *v1alpha1.HumanSession) error {
	objects := []client.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace}},
	}
	for _, object := range objects {
		if err := r.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *HumanSessionReconciler) finalizeWorkspaceWriter(ctx context.Context, session *v1alpha1.HumanSession) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(session, WorkspaceWriterFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.deleteWorkspaceWriterWorkloads(ctx, session); err != nil {
		return ctrl.Result{}, err
	}
	quiet, err := PodWriterQuiescent(ctx, r.Client, session.Namespace, session.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !quiet {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.releaseWorkspaceWriter(ctx, session); err != nil {
		return ctrl.Result{}, err
	}
	controllerutil.RemoveFinalizer(session, WorkspaceWriterFinalizer)
	return ctrl.Result{}, r.Update(ctx, session)
}

func (r *HumanSessionReconciler) appendHumanSessionEvent(ctx context.Context, session *v1alpha1.HumanSession, eventType, action, outcome, reason string) error {
	return audit.AppendControllerEvent(ctx, r.Audit, "humansession-controller", r.Now, audit.EventOptions{
		Type: eventType,
		Subject: audit.Subject{
			Namespace: session.Namespace,
			Workflow:  session.Spec.WorkflowRef.Name,
		},
		Action:  action,
		Target:  session.Name,
		Outcome: outcome,
		Reason:  reason,
		References: map[string]string{
			"humanSession":         session.Name,
			"pod":                  session.Status.PodRef,
			"service":              session.Status.ServiceRef,
			"accessURL":            session.Status.AccessURL,
			"workspaceWriterLease": session.Status.WorkspaceWriterLeaseRef,
			"writerEpoch":          fmt.Sprint(session.Status.WorkspaceWriterEpoch),
		},
		Data: map[string]any{
			"requesterSubject": session.Spec.RequesterSubject,
			"toolProfile":      session.Spec.ToolProfile,
			"capabilities":     session.Spec.Capabilities,
			"expiresAt":        session.Status.ExpiresAt,
		},
	})
}

// For MVP we'll use a pre-defined image
func (r *HumanSessionReconciler) image() string {
	if r.CodeServerImage != "" {
		return r.CodeServerImage
	}
	return "ghcr.io/coder/code-server:4.99.4"
}

// Builds human session workload (pod, service, serviceAccount) primitives
func buildHumanSessionWorkloads(session *v1alpha1.HumanSession, image string, grant WorkspaceWriterGrant) (*corev1.Pod, *corev1.Service, *corev1.ServiceAccount) {
	labels := map[string]string{"app.kubernetes.io/name": "sovereign-human-session", "sovereign-ai.io/human-session": session.Name, controllermeta.LabelWorkflow: session.Spec.WorkflowRef.Name}
	automount := false
	nonRoot := true
	allowPrivilegeEscalation := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace, Labels: labels, Annotations: WorkspaceWriterAnnotations(grant)},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: session.Name, AutomountServiceAccountToken: &automount,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot},
			Containers: []corev1.Container{{Name: "code-server", Image: image, Args: []string{"--auth", "none", "--bind-addr", "0.0.0.0:8080", "/workspace"}, Env: WorkspaceWriterEnv(grant),
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Ports:           []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}}, VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}}},
			Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: session.Spec.WorkflowRef.Name + "-workspace"}}}},
		},
	}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace, Labels: labels}, Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Name: "http", Port: 8080}}}}
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: session.Name, Namespace: session.Namespace, Labels: labels}, AutomountServiceAccountToken: &automount}
	return pod, service, serviceAccount
}
