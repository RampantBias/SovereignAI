package agentrun

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/domain/state"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
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
	MCPImage       string
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

	if notDeleting, result, err := r.notDeleted(ctx, &run); !notDeleting {
		return result, err
	}
	if run.Status.Phase == "" {
		return ctrl.Result{}, r.setPhase(ctx, &run, v1alpha1.PhasePending, "Initialized", "agent run initialized")
	}
	if state.IsTerminal(run.Status.Phase) {
		released, err := r.reconcileInferenceLeaseRelease(ctx, &run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if !released {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		return r.reconcileWorkspaceWriterRelease(ctx, &run)
	}

	authorized, err := controllers.ValidateDomainAuthority(ctx, r.Client, &run, run.Spec.AttemptRef, v1alpha1.ExecutionKindAgent, run.Spec.WorkflowRef, run.Spec.StepName, run.Spec.Attempt)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, &run, "InvalidStepAttemptAuthority", false)
	}
	if !authorized {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	var resolvedInputs []agentcontract.ArtifactInput
	inputEventID := ""
	if run.Status.PodRef == "" {
		var ready bool
		var invalidReason string
		resolvedInputs, ready, invalidReason, err = r.resolveAgentInputs(ctx, &run)
		if err != nil {
			return ctrl.Result{}, err
		}
		if invalidReason != "" {
			return ctrl.Result{}, r.fail(ctx, &run, "InvalidArtifactInputs", false)
		}
		if !ready {
			return ctrl.Result{RequeueAfter: 2 * time.Second}, r.setPhase(
				ctx,
				&run,
				v1alpha1.PhasePending,
				"WaitingForArtifactInputs",
				"waiting for all declared input artifacts to be accepted",
			)
		}
		if r.Audit != nil {
			evidence, evidenceReady, evidenceInvalidReason, err := controllers.ResolveArtifactEvidence(
				ctx,
				r.Client,
				run.Namespace,
				run.Spec.WorkflowRef,
				run.Spec.Inputs,
			)
			if err != nil {
				return ctrl.Result{}, err
			}
			if evidenceInvalidReason != "" {
				return ctrl.Result{}, r.fail(ctx, &run, "InvalidArtifactInputs", false)
			}
			if !evidenceReady {
				return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
			}
			inputEventID, err = r.appendInputsResolved(ctx, &run, evidence)
			if err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// ensure endpoint
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

	grant, writerState, err := r.ensureWorkspaceWriter(ctx, &run)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch writerState {
	case controllers.WorkspaceWriterBlocked:
		if run.Status.PodRef != "" || run.Status.CollectorJobRef != "" {
			if err := r.deleteWorkspaceWriterWorkloads(ctx, &run); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.interrupt(ctx, &run, "WorkspaceWriterAuthorityNotEstablished", true)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, r.setPhase(ctx, &run, v1alpha1.PhasePending, "WorkspaceWriterBlocked", "another execution unit holds workspace write authority")
	case controllers.WorkspaceWriterLost:
		if err := r.deleteWorkspaceWriterWorkloads(ctx, &run); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.interrupt(ctx, &run, "WorkspaceWriterAuthorityLost", true)
	}

	// ensure workload
	if run.Status.PodRef == "" {
		authorityEventID, err := r.appendExecutionAuthorityEstablished(ctx, &run, grant)
		if err != nil {
			return ctrl.Result{}, err
		}
		if err := r.appendAgentExecutionAuthorized(ctx, &run, authorityEventID, inputEventID); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.ensureWorkload(ctx, &run, endpoint, grant, resolvedInputs); err != nil {
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
		run.Status.FailureReason, run.Status.FailureMessage = agentPodFailure(&pod)
		run.Status.Retryable = true
		return r.startCollection(ctx, &run)
	case corev1.PodRunning:
		return ctrl.Result{}, r.setPhase(ctx, &run, v1alpha1.PhaseRunning, "AgentRunning", "agent is running")
	default:
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
}

func (r *AgentRunReconciler) notDeleted(ctx context.Context, run *v1alpha1.AgentRun) (bool, ctrl.Result, error) {
	if !run.DeletionTimestamp.IsZero() {
		released, err := r.reconcileInferenceLeaseRelease(ctx, run)
		if err != nil {
			return false, ctrl.Result{}, err
		}
		if !released {
			return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
		result, err := r.finalizeWorkspaceWriter(ctx, run)
		return false, result, err
	}
	if !controllerutil.ContainsFinalizer(run, controllers.WorkspaceWriterFinalizer) {
		controllerutil.AddFinalizer(run, controllers.WorkspaceWriterFinalizer)
		if err := r.Update(ctx, run); err != nil {
			return false, ctrl.Result{}, err
		}
	}
	terminating, err := controllers.NamespaceTerminating(ctx, r.Client, run.Namespace)
	if err != nil || terminating {
		return false, ctrl.Result{}, err
	}
	return true, ctrl.Result{}, err
}

func (r *AgentRunReconciler) ensureWorkspaceWriter(ctx context.Context, run *v1alpha1.AgentRun) (controllers.WorkspaceWriterGrant, controllers.WorkspaceWriterState, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return controllers.WorkspaceWriterGrant{}, controllers.WorkspaceWriterBlocked, err
	}
	if workflow.Status.WorkspaceWriterLeaseRef == "" {
		return controllers.WorkspaceWriterGrant{}, controllers.WorkspaceWriterBlocked, nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	grant, state, err := controllers.AcquireWorkspaceWriter(ctx, r.Client, &workflow, workflow.Status.WorkspaceWriterLeaseRef, "AgentRun", run, run.Status.WorkspaceWriterEpoch, now)
	if err != nil || state != controllers.WorkspaceWriterGranted {
		return grant, state, err
	}
	newGrant := run.Status.WorkspaceWriterEpoch == 0
	run.Status.WorkspaceWriterLeaseRef = grant.LeaseName
	run.Status.WorkspaceWriterEpoch = grant.Epoch
	run.Status.WorkspaceWriterReleased = false
	if err := r.updateStatusFields(ctx, run); err != nil {
		return controllers.WorkspaceWriterGrant{}, controllers.WorkspaceWriterBlocked, err
	}
	if newGrant {
		if err := r.appendEvent(ctx, run, "WorkspaceWriterAcquired", "acquire", grant.LeaseName, "granted", fmt.Sprintf("WriterEpoch%d", grant.Epoch)); err != nil {
			return controllers.WorkspaceWriterGrant{}, controllers.WorkspaceWriterBlocked, err
		}
	}
	return grant, state, nil
}

func (r *AgentRunReconciler) workspaceWriterGrant(run *v1alpha1.AgentRun) (controllers.WorkspaceWriterGrant, error) {
	holder, err := controllers.WorkspaceWriterIdentity("AgentRun", run)
	if err != nil {
		return controllers.WorkspaceWriterGrant{}, err
	}
	if run.Status.WorkspaceWriterLeaseRef == "" || run.Status.WorkspaceWriterEpoch < 1 {
		return controllers.WorkspaceWriterGrant{}, fmt.Errorf("AgentRun %s has no workspace writer grant", run.Name)
	}
	return controllers.WorkspaceWriterGrant{LeaseName: run.Status.WorkspaceWriterLeaseRef, HolderIdentity: holder, Epoch: run.Status.WorkspaceWriterEpoch}, nil
}

func (r *AgentRunReconciler) reconcileWorkspaceWriterRelease(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	if run.Status.WorkspaceWriterEpoch < 1 || run.Status.WorkspaceWriterReleased {
		return ctrl.Result{}, nil
	}
	podQuiet, err := controllers.PodWriterQuiescent(ctx, r.Client, run.Namespace, run.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	collectorQuiet, err := controllers.JobWriterQuiescent(ctx, r.Client, run.Namespace, run.Name+"-collect")
	if err != nil {
		return ctrl.Result{}, err
	}
	if !podQuiet || !collectorQuiet {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	grant, err := r.workspaceWriterGrant(run)
	if err != nil {
		return ctrl.Result{}, err
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	if err := controllers.ReleaseWorkspaceWriter(ctx, r.Client, run.Namespace, grant, now); err != nil {
		return ctrl.Result{}, err
	}
	run.Status.WorkspaceWriterReleased = true
	if err := r.updateStatusFields(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.appendEvent(ctx, run, "WorkspaceWriterReleased", "release", grant.LeaseName, "released", fmt.Sprintf("WriterEpoch%d", grant.Epoch))
}

func (r *AgentRunReconciler) deleteWorkspaceWriterWorkloads(ctx context.Context, run *v1alpha1.AgentRun) error {
	for _, object := range []client.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: run.Name, Namespace: run.Namespace}},
		&batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: run.Name + "-collect", Namespace: run.Namespace}},
	} {
		if err := r.Delete(ctx, object); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *AgentRunReconciler) finalizeWorkspaceWriter(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(run, controllers.WorkspaceWriterFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.deleteWorkspaceWriterWorkloads(ctx, run); err != nil {
		return ctrl.Result{}, err
	}
	podQuiet, err := controllers.PodWriterQuiescent(ctx, r.Client, run.Namespace, run.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	collectorQuiet, err := controllers.JobWriterQuiescent(ctx, r.Client, run.Namespace, run.Name+"-collect")
	if err != nil {
		return ctrl.Result{}, err
	}
	if !podQuiet || !collectorQuiet {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if run.Status.WorkspaceWriterEpoch > 0 && !run.Status.WorkspaceWriterReleased {
		grant, err := r.workspaceWriterGrant(run)
		if err != nil {
			return ctrl.Result{}, err
		}
		now := time.Now()
		if r.Now != nil {
			now = r.Now()
		}
		if err := controllers.ReleaseWorkspaceWriter(ctx, r.Client, run.Namespace, grant, now); err != nil {
			return ctrl.Result{}, err
		}
	}
	controllerutil.RemoveFinalizer(run, controllers.WorkspaceWriterFinalizer)
	return ctrl.Result{}, r.Update(ctx, run)
}

func (r *AgentRunReconciler) deletePod(ctx context.Context, run *v1alpha1.AgentRun) error {
	if run.Status.PodRef == "" {
		return nil
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: run.Status.PodRef, Namespace: run.Namespace}}
	return client.IgnoreNotFound(r.Delete(ctx, pod))
}

func (r *AgentRunReconciler) ensureWorkload(ctx context.Context, run *v1alpha1.AgentRun, endpoint string, grant controllers.WorkspaceWriterGrant, inputs []agentcontract.ArtifactInput) error {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return err
	}
	if workflow.UID != run.Spec.WorkflowRef.UID {
		return fmt.Errorf("workflow %q UID does not match AgentRun workflow reference", workflow.Name)
	}
	outputs := make([]agentcontract.OutputObligation, 0, len(run.Spec.OutputContracts))
	for _, output := range run.Spec.OutputContracts {
		outputs = append(outputs, agentcontract.OutputObligation{Name: output.Name, Version: output.Version, Required: true, MediaType: "application/json"})
	}
	inferenceModel := ""
	if run.Spec.Inference != nil {
		inferenceModel = run.Spec.Inference.Model
	}
	input := agentcontract.Input{
		SchemaVersion:     agentcontract.Version,
		WorkflowID:        workflow.Spec.WorkflowID,
		StepName:          run.Spec.StepName,
		Attempt:           run.Spec.Attempt,
		Role:              run.Spec.StepName,
		Responsibility:    run.Spec.Responsibility,
		InferenceModel:    inferenceModel,
		Inputs:            inputs,
		Outputs:           outputs,
		Capabilities:      append([]string(nil), run.Spec.Capabilities...),
		InferenceEndpoint: endpoint,
		MCPServer:         "http://127.0.0.1:8080/mcp",
		WorkspacePath:     "/workspace",
		StagingPath:       controllers.ExecutionStagingPath(run.Name),
		ControlPath:       controllers.ExecutionControlPath(run.Name),
		ResultPath:        controllers.ExecutionResultPath(run.Name),
		AuditEventsPath:   controllers.ExecutionAuditEventsPath(run.Name),
		WorkspaceWrite:    agentcontract.WorkspaceWriteAuthority{LeaseName: grant.LeaseName, HolderIdentity: grant.HolderIdentity, WriterEpoch: grant.Epoch},
	}
	if run.Spec.PriorAttemptRef != nil {
		input.RetryFeedback = &agentcontract.RetryFeedback{
			PreviousAttemptRef: run.Spec.PriorAttemptRef.PreviousAttemptRef,
			Code:               run.Spec.PriorAttemptRef.Code,
			Message:            run.Spec.PriorAttemptRef.Message,
		}
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
	pod := BuildAgentRunPod(run, workflow.Status.PvcName, configName, r.MCPImage, grant)
	if err := controllerutil.SetControllerReference(run, pod, r.Scheme); err != nil {
		return err
	}
	return client.IgnoreAlreadyExists(r.Create(ctx, pod))
}

func (r *AgentRunReconciler) resolveAgentInputs(ctx context.Context, run *v1alpha1.AgentRun) ([]agentcontract.ArtifactInput, bool, string, error) {
	if len(run.Spec.Inputs) == 0 {
		return nil, true, "", nil
	}

	var artifactList v1alpha1.ArtifactList
	if err := r.List(ctx, &artifactList, client.InNamespace(run.Namespace)); err != nil {
		return nil, false, "", fmt.Errorf("list input artifacts: %w", err)
	}

	resolved := make([]agentcontract.ArtifactInput, 0, len(run.Spec.Inputs))
	seen := make(map[string]struct{}, len(run.Spec.Inputs))
	for _, requested := range run.Spec.Inputs {
		if requested.Name == "" {
			return nil, false, "input artifact name is empty", nil
		}
		if _, exists := seen[requested.Name]; exists {
			return nil, false, fmt.Sprintf("input artifact %q is declared more than once", requested.Name), nil
		}
		seen[requested.Name] = struct{}{}

		if requested.ArtifactRef != nil {
			artifact, ready, invalidReason, err := artifacts.ResolvePinnedInput(
				ctx,
				r.Client,
				run.Namespace,
				run.Spec.WorkflowRef,
				requested,
			)
			if err != nil {
				return nil, false, "", err
			}
			if invalidReason != "" {
				return nil, false, invalidReason, nil
			}
			if !ready {
				return nil, false, "", nil
			}
			resolved = append(resolved, agentcontract.ArtifactInput{
				Name:     requested.Name,
				Contract: artifact.Spec.Contract.Name + "/" + artifact.Spec.Contract.Version,
				Digest:   artifact.Spec.Digest,
				Path:     artifact.Spec.Path,
			})
			continue
		}

		matchingContract := make([]v1alpha1.Artifact, 0)
		matchingIdentity := make([]v1alpha1.Artifact, 0)
		for index := range artifactList.Items {
			artifact := artifactList.Items[index]
			if artifact.Spec.WorkflowRef != run.Spec.WorkflowRef ||
				artifact.Spec.Contract.Name != requested.Name {
				continue
			}
			matchingContract = append(matchingContract, artifact)
			if requested.Digest == "" || artifact.Spec.Digest == requested.Digest {
				matchingIdentity = append(matchingIdentity, artifact)
			}
		}

		accepted := make([]v1alpha1.Artifact, 0, len(matchingIdentity))
		pending := false
		rejected := false
		for index := range matchingIdentity {
			artifact := matchingIdentity[index]
			switch {
			case artifacts.ArtifactAccepted(&artifact):
				accepted = append(accepted, artifact)
			case artifact.Status.Phase == v1alpha1.PhaseFailed:
				rejected = true
			default:
				pending = true
			}
		}

		if len(accepted) > 1 {
			return nil, false, fmt.Sprintf("input artifact %q resolves to %d accepted artifacts", requested.Name, len(accepted)), nil
		}
		if len(accepted) == 0 {
			if pending {
				return nil, false, "", nil
			}
			if rejected {
				return nil, false, fmt.Sprintf("input artifact %q was rejected", requested.Name), nil
			}
			if requested.Digest != "" {
				for index := range matchingContract {
					if artifacts.ArtifactAccepted(&matchingContract[index]) {
						return nil, false, fmt.Sprintf("input artifact %q has no accepted artifact with digest %q", requested.Name, requested.Digest), nil
					}
				}
			}
			return nil, false, "", nil
		}

		artifact := accepted[0]
		resolved = append(resolved, agentcontract.ArtifactInput{
			Name:     requested.Name,
			Contract: artifact.Spec.Contract.Name + "/" + artifact.Spec.Contract.Version,
			Digest:   artifact.Spec.Digest,
			Path:     artifact.Spec.Path,
		})
	}

	return resolved, true, "", nil
}

func (r *AgentRunReconciler) startCollection(ctx context.Context, run *v1alpha1.AgentRun) (ctrl.Result, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return ctrl.Result{}, err
	}
	grant, err := r.workspaceWriterGrant(run)
	if err != nil {
		return ctrl.Result{}, err
	}
	objects := controllers.BuildCollectorResources(run, &workflow, run.Spec.StepName, r.collectorImage(), grant)
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
		latest.Status.WorkspaceWriterLeaseRef = run.Status.WorkspaceWriterLeaseRef
		latest.Status.WorkspaceWriterEpoch = run.Status.WorkspaceWriterEpoch
		latest.Status.WorkspaceWriterReleased = run.Status.WorkspaceWriterReleased
		latest.Status.FailureReason = run.Status.FailureReason
		latest.Status.FailureMessage = run.Status.FailureMessage
		latest.Status.Retryable = run.Status.Retryable
		latest.Status.ObservedGeneration = latest.Generation
		if phase == v1alpha1.PhaseRunning && latest.Status.StartedAt == nil {
			latest.Status.StartedAt = &now
		}
		if state.IsTerminal(phase) {
			latest.Status.CompletedAt = &now
		}
		apiMeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{Type: "Ready", Status: state.ConditionStatus(phase), Reason: reason, Message: message, ObservedGeneration: latest.Generation})
		return true
	})
	if err != nil || !changed {
		return err
	}
	return r.appendEvent(ctx, updated, "AgentRun"+string(phase), "reconcile", updated.Name, string(phase), reason)
}

func (r *AgentRunReconciler) fail(ctx context.Context, run *v1alpha1.AgentRun, reason string, retryable bool) error {
	run.Status.FailureReason, run.Status.Retryable = reason, retryable
	message := run.Status.FailureMessage
	if message == "" {
		message = "agent run failed"
	}
	return r.setPhase(ctx, run, v1alpha1.PhaseFailed, reason, message)
}

func (r *AgentRunReconciler) interrupt(ctx context.Context, run *v1alpha1.AgentRun, reason string, retryable bool) error {
	run.Status.FailureReason, run.Status.Retryable = reason, retryable
	return r.setPhase(ctx, run, v1alpha1.PhaseInterrupted, reason, "agent run interrupted")
}

func (r *AgentRunReconciler) updateStatusFields(ctx context.Context, run *v1alpha1.AgentRun) error {
	_, _, err := r.updateStatus(ctx, client.ObjectKeyFromObject(run), func(latest *v1alpha1.AgentRun) bool {
		if latest.Status.InferenceLeaseRef == run.Status.InferenceLeaseRef &&
			latest.Status.WorkspaceWriterLeaseRef == run.Status.WorkspaceWriterLeaseRef &&
			latest.Status.WorkspaceWriterEpoch == run.Status.WorkspaceWriterEpoch &&
			latest.Status.WorkspaceWriterReleased == run.Status.WorkspaceWriterReleased {
			return false
		}
		latest.Status.InferenceLeaseRef = run.Status.InferenceLeaseRef
		latest.Status.WorkspaceWriterLeaseRef = run.Status.WorkspaceWriterLeaseRef
		latest.Status.WorkspaceWriterEpoch = run.Status.WorkspaceWriterEpoch
		latest.Status.WorkspaceWriterReleased = run.Status.WorkspaceWriterReleased
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

func (r *AgentRunReconciler) appendExecutionAuthorityEstablished(ctx context.Context, run *v1alpha1.AgentRun, grant controllers.WorkspaceWriterGrant) (string, error) {
	if r.Audit == nil {
		return "", nil
	}
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return "", fmt.Errorf("read workflow for execution authority evidence: %w", err)
	}
	var attempt v1alpha1.StepAttempt
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Spec.AttemptRef}, &attempt); err != nil {
		return "", fmt.Errorf("read step attempt for execution authority evidence: %w", err)
	}
	var lease coordinationv1.Lease
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: grant.LeaseName}, &lease); err != nil {
		return "", fmt.Errorf("read workspace writer lease for execution authority evidence: %w", err)
	}
	owner := metav1.GetControllerOf(run)
	if workflow.UID == "" || attempt.UID == "" || run.UID == "" || lease.UID == "" {
		return "", fmt.Errorf("execution authority evidence requires workflow, step attempt, primitive, and lease UIDs")
	}
	if workflow.UID != run.Spec.WorkflowRef.UID {
		return "", fmt.Errorf("execution authority evidence workflow UID does not match primitive workflow reference")
	}
	if owner == nil || owner.UID != attempt.UID {
		return "", fmt.Errorf("execution authority evidence step attempt does not control primitive")
	}
	observedHolder := ""
	if lease.Spec.HolderIdentity != nil {
		observedHolder = *lease.Spec.HolderIdentity
	}
	observedEpoch := int32(0)
	if lease.Spec.LeaseTransitions != nil {
		observedEpoch = *lease.Spec.LeaseTransitions
	}
	if observedHolder != grant.HolderIdentity || observedEpoch != grant.Epoch {
		return "", fmt.Errorf("execution authority evidence workspace writer grant is no longer current")
	}
	payload := audit.AuthorityEstablished{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Workflow: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    v1alpha1.GroupVersion.String(),
			Kind:          "SovereignWorkflow",
			Namespace:     workflow.Namespace,
			Name:          workflow.Name,
			UID:           string(workflow.UID),
		},
		StepAttempt: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    v1alpha1.GroupVersion.String(),
			Kind:          "StepAttempt",
			Namespace:     attempt.Namespace,
			Name:          attempt.Name,
			UID:           string(attempt.UID),
		},
		Primitive: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    v1alpha1.GroupVersion.String(),
			Kind:          "AgentRun",
			Namespace:     run.Namespace,
			Name:          run.Name,
			UID:           string(run.UID),
		},
		WorkspaceWrite: &audit.WorkspaceAuthority{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			Lease: audit.ResourceRef{
				SchemaVersion: audit.PayloadSchemaVersionV1,
				APIVersion:    coordinationv1.SchemeGroupVersion.String(),
				Kind:          "Lease",
				Namespace:     lease.Namespace,
				Name:          lease.Name,
				UID:           string(lease.UID),
			},
			HolderIdentity: grant.HolderIdentity,
			WriterEpoch:    grant.Epoch,
			Capabilities:   append([]string(nil), run.Spec.Capabilities...),
		},
		Invariants: []audit.InvariantResult{
			{
				SchemaVersion: audit.PayloadSchemaVersionV1,
				ID:            "workflow-uid-bound",
				Outcome:       "passed",
				Expected:      string(workflow.UID),
				Observed:      string(run.Spec.WorkflowRef.UID),
				Reason:        "the primitive is bound to the admitted workflow",
			},
			{
				SchemaVersion: audit.PayloadSchemaVersionV1,
				ID:            "step-attempt-owns-primitive",
				Outcome:       "passed",
				Expected:      string(attempt.UID),
				Observed:      string(owner.UID),
				Reason:        "the authorized step attempt controls the primitive",
			},
			{
				SchemaVersion: audit.PayloadSchemaVersionV1,
				ID:            "workspace-writer-fence-current",
				Outcome:       "passed",
				Expected:      fmt.Sprintf("%s@%d", grant.HolderIdentity, grant.Epoch),
				Observed:      fmt.Sprintf("%s@%d", observedHolder, observedEpoch),
				Reason:        "the primitive holds the current exclusive writer term",
			},
		},
	}
	event, err := r.appendEventWithDataResult(ctx, run, "ExecutionAuthorityEstablished", "authorize", run.Name, "established", "", payload)
	if err != nil {
		return "", err
	}
	return event.ID, nil
}

