package inference

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type inferenceService struct {
	apiReader client.Reader
	client    client.Client
}
