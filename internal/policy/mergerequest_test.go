package policy

import (
	"context"
	"testing"
)

func TestMergeRequestPolicyRequiresRepositoryCredential(t *testing.T) {
	evaluator, err := NewMVP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, class         string
		credential, allowed bool
	}{
		{"repository credential", "repository", true, true},
		{"missing credential", "repository", false, false},
		{"wrong credential class", "registry", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision, err := evaluator.Evaluate(context.Background(), map[string]any{
				"operation": "utility.execute",
				"request":   map[string]any{"operation": "git.mergeRequest", "credentialClass": test.class, "hasCredential": test.credential},
			})
			if err != nil {
				t.Fatal(err)
			}
			if decision.Allowed != test.allowed {
				t.Fatalf("allowed = %v, want %v", decision.Allowed, test.allowed)
			}
		})
	}
}
