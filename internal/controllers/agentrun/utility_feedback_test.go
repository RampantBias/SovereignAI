package agentrun

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/utilitycontract"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAgentRunInjectsUtilityDiagnosticIntoInputContext(t *testing.T) {
	scheme := attemptScheme(t)
	workflow := workflowFixture()
	feedback := &v1alpha1.FailedAgentAttempt{PreviousAttemptRef: "test-candidate-w000-r001", Code: utilitycontract.TestRunCodeError, Message: "main_test.go:42: expected 2, got 3"}
	run := &v1alpha1.AgentRun{
		ObjectMeta: metav1.ObjectMeta{Name: "test-author-w001-r001", Namespace: workflow.Namespace, UID: "restarted-agent-uid"},
		Spec: v1alpha1.AgentRunSpec{AttemptRef: "test-author-w001-r001", WorkflowRef: workflowRef(workflow), StepName: "test-author", Attempt: 1,
			Responsibility: "write tests", Image: "agent:test", Executable: []string{"/reference-agent"}, PriorAttemptRef: feedback},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workflow, run).Build()
	r := &AgentRunReconciler{Client: kube, Scheme: scheme}
	grant := controllers.WorkspaceWriterGrant{LeaseName: workflow.Status.WorkspaceWriterLeaseRef, HolderIdentity: "restarted-agent", Epoch: 2}
	if err := r.ensureWorkload(context.Background(), run, "", grant, nil); err != nil {
		t.Fatal(err)
	}
	var config corev1.ConfigMap
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: run.Namespace, Name: run.Name + "-input"}, &config); err != nil {
		t.Fatal(err)
	}
	var input agentcontract.Input
	if err := json.Unmarshal([]byte(config.Data["input.json"]), &input); err != nil {
		t.Fatal(err)
	}
	if input.RetryFeedback == nil || input.RetryFeedback.PreviousAttemptRef != feedback.PreviousAttemptRef ||
		input.RetryFeedback.Code != feedback.Code || input.RetryFeedback.Message != feedback.Message {
		t.Fatalf("agent input lost utility diagnostic: %#v", input.RetryFeedback)
	}
	if input.Attempt != 1 || input.Responsibility != run.Spec.Responsibility {
		t.Fatalf("feedback changed the attempt count or responsibility: %#v", input)
	}
}
