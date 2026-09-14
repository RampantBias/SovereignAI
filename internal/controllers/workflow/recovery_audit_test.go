package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func recoveryTestCreate(ctx context.Context, c client.WithWatch, object client.Object, opts ...client.CreateOption) error {
	if object.GetUID() == "" {
		object.SetUID(types.UID("uid-" + object.GetName()))
	}
	return c.Create(ctx, object, opts...)
}

type recoveryRecorder struct {
	*audit.MemoryRecorder
	failType  string
	attempted []audit.Event
}

func (r *recoveryRecorder) Append(ctx context.Context, event audit.Event) error {
	r.attempted = append(r.attempted, event)
	if event.Type == r.failType {
		r.failType = ""
		return errors.New("audit unavailable")
	}
	return r.MemoryRecorder.Append(ctx, event)
}

// A real two-hop consumed path plus a later author output that was never consumed.
func recoveryAuditFixture(t *testing.T, iterations ...int32) (*WorkflowReconciler, *v1alpha1.SovereignWorkflow, *v1alpha1.StepAttempt, *recoveryRecorder) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, coordinationv1.AddToScheme, batchv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	var iteration int32
	if len(iterations) != 0 {
		iteration = iterations[0]
	}
	workflow := changeRequestWorkflow()
	workflow.Status.WorkflowAttempt = iteration
	workflow.Status.WorkspaceWriterLeaseRef = "wf-1-workspace-writer"
	workflow.Status.BootstrapWriterReleased = true
	workflow.Spec.MaxWorkflowAttempt = 2
	workflow.Spec.Steps = []v1alpha1.StepConfig{
		{Name: "test-author", Kind: v1alpha1.ExecutionKindAgent, Order: 1,
			Agent:  &v1alpha1.AgentStepSpec{Image: "agent:test", Executable: []string{"/reference-agent"}, Responsibility: "fix stale tests"},
			Inputs: []v1alpha1.ArtifactReference{{Name: "change-request"}}},
		{Name: "prepare-candidate", Kind: v1alpha1.ExecutionKindUtility, Order: 2},
		{Name: "test-candidate", Kind: v1alpha1.ExecutionKindUtility, Order: 3, MaxAttempts: 3, Utility: &v1alpha1.UtilityOperationRequest{Name: "test.run"}},
	}
	workflow.Status.Phase = string(v1alpha1.PhaseRunning)
	var objects []client.Object
	makeAttempt := func(step string, kind v1alpha1.ExecutionKind, retry int32) *v1alpha1.StepAttempt {
		a := &v1alpha1.StepAttempt{ObjectMeta: metav1.ObjectMeta{Name: attemptName(step, iteration, retry), Namespace: workflow.Namespace, UID: types.UID(step + "-" + string(rune('0'+retry)))},
			Spec:   v1alpha1.StepAttemptSpec{WorkflowRef: recoveryUID(workflow), StepName: step, Kind: kind, RetryNumber: retry, WorkflowAttempt: iteration},
			Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhaseSucceeded}}
		if err := controllerutil.SetControllerReference(workflow, a, scheme); err != nil {
			t.Fatal(err)
		}
		objects = append(objects, a)
		return a
	}
	makeExecution := func(a *v1alpha1.StepAttempt, inputs []v1alpha1.ArtifactReference) client.Object {
		meta := metav1.ObjectMeta{Name: a.Name, Namespace: a.Namespace, UID: types.UID("execution-" + string(a.UID))}
		var object client.Object
		kind := "UtilityOperation"
		if a.Spec.Kind == v1alpha1.ExecutionKindAgent {
			kind = "AgentRun"
			object = &v1alpha1.AgentRun{ObjectMeta: meta, Spec: v1alpha1.AgentRunSpec{WorkflowRef: a.Spec.WorkflowRef, AttemptRef: a.Name, StepName: a.Spec.StepName, Attempt: a.Spec.RetryNumber, Inputs: inputs}}
		} else {
			object = &v1alpha1.UtilityOperation{ObjectMeta: meta, Spec: v1alpha1.UtilityOperationSpec{WorkflowRef: a.Spec.WorkflowRef, AttemptRef: a.Name, StepName: a.Spec.StepName, Attempt: a.Spec.RetryNumber, Inputs: inputs}}
		}
		if err := controllerutil.SetControllerReference(a, object, scheme); err != nil {
			t.Fatal(err)
		}
		a.Status.ExecutionRef = &v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: kind, Name: a.Name}
		objects = append(objects, object)
		return object
	}
	makeArtifact := func(a *v1alpha1.StepAttempt, execution client.Object, name, contract string) v1alpha1.ArtifactReference {
		artifact := acceptedLineageArtifact(name, types.UID(name+"-uid"), recoveryUID(workflow), a.Name, contract, "sha256:"+name)
		artifact.Namespace = workflow.Namespace
		artifact.Spec.ProducerRef = *a.Status.ExecutionRef
		artifact.Spec.ProducerUID = execution.GetUID()
		artifact.Spec.ProducerGrantRef = recoveryUID(a)
		objects = append(objects, artifact)
		return recoveryArtifactPin(artifact)
	}
	author := makeAttempt("test-author", v1alpha1.ExecutionKindAgent, 1)
	tests := makeArtifact(author, makeExecution(author, nil), "consumed-tests", "test-change-set")
	later := makeAttempt("test-author", v1alpha1.ExecutionKindAgent, 2)
	makeArtifact(later, makeExecution(later, nil), "unconsumed-tests", "test-change-set")
	prepare := makeAttempt("prepare-candidate", v1alpha1.ExecutionKindUtility, 1)
	candidate := makeArtifact(prepare, makeExecution(prepare, []v1alpha1.ArtifactReference{tests}), "prepared", "prepared-candidate")
	failed := makeAttempt("test-candidate", v1alpha1.ExecutionKindUtility, 2)
	failed.Status.Phase = v1alpha1.PhaseFailed
	failed.Status.FailureReason = "TestRunCodeError"
	failed.Status.FailureMessage = "main_test.go:42: stale addition test remains"
	makeArtifact(failed, makeExecution(failed, []v1alpha1.ArtifactReference{candidate}), "failed-report", "test-report")
	workflow.Status.ActiveStepName = failed.Spec.StepName
	workflow.Status.ActiveAttemptRef = failed.Name
	objects = append(objects, workflow, changeRequestArtifact(workflow))
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}, &v1alpha1.AgentRun{}).
		WithInterceptorFuncs(interceptor.Funcs{Create: recoveryTestCreate}).Build()
	recorder := &recoveryRecorder{MemoryRecorder: audit.NewMemoryRecorder()}
	if err := recorder.Append(context.Background(), audit.Event{ID: "failure-event", Type: "StepAttemptFailed", Target: failed.Name, Subject: audit.Subject{Workflow: workflow.Name, Namespace: workflow.Namespace, Step: failed.Spec.StepName, Attempt: failed.Spec.RetryNumber}}); err != nil {
		t.Fatal(err)
	}
	return &WorkflowReconciler{Client: c, Reader: c, Scheme: scheme, Audit: recorder, Now: func() time.Time { return time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC) }}, workflow, failed, recorder
}

