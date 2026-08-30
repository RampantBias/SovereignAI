package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/controllers"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestDecideFailure(t *testing.T) {
	steps := []v1alpha1.StepConfig{
		{Name: "test-author"},
		{Name: "developer"},
		{Name: "prepare-candidate"},
		{Name: "test-candidate"},
	}

	tests := []struct {
		name        string
		failedStep  string
		phase       v1alpha1.ResourcePhase
		wantAction  failureAction
		wantRestart string
	}{
		{
			name:       "ordinary step retains step retry handling",
			failedStep: "developer",
			phase:      v1alpha1.PhaseFailed,
			wantAction: failureActionRetryStep,
		},
		{
			name:        "test candidate failure rewinds workflow",
			failedStep:  "test-candidate",
			phase:       v1alpha1.PhaseFailed,
			wantAction:  failureActionRetryWorkflow,
			wantRestart: "test-author",
		},
		{
			name:       "test candidate interruption remains a step retry",
			failedStep: "test-candidate",
			phase:      v1alpha1.PhaseInterrupted,
			wantAction: failureActionRetryStep,
		},
		{
			name:       "prepare candidate failure is terminal",
			failedStep: "prepare-candidate",
			phase:      v1alpha1.PhaseFailed,
			wantAction: failureActionFailWorkflow,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			step, found := findStep(steps, test.failedStep)
			if !found {
				t.Fatalf("test step %q not found", test.failedStep)
			}

			got, err := decideFailure(steps, step, test.phase)
			if err != nil {
				t.Fatal(err)
			}

			if got.Action != test.wantAction {
				t.Fatalf(
					"action = %q, want %q",
					got.Action,
					test.wantAction,
				)
			}

			if got.RestartStep != test.wantRestart {
				t.Fatalf(
					"restart step = %q, want %q",
					got.RestartStep,
					test.wantRestart,
				)
			}
		})
	}
}

