package controllers

import (
	"context"
	"encoding/json"
	"os"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// AgentRunReconciler owns delegated autonomous execution. It is the only
// controller that creates agent pods or inference leases for an agent run.
type AgentRunReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Audit          audit.Recorder
	Now            func() time.Time
	CollectorImage string
}

func (r *AgentRunReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.AgentRun{}).
		Owns(&corev1.Pod{}).
		Owns(&batchv1.Job{}).
		Owns(&v1alpha1.InferenceLease{}).
		Complete(r)
}

func (r *AgentRunReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var run v1alpha1.AgentRun
	if err := r.Get(ctx, request.NamespacedName, &run); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !run.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}
	terminating, err := namespaceTerminating(ctx, r.Client, run.Namespace)
	if err != nil || terminating {
		return ctrl.Result{}, err
	}
	if run.Status.Phase == "" {
		return ctrl.Result{}, r.setPhase(ctx, &run, v1alpha1.PhasePending, "Initialized", "agent run initialized")
	}
	if terminalAttempt(run.Status.Phase) {
		return ctrl.Result{}, nil
	}
	authorized, err := validateDomainAuthority(ctx, r.Client, &run, run.Spec.AttemptRef, v1alpha1.ExecutionKindAgent, run.Spec.WorkflowRef, run.Spec.StepName, run.Spec.Attempt)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, &run, "InvalidStepAttemptAuthority", false)
	}
	if !authorized {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	endpoint := ""
	if run.Spec.Inference != nil {
		lease, err := r.ensureLease(ctx, &run)
		if err != nil {
			return ctrl.Result{}, err
		}
		switch lease.Status.Phase {
		case v1alpha1.PhaseInterrupted:
			if err := r.deletePod(ctx, &run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.interrupt(ctx, &run, lease.Status.Reason, true)
		case v1alpha1.PhaseFailed:
			return ctrl.Result{}, r.fail(ctx, &run, "InferenceAdmissionFailed", true)
		case v1alpha1.PhaseRunning:
			endpoint = lease.Status.EndpointURL
		default:
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	if run.Status.PodRef == "" {
		if err := r.ensureWorkload(ctx, &run, endpoint); err != nil {
			return ctrl.Result{}, err
		}
		run.Status.PodRef = run.Name
		return ctrl.Result{}, r.setPhase(ctx, &run, v1alpha1.PhasePreparing, "PodCreated", "agent pod created")
	}
	if run.Status.CollectorJobRef != "" {
		return r.reconcileCollection(ctx, &run)
	}
	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Status.PodRef}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.interrupt(ctx, &run, "AgentPodLost", true)
		}
		return ctrl.Result{}, err
	}
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		return r.startCollection(ctx, &run)
	case corev1.PodFailed:
		run.Status.FailureReason = "AgentPodFailed"
		run.Status.Retryable = true
		return r.startCollection(ctx, &run)
	case corev1.PodRunning:
		return ctrl.Result{}, r.setPhase(ctx, &run, v1alpha1.PhaseRunning, "AgentRunning", "agent is running")
	default:
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
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
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef}, &workflow); err != nil {
		return nil, err
	}
	lease = v1alpha1.InferenceLease{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: run.Namespace}, Spec: v1alpha1.InferenceLeaseSpec{
		WorkflowRef: workflow.Name, AttemptRef: run.Spec.AttemptRef, ProjectRef: workflow.Spec.ProjectName,
		Tenant: workflow.Spec.ProjectName, Classification: workflow.Spec.Classification,
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

func (r *AgentRunReconciler) deletePod(ctx context.Context, run *v1alpha1.AgentRun) error {
	if run.Status.PodRef == "" {
		return nil
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: run.Status.PodRef, Namespace: run.Namespace}}
	return client.IgnoreNotFound(r.Delete(ctx, pod))
}

func (r *AgentRunReconciler) ensureWorkload(ctx context.Context, run *v1alpha1.AgentRun, endpoint string) error {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef}, &workflow); err != nil {
		return err
	}
	outputs := make([]agentcontract.OutputObligation, 0, len(run.Spec.OutputContracts))
	for _, output := range run.Spec.OutputContracts {
		outputs = append(outputs, agentcontract.OutputObligation{Name: output.Name, Version: output.Version, Required: true})
	}
	input := agentcontract.Input{
		SchemaVersion: agentcontract.Version, WorkflowID: workflow.Spec.WorkflowID, StepName: run.Spec.StepName,
		Attempt: run.Spec.Attempt, Role: run.Spec.StepName, Responsibility: run.Spec.Responsibility,
		Capabilities: append([]string(nil), run.Spec.Capabilities...), Outputs: outputs, InferenceEndpoint: endpoint,
		MCPServer: "https://sovereign-mcp.sovereign-orchestrator-system.svc", WorkspacePath: "/workspace",
		StagingPath: executionStagingPath(run.Name), ControlPath: executionControlPath(run.Name),
		ResultPath: executionResultPath(run.Name), AuditEventsPath: executionAuditEventsPath(run.Name),
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	configName := run.Name + "-input"
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configName, Namespace: run.Namespace}, Data: map[string]string{"input.json": string(data)}}
	if err := controllerutil.SetControllerReference(run, config, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, config); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	pod := buildAgentRunPod(run, workflow.Status.PvcName, configName)
	if err := controllerutil.SetControllerReference(run, pod, r.Scheme); err != nil {
		return err
	}
	return client.IgnoreAlreadyExists(r.Create(ctx, pod))
}

