package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	defaultBootstrapImage = "sovereign-artifact-bootstrap:dev"
)

func (r *WorkflowReconciler) ensureBootstrap(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) (bool, ctrl.Result, error) {
	if err := validateBootstrapSpec(workflow); err != nil {
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrap", err.Error())
	}

	// Ensure bootstrap is ready
	if apiMeta.IsStatusConditionTrue(workflow.Status.Conditions, "BootstrapReady") {
		_, err := r.acceptedBootstrapArtifact(ctx, workflow)
		if err != nil {
			return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrapArtifact", err.Error())
		}
		if err := r.cleanupBootstrapResources(ctx, workflow); err != nil {
			return false, ctrl.Result{}, err
		}
		return true, ctrl.Result{}, nil
	}

	var artifact v1alpha1.Artifact
	artifactKey := types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Spec.Bootstrap.ArtifactName}
	artifactErr := r.Client.Get(ctx, artifactKey, &artifact)
	if artifactErr != nil && !apierrors.IsNotFound(artifactErr) {
		return false, ctrl.Result{}, artifactErr
	}
	if artifactErr == nil {
		if err := controllers.ValidateBootstrapArtifactIdentity(workflow, &artifact); err != nil {
			return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrapArtifact", err.Error())
		}
		if err := r.releaseBootstrapWriter(ctx, workflow); err != nil {
			return false, ctrl.Result{}, err
		}
		switch artifact.Status.Phase {
		case v1alpha1.PhaseSucceeded:
			if err := r.completeBootstrap(ctx, workflow, &artifact); err != nil {
				return false, ctrl.Result{}, err
			}
			return true, ctrl.Result{}, nil
		case v1alpha1.PhaseFailed:
			return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "BootstrapArtifactRejected", artifactConditionMessage(&artifact))
		default:
			return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
		}
	}

	_, changeRequest, err := controllers.LoadBootstrapChangeRequest(ctx, r.Client, workflow)
	if err != nil {
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrapInput", err.Error())
	}

	hasWriter, grant, result, err := r.getWorkspaceWriter(ctx, workflow)
	if !hasWriter {
		return hasWriter, result, err
	}

	// Deploy and watch for bootstrap job completion (success/failure)
	// releases held writer grant in either scenario

	if jobDeployed, result, err := r.deployBootstrapJob(ctx, workflow, grant); !jobDeployed {
		return jobDeployed, result, err
	}

	if artifactCreated, result, err := r.createChangeRequest(ctx, workflow, changeRequest); !artifactCreated {
		return artifactCreated, result, err
	}
	return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

