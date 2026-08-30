package v1alpha1

import (
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	ProjectConditionConfigurationValid      = "ConfigurationValid"
	ProjectConditionValidationProviderReady = "ValidationProviderReady"
)

// ProjectReady requires both decisions to describe the current spec generation.
func ProjectReady(project *SovereignProject) bool {
	if project == nil || !project.DeletionTimestamp.IsZero() || project.Status.ValidationProviderRef == "" {
		return false
	}
	for _, kind := range []string{ProjectConditionConfigurationValid, ProjectConditionValidationProviderReady} {
		condition := apiMeta.FindStatusCondition(project.Status.Conditions, kind)
		if condition == nil || condition.Status != metav1.ConditionTrue || condition.ObservedGeneration != project.Generation {
			return false
		}
	}
	return true
}
