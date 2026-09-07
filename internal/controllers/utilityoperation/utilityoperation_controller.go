package utilityoperation

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/domain/state"
	policyengine "github.com/SovereignAI/internal/policy"
	"github.com/SovereignAI/internal/utility"
	"github.com/SovereignAI/internal/utilitycontract"
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

// UtilityOperationReconciler is the authority owner for deterministic
// platform operations. StepAttempt observes this resource but cannot execute it.
type UtilityOperationReconciler struct {
	client.Client
	Reader         client.Reader
	Scheme         *runtime.Scheme
	Audit          audit.Recorder
	Now            func() time.Time
	CollectorImage string
	UtilityImage   string
	Policy         policyengine.Evaluator
}

func (r *UtilityOperationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Read terminal pod diagnostics directly; the Job watch can precede the
	// pod informer update, and collection must not persist a stale fallback.
	if r.Reader == nil {
		r.Reader = mgr.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.UtilityOperation{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}

func (r *UtilityOperationReconciler) Reconcile(ctx context.Context, request ctrl.Request) (ctrl.Result, error) {
	var operation v1alpha1.UtilityOperation
	if err := r.Get(ctx, request.NamespacedName, &operation); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if isDeleting, result, err := r.notDeleted(ctx, &operation); !isDeleting {
		return result, err
	}

	if operation.Status.Phase == "" {
		return ctrl.Result{}, r.setPhase(ctx, &operation, v1alpha1.PhasePending, "Initialized", "utility operation initialized")
	}
	if state.IsTerminal(operation.Status.Phase) {
		return r.reconcileWorkspaceWriterRelease(ctx, &operation)
	}

	authorized, err := controllers.ValidateDomainAuthority(ctx, r.Client, &operation, operation.Spec.AttemptRef, v1alpha1.ExecutionKindUtility, operation.Spec.WorkflowRef, operation.Spec.StepName, operation.Spec.Attempt)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, &operation, "InvalidStepAttemptAuthority", false)
	}
	if !authorized {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	if denied, result, err := r.ensureValidOperation(ctx, &operation); denied {
		return result, err
	}

	grant, writerState, err := r.ensureWorkspaceWriter(ctx, &operation)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch writerState {
	case controllers.WorkspaceWriterBlocked:
		if operation.Status.JobRef != "" || operation.Status.CollectorJobRef != "" {
			if err := r.deleteWorkspaceWriterWorkloads(ctx, &operation); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.interrupt(ctx, &operation, "WorkspaceWriterAuthorityNotEstablished", true)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, r.setPhase(ctx, &operation, v1alpha1.PhasePending, "WorkspaceWriterBlocked", "another execution unit holds workspace write authority")
	case controllers.WorkspaceWriterLost:
		if err := r.deleteWorkspaceWriterWorkloads(ctx, &operation); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.interrupt(ctx, &operation, "WorkspaceWriterAuthorityLost", true)
	}

	if operation.Status.JobRef == "" {
		if err := r.ensureWorkload(ctx, &operation, grant); err != nil {
			return ctrl.Result{}, err
		}
		operation.Status.JobRef = operation.Name
		return ctrl.Result{}, r.setPhase(ctx, &operation, v1alpha1.PhasePreparing, "JobCreated", "utility job created")
	}
	if operation.Status.CollectorJobRef != "" {
		return r.reconcileCollection(ctx, &operation)
	}
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: operation.Status.JobRef}, &job); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.interrupt(ctx, &operation, "UtilityJobLost", true)
		}
		return ctrl.Result{}, err
	}
	if job.Status.Succeeded > 0 {
		return r.startCollection(ctx, &operation)
	}
	if job.Status.Failed > 0 {
		if err := r.recordJobFailure(ctx, &operation, &job); err != nil {
			return ctrl.Result{}, err
		}
		return r.startCollection(ctx, &operation)
	}
	if job.Status.Active > 0 {
		return ctrl.Result{}, r.setPhase(ctx, &operation, v1alpha1.PhaseRunning, "UtilityRunning", "utility operation is running")
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *UtilityOperationReconciler) notDeleted(ctx context.Context, operation *v1alpha1.UtilityOperation) (bool, ctrl.Result, error) {
	if !operation.DeletionTimestamp.IsZero() {
		result, err := r.finalizeWorkspaceWriter(ctx, operation)
		return false, result, err
	}
	if !controllerutil.ContainsFinalizer(operation, controllers.WorkspaceWriterFinalizer) {
		controllerutil.AddFinalizer(operation, controllers.WorkspaceWriterFinalizer)
		if err := r.Update(ctx, operation); err != nil {
			return false, ctrl.Result{}, err
		}
	}
	terminating, err := controllers.NamespaceTerminating(ctx, r.Client, operation.Namespace)
	if err != nil || terminating {
		return false, ctrl.Result{}, err
	}
	return true, ctrl.Result{}, err
}

func (r *UtilityOperationReconciler) ensureValidOperation(ctx context.Context, operation *v1alpha1.UtilityOperation) (bool, ctrl.Result, error) {
	if !utility.IsSupportedOperation(operation.Spec.Operation.Name) {
		return true, ctrl.Result{}, r.fail(ctx, operation, "UnsupportedUtilityOperation", false)
	}
	if operation.Spec.Approval != nil || operation.Spec.Operation.Name == utility.OperationGitMergeRequest {
		workflow, _, err := r.resolveContext(ctx, operation)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		if err := controllers.ValidateOperationApproval(ctx, r.Client, r.Audit, operation, workflow); err != nil {
			return true, ctrl.Result{}, err
		}
	}
	if operation.Status.PolicyDecisionID == "" {
		allowed, decisionID, reason, err := r.admit(ctx, operation)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		operation.Status.PolicyDecisionID = decisionID
		payload, err := utilityAdmissionDecision(operation, decisionID, allowed, reason)
		if err != nil {
			return true, ctrl.Result{}, err
		}
		if _, err := r.appendEventWithDataResult(ctx, operation, map[bool]string{true: "UtilityOperationAdmitted", false: "UtilityOperationRejected"}[allowed], "admit", operation.Spec.Operation.Name, map[bool]string{true: "admitted", false: "rejected"}[allowed], reason, payload); err != nil {
			return true, ctrl.Result{}, err
		}
		if !allowed {
			return true, ctrl.Result{}, r.fail(ctx, operation, "UtilityOperationDenied", false)
		}
		if err := r.recordPolicyDecision(ctx, operation); err != nil {
			return true, ctrl.Result{}, err
		}
	}
	return false, ctrl.Result{}, nil
}

