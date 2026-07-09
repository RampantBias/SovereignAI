package validation

import "context"

type Request struct {
	Name                   string
	WorkflowNamespace      string
	Project                string
	InfrastructureRepo     string
	InfrastructureRevision string
	OverlayPath            string
	ImageName              string
	ImageDigest            string
	Commit                 string
}

type Status struct {
	Phase     string
	Ready     bool
	Failed    bool
	AccessURL string
	Message   string
}

type Provider interface {
	Start(context.Context, Request) (string, error)
	Status(context.Context, string) (Status, error)
	Destroy(context.Context, string) error
}
