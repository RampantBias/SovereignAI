package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/controllermeta"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type approvalFixture struct {
	workflow *v1alpha1.SovereignWorkflow
	attempt  *v1alpha1.StepAttempt
	request  *v1alpha1.ApprovalRequest
	decision *v1alpha1.ApprovalDecision
}

func newApprovalFixture() approvalFixture {
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "wf", Namespace: "wf", UID: "wf-uid"},
		Status:     v1alpha1.SovereignWorkflowStatus{Phase: "AwaitingApproval", ActiveStepName: "gate", ActiveAttemptRef: "gate-1"},
	}
	attempt := &v1alpha1.StepAttempt{
		ObjectMeta: metav1.ObjectMeta{Name: "gate-1", Namespace: "wf", UID: "attempt-uid",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(workflow, v1alpha1.GroupVersion.WithKind("SovereignWorkflow"))}},
		Spec:   v1alpha1.StepAttemptSpec{WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}, StepName: "gate", RetryNumber: 1, Kind: v1alpha1.ExecutionKindHumanGate},
		Status: v1alpha1.StepAttemptStatus{Phase: v1alpha1.PhaseAwaitingApproval, ExecutionRef: &v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ApprovalRequest", Name: "gate-1"}},
	}
	started := metav1.NewTime(time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC))
	request := &v1alpha1.ApprovalRequest{
		ObjectMeta: metav1.ObjectMeta{Name: "gate-1", Namespace: "wf", UID: "request-uid", Generation: 1,
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(attempt, v1alpha1.GroupVersion.WithKind("StepAttempt"))}},
		Spec: v1alpha1.ApprovalRequestSpec{WorkflowRef: attempt.Spec.WorkflowRef, AttemptRef: v1alpha1.UIDReference{Name: attempt.Name, UID: attempt.UID}, StepName: "gate", Attempt: 1,
			Approval: v1alpha1.ApprovalSpec{Mode: v1alpha1.AnyOf, RequiredGroups: []string{"maintainers"}, DenyBehavior: "Fail"}},
		Status: v1alpha1.ApprovalRequestStatus{Phase: v1alpha1.PhaseAwaitingApproval, StartedAt: &started},
	}
	authored := v1alpha1.NewAuditTime(started.Time)
	decision := &v1alpha1.ApprovalDecision{
		ObjectMeta: metav1.ObjectMeta{Name: controllermeta.ApprovalDecisionName(request.UID), Namespace: "wf", UID: "decision-uid",
			OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(request, v1alpha1.GroupVersion.WithKind("ApprovalRequest"))}},
		Spec: v1alpha1.ApprovalDecisionSpec{WorkflowRef: request.Spec.WorkflowRef, StepAttemptRef: request.Spec.AttemptRef,
			ApprovalRequestRef: v1alpha1.UIDReference{Name: request.Name, UID: request.UID}, Decision: v1alpha1.Approved,
			AuthoredAt: &authored, Subject: v1alpha1.Subject{SubjectId: "alice", Groups: []string{"maintainers"}}},
	}
	return approvalFixture{workflow, attempt, request, decision}
}

func (f approvalFixture) kube(t *testing.T, includeDecision bool) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	objects := []client.Object{f.workflow, f.attempt, f.request}
	if includeDecision {
		objects = append(objects, f.decision)
	}
	return fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&v1alpha1.SovereignWorkflow{}, &v1alpha1.StepAttempt{}, &v1alpha1.ApprovalRequest{}).
		WithObjects(objects...).Build()
}

func reconcileApproval(t *testing.T, r *ApprovalRequestReconciler, request *v1alpha1.ApprovalRequest) v1alpha1.ApprovalRequest {
	t.Helper()
	ctx := context.Background()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(request)}); err != nil {
		t.Fatal(err)
	}
	var latest v1alpha1.ApprovalRequest
	if err := r.Get(ctx, client.ObjectKeyFromObject(request), &latest); err != nil {
		t.Fatal(err)
	}
	return latest
}