func (r *UtilityOperationReconciler) ensureWorkspaceWriter(ctx context.Context, operation *v1alpha1.UtilityOperation) (controllers.WorkspaceWriterGrant, controllers.WorkspaceWriterState, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: operation.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return controllers.WorkspaceWriterGrant{}, controllers.WorkspaceWriterBlocked, err
	}
	if workflow.Status.WorkspaceWriterLeaseRef == "" {
		return controllers.WorkspaceWriterGrant{}, controllers.WorkspaceWriterBlocked, nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	grant, state, err := controllers.AcquireWorkspaceWriter(ctx, r.Client, &workflow, workflow.Status.WorkspaceWriterLeaseRef, "UtilityOperation", operation, operation.Status.WorkspaceWriterEpoch, now)
	if err != nil || state != controllers.WorkspaceWriterGranted {
		return grant, state, err
	}
	newGrant := operation.Status.WorkspaceWriterEpoch == 0
	operation.Status.WorkspaceWriterLeaseRef = grant.LeaseName
	operation.Status.WorkspaceWriterEpoch = grant.Epoch
	operation.Status.WorkspaceWriterReleased = false
	if err := r.recordWorkspaceWriterStatus(ctx, operation); err != nil {
		return controllers.WorkspaceWriterGrant{}, controllers.WorkspaceWriterBlocked, err
	}
	if newGrant {
		if err := r.appendEvent(ctx, operation, "WorkspaceWriterAcquired", "acquire", grant.LeaseName, "granted", fmt.Sprintf("WriterEpoch%d", grant.Epoch), nil); err != nil {
			return controllers.WorkspaceWriterGrant{}, controllers.WorkspaceWriterBlocked, err
		}
	}
	return grant, state, nil
}

func (r *UtilityOperationReconciler) workspaceWriterGrant(operation *v1alpha1.UtilityOperation) (controllers.WorkspaceWriterGrant, error) {
	holder, err := controllers.WorkspaceWriterIdentity("UtilityOperation", operation)
	if err != nil {
		return controllers.WorkspaceWriterGrant{}, err
	}
	if operation.Status.WorkspaceWriterLeaseRef == "" || operation.Status.WorkspaceWriterEpoch < 1 {
		return controllers.WorkspaceWriterGrant{}, fmt.Errorf("UtilityOperation %s has no workspace writer grant", operation.Name)
	}
	return controllers.WorkspaceWriterGrant{LeaseName: operation.Status.WorkspaceWriterLeaseRef, HolderIdentity: holder, Epoch: operation.Status.WorkspaceWriterEpoch}, nil
}

func (r *UtilityOperationReconciler) recordWorkspaceWriterStatus(ctx context.Context, operation *v1alpha1.UtilityOperation) error {
	_, _, err := r.updateStatus(ctx, client.ObjectKeyFromObject(operation), func(latest *v1alpha1.UtilityOperation) bool {
		if latest.Status.WorkspaceWriterLeaseRef == operation.Status.WorkspaceWriterLeaseRef &&
			latest.Status.WorkspaceWriterEpoch == operation.Status.WorkspaceWriterEpoch &&
			latest.Status.WorkspaceWriterReleased == operation.Status.WorkspaceWriterReleased {
			return false
		}
		latest.Status.WorkspaceWriterLeaseRef = operation.Status.WorkspaceWriterLeaseRef
		latest.Status.WorkspaceWriterEpoch = operation.Status.WorkspaceWriterEpoch
		latest.Status.WorkspaceWriterReleased = operation.Status.WorkspaceWriterReleased
		latest.Status.ObservedGeneration = latest.Generation
		return true
	})
	return err
}

func (r *UtilityOperationReconciler) reconcileWorkspaceWriterRelease(ctx context.Context, operation *v1alpha1.UtilityOperation) (ctrl.Result, error) {
	if operation.Status.WorkspaceWriterEpoch < 1 || operation.Status.WorkspaceWriterReleased {
		return ctrl.Result{}, nil
	}
	jobQuiet, err := controllers.JobWriterQuiescent(ctx, r.Client, operation.Namespace, operation.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	collectorQuiet, err := controllers.JobWriterQuiescent(ctx, r.Client, operation.Namespace, operation.Name+"-collect")
	if err != nil {
		return ctrl.Result{}, err
	}
	if !jobQuiet || !collectorQuiet {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	grant, err := r.workspaceWriterGrant(operation)
	if err != nil {
		return ctrl.Result{}, err
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	if err := controllers.ReleaseWorkspaceWriter(ctx, r.Client, operation.Namespace, grant, now); err != nil {
		return ctrl.Result{}, err
	}
	operation.Status.WorkspaceWriterReleased = true
	if err := r.recordWorkspaceWriterStatus(ctx, operation); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, r.appendEvent(ctx, operation, "WorkspaceWriterReleased", "release", grant.LeaseName, "released", fmt.Sprintf("WriterEpoch%d", grant.Epoch), nil)
}

func (r *UtilityOperationReconciler) deleteWorkspaceWriterWorkloads(ctx context.Context, operation *v1alpha1.UtilityOperation) error {
	for _, name := range []string{operation.Name, operation.Name + "-collect"} {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: operation.Namespace}}
		if err := r.Delete(ctx, job); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *UtilityOperationReconciler) finalizeWorkspaceWriter(ctx context.Context, operation *v1alpha1.UtilityOperation) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(operation, controllers.WorkspaceWriterFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.deleteWorkspaceWriterWorkloads(ctx, operation); err != nil {
		return ctrl.Result{}, err
	}
	jobQuiet, err := controllers.JobWriterQuiescent(ctx, r.Client, operation.Namespace, operation.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	collectorQuiet, err := controllers.JobWriterQuiescent(ctx, r.Client, operation.Namespace, operation.Name+"-collect")
	if err != nil {
		return ctrl.Result{}, err
	}
	if !jobQuiet || !collectorQuiet {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if operation.Status.WorkspaceWriterEpoch > 0 && !operation.Status.WorkspaceWriterReleased {
		grant, err := r.workspaceWriterGrant(operation)
		if err != nil {
			return ctrl.Result{}, err
		}
		now := time.Now()
		if r.Now != nil {
			now = r.Now()
		}
		if err := controllers.ReleaseWorkspaceWriter(ctx, r.Client, operation.Namespace, grant, now); err != nil {
			return ctrl.Result{}, err
		}
	}
	controllerutil.RemoveFinalizer(operation, controllers.WorkspaceWriterFinalizer)
	return ctrl.Result{}, r.Update(ctx, operation)
}

func (r *UtilityOperationReconciler) admit(ctx context.Context, operation *v1alpha1.UtilityOperation) (bool, string, string, error) {
	if r.Policy == nil {
		if utility.IsPrivilegedOperation(operation.Spec.Operation.Name) {
			return false, "policy-unavailable", "privileged utility operations fail closed when policy is unavailable", nil
		}
		return true, "development-no-policy", "non-privileged development operation", nil
	}
	workflow, project, err := r.resolveContext(ctx, operation)
	if err != nil {
		return false, "", "", err
	}
	var approvalFacts any
	if operation.Spec.Approval != nil || operation.Spec.Operation.Name == utility.OperationGitMergeRequest {
		if err := controllers.ValidateOperationApproval(ctx, r.Client, r.Audit, operation, workflow); err != nil {
			return false, "", "", err
		}
		approvalFacts = map[string]any{"admitted": true, "subjectMatches": true, "binding": operation.Spec.Approval}
	}
	credentialRef := utilityCredentialReference(project, operation.Spec.Operation.Name)
	parameters := copyStringMap(operation.Spec.Operation.Parameters)
	if operation.Spec.Operation.Name == utility.OperationBuildImage && project.Spec.Validation.ImageName != "" {
		parameters["imageName"] = project.Spec.Validation.ImageName
	}
	decision, err := r.Policy.Evaluate(ctx, map[string]any{
		"operation": "utility.execute",
		"request": map[string]any{
			"operation": operation.Spec.Operation.Name, "workflow": operation.Spec.WorkflowRef,
			"step": operation.Spec.StepName, "attempt": operation.Spec.Attempt, "project": project.Name,
			"policyProfile": project.Spec.PolicyProfileRef, "parameters": parameters, "approval": approvalFacts,
			"credentialClass": utility.ExpectedCredentialClass(operation.Spec.Operation.Name), "hasCredential": credentialRef.Name != "",
			"authorityResource": map[string]any{"kind": "UtilityOperation", "name": operation.Name, "uid": string(operation.UID)},
		},
	})
	if err != nil {
		return false, "", "", err
	}
	_ = workflow
	return decision.Allowed, decision.ID, fmt.Sprint(decision.Reasons), nil
}

func utilityAdmissionDecision(operation *v1alpha1.UtilityOperation, policyDecisionID string, allowed bool, reason string) (audit.DecisionEvaluated, error) {
	decisionInput, err := json.Marshal(struct {
		OperationSpec   v1alpha1.UtilityOperationSpec `json:"operationSpec"`
		CredentialClass string                        `json:"credentialClass"`
		PolicyDecision  string                        `json:"policyDecisionId"`
		Reason          string                        `json:"reason"`
	}{
		OperationSpec:   operation.Spec,
		CredentialClass: utility.ExpectedCredentialClass(operation.Spec.Operation.Name),
		PolicyDecision:  policyDecisionID,
		Reason:          reason,
	})
	if err != nil {
		return audit.DecisionEvaluated{}, fmt.Errorf("marshal utility admission decision input: %w", err)
	}
	inputDigest := artifactcontract.DigestBytes(decisionInput)
	outcome := "denied"
	if allowed {
		outcome = "allowed"
	}
	var evidenceEvents []string
	if allowed && operation.Spec.Approval != nil {
		evidenceEvents = []string{operation.Spec.Approval.AdmissionEventID}
	}
	return audit.DecisionEvaluated{
		ApprovalBinding: operation.Spec.Approval,
		EvidenceEvents:  evidenceEvents,
		SchemaVersion:   audit.PayloadSchemaVersionV1,
		Primitive: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    v1alpha1.GroupVersion.String(),
			Kind:          "UtilityOperation",
			Namespace:     operation.Namespace,
			Name:          operation.Name,
			UID:           string(operation.UID),
		},
		Decision: audit.DecisionRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			ID:            audit.DeterministicID("utility-operation-admission", string(operation.UID), inputDigest),
			Kind:          "policy",
			Revision:      policyDecisionID,
			InputDigest:   inputDigest,
			Outcome:       outcome,
		},
		Invariants: []audit.InvariantResult{
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "utility-operation-supported", Outcome: "passed", Expected: "supported operation", Observed: operation.Spec.Operation.Name, Reason: "only registered utility operations can reach policy admission"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "utility-policy-decision-recorded", Outcome: "passed", Expected: "policy decision identity", Observed: policyDecisionID, Reason: reason},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "utility-credential-class-derived", Outcome: "passed", Expected: "operation-derived credential class", Observed: utility.ExpectedCredentialClass(operation.Spec.Operation.Name), Reason: "the credential class is derived from the admitted operation rather than caller input"},
		},
	}, nil
}