func TestDecideFailureRejectsMissingRestartStep(t *testing.T) {
	steps := []v1alpha1.StepConfig{
		{Name: "developer"},
		{Name: "test-candidate"},
	}

	decision, err := decideFailure(
		steps,
		steps[1],
		v1alpha1.PhaseFailed,
	)

	if err == nil {
		t.Fatalf("expected error, got decision %#v", decision)
	}

	if !strings.Contains(err.Error(), `retry target "test-author" does not exist`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecideFailureRejectsForwardRestartStep(t *testing.T) {
	steps := []v1alpha1.StepConfig{
		{Name: "test-candidate"},
		{Name: "test-author"},
	}

	decision, err := decideFailure(
		steps,
		steps[0],
		v1alpha1.PhaseFailed,
	)

	if err == nil {
		t.Fatalf("expected error, got decision %#v", decision)
	}

	if !strings.Contains(err.Error(), "must precede failed step") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWorkflowRewindsFailedTestCandidateToTestAuthor(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()

	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	workflow := changeRequestWorkflow()
	workflow.Spec.MaxWorkflowAttempt = 1
	workflow.Spec.Steps = []v1alpha1.StepConfig{
		{
			Name:  "test-author",
			Kind:  v1alpha1.ExecutionKindAgent,
			Order: 1,
			Agent: &v1alpha1.AgentStepSpec{
				Responsibility: "write tests",
				Image:          "agent:test",
				Executable:     []string{"/reference-agent"},
			},
		},
		{
			Name:        "test-candidate",
			Kind:        v1alpha1.ExecutionKindUtility,
			Order:       2,
			MaxAttempts: 3,
			Utility: &v1alpha1.UtilityOperationRequest{
				Name: "test.run",
			},
		},
	}

	workflow.Status.Phase = string(v1alpha1.PhaseRunning)
	workflow.Status.WorkflowAttempt = 0
	workflow.Status.ActiveStepName = "test-candidate"
	workflow.Status.ActiveAttemptRef = "test-candidate-002"

	// Bypass workspace/bootstrap setup so this test only exercises recovery.
	workflow.Status.WorkspaceWriterLeaseRef = "wf-1-workspace-writer"
	workflow.Status.BootstrapWriterReleased = true

	controller := true
	failed := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-candidate-002",
			Namespace: workflow.Namespace,
			Labels: map[string]string{
				controllermeta.LabelWorkflow: workflow.Spec.WorkflowID,
				controllermeta.LabelStep:     "test-candidate",
			},
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "SovereignWorkflow",
				Name:       workflow.Name,
				UID:        workflow.UID,
				Controller: &controller,
			}},
		},
		Spec: v1alpha1.StepAttemptSpec{
			WorkflowRef: v1alpha1.UIDReference{
				Name: workflow.Name,
				UID:  workflow.UID,
			},
			StepName:        "test-candidate",
			RetryNumber:     2,
			WorkflowAttempt: 0,
			Kind:            v1alpha1.ExecutionKindUtility,
		},
		Status: v1alpha1.StepAttemptStatus{
			Phase:          v1alpha1.PhaseFailed,
			FailureReason:  "TestsFailed",
			FailureMessage: "go test ./...: expected 2, got 3",
			Retryable:      false,
		},
	}

	bootstrapArtifact := changeRequestArtifact(workflow)

	kubeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithStatusSubresource(
			&v1alpha1.SovereignWorkflow{},
			&v1alpha1.StepAttempt{},
			&v1alpha1.AgentRun{},
			&v1alpha1.Artifact{},
		).
		WithObjects(
			workflow,
			failed,
			bootstrapArtifact,
		).
		Build()

	reconciler := &WorkflowReconciler{
		Client: kubeClient,
		Reader: kubeClient,
		Scheme: scheme,
		Audit:  audit.NewMemoryRecorder(),
	}

	request := ctrl.Request{
		NamespacedName: client.ObjectKeyFromObject(workflow),
	}

	// First reconcile durably records the rewind. It must not create the
	// replacement attempt in the same transition.
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}

	var updated v1alpha1.SovereignWorkflow
	if err := kubeClient.Get(ctx, request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}

	if updated.Status.WorkflowAttempt != 1 {
		t.Fatalf(
			"workflow attempt = %d, want 1",
			updated.Status.WorkflowAttempt,
		)
	}

	if updated.Status.ActiveStepName != "test-author" {
		t.Fatalf(
			"active step = %q, want test-author",
			updated.Status.ActiveStepName,
		)
	}

	if updated.Status.ActiveAttemptRef != "" {
		t.Fatalf(
			"active attempt = %q, want empty during rewind",
			updated.Status.ActiveAttemptRef,
		)
	}

	var original v1alpha1.StepAttempt
	if err := kubeClient.Get(
		ctx,
		client.ObjectKeyFromObject(failed),
		&original,
	); err != nil {
		t.Fatal(err)
	}

	if original.Status.Phase != v1alpha1.PhaseFailed ||
		original.Spec.WorkflowAttempt != 0 ||
		original.Spec.RetryNumber != 2 {
		t.Fatalf("failed attempt history was modified: %#v", original)
	}

	var attemptsAfterRewind v1alpha1.StepAttemptList
	if err := kubeClient.List(ctx, &attemptsAfterRewind); err != nil {
		t.Fatal(err)
	}
	if len(attemptsAfterRewind.Items) != 1 {
		t.Fatalf(
			"rewind created an attempt prematurely: %#v",
			attemptsAfterRewind.Items,
		)
	}

	// The following reconcile creates test-author in the new workflow
	// attempt, with its step retry number reset.
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}

	retryName := attemptName("test-author", 1, 1)

	var restarted v1alpha1.StepAttempt
	if err := kubeClient.Get(
		ctx,
		types.NamespacedName{
			Namespace: workflow.Namespace,
			Name:      retryName,
		},
		&restarted,
	); err != nil {
		t.Fatal(err)
	}

	if restarted.Spec.StepName != "test-author" {
		t.Fatalf("restart step = %q, want test-author", restarted.Spec.StepName)
	}
	if restarted.Spec.WorkflowAttempt != 1 {
		t.Fatalf(
			"restart workflow attempt = %d, want 1",
			restarted.Spec.WorkflowAttempt,
		)
	}
	if restarted.Spec.RetryNumber != 1 {
		t.Fatalf(
			"restart retry number = %d, want 1",
			restarted.Spec.RetryNumber,
		)
	}

	// A repeated reconcile must observe the new attempt, not rewind again or
	// create another test-author attempt
	if _, err := reconciler.Reconcile(ctx, request); err != nil {
		t.Fatal(err)
	}

	if err := kubeClient.Get(ctx, request.NamespacedName, &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Status.WorkflowAttempt != 1 {
		t.Fatalf(
			"workflow attempt incremented twice: %d",
			updated.Status.WorkflowAttempt,
		)
	}

	var allAttempts v1alpha1.StepAttemptList
	if err := kubeClient.List(ctx, &allAttempts); err != nil {
		t.Fatal(err)
	}
	if len(allAttempts.Items) != 2 {
		t.Fatalf(
			"expected immutable failed attempt and one restarted attempt, got %#v",
			allAttempts.Items,
		)
	}
}
func changeRequestArtifact(workflow *v1alpha1.SovereignWorkflow) *v1alpha1.Artifact {
	workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.ObjectMeta.UID}
	controller := true

	return &v1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{
			Name: "change-request", Namespace: workflow.Namespace, UID: "artifact-uid",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: v1alpha1.GroupVersion.String(), Kind: "SovereignWorkflow",
				Name: workflow.Name, UID: workflow.UID, Controller: &controller,
			}},
		},
		Spec: v1alpha1.ArtifactSpec{
			WorkflowRef: workflowRef,
			ProducerRef: v1alpha1.TypedLocalReference{
				APIVersion: v1alpha1.GroupVersion.String(), Kind: "SovereignWorkflow", Name: workflow.Name,
			},
			Contract: workflow.Spec.Bootstrap.Contract,
			Digest:   workflow.Spec.Bootstrap.ExpectedDigest,
			Path:     controllers.BootstrapArtifactPath(workflow.Spec.Bootstrap.ExpectedDigest),
		},
		Status: v1alpha1.ArtifactStatus{
			Phase: v1alpha1.PhaseSucceeded,
			Conditions: []metav1.Condition{{
				Type: "Valid", Status: metav1.ConditionTrue, Reason: "ContractAccepted",
			}},
		},
	}
}

