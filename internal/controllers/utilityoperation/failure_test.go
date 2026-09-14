package utilityoperation

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/controllermeta"
	"github.com/SovereignAI/internal/utilitycontract"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestUtilityFailureDiagnosticSurvivesCollection(t *testing.T) {
	ctx := context.Background()
	scheme := attemptScheme(t)
	if err := rbacv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	workflow := workflowFixture()
	attempt := authorizedAttempt("test-candidate-w001-r001", "wf", "test-candidate", v1alpha1.ExecutionKindUtility)
	operation := &v1alpha1.UtilityOperation{
		ObjectMeta: metav1.ObjectMeta{Name: attempt.Name, Namespace: "wf", UID: "utility-uid", Labels: map[string]string{controllermeta.LabelWorkflow: "wf"}},
		Spec: v1alpha1.UtilityOperationSpec{AttemptRef: attempt.Name, WorkflowRef: workflowRef(workflow), StepName: attempt.Spec.StepName, Attempt: 1,
			Operation: v1alpha1.UtilityOperationRequest{Name: "test.run"}},
		Status: v1alpha1.UtilityOperationStatus{Phase: v1alpha1.PhasePending},
	}
	ownByAttempt(operation, attempt)
	kube := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.UtilityOperation{}, &batchv1.Job{}).
		WithObjects(workflow, workspaceLeaseFixture(), projectFixture(), attempt, operation).Build()
	r := &UtilityOperationReconciler{Client: kube, Scheme: scheme, Policy: allowPolicy{}}
	for range 2 {
		if _, err := r.Reconcile(ctx, requestFor(operation)); err != nil {
			t.Fatal(err)
		}
	}
	var job batchv1.Job
	if err := kube.Get(ctx, client.ObjectKeyFromObject(operation), &job); err != nil {
		t.Fatal(err)
	}
	job.UID = "job-uid"
	if err := kube.Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	job.Status.Failed = 1
	if err := kube.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	detail := utilitycontract.ResultError{Code: utilitycontract.TestRunCodeError, Message: "main_test.go:42: expected 2, got 3"}
	pod := failedUtilityPod(&job, detail)
	// The cached client has no pod yet. The API reader must provide its final diagnostic.
	r.Reader = fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	if _, err := r.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status.Phase != v1alpha1.PhaseCollecting || operation.Status.FailureMessage != detail.Message || operation.Status.FailureReason != detail.Code {
		t.Fatalf("collection lost the command diagnostic: %#v", operation.Status)
	}
	var collector batchv1.Job
	if err := kube.Get(ctx, client.ObjectKey{Namespace: operation.Namespace, Name: operation.Status.CollectorJobRef}, &collector); err != nil {
		t.Fatal(err)
	}
	collector.Status.Succeeded = 1
	if err := kube.Status().Update(ctx, &collector); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, requestFor(operation)); err != nil {
		t.Fatal(err)
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(operation), operation); err != nil {
		t.Fatal(err)
	}
	if operation.Status.Phase != v1alpha1.PhaseFailed || operation.Status.FailureReason != detail.Code || operation.Status.FailureMessage != detail.Message {
		t.Fatalf("terminal utility lost the diagnostic: %#v", operation.Status)
	}
}

func TestUtilityJobFailureRejectsUntrustedOrMissingDiagnostics(t *testing.T) {
	for _, scenario := range []string{"wrong pod owner", "wrong job owner", "wrong container", "invalid JSON", "invalid code", "empty message", "successful container"} {
		t.Run(scenario, func(t *testing.T) {
			scheme := attemptScheme(t)
			operation := &v1alpha1.UtilityOperation{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "wf", UID: "utility-uid"}, Spec: v1alpha1.UtilityOperationSpec{AttemptRef: "test"}}
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "wf", UID: "job-uid", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(operation, v1alpha1.GroupVersion.WithKind("UtilityOperation"))}}}
			pod := failedUtilityPod(job, utilitycontract.ResultError{Code: utilitycontract.TestRunCodeError, Message: "main.go:1:1: undefined: Divide"})
			switch scenario {
			case "wrong pod owner":
				pod.OwnerReferences[0].UID = "other-job"
			case "wrong job owner":
				job.OwnerReferences[0].UID = "other-operation"
			case "wrong container":
				pod.Status.ContainerStatuses[0].Name = "sidecar"
			case "invalid JSON":
				pod.Status.ContainerStatuses[0].State.Terminated.Message = "unstructured logs"
			case "invalid code":
				pod.Status.ContainerStatuses[0].State.Terminated.Message = `{"code":"bad code","message":"failure"}`
			case "empty message":
				pod.Status.ContainerStatuses[0].State.Terminated.Message = `{"code":"TestRunCodeError","message":""}`
			case "successful container":
				pod.Status.ContainerStatuses[0].State.Terminated.ExitCode = 0
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
			r := &UtilityOperationReconciler{Client: kube, Scheme: scheme}
			if err := r.recordJobFailure(context.Background(), operation, job); err != nil {
				t.Fatal(err)
			}
			if operation.Status.FailureReason != "UtilityJobFailed" || operation.Status.FailureMessage != "" {
				t.Fatalf("accepted invalid diagnostic: %#v", operation.Status)
			}
		})
	}
}

func failedUtilityPod(job *batchv1.Job, detail utilitycontract.ResultError) *corev1.Pod {
	data, _ := json.Marshal(detail)
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-pod", Namespace: job.Namespace,
			Labels:          map[string]string{"batch.kubernetes.io/controller-uid": string(job.UID)},
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(job, batchv1.SchemeGroupVersion.WithKind("Job"))}},
		Status: corev1.PodStatus{Phase: corev1.PodFailed, ContainerStatuses: []corev1.ContainerStatus{{
			Name: "utility", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 1, Message: string(data)}},
		}}},
	}
}