func buildAgentRunPod(run *v1alpha1.AgentRun, pvcName, configName string) *corev1.Pod {
	if pvcName == "" {
		pvcName = run.Spec.WorkflowRef + "-workspace"
	}
	automount, nonRoot, readOnly, allowPrivilegeEscalation := false, true, true, false
	runAsUser := int64(65532)
	executable, _ := json.Marshal(run.Spec.Executable)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: run.Name, Namespace: run.Namespace, Labels: map[string]string{
		LabelWorkflow: run.Labels[LabelWorkflow], LabelStep: run.Spec.StepName, "sovereign-ai.io/agent-run": run.Name,
	}}, Spec: corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &automount,
		SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot, RunAsUser: &runAsUser, FSGroup: &runAsUser},
		Containers: []corev1.Container{{Name: "agent", Image: run.Spec.Image, Command: []string{"/agent-wrapper"},
			Args:            []string{"--input", "/control/input.json", "--result", executionResultPath(run.Name)},
			Env:             []corev1.EnvVar{{Name: "SOVEREIGN_AGENT_EXECUTABLE", Value: string(executable)}},
			SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation, ReadOnlyRootFilesystem: &readOnly, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			VolumeMounts:    []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "input", MountPath: "/control", ReadOnly: true}},
		}}, Volumes: []corev1.Volume{
			{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}}},
			{Name: "input", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}},
		},
	}}
}

func (r *AgentRunReconciler) startCollection(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef}, &workflow); err != nil {
		return ctrl.Result{}, err
	}
	objects := buildCollectorResources(run, &workflow, run.Spec.StepName, r.collectorImage())
	for _, object := range objects {
		if err := controllerutil.SetControllerReference(run, object, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, object); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
	}
	run.Status.CollectorJobRef = run.Name + "-collect"
	return ctrl.Result{}, r.setPhase(ctx, run, v1alpha1.PhaseCollecting, "CollectorCreated", "agent result collector created")
}

func (r *AgentRunReconciler) reconcileCollection(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Status.CollectorJobRef}, &job); err != nil {
		return ctrl.Result{}, err
	}
	if job.Status.Succeeded > 0 {
		if run.Status.FailureReason != "" {
			return ctrl.Result{}, r.fail(ctx, run, run.Status.FailureReason, run.Status.Retryable)
		}
		return ctrl.Result{}, r.setPhase(ctx, run, v1alpha1.PhaseSucceeded, "ArtifactsCollected", "agent result and artifacts accepted")
	}
	if job.Status.Failed > 0 {
		if run.Status.FailureReason != "" {
			return ctrl.Result{}, r.fail(ctx, run, run.Status.FailureReason, run.Status.Retryable)
		}
		return ctrl.Result{}, r.fail(ctx, run, "ArtifactCollectionFailed", false)
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *AgentRunReconciler) collectorImage() string {
	if r.CollectorImage != "" {
		return r.CollectorImage
	}
	return "sovereign-artifact-collector:dev"
}

func (r *AgentRunReconciler) setPhase(ctx context.Context, run *v1alpha1.AgentRun, phase v1alpha1.ResourcePhase, reason, message string) error {
	updated, changed, err := r.updateStatus(ctx, client.ObjectKeyFromObject(run), func(latest *v1alpha1.AgentRun) bool {
		condition := apiMeta.FindStatusCondition(latest.Status.Conditions, "Ready")
		if latest.Status.Phase == phase && condition != nil && condition.Reason == reason && condition.Message == message {
			return false
		}
		now := metav1.Now()
		if r.Now != nil {
			now = metav1.NewTime(r.Now())
		}
		latest.Status.Phase = phase
		latest.Status.PodRef = run.Status.PodRef
		latest.Status.CollectorJobRef = run.Status.CollectorJobRef
		latest.Status.InferenceLeaseRef = run.Status.InferenceLeaseRef
		latest.Status.FailureReason = run.Status.FailureReason
		latest.Status.Retryable = run.Status.Retryable
		latest.Status.ObservedGeneration = latest.Generation
		if phase == v1alpha1.PhaseRunning && latest.Status.StartedAt == nil {
			latest.Status.StartedAt = &now
		}
		if terminalAttempt(phase) {
			latest.Status.CompletedAt = &now
		}
		apiMeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{Type: "Ready", Status: conditionStatus(phase), Reason: reason, Message: message, ObservedGeneration: latest.Generation})
		return true
	})
	if err != nil || !changed {
		return err
	}
	return r.appendEvent(ctx, updated, "AgentRun"+string(phase), "reconcile", updated.Name, string(phase), reason)
}

