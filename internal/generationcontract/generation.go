// Package generationcontract preserves the legacy generation-facing API.
// Contract-specific materialization is owned by artifactcontract.
package generationcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/contractschema"
)

const (
	minimumPatchLines     = artifactcontract.MinimumCandidatePatchLines
	maximumPatchLines     = artifactcontract.MaximumCandidatePatchLines
	maximumPatchLineBytes = artifactcontract.MaximumCandidatePatchLineBytes
)

var schemaFiles = map[string]string{
	artifactcontract.ImplementationPlanContract: "implementation-plan.schema.json",
	artifactcontract.TestChangeSetContract:      "change-set.schema.json",
	artifactcontract.ChangeSetContract:          "change-set.schema.json",
}

type SourceArtifact = artifactcontract.SourceArtifact
type RejectionDiagnostic = artifactcontract.RejectionDiagnostic

type Binding struct {
	Schema       contractschema.Definition
	materializer artifactcontract.Materializer
}

type Catalog struct {
	materializers *artifactcontract.MaterializerRegistry
}

func NewCatalog() (*Catalog, error) {
	registry, err := artifactcontract.NewMaterializerRegistry()
	if err != nil {
		return nil, err
	}
	return &Catalog{materializers: registry}, nil
}

func (c *Catalog) Resolve(name, version string) (Binding, error) {
	materializer, err := c.materializers.Resolve(name, version)
	if err != nil {
		return Binding{}, err
	}
	return Binding{Schema: materializer.CandidateSchema, materializer: materializer}, nil
}

func (b Binding) Finalize(candidate []byte, sources []SourceArtifact) ([]byte, error) {
	return b.materializer.Materialize(artifactcontract.MaterializationEvidence{
		Candidate: candidate,
		Sources:   sources,
	})
}

func DiagnoseRejection(err error) RejectionDiagnostic {
	diagnostic := artifactcontract.DiagnoseMaterialization(err)
	if strings.Contains(diagnostic.Message, "decode generation") {
		diagnostic.Code = "GenerationSchemaViolation"
		return diagnostic
	}
	switch diagnostic.Code {
	case "CandidateRejected":
		diagnostic.Code = "GeneratedArtifactRejected"
	case "CandidateSchemaViolation":
		diagnostic.Code = "GenerationSchemaViolation"
	}
	return diagnostic
}

func FinalizeWorkspaceChangeSet(contract, summary, patch string, sources []SourceArtifact) ([]byte, error) {
	name, version, ok := strings.Cut(contract, "/")
	if !ok {
		return nil, fmt.Errorf("contract %q must use exact name/version form", contract)
	}
	registry, err := artifactcontract.NewMaterializerRegistry()
	if err != nil {
		return nil, err
	}
	materializer, err := registry.Resolve(name, version)
	if err != nil {
		return nil, err
	}
	return materializer.Materialize(artifactcontract.MaterializationEvidence{
		Summary: summary,
		Patch:   patch,
		Sources: sources,
	})
}

func joinPatchLines(lines []string) (string, error) {
	return artifactcontract.JoinCandidatePatchLines(lines)
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(artifactcontract.NormalizeCandidateJSON(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode generation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode generation trailing data")
	}
	return nil
}

func normalizeJSONControlCharacters(data []byte) []byte {
	return artifactcontract.NormalizeCandidateJSON(data)
}