func utilityCredentialReference(project *v1alpha1.SovereignProject, operation string) v1alpha1.NamespacedReference {
	switch utility.ExpectedCredentialClass(operation) {
	case utility.CredentialClassRepository:
		return project.Spec.ApplicationRepository.CredentialRef
	case utility.CredentialClassRegistry:
		return project.Spec.BuildJob.CredentialRef
	default:
		return v1alpha1.NamespacedReference{}
	}
}

func (r *UtilityOperationReconciler) resolveContext(ctx context.Context, operation *v1alpha1.UtilityOperation) (*v1alpha1.SovereignWorkflow, *v1alpha1.SovereignProject, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: operation.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return nil, nil, err
	}
	if workflow.Spec.Project.Name == "" {
		return nil, nil, fmt.Errorf("utility operation requires a workflow projectRef")
	}
	var project v1alpha1.SovereignProject
	if err := r.Get(ctx, types.NamespacedName{Name: workflow.Spec.Project.Name}, &project); err != nil {
		return nil, nil, err
	}
	return &workflow, &project, nil
}

func (r *UtilityOperationReconciler) recordPolicyDecision(ctx context.Context, operation *v1alpha1.UtilityOperation) error {
	_, _, err := r.updateStatus(ctx, client.ObjectKeyFromObject(operation), func(latest *v1alpha1.UtilityOperation) bool {
		if latest.Status.PolicyDecisionID == operation.Status.PolicyDecisionID {
			return false
		}
		latest.Status.PolicyDecisionID = operation.Status.PolicyDecisionID
		latest.Status.ObservedGeneration = latest.Generation
		return true
	})
	return err
}

type utilityWorkloadConfig struct {
	input            utilitycontract.Input
	executionImage   string
	executionProfile utilityExecutionProfile
	credentialRef    v1alpha1.NamespacedReference
	credentialClass  string
	credentialSecret string
	bootstrapRuntime bool
}

type utilityExecutionProfile string

const (
	utilityExecutionProfileHardened         utilityExecutionProfile = "hardened-v1"
	utilityExecutionProfileBuildKitRootless utilityExecutionProfile = "buildkit-rootless-v2"
	executionProfileAnnotation                                      = "sovereign-ai.io/execution-profile"
)

func (r *UtilityOperationReconciler) ensureWorkload(ctx context.Context, operation *v1alpha1.UtilityOperation, grant controllers.WorkspaceWriterGrant) error {
	workflow, project, err := r.resolveContext(ctx, operation)
	if err != nil {
		return err
	}
	workload, err := buildUtilityWorkloadConfig(operation, workflow, project, r.utilityImage(), grant)
	if err != nil {
		return err
	}
	workload.input.Inputs, err = r.resolveUtilityInputs(ctx, operation)
	if err != nil {
		return err
	}
	var evidence []audit.ArtifactEvidence
	if r.Audit != nil {
		var ready bool
		var invalidReason string
		evidence, ready, invalidReason, err = controllers.ResolveArtifactEvidence(
			ctx,
			r.Client,
			operation.Namespace,
			operation.Spec.WorkflowRef,
			operation.Spec.Inputs,
		)
		if err != nil {
			return err
		}
		if invalidReason != "" {
			return fmt.Errorf("resolve utility input evidence: %s", invalidReason)
		}
		if !ready {
			return fmt.Errorf("utility input evidence is not ready")
		}
	}
	if workload.credentialRef.Name != "" {
		workload.credentialSecret, err = r.ensureCredential(ctx, operation, workload.credentialRef, workload.credentialClass)
		if err != nil {
			return err
		}
	}
	if r.Audit != nil {
		admissionEventID, err := r.findOperationEventID(ctx, operation, "UtilityOperationAdmitted")
		if err != nil {
			return err
		}
		authorityEventID, err := r.appendExecutionAuthorityEstablished(ctx, operation, grant)
		if err != nil {
			return err
		}
		inputEventID, err := r.appendInputsResolved(ctx, operation, evidence)
		if err != nil {
			return err
		}
		executionDecisionEventID, err := r.appendUtilityExecutionAuthorized(ctx, operation, workload, admissionEventID, authorityEventID, inputEventID)
		if err != nil {
			return err
		}
		workload.input.Lineage = &utilitycontract.LineageReferences{
			AdmissionDecisionEvent: admissionEventID,
			AuthorityEvent:         authorityEventID,
			InputsEvent:            inputEventID,
			ExecutionDecisionEvent: executionDecisionEventID,
		}
	}
	data, err := json.Marshal(workload.input)
	if err != nil {
		return err
	}
	configName := operation.Name + "-input"
	config := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configName, Namespace: operation.Namespace}, Data: map[string]string{"input.json": string(data)}}
	if err := controllerutil.SetControllerReference(operation, config, r.Scheme); err != nil {
		return err
	}
	if err := r.Create(ctx, config); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	job := buildUtilityJob(operation, workflow.Status.PvcName, configName, r.utilityImage(), workload, grant)
	if err := controllerutil.SetControllerReference(operation, job, r.Scheme); err != nil {
		return err
	}
	return client.IgnoreAlreadyExists(r.Create(ctx, job))
}