func (r *AgentRunReconciler) fail(ctx context.Context, run *v1alpha1.AgentRun, reason string, retryable bool) error {
	run.Status.FailureReason, run.Status.Retryable = reason, retryable
	return r.setPhase(ctx, run, v1alpha1.PhaseFailed, reason, "agent run failed")
}

func (r *AgentRunReconciler) interrupt(ctx context.Context, run *v1alpha1.AgentRun, reason string, retryable bool) error {
	run.Status.FailureReason, run.Status.Retryable = reason, retryable
	return r.setPhase(ctx, run, v1alpha1.PhaseInterrupted, reason, "agent run interrupted")
}

func (r *AgentRunReconciler) updateStatusFields(ctx context.Context, run *v1alpha1.AgentRun) error {
	_, _, err := r.updateStatus(ctx, client.ObjectKeyFromObject(run), func(latest *v1alpha1.AgentRun) bool {
		if latest.Status.InferenceLeaseRef == run.Status.InferenceLeaseRef {
			return false
		}
		latest.Status.InferenceLeaseRef = run.Status.InferenceLeaseRef
		latest.Status.ObservedGeneration = latest.Generation
		return true
	})
	return err
}

func (r *AgentRunReconciler) updateStatus(ctx context.Context, key types.NamespacedName, mutate func(*v1alpha1.AgentRun) bool) (*v1alpha1.AgentRun, bool, error) {
	var updated v1alpha1.AgentRun
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.AgentRun
		if err := r.Get(ctx, key, &latest); err != nil {
			return err
		}
		if !mutate(&latest) {
			updated = latest
			return nil
		}
		if err := r.Status().Update(ctx, &latest); err != nil {
			return err
		}
		updated, changed = latest, true
		return nil
	})
	return &updated, changed, err
}

func (r *AgentRunReconciler) appendEvent(ctx context.Context, run *v1alpha1.AgentRun, eventType, action, target, outcome, reason string) error {
	return appendControllerEvent(ctx, r.Audit, "agentrun-controller", r.Now, audit.EventOptions{
		Type: eventType, Subject: audit.Subject{Namespace: run.Namespace, Workflow: run.Spec.WorkflowRef, Step: run.Spec.StepName, Attempt: run.Spec.Attempt},
		Action: action, Target: target, Outcome: outcome, Reason: reason,
		References: map[string]string{"agentRun": run.Name, "stepAttempt": run.Spec.AttemptRef, "pod": run.Status.PodRef, "collector": run.Status.CollectorJobRef, "lease": run.Status.InferenceLeaseRef},
	})
}

func executionControlPath(name string) string { return "/workspace/attempts/" + name + "/control" }
func executionStagingPath(name string) string { return "/workspace/attempts/" + name + "/staging" }
func executionResultPath(name string) string  { return executionControlPath(name) + "/result.json" }
func executionAuditEventsPath(name string) string {
	return executionControlPath(name) + "/events.jsonl"
}

func buildCollectorResources(owner client.Object, workflow *v1alpha1.SovereignWorkflow, stepName, image string) []client.Object {
	name := owner.GetName() + "-collector"
	jobName := owner.GetName() + "-collect"
	automount, backoff := true, int32(0)
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.GetNamespace()}, AutomountServiceAccountToken: &automount}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.GetNamespace()}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"artifacts"}, Verbs: []string{"create", "get"}}}}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: owner.GetNamespace()}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: owner.GetNamespace()}}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: owner.GetNamespace()}, Spec: batchv1.JobSpec{BackoffLimit: &backoff, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: name,
		Containers: []corev1.Container{{Name: "collector", Image: image, Args: []string{
			"--namespace", owner.GetNamespace(), "--workflow", workflow.Name, "--attempt", owner.GetName(),
			"--producer-kind", producerKind(owner), "--producer-api-version", v1alpha1.GroupVersion.String(),
			"--result", executionResultPath(owner.GetName()), "--staging", executionStagingPath(owner.GetName()),
			"--artifact-store", "/workspace/.sovereign/artifacts", "--audit-events", executionAuditEventsPath(owner.GetName()),
			"--source-revision", workflow.Spec.DefinitionRevision,
		}, Env: collectorAuditEnv(), VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}}},
		Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: workflow.Status.PvcName}}}},
	}}}}
	_ = stepName
	return []client.Object{serviceAccount, role, binding, job}
}

func producerKind(owner client.Object) string {
	switch owner.(type) {
	case *v1alpha1.AgentRun:
		return "AgentRun"
	case *v1alpha1.UtilityOperation:
		return "UtilityOperation"
	default:
		return "Unknown"
	}
}

func collectorAuditEnv() []corev1.EnvVar {
	var env []corev1.EnvVar
	for _, name := range []string{"SOVEREIGN_AUDIT_DSN", "SOVEREIGN_AUDIT_REQUIRED"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, corev1.EnvVar{Name: name, Value: value})
		}
	}
	return env
}
