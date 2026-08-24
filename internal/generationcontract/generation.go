package generationcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/contractschema"
	generationschemas "github.com/SovereignAI/schemas/generation/v1"
)

const (
	minimumPatchLines     = 5
	maximumPatchLines     = 8192
	maximumPatchLineBytes = 16 << 10
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

type RejectionDiagnostic struct {
	Code    string
	Message string
}

type Catalog struct{ schemas contractschema.Registry }

func NewCatalog() (*Catalog, error) {
	schemas, err := contractschema.LoadStandalone(generationschemas.Files, schemaFiles, "-generation")
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

// DiagnoseRejection converts a finalizer error into a stable code while
// retaining the validator's bounded, actionable message. The rejected model
// response is deliberately not part of the diagnostic.
func DiagnoseRejection(err error) RejectionDiagnostic {
	message := err.Error()
	code := "GeneratedArtifactRejected"
	switch {
	case strings.Contains(message, "recognized test path"):
		code = "TestPathNotRecognized"
	case strings.Contains(message, "affectedPaths") &&
		(strings.Contains(message, "forbidden path segment") || strings.Contains(message, "repository-relative path")):
		code = "InvalidRepositoryPath"
	case strings.Contains(message, "decode generation"):
		code = "GenerationSchemaViolation"
	case strings.Contains(message, "patchLines"):
		code = "InvalidPatchLines"
	case strings.Contains(message, "patch line") || strings.Contains(message, "unified diff") || strings.Contains(message, "patch has") ||
		strings.Contains(message, "patch contains") || strings.Contains(message, "patch is missing") || strings.Contains(message, "patch must"):
		code = "InvalidUnifiedDiff"
	}
	return RejectionDiagnostic{Code: code, Message: message}
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
		Summary    string   `json:"summary"`
		PatchLines []string `json:"patchLines"`
	}
	if err := decode(candidate, &generated); err != nil {
		return nil, err
	}
	patch, err := joinPatchLines(generated.PatchLines)
	if err != nil {
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
	files, lineCounts, err := artifactcontract.DerivePatchMetadata(patch)
	if err != nil {
		return nil, err
	}
	patchBytes := []byte(patch)
	return marshalArtifact(contract, artifactcontract.ChangeSet{
		Format: "unified-diff", Summary: generated.Summary, BaseCommit: revision.ResolvedCommit,
		ChangeRequestDigest: changeRequest.Digest, ImplementationPlanDigest: plan.Digest,
		Patch: patch, PatchDigest: artifactcontract.DigestBytes(patchBytes),
		Files: files, ByteCount: len(patchBytes), LineCounts: lineCounts,
	})
}

func joinPatchLines(lines []string) (string, error) {
	if len(lines) < minimumPatchLines || len(lines) > maximumPatchLines {
		return "", fmt.Errorf("patchLines must contain %d through %d entries", minimumPatchLines, maximumPatchLines)
	}
	for index, line := range lines {
		if line == "" {
			return "", fmt.Errorf("patchLines[%d] must not be empty", index)
		}
		if len([]byte(line)) > maximumPatchLineBytes {
			return "", fmt.Errorf("patchLines[%d] exceeds %d bytes", index, maximumPatchLineBytes)
		}
		if !utf8.ValidString(line) || strings.ContainsAny(line, "\r\n\x00") {
			return "", fmt.Errorf("patchLines[%d] must be one UTF-8 line with no CR, LF, or NUL", index)
		}
	}
	patch := strings.Join(lines, "\n") + "\n"
	if len([]byte(patch)) > artifactcontract.MaxPatchBytes {
		return "", fmt.Errorf("joined patch exceeds %d bytes", artifactcontract.MaxPatchBytes)
	}
	return patch, nil
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
	decoder := json.NewDecoder(bytes.NewReader(normalizeJSONControlCharacters(data)))
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
	const hexadecimal = "0123456789abcdef"
	var normalized []byte
	inString := false
	escaped := false
	for index, current := range data {
		if !inString {
			if current == '"' {
				inString = true
			}
		} else if escaped {
			escaped = false
		} else {
			switch current {
			case '\\':
				escaped = true
			case '"':
				inString = false
			default:
				if current < 0x20 {
					if normalized == nil {
						normalized = make([]byte, 0, len(data)+8)
						normalized = append(normalized, data[:index]...)
					}
					switch current {
					case '\b':
						normalized = append(normalized, '\\', 'b')
					case '\t':
						normalized = append(normalized, '\\', 't')
					case '\n':
						normalized = append(normalized, '\\', 'n')
					case '\f':
						normalized = append(normalized, '\\', 'f')
					case '\r':
						normalized = append(normalized, '\\', 'r')
					default:
						normalized = append(normalized, '\\', 'u', '0', '0', hexadecimal[current>>4], hexadecimal[current&0x0f])
					}
					continue
				}
			}
		}
		if normalized != nil {
			normalized = append(normalized, current)
		}
	}
	if normalized == nil {
		return data
	}
	return normalized
}
