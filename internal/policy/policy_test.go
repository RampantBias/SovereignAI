package policy

import (
	"context"
	"testing"
)

const testPolicy = `package sovereign

default decision := {"allowed": false, "reasons": ["classification mismatch"], "evict": []}

decision := {"allowed": true, "reasons": [], "evict": []} if {
  input.request.classification == input.endpoint.classification
  input.request.model == input.endpoint.model
}`

func TestOPAEvaluatorExplainsSharingDecision(t *testing.T) {
	evaluator, err := NewOPA(context.Background(), "policy-v1", "data.sovereign.decision", testPolicy)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := evaluator.Evaluate(context.Background(), map[string]any{
		"request":  map[string]any{"classification": "confidential", "model": "code"},
		"endpoint": map[string]any{"classification": "confidential", "model": "code"},
	})
	if err != nil || !allowed.Allowed || allowed.ID != "policy-v1" {
		t.Fatalf("unexpected allow decision: %#v, %v", allowed, err)
	}
	denied, err := evaluator.Evaluate(context.Background(), map[string]any{
		"request":  map[string]any{"classification": "classified", "model": "code"},
		"endpoint": map[string]any{"classification": "public", "model": "code"},
	})
	if err != nil || denied.Allowed || len(denied.Reasons) == 0 {
		t.Fatalf("unexpected deny decision: %#v, %v", denied, err)
	}
}

func TestMVPPolicyGovernsUtilityOperations(t *testing.T) {
	evaluator, err := NewMVP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		request map[string]any
		allowed bool
	}{
		{name: "test operation", allowed: true, request: map[string]any{
			"operation": "test.run", "workflow": "wf", "step": "tests", "project": "project",
		}},
		{name: "push without repository credential", allowed: false, request: map[string]any{
			"operation": "git.push", "credentialClass": "repository", "hasCredential": false,
			"parameters": map[string]string{"branch": "feature"},
		}},
		{name: "push with repository credential", allowed: true, request: map[string]any{
			"operation": "git.push", "credentialClass": "repository", "hasCredential": true,
			"parameters": map[string]string{"branch": "feature"},
		}},
		{name: "merge without decisions", allowed: false, request: map[string]any{
			"operation": "git.merge", "parameters": map[string]string{"sourceBranch": "feature", "candidateRevision": "abc"},
		}},
		{name: "merge with approval and validation", allowed: true, request: map[string]any{
			"operation": "git.merge", "credentialClass": "repository", "hasCredential": true, "parameters": map[string]string{
				"sourceBranch": "feature", "candidateRevision": "abc", "approvalDecisionRef": "approval-1", "validationRunRef": "validation-1",
			},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision, err := evaluator.Evaluate(context.Background(), map[string]any{"operation": "utility.execute", "request": test.request})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed != test.allowed {
				t.Fatalf("allowed = %v, want %v; decision=%#v", decision.Allowed, test.allowed, decision)
			}
		})
	}
}
