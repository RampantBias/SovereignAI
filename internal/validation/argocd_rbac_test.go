package validation

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func TestArgoPermissionsRemainNamespaceScoped(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(path string, value any) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.UnmarshalStrict(data, value); err != nil {
			t.Fatal(err)
		}
	}
	var role rbacv1.Role
	read("deploy/demo/argocd/rbac.yaml", &role)
	if role.Namespace != "argocd" || role.Kind != "Role" || role.Name != "sovereign-validation-provider" {
		t.Fatalf("unexpected Argo role: %#v", role)
	}
	if len(role.Rules) != 2 || !slices.Equal(role.Rules[0].APIGroups, []string{"argoproj.io"}) || !slices.Equal(role.Rules[0].Resources, []string{"applications", "appprojects"}) {
		t.Fatalf("unexpected privileges: %#v", role.Rules)
	}
	for _, verb := range []string{"get", "list", "watch", "create", "update", "patch", "delete"} {
		if !slices.Contains(role.Rules[0].Verbs, verb) {
			t.Fatalf("missing lifecycle verb: %s", verb)
		}
	}
	secretRule := role.Rules[1]
	if !slices.Equal(secretRule.APIGroups, []string{""}) || !slices.Equal(secretRule.Resources, []string{"secrets"}) ||
		!slices.Equal(secretRule.Verbs, []string{"create", "delete", "get", "update"}) {
		t.Fatalf("unexpected Argo repository Secret privileges: %#v", secretRule)
	}
	var binding rbacv1.RoleBinding
	read("deploy/demo/argocd/sovereign-controller.rbac.yaml", &binding)
	if binding.Namespace != role.Namespace || binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != role.Name || len(binding.Subjects) != 1 {
		t.Fatalf("unexpected binding: %#v", binding)
	}
	subject := binding.Subjects[0]
	if subject.Kind != "ServiceAccount" || subject.Name != "sovereign-controller" || subject.Namespace != "sovereign-orchestrator-system" {
		t.Fatalf("wrong service account: %#v", subject)
	}
	var kustomization struct {
		Resources []string `json:"resources"`
	}
	data, err := os.ReadFile(filepath.Join(root, "deploy/demo/argocd/kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, &kustomization); err != nil {
		t.Fatal(err)
	}
	for _, path := range kustomization.Resources {
		if strings.HasPrefix(path, "https://") {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "deploy/demo/argocd", path)); err != nil {
			t.Fatal(err)
		}
	}
	data, err = os.ReadFile(filepath.Join(root, "deploy/base/rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var base rbacv1.ClusterRole
	if err := yaml.Unmarshal([]byte(strings.Split(string(data), "---")[0]), &base); err != nil {
		t.Fatal(err)
	}
	foundFinalizer := false
	for _, rule := range base.Rules {
		if slices.Contains(rule.APIGroups, "argoproj.io") {
			t.Fatal("Argo permissions escaped into base ClusterRole")
		}
		if slices.Contains(rule.Resources, "sovereignprojects/finalizers") && slices.Contains(rule.Verbs, "update") {
			foundFinalizer = true
		}
	}
	if !foundFinalizer {
		t.Fatal("missing SovereignProject finalizer permission")
	}
}
