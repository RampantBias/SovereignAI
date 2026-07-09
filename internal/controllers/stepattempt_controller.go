package controllers

import (
	"context"
	"encoding/json"
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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

type StepAttemptReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Audit          audit.Recorder
	Now            func() time.Time
	CollectorImage string
}

func (r *StepAttemptReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.StepAttempt{}).
		Owns(&corev1.Pod{}).
		Owns(&batchv1.Job{}).
		Owns(&v1alpha1.InferenceLease{}).
		Owns(&v1alpha1.HumanSession{}).
		Owns(&v1alpha1.ValidationRun{}).
		Complete(r)
}

func (r *StepAttemptReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	// Get step attempt CRD
	var attempt v1alpha1.StepAttempt
	if err := r.Get(ctx, request.NamespacedName, &attempt); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Check for deletion
	if !attempt.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// Transition to pending
	if attempt.Status.Phase == "" {
		return ctrl.Result{}, r.setPhase(ctx, &attempt, v1alpha1.PhasePending, "Initialized", "attempt initialized")
	}
	if terminalAttempt(attempt.Status.Phase) {
		return ctrl.Result{}, nil
	}

	// Reconcile each step type separately
	switch attempt.Spec.Kind {
	case v1alpha1.ExecutionKindAgent:
		return r.reconcileAgent(ctx, &attempt)
	case v1alpha1.ExecutionKindUtility:
		return r.reconcileUtility(ctx, &attempt)
	case v1alpha1.ExecutionKindHumanGate:
		return ctrl.Result{}, r.setPhase(ctx, &attempt, v1alpha1.PhaseAwaitingApproval, "HumanApprovalRequired", "waiting for an authorized human decision")
	case v1alpha1.ExecutionKindValidation:
		return r.reconcileValidation(ctx, &attempt)
	default:
		return ctrl.Result{}, r.fail(ctx, &attempt, "UnsupportedKind", false)
	}
}

func (r *StepAttemptReconciler) reconcileAgent(ctx context.Context, attempt *v1alpha1.StepAttempt) (ctrl.Result, error) {
	endpoint := ""

	// Verify lease
	if attempt.Spec.Inference != nil {
		lease, err := r.ensureLease(ctx, attempt)
		if err != nil {
			return ctrl.Result{}, err
		}

		// Evaluate lease phase
		switch lease.Status.Phase {
		case v1alpha1.PhaseInterrupted:
			// Must delete agent pod to avoid concurrent attempts running after inference interrupt
			if err := r.deleteAgentPod(ctx, attempt); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.interrupt(ctx, attempt, lease.Status.Reason, true)
		case v1alpha1.PhaseFailed:
			return ctrl.Result{}, r.fail(ctx, attempt, "InferenceAdmissionFailed", true)
		case v1alpha1.PhaseRunning:
			endpoint = lease.Status.EndpointURL
		default:
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	// Create or observe agent pod
	if attempt.Status.PodRef == "" {
		if err := r.ensureAgentWorkload(ctx, attempt, endpoint); err != nil {
			return ctrl.Result{}, err
		}
		attempt.Status.PodRef = attempt.Name
		return ctrl.Result{}, r.setPhase(ctx, attempt, v1alpha1.PhasePreparing, "PodCreated", "agent pod created")
	}

	// Get agent pod
	var pod corev1.Pod
	if err := r.Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Status.PodRef}, &pod); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.interrupt(ctx, attempt, "AgentPodLost", true)
		}
		return ctrl.Result{}, err
	}

	// Evaluate agent phase
	switch pod.Status.Phase {
	case corev1.PodSucceeded:
		if len(attempt.Spec.OutputContracts) > 0 {
			return r.reconcileCollection(ctx, attempt)
		}
		return ctrl.Result{}, r.setPhase(ctx, attempt, v1alpha1.PhaseSucceeded, "AgentCompleted", "agent contract completed")
	case corev1.PodFailed:
		return ctrl.Result{}, r.fail(ctx, attempt, "AgentPodFailed", true)
	case corev1.PodRunning:
		return ctrl.Result{}, r.setPhase(ctx, attempt, v1alpha1.PhaseRunning, "AgentRunning", "agent is running")
	default:
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
}

