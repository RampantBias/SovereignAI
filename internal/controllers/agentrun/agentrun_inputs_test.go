package agentrun

import (
	"context"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAgentPodFailureReadsStructuredBoundedDiagnostic(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "test-author-001"},
		Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{
			Name: "agent",
			State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
				Message: `{"code":"TestPathNotRecognized","message":"test change set file \"src/main.go\" is not a recognized test path"}`,
			}},
		}}},
	}
	code, message := agentPodFailure(pod)
	if code != "TestPathNotRecognized" || !strings.Contains(message, "src/main.go") {
		t.Fatalf("unexpected pod diagnostic: code=%q message=%q", code, message)
	}

	pod.Status.ContainerStatuses[0].State.Terminated.Message = `{"code":"invalid-code","message":"ignore me"}`
	code, message = agentPodFailure(pod)
	if code != "AgentPodFailed" || !strings.Contains(message, "without a valid structured diagnostic") {
		t.Fatalf("invalid pod diagnostic was trusted: code=%q message=%q", code, message)
	}
}

func TestResolveAgentInputsSelectsAcceptedArtifactByLogicalNameAndDigest(t *testing.T) {
	scheme := attemptScheme(t)
	want := acceptedAgentInputArtifact(
		"initialize-repository-a1-repository-revision-v1-00",
		"repository-revision",
		"v1",
		"sha256:wanted",
		"/workspace/.sovereign/artifacts/wanted.json",
		workflowRef(workflowFixture()),
	)
	otherDigest := acceptedAgentInputArtifact(
		"initialize-repository-a2-repository-revision-v1-00",
		"repository-revision",
		"v1",
		"sha256:other",
		"/workspace/.sovereign/artifacts/other.json",
		workflowRef(workflowFixture()),
	)
	wrongWorkflow := acceptedAgentInputArtifact(
		"other-workflow-repository-revision-v1-00",
		"repository-revision",
		"v1",
		"sha256:wanted",
		"/workspace/.sovereign/artifacts/wrong-workflow.json",
		v1alpha1.UIDReference{Name: "other", UID: "other-uid"},
	)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(want, otherDigest, wrongWorkflow).
		Build()
	reconciler := &AgentRunReconciler{Client: kubeClient, Scheme: scheme}
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "architect-a1", Namespace: "wf"},
		Spec: v1alpha1.AgentRunSpec{
			WorkflowRef: workflowRef(workflowFixture()),
			Inputs: []v1alpha1.ArtifactReference{{
				Name:   "repository-revision",
				Digest: "sha256:wanted",
			}},
		},
	}

	resolved, ready, invalidReason, err := reconciler.resolveAgentInputs(context.Background(), run)
	if err != nil {
		t.Fatalf("resolveAgentInputs: %v", err)
	}
	if !ready || invalidReason != "" {
		t.Fatalf("resolution ready = %t, invalid reason = %q", ready, invalidReason)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved input count = %d, want 1", len(resolved))
	}
	if resolved[0].Name != "repository-revision" {
		t.Fatalf("resolved logical name = %q, want repository-revision", resolved[0].Name)
	}
	if resolved[0].Contract != "repository-revision/v1" ||
		resolved[0].Digest != want.Spec.Digest ||
		resolved[0].Path != want.Spec.Path {
		t.Fatalf("unexpected resolved input: %#v", resolved[0])
	}
}

func TestResolveAgentInputsRejectsAmbiguousAcceptedArtifacts(t *testing.T) {
	scheme := attemptScheme(t)
	first := acceptedAgentInputArtifact(
		"architect-a1-implementation-plan-v1-00",
		"implementation-plan",
		"v1",
		"sha256:first",
		"/workspace/.sovereign/artifacts/first.json",
		workflowRef(workflowFixture()),
	)
	second := acceptedAgentInputArtifact(
		"architect-a2-implementation-plan-v1-00",
		"implementation-plan",
		"v1",
		"sha256:second",
		"/workspace/.sovereign/artifacts/second.json",
		workflowRef(workflowFixture()),
	)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(first, second).Build()
	reconciler := &AgentRunReconciler{Client: kubeClient, Scheme: scheme}
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "test-author-a1", Namespace: "wf"},
		Spec: v1alpha1.AgentRunSpec{
			WorkflowRef: workflowRef(workflowFixture()),
			Inputs:      []v1alpha1.ArtifactReference{{Name: "implementation-plan"}},
		},
	}

	_, ready, invalidReason, err := reconciler.resolveAgentInputs(context.Background(), run)
	if err != nil {
		t.Fatalf("resolveAgentInputs: %v", err)
	}
	if ready || !strings.Contains(invalidReason, "resolves to 2 accepted artifacts") {
		t.Fatalf("resolution ready = %t, invalid reason = %q", ready, invalidReason)
	}
}