func TestWorkflowRecoveryAuditPersistsCausalPathAndSurvivesWriteFailures(t *testing.T) {
	for _, failType := range []string{"", "RecoveryDecisionSelected", "StepAttemptRetried"} {
		t.Run("fail-"+failType, func(t *testing.T) {
			ctx := context.Background()
			r, w, failed, recorder := recoveryAuditFixture(t)
			recorder.failType = failType
			err := r.beginWorkflowRetry(ctx, w, failed, "test-author")
			if (err != nil) != (failType == "RecoveryDecisionSelected") {
				t.Fatalf("rewind error: %v", err)
			}
			var updated v1alpha1.SovereignWorkflow
			if err := r.Client.Get(ctx, client.ObjectKeyFromObject(w), &updated); err != nil {
				t.Fatal(err)
			}
			evidence := updated.Status.Refinement.Recovery
			if updated.Status.WorkflowAttempt != 1 || evidence == nil || evidence.PreviousAttempt.Name != attemptName("test-author", 0, 1) || evidence.PreviousOutcome != v1alpha1.PhaseSucceeded || evidence.TriggerAttempt != recoveryUID(failed) {
				t.Fatalf("incorrect frozen recovery: %#v", evidence)
			}
			if len(evidence.Missing) != 0 || len(evidence.InputLinks) != 2 || len(evidence.Reports) != 1 || !reflect.DeepEqual(evidence.EvidenceEvents, []string{"failure-event"}) {
				t.Fatalf("incomplete evidence: %#v", evidence)
			}
			if evidence.InputLinks[0].Input.ArtifactRef.Name != "prepared" || evidence.InputLinks[1].Input.ArtifactRef.Name != "consumed-tests" {
				t.Fatalf("wrong consumed path: %#v", evidence.InputLinks)
			}
			// Frozen evidence and feedback survive source deletion and a controller restart.
			if err := r.Delete(ctx, failed); err != nil {
				t.Fatal(err)
			}
			restarted := &WorkflowReconciler{Client: r.Client, Reader: r.Reader, Scheme: r.Scheme, Audit: recorder}
			err = restarted.createAttempt(ctx, &updated, w.Spec.Steps[0], 1)
			if (err != nil) != (failType == "StepAttemptRetried") {
				t.Fatalf("retry error: %v", err)
			}
			if err != nil {
				var pending v1alpha1.StepAttempt
				if err := r.Client.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: attemptName("test-author", 1, 1)}, &pending); err != nil {
					t.Fatal(err)
				}
				if pending.Status.ExecutionRef != nil {
					t.Fatal("execution authorized before retry audit was saved")
				}
			}
			if err := restarted.createAttempt(ctx, &updated, w.Spec.Steps[0], 1); err != nil {
				t.Fatal(err)
			}
			if err := restarted.recordWorkflowRecovery(ctx, &updated); err != nil {
				t.Fatal(err)
			}
			counts := map[string]int{}
			var decision audit.WorkflowRecoveryDecision
			var retry audit.WorkflowRetryRecorded
			for _, event := range recorder.AllEvents() {
				counts[event.Type]++
				switch event.Type {
				case "RecoveryDecisionSelected":
					if err := json.Unmarshal(event.Data, &decision); err != nil {
						t.Fatal(err)
					}
					if !event.OccurredAt.Equal(evidence.SelectedAt.Time) {
						t.Fatal("decision timestamp drifted")
					}
				case "StepAttemptRetried":
					if err := json.Unmarshal(event.Data, &retry); err != nil {
						t.Fatal(err)
					}
					if event.CausationID != workflowRecoveryEventID(&updated) {
						t.Fatal("missing causal event")
					}
				}
			}
			if counts["RecoveryDecisionSelected"] != 1 || counts["StepAttemptRetried"] != 1 {
				t.Fatalf("duplicate or absent events: %v", counts)
			}
			if !reflect.DeepEqual(decision.Recovery, *evidence) || decision.Action != "RetryWorkflow" || decision.ToWorkflowAttempt != 1 {
				t.Fatalf("decision did not preserve frozen input: %#v", decision)
			}
			if retry.RetryOf == nil || *retry.RetryOf != *evidence.PreviousAttempt || retry.TriggeredBy != recoveryUID(failed) || retry.DecisionEvent != workflowRecoveryEventID(&updated) {
				t.Fatalf("incorrect retry ancestry: %#v", retry)
			}
			var run v1alpha1.AgentRun
			if err := r.Client.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: retry.Execution.Name}, &run); err != nil {
				t.Fatal(err)
			}
			if retry.Execution.UID != string(run.UID) || retry.Attempt.UID == "" || !reflect.DeepEqual(retry.Inputs, run.Spec.Inputs) || !reflect.DeepEqual(retry.Feedback, run.Spec.PriorAttemptRef) || retry.Feedback.Message != failed.Status.FailureMessage {
				t.Fatalf("retry does not describe stored execution: %#v", retry)
			}
			if len(retry.Inputs) != 1 || retry.Inputs[0].ArtifactRef == nil {
				t.Fatal("retry inputs are not pinned")
			}
			var prior v1alpha1.StepAttempt
			if err := r.Client.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: retry.RetryOf.Name}, &prior); err != nil {
				t.Fatal(err)
			}
			if prior.Status.Phase != v1alpha1.PhaseSucceeded {
				t.Fatal("successful prior author was relabeled failed")
			}
			var attempts v1alpha1.StepAttemptList
			if err := r.Client.List(ctx, &attempts); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, a := range attempts.Items {
				if a.Spec.WorkflowAttempt == 1 {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("created %d replacement attempts", count)
			}
			// Replayed event content is stable even if the first append did not persist.
			first := map[string]audit.Event{}
			for _, event := range recorder.attempted {
				if event.Type != "RecoveryDecisionSelected" && event.Type != "StepAttemptRetried" {
					continue
				}
				if old, ok := first[event.ID]; ok && (!reflect.DeepEqual(old.Data, event.Data) || !old.OccurredAt.Equal(event.OccurredAt)) {
					t.Fatal("replayed event content changed")
				}
				first[event.ID] = event
			}
		})
	}
}