func (r *StepAttemptReconciler) deleteAgentPod(ctx context.Context, attempt *v1alpha1.StepAttempt) error {
	if attempt.Status.PodRef == "" {
		return nil
	}

	var pod corev1.Pod
	key := types.NamespacedName{
		Namespace: attempt.Namespace,
		Name:      attempt.Status.PodRef,
	}
	if err := r.Get(ctx, key, &pod); err != nil {
		return client.IgnoreNotFound(err)
	}

	return client.IgnoreNotFound(r.Delete(ctx, &pod))
}

// Reconciles artifacts created upon agent success
func (r *StepAttemptReconciler) reconcileCollection(ctx context.Context, attempt *v1alpha1.StepAttempt) (ctrl.Result, error) {
	if attempt.Status.JobRef == "" {
		var workflow v1alpha1.SovereignWorkflow
		if err := r.Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Spec.WorkflowRef}, &workflow); err != nil {
			return ctrl.Result{}, err
		}
		objects := buildCollectorResources(attempt, &workflow, r.collectorImage())
		for _, object := range objects {
			if err := controllerutil.SetControllerReference(attempt, object, r.Scheme); err != nil {
				return ctrl.Result{}, err
			}
			if err := r.Create(ctx, object); err != nil && !apierrors.IsAlreadyExists(err) {
				return ctrl.Result{}, err
			}
		}
		attempt.Status.JobRef = attempt.Name + "-collect"
		return ctrl.Result{}, r.setPhase(ctx, attempt, v1alpha1.PhaseCollecting, "CollectorCreated", "artifact collector job created")
	}
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Status.JobRef}, &job); err != nil {
		return ctrl.Result{}, err
	}
	if job.Status.Succeeded > 0 {
		attempt.Status.ResultRef = job.Name
		return ctrl.Result{}, r.setPhase(ctx, attempt, v1alpha1.PhaseSucceeded, "ArtifactsCollected", "result contract and artifacts accepted")
	}
	if job.Status.Failed > 0 {
		return ctrl.Result{}, r.fail(ctx, attempt, "ArtifactCollectionFailed", false)
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *StepAttemptReconciler) collectorImage() string {
	if r.CollectorImage != "" {
		return r.CollectorImage
	}
	return "sovereign-artifact-collector:dev"
}

func (r *StepAttemptReconciler) reconcileUtility(ctx context.Context, attempt *v1alpha1.StepAttempt) (ctrl.Result, error) {
	if attempt.Status.JobRef == "" {
		job := buildUtilityJob(attempt)
		if err := controllerutil.SetControllerReference(attempt, job, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
		attempt.Status.JobRef = job.Name
		return ctrl.Result{}, r.setPhase(ctx, attempt, v1alpha1.PhasePreparing, "JobCreated", "utility job created")
	}
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Status.JobRef}, &job); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if job.Status.Succeeded > 0 {
		return ctrl.Result{}, r.setPhase(ctx, attempt, v1alpha1.PhaseSucceeded, "UtilityCompleted", "utility job completed")
	}
	if job.Status.Failed > 0 {
		return ctrl.Result{}, r.fail(ctx, attempt, "UtilityJobFailed", true)
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// Reconciles the Validation step status (ephemeral testing)
func (r *StepAttemptReconciler) reconcileValidation(ctx context.Context, attempt *v1alpha1.StepAttempt) (ctrl.Result, error) {
	name := attempt.Name + "-validation"
	var run v1alpha1.ValidationRun
	err := r.Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: name}, &run)
	if apierrors.IsNotFound(err) {
		run = v1alpha1.ValidationRun{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: attempt.Namespace}, Spec: v1alpha1.ValidationRunSpec{WorkflowRef: attempt.Spec.WorkflowRef, Provider: "argocd-kustomize"}}
		if err := controllerutil.SetControllerReference(attempt, &run, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.setPhase(ctx, attempt, v1alpha1.PhaseValidating, "ValidationCreated", "validation run created")
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	if run.Status.Phase == v1alpha1.PhaseSucceeded {
		return ctrl.Result{}, r.setPhase(ctx, attempt, v1alpha1.PhaseSucceeded, "ValidationSucceeded", "validation succeeded")
	}
	if run.Status.Phase == v1alpha1.PhaseFailed {
		return ctrl.Result{}, r.fail(ctx, attempt, "ValidationFailed", false)
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// Verifies or creates new inference lease based on inference request, attempt, and workflow
func (r *StepAttemptReconciler) ensureLease(ctx context.Context, attempt *v1alpha1.StepAttempt) (*v1alpha1.InferenceLease, error) {
	name := attempt.Spec.InferenceLeaseRef
	var lease v1alpha1.InferenceLease
	err := r.Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: name}, &lease)
	if err == nil {
		if err := r.appendInferenceLeaseRequested(ctx, attempt, lease.Name); err != nil {
			return nil, err
		}
		return &lease, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Spec.WorkflowRef}, &workflow); err != nil {
		return nil, err
	}
	lease = v1alpha1.InferenceLease{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: attempt.Namespace},
		Spec: v1alpha1.InferenceLeaseSpec{
			WorkflowRef:       workflow.Name,
			AttemptRef:        attempt.Name,
			ProjectRef:        workflow.Spec.ProjectName,
			Tenant:            workflow.Spec.ProjectName,
			Classification:    workflow.Spec.Classification,
			SharingScope:      attempt.Spec.Inference.SharingScope,
			Model:             attempt.Spec.Inference.Model,
			ModelRevision:     attempt.Spec.Inference.ModelRevision,
			EstimatedKVRAMMiB: attempt.Spec.Inference.EstimatedKVRAMMiB,
			Priority:          attempt.Spec.Inference.Priority,
			Evictable:         attempt.Spec.Inference.Evictable,
		},
	}
	if err := controllerutil.SetControllerReference(attempt, &lease, r.Scheme); err != nil {
		return nil, err
	}
	if err := r.Create(ctx, &lease); err != nil {
		return nil, err
	}
	if err := r.appendInferenceLeaseRequested(ctx, attempt, lease.Name); err != nil {
		return nil, err
	}
	return &lease, nil
}

