package validation

import (
	"context"

	"k8s.io/apimachinery/pkg/types"
)

// ProjectRequest identifies the durable owner and its allowed source repository.
// The namespace pattern and resource allowlist are platform policy, not caller input.
type ProjectRequest struct {
	Name                 string
	UID                  types.UID
	InfrastructureRepo   string
	RepositoryCredential RepositoryCredential
}

// RepositoryCredential contains the HTTPS Git credential needed by Argo CD.
// It is held only in memory and projected into an Argo repository Secret.
type RepositoryCredential struct {
	Username string
	Password string
}

type ProjectProvisioner interface {
	EnsureProject(context.Context, ProjectRequest) (string, error)
	// DestroyProject returns true only after the owned AppProject is absent.
	// Owner identity is required even if provisioning never recorded its result.
	DestroyProject(context.Context, ProjectRequest) (bool, error)
}

type Request struct {
	Name                   string
	WorkflowNamespace      string
	Project                string
	InfrastructureRepo     string
	InfrastructureRevision string
	OverlayPath            string
	ImageSelector          string
	ImageDigest            string
	Commit                 string
}

type Status struct {
	ValidationResultDigest string
	ApplicationNamespace   string
	ApplicationName        string
	ApplicationUID         string
	ObservedRevision       string
	ObservedImages         []string
	DestinationNamespace   string
	Phase                  string
	SyncStatus             string
	HealthStatus           string
	Ready                  bool
	Failed                 bool
	AccessURL              string
	Message                string
}

type Provider interface {
	Start(context.Context, Request) (string, error)
	Status(context.Context, string) (Status, error)
	Destroy(context.Context, string) error
}
