package validation

import (
	"context"
	"encoding/base64"
	"fmt"
	"reflect"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func projectClient(objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		applicationGVR: "ApplicationList", appProjectGVR: "AppProjectList", repositorySecretGVR: "SecretList",
	}, objects...)
}

func projectRequestFixture() ProjectRequest {
	return ProjectRequest{
		Name: "calculator", UID: "project-uid", InfrastructureRepo: "https://git.example.test/calculator.git",
		RepositoryCredential: RepositoryCredential{Username: "demo-user", Password: "demo-token"},
	}
}

func TestEnsureProjectPolicyAndDrift(t *testing.T) {
	ctx := context.Background()
	request := projectRequestFixture()
	kube := projectClient()
	provider := NewArgoKustomize(kube, "argocd")
	name, err := provider.EnsureProject(ctx, request)
	if err != nil || name != "sov-calculator" {
		t.Fatalf("EnsureProject = %q, %v", name, err)
	}
	resource := kube.Resource(appProjectGVR).Namespace("argocd")
	project, err := resource.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantSpec := map[string]any{
		"sourceRepos":                []any{request.InfrastructureRepo},
		"destinations":               []any{map[string]any{"server": "https://kubernetes.default.svc", "namespace": "calculator-*"}},
		"namespaceResourceWhitelist": []any{map[string]any{"group": "", "kind": "Service"}, map[string]any{"group": "apps", "kind": "Deployment"}},
		"clusterResourceWhitelist":   []any{},
	}
	if !reflect.DeepEqual(project.Object["spec"], wantSpec) {
		t.Fatalf("unexpected policy: %#v", project.Object["spec"])
	}
	if err := checkProjectOwner(project, request); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(project.GetFinalizers(), []string{argoResourcesFinalizer}) {
		t.Fatal("missing Argo resource finalizer")
	}
	credential, err := kube.Resource(repositorySecretGVR).Namespace("argocd").Get(ctx, repositoryCredentialName(name), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRepositoryCredentialOwner(credential, request); err != nil {
		t.Fatal(err)
	}
	credentialData, found, err := unstructured.NestedStringMap(credential.Object, "data")
	if err != nil || !found {
		t.Fatalf("missing repository credential data: %#v %v", credential.Object, err)
	}
	decode := func(key string) string {
		value, err := base64.StdEncoding.DecodeString(credentialData[key])
		if err != nil {
			t.Fatal(err)
		}
		return string(value)
	}
	if decode("type") != "git" || decode("url") != request.InfrastructureRepo ||
		decode("username") != request.RepositoryCredential.Username ||
		decode("password") != request.RepositoryCredential.Password ||
		decode("project") != name {
		t.Fatalf("unexpected repository credential projection")
	}
	kube.ClearActions()
	if _, err := provider.EnsureProject(ctx, request); err != nil {
		t.Fatal(err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() != "get" {
			t.Fatalf("ready project should not be rewritten: %s", action.GetVerb())
		}
	}

	project.Object["spec"] = map[string]any{"sourceRepos": []any{"*"}, "destinations": []any{map[string]any{"server": "*", "namespace": "*"}}, "roles": []any{map[string]any{"name": "extra-access"}}}
	project.SetFinalizers(nil)
	project.SetAnnotations(map[string]string{"example.test/note": "preserve"})
	project.SetResourceVersion("7")
	if _, err := resource.Update(ctx, project, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	kube.PrependReactor("update", "appprojects", func(action ktesting.Action) (bool, runtime.Object, error) {
		updated := action.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		if updated.GetResourceVersion() != "7" {
			t.Fatal("policy repair lost its resourceVersion precondition")
		}
		return false, nil, nil
	})
	request.InfrastructureRepo = "https://git.example.test/new-calculator.git"
	wantSpec["sourceRepos"] = []any{request.InfrastructureRepo}
	if _, err := provider.EnsureProject(ctx, request); err != nil {
		t.Fatal(err)
	}
	updated, err := resource.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.Object["spec"], wantSpec) || updated.GetAnnotations()["example.test/note"] != "preserve" || len(updated.GetFinalizers()) != 1 {
		t.Fatalf("drift was not repaired safely: %#v", updated.Object)
	}
	credential, err = kube.Resource(repositorySecretGVR).Namespace("argocd").Get(ctx, repositoryCredentialName(name), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	credentialData, _, _ = unstructured.NestedStringMap(credential.Object, "data")
	encodedURL, _ := base64.StdEncoding.DecodeString(credentialData["url"])
	if string(encodedURL) != request.InfrastructureRepo {
		t.Fatalf("repository credential URL drift was not repaired")
	}
	// External deletion is repaired on the next reconciliation.
	if err := kube.Tracker().Delete(appProjectGVR, "argocd", name); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.EnsureProject(ctx, request); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryCredentialOwnershipConflictNeverMutates(t *testing.T) {
	request := projectRequestFixture()
	project := desiredAppProject(request, "sov-calculator", "argocd")
	credential := desiredRepositoryCredential(request, "sov-calculator", "argocd")
	credential.SetOwnerReferences(nil)
	kube := projectClient(project, credential)
	if _, err := NewArgoKustomize(kube, "argocd").EnsureProject(context.Background(), request); !apierrors.IsConflict(err) {
		t.Fatalf("expected repository credential ownership conflict: %v", err)
	}
	for _, action := range kube.Actions() {
		if action.GetResource().Resource == "secrets" && action.GetVerb() != "get" {
			t.Fatalf("credential conflict caused %s", action.GetVerb())
		}
	}
}

func TestProjectOwnershipConflictsNeverMutate(t *testing.T) {
	for _, kind := range []string{"unmanaged", "wrong UID", "wrong owner", "no owner"} {
		t.Run(kind, func(t *testing.T) {
			request := projectRequestFixture()
			project := desiredAppProject(request, "sov-calculator", "argocd")
			switch kind {
			case "unmanaged":
				project.SetLabels(nil)
			case "wrong UID":
				refs := project.GetOwnerReferences()
				refs[0].UID = "old-project-uid"
				project.SetOwnerReferences(refs)
			case "wrong owner":
				refs := project.GetOwnerReferences()
				refs[0].Name = "other"
				project.SetOwnerReferences(refs)
			case "no owner":
				project.SetOwnerReferences(nil)
			}
			kube := projectClient(project)
			provider := NewArgoKustomize(kube, "argocd")
			if _, err := provider.EnsureProject(context.Background(), request); !apierrors.IsConflict(err) {
				t.Fatalf("ensure must conflict: %v", err)
			}
			if _, err := provider.DestroyProject(context.Background(), request); !apierrors.IsConflict(err) {
				t.Fatalf("destroy must conflict: %v", err)
			}
			for _, action := range kube.Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("conflict caused %s", action.GetVerb())
				}
			}
		})
	}
}

func TestProjectProvisioningOperationalErrors(t *testing.T) {
	for _, verb := range []string{"get", "create", "update"} {
		t.Run(verb, func(t *testing.T) {
			request := projectRequestFixture()
			kube := projectClient()
			if verb == "update" {
				project := desiredAppProject(request, "sov-calculator", "argocd")
				project.SetFinalizers(nil)
				if err := kube.Tracker().Add(project); err != nil {
					t.Fatal(err)
				}
			}
			kube.PrependReactor(verb, "appprojects", func(ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, apierrors.NewForbidden(appProjectGVR.GroupResource(), "sov-calculator", fmt.Errorf("denied"))
			})
			if _, err := NewArgoKustomize(kube, "argocd").EnsureProject(context.Background(), request); !apierrors.IsForbidden(err) {
				t.Fatalf("expected operational error: %v", err)
			}
		})
	}
}

func TestRepositoryCredentialProvisioningErrorIsReturned(t *testing.T) {
	request := projectRequestFixture()
	kube := projectClient(desiredAppProject(request, "sov-calculator", "argocd"))
	kube.PrependReactor("create", "secrets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(repositorySecretGVR.GroupResource(), repositoryCredentialName("sov-calculator"), fmt.Errorf("denied"))
	})
	if _, err := NewArgoKustomize(kube, "argocd").EnsureProject(context.Background(), request); !apierrors.IsForbidden(err) {
		t.Fatalf("expected repository credential provisioning error: %v", err)
	}
}

func TestProjectCreateRaceDoesNotAdoptCollision(t *testing.T) {
	kube := projectClient()
	kube.PrependReactor("create", "appprojects", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewAlreadyExists(appProjectGVR.GroupResource(), "sov-calculator")
	})
	if _, err := NewArgoKustomize(kube, "argocd").EnsureProject(context.Background(), projectRequestFixture()); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("expected create conflict: %v", err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "patch" || action.GetVerb() == "update" {
			t.Fatal("race must not overwrite the winner")
		}
	}
}