func (r *StepAttemptReconciler) appendInferenceLeaseRequested(ctx context.Context, attempt *v1alpha1.StepAttempt, leaseName string) error {
	return r.appendAttemptEvent(ctx, attempt, "InferenceLeaseRequested", "request", leaseName, "requested", "", map[string]string{"lease": leaseName}, nil)
}

func (r *StepAttemptReconciler) ensureAgentWorkload(ctx context.Context, attempt *v1alpha1.StepAttempt, endpoint string) error {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: attempt.Namespace, Name: attempt.Spec.WorkflowRef}, &workflow); err != nil {
		return err
	}

	// Map output contracts to output obligations
	var outputObligations []agentcontract.OutputObligation
	for _, output := range attempt.Spec.OutputContracts {
		outputObligations = append(outputObligations,
			agentcontract.OutputObligation{Name: output.Name, Version: output.Version, Required: true})
	}

	// Generate input contract and write it to a configmap
	input := agentcontract.Input{
		SchemaVersion:     agentcontract.Version,
		WorkflowID:        workflow.Spec.WorkflowID,
		StepName:          attempt.Spec.StepName,
		Attempt:           attempt.Spec.Attempt,
		Role:              attempt.Spec.StepName,
		Goal:              attempt.Spec.Goal,
		Capabilities:      append([]string(nil), attempt.Spec.Capabilities...),
		Outputs:           outputObligations,
		InferenceEndpoint: endpoint,
		MCPServer:         "https://sovereign-mcp.sovereign-orchestrator-system.svc",
		WorkspacePath:     "/workspace",
		StagingPath:       "/workspace/attempts/" + attempt.Name + "/staging",
	}
	data, err := json.Marshal(input)
	if err != nil {
		return err
	}
	configName := attempt.Name + "-input"
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configName, Namespace: attempt.Namespace}, Data: map[string]string{"input.json": string(data)}}
	if err := controllerutil.SetControllerReference(attempt, config, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, config); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	pod := buildAgentPod(attempt, workflow.Status.PvcName, configName)
	if err := controllerutil.SetControllerReference(attempt, pod, r.Scheme); err != nil {
		return err
	}
	return client.IgnoreAlreadyExists(r.Create(ctx, pod))
}

func buildAgentPod(attempt *v1alpha1.StepAttempt, pvcName, configName string) *corev1.Pod {
	if pvcName == "" {
		pvcName = attempt.Spec.WorkflowRef + "-workspace"
	}

	// Security configuration
	automount := false // Prevents SA token from being mounted
	nonRoot := true
	readOnly := true
	allowPrivilegeEscalation := false
	runAsUser := int64(65532)

	executable, _ := json.Marshal(attempt.Spec.Executable)
	resultPath := "/workspace/attempts/" + attempt.Name + "/control/result.json"
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: attempt.Name, Namespace: attempt.Namespace, Labels: map[string]string{LabelWorkflow: attempt.Labels[LabelWorkflow], LabelStep: attempt.Spec.StepName, "sovereign-ai.io/attempt": attempt.Name}},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: &automount,
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: &nonRoot, RunAsUser: &runAsUser},
			Containers: []corev1.Container{{
				Name: "agent", Image: attempt.Spec.Image, Command: []string{"/agent-wrapper"},
				Args:            []string{"--input", "/control/input.json", "--result", resultPath},
				Env:             []corev1.EnvVar{{Name: "SOVEREIGN_AGENT_EXECUTABLE", Value: string(executable)}},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation, ReadOnlyRootFilesystem: &readOnly, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "input", MountPath: "/control", ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{
				{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}}},
				{Name: "input", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}},
			},
		},
	}
}

