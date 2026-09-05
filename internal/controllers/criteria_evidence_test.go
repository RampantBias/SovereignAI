package controllers

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestAcceptedCriteriaReachInputsResolved(t *testing.T) {
	data := validChangeRequestContent(t)
	artifact := artifactFixture(t, data, artifactcontract.DigestBytes(data), v1alpha1.ContractReference{Name: "change-request", Version: "v1"})
	artifact.UID = "criteria-artifact"
	accepted := reconcileArtifactFixture(t, artifact)
	if !artifacts.ArtifactAccepted(accepted) {
		t.Fatal("fixture was not admitted")
	}
	workflowRef := accepted.Spec.WorkflowRef
	input := v1alpha1.ArtifactReference{Name: "change-request", Digest: accepted.Spec.Digest, ArtifactRef: &v1alpha1.UIDReference{Name: accepted.Name, UID: accepted.UID}}
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(accepted).Build()
	evidence, ready, reason, err := ResolveArtifactEvidence(context.Background(), kube, accepted.Namespace, workflowRef, []v1alpha1.ArtifactReference{input})
	if err != nil || !ready || reason != "" {
		t.Fatalf("resolve: %v %v %s", ready, err, reason)
	}
	recorder := audit.NewMemoryRecorder()
	r := ApprovalRequestReconciler{Audit: recorder}
	approval := v1alpha1.ApprovalRequest{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: accepted.Namespace, UID: "consumer-uid"}, Spec: v1alpha1.ApprovalRequestSpec{WorkflowRef: workflowRef}}
	if err := r.appendInputsResolved(context.Background(), &approval, evidence); err != nil {
		t.Fatal(err)
	}
	events := recorder.AllEvents()
	if len(events) != 1 || events[0].Type != "InputsResolved" {
		t.Fatal("input event missing")
	}
	var payload audit.InputsResolved
	if err := json.Unmarshal(events[0].Data, &payload); err != nil {
		t.Fatal(err)
	}
	claims := accepted.Spec.Claims.ChangeRequest
	if len(payload.Inputs) != 1 || payload.Inputs[0].Digest != accepted.Spec.Digest || payload.Inputs[0].Artifact.UID != string(accepted.UID) || payload.Inputs[0].AcceptanceCriteriaSetDigest != claims.AcceptanceCriteriaSetDigest {
		t.Fatal("event lost artifact or criterion set identity")
	}
	if len(payload.Inputs[0].AcceptanceCriteria) != len(claims.AcceptanceCriteria) {
		t.Fatal("event lost criteria")
	}
	for i, c := range claims.AcceptanceCriteria {
		if payload.Inputs[0].AcceptanceCriteria[i].ID != c.ID || payload.Inputs[0].AcceptanceCriteria[i].Digest != c.Digest {
			t.Fatal("event altered criterion identity")
		}
	}
	// Audit evidence owns its slice; no mutable alias to the Kubernetes claims.
	evidence[0].AcceptanceCriteria[0].ID = "altered"
	if accepted.Spec.Claims.ChangeRequest.AcceptanceCriteria[0].ID != "RQ-001" {
		t.Fatal("evidence aliases claims")
	}
}

func TestCriteriaInputsRejectMissingOrMismatchedClaims(t *testing.T) {
	data := validChangeRequestContent(t)
	base := artifactFixture(t, data, artifactcontract.DigestBytes(data), v1alpha1.ContractReference{Name: "change-request", Version: "v1"})
	base.UID = "criteria-artifact"
	base = reconcileArtifactFixture(t, base)
	for _, pinned := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			artifact := base.DeepCopy()
			if missing {
				artifact.Spec.Claims = nil
			} else {
				artifact.Spec.Claims.ChangeRequest.AcceptanceCriteria[0].Digest = artifactcontract.DigestBytes([]byte("different"))
			}
			scheme := runtime.NewScheme()
			if err := v1alpha1.AddToScheme(scheme); err != nil {
				t.Fatal(err)
			}
			kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(artifact).Build()
			input := v1alpha1.ArtifactReference{Name: "change-request", Digest: artifact.Spec.Digest}
			if pinned {
				input.ArtifactRef = &v1alpha1.UIDReference{Name: artifact.Name, UID: artifact.UID}
			}
			evidence, ready, reason, err := ResolveArtifactEvidence(context.Background(), kube, artifact.Namespace, artifact.Spec.WorkflowRef, []v1alpha1.ArtifactReference{input})
			if err != nil || ready || reason == "" || len(evidence) != 0 {
				t.Fatalf("bad claims consumed: ready=%v reason=%s err=%v", ready, reason, err)
			}
		}
	}
}

func TestBootstrapCriteriaClaimsMustMatchSource(t *testing.T) {
	data := validChangeRequestContent(t)
	var cr artifactcontract.ChangeRequest
	if err := json.Unmarshal(data, &cr); err != nil {
		t.Fatal(err)
	}
	claims, err := artifacts.ProjectChangeRequestClaims(cr)
	if err != nil {
		t.Fatal(err)
	}
	workflow := &v1alpha1.SovereignWorkflow{ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "workflow", UID: "wf-uid"}}
	workflow.Spec.Bootstrap = v1alpha1.WorkflowBootstrapSpec{ArtifactName: "change-request", Contract: v1alpha1.ContractReference{Name: "change-request", Version: "v1"}, ExpectedDigest: artifactcontract.DigestBytes(data), SourceRef: v1alpha1.UIDReference{Name: "source", UID: "source-uid"}, Key: "request.json"}
	immutable := true
	controller := true
	source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "workflow", UID: "source-uid"}, Immutable: &immutable, BinaryData: map[string][]byte{"request.json": data}}
	artifact := &v1alpha1.Artifact{ObjectMeta: metav1.ObjectMeta{Name: "change-request", Namespace: "workflow", OwnerReferences: []metav1.OwnerReference{{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SovereignWorkflow", Name: workflow.Name, UID: workflow.UID, Controller: &controller}}}, Spec: v1alpha1.ArtifactSpec{
		WorkflowRef: v1alpha1.UIDReference{Name: workflow.Name, UID: workflow.UID}, ProducerRef: v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "SovereignWorkflow", Name: workflow.Name}, Contract: workflow.Spec.Bootstrap.Contract, Digest: workflow.Spec.Bootstrap.ExpectedDigest, Path: BootstrapArtifactPath(workflow.Spec.Bootstrap.ExpectedDigest), SourceRevision: cr.SourceCommit, Claims: claims,
	}}
	// Forge internally self-consistent claims from a different request. Shape and
	// set-hash checks alone pass; source comparison must still reject them.
	cr.AcceptanceCriteria[0].Text = "84 / 2 returns 41"
	cr.AcceptanceCriteria[0].Digest = artifactcontract.CriterionDigest(cr.AcceptanceCriteria[0].ID, cr.AcceptanceCriteria[0].Text)
	cr.AcceptanceCriteriaSetDigest = artifactcontract.CriteriaSetDigest(artifactcontract.CriterionIdentities(cr.AcceptanceCriteria))
	forged, err := artifacts.ProjectChangeRequestClaims(cr)
	if err != nil {
		t.Fatal(err)
	}
	if reflect.DeepEqual(forged, claims) {
		t.Fatal("fixture was not altered")
	}
	artifact.Spec.Claims = forged
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workflow, source).Build()
	r := ArtifactReconciler{Client: kube}
	reason, _ := r.validateStoredArtifact(context.Background(), artifact)
	if reason != "InvalidBootstrapClaims" {
		t.Fatalf("forged claims not rejected against source: %s", reason)
	}
}
