package controllers

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// validateDomainAuthority proves the bidirectional relationship between a
// workflow attempt and the domain primitive that is allowed to act for it.
// A false result without an error means the workflow controller has not yet
// published status.executionRef and reconciliation should wait.
func validateDomainAuthority(ctx context.Context, reader client.Reader, object client.Object, attemptRef string, expectedKind v1alpha1.ExecutionKind, workflowRef, stepName string, attemptNumber int32) (bool, error) {
	var attempt v1alpha1.StepAttempt
	if err := reader.Get(ctx, types.NamespacedName{Namespace: object.GetNamespace(), Name: attemptRef}, &attempt); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if err := validateDomainBinding(&attempt, object, attemptRef, expectedKind, workflowRef, stepName, attemptNumber); err != nil {
		return false, err
	}
	if attempt.Status.ExecutionRef == nil {
		return false, nil
	}
	wantKind := domainKind(expectedKind)
	if attempt.Status.ExecutionRef.APIVersion != v1alpha1.GroupVersion.String() || attempt.Status.ExecutionRef.Kind != wantKind || attempt.Status.ExecutionRef.Name != object.GetName() {
		return false, fmt.Errorf("StepAttempt %s does not authorize %s/%s", attempt.Name, wantKind, object.GetName())
	}
	return true, nil
}

// Verifies
func validateDomainBinding(attempt *v1alpha1.StepAttempt, object client.Object, attemptRef string, expectedKind v1alpha1.ExecutionKind, workflowRef, stepName string, attemptNumber int32) error {
	if attemptRef != attempt.Name || !metav1.IsControlledBy(object, attempt) {
		return fmt.Errorf("%T %s is not controlled by StepAttempt %s", object, object.GetName(), attempt.Name)
	}
	if attempt.Spec.Kind != expectedKind || attempt.Spec.WorkflowRef != workflowRef || attempt.Spec.StepName != stepName || attempt.Spec.Attempt != attemptNumber {
		return fmt.Errorf("domain execution identity does not match StepAttempt %s", attempt.Name)
	}
	return nil
}

func domainKind(kind v1alpha1.ExecutionKind) string {
	switch kind {
	case v1alpha1.ExecutionKindAgent:
		return "AgentRun"
	case v1alpha1.ExecutionKindUtility:
		return "UtilityOperation"
	case v1alpha1.ExecutionKindHumanGate:
		return "ApprovalRequest"
	case v1alpha1.ExecutionKindValidation:
		return "ValidationRun"
	default:
		return ""
	}
}
