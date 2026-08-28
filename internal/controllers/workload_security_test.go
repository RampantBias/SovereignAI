package controllers

import (
	"testing"

	"github.com/SovereignAI/internal/api/v1alpha1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestWorkspaceWorkloadsShareNonRootIdentity(t *testing.T) {
	workflow := &v1alpha1.SovereignWorkflow{
		ObjectMeta: metav1.ObjectMeta{Name: "workflow", Namespace: "namespace"},
		Status:     v1alpha1.SovereignWorkflowStatus{PvcName: "workspace"},
	}
	run := &v1alpha1.AgentRun{ObjectMeta: metav1.ObjectMeta{Name: "agent", Namespace: workflow.Namespace}}
	operation := &v1alpha1.UtilityOperation{ObjectMeta: metav1.ObjectMeta{Name: "utility", Namespace: workflow.Namespace}}
	grant := workspaceWriterGrant{LeaseName: "writer", HolderIdentity: "holder", Epoch: 1}

	collectorObjects := buildCollectorResources(run, workflow, "agent", "collector", grant)
	collector := collectorObjects[len(collectorObjects)-1].(*batchv1.Job)
	contexts := map[string]*corev1.PodSecurityContext{
		"bootstrap": buildBootstrapJob(workflow, "bootstrap", grant).Spec.Template.Spec.SecurityContext,
		"utility":   buildUtilityJob(operation, workflow.Status.PvcName, "input", "runtime", utilityWorkloadConfig{}, grant).Spec.Template.Spec.SecurityContext,
		"agent":     buildAgentRunPod(run, workflow.Status.PvcName, "input", "mcp", grant).Spec.SecurityContext,
		"collector": collector.Spec.Template.Spec.SecurityContext,
	}
	for name, security := range contexts {
		assertWorkspaceWorkloadIdentity(t, name, security)
	}
}

func assertWorkspaceWorkloadIdentity(t *testing.T, name string, security *corev1.PodSecurityContext) {
	t.Helper()
	if security == nil || security.RunAsNonRoot == nil || !*security.RunAsNonRoot ||
		security.RunAsUser == nil || *security.RunAsUser != workspaceWorkloadID ||
		security.RunAsGroup == nil || *security.RunAsGroup != workspaceWorkloadID ||
		security.FSGroup == nil || *security.FSGroup != workspaceWorkloadID {
		t.Fatalf("%s security context does not use shared identity %d: %#v", name, workspaceWorkloadID, security)
	}
}

var _ client.Object = (*v1alpha1.AgentRun)(nil)
