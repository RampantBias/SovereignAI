package utility

import (
	"context"
	"fmt"

	"github.com/SovereignAI/internal/utilitycontract"
)

const (
	OperationRepositoryInitialize = "repository.initialize"
	OperationGitCreateBranch      = "git.createBranch"
	OperationGitCommit            = "git.commit"
	OperationGitPush              = "git.push"
	OperationGitMerge             = "git.merge"
	OperationTestRun              = "test.run"
	OperationBuildImage           = "build.image"

	CredentialClassRepository = "repository"
	CredentialClassRegistry   = "registry"
)

type Operation interface {
	Name() string
	Validate(input utilitycontract.Input) error
	Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error)
}

// Registry is the runner's closed operation vocabulary. A workflow selects a
// name from this registry; it cannot introduce executable implementation code.
type Registry struct {
	operations map[string]Operation
}

func NewRegistry(operations ...Operation) Registry {
	registry := Registry{operations: map[string]Operation{}}
	for _, operation := range operations {
		registry.operations[operation.Name()] = operation
	}
	return registry
}

func DefaultRegistry() Registry {
	return NewRegistry(
		RepositoryInitialize{},
		GitCreateBranch{},
		GitCommit{},
		GitPush{},
		GitMerge{},
		TestRun{},
		BuildImage{},
	)
}

func IsSupportedOperation(name string) bool {
	_, ok := DefaultRegistry().operations[name]
	return ok
}

func IsPrivilegedOperation(name string) bool {
	// Privileged means the controller must require a policy decision before the
	// operation is materialized. Credential needs are classified separately.
	switch name {
	case OperationGitCommit, OperationGitPush, OperationGitMerge, OperationBuildImage:
		return true
	default:
		return false
	}
}

// ExpectedCredentialClass tells the controller which operation-scoped secret,
// if any, may be mounted. The class names identify authority categories rather
// than concrete Secret names.
func ExpectedCredentialClass(name string) string {
	switch name {
	case OperationRepositoryInitialize, OperationGitPush, OperationGitMerge:
		return CredentialClassRepository
	case OperationBuildImage:
		return CredentialClassRegistry
	default:
		return ""
	}
}

func (r Registry) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	operation, ok := r.operations[input.Operation]
	if !ok {
		return utilitycontract.Result{}, fmt.Errorf("unsupported utility operation %q", input.Operation)
	}
	if err := operation.Validate(input); err != nil {
		return utilitycontract.Result{}, err
	}
	result, err := operation.Run(ctx, input)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if result.SchemaVersion == "" {
		result.SchemaVersion = utilitycontract.Version
	}
	if result.Outcome == "" {
		result.Outcome = "Succeeded"
	}
	// Operation code creates the effect and its proposed evidence. These checks
	// enforce the common contract before the collector is allowed to register
	// any output as an Artifact.
	if err := result.Validate(input.StagingPath); err != nil {
		return utilitycontract.Result{}, err
	}
	if err := result.ValidateAgainst(input); err != nil {
		return utilitycontract.Result{}, err
	}
	if err := result.ValidateArtifactFiles(input.StagingPath); err != nil {
		return utilitycontract.Result{}, err
	}
	return result, nil
}
