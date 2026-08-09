package controllers

import (
	"context"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestResolveAgentInputsSelectsAcceptedArtifactByLogicalNameAndDigest(t *testing.T) {
	scheme := attemptScheme(t)
	want := acceptedAgentInputArtifact(
		"initialize-repository-a1-repository-revision-v1-00",
		"repository-revision",
		"v1",
		"sha256:wanted",
		"/workspace/.sovereign/artifacts/wanted.json",
		workflowRefFixture(),
	)
	otherDigest := acceptedAgentInputArtifact(
		"initialize-repository-a2-repository-revision-v1-00",
		"repository-revision",
		"v1",
		"sha256:other",
		"/workspace/.sovereign/artifacts/other.json",
		workflowRefFixture(),
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
			WorkflowRef: workflowRefFixture(),
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
		workflowRefFixture(),
	)
	second := acceptedAgentInputArtifact(
		"architect-a2-implementation-plan-v1-00",
		"implementation-plan",
		"v1",
		"sha256:second",
		"/workspace/.sovereign/artifacts/second.json",
		workflowRefFixture(),
	)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(first, second).Build()
	reconciler := &AgentRunReconciler{Client: kubeClient, Scheme: scheme}
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "test-author-a1", Namespace: "wf"},
		Spec: v1alpha1.AgentRunSpec{
			WorkflowRef: workflowRefFixture(),
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
		workflowRefFixture(),
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
			WorkflowRef: workflowRefFixture(),
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
				workflowRefFixture(),
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
					workflowRefFixture(),
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
					WorkflowRef: workflowRefFixture(),
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

func acceptedAgentInputArtifact(name, contractName, contractVersion, digest, path string, workflowRef v1alpha1.UIDReference) *v1alpha1.Artifact {
	const generation = int64(1)
	return &v1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{
			Name:       name,
			Namespace:  "wf",
			Generation: generation,
		},
		Spec: v1alpha1.ArtifactSpec{
			WorkflowRef: workflowRef,
			Contract: v1alpha1.ContractReference{
				Name:    contractName,
				Version: contractVersion,
			},
			Digest: digest,
			Path:   path,
		},
		Status: v1alpha1.ArtifactStatus{
			ObservedGeneration: generation,
			Phase:              v1alpha1.PhaseSucceeded,
			Conditions: []metav1.Condition{{
				Type:               "Valid",
				Status:             metav1.ConditionTrue,
				Reason:             "ContractAccepted",
				ObservedGeneration: generation,
			}},
		},
	}
}
