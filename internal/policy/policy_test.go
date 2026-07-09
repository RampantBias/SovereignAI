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
