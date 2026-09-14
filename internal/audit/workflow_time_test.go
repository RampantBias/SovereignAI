package audit

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
)

func TestWorkflowEventsKeepSubmillisecondOrder(t *testing.T) {
	wf := viewWorkflow(t)
	a := viewAttempt("test-1", "test-candidate", "Utility", "test", 0, 1, v1alpha1.PhaseFailed)
	base := time.Date(2026, 9, 11, 12, 0, 0, 123456000, time.UTC)
	events := []Event{
		{ID: "a-later", Type: "StepAttemptFailed", SchemaVersion: "v1", OccurredAt: base.Add(time.Nanosecond), Subject: Subject{Workflow: wf.Name, Namespace: wf.Namespace, Step: a.Spec.StepName, Attempt: 1}, Target: a.Name},
		{ID: "z-earlier", Type: "StepAttemptRunning", SchemaVersion: "v1", OccurredAt: base, Subject: Subject{Workflow: wf.Name, Namespace: wf.Namespace, Step: a.Spec.StepName, Attempt: 1}, Target: a.Name},
	}
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &events); err != nil {
		t.Fatal(err)
	}
	v, err := BuildWorkflowView(wf, []v1alpha1.StepAttempt{a}, nil, events)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Attempts) != 1 || len(v.Attempts[0].Checkpoints) != 2 {
		t.Fatal("missing events")
	}
	checkpoints := v.Attempts[0].Checkpoints
	if checkpoints[0].EventID != "z-earlier" || checkpoints[1].EventID != "a-later" {
		t.Fatal("event IDs overrode timestamp precision")
	}
}