func (r *UtilityOperationReconciler) resolveUtilityInputs(ctx context.Context, operation *v1alpha1.UtilityOperation) ([]utilitycontract.ArtifactInput, error) {
	if len(operation.Spec.Inputs) == 0 {
		return nil, nil
	}
	var artifactList v1alpha1.ArtifactList
	if err := r.List(ctx, &artifactList, client.InNamespace(operation.Namespace)); err != nil {
		return nil, fmt.Errorf("list utility input artifacts: %w", err)
	}
	resolved := make([]utilitycontract.ArtifactInput, 0, len(operation.Spec.Inputs))
	for _, requested := range operation.Spec.Inputs {
		if requested.ArtifactRef != nil {
			artifact, ready, invalidReason, err := artifacts.ResolvePinnedInput(
				ctx,
				r.Client,
				operation.Namespace,
				operation.Spec.WorkflowRef,
				requested,
			)
			if err != nil {
				return nil, err
			}
			if invalidReason != "" {
				return nil, fmt.Errorf("%s", invalidReason)
			}
			if !ready {
				return nil, fmt.Errorf("input artifact %q is not accepted yet", requested.Name)
			}
			resolved = append(resolved, utilitycontract.ArtifactInput{
				Name:     requested.Name,
				Contract: artifact.Spec.Contract.Name + "/" + artifact.Spec.Contract.Version,
				Digest:   artifact.Spec.Digest,
				Path:     artifact.Spec.Path,
			})
			continue
		}

		matches := make([]v1alpha1.Artifact, 0, 1)
		for index := range artifactList.Items {
			artifact := artifactList.Items[index]
			if artifact.Spec.WorkflowRef == operation.Spec.WorkflowRef &&
				artifact.Spec.Contract.Name == requested.Name &&
				(requested.Digest == "" || artifact.Spec.Digest == requested.Digest) && artifacts.ArtifactAccepted(&artifact) {
				matches = append(matches, artifact)
			}
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf("utility input artifact %q resolves to %d accepted artifacts", requested.Name, len(matches))
		}
		artifact := matches[0]
		resolved = append(resolved, utilitycontract.ArtifactInput{
			Name: requested.Name, Contract: artifact.Spec.Contract.Name + "/" + artifact.Spec.Contract.Version,
			Digest: artifact.Spec.Digest, Path: artifact.Spec.Path,
		})
	}
	return resolved, nil
}

func (r *UtilityOperationReconciler) ensureCredential(ctx context.Context, operation *v1alpha1.UtilityOperation, ref v1alpha1.NamespacedReference, credentialClass string) (string, error) {
	sourceNamespace := ref.Namespace
	if sourceNamespace == "" {
		sourceNamespace = operation.Namespace
	}
	var source corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: sourceNamespace, Name: ref.Name}, &source); err != nil {
		return "", fmt.Errorf("resolve %s credential %s/%s: %w", credentialClass, sourceNamespace, ref.Name, err)
	}
	credentialData, err := operationCredentialData(&source, credentialClass)
	if err != nil {
		return "", fmt.Errorf("validate %s credential %s/%s: %w", credentialClass, sourceNamespace, ref.Name, err)
	}
	targetName := operation.Name + "-credential"
	var existing corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: targetName}, &existing); err == nil {
		if existing.Annotations["sovereign-ai.io/source-credential-uid"] != string(source.UID) {
			return "", fmt.Errorf("operation credential %s already exists for a different source", targetName)
		}
		return targetName, nil
	} else if !apierrors.IsNotFound(err) {
		return "", err
	}
	immutable := true
	target := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: targetName, Namespace: operation.Namespace,
		Labels:      map[string]string{"sovereign-ai.io/utility-operation": operation.Name, "sovereign-ai.io/credential-class": credentialClass},
		Annotations: map[string]string{"sovereign-ai.io/source-credential": sourceNamespace + "/" + source.Name, "sovereign-ai.io/source-credential-uid": string(source.UID)},
	}, Type: source.Type, Immutable: &immutable, Data: credentialData}
	if err := controllerutil.SetControllerReference(operation, target, r.Scheme); err != nil {
		return "", err
	}
	if err := r.Create(ctx, target); err != nil && !apierrors.IsAlreadyExists(err) {
		return "", err
	}
	if err := r.appendEvent(ctx, operation, "UtilityCredentialIssued", "issue", targetName, "issued", "", map[string]string{"sourceCredential": sourceNamespace + "/" + source.Name, "credentialClass": credentialClass}); err != nil {
		return "", err
	}
	return targetName, nil
}

// factory for credential extraction/validation
func operationCredentialData(source *corev1.Secret, credentialClass string) (map[string][]byte, error) {
	if credentialClass == utility.CredentialClassRepository {
		return repositoryCredentialData(source)
	}

	data := make(map[string][]byte, len(source.Data))
	for name, value := range source.Data {
		data[name] = append([]byte(nil), value...)
	}
	return data, nil
}

