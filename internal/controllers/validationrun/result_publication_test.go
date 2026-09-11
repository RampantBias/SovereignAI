package validationrun

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifacts"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/controllers"
	"github.com/SovereignAI/internal/validation"
	batchv1 "k8s.io/api/batch/v1"
	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type resultProvider struct{ status validation.Status }

func (p *resultProvider) Start(context.Context, validation.Request) (string, error) {
	return "", fmt.Errorf("unexpected deployment")
}
func (p *resultProvider) Status(context.Context, string) (validation.Status, error) {
	return p.status, nil
}
func (p *resultProvider) Destroy(context.Context, string) error { return nil }

type probeTransport func(*http.Request) (*http.Response, error)

func (f probeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func publicationFixture(t *testing.T) (*ValidationRunReconciler, *v1alpha1.ValidationRun, *resultProvider, *int) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{v1alpha1.AddToScheme, corev1.AddToScheme, batchv1.AddToScheme, rbacv1.AddToScheme, coordinationv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	project := &v1alpha1.SovereignProject{ObjectMeta: metav1.ObjectMeta{Name: "project", UID: "project-uid"}}
	project.Spec.ApplicationRepository.URL = "https://example.test/calculator.git"
	project.Spec.Validation.InfrastructureRepo = project.Spec.ApplicationRepository.URL
	project.Spec.Validation.ImageName = "registry.example.test/calculator"
	wf := &v1alpha1.SovereignWorkflow{ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "wf", UID: "workflow-uid"}}
	wf.Spec.Project = v1alpha1.UIDReference{Name: project.Name, UID: project.UID}
	wf.Spec.Steps = []v1alpha1.StepConfig{{Name: "validation", Kind: v1alpha1.ExecutionKindValidation, Outputs: []v1alpha1.ContractReference{{Name: "validation-result", Version: "v1"}}}}
	wf.Status.PvcName = "workspace"
	wf.Status.WorkspaceWriterLeaseRef = "writer"
	ref := v1alpha1.UIDReference{Name: wf.Name, UID: wf.UID}
	attempt := &v1alpha1.StepAttempt{ObjectMeta: metav1.ObjectMeta{Name: "validation", Namespace: "wf", UID: "attempt-uid"}, Spec: v1alpha1.StepAttemptSpec{WorkflowRef: ref, StepName: "validation", RetryNumber: 1, Kind: v1alpha1.ExecutionKindValidation}, Status: v1alpha1.StepAttemptStatus{ExecutionRef: &v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ValidationRun", Name: "validation"}}}
	run := &v1alpha1.ValidationRun{ObjectMeta: metav1.ObjectMeta{Name: "validation", Namespace: "wf", UID: "run-uid", Generation: 1, Finalizers: []string{ValidationFinalizer}, OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(attempt, v1alpha1.GroupVersion.WithKind("StepAttempt"))}}, Spec: v1alpha1.ValidationRunSpec{WorkflowRef: ref, AttemptRef: attempt.Name, StepName: "validation", Attempt: 1}, Status: v1alpha1.ValidationRunStatus{Phase: v1alpha1.PhaseValidating, ProviderRef: "application"}}
	commit, tree, digest := strings.Repeat("a", 40), strings.Repeat("b", 40), "sha256:"+strings.Repeat("c", 64)
	inputs := validationArtifactFixtures(wf, project, commit, tree, "sha256:"+strings.Repeat("d", 64), digest)
	zero := int32(0)
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{Name: "writer", Namespace: "wf", OwnerReferences: []metav1.OwnerReference{*metav1.NewControllerRef(wf, v1alpha1.GroupVersion.WithKind("SovereignWorkflow"))}}, Spec: coordinationv1.LeaseSpec{LeaseTransitions: &zero}}
	service := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "calculator", Namespace: "wf"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "calculator"}, Ports: []corev1.ServicePort{{Name: "http", Port: 8080}}}}
	image := project.Spec.Validation.ImageName + "@" + digest
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "calculator", Namespace: "wf", Labels: service.Spec.Selector}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "calculator", Image: image}}}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	objects := []client.Object{project, wf, attempt, run, lease, service, pod}
	for i := range inputs {
		objects = append(objects, &inputs[i])
		run.Spec.Inputs = append(run.Spec.Inputs, v1alpha1.ArtifactReference{Name: inputs[i].Spec.Contract.Name, Digest: inputs[i].Spec.Digest, ArtifactRef: &v1alpha1.UIDReference{Name: inputs[i].Name, UID: inputs[i].UID}})
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&v1alpha1.ValidationRun{}, &v1alpha1.Artifact{}, &batchv1.Job{}).WithObjects(objects...).Build()
	provider := &resultProvider{status: validation.Status{Ready: true, SyncStatus: "Synced", HealthStatus: "Healthy", ObservedRevision: commit, ObservedImages: []string{image}, ApplicationName: "application", ApplicationNamespace: "argocd", ApplicationUID: "app-uid", DestinationNamespace: "wf"}}
	calls := new(int)
	transport := probeTransport(func(req *http.Request) (*http.Response, error) {
		*calls++
		code, body := 200, `{"status":"ok"}`
		if req.Method == http.MethodPost {
			var equation struct {
				Operation   string
				Left, Right float64
			}
			if err := json.NewDecoder(req.Body).Decode(&equation); err != nil {
				return nil, err
			}
			var result float64
			switch equation.Operation {
			case "add":
				result = equation.Left + equation.Right
			case "subtract":
				result = equation.Left - equation.Right
			case "multiply":
				result = equation.Left * equation.Right
			case "divide":
				if equation.Right == 0 {
					code = 422
					body = `{"error":{"code":"division_by_zero"}}`
				} else {
					result = equation.Left / equation.Right
				}
			}
			if code == 200 {
				body = fmt.Sprintf(`{"result":%g}`, result)
			}
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	reconciler := &ValidationRunReconciler{Client: kube, Provider: provider, Audit: audit.NewMemoryRecorder(), HTTPClient: &http.Client{Transport: transport}, Now: func() time.Time { return time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC) }}
	if _, err := reconciler.appendExecutionAuthorityEstablished(context.Background(), run, project); err != nil {
		t.Fatal(err)
	}
	evidence, ready, reason, err := controllers.ResolveArtifactEvidence(context.Background(), kube, run.Namespace, run.Spec.WorkflowRef, run.Spec.Inputs)
	if err != nil || !ready || reason != "" {
		t.Fatalf("fixture inputs: %v %v %s", err, ready, reason)
	}
	if _, err := reconciler.appendInputsResolved(context.Background(), run, evidence); err != nil {
		t.Fatal(err)
	}
	return reconciler, run, provider, calls
}

// Exercise the real typed collector and artifact acceptance path between reconciles.
func finishPublication(t *testing.T, r *ValidationRunReconciler, run *v1alpha1.ValidationRun, accept bool) *v1alpha1.Artifact {
	t.Helper()
	ctx := context.Background()
	var config corev1.ConfigMap
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-result"}, &config); err != nil {
		t.Fatal(err)
	}
	staging, store := t.TempDir(), t.TempDir()
	source := filepath.Join(staging, "validation-result.json")
	if err := os.WriteFile(source, []byte(config.Data["validation-result.json"]), 0600); err != nil {
		t.Fatal(err)
	}
	var envelope agentcontract.Result
	if err := json.Unmarshal([]byte(config.Data["result.json"]), &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Artifacts[0].Path = source
	if err := envelope.Validate(staging); err != nil {
		t.Fatal(err)
	}
	collected, err := artifacts.Collect(staging, store, run.Spec.WorkflowRef, v1alpha1.TypedLocalReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "ValidationRun", Name: run.Name}, strings.Repeat("a", 40), envelope.Artifacts)
	if err != nil {
		t.Fatal(err)
	}
	spec := collected[0].Spec
	if _, err := artifacts.Verify(store, spec.Digest); err != nil {
		t.Fatal(err)
	}
	spec.Path = "/workspace/.sovereign/artifacts/" + strings.TrimPrefix(spec.Digest, "sha256:")
	spec.ProducerUID = run.UID
	spec.ProducerGrantRef = v1alpha1.UIDReference{Name: run.Spec.AttemptRef, UID: "attempt-uid"}
	artifact := &v1alpha1.Artifact{ObjectMeta: metav1.ObjectMeta{Name: run.Name + "-validation-result-00", Namespace: run.Namespace, UID: "result-uid", Generation: 1}, Spec: spec}
	if err := r.Create(ctx, artifact); err != nil {
		t.Fatal(err)
	}
	var job batchv1.Job
	if err := r.Get(ctx, types.NamespacedName{Namespace: run.Namespace, Name: run.Name + "-collect"}, &job); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(job.Spec.Template.Spec.Containers[0].Args, " "), "--producer-kind ValidationRun") {
		t.Fatal("collector did not identify ValidationRun producer")
	}
	job.Status.Succeeded = 1
	if err := r.Status().Update(ctx, &job); err != nil {
		t.Fatal(err)
	}
	if accept {
		ac := controllers.ArtifactReconciler{Client: r.Client, Audit: r.Audit}
		if _, err := ac.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(artifact)}); err != nil {
			t.Fatal(err)
		}
	}
	return artifact
}