func (r *AgentRunReconciler) appendInputsResolved(ctx context.Context, run *v1alpha1.AgentRun, evidence []audit.ArtifactEvidence) (string, error) {
	if r.Audit == nil {
		return "", nil
	}
	payload := controllers.InputsResolvedPayload("AgentRun", run, evidence)
	event, err := r.appendEventWithDataResult(ctx, run, "InputsResolved", "resolve-inputs", run.Name, "resolved", "", payload)
	if err != nil {
		return "", err
	}
	return event.ID, nil
}

func (r *AgentRunReconciler) appendAgentExecutionAuthorized(ctx context.Context, run *v1alpha1.AgentRun, authorityEventID, inputEventID string) error {
	if r.Audit == nil {
		return nil
	}
	decisionInput, err := json.Marshal(struct {
		RunSpec        v1alpha1.AgentRunSpec `json:"runSpec"`
		AuthorityEvent string                `json:"authorityEvent"`
		InputEvent     string                `json:"inputEvent"`
	}{
		RunSpec:        run.Spec,
		AuthorityEvent: authorityEventID,
		InputEvent:     inputEventID,
	})
	if err != nil {
		return fmt.Errorf("marshal agent execution authorization input: %w", err)
	}
	inputDigest := artifactcontract.DigestBytes(decisionInput)
	decisionID := audit.DeterministicID("agent-execution-authorization", string(run.UID), inputDigest)
	inferenceExpected := "not-required"
	inferenceObserved := "not-required"
	inferenceReason := "the primitive does not require an inference lease"
	if run.Spec.Inference != nil {
		inferenceExpected = run.Status.InferenceLeaseRef
		inferenceObserved = run.Status.InferenceLeaseRef
		inferenceReason = "the requested inference lease was admitted before agent execution"
	}
	payload := audit.DecisionEvaluated{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Primitive: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    v1alpha1.GroupVersion.String(),
			Kind:          "AgentRun",
			Namespace:     run.Namespace,
			Name:          run.Name,
			UID:           string(run.UID),
		},
		Decision: audit.DecisionRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			ID:            decisionID,
			Kind:          "controller",
			Revision:      "agentrun-execution/v1",
			InputDigest:   inputDigest,
			Outcome:       "allowed",
		},
		AuthorityEvent: authorityEventID,
		InputEvent:     inputEventID,
		Invariants: []audit.InvariantResult{
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "execution-authority-linked", Outcome: "passed", Expected: authorityEventID, Observed: authorityEventID, Reason: "execution authority was established for this exact primitive"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "execution-inputs-linked", Outcome: "passed", Expected: inputEventID, Observed: inputEventID, Reason: "resolved input evidence was recorded before execution"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "inference-authority-ready", Outcome: "passed", Expected: inferenceExpected, Observed: inferenceObserved, Reason: inferenceReason},
		},
	}
	_, err = r.appendEventWithDataResult(ctx, run, "AgentExecutionAuthorized", "authorize-execution", run.Name, "authorized", "", payload)
	return err
}