// Extracts and validates repository credential URL, returns formatted credentials for container env injection
func repositoryCredentialData(source *corev1.Secret) (map[string][]byte, error) {
	if source.Type != corev1.SecretTypeOpaque {
		return nil, fmt.Errorf("repository credential Secret must have type %q", corev1.SecretTypeOpaque)
	}
	if len(source.Data) != 1 {
		return nil, fmt.Errorf("repository credential Secret must contain exactly the %q key", controllermeta.RepositoryCredentialKey)
	}

	contents, ok := source.Data[controllermeta.RepositoryCredentialKey]
	if !ok || len(contents) == 0 {
		return nil, fmt.Errorf("repository credential Secret must contain a non-empty %q key", controllermeta.RepositoryCredentialKey)
	}
	if !utf8.Valid(contents) {
		return nil, fmt.Errorf("repository credential entry must be valid UTF-8")
	}

	entry := strings.ReplaceAll(string(contents), "\r\n", "\n")
	entry = strings.TrimSuffix(entry, "\n")
	if entry == "" || strings.ContainsAny(entry, "\r\n") || strings.TrimSpace(entry) != entry {
		return nil, fmt.Errorf("repository credential Secret must contain exactly one credential-store entry")
	}

	credentialURL, err := url.Parse(entry)
	if err != nil {
		return nil, fmt.Errorf("repository credential entry must be a valid HTTPS credential-store URL")
	}
	if credentialURL.User == nil {
		return nil, fmt.Errorf("repository credential entry must be one HTTPS URL with an encoded username and secret")
	}
	password, hasPassword := credentialURL.User.Password()
	// Validate credential entry for valid URL
	if !strings.EqualFold(credentialURL.Scheme, "https") ||
		credentialURL.Host == "" ||
		credentialURL.User.Username() == "" ||
		!hasPassword ||
		password == "" ||
		credentialURL.RawQuery != "" ||
		credentialURL.Fragment != "" {
		return nil, fmt.Errorf("repository credential entry must be one HTTPS URL with an encoded username and secret")
	}

	return map[string][]byte{controllermeta.RepositoryCredentialKey: append([]byte(nil), contents...)}, nil
}

func (r *UtilityOperationReconciler) startCollection(ctx context.Context, operation *v1alpha1.UtilityOperation) (ctrl.Result, error) {
	workflow, _, err := r.resolveContext(ctx, operation)
	if err != nil {
		return ctrl.Result{}, err
	}
	credentialName := operation.Name + "-credential"
	credential := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: credentialName, Namespace: operation.Namespace}}
	if err := r.Delete(ctx, credential); err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, err
	} else if err == nil {
		if err := r.appendEvent(ctx, operation, "UtilityCredentialRevoked", "revoke", credentialName, "revoked", "UtilityJobTerminal", nil); err != nil {
			return ctrl.Result{}, err
		}
	}
	grant, err := r.workspaceWriterGrant(operation)
	if err != nil {
		return ctrl.Result{}, err
	}
	objects := controllers.BuildCollectorResources(operation, workflow, operation.Spec.StepName, r.collectorImage(), grant)
	for _, object := range objects {
		if err := controllerutil.SetControllerReference(operation, object, r.Scheme); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Create(ctx, object); err != nil && !apierrors.IsAlreadyExists(err) {
			return ctrl.Result{}, err
		}
	}
	operation.Status.CollectorJobRef = operation.Name + "-collect"
	return ctrl.Result{}, r.setPhase(ctx, operation, v1alpha1.PhaseCollecting, "CollectorCreated", "utility result collector created")
}

func (r *UtilityOperationReconciler) reconcileCollection(ctx context.Context, operation *v1alpha1.UtilityOperation) (ctrl.Result, error) {
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: operation.Status.CollectorJobRef}, &job); err != nil {
		return ctrl.Result{}, err
	}
	if job.Status.Succeeded > 0 {
		if operation.Status.FailureReason != "" {
			return ctrl.Result{}, r.fail(ctx, operation, operation.Status.FailureReason, operation.Status.Retryable)
		}
		return ctrl.Result{}, r.setPhase(ctx, operation, v1alpha1.PhaseSucceeded, "UtilityCompleted", "utility result and artifacts accepted")
	}
	if job.Status.Failed > 0 {
		return ctrl.Result{}, r.fail(ctx, operation, "UtilityCollectionFailed", false)
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func buildUtilityWorkloadConfig(operation *v1alpha1.UtilityOperation, workflow *v1alpha1.SovereignWorkflow, project *v1alpha1.SovereignProject, runtimeImage string, grant controllers.WorkspaceWriterGrant) (utilityWorkloadConfig, error) {
	if !utility.IsSupportedOperation(operation.Spec.Operation.Name) {
		return utilityWorkloadConfig{}, fmt.Errorf("unsupported utility operation %q", operation.Spec.Operation.Name)
	}
	outputs := make([]utilitycontract.OutputObligation, 0, len(operation.Spec.OutputContracts))
	for _, output := range operation.Spec.OutputContracts {
		outputs = append(outputs, utilitycontract.OutputObligation{Name: output.Name, Version: output.Version, Required: true})
	}
	parameters := copyStringMap(operation.Spec.Operation.Parameters)
	switch operation.Spec.Operation.Name {
	case utility.OperationGitCreateBranch, utility.OperationGitCommit, utility.OperationGitPush, utility.OperationGitMerge, utility.OperationGitMergeRequest, utility.OperationBuildImage:
		parameters["repositoryURL"] = project.Spec.ApplicationRepository.URL
	}
	result := utilityWorkloadConfig{
		executionImage: runtimeImage, executionProfile: utilityExecutionProfileHardened,
		credentialClass: utility.ExpectedCredentialClass(operation.Spec.Operation.Name),
		input: utilitycontract.Input{
			SchemaVersion:    utilitycontract.Version,
			WorkflowID:       workflow.Spec.WorkflowID,
			StepName:         operation.Spec.StepName,
			Attempt:          operation.Spec.Attempt,
			Authority:        utilitycontract.AuthorityReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "UtilityOperation", Namespace: operation.Namespace, Name: operation.Name, UID: string(operation.UID)},
			PolicyDecisionID: operation.Status.PolicyDecisionID,
			Operation:        operation.Spec.Operation.Name,
			IdempotencyKey:   utilityIdempotencyKey(operation, workflow),
			CredentialClass:  utility.ExpectedCredentialClass(operation.Spec.Operation.Name),
			Parameters:       parameters,
			Outputs:          outputs,
			WorkspacePath:    "/workspace",
			StagingPath:      controllers.ExecutionStagingPath(operation.Name),
			ControlPath:      controllers.ExecutionControlPath(operation.Name),
			ResultPath:       controllers.ExecutionResultPath(operation.Name),
			AuditEventsPath:  controllers.ExecutionAuditEventsPath(operation.Name),
			WorkspaceWrite:   utilitycontract.WorkspaceWriteAuthority{LeaseName: grant.LeaseName, HolderIdentity: grant.HolderIdentity, WriterEpoch: grant.Epoch},
		},
	}
	switch operation.Spec.Operation.Name {
	case utility.OperationRepositoryInitialize:
		result.input.Parameters["repositoryURL"] = project.Spec.ApplicationRepository.URL
		result.input.Parameters["revision"] = project.Spec.ApplicationRepository.DefaultRevision
		result.credentialRef = utilityCredentialReference(project, operation.Spec.Operation.Name)
	case utility.OperationGitPush, utility.OperationGitMerge, utility.OperationGitMergeRequest:
		result.credentialRef = utilityCredentialReference(project, operation.Spec.Operation.Name)
	case utility.OperationTestRun:
		if project.Spec.TestJob.Image == "" || len(project.Spec.TestJob.Command) == 0 {
			return utilityWorkloadConfig{}, fmt.Errorf("project %s has no testJob image and command", project.Name)
		}
		result.executionImage = project.Spec.TestJob.Image
		result.bootstrapRuntime = result.executionImage != runtimeImage
		result.input.Command = append(append([]string(nil), project.Spec.TestJob.Command...), project.Spec.TestJob.Args...)
		result.input.Parameters["environmentImageDigest"] = admittedImageIdentityDigest(project.Spec.TestJob.Image)
		result.credentialRef = project.Spec.TestJob.CredentialRef
	case utility.OperationBuildImage:
		if project.Spec.BuildJob.Image == "" || len(project.Spec.BuildJob.Command) == 0 {
			return utilityWorkloadConfig{}, fmt.Errorf("project %s has no buildJob image and command", project.Name)
		}
		result.executionImage = project.Spec.BuildJob.Image
		result.executionProfile = utilityExecutionProfileBuildKitRootless
		result.bootstrapRuntime = result.executionImage != runtimeImage
		result.input.Command = append(append([]string(nil), project.Spec.BuildJob.Command...), project.Spec.BuildJob.Args...)
		result.input.Parameters["builderImageDigest"] = admittedImageIdentityDigest(project.Spec.BuildJob.Image)
		result.input.Parameters["digestFile"] = "image-metadata.json"
		result.input.Parameters["dockerfile"] = "Dockerfile"
		result.credentialRef = utilityCredentialReference(project, operation.Spec.Operation.Name)
		if project.Spec.Validation.ImageName != "" {
			result.input.Parameters["imageName"] = project.Spec.Validation.ImageName
		}
	}
	result.input.Parameters["executionProfile"] = string(result.executionProfile)
	if result.input.Operation == utility.OperationRepositoryInitialize && (result.input.Parameters["repositoryURL"] == "" || result.input.Parameters["revision"] == "") {
		return utilityWorkloadConfig{}, fmt.Errorf("project %s repository URL and default revision are required", project.Name)
	}
	return result, result.input.Validate()
}

