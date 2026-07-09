package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "aim.sovereign.io", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &runtime.SchemeBuilder{AddKnownTypes}

	// AddToScheme applies all the stored functions to a scheme
	AddToScheme = SchemeBuilder.AddToScheme
)

// AddKnownTypes adds the list of known types to Scheme.
func AddKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion,
		&SovereignWorkflow{},
		&SovereignWorkflowList{},
		&SovereignProject{},
		&SovereignProjectList{},
		&StepAttempt{},
		&StepAttemptList{},
		&Artifact{},
		&ArtifactList{},
		&HumanSession{},
		&HumanSessionList{},
		&ValidationRun{},
		&ValidationRunList{},
		&InferenceEndpoint{},
		&InferenceEndpointList{},
		&InferenceLease{},
		&InferenceLeaseList{},
		&PolicyProfile{},
		&PolicyProfileList{},
		&GPUNode{},
		&GPUNodeList{},
	)
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}