func (r *AgentRunReconciler) appendEvent(ctx context.Context, run *v1alpha1.AgentRun, eventType, action, target, outcome, reason string) error {
	return r.appendEventWithData(ctx, run, eventType, action, target, outcome, reason, nil)
}

func (r *AgentRunReconciler) appendEventWithData(ctx context.Context, run *v1alpha1.AgentRun, eventType, action, target, outcome, reason string, data any) error {
	_, err := r.appendEventWithDataResult(ctx, run, eventType, action, target, outcome, reason, data)
	return err
}

func (r *AgentRunReconciler) appendEventWithDataResult(ctx context.Context, run *v1alpha1.AgentRun, eventType, action, target, outcome, reason string, data any) (audit.Event, error) {
	decisionID := ""
	if payload, ok := data.(audit.DecisionEvaluated); ok {
		decisionID = payload.Decision.ID
	}
	return audit.BuildAndAppendControllerEvent(ctx, r.Audit, "agentrun-controller", r.Now, audit.EventOptions{
		Type: eventType, Subject: audit.Subject{Namespace: run.Namespace, Workflow: run.Spec.WorkflowRef.Name, Step: run.Spec.StepName, Attempt: run.Spec.Attempt},
		Action: action, Target: target, Outcome: outcome, Reason: reason, DecisionID: decisionID,
		References: map[string]string{"agentRun": run.Name, "stepAttempt": run.Spec.AttemptRef, "pod": run.Status.PodRef, "collector": run.Status.CollectorJobRef, "lease": run.Status.InferenceLeaseRef, "workspaceWriterLease": run.Status.WorkspaceWriterLeaseRef, "writerEpoch": fmt.Sprint(run.Status.WorkspaceWriterEpoch)},
		Data:       data,
	})
}

func agentPodFailure(pod *corev1.Pod) (string, string) {
	for _, status := range pod.Status.ContainerStatuses {
		if status.Name != "agent" || status.State.Terminated == nil || status.State.Terminated.Message == "" {
			continue
		}
		var detail agentcontract.ResultError
		if err := json.Unmarshal([]byte(status.State.Terminated.Message), &detail); err != nil {
			break
		}
		feedback := agentcontract.RetryFeedback{
			PreviousAttemptRef: pod.Name,
			Code:               detail.Code,
			Message:            agentcontract.SanitizeRetryFeedbackMessage(detail.Message),
		}
		if err := feedback.Validate(); err == nil {
			return feedback.Code, feedback.Message
		}
		break
	}
	return "AgentPodFailed", "agent pod failed without a valid structured diagnostic"
}