func TestValidationPublishesAcceptedResultBeforeSuccess(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(fmt.Sprint(recovery), func(t *testing.T) {
			r, run, _, calls := publicationFixture(t)
			ctx := context.Background()
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
			if recovery {
				run.Status.Phase = v1alpha1.PhaseSucceeded
				if err := r.Status().Update(ctx, run); err != nil {
					t.Fatal(err)
				}
			}
			result, err := r.Reconcile(ctx, req)
			if err != nil || result.RequeueAfter == 0 {
				t.Fatalf("publication did not wait: %v %v", result, err)
			}
			if *calls != 6 {
				t.Fatalf("checks = %d", *calls)
			}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if *calls != 6 {
				t.Fatal("publication retry changed frozen evidence")
			}
			artifact := finishPublication(t, r, run, false)
			result, err = r.Reconcile(ctx, req)
			if err != nil || result.RequeueAfter == 0 {
				t.Fatalf("must wait for Artifact acceptance: %v %v", result, err)
			}
			if err := r.Get(ctx, req.NamespacedName, run); err != nil {
				t.Fatal(err)
			}
			if !recovery && run.Status.Phase == v1alpha1.PhaseSucceeded {
				t.Fatal("completed before acceptance")
			}
			ac := controllers.ArtifactReconciler{Client: r.Client, Audit: r.Audit}
			if _, err := ac.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(artifact)}); err != nil {
				t.Fatal(err)
			}
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, req.NamespacedName, run); err != nil {
				t.Fatal(err)
			}
			if run.Status.Phase != v1alpha1.PhaseSucceeded {
				t.Fatalf("run = %#v", run.Status)
			}
			version := run.ResourceVersion
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, req.NamespacedName, run); err != nil {
				t.Fatal(err)
			}
			if run.ResourceVersion != version {
				t.Fatal("terminal run continually writes its status")
			}
			var lease coordinationv1.Lease
			if err := r.Get(ctx, types.NamespacedName{Namespace: "wf", Name: "writer"}, &lease); err != nil {
				t.Fatal(err)
			}
			if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" {
				t.Fatal("publication retained workspace writer")
			}
		})
	}
}

