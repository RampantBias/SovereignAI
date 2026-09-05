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
				"request":   map[string]any{"operation": "git.mergeRequest", "credentialClass": test.class, "hasCredential": test.credential, "approval": map[string]any{"admitted": true, "subjectMatches": true, "binding": map[string]string{"admissionEventId": "admission"}}},
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

func TestMergeRequestPolicyRequiresVerifiedApproval(t *testing.T) {
	evaluator, err := NewMVP(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		approval any
		allowed  bool
	}{
		{"absent", nil, false},
		{"unadmitted", map[string]any{"admitted": false, "subjectMatches": true, "binding": map[string]string{"admissionEventId": "admission"}}, false},
		{"wrong subject", map[string]any{"admitted": true, "subjectMatches": false, "binding": map[string]string{"admissionEventId": "admission"}}, false},
		{"no evidence link", map[string]any{"admitted": true, "subjectMatches": true}, false},
		{"verified", map[string]any{"admitted": true, "subjectMatches": true, "binding": map[string]string{"admissionEventId": "admission"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := map[string]any{"operation": "git.mergeRequest", "credentialClass": "repository", "hasCredential": true, "approval": tc.approval,
				"parameters": map[string]string{"approvalDecisionRef": "fabricated"}}
			result, err := evaluator.Evaluate(context.Background(), map[string]any{"operation": "utility.execute", "request": request})
			if err != nil || result.Allowed != tc.allowed {
				t.Fatalf("decision=%#v err=%v", result, err)
			}
		})
	}
}