func TestWorkflowRecoveryRejectsWrongPinsAndExposesMissingHistory(t *testing.T) {
	for _, scenario := range []string{"artifact UID", "producer UID", "missing artifact", "missing failure event"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			r, w, failed, _ := recoveryAuditFixture(t)
			var artifact v1alpha1.Artifact
			if err := r.Client.Get(ctx, types.NamespacedName{Namespace: w.Namespace, Name: "consumed-tests"}, &artifact); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "artifact UID":
				artifact.UID = "recreated"
			case "producer UID":
				artifact.Spec.ProducerUID = "recreated"
			case "missing artifact":
				if err := r.Delete(ctx, &artifact); err != nil {
					t.Fatal(err)
				}
			case "missing failure event":
				r.Audit = audit.NewMemoryRecorder()
			}
			if strings.HasSuffix(scenario, "UID") {
				if err := r.Update(ctx, &artifact); err != nil {
					t.Fatal(err)
				}
			}
			evidence, err := r.captureWorkflowRecovery(ctx, w, failed, "test-author")
			if strings.HasSuffix(scenario, "UID") {
				if err == nil {
					t.Fatal("accepted mismatched identity")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(evidence.Missing) == 0 {
				t.Fatal("missing evidence was hidden")
			}
			if scenario == "missing artifact" && evidence.PreviousAttempt != nil {
				t.Fatal("invented ancestry from the later unconsumed output")
			}
		})
	}
}