func admittedImageIdentityDigest(image string) string {
	if separator := strings.LastIndex(image, "@sha256:"); separator >= 0 {
		candidate := image[separator+1:]
		if len(candidate) == 71 && candidate == strings.ToLower(candidate) {
			return candidate
		}
	}
	return artifactcontract.DigestBytes([]byte(image))
}
func utilityIdempotencyKey(operation *v1alpha1.UtilityOperation, workflow *v1alpha1.SovereignWorkflow) string {
	identity := string(workflow.UID)
	if identity == "" {
		identity = workflow.Name
	}
	return operation.Namespace + "/" + identity + "/" + operation.Spec.StepName
}

func buildUtilityJob(operation *v1alpha1.UtilityOperation, pvcName, configName, runtimeImage string, workload utilityWorkloadConfig, grant controllers.WorkspaceWriterGrant) *batchv1.Job {
	automount, allowPrivilegeEscalation := false, false
	backoff, ttl := int32(0), int32(3600)
	if workload.executionProfile == "" {
		workload.executionProfile = utilityExecutionProfileHardened
	}
	if pvcName == "" {
		pvcName = operation.Spec.WorkflowRef.Name + "-workspace"
	}
	runnerPath := "/utility-runner"
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      operation.Name,
			Namespace: operation.Namespace,
			Labels: map[string]string{
				controllermeta.LabelWorkflow:        operation.Labels[controllermeta.LabelWorkflow],
				controllermeta.LabelStep:            operation.Spec.StepName,
				"sovereign-ai.io/utility-operation": operation.Name,
			},
			Annotations: mergeStringMaps(controllers.WorkspaceWriterAnnotations(grant), map[string]string{executionProfileAnnotation: string(workload.executionProfile)}),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: mergeStringMaps(controllers.WorkspaceWriterAnnotations(grant), map[string]string{executionProfileAnnotation: string(workload.executionProfile)})},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automount,
					SecurityContext:              controllers.WorkspaceWorkloadSecurityContext(),
					Containers: []corev1.Container{
						{
							Name:            "utility",
							Image:           workload.executionImage,
							Command:         []string{runnerPath},
							Args:            []string{"--input", "/control/input.json", "--result", controllers.ExecutionResultPath(operation.Name)},
							SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
							VolumeMounts:    []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}, {Name: "input", MountPath: "/control", ReadOnly: true}},
						}},
					Volumes: []corev1.Volume{
						{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}}},
						{Name: "input", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: configName}}}},
					},
				}}}}
	if operation.Spec.Timeout != nil && operation.Spec.Timeout.Duration > 0 {
		seconds := int64(operation.Spec.Timeout.Duration.Seconds())
		job.Spec.ActiveDeadlineSeconds = &seconds
	}
	container := &job.Spec.Template.Spec.Containers[0]
	container.Env = append(container.Env, controllers.WorkspaceWriterEnv(grant)...)
	applyUtilityExecutionProfile(&job.Spec.Template.Spec, container, workload.executionProfile)

	if workload.bootstrapRuntime {
		runnerPath = "/sovereign-bin/utility-runner"
		container.Command = []string{runnerPath}
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "utility-runtime", MountPath: "/sovereign-bin"})
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes,
			corev1.Volume{Name: "utility-runtime", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}})
		job.Spec.Template.Spec.InitContainers = []corev1.Container{
			{
				Name:            "install-utility-runtime",
				Image:           runtimeImage,
				Command:         []string{"/bin/cp", "/utility-runner", runnerPath},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: &allowPrivilegeEscalation, Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "utility-runtime", MountPath: "/sovereign-bin"}},
			}}
	}
	// Add secret to container environment
	if workload.credentialSecret != "" {
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "operation-credential", MountPath: "/var/run/sovereign/credentials", ReadOnly: true})
		secretSource := &corev1.SecretVolumeSource{SecretName: workload.credentialSecret}
		switch workload.credentialClass {
		case utility.CredentialClassRegistry:
			secretSource.Items = []corev1.KeyToPath{{Key: corev1.DockerConfigJsonKey, Path: "config.json"}}
			container.Env = append(container.Env, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: "/var/run/sovereign/credentials"})
		case utility.CredentialClassRepository:
			secretSource.Items = []corev1.KeyToPath{{Key: controllermeta.RepositoryCredentialKey, Path: controllermeta.RepositoryCredentialKey}}
			if operation.Spec.Operation.Name == utility.OperationGitMergeRequest {
				container.Env = append(container.Env, corev1.EnvVar{Name: utility.RepositoryCredentialFileEnv, Value: "/var/run/sovereign/credentials/" + controllermeta.RepositoryCredentialKey})
			}
			container.Env = append(container.Env,
				corev1.EnvVar{Name: "GIT_CONFIG_COUNT", Value: "2"},
				corev1.EnvVar{Name: "GIT_CONFIG_KEY_0", Value: "credential.helper"}, corev1.EnvVar{Name: "GIT_CONFIG_VALUE_0", Value: ""},
				corev1.EnvVar{Name: "GIT_CONFIG_KEY_1", Value: "credential.helper"},
				corev1.EnvVar{Name: "GIT_CONFIG_VALUE_1", Value: "store --file=/var/run/sovereign/credentials/credentials"},
				corev1.EnvVar{Name: "GIT_TERMINAL_PROMPT", Value: "0"},
			)
		}
		job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes, corev1.Volume{Name: "operation-credential", VolumeSource: corev1.VolumeSource{Secret: secretSource}})
	}
	return job
}

