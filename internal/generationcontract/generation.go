package generationcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/contractschema"
	artifactschemas "github.com/SovereignAI/schemas/artifacts/v1"
	generationschemas "github.com/SovereignAI/schemas/generation/v1"
)

var schemaFiles = map[string]string{
	artifactcontract.ImplementationPlanContract: "implementation-plan.schema.json",
	artifactcontract.TestChangeSetContract:      "change-set.schema.json",
	artifactcontract.ChangeSetContract:          "change-set.schema.json",
}

type SourceArtifact struct {
	Contract string
	Digest   string
	Content  []byte
}

type Binding struct {
	Schema contractschema.Definition
}

type Catalog struct{ schemas contractschema.Registry }

func NewCatalog() (*Catalog, error) {
	schemas, err := contractschema.Load(generationschemas.Files, artifactschemas.Files, schemaFiles, "-generation")
	if err != nil {
		return nil, err
	}
	return &Catalog{schemas: schemas}, nil
}

func (c *Catalog) Resolve(name, version string) (Binding, error) {
	schema, err := c.schemas.Lookup(name, version)
	if err != nil {
		return Binding{}, err
	}
	return Binding{Schema: schema}, nil
}

func (b Binding) Finalize(candidate []byte, sources []SourceArtifact) ([]byte, error) {
	switch b.Schema.Contract {
	case artifactcontract.ImplementationPlanContract:
		return finalizePlan(candidate, sources)
	case artifactcontract.TestChangeSetContract, artifactcontract.ChangeSetContract:
		return finalizeChangeSet(b.Schema.Contract, candidate, sources)
	default:
		return nil, fmt.Errorf("contract %q has no finalizer", b.Schema.Contract)
	}
}

func finalizePlan(candidate []byte, sources []SourceArtifact) ([]byte, error) {
	var generated struct {
		Summary             string                               `json:"summary"`
		AffectedPaths       []artifactcontract.AffectedPath      `json:"affectedPaths"`
		ImplementationSteps []string                             `json:"implementationSteps"`
		TestStrategy        []string                             `json:"testStrategy"`
		AcceptanceMapping   []artifactcontract.AcceptanceMapping `json:"acceptanceMapping"`
		Risks               []string                             `json:"risks"`
		Assumptions         []string                             `json:"assumptions"`
	}
	if err := decode(candidate, &generated); err != nil {
		return nil, err
	}
	changeRequest, err := requiredSource(sources, artifactcontract.ChangeRequestContract)
	if err != nil {
		return nil, err
	}
	revisionSource, revision, err := repositoryRevision(sources)
	if err != nil {
		return nil, err
	}
	sort.Slice(generated.AffectedPaths, func(i, j int) bool {
		return generated.AffectedPaths[i].Path < generated.AffectedPaths[j].Path
	})
	return marshalArtifact(artifactcontract.ImplementationPlanContract, artifactcontract.ImplementationPlan{
		Summary: generated.Summary, ChangeRequestDigest: changeRequest.Digest,
		RepositoryRevisionDigest: revisionSource.Digest, SourceCommit: revision.ResolvedCommit,
		AffectedPaths: generated.AffectedPaths, ImplementationSteps: generated.ImplementationSteps,
		TestStrategy: generated.TestStrategy, AcceptanceMapping: generated.AcceptanceMapping,
		Risks: generated.Risks, Assumptions: generated.Assumptions,
	})
}

func finalizeChangeSet(contract string, candidate []byte, sources []SourceArtifact) ([]byte, error) {
	var generated struct {
		Summary string `json:"summary"`
		Patch   string `json:"patch"`
	}
	if err := decode(candidate, &generated); err != nil {
		return nil, err
	}
	changeRequest, err := requiredSource(sources, artifactcontract.ChangeRequestContract)
	if err != nil {
		return nil, err
	}
	plan, err := requiredSource(sources, artifactcontract.ImplementationPlanContract)
	if err != nil {
		return nil, err
	}
	_, revision, err := repositoryRevision(sources)
	if err != nil {
		return nil, err
	}
	files, lineCounts, err := artifactcontract.DerivePatchMetadata(generated.Patch)
	if err != nil {
		return nil, err
	}
	patchBytes := []byte(generated.Patch)
	return marshalArtifact(contract, artifactcontract.ChangeSet{
		Format: "unified-diff", Summary: generated.Summary, BaseCommit: revision.ResolvedCommit,
		ChangeRequestDigest: changeRequest.Digest, ImplementationPlanDigest: plan.Digest,
		Patch: generated.Patch, PatchDigest: artifactcontract.DigestBytes(patchBytes),
		Files: files, ByteCount: len(patchBytes), LineCounts: lineCounts,
	})
}

func repositoryRevision(sources []SourceArtifact) (SourceArtifact, artifactcontract.RepositoryRevision, error) {
	source, err := requiredSource(sources, artifactcontract.RepositoryRevisionContract)
	if err != nil {
		return SourceArtifact{}, artifactcontract.RepositoryRevision{}, err
	}
	var revision artifactcontract.RepositoryRevision
	if err := decode(source.Content, &revision); err != nil {
		return SourceArtifact{}, artifactcontract.RepositoryRevision{}, fmt.Errorf("decode repository revision: %w", err)
	}
	return source, revision, nil
}

func requiredSource(sources []SourceArtifact, contract string) (SourceArtifact, error) {
	var found *SourceArtifact
	for index := range sources {
		if sources[index].Contract == contract {
			if found != nil {
				return SourceArtifact{}, fmt.Errorf("multiple %s inputs", contract)
			}
			found = &sources[index]
		}
	}
	if found == nil {
		return SourceArtifact{}, fmt.Errorf("required %s input is missing", contract)
	}
	return *found, nil
}

func marshalArtifact(contract string, value any) ([]byte, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := artifactcontract.ValidateContract(contract, content); err != nil {
		return nil, fmt.Errorf("final artifact: %w", err)
	}
	return content, nil
}

func decode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
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