func changeRequestWorkflow() *v1alpha1.SovereignWorkflow {
	return &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf-1", Namespace: "wf-1", UID: "uid-1", Finalizers: []string{controllermeta.WorkflowFinalizer}},
		Spec: v1alpha1.SovereignWorkflowSpec{
			Project: v1alpha1.UIDReference{Name: "project"}, WorkflowID: "wf-1",
			Bootstrap: v1alpha1.WorkflowBootstrapSpec{
				SourceRef:      v1alpha1.UIDReference{Name: "wf-1-bootstrap-input", UID: "source-uid"},
				Key:            "change-request.json",
				ExpectedDigest: "sha256:" + strings.Repeat("a", 64),
				Contract:       v1alpha1.ContractReference{Name: "change-request", Version: "v1"},
				ArtifactName:   "change-request",
			},
		},
		Status: v1alpha1.SovereignWorkflowStatus{
			BootstrapArtifactRef: &v1alpha1.UIDReference{Name: "change-request", UID: "artifact-uid"},
			Conditions: []metav1.Condition{{
				Type: "BootstrapReady", Status: metav1.ConditionTrue, Reason: "ChangeRequestAccepted",
			}},
			PvcName: "aaa",
		},
	}
}
func TestResolveStepInputsPreservesUpstreamAndSelectsRecomputedArtifacts(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "wf", UID: "workflow-uid"},
		Spec: v1alpha1.SovereignWorkflowSpec{
			WorkflowID: "wf",
			Steps: []v1alpha1.StepConfig{
				{
					Name:    "architect",
					Outputs: []v1alpha1.ContractReference{{Name: "implementation-plan", Version: "v1"}},
				},
				{
					Name:    "test-author",
					Inputs:  []v1alpha1.ArtifactReference{{Name: "implementation-plan"}},
					Outputs: []v1alpha1.ContractReference{{Name: "test-change-set", Version: "v1"}},
				},
				{
					Name:   "developer",
					Inputs: []v1alpha1.ArtifactReference{{Name: "test-change-set"}},
				},
			},
		},
		Status: v1alpha1.SovereignWorkflowStatus{
			WorkflowAttempt: 1,
			Refinement: &v1alpha1.WorkflowRefinementStatus{
				Iteration:       1,
				RestartStepName: "test-author",
			},
		},
	}
	workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}

	architectFirst := succeededLineageAttempt("architect-w000-r001", workflowRef, "architect", 0, 1)
	architectLatest := succeededLineageAttempt("architect-w000-r002", workflowRef, "architect", 0, 2)
	oldTests := succeededLineageAttempt("test-author-w000-r002", workflowRef, "test-author", 0, 2)
	newTests := succeededLineageAttempt("test-author-w001-r001", workflowRef, "test-author", 1, 1)

	objects := []client.Object{
		workflow,
		architectFirst,
		architectLatest,
		oldTests,
		newTests,
		acceptedLineageArtifact("plan-old", "plan-old-uid", workflowRef, architectFirst.Name, "implementation-plan", "sha256:plan-old"),
		acceptedLineageArtifact("plan-latest", "plan-latest-uid", workflowRef, architectLatest.Name, "implementation-plan", "sha256:plan-latest"),
		acceptedLineageArtifact("tests-old", "tests-old-uid", workflowRef, oldTests.Name, "test-change-set", "sha256:tests-old"),
		acceptedLineageArtifact("tests-new", "tests-new-uid", workflowRef, newTests.Name, "test-change-set", "sha256:tests-new"),
	}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	reconciler := &WorkflowReconciler{Client: kubeClient, Reader: kubeClient}

	restartedAuthor := &v1alpha1.StepAttempt{Spec: v1alpha1.StepAttemptSpec{
		WorkflowRef: workflowRef, StepName: "test-author", WorkflowAttempt: 1, RetryNumber: 1,
	}}
	authorInputs, err := reconciler.resolveStepInputs(ctx, workflow, restartedAuthor, workflow.Spec.Steps[1])
	if err != nil {
		t.Fatal(err)
	}
	if len(authorInputs) != 1 ||
		authorInputs[0].ArtifactRef == nil ||
		authorInputs[0].ArtifactRef.Name != "plan-latest" ||
		authorInputs[0].ProducerAttemptRef != architectLatest.Name {
		t.Fatalf("restart did not preserve the latest pre-rewind input: %#v", authorInputs)
	}

	restartedDeveloper := &v1alpha1.StepAttempt{Spec: v1alpha1.StepAttemptSpec{
		WorkflowRef: workflowRef, StepName: "developer", WorkflowAttempt: 1, RetryNumber: 1,
	}}
	developerInputs, err := reconciler.resolveStepInputs(ctx, workflow, restartedDeveloper, workflow.Spec.Steps[2])
	if err != nil {
		t.Fatal(err)
	}
	if len(developerInputs) != 1 ||
		developerInputs[0].ArtifactRef == nil ||
		developerInputs[0].ArtifactRef.Name != "tests-new" ||
		developerInputs[0].ProducerAttemptRef != newTests.Name {
		t.Fatalf("recomputed step consumed stale test output: %#v", developerInputs)
	}
}