func applyUtilityExecutionProfile(pod *corev1.PodSpec, container *corev1.Container, profile utilityExecutionProfile) {
	switch profile {
	case utilityExecutionProfileBuildKitRootless:
		identity, allowPrivilegeEscalation := int64(1000), true
		container.SecurityContext.RunAsUser = &identity
		container.SecurityContext.RunAsGroup = &identity
		container.SecurityContext.AllowPrivilegeEscalation = &allowPrivilegeEscalation
		container.SecurityContext.Capabilities.Add = []corev1.Capability{"SETUID", "SETGID"}
		container.SecurityContext.SeccompProfile = &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeUnconfined}
		container.SecurityContext.AppArmorProfile = &corev1.AppArmorProfile{Type: corev1.AppArmorProfileTypeUnconfined}
		setContainerEnv(container,
			corev1.EnvVar{Name: "HOME", Value: "/home/user"},
			corev1.EnvVar{Name: "XDG_CACHE_HOME", Value: "/home/user/.cache"},
			corev1.EnvVar{Name: "BUILDKITD_FLAGS", Value: "--oci-worker-no-process-sandbox"},
		)
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "buildkit-state", MountPath: "/home/user/.local/share/buildkit"})
		pod.Volumes = append(pod.Volumes, corev1.Volume{
			Name: "buildkit-state",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	default:
		setContainerEnv(container,
			corev1.EnvVar{Name: "HOME", Value: "/home/utility"},
			corev1.EnvVar{Name: "XDG_CACHE_HOME", Value: "/home/utility/.cache"},
		)
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{Name: "utility-home", MountPath: "/home/utility"})
		pod.Volumes = append(pod.Volumes, corev1.Volume{
			Name: "utility-home",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
	}
}

func setContainerEnv(container *corev1.Container, values ...corev1.EnvVar) {
	for _, value := range values {
		replaced := false
		for index := range container.Env {
			if container.Env[index].Name == value.Name {
				container.Env[index] = value
				replaced = true
				break
			}
		}
		if !replaced {
			container.Env = append(container.Env, value)
		}
	}
}

func mergeStringMaps(base, additions map[string]string) map[string]string {
	result := make(map[string]string, len(base)+len(additions))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range additions {
		result[key] = value
	}
	return result
}

func (r *UtilityOperationReconciler) collectorImage() string {
	if r.CollectorImage != "" {
		return r.CollectorImage
	}
	return "sovereign-artifact-collector:dev"
}

func (r *UtilityOperationReconciler) utilityImage() string {
	if r.UtilityImage != "" {
		return r.UtilityImage
	}
	return "sovereign-utility-runner:dev"
}

func (r *UtilityOperationReconciler) setPhase(ctx context.Context, operation *v1alpha1.UtilityOperation, phase v1alpha1.ResourcePhase, reason, message string) error {
	updated, changed, err := r.updateStatus(ctx, client.ObjectKeyFromObject(operation), func(latest *v1alpha1.UtilityOperation) bool {
		condition := apiMeta.FindStatusCondition(latest.Status.Conditions, "Ready")
		if latest.Status.Phase == phase && condition != nil && condition.Reason == reason && condition.Message == message &&
			latest.Status.FailureReason == operation.Status.FailureReason && latest.Status.FailureMessage == operation.Status.FailureMessage {
			return false
		}
		now := metav1.Now()
		if r.Now != nil {
			now = metav1.NewTime(r.Now())
		}
		latest.Status.Phase = phase
		latest.Status.JobRef = operation.Status.JobRef
		latest.Status.CollectorJobRef = operation.Status.CollectorJobRef
		latest.Status.PolicyDecisionID = operation.Status.PolicyDecisionID
		latest.Status.WorkspaceWriterLeaseRef = operation.Status.WorkspaceWriterLeaseRef
		latest.Status.WorkspaceWriterEpoch = operation.Status.WorkspaceWriterEpoch
		latest.Status.WorkspaceWriterReleased = operation.Status.WorkspaceWriterReleased
		latest.Status.FailureReason = operation.Status.FailureReason
		latest.Status.FailureMessage = operation.Status.FailureMessage
		latest.Status.Retryable = operation.Status.Retryable
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
	return r.appendEvent(ctx, updated, "UtilityOperation"+string(phase), "reconcile", updated.Spec.Operation.Name, string(phase), reason, nil)
}

func (r *UtilityOperationReconciler) fail(ctx context.Context, operation *v1alpha1.UtilityOperation, reason string, retryable bool) error {
	if operation.Status.FailureReason != reason {
		operation.Status.FailureMessage = ""
	}
	operation.Status.FailureReason, operation.Status.Retryable = reason, retryable
	return r.setPhase(ctx, operation, v1alpha1.PhaseFailed, reason, "utility operation failed")
}

func (r *UtilityOperationReconciler) interrupt(ctx context.Context, operation *v1alpha1.UtilityOperation, reason string, retryable bool) error {
	operation.Status.FailureMessage = ""
	operation.Status.FailureReason, operation.Status.Retryable = reason, retryable
	return r.setPhase(ctx, operation, v1alpha1.PhaseInterrupted, reason, "utility operation interrupted")
}

func (r *UtilityOperationReconciler) updateStatus(ctx context.Context, key types.NamespacedName, mutate func(*v1alpha1.UtilityOperation) bool) (*v1alpha1.UtilityOperation, bool, error) {
	var updated v1alpha1.UtilityOperation
	changed := false
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var latest v1alpha1.UtilityOperation
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

func (r *UtilityOperationReconciler) appendExecutionAuthorityEstablished(ctx context.Context, operation *v1alpha1.UtilityOperation, grant controllers.WorkspaceWriterGrant) (string, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: operation.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return "", fmt.Errorf("read workflow for utility authority evidence: %w", err)
	}
	var attempt v1alpha1.StepAttempt
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: operation.Spec.AttemptRef}, &attempt); err != nil {
		return "", fmt.Errorf("read step attempt for utility authority evidence: %w", err)
	}
	var lease coordinationv1.Lease
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: grant.LeaseName}, &lease); err != nil {
		return "", fmt.Errorf("read workspace writer lease for utility authority evidence: %w", err)
	}
	owner := metav1.GetControllerOf(operation)
	if workflow.UID == "" || attempt.UID == "" || operation.UID == "" || lease.UID == "" {
		return "", fmt.Errorf("utility authority evidence requires workflow, step attempt, primitive, and lease UIDs")
	}
	if workflow.UID != operation.Spec.WorkflowRef.UID {
		return "", fmt.Errorf("utility authority evidence workflow UID does not match primitive workflow reference")
	}
	if owner == nil || owner.UID != attempt.UID {
		return "", fmt.Errorf("utility authority evidence step attempt does not control primitive")
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
		return "", fmt.Errorf("utility authority evidence workspace writer grant is no longer current")
	}
	payload := audit.AuthorityEstablished{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Workflow:      utilityResourceRef("SovereignWorkflow", &workflow),
		StepAttempt:   utilityResourceRef("StepAttempt", &attempt),
		Primitive:     utilityResourceRef("UtilityOperation", operation),
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
			Capabilities:   []string{operation.Spec.Operation.Name},
		},
		Invariants: []audit.InvariantResult{
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "workflow-uid-bound", Outcome: "passed", Expected: string(workflow.UID), Observed: string(operation.Spec.WorkflowRef.UID), Reason: "the utility primitive is bound to the admitted workflow"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "step-attempt-owns-primitive", Outcome: "passed", Expected: string(attempt.UID), Observed: string(owner.UID), Reason: "the authorized step attempt controls the utility primitive"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "workspace-writer-fence-current", Outcome: "passed", Expected: fmt.Sprintf("%s@%d", grant.HolderIdentity, grant.Epoch), Observed: fmt.Sprintf("%s@%d", observedHolder, observedEpoch), Reason: "the utility primitive holds the current exclusive writer term"},
		},
	}
	event, err := r.appendEventWithDataResult(ctx, operation, "ExecutionAuthorityEstablished", "authorize", operation.Name, "established", "", payload)
	if err != nil {
		return "", err
	}
	return event.ID, nil
}