func TestResolveAgentInputsWaitsForAcceptanceAndIgnoresOtherWorkflows(t *testing.T) {
	scheme := attemptScheme(t)
	pending := acceptedAgentInputArtifact(
		"architect-a1-implementation-plan-v1-00",
		"implementation-plan",
		"v1",
		"sha256:pending",
		"/workspace/.sovereign/artifacts/pending.json",
		workflowRef(workflowFixture()),
	)
	pending.Status = v1alpha1.ArtifactStatus{Phase: v1alpha1.PhasePending}
	wrongWorkflow := acceptedAgentInputArtifact(
		"other-architect-a1-implementation-plan-v1-00",
		"implementation-plan",
		"v1",
		"sha256:accepted",
		"/workspace/.sovereign/artifacts/other.json",
		v1alpha1.UIDReference{Name: "other", UID: "other-uid"},
	)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pending, wrongWorkflow).Build()
	reconciler := &AgentRunReconciler{Client: kubeClient, Scheme: scheme}
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "test-author-a1", Namespace: "wf"},
		Spec: v1alpha1.AgentRunSpec{
			WorkflowRef: workflowRef(workflowFixture()),
			Inputs:      []v1alpha1.ArtifactReference{{Name: "implementation-plan"}},
		},
	}

	resolved, ready, invalidReason, err := reconciler.resolveAgentInputs(context.Background(), run)
	if err != nil {
		t.Fatalf("resolveAgentInputs: %v", err)
	}
	if ready || invalidReason != "" || len(resolved) != 0 {
		t.Fatalf("resolved = %#v, ready = %t, invalid reason = %q", resolved, ready, invalidReason)
	}
}

func TestResolveAgentInputsRejectsDigestMismatchAndRejectedArtifact(t *testing.T) {
	tests := []struct {
		name      string
		requested v1alpha1.ArtifactReference
		artifact  *v1alpha1.Artifact
		want      string
	}{
		{
			name:      "digest mismatch",
			requested: v1alpha1.ArtifactReference{Name: "implementation-plan", Digest: "sha256:wanted"},
			artifact: acceptedAgentInputArtifact(
				"architect-a1-implementation-plan-v1-00",
				"implementation-plan",
				"v1",
				"sha256:other",
				"/workspace/.sovereign/artifacts/other.json",
				workflowRef(workflowFixture()),
			),
			want: "no accepted artifact with digest",
		},
		{
			name:      "rejected artifact",
			requested: v1alpha1.ArtifactReference{Name: "implementation-plan"},
			artifact: func() *v1alpha1.Artifact {
				artifact := acceptedAgentInputArtifact(
					"architect-a1-implementation-plan-v1-00",
					"implementation-plan",
					"v1",
					"sha256:rejected",
					"/workspace/.sovereign/artifacts/rejected.json",
					workflowRef(workflowFixture()),
				)
				artifact.Status = v1alpha1.ArtifactStatus{Phase: v1alpha1.PhaseFailed}
				return artifact
			}(),
			want: "was rejected",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scheme := attemptScheme(t)
			kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(test.artifact).Build()
			reconciler := &AgentRunReconciler{Client: kubeClient, Scheme: scheme}
			run := &v1alpha1.AgentRun{
				ObjectMeta: metav1.ObjectMeta{Name: "test-author-a1", Namespace: "wf"},
				Spec: v1alpha1.AgentRunSpec{
					WorkflowRef: workflowRef(workflowFixture()),
					Inputs:      []v1alpha1.ArtifactReference{test.requested},
				},
			}

			_, ready, invalidReason, err := reconciler.resolveAgentInputs(context.Background(), run)
			if err != nil {
				t.Fatalf("resolveAgentInputs: %v", err)
			}
			if ready || !strings.Contains(invalidReason, test.want) {
				t.Fatalf("resolution ready = %t, invalid reason = %q, want %q", ready, invalidReason, test.want)
			}
		})
	}
}