func TestValidationRequiresObservedCandidateAndPassingEndpoints(t *testing.T) {
	for _, scenario := range []string{"wrong commit", "wrong image", "missing application uid", "wrong destination", "wrong arithmetic", "wrong division error", "collector failed", "wrong artifact digest"} {
		t.Run(scenario, func(t *testing.T) {
			r, run, provider, _ := publicationFixture(t)
			ctx := context.Background()
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
			switch scenario {
			case "wrong commit":
				provider.status.ObservedRevision = strings.Repeat("e", 40)
			case "wrong image":
				provider.status.ObservedImages = []string{"wrong"}
			case "missing application uid":
				provider.status.ApplicationUID = ""
			case "wrong destination":
				provider.status.DestinationNamespace = "other"
			case "wrong arithmetic", "wrong division error":
				original := r.HTTPClient.Transport
				r.HTTPClient.Transport = probeTransport(func(req *http.Request) (*http.Response, error) {
					resp, err := original.RoundTrip(req)
					if err != nil {
						return nil, err
					}
					if (scenario == "wrong arithmetic" && resp.StatusCode == 200 && req.Method == http.MethodPost) || (scenario == "wrong division error" && resp.StatusCode == 422) {
						resp.Body = io.NopCloser(strings.NewReader(`{"result":999,"error":{"code":"wrong"}}`))
					}
					return resp, nil
				})
			}
			_, err := r.Reconcile(ctx, req)
			if strings.HasPrefix(scenario, "wrong ") && scenario != "wrong arithmetic" && scenario != "wrong division error" && scenario != "wrong artifact digest" || scenario == "missing application uid" {
				if err == nil {
					t.Fatal("accepted wrong observation")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "collector failed" {
				var job batchv1.Job
				if err := r.Get(ctx, types.NamespacedName{Namespace: "wf", Name: "validation-collect"}, &job); err != nil {
					t.Fatal(err)
				}
				job.Status.Failed = 1
				if err := r.Status().Update(ctx, &job); err != nil {
					t.Fatal(err)
				}
			} else {
				artifact := finishPublication(t, r, run, true)
				if scenario == "wrong artifact digest" {
					if err := r.Get(ctx, client.ObjectKeyFromObject(artifact), artifact); err != nil {
						t.Fatal(err)
					}
					artifact.Spec.Digest = "sha256:" + strings.Repeat("0", 64)
					if err := r.Update(ctx, artifact); err != nil {
						t.Fatal(err)
					}
				}
			}
			_, err = r.Reconcile(ctx, req)
			if scenario == "wrong artifact digest" {
				if err == nil {
					t.Fatal("accepted substituted artifact")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Get(ctx, req.NamespacedName, run); err != nil {
				t.Fatal(err)
			}
			if run.Status.Phase != v1alpha1.PhaseFailed {
				t.Fatalf("invalid validation passed: %#v", run.Status)
			}
		})
	}
}

func TestValidationPublicationWaitsForWriterAndCleansUp(t *testing.T) {
	r, run, _, _ := publicationFixture(t)
	ctx := context.Background()
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)}
	var lease coordinationv1.Lease
	key := types.NamespacedName{Namespace: "wf", Name: "writer"}
	if err := r.Get(ctx, key, &lease); err != nil {
		t.Fatal(err)
	}
	other := "another-writer"
	lease.Spec.HolderIdentity = &other
	if err := r.Update(ctx, &lease); err != nil {
		t.Fatal(err)
	}
	result, err := r.Reconcile(ctx, req)
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("did not wait for writer: %v %v", result, err)
	}
	var jobs batchv1.JobList
	if err := r.List(ctx, &jobs); err != nil {
		t.Fatal(err)
	}
	if len(jobs.Items) != 0 {
		t.Fatal("started collector while another writer held lease")
	}
	if err := r.Get(ctx, key, &lease); err != nil {
		t.Fatal(err)
	}
	lease.Spec.HolderIdentity = nil
	if err := r.Update(ctx, &lease); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := r.Get(ctx, req.NamespacedName, run); err != nil {
		t.Fatal(err)
	}
	if err := r.Delete(ctx, run); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := r.Reconcile(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Get(ctx, key, &lease); err != nil {
		t.Fatal(err)
	}
	if lease.Spec.HolderIdentity != nil && *lease.Spec.HolderIdentity != "" {
		t.Fatal("deleting run retained its writer")
	}
}

func TestValidationPublicationWaitsForArgoReadiness(t *testing.T) {
	r, run, provider, calls := publicationFixture(t)
	provider.status.Ready = false
	provider.status.HealthStatus = "Progressing"
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(run)})
	if err != nil || result.RequeueAfter == 0 {
		t.Fatalf("did not wait: %v %v", result, err)
	}
	if *calls != 0 {
		t.Fatal("probed an unready deployment")
	}
	var configs corev1.ConfigMapList
	if err := r.List(context.Background(), &configs); err != nil {
		t.Fatal(err)
	}
	if len(configs.Items) != 0 {
		t.Fatal("published result before Argo readiness")
	}
}