func (r *UtilityOperationReconciler) appendUtilityExecutionAuthorized(ctx context.Context, operation *v1alpha1.UtilityOperation, workload utilityWorkloadConfig, admissionEventID, authorityEventID, inputEventID string) (string, error) {
	decisionInput, err := json.Marshal(struct {
		Input          utilitycontract.Input `json:"input"`
		AdmissionEvent string                `json:"admissionEvent"`
		AuthorityEvent string                `json:"authorityEvent"`
		InputsEvent    string                `json:"inputsEvent"`
		Credential     string                `json:"credential"`
	}{workload.input, admissionEventID, authorityEventID, inputEventID, workload.credentialSecret})
	if err != nil {
		return "", fmt.Errorf("marshal utility execution authorization input: %w", err)
	}
	inputDigest := artifactcontract.DigestBytes(decisionInput)
	payload := audit.DecisionEvaluated{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Primitive:     utilityResourceRef("UtilityOperation", operation),
		Decision: audit.DecisionRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			ID:            audit.DeterministicID("utility-execution-authorization", string(operation.UID), inputDigest),
			Kind:          "controller",
			Revision:      "utilityoperation-execution/v1",
			InputDigest:   inputDigest,
			Outcome:       "allowed",
		},
		AuthorityEvent: authorityEventID,
		InputEvent:     inputEventID,
		EvidenceEvents: []string{admissionEventID},
		Invariants: []audit.InvariantResult{
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "utility-admission-linked", Outcome: "passed", Expected: admissionEventID, Observed: admissionEventID, Reason: "policy admitted this exact utility operation"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "execution-authority-linked", Outcome: "passed", Expected: authorityEventID, Observed: authorityEventID, Reason: "workflow, attempt ownership, and writer authority were established"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "execution-inputs-linked", Outcome: "passed", Expected: inputEventID, Observed: inputEventID, Reason: "resolved input evidence was recorded before utility execution"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "credential-scope-resolved", Outcome: "passed", Expected: workload.credentialClass, Observed: workload.credentialClass, Reason: "any credential is operation-scoped and derived from project policy"},
			{SchemaVersion: audit.PayloadSchemaVersionV1, ID: "utility-execution-profile-selected", Outcome: "passed", Expected: string(workload.executionProfile), Observed: string(workload.executionProfile), Reason: "the platform selected a closed execution profile for this allow-listed utility operation"},
		},
	}
	event, err := r.appendEventWithDataResult(ctx, operation, "UtilityExecutionAuthorized", "authorize-execution", operation.Name, "authorized", "", payload)
	if err != nil {
		return "", err
	}
	return event.ID, nil
}

func utilityResourceRef(kind string, object client.Object) audit.ResourceRef {
	return audit.ResourceRef{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		APIVersion:    v1alpha1.GroupVersion.String(),
		Kind:          kind,
		Namespace:     object.GetNamespace(),
		Name:          object.GetName(),
		UID:           string(object.GetUID()),
	}
}

func (r *UtilityOperationReconciler) findOperationEventID(ctx context.Context, operation *v1alpha1.UtilityOperation, eventType string) (string, error) {
	if r.Audit == nil {
		return "", nil
	}
	events, err := r.Audit.ListWorkflow(ctx, operation.Spec.WorkflowRef.Name)
	if err != nil {
		return "", fmt.Errorf("list utility lineage events: %w", err)
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.Type == eventType && event.Subject.Step == operation.Spec.StepName && event.Subject.Attempt == operation.Spec.Attempt && event.References["utilityOperation"] == operation.Name {
			return event.ID, nil
		}
	}
	return "", fmt.Errorf("required %s event for UtilityOperation %s was not recorded", eventType, operation.Name)
}

func (r *UtilityOperationReconciler) appendEvent(ctx context.Context, operation *v1alpha1.UtilityOperation, eventType, action, target, outcome, reason string, data any) error {
	_, err := r.appendEventWithDataResult(ctx, operation, eventType, action, target, outcome, reason, data)
	return err
}

func (r *UtilityOperationReconciler) appendEventWithDataResult(ctx context.Context, operation *v1alpha1.UtilityOperation, eventType, action, target, outcome, reason string, data any) (audit.Event, error) {
	decisionID := operation.Status.PolicyDecisionID
	if payload, ok := data.(audit.DecisionEvaluated); ok {
		decisionID = payload.Decision.ID
	}
	return audit.BuildAndAppendControllerEvent(ctx, r.Audit, "utilityoperation-controller", r.Now, audit.EventOptions{
		Type: eventType, Subject: audit.Subject{Namespace: operation.Namespace, Workflow: operation.Spec.WorkflowRef.Name, Step: operation.Spec.StepName, Attempt: operation.Spec.Attempt},
		Action: action, Target: target, Outcome: outcome, Reason: reason, DecisionID: decisionID,
		References: map[string]string{"utilityOperation": operation.Name, "stepAttempt": operation.Spec.AttemptRef, "job": operation.Status.JobRef, "collector": operation.Status.CollectorJobRef, "workspaceWriterLease": operation.Status.WorkspaceWriterLeaseRef, "writerEpoch": fmt.Sprint(operation.Status.WorkspaceWriterEpoch)},
		Data:       data,
	})
}

func (r *UtilityOperationReconciler) appendInputsResolved(ctx context.Context, operation *v1alpha1.UtilityOperation, evidence []audit.ArtifactEvidence) (string, error) {
	payload := controllers.InputsResolvedPayload("UtilityOperation", operation, evidence)
	event, err := audit.BuildAndAppendControllerEvent(ctx, r.Audit, "utilityoperation-controller", r.Now, audit.EventOptions{
		Type: "InputsResolved",
		Subject: audit.Subject{
			Namespace: operation.Namespace,
			Workflow:  operation.Spec.WorkflowRef.Name,
			Step:      operation.Spec.StepName,
			Attempt:   operation.Spec.Attempt,
		},
		Action:  "resolve-inputs",
		Target:  operation.Name,
		Outcome: "resolved",
		References: map[string]string{
			"utilityOperation": operation.Name,
			"stepAttempt":      operation.Spec.AttemptRef,
		},
		Data: payload,
	})
	if err != nil {
		return "", err
	}
	return event.ID, nil
}

func copyStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for name, value := range source {
		result[name] = value
	}
	return result
}