func TestApprovalDecisionCompletesRequestOnce(t *testing.T) {
	for _, choice := range []v1alpha1.ApprovalChoice{v1alpha1.Approved, v1alpha1.Denied} {
		t.Run(string(choice), func(t *testing.T) {
			f := newApprovalFixture()
			f.decision.Spec.Decision = choice
			kube := f.kube(t, true)
			now := f.request.Status.StartedAt.Add(time.Hour)
			r := &ApprovalRequestReconciler{Client: kube, Now: func() time.Time { return now }}
			latest := reconcileApproval(t, r, f.request)
			wantPhase, wantReason := v1alpha1.PhaseSucceeded, ""
			if choice == v1alpha1.Denied {
				wantPhase, wantReason = v1alpha1.PhaseFailed, "ApprovalDenied"
			}
			if latest.Status.Phase != wantPhase || latest.Status.FailureReason != wantReason || latest.Status.Retryable ||
				latest.Status.DecisionRef != f.decision.Name || latest.Status.CompletedAt == nil || !latest.Status.CompletedAt.Time.Equal(now) ||
				!latest.Status.StartedAt.Equal(f.request.Status.StartedAt) || latest.Status.ObservedGeneration != latest.Generation {
				t.Fatalf("incorrect completion: %#v", latest.Status)
			}
			condition := apiMeta.FindStatusCondition(latest.Status.Conditions, "Ready")
			if condition == nil || (choice == v1alpha1.Approved && condition.Status != metav1.ConditionTrue) || (choice == v1alpha1.Denied && condition.Status != metav1.ConditionFalse) {
				t.Fatalf("incorrect condition: %#v", condition)
			}
			version := latest.ResourceVersion
			now = now.Add(time.Hour)
			// Simulate a controller restart after completion.
			restarted := &ApprovalRequestReconciler{Client: kube, Now: func() time.Time { return now }}
			latest = reconcileApproval(t, restarted, f.request)
			if latest.ResourceVersion != version {
				t.Fatal("terminal request was rewritten")
			}
			// Stale initialization/failure writes must not undo completion either.
			if err := restarted.setAwaiting(context.Background(), f.request); err != nil {
				t.Fatal(err)
			}
			if err := restarted.setFailed(context.Background(), f.request, "LateFailure"); err != nil {
				t.Fatal(err)
			}
			latest = reconcileApproval(t, restarted, f.request)
			if latest.ResourceVersion != version {
				t.Fatal("stale status helper overwrote completion")
			}
			var attempt v1alpha1.StepAttempt
			if err := kube.Get(context.Background(), client.ObjectKeyFromObject(f.attempt), &attempt); err != nil {
				t.Fatal(err)
			}
			if attempt.Status.Phase != v1alpha1.PhaseAwaitingApproval {
				t.Fatal("approval controller wrote attempt status")
			}
		})
	}
}

func TestApprovalWaitsWithoutItsDecision(t *testing.T) {
	f := newApprovalFixture()
	f.decision.Name = "unrelated-decision"
	kube := f.kube(t, true)
	r := &ApprovalRequestReconciler{Client: kube}
	first := reconcileApproval(t, r, f.request)
	second := reconcileApproval(t, r, f.request)
	if first.Status.Phase != v1alpha1.PhaseAwaitingApproval || first.Status.CompletedAt != nil || first.Status.DecisionRef != "" || first.ResourceVersion != second.ResourceVersion {
		t.Fatalf("missing decision changed request: %#v", second.Status)
	}
}