func TestResolveStepInputsDoesNotFallBackForRecomputedProducer(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "wf", UID: "workflow-uid"},
		Spec: v1alpha1.SovereignWorkflowSpec{
			WorkflowID: "wf",
			Steps: []v1alpha1.StepConfig{
				{Name: "test-author", Outputs: []v1alpha1.ContractReference{{Name: "test-change-set", Version: "v1"}}},
				{Name: "developer", Inputs: []v1alpha1.ArtifactReference{{Name: "test-change-set"}}},
			},
		},
		Status: v1alpha1.SovereignWorkflowStatus{
			WorkflowAttempt: 1,
			Refinement: &v1alpha1.WorkflowRefinementStatus{
				Iteration:       1,
				RestartStepName: "test-author",
			},
		},
	}
	workflowRef := v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}
	oldTests := succeededLineageAttempt("test-author-w000-r002", workflowRef, "test-author", 0, 2)
	oldArtifact := acceptedLineageArtifact(
		"tests-old",
		"tests-old-uid",
		workflowRef,
		oldTests.Name,
		"test-change-set",
		"sha256:tests-old",
	)

	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(workflow, oldTests, oldArtifact).
		Build()
	reconciler := &WorkflowReconciler{Client: kubeClient, Reader: kubeClient}
	consumer := &v1alpha1.StepAttempt{Spec: v1alpha1.StepAttemptSpec{
		WorkflowRef: workflowRef, StepName: "developer", WorkflowAttempt: 1, RetryNumber: 1,
	}}

	_, err := reconciler.resolveStepInputs(ctx, workflow, consumer, workflow.Spec.Steps[1])
	if err == nil || !strings.Contains(err.Error(), "no eligible accepted artifact") {
		t.Fatalf("expected stale artifact rejection, got %v", err)
	}
}

func succeededLineageAttempt(
	name string,
	workflowRef v1alpha1.UIDReference,
	step string,
	workflowAttempt int32,
	retryNumber int32,
) *v1alpha1.StepAttempt {
	return &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "wf"},
		Spec: v1alpha1.StepAttemptSpec{
			WorkflowRef:     workflowRef,
			StepName:        step,
			WorkflowAttempt: workflowAttempt,
			RetryNumber:     retryNumber,
		},
		Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhaseSucceeded},
	}
}

func acceptedLineageArtifact(
	name string,
	uid types.UID,
	workflowRef v1alpha1.UIDReference,
	producerAttempt string,
	contract string,
	digest string,
) *v1alpha1.Artifact {
	const generation = int64(1)
	return &v1alpha1.Artifact{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "wf", UID: uid, Generation: generation,
		},
		Spec: v1alpha1.ArtifactSpec{
			WorkflowRef: workflowRef,
			ProducerRef: v1alpha1.TypedLocalReference{
				APIVersion: v1alpha1.GroupVersion.String(),
				Kind:       "AgentRun",
				Name:       producerAttempt,
			},
			Contract: v1alpha1.ContractReference{Name: contract, Version: "v1"},
			Digest:   digest,
			Path:     "/workspace/.sovereign/artifacts/" + digest,
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