func validateBootstrapSpec(workflow *v1alpha1.SovereignWorkflow) error {
	bootstrap := workflow.Spec.Bootstrap
	if bootstrap.SourceRef.Name == "" || bootstrap.SourceRef.UID == "" {
		return fmt.Errorf("bootstrap source requires ConfigMap name and UID")
	}
	if bootstrap.Key == "" || strings.ContainsAny(bootstrap.Key, `/\`) {
		return fmt.Errorf("bootstrap source key must be one filename")
	}
	if bootstrap.Contract.Name != "change-request" || bootstrap.Contract.Version != "v1" {
		return fmt.Errorf("bootstrap contract must be change-request/v1")
	}
	if bootstrap.ArtifactName != "change-request" {
		return fmt.Errorf("bootstrap Artifact must be named change-request")
	}
	if _, err := controllers.BootstrapArtifactDigestFilename(bootstrap.ExpectedDigest); err != nil {
		return err
	}
	return nil
}

func (r *WorkflowReconciler) getWorkspaceWriter(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) (bool, controllers.WorkspaceWriterGrant, ctrl.Result, error) {
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	grant, state, err := controllers.AcquireWorkspaceWriter(
		ctx, r.Client, workflow, workflow.Status.WorkspaceWriterLeaseRef,
		"WorkflowBootstrap", workflow, workflow.Status.BootstrapWriterEpoch, now,
	)
	if err != nil {
		return false, grant, ctrl.Result{}, err
	}
	switch state {
	case controllers.WorkspaceWriterBlocked:
		return false, grant, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	case controllers.WorkspaceWriterLost:
		return false, grant, ctrl.Result{}, r.failWorkflow(ctx, workflow, "BootstrapWriterLost", "bootstrap lost its fenced workspace writer grant")
	}
	if workflow.Status.BootstrapWriterEpoch != grant.Epoch || workflow.Status.BootstrapWriterReleased {
		if _, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
			latest.Status.BootstrapWriterEpoch = grant.Epoch
			latest.Status.BootstrapWriterReleased = false
		}); err != nil {
			return false, grant, ctrl.Result{}, err
		}
		return false, grant, ctrl.Result{Requeue: true}, nil
	}
	return true, grant, ctrl.Result{}, nil
}

func (r *WorkflowReconciler) deployBootstrapJob(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, grant controllers.WorkspaceWriterGrant) (bool, ctrl.Result, error) {
	jobName := controllers.BootstrapJobName(workflow.Name)
	var job batchv1.Job
	jobKey := types.NamespacedName{Namespace: workflow.Namespace, Name: jobName}
	err := r.Client.Get(ctx, jobKey, &job)
	if err != nil && !apierrors.IsNotFound(err) {
		return false, ctrl.Result{}, err
	}
	if apierrors.IsNotFound(err) {
		image := r.BootstrapImage
		if image == "" {
			image = defaultBootstrapImage
		}
		job = *controllers.BuildBootstrapJob(workflow, image, grant)
		if err := controllerutil.SetControllerReference(workflow, &job, r.Scheme); err != nil {
			return false, ctrl.Result{}, err
		}
		if err := r.Create(ctx, &job); err != nil && !apierrors.IsAlreadyExists(err) {
			return false, ctrl.Result{}, err
		}
		updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
			latest.Status.BootstrapJobRef = jobName
		})
		if err != nil {
			return false, ctrl.Result{}, err
		}
		if err := r.appendWorkflowEvent(ctx, updated, "WorkflowBootstrapStarted", "", 0, "create", jobName, "started", "",
			map[string]string{"job": jobName, "digest": workflow.Spec.Bootstrap.ExpectedDigest}, nil); err != nil {
			return false, ctrl.Result{}, err
		}
		return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if !metav1.IsControlledBy(&job, workflow) {
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "InvalidBootstrapJob", fmt.Sprintf("job %s is not controlled by workflow", job.Name))
	}
	if workflow.Status.BootstrapJobRef != job.Name {
		updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
			latest.Status.BootstrapJobRef = job.Name
		})
		if err != nil {
			return false, ctrl.Result{}, err
		}
		workflow.Status = updated.Status
	}
	if job.Status.Failed > 0 {
		if err := r.releaseBootstrapWriter(ctx, workflow); err != nil {
			return false, ctrl.Result{}, err
		}
		return false, ctrl.Result{}, r.failWorkflow(ctx, workflow, "BootstrapJobFailed", "bootstrap storage job failed")
	}
	if job.Status.Succeeded == 0 {
		return false, ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}
	if err := r.releaseBootstrapWriter(ctx, workflow); err != nil {
		return false, ctrl.Result{}, err
	}
	return true, ctrl.Result{}, nil
}

func (r WorkflowReconciler) createChangeRequest(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, cr artifactcontract.ChangeRequest) (bool, ctrl.Result, error) {
	artifact := v1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{
			Name:      workflow.Spec.Bootstrap.ArtifactName,
			Namespace: workflow.Namespace,
			Labels: map[string]string{
				controllermeta.LabelWorkflow:   workflow.Spec.WorkflowID,
				"sovereign-ai.io/bootstrap":    "true",
				"sovereign-ai.io/contribution": "contributing",
			},
		},
		Spec: v1alpha1.ArtifactSpec{
			WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.ObjectMeta.UID},
			ProducerRef: v1alpha1.TypedLocalReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "SovereignWorkflow",
				Name:       workflow.Name,
			},
			Contract:       workflow.Spec.Bootstrap.Contract,
			Digest:         workflow.Spec.Bootstrap.ExpectedDigest,
			Path:           controllers.BootstrapArtifactPath(workflow.Spec.Bootstrap.ExpectedDigest),
			Classification: workflow.Spec.Classification,
			SourceRevision: cr.SourceCommit,
		},
	}
	if err := controllerutil.SetControllerReference(workflow, &artifact, r.Scheme); err != nil {
		return false, ctrl.Result{}, err
	}
	if err := r.Create(ctx, &artifact); err != nil && !apierrors.IsAlreadyExists(err) {
		return false, ctrl.Result{}, err
	}
	if err := r.appendWorkflowEvent(ctx, workflow, "WorkflowBootstrapArtifactCreated", "", 0, "create", artifact.Name, "created", "",
		map[string]string{"artifact": artifact.Name, "digest": artifact.Spec.Digest, "job": controllers.BootstrapJobName(workflow.Name)}, nil); err != nil {
		return false, ctrl.Result{}, err
	}
	return true, ctrl.Result{}, nil
}

func (r *WorkflowReconciler) releaseBootstrapWriter(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) error {
	if workflow.Status.BootstrapWriterEpoch < 1 || workflow.Status.BootstrapWriterReleased {
		return nil
	}
	holder, err := controllers.WorkspaceWriterIdentity("WorkflowBootstrap", workflow)
	if err != nil {
		return err
	}
	grant := controllers.WorkspaceWriterGrant{
		LeaseName: workflow.Status.WorkspaceWriterLeaseRef, HolderIdentity: holder, Epoch: workflow.Status.BootstrapWriterEpoch,
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	if err := controllers.ReleaseWorkspaceWriter(ctx, r.Client, workflow.Namespace, grant, now); err != nil {
		return err
	}
	updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
		latest.Status.BootstrapWriterReleased = true
	})
	if err != nil {
		return err
	}
	workflow.Status = updated.Status
	return nil
}

func (r *WorkflowReconciler) completeBootstrap(ctx context.Context, workflow *v1alpha1.SovereignWorkflow, artifact *v1alpha1.Artifact) error {
	alreadyReady := apiMeta.IsStatusConditionTrue(workflow.Status.Conditions, "BootstrapReady")
	updated, err := r.updateWorkflowStatus(ctx, client.ObjectKeyFromObject(workflow), func(latest *v1alpha1.SovereignWorkflow) {
		latest.Status.BootstrapArtifactRef = &v1alpha1.UIDReference{Name: artifact.Name, UID: artifact.UID}
		latest.Status.ObservedGeneration = latest.Generation
		apiMeta.SetStatusCondition(&latest.Status.Conditions, metav1.Condition{
			Type: "BootstrapReady", Status: metav1.ConditionTrue, Reason: "ChangeRequestAccepted",
			Message:            "the exact submitted change request is stored and accepted",
			ObservedGeneration: latest.Generation, LastTransitionTime: metav1.Now(),
		})
	})
	if err != nil {
		return err
	}
	workflow.Status = updated.Status
	if !alreadyReady {
		decision := workflowAdmissionDecision(updated, artifact)
		if err := r.appendWorkflowEvent(ctx, updated, "WorkflowAdmitted", "", 0, "accept", artifact.Name, "accepted", "",
			map[string]string{"artifact": artifact.Name, "artifactUID": string(artifact.UID), "digest": artifact.Spec.Digest}, decision); err != nil {
			return err
		}
	}
	return r.cleanupBootstrapResources(ctx, updated)
}

func workflowAdmissionDecision(workflow *v1alpha1.SovereignWorkflow, artifact *v1alpha1.Artifact) audit.DecisionEvaluated {
	contract := artifact.Spec.Contract.Name + "/" + artifact.Spec.Contract.Version
	expectedEvidence := workflow.Spec.Bootstrap.Contract.Name + "/" + workflow.Spec.Bootstrap.Contract.Version + "@" + workflow.Spec.Bootstrap.ExpectedDigest
	observedEvidence := contract + "@" + artifact.Spec.Digest
	decisionID := audit.DeterministicID(
		"workflow-admission",
		string(workflow.UID),
		string(artifact.UID),
		artifact.Spec.Digest,
		workflow.Spec.DefinitionRevision,
	)
	return audit.DecisionEvaluated{
		SchemaVersion: audit.PayloadSchemaVersionV1,
		Primitive: audit.ResourceRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			APIVersion:    v1alpha1.GroupVersion.String(),
			Kind:          "SovereignWorkflow",
			Namespace:     workflow.Namespace,
			Name:          workflow.Name,
			UID:           string(workflow.UID),
		},
		Decision: audit.DecisionRef{
			SchemaVersion: audit.PayloadSchemaVersionV1,
			ID:            decisionID,
			Kind:          "controller",
			Revision:      workflow.Spec.DefinitionRevision,
			InputDigest:   artifact.Spec.Digest,
			Outcome:       "allowed",
		},
		Invariants: []audit.InvariantResult{
			{
				SchemaVersion: audit.PayloadSchemaVersionV1,
				ID:            "bootstrap-spec-valid",
				Outcome:       "passed",
				Expected:      expectedEvidence,
				Observed:      observedEvidence,
				Reason:        "the workflow bootstrap declaration is valid",
			},
			{
				SchemaVersion: audit.PayloadSchemaVersionV1,
				ID:            "bootstrap-artifact-identity-valid",
				Outcome:       "passed",
				Expected:      string(workflow.UID),
				Observed:      string(artifact.Spec.WorkflowRef.UID),
				Reason:        "the accepted artifact is bound to this workflow",
			},
			{
				SchemaVersion: audit.PayloadSchemaVersionV1,
				ID:            "bootstrap-artifact-accepted",
				Outcome:       "passed",
				Expected:      string(v1alpha1.PhaseSucceeded),
				Observed:      string(artifact.Status.Phase),
				Reason:        "the exact change-request artifact passed validation",
			},
		},
	}
}

func (r *WorkflowReconciler) acceptedBootstrapArtifact(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) (*v1alpha1.Artifact, error) {
	ref := workflow.Status.BootstrapArtifactRef
	if ref == nil {
		return nil, fmt.Errorf("BootstrapReady has no Artifact reference")
	}

	var artifact v1alpha1.Artifact
	key := types.NamespacedName{
		Namespace: workflow.Namespace,
		Name:      workflow.Status.BootstrapArtifactRef.Name,
	}
	if err := r.Client.Get(ctx, key, &artifact); err != nil {
		return nil, err
	}
	if artifact.UID != workflow.Status.BootstrapArtifactRef.UID {
		return nil, fmt.Errorf("bootstrap Artifact UID changed")
	}
	if !artifacts.ArtifactAccepted(&artifact) {
		return nil, fmt.Errorf("bootstrap Artifact is not accepted")
	}
	return &artifact, nil
}

func (r *WorkflowReconciler) cleanupBootstrapResources(ctx context.Context, workflow *v1alpha1.SovereignWorkflow) error {
	var source corev1.ConfigMap
	sourceKey := types.NamespacedName{Namespace: workflow.Namespace, Name: workflow.Spec.Bootstrap.SourceRef.Name}
	if err := r.Client.Get(ctx, sourceKey, &source); err == nil {
		if source.UID != workflow.Spec.Bootstrap.SourceRef.UID {
			return fmt.Errorf("refusing to delete recreated bootstrap ConfigMap")
		}
		if err := r.Delete(ctx, &source); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	var job batchv1.Job
	jobKey := types.NamespacedName{Namespace: workflow.Namespace, Name: controllers.BootstrapJobName(workflow.Name)}
	if err := r.Client.Get(ctx, jobKey, &job); err == nil {
		if !metav1.IsControlledBy(&job, workflow) {
			return fmt.Errorf("refusing to delete bootstrap Job not controlled by workflow")
		}
		if err := r.Delete(ctx, &job); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}

func artifactConditionMessage(artifact *v1alpha1.Artifact) string {
	condition := apiMeta.FindStatusCondition(artifact.Status.Conditions, "Valid")
	if condition == nil || condition.Message == "" {
		return "bootstrap Artifact was rejected"
	}
	return condition.Message
}