func buildUtilityJob(attempt *v1alpha1.StepAttempt) *batchv1.Job {
	automount := false
	backoff := int32(0)
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      attempt.Name,
			Namespace: attempt.Namespace,
			Labels: map[string]string{
				LabelWorkflow: attempt.Labels[LabelWorkflow],
				LabelStep:     attempt.Spec.StepName}},
		Spec: batchv1.JobSpec{
			BackoffLimit: &backoff,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automount,
					Containers: []corev1.Container{{
						Name:    "utility",
						Image:   attempt.Spec.Image,
						Command: attempt.Spec.Executable,
						VolumeMounts: []corev1.VolumeMount{{
							Name:      "workspace",
							MountPath: "/workspace"}}}},
					Volumes: []corev1.Volume{{
						Name: "workspace",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{
								ClaimName: attempt.Spec.WorkflowRef + "-workspace"}}}},
				}}},
	}
}

func buildCollectorResources(attempt *v1alpha1.StepAttempt, workflow *v1alpha1.SovereignWorkflow, image string) []client.Object {
	name := attempt.Name + "-collector"
	jobName := attempt.Name + "-collect"
	automount := true
	backoff := int32(0)
	resultPath := "/workspace/attempts/" + attempt.Name + "/control/result.json"
	stagingPath := "/workspace/attempts/" + attempt.Name + "/staging"
	serviceAccount := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: attempt.Namespace}, AutomountServiceAccountToken: &automount}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: attempt.Namespace}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"artifacts"}, Verbs: []string{"create", "get"}}}}
	binding := &rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: attempt.Namespace}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: name, Namespace: attempt.Namespace}}, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}}
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: jobName, Namespace: attempt.Namespace}, Spec: batchv1.JobSpec{BackoffLimit: &backoff, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever, ServiceAccountName: name,
		Containers: []corev1.Container{{Name: "collector", Image: image, Args: []string{
			"--namespace", attempt.Namespace, "--workflow", workflow.Name, "--attempt", attempt.Name,
			"--result", resultPath, "--staging", stagingPath, "--artifact-store", "/workspace/.sovereign/artifacts",
			"--source-revision", workflow.Spec.DefinitionRevision,
		}, VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}}},
		Volumes: []corev1.Volume{{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: workflow.Status.PvcName}}}},
	}}}}
	return []client.Object{serviceAccount, role, binding, job}
}

func (r *StepAttemptReconciler) setPhase(ctx context.Context, attempt *v1alpha1.StepAttempt, phase v1alpha1.ResourcePhase, reason, message string) error {
	if attempt.Status.Phase == phase {
		condition := apiMeta.FindStatusCondition(attempt.Status.Conditions, "Ready")
		if condition != nil && condition.Reason == reason && condition.Message == message {
			return nil
		}
	}
	now := metav1.Now()
	if r.Now != nil {
		now = metav1.NewTime(r.Now())
	}
	attempt.Status.Phase = phase
	attempt.Status.ObservedGeneration = attempt.Generation
	if phase == v1alpha1.PhaseRunning && attempt.Status.StartedAt == nil {
		attempt.Status.StartedAt = &now
	}
	if terminalAttempt(phase) {
		attempt.Status.CompletedAt = &now
	}
	apiMeta.SetStatusCondition(&attempt.Status.Conditions, metav1.Condition{Type: "Ready", Status: conditionStatus(phase), Reason: reason, Message: message, ObservedGeneration: attempt.Generation})
	if err := r.Status().Update(ctx, attempt); err != nil {
		return err
	}
	return r.appendPhaseEvent(ctx, attempt, phase, reason)
}

func (r *StepAttemptReconciler) fail(ctx context.Context, attempt *v1alpha1.StepAttempt, reason string, retryable bool) error {
	attempt.Status.FailureReason = reason
	attempt.Status.Retryable = retryable
	return r.setPhase(ctx, attempt, v1alpha1.PhaseFailed, reason, "attempt failed")
}

