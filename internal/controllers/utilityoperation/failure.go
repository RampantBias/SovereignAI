package utilityoperation

import (
	"context"
	"encoding/json"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/utilitycontract"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *UtilityOperationReconciler) recordJobFailure(ctx context.Context, operation *v1alpha1.UtilityOperation, job *batchv1.Job) error {
	operation.Status.FailureReason = "UtilityJobFailed"
	operation.Status.FailureMessage = ""
	operation.Status.Retryable = true
	if job.UID == "" || !metav1.IsControlledBy(job, operation) {
		return nil
	}
	reader := r.Reader
	if reader == nil {
		reader = r.Client
	}
	var pods corev1.PodList
	if err := reader.List(ctx, &pods, client.InNamespace(job.Namespace),
		client.MatchingLabels{"batch.kubernetes.io/controller-uid": string(job.UID)}); err != nil {
		return err
	}
	for _, pod := range pods.Items {
		if !metav1.IsControlledBy(&pod, job) {
			continue
		}
		for _, status := range pod.Status.ContainerStatuses {
			terminated := status.State.Terminated
			if status.Name != "utility" || terminated == nil || terminated.ExitCode == 0 || terminated.Message == "" {
				continue
			}
			var detail utilitycontract.ResultError
			if err := json.Unmarshal([]byte(terminated.Message), &detail); err != nil {
				continue
			}
			feedback := agentcontract.RetryFeedback{
				PreviousAttemptRef: operation.Spec.AttemptRef,
				Code:               detail.Code, Message: agentcontract.SanitizeRetryFeedbackMessage(detail.Message),
			}
			if err := feedback.Validate(); err != nil {
				continue
			}
			operation.Status.FailureReason = feedback.Code
			operation.Status.FailureMessage = feedback.Message
			return nil
		}
	}
	return nil
}