func TestDestroyProjectWaitsForApplicationsAndDeletion(t *testing.T) {
	ctx := context.Background()
	request := projectRequestFixture()
	project := desiredAppProject(request, "sov-calculator", "argocd")
	project.SetUID("argo-project-uid")
	project.SetResourceVersion("7")
	// Even an unmanaged reference must block project deletion.
	app := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Application", "metadata": map[string]any{"name": "preview", "namespace": "argocd"}, "spec": map[string]any{"project": "sov-calculator"}}}
	kube := projectClient(project, app)
	provider := NewArgoKustomize(kube, "argocd")
	if done, err := provider.DestroyProject(ctx, request); done || err != nil {
		t.Fatalf("must wait for app: %t %v", done, err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("active Application must not be deleted")
		}
	}
	if err := kube.Tracker().Delete(applicationGVR, "argocd", "preview"); err != nil {
		t.Fatal(err)
	}
	kube.PrependReactor("delete", "appprojects", func(action ktesting.Action) (bool, runtime.Object, error) {
		options := action.(ktesting.DeleteAction).GetDeleteOptions()
		if options.Preconditions == nil || *options.Preconditions.UID != "argo-project-uid" || *options.Preconditions.ResourceVersion != "7" {
			t.Fatalf("unsafe delete: %#v", options)
		}
		project.SetDeletionTimestamp(&metav1.Time{Time: metav1.Now().Time})
		return true, nil, kube.Tracker().Update(appProjectGVR, project, "argocd")
	})
	if done, err := provider.DestroyProject(ctx, request); done || err != nil {
		t.Fatalf("DELETE is not completion: %t %v", done, err)
	}
	if done, err := provider.DestroyProject(ctx, request); done || err != nil {
		t.Fatalf("terminating project must still wait: %t %v", done, err)
	}
	if _, err := provider.EnsureProject(ctx, request); err == nil {
		t.Fatal("terminating project cannot be marked ready")
	}
	if err := kube.Tracker().Delete(appProjectGVR, "argocd", "sov-calculator"); err != nil {
		t.Fatal(err)
	}
	if done, err := provider.DestroyProject(ctx, request); !done || err != nil {
		t.Fatalf("absent project must finish: %t %v", done, err)
	}
}

func TestDestroyProjectListFailureDoesNotDelete(t *testing.T) {
	request := projectRequestFixture()
	kube := projectClient(desiredAppProject(request, "sov-calculator", "argocd"))
	kube.PrependReactor("list", "applications", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, fmt.Errorf("unavailable") })
	if done, err := NewArgoKustomize(kube, "argocd").DestroyProject(context.Background(), request); done || err == nil {
		t.Fatalf("must retain on list failure: %t %v", done, err)
	}
	for _, action := range kube.Actions() {
		if action.GetVerb() == "delete" {
			t.Fatal("cannot delete without checking Applications")
		}
	}
}
