package artifactcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
)

type Validator func([]byte) error

type Registry struct {
	validators map[string]Validator
}

func NewRegistry() *Registry {
	registry := &Registry{validators: map[string]Validator{}}
	register[ChangeRequest](registry, ChangeRequestContract)
	register[RepositoryRevision](registry, RepositoryRevisionContract)
	register[ImplementationPlan](registry, ImplementationPlanContract)
	register[TestChangeSet](registry, TestChangeSetContract)
	register[ChangeSet](registry, ChangeSetContract)
	register[PreparedCandidate](registry, PreparedCandidateContract)
	register[TestReport](registry, TestReportContract)
	register[CandidateRevision](registry, CandidateRevisionContract)
	register[ImageDigest](registry, ImageDigestContract)
	register[ValidationResult](registry, ValidationResultContract)
	register[MergeRevision](registry, MergeRevisionContract)
	return registry
}

var (
	defaultRegistry     *Registry
	defaultRegistryOnce sync.Once
)

func DefaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() {
		defaultRegistry = NewRegistry()
	})
	return defaultRegistry
}

func (r *Registry) Contracts() []string {
	contracts := make([]string, 0, len(r.validators))
	for contract := range r.validators {
		contracts = append(contracts, contract)
	}
	sort.Strings(contracts)
	return contracts
}

func (r *Registry) Validate(name, version string, data []byte) error {
	if strings.Contains(name, "/") || strings.Contains(version, "/") || name == "" || version == "" {
		return fmt.Errorf("contract must use exact non-empty name/version components")
	}
	if len(data) > MaxArtifactBytes {
		return fmt.Errorf("artifact content exceeds %d-byte limit", MaxArtifactBytes)
	}
	key := name + "/" + version
	validator, ok := r.validators[key]
	if !ok {
		return fmt.Errorf("artifact contract %q is not registered", key)
	}
	if err := validator(data); err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	return nil
}

func ValidateContract(contract string, data []byte) error {
	name, version, ok := strings.Cut(contract, "/")
	if !ok || strings.Contains(version, "/") {
		return fmt.Errorf("artifact contract %q must use exact name/version form", contract)
	}
	return DefaultRegistry().Validate(name, version, data)
}

type validatable interface {
	Validate() error
}

func register[T validatable](registry *Registry, contract string) {
	registry.validators[contract] = func(data []byte) error {
		_, err := decodeStrict[T](data)
		return err
	}
}

func decodeStrict[T validatable](data []byte) (T, error) {
	var value T
	if len(data) > MaxArtifactBytes {
		return value, fmt.Errorf("content exceeds %d-byte limit", MaxArtifactBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return value, fmt.Errorf("decode JSON: trailing value")
		}
		return value, fmt.Errorf("decode JSON trailing data: %w", err)
	}
	if err := value.Validate(); err != nil {
		return value, err
	}
	return value, nil
}