func TestApprovalRejectsInvalidDecisionOrAuthority(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*approvalFixture)
		reason string
	}{
		{"request UID", func(f *approvalFixture) { f.decision.Spec.ApprovalRequestRef.UID = "old" }, "InvalidApprovalDecision"},
		{"attempt UID", func(f *approvalFixture) { f.decision.Spec.StepAttemptRef.UID = "old" }, "InvalidApprovalDecision"},
		{"workflow UID", func(f *approvalFixture) { f.decision.Spec.WorkflowRef.UID = "old" }, "InvalidApprovalDecision"},
		{"owner", func(f *approvalFixture) { f.decision.OwnerReferences = nil }, "InvalidApprovalDecision"},
		{"deleting decision", func(f *approvalFixture) {
			now := metav1.Now()
			f.decision.DeletionTimestamp = &now
			f.decision.Finalizers = []string{"test"}
		}, "InvalidApprovalDecision"},
		{"subject", func(f *approvalFixture) { f.decision.Spec.Subject.SubjectId = " " }, "InvalidApprovalDecision"},
		{"timestamp", func(f *approvalFixture) { f.decision.Spec.AuthoredAt = nil }, "InvalidApprovalDecision"},
		{"groups", func(f *approvalFixture) { f.decision.Spec.Subject.Groups = []string{"other"} }, "InvalidApprovalDecision"},
		{"empty required groups", func(f *approvalFixture) { f.request.Spec.Approval.RequiredGroups = nil }, "InvalidApprovalDecision"},
		{"mode", func(f *approvalFixture) { f.request.Spec.Approval.Mode = "AllOf" }, "InvalidApprovalDecision"},
		{"deny behavior", func(f *approvalFixture) { f.request.Spec.Approval.DenyBehavior = "Retry" }, "InvalidApprovalDecision"},
		{"choice", func(f *approvalFixture) { f.decision.Spec.Decision = "Maybe" }, "InvalidApprovalDecision"},
		{"recreated attempt", func(f *approvalFixture) { f.attempt.UID = "replacement" }, "InvalidStepAttemptAuthority"},
		{"finished attempt", func(f *approvalFixture) { f.attempt.Status.Phase = v1alpha1.PhaseSucceeded }, "InvalidStepAttemptAuthority"},
		{"execution binding", func(f *approvalFixture) { f.attempt.Status.ExecutionRef.Name = "other" }, "InvalidStepAttemptAuthority"},
		{"inactive gate", func(f *approvalFixture) { f.workflow.Status.ActiveAttemptRef = "other" }, "InvalidWorkflowAuthority"},
		{"finished workflow", func(f *approvalFixture) { f.workflow.Status.Phase = "Failed" }, "InvalidWorkflowAuthority"},
		{"recreated workflow", func(f *approvalFixture) { f.workflow.UID = "replacement" }, "InvalidWorkflowAuthority"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newApprovalFixture()
			tc.mutate(&f)
			latest := reconcileApproval(t, &ApprovalRequestReconciler{Client: f.kube(t, true)}, f.request)
			if latest.Status.Phase != v1alpha1.PhaseFailed || latest.Status.FailureReason != tc.reason || latest.Status.Retryable || latest.Status.CompletedAt == nil || latest.Status.DecisionRef != "" {
				t.Fatalf("invalid decision/authority was accepted: %#v", latest.Status)
			}
		})
	}
}

type approvalReadFailureClient struct {
	client.Client
	kind   string
	failed bool
}

func (c *approvalReadFailureClient) Get(ctx context.Context, key client.ObjectKey, object client.Object, opts ...client.GetOption) error {
	kind := ""
	switch object.(type) {
	case *v1alpha1.ApprovalDecision:
		kind = "decision"
	case *v1alpha1.StepAttempt:
		kind = "attempt"
	case *v1alpha1.SovereignWorkflow:
		kind = "workflow"
	}
	if !c.failed && kind == c.kind {
		c.failed = true
		return errors.New("temporary read failure")
	}
	return c.Client.Get(ctx, key, object, opts...)
}

func TestApprovalReadFailuresRetryWithoutFailingRequest(t *testing.T) {
	for _, kind := range []string{"decision", "attempt", "workflow", "initial-attempt"} {
		t.Run(kind, func(t *testing.T) {
			f := newApprovalFixture()
			failKind := kind
			if kind == "initial-attempt" {
				f.request.Status.Phase = v1alpha1.PhasePending
				failKind = "attempt"
			}
			base := f.kube(t, true)
			kube := &approvalReadFailureClient{Client: base, kind: failKind}
			r := &ApprovalRequestReconciler{Client: kube}
			if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(f.request)}); err == nil {
				t.Fatal("read error was swallowed")
			}
			var latest v1alpha1.ApprovalRequest
			if err := base.Get(context.Background(), client.ObjectKeyFromObject(f.request), &latest); err != nil {
				t.Fatal(err)
			}
			if latest.Status.Phase != f.request.Status.Phase || latest.Status.CompletedAt != nil || latest.Status.FailureReason != "" {
				t.Fatalf("read error failed gate: %#v", latest.Status)
			}
			latest = reconcileApproval(t, r, f.request)
			if kind == "initial-attempt" {
				latest = reconcileApproval(t, r, f.request)
			}
			if latest.Status.Phase != v1alpha1.PhaseSucceeded {
				t.Fatalf("retry did not recover: %#v", latest.Status)
			}
		})
	}
}

