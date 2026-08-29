package controllers

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
	policyengine "github.com/SovereignAI/internal/policy"
	"github.com/SovereignAI/internal/utility"
	"github.com/SovereignAI/internal/utilitycontract"
	batchv1 "k8s.io/api/batch/v1"
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

const repositoryCredentialKey = "credentials"

// UtilityOperationReconciler is the authority owner for deterministic
// platform operations. StepAttempt observes this resource but cannot execute it.
type UtilityOperationReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Audit          audit.Recorder
	Now            func() time.Time
	CollectorImage string
	UtilityImage   string
	Policy         policyengine.Evaluator
}

func (r *UtilityOperationReconciler) SetupWithManager(mgr ctrl.Manager) error {
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
	if !operation.DeletionTimestamp.IsZero() {
		return r.finalizeWorkspaceWriter(ctx, &operation)
	}
	if !controllerutil.ContainsFinalizer(&operation, WorkspaceWriterFinalizer) {
		controllerutil.AddFinalizer(&operation, WorkspaceWriterFinalizer)
		if err := r.Update(ctx, &operation); err != nil {
			return ctrl.Result{}, err
		}
	}
	terminating, err := NamespaceTerminating(ctx, r.Client, operation.Namespace)
	if err != nil || terminating {
		return ctrl.Result{}, err
	}
	if operation.Status.Phase == "" {
		return ctrl.Result{}, r.setPhase(ctx, &operation, v1alpha1.PhasePending, "Initialized", "utility operation initialized")
	}
	if terminalAttempt(operation.Status.Phase) {
		return r.reconcileWorkspaceWriterRelease(ctx, &operation)
	}
	authorized, err := validateDomainAuthority(ctx, r.Client, &operation, operation.Spec.AttemptRef, v1alpha1.ExecutionKindUtility, operation.Spec.WorkflowRef, operation.Spec.StepName, operation.Spec.Attempt)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, &operation, "InvalidStepAttemptAuthority", false)
	}
	if !authorized {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if !utility.IsSupportedOperation(operation.Spec.Operation.Name) {
		return ctrl.Result{}, r.fail(ctx, &operation, "UnsupportedUtilityOperation", false)
	}
	if operation.Status.PolicyDecisionID == "" {
		allowed, decisionID, reason, err := r.admit(ctx, &operation)
		if err != nil {
			return ctrl.Result{}, err
		}
		operation.Status.PolicyDecisionID = decisionID
		if err := r.appendEvent(ctx, &operation, map[bool]string{true: "UtilityOperationAdmitted", false: "UtilityOperationRejected"}[allowed], "admit", operation.Spec.Operation.Name, map[bool]string{true: "admitted", false: "rejected"}[allowed], reason, nil); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.recordPolicyDecision(ctx, &operation); err != nil {
			return ctrl.Result{}, err
		}
		if !allowed {
			return ctrl.Result{}, r.fail(ctx, &operation, "UtilityOperationDenied", false)
		}
	}
	grant, writerState, err := r.ensureWorkspaceWriter(ctx, &operation)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch writerState {
	case WorkspaceWriterBlocked:
		if operation.Status.JobRef != "" || operation.Status.CollectorJobRef != "" {
			if err := r.deleteWorkspaceWriterWorkloads(ctx, &operation); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, r.interrupt(ctx, &operation, "WorkspaceWriterAuthorityNotEstablished", true)
		}
		return ctrl.Result{RequeueAfter: 2 * time.Second}, r.setPhase(ctx, &operation, v1alpha1.PhasePending, "WorkspaceWriterBlocked", "another execution unit holds workspace write authority")
	case WorkspaceWriterLost:
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
		operation.Status.FailureReason, operation.Status.Retryable = "UtilityJobFailed", true
		return r.startCollection(ctx, &operation)
	}
	if job.Status.Active > 0 {
		return ctrl.Result{}, r.setPhase(ctx, &operation, v1alpha1.PhaseRunning, "UtilityRunning", "utility operation is running")
	}
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func (r *UtilityOperationReconciler) ensureWorkspaceWriter(ctx context.Context, operation *v1alpha1.UtilityOperation) (WorkspaceWriterGrant, WorkspaceWriterState, error) {
	var workflow v1alpha1.SovereignWorkflow
	if err := r.Get(ctx, types.NamespacedName{Namespace: operation.Namespace, Name: operation.Spec.WorkflowRef.Name}, &workflow); err != nil {
		return WorkspaceWriterGrant{}, WorkspaceWriterBlocked, err
	}
	if workflow.Status.WorkspaceWriterLeaseRef == "" {
		return WorkspaceWriterGrant{}, WorkspaceWriterBlocked, nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	grant, state, err := AcquireWorkspaceWriter(ctx, r.Client, &workflow, workflow.Status.WorkspaceWriterLeaseRef, "UtilityOperation", operation, operation.Status.WorkspaceWriterEpoch, now)
	if err != nil || state != WorkspaceWriterGranted {
		return grant, state, err
	}
	newGrant := operation.Status.WorkspaceWriterEpoch == 0
	operation.Status.WorkspaceWriterLeaseRef = grant.LeaseName
	operation.Status.WorkspaceWriterEpoch = grant.Epoch
	operation.Status.WorkspaceWriterReleased = false
	if err := r.recordWorkspaceWriterStatus(ctx, operation); err != nil {
		return WorkspaceWriterGrant{}, WorkspaceWriterBlocked, err
	}
	if newGrant {
		if err := r.appendEvent(ctx, operation, "WorkspaceWriterAcquired", "acquire", grant.LeaseName, "granted", fmt.Sprintf("WriterEpoch%d", grant.Epoch), nil); err != nil {
			return WorkspaceWriterGrant{}, WorkspaceWriterBlocked, err
		}
	}
	return grant, state, nil
}

func (r *UtilityOperationReconciler) workspaceWriterGrant(operation *v1alpha1.UtilityOperation) (WorkspaceWriterGrant, error) {
	holder, err := WorkspaceWriterIdentity("UtilityOperation", operation)
	if err != nil {
		return WorkspaceWriterGrant{}, err
	}
	if operation.Status.WorkspaceWriterLeaseRef == "" || operation.Status.WorkspaceWriterEpoch < 1 {
		return WorkspaceWriterGrant{}, fmt.Errorf("UtilityOperation %s has no workspace writer grant", operation.Name)
	}
	return WorkspaceWriterGrant{LeaseName: operation.Status.WorkspaceWriterLeaseRef, HolderIdentity: holder, Epoch: operation.Status.WorkspaceWriterEpoch}, nil
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
	jobQuiet, err := JobWriterQuiescent(ctx, r.Client, operation.Namespace, operation.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	collectorQuiet, err := JobWriterQuiescent(ctx, r.Client, operation.Namespace, operation.Name+"-collect")
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
	if err := ReleaseWorkspaceWriter(ctx, r.Client, operation.Namespace, grant, now); err != nil {
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
	if !controllerutil.ContainsFinalizer(operation, WorkspaceWriterFinalizer) {
		return ctrl.Result{}, nil
	}
	if err := r.deleteWorkspaceWriterWorkloads(ctx, operation); err != nil {
		return ctrl.Result{}, err
	}
	jobQuiet, err := JobWriterQuiescent(ctx, r.Client, operation.Namespace, operation.Name)
	if err != nil {
		return ctrl.Result{}, err
	}
	collectorQuiet, err := JobWriterQuiescent(ctx, r.Client, operation.Namespace, operation.Name+"-collect")
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
		if err := ReleaseWorkspaceWriter(ctx, r.Client, operation.Namespace, grant, now); err != nil {
			return ctrl.Result{}, err
		}
	}
	controllerutil.RemoveFinalizer(operation, WorkspaceWriterFinalizer)
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
			"policyProfile": project.Spec.PolicyProfileRef, "parameters": parameters,
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
	credentialRef    v1alpha1.NamespacedReference
	credentialClass  string
	credentialSecret string
	bootstrapRuntime bool
}

func (r *UtilityOperationReconciler) ensureWorkload(ctx context.Context, operation *v1alpha1.UtilityOperation, grant WorkspaceWriterGrant) error {
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
	if workload.credentialRef.Name != "" {
		workload.credentialSecret, err = r.ensureCredential(ctx, operation, workload.credentialRef, workload.credentialClass)
		if err != nil {
			return err
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
		return nil, fmt.Errorf("repository credential Secret must contain exactly the %q key", repositoryCredentialKey)
	}

	contents, ok := source.Data[repositoryCredentialKey]
	if !ok || len(contents) == 0 {
		return nil, fmt.Errorf("repository credential Secret must contain a non-empty %q key", repositoryCredentialKey)
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

	return map[string][]byte{repositoryCredentialKey: append([]byte(nil), contents...)}, nil
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
	objects := buildCollectorResources(operation, workflow, operation.Spec.StepName, r.collectorImage(), grant)
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

func buildUtilityWorkloadConfig(operation *v1alpha1.UtilityOperation, workflow *v1alpha1.SovereignWorkflow, project *v1alpha1.SovereignProject, runtimeImage string, grant WorkspaceWriterGrant) (utilityWorkloadConfig, error) {
	if !utility.IsSupportedOperation(operation.Spec.Operation.Name) {
		return utilityWorkloadConfig{}, fmt.Errorf("unsupported utility operation %q", operation.Spec.Operation.Name)
	}
	outputs := make([]utilitycontract.OutputObligation, 0, len(operation.Spec.OutputContracts))
	for _, output := range operation.Spec.OutputContracts {
		outputs = append(outputs, utilitycontract.OutputObligation{Name: output.Name, Version: output.Version, Required: true})
	}
	parameters := copyStringMap(operation.Spec.Operation.Parameters)
	switch operation.Spec.Operation.Name {
	case utility.OperationGitCreateBranch, utility.OperationGitCommit, utility.OperationGitPush, utility.OperationGitMerge, utility.OperationBuildImage:
		parameters["repositoryURL"] = project.Spec.ApplicationRepository.URL
	}
	result := utilityWorkloadConfig{
		executionImage: runtimeImage, credentialClass: utility.ExpectedCredentialClass(operation.Spec.Operation.Name),
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
			StagingPath:      executionStagingPath(operation.Name),
			ControlPath:      executionControlPath(operation.Name),
			ResultPath:       executionResultPath(operation.Name),
			AuditEventsPath:  executionAuditEventsPath(operation.Name),
			WorkspaceWrite:   utilitycontract.WorkspaceWriteAuthority{LeaseName: grant.LeaseName, HolderIdentity: grant.HolderIdentity, WriterEpoch: grant.Epoch},
		},
	}
	switch operation.Spec.Operation.Name {
	case utility.OperationRepositoryInitialize:
		result.input.Parameters["repositoryURL"] = project.Spec.ApplicationRepository.URL
		result.input.Parameters["revision"] = project.Spec.ApplicationRepository.DefaultRevision
		result.credentialRef = utilityCredentialReference(project, operation.Spec.Operation.Name)
	case utility.OperationGitPush, utility.OperationGitMerge:
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

func buildUtilityJob(operation *v1alpha1.UtilityOperation, pvcName, configName, runtimeImage string, workload utilityWorkloadConfig, grant WorkspaceWriterGrant) *batchv1.Job {
	automount, allowPrivilegeEscalation := false, false
	backoff, ttl := int32(0), int32(3600)
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
			Annotations: WorkspaceWriterAnnotations(grant),
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoff,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Annotations: WorkspaceWriterAnnotations(grant)},
				Spec: corev1.PodSpec{
					RestartPolicy:                corev1.RestartPolicyNever,
					AutomountServiceAccountToken: &automount,
					SecurityContext:              WorkspaceWorkloadSecurityContext(),
					Containers: []corev1.Container{
						{
							Name:            "utility",
							Image:           workload.executionImage,
							Command:         []string{runnerPath},
							Args:            []string{"--input", "/control/input.json", "--result", executionResultPath(operation.Name)},
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
	container.Env = append(container.Env, WorkspaceWriterEnv(grant)...)
	container.Env = append(container.Env,
		corev1.EnvVar{Name: "HOME", Value: "/home/utility"},
		corev1.EnvVar{Name: "XDG_CACHE_HOME", Value: "/home/utility/.cache"},
	)

	container.VolumeMounts = append(container.VolumeMounts,
		corev1.VolumeMount{
			Name:      "utility-home",
			MountPath: "/home/utility",
		},
	)

	job.Spec.Template.Spec.Volumes = append(job.Spec.Template.Spec.Volumes,
		corev1.Volume{
			Name: "utility-home",
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		},
	)

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
			secretSource.Items = []corev1.KeyToPath{{Key: repositoryCredentialKey, Path: repositoryCredentialKey}}
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
		if latest.Status.Phase == phase && condition != nil && condition.Reason == reason && condition.Message == message {
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
		latest.Status.Retryable = operation.Status.Retryable
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
	return r.appendEvent(ctx, updated, "UtilityOperation"+string(phase), "reconcile", updated.Spec.Operation.Name, string(phase), reason, nil)
}

func (r *UtilityOperationReconciler) fail(ctx context.Context, operation *v1alpha1.UtilityOperation, reason string, retryable bool) error {
	operation.Status.FailureReason, operation.Status.Retryable = reason, retryable
	return r.setPhase(ctx, operation, v1alpha1.PhaseFailed, reason, "utility operation failed")
}

func (r *UtilityOperationReconciler) interrupt(ctx context.Context, operation *v1alpha1.UtilityOperation, reason string, retryable bool) error {
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

func (r *UtilityOperationReconciler) appendEvent(ctx context.Context, operation *v1alpha1.UtilityOperation, eventType, action, target, outcome, reason string, data any) error {
	return audit.AppendControllerEvent(ctx, r.Audit, "utilityoperation-controller", r.Now, audit.EventOptions{
		Type: eventType, Subject: audit.Subject{Namespace: operation.Namespace, Workflow: operation.Spec.WorkflowRef.Name, Step: operation.Spec.StepName, Attempt: operation.Spec.Attempt},
		Action: action, Target: target, Outcome: outcome, Reason: reason, DecisionID: operation.Status.PolicyDecisionID,
		References: map[string]string{"utilityOperation": operation.Name, "stepAttempt": operation.Spec.AttemptRef, "job": operation.Status.JobRef, "collector": operation.Status.CollectorJobRef, "workspaceWriterLease": operation.Status.WorkspaceWriterLeaseRef, "writerEpoch": fmt.Sprint(operation.Status.WorkspaceWriterEpoch)},
		Data:       data,
	})
}

func copyStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for name, value := range source {
		result[name] = value
	}
	return result
}
