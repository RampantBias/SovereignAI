package artifactcontract

import (
	"encoding/json"
	"fmt"
	"slices"
)

type WorkspaceMutationPolicy struct {
	contract string
	actions  map[string]string
}

func NewWorkspaceMutationPolicy(contract string, sources []SourceArtifact) (*WorkspaceMutationPolicy, error) {
	policy := &WorkspaceMutationPolicy{contract: contract, actions: map[string]string{}}
	switch contract {
	case TestChangeSetContract:
		return policy, nil
	case ChangeSetContract:
		source, err := requiredMaterializationSource(sources, ImplementationPlanContract)
		if err != nil {
			return nil, err
		}
		var plan ImplementationPlan
		if err := json.Unmarshal(source.Content, &plan); err != nil {
			return nil, fmt.Errorf("decode implementation plan path policy: %w", err)
		}
		for _, affected := range plan.AffectedPaths {
			policy.actions[affected.Path] = affected.Action
		}
		return policy, nil
	default:
		return nil, fmt.Errorf("output contract %q has no workspace mutation policy", contract)
	}
}

func (p *WorkspaceMutationPolicy) ValidateUpdate(path string) error {
	if p == nil {
		return nil
	}
	if p.contract == TestChangeSetContract {
		if !isTestPath(path) {
			return fmt.Errorf("workspace update path %q is not a recognized test path", path)
		}
		return nil
	}
	action, ok := p.actions[path]
	if !ok {
		return fmt.Errorf("workspace update path %q is not authorized by implementation-plan affectedPaths", path)
	}
	if isTestPath(path) {
		return fmt.Errorf("developer workspace update path %q is a recognized test path", path)
	}
	if action != "modify" && action != "add" {
		return fmt.Errorf("workspace update path %q requires affectedPaths action modify or add, got %q", path, action)
	}
	return nil
}

// AuthorizedUpdatePaths returns the unique, sorted subset of paths that the
// current output contract permits an agent to update. Callers can use this to
// recover an omitted model-authored path only when the contract leaves exactly
// one possible target.
func (p *WorkspaceMutationPolicy) AuthorizedUpdatePaths(paths []string) []string {
	authorized := make([]string, 0, len(paths))
	for _, path := range paths {
		if p == nil || p.ValidateUpdate(path) == nil {
			authorized = append(authorized, path)
		}
	}
	slices.Sort(authorized)
	return slices.Compact(authorized)
}

func (p *WorkspaceMutationPolicy) ValidateCreate(path string) error {
	if p == nil {
		return nil
	}
	if p.contract == TestChangeSetContract {
		return fmt.Errorf("test-change-set creation is not authorized; select an existing test path returned by workspace_tree")
	}
	return p.requireAction(path, "add", "create")
}

func (p *WorkspaceMutationPolicy) ValidateDelete(path string) error {
	if p == nil {
		return nil
	}
	if p.contract == TestChangeSetContract {
		if !isTestPath(path) {
			return fmt.Errorf("workspace delete path %q is not a recognized test path", path)
		}
		return nil
	}
	return p.requireAction(path, "delete", "delete")
}

func (p *WorkspaceMutationPolicy) requireAction(path, expected, operation string) error {
	if isTestPath(path) {
		return fmt.Errorf("developer workspace %s path %q is a recognized test path", operation, path)
	}
	action, ok := p.actions[path]
	if !ok {
		return fmt.Errorf("workspace %s path %q is not authorized by implementation-plan affectedPaths", operation, path)
	}
	if action != expected {
		return fmt.Errorf("workspace %s path %q requires affectedPaths action %q, got %q", operation, path, expected, action)
	}
	return nil
}

func validateAffectedWorkspacePaths(affected []AffectedPath, paths []string, observed, complete bool) error {
	if !observed {
		return nil
	}
	if !complete {
		return fmt.Errorf("workspace_tree must return a complete repository path set before implementation-plan affectedPaths can be accepted")
	}
	existing := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		existing[path] = struct{}{}
	}
	for index, item := range affected {
		_, exists := existing[item.Path]
		switch item.Action {
		case "add":
			if exists {
				return fmt.Errorf("affectedPaths[%d] path %q uses action add but already exists", index, item.Path)
			}
		case "modify", "delete":
			if !exists {
				return fmt.Errorf("affectedPaths[%d] path %q uses action %s but was not returned by workspace_tree", index, item.Path, item.Action)
			}
		}
	}
	return nil
}