type approvalConflictClient struct {
	client.Client
	beforeConflict func(context.Context) error
	conflicted     bool
}
type approvalConflictWriter struct {
	client.SubResourceWriter
	parent *approvalConflictClient
}

func (c *approvalConflictClient) Status() client.SubResourceWriter {
	return &approvalConflictWriter{SubResourceWriter: c.Client.Status(), parent: c}
}
func (w *approvalConflictWriter) Update(ctx context.Context, object client.Object, opts ...client.SubResourceUpdateOption) error {
	if !w.parent.conflicted {
		w.parent.conflicted = true
		if err := w.parent.beforeConflict(ctx); err != nil {
			return err
		}
		return apierrors.NewConflict(schema.GroupResource{Group: v1alpha1.GroupVersion.Group, Resource: "approvalrequests"}, object.GetName(), errors.New("concurrent update"))
	}
	return w.SubResourceWriter.Update(ctx, object, opts...)
}

func TestApprovalConflictRechecksCurrentState(t *testing.T) {
	for _, change := range []string{"retry", "gate moved", "already completed", "request replaced"} {
		t.Run(change, func(t *testing.T) {
			f := newApprovalFixture()
			base := f.kube(t, true)
			kube := &approvalConflictClient{Client: base}
			kube.beforeConflict = func(ctx context.Context) error {
				switch change {
				case "gate moved":
					if err := base.Get(ctx, client.ObjectKeyFromObject(f.workflow), f.workflow); err != nil {
						return err
					}
					f.workflow.Status.ActiveAttemptRef = "next-attempt"
					return base.Status().Update(ctx, f.workflow)
				case "already completed":
					if err := base.Get(ctx, client.ObjectKeyFromObject(f.request), f.request); err != nil {
						return err
					}
					f.request.Status.Phase = v1alpha1.PhaseFailed
					f.request.Status.DecisionRef = "existing-decision"
					f.request.Status.FailureReason = "ApprovalDenied"
					completed := v1alpha1.NewAuditTime(f.request.Status.StartedAt.Time)
					f.request.Status.CompletedAt = &completed
					return base.Status().Update(ctx, f.request)
				case "request replaced":
					if err := base.Delete(ctx, f.request); err != nil {
						return err
					}
					replacement := f.request.DeepCopy()
					replacement.UID = "replacement-uid"
					replacement.ResourceVersion = ""
					return base.Create(ctx, replacement)
				}
				return nil
			}
			latest := reconcileApproval(t, &ApprovalRequestReconciler{Client: kube}, f.request)
			switch change {
			case "retry":
				if latest.Status.Phase != v1alpha1.PhaseSucceeded {
					t.Fatalf("conflict did not recover: %#v", latest.Status)
				}
			case "gate moved":
				if latest.Status.FailureReason != "InvalidWorkflowAuthority" {
					t.Fatalf("stale gate completed: %#v", latest.Status)
				}
			case "already completed":
				if latest.Status.Phase != v1alpha1.PhaseFailed || latest.Status.DecisionRef != "existing-decision" || !latest.Status.CompletedAt.Equal(f.request.Status.StartedAt.Time) {
					t.Fatalf("terminal result overwritten: %#v", latest.Status)
				}
			case "request replaced":
				if latest.UID != "replacement-uid" || latest.Status.Phase != v1alpha1.PhaseAwaitingApproval || latest.Status.CompletedAt != nil {
					t.Fatalf("replacement request overwritten: %#v", latest)
				}
			}
		})
	}
}
