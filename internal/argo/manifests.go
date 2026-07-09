package argo

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/orchestration"
	"go.yaml.in/yaml/v2"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var AppProjectGVR = schema.GroupVersionResource{
	Group:    "argoproj.io",
	Version:  "v1alpha1",
	Resource: "appprojects",
}

func (argo *argoManager) RegisterProject(ctx context.Context, projectName, infraRepo, appRepo string) error {
	appProj, err := argo.buildAppProjectManifest(projectName, infraRepo, appRepo)
	if err != nil {
		return fmt.Errorf("failed to create the app project manifest for %s: %v", projectName, err)
	}
	codeAppSet, err := argo.buildCodeApplicationSet(projectName, infraRepo, appRepo)
	if err != nil {
		return fmt.Errorf("failed to create the application set manifest for %s: %v", projectName, err)
	}

	// TODO: Need to move towards App of Apps pattern and add project files to orchestrator repo (separate from standardized)
	// Apply to cluster; Keeping the build and apply separate to make migration easier later.
	err = argo.ApplyManifests(ctx, appProj, codeAppSet)
	if err != nil {
		return fmt.Errorf("failed to write AppProject to K8s API: %v", err)
	}
	return nil
}

// / Every Project needs an AppProject manifest which links the Project to the Repositories
func (argo *argoManager) buildAppProjectManifest(projectName string, infraRepo, appRepo string) (*orchestration.ManifestTemplate, error) {
	manifestMap := map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "AppProject",
		"metadata": map[string]interface{}{
			"name":      projectName,
			"namespace": "argocd",
			"labels": map[string]interface{}{
				"app.kubernetes.io/managed-by": "sovereign-orchestrator",
			},
		},
		"spec": map[string]interface{}{
			"description": fmt.Sprintf("Automated pipeline safety sandbox for %s", projectName),
			"sourceRepos": []interface{}{
				infraRepo,
				appRepo,
			},
			"destinations": []interface{}{
				map[string]interface{}{
					"server":    "https://kubernetes.default.svc",
					"namespace": projectName,
				},
			},
			"clusterResourceWhitelist": []interface{}{
				map[string]interface{}{
					"group": "",
					"kind":  "Namespace",
				},
			},
		},
	}

	yamlByes, err := yaml.Marshal(manifestMap)
	if err != nil {
		return &orchestration.ManifestTemplate{}, fmt.Errorf("failed to marshal application set to yaml: %w", err)
	}

	return &orchestration.ManifestTemplate{
		Name: projectName,
		Data: yamlByes,
	}, nil
}

// BuildCodeApplicationSet monitors the code repo, testing it against stable main infra
func (argo *argoManager) buildCodeApplicationSet(projectName string, infraRepo, appRepo string) (*orchestration.ManifestTemplate, error) {
	manifestMap := map[string]interface{}{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "ApplicationSet",
		"metadata": map[string]interface{}{
			"name":      fmt.Sprintf("%s-code-pipeline", projectName),
			"namespace": "argocd",
			"labels": map[string]interface{}{
				"app.kubernetes.io/managed-by": "sovereign-orchestrator",
			},
		},
		"spec": map[string]interface{}{
			"generators": []interface{}{
				map[string]interface{}{
					"git": map[string]interface{}{
						"repoURL":  appRepo,
						"revision": "*",
						"branches": true,
					},
				},
			},
			"template": map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "test-code-{{branch}}",
				},
				"spec": map[string]interface{}{
					"project": projectName,
					"sources": []interface{}{
						map[string]interface{}{
							"repoURL":        infraRepo,
							"targetRevision": "main", // Stable infrastructure base
							"path":           "deployments/overlays/testground",
						},
					},
					"destination": map[string]interface{}{
						"server":    "https://kubernetes.default.svc",
						"namespace": projectName,
					},
					"syncPolicy": map[string]interface{}{
						"automated": map[string]interface{}{
							"prune":    true,
							"selfHeal": true,
						},
						"syncOptions": []interface{}{
							"CreateNamespace=true",
						},
					},
				},
			},
		},
	}

	yamlBytes, err := yaml.Marshal(manifestMap)
	if err != nil {
		return &orchestration.ManifestTemplate{}, fmt.Errorf("failed to marshal application set to yaml: %w", err)
	}

	return &orchestration.ManifestTemplate{
		Name: fmt.Sprintf("%s-appset", projectName),
		Data: yamlBytes,
	}, nil

}