func TestWorkflowRecoveryAcrossIterations(t *testing.T) {
	ctx := context.Background()
	var ids []string
	for _, iteration := range []int32{0, 1} {
		r, w, failed, recorder := recoveryAuditFixture(t, iteration)
		if err := r.beginWorkflowRetry(ctx, w, failed, "test-author"); err != nil {
			t.Fatal(err)
		}
		// A stale reconcile must not increment the iteration a second time.
		if err := r.beginWorkflowRetry(ctx, w, failed, "test-author"); err != nil {
			t.Fatal(err)
		}
		var updated v1alpha1.SovereignWorkflow
		if err := r.Client.Get(ctx, client.ObjectKeyFromObject(w), &updated); err != nil {
			t.Fatal(err)
		}
		if updated.Status.WorkflowAttempt != iteration+1 || updated.Status.Refinement.Recovery.PreviousAttempt.Name != attemptName("test-author", iteration, 1) {
			t.Fatal("incorrect iteration ancestry")
		}
		count := 0
		for _, event := range recorder.AllEvents() {
			if event.Type == "RecoveryDecisionSelected" {
				count++
				ids = append(ids, event.ID)
			}
		}
		if count != 1 {
			t.Fatalf("duplicate recovery selection: %d", count)
		}
	}
	if ids[0] == ids[1] {
		t.Fatal("recovery event IDs collided across iterations")
	}
}

func TestWorkflowRecoveryBudgetAndInterruptionRetainExistingBehavior(t *testing.T) {
	for _, scenario := range []string{"exhausted", "interrupted"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			r, w, failed, recorder := recoveryAuditFixture(t)
			if scenario == "exhausted" {
				w.Spec.MaxWorkflowAttempt = 0
				if err := r.Update(ctx, w); err != nil {
					t.Fatal(err)
				}
			} else {
				failed.Status.Phase = v1alpha1.PhaseInterrupted
				failed.Status.Retryable = true
				if err := r.Status().Update(ctx, failed); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(w)}); err != nil {
				t.Fatal(err)
			}
			var updated v1alpha1.SovereignWorkflow
			if err := r.Client.Get(ctx, client.ObjectKeyFromObject(w), &updated); err != nil {
				t.Fatal(err)
			}
			if updated.Status.WorkflowAttempt != 0 || updated.Status.Refinement != nil {
				t.Fatal("unexpected workflow rewind")
			}
			if scenario == "exhausted" {
				if updated.Status.Phase != string(v1alpha1.PhaseFailed) || recorder.Has("RecoveryDecisionSelected") || recorder.Has("StepAttemptRetried") {
					t.Fatal("exhausted workflow retried")
				}
			} else if updated.Status.ActiveAttemptRef != attemptName("test-candidate", 0, 3) || !recorder.Has("StepAttemptRetried") {
				t.Fatal("interruption did not use ordinary step retry")
			}
		})
	}
}