func (r *StepAttemptReconciler) interrupt(ctx context.Context, attempt *v1alpha1.StepAttempt, reason string, retryable bool) error {
	attempt.Status.FailureReason = reason
	attempt.Status.Retryable = retryable
	return r.setPhase(ctx, attempt, v1alpha1.PhaseInterrupted, reason, "attempt interrupted")
}

func (r *StepAttemptReconciler) appendPhaseEvent(ctx context.Context, attempt *v1alpha1.StepAttempt, phase v1alpha1.ResourcePhase, reason string) error {
	eventType, action, outcome := phaseEvent(attempt.Spec.Kind, phase, reason)
	if eventType == "" {
		return nil
	}
	references := make(map[string]string)
	if attempt.Status.PodRef != "" {
		references["pod"] = attempt.Status.PodRef
	}
	if attempt.Status.JobRef != "" {
		references["job"] = attempt.Status.JobRef
	}
	if attempt.Spec.InferenceLeaseRef != "" {
		references["lease"] = attempt.Spec.InferenceLeaseRef
	}
	if attempt.Status.ResultRef != "" {
		references["result"] = attempt.Status.ResultRef
	}
	return r.appendAttemptEvent(ctx, attempt, eventType, action, attempt.Name, outcome, reason, references, nil)
}

func (r *StepAttemptReconciler) appendAttemptEvent(ctx context.Context, attempt *v1alpha1.StepAttempt, eventType, action, target, outcome, reason string, references map[string]string, data any) error {
	if r.Audit == nil {
		return nil
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	return audit.AppendEvent(ctx, r.Audit, audit.EventOptions{
		Source:     "stepattempt-controller",
		Type:       eventType,
		OccurredAt: now,
		Actor:      audit.Actor{Kind: "Controller", ID: "stepattempt-controller"},
		Subject: audit.Subject{
			Namespace: attempt.Namespace,
			Workflow:  attempt.Spec.WorkflowRef,
			Step:      attempt.Spec.StepName,
			Attempt:   attempt.Spec.Attempt,
		},
		Action:        action,
		Target:        target,
		Outcome:       outcome,
		Reason:        reason,
		CorrelationID: attempt.Spec.WorkflowRef,
		References:    references,
		Data:          data,
	})
}

func phaseEvent(kind v1alpha1.ExecutionKind, phase v1alpha1.ResourcePhase, reason string) (eventType, action, outcome string) {
	switch phase {
	case v1alpha1.PhasePreparing:
		if reason == "JobCreated" {
			return "UtilityJobCreated", "create", "created"
		}
		if reason == "PodCreated" {
			return "StepAttemptPodCreated", "create", "created"
		}
	case v1alpha1.PhaseRunning:
		return "StepAttemptStarted", "start", "started"
	case v1alpha1.PhaseAwaitingApproval:
		return "HumanApprovalRequested", "request", "awaitingApproval"
	case v1alpha1.PhaseValidating:
		return "ValidationRunCreated", "create", "created"
	case v1alpha1.PhaseSucceeded:
		if kind == v1alpha1.ExecutionKindUtility {
			return "UtilityJobSucceeded", "complete", "succeeded"
		}
		if kind == v1alpha1.ExecutionKindValidation {
			return "ValidationRunReady", "complete", "succeeded"
		}
		return "StepAttemptSucceeded", "complete", "succeeded"
	case v1alpha1.PhaseFailed:
		if kind == v1alpha1.ExecutionKindUtility {
			return "UtilityJobFailed", "complete", "failed"
		}
		if kind == v1alpha1.ExecutionKindValidation {
			return "ValidationRunFailed", "complete", "failed"
		}
		return "StepAttemptFailed", "complete", "failed"
	case v1alpha1.PhaseInterrupted:
		return "StepAttemptInterrupted", "interrupt", "interrupted"
	}
	return "", "", ""
}

func terminalAttempt(phase v1alpha1.ResourcePhase) bool {
	return phase == v1alpha1.PhaseSucceeded || phase == v1alpha1.PhaseFailed || phase == v1alpha1.PhaseCancelled || phase == v1alpha1.PhaseInterrupted
}

func conditionStatus(phase v1alpha1.ResourcePhase) metav1.ConditionStatus {
	if phase == v1alpha1.PhaseSucceeded {
		return metav1.ConditionTrue
	}
	return metav1.ConditionFalse
}
