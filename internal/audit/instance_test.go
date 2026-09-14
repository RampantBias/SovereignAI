package audit

import "testing"

func TestExecutionIdentitySeparatesRewindsWithoutDuplicatingReconciliation(t *testing.T) {
	for _, source := range []string{"utilityoperation-controller", "utility-runner"} {
		t.Run(source, func(t *testing.T) {
			options := EventOptions{Source: source, Type: "Admitted", Subject: Subject{Namespace: "wf-ns", Workflow: "wf", Step: "prepare-candidate", Attempt: 1}, Action: "admit", Target: "candidate.prepare", Outcome: "allowed", InstanceID: "w000-operation-uid"}
			first, err := NewEvent(options)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := NewEvent(options)
			if err != nil {
				t.Fatal(err)
			}
			options.InstanceID = "w001-operation-uid"
			rewound, err := NewEvent(options)
			if err != nil {
				t.Fatal(err)
			}
			if first.ID != repeated.ID || first.ID == rewound.ID {
				t.Fatal("audit identity does not distinguish replay from rewind")
			}
		})
	}
}
