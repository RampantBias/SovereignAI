package artifactcontract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/SovereignAI/internal/contractschema"
	generationschemas "github.com/SovereignAI/schemas/generation/v1"
)

const (
	MaterializationCandidate MaterializationKind = "candidate"
	MaterializationWorkspace MaterializationKind = "workspace"

	MinimumCandidatePatchLines     = 5
	MaximumCandidatePatchLines     = 256
	MaximumCandidatePatchLineBytes = 4 << 10
)

var candidateSchemaFiles = map[string]string{
	ImplementationPlanContract: "implementation-plan.schema.json",
	TestChangeSetContract:      "change-set.schema.json",
	ChangeSetContract:          "change-set.schema.json",
}

// MaterializationKind identifies the durable evidence an agent must create.
// Candidate evidence is a structured domain document. Workspace evidence is
// the trusted diff derived from the attempt overlay.
type MaterializationKind string

// SourceArtifact is immutable accepted input used to derive authoritative
// provenance fields during materialization.
type SourceArtifact struct {
	Contract string
	Digest   string
	Content  []byte
}

// MaterializationEvidence is the complete trusted input to one materializer.
// Candidate is model-authored evidence. Summary and Patch are runtime-derived
// workspace evidence. A binding accepts only the evidence kind it declares.
type MaterializationEvidence struct {
	Candidate              []byte
	Summary                string
	Patch                  string
	Sources                []SourceArtifact
	WorkspacePaths         []string
	WorkspacePathsObserved bool
	WorkspacePathsComplete bool
}

type RejectionDiagnostic struct {
	Code    string
	Message string
}

// MaterializeFunc converts durable agent evidence into an authoritative
// artifact. Keeping this function type in artifactcontract lets new contracts
// register their policy without adding contract-specific code to an agent
// binary.
type MaterializeFunc func(MaterializationEvidence) ([]byte, error)

// Materializer owns the candidate schema and deterministic conversion policy
// for one artifact contract.
type Materializer struct {
	Contract        string
	Kind            MaterializationKind
	CandidateSchema contractschema.Definition
	materialize     MaterializeFunc
}

type MaterializerRegistry struct {
	bindings map[string]Materializer
}

func NewMaterializer(
	contract string,
	kind MaterializationKind,
	candidateSchema contractschema.Definition,
	materialize MaterializeFunc,
) Materializer {
	return Materializer{
		Contract: contract, Kind: kind, CandidateSchema: candidateSchema,
		materialize: materialize,
	}
}

func NewMaterializerRegistry() (*MaterializerRegistry, error) {
	schemas, err := contractschema.LoadStandalone(generationschemas.Files, candidateSchemaFiles, "-candidate")
	if err != nil {
		return nil, err
	}
	registry := &MaterializerRegistry{bindings: map[string]Materializer{}}
	planSchema, err := schemas.Lookup("implementation-plan", "v1")
	if err != nil {
		return nil, err
	}
	if err := registry.Register(NewMaterializer(
		ImplementationPlanContract, MaterializationCandidate,
		planSchema, materializeImplementationPlan,
	)); err != nil {
		return nil, err
	}
	for _, contract := range []string{TestChangeSetContract, ChangeSetContract} {
		name, version, _ := strings.Cut(contract, "/")
		schema, err := schemas.Lookup(name, version)
		if err != nil {
			return nil, err
		}
		contract := contract
		if err := registry.Register(NewMaterializer(
			contract, MaterializationWorkspace, schema,
			func(evidence MaterializationEvidence) ([]byte, error) {
				return materializeChangeSet(contract, evidence)
			},
		)); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

func (r *MaterializerRegistry) Register(binding Materializer) error {
	if r == nil {
		return fmt.Errorf("materializer registry is nil")
	}
	name, version, ok := strings.Cut(binding.Contract, "/")
	if !ok || name == "" || version == "" || strings.Contains(version, "/") {
		return fmt.Errorf("materializer contract %q must use exact name/version form", binding.Contract)
	}
	if binding.Kind != MaterializationCandidate && binding.Kind != MaterializationWorkspace {
		return fmt.Errorf("materializer %q has invalid kind %q", binding.Contract, binding.Kind)
	}
	if binding.materialize == nil {
		return fmt.Errorf("materializer %q has no implementation", binding.Contract)
	}
	if _, exists := r.bindings[binding.Contract]; exists {
		return fmt.Errorf("materializer %q is already registered", binding.Contract)
	}
	r.bindings[binding.Contract] = binding
	return nil
}

func (r *MaterializerRegistry) Resolve(name, version string) (Materializer, error) {
	if r == nil {
		return Materializer{}, fmt.Errorf("materializer registry is nil")
	}
	contract := name + "/" + version
	binding, ok := r.bindings[contract]
	if !ok {
		return Materializer{}, fmt.Errorf("output contract %q has no registered materializer", contract)
	}
	binding.CandidateSchema.JSON = append(json.RawMessage(nil), binding.CandidateSchema.JSON...)
	return binding, nil
}

func (m Materializer) Materialize(evidence MaterializationEvidence) ([]byte, error) {
	switch m.Kind {
	case MaterializationCandidate:
		if len(evidence.Candidate) == 0 {
			return nil, fmt.Errorf("%s candidate evidence is required", m.Contract)
		}
		if evidence.Patch != "" {
			return nil, fmt.Errorf("%s does not accept workspace patch evidence", m.Contract)
		}
	case MaterializationWorkspace:
		if len(evidence.Candidate) == 0 && strings.TrimSpace(evidence.Patch) == "" {
			return nil, fmt.Errorf("%s workspace patch evidence is required", m.Contract)
		}
	default:
		return nil, fmt.Errorf("materializer %q has invalid kind %q", m.Contract, m.Kind)
	}
	return m.materialize(evidence)
}

func materializeImplementationPlan(evidence MaterializationEvidence) ([]byte, error) {
	var candidate struct {
		Summary             string              `json:"summary"`
		AffectedPaths       []AffectedPath      `json:"affectedPaths"`
		ImplementationSteps []string            `json:"implementationSteps"`
		TestStrategy        []string            `json:"testStrategy"`
		AcceptanceMapping   []AcceptanceMapping `json:"acceptanceMapping"`
		Risks               []string            `json:"risks"`
		Assumptions         []string            `json:"assumptions"`
	}
	if err := decodeCandidate(evidence.Candidate, &candidate); err != nil {
		return nil, err
	}
	if err := validateAffectedWorkspacePaths(candidate.AffectedPaths, evidence.WorkspacePaths, evidence.WorkspacePathsObserved, evidence.WorkspacePathsComplete); err != nil {
		return nil, err
	}
	changeRequest, err := requiredMaterializationSource(evidence.Sources, ChangeRequestContract)
	if err != nil {
		return nil, err
	}
	revisionSource, revision, err := materializationRepositoryRevision(evidence.Sources)
	if err != nil {
		return nil, err
	}
	sort.Slice(candidate.AffectedPaths, func(i, j int) bool {
		return candidate.AffectedPaths[i].Path < candidate.AffectedPaths[j].Path
	})
	return marshalMaterializedArtifact(ImplementationPlanContract, ImplementationPlan{
		Summary: candidate.Summary, ChangeRequestDigest: changeRequest.Digest,
		RepositoryRevisionDigest: revisionSource.Digest, SourceCommit: revision.ResolvedCommit,
		AffectedPaths: candidate.AffectedPaths, ImplementationSteps: candidate.ImplementationSteps,
		TestStrategy: candidate.TestStrategy, AcceptanceMapping: candidate.AcceptanceMapping,
		Risks: candidate.Risks, Assumptions: candidate.Assumptions,
	})
}

func materializeChangeSet(contract string, evidence MaterializationEvidence) ([]byte, error) {
	if len(evidence.Candidate) != 0 {
		var candidate struct {
			Summary    string   `json:"summary"`
			PatchLines []string `json:"patchLines"`
		}
		if err := decodeCandidate(evidence.Candidate, &candidate); err != nil {
			return nil, err
		}
		patch, err := JoinCandidatePatchLines(candidate.PatchLines)
		if err != nil {
			return nil, err
		}
		evidence.Summary, evidence.Patch = candidate.Summary, patch
	}
	if strings.TrimSpace(evidence.Summary) == "" {
		return nil, fmt.Errorf("summary is required")
	}
	changeRequest, err := requiredMaterializationSource(evidence.Sources, ChangeRequestContract)
	if err != nil {
		return nil, err
	}
	plan, err := requiredMaterializationSource(evidence.Sources, ImplementationPlanContract)
	if err != nil {
		return nil, err
	}
	_, revision, err := materializationRepositoryRevision(evidence.Sources)
	if err != nil {
		return nil, err
	}
	files, lineCounts, err := DerivePatchMetadata(evidence.Patch)
	if err != nil {
		return nil, err
	}
	patchBytes := []byte(evidence.Patch)
	return marshalMaterializedArtifact(contract, ChangeSet{
		Format: "unified-diff", Summary: evidence.Summary, BaseCommit: revision.ResolvedCommit,
		ChangeRequestDigest: changeRequest.Digest, ImplementationPlanDigest: plan.Digest,
		Patch: evidence.Patch, PatchDigest: DigestBytes(patchBytes), Files: files,
		ByteCount: len(patchBytes), LineCounts: lineCounts,
	})
}

func JoinCandidatePatchLines(lines []string) (string, error) {
	if len(lines) < MinimumCandidatePatchLines || len(lines) > MaximumCandidatePatchLines {
		return "", fmt.Errorf("patchLines must contain %d through %d entries", MinimumCandidatePatchLines, MaximumCandidatePatchLines)
	}
	for index, line := range lines {
		if line == "" {
			return "", fmt.Errorf("patchLines[%d] must not be empty", index)
		}
		if len([]byte(line)) > MaximumCandidatePatchLineBytes {
			return "", fmt.Errorf("patchLines[%d] exceeds %d bytes", index, MaximumCandidatePatchLineBytes)
		}
		if !utf8.ValidString(line) || strings.ContainsAny(line, "\r\n\x00") {
			return "", fmt.Errorf("patchLines[%d] must be one UTF-8 line with no CR, LF, or NUL", index)
		}
	}
	patch := strings.Join(lines, "\n") + "\n"
	if len([]byte(patch)) > MaxPatchBytes {
		return "", fmt.Errorf("joined patch exceeds %d bytes", MaxPatchBytes)
	}
	return patch, nil
}

func materializationRepositoryRevision(sources []SourceArtifact) (SourceArtifact, RepositoryRevision, error) {
	source, err := requiredMaterializationSource(sources, RepositoryRevisionContract)
	if err != nil {
		return SourceArtifact{}, RepositoryRevision{}, err
	}
	var revision RepositoryRevision
	if err := decodeCandidate(source.Content, &revision); err != nil {
		return SourceArtifact{}, RepositoryRevision{}, fmt.Errorf("decode repository revision: %w", err)
	}
	return source, revision, nil
}

func requiredMaterializationSource(sources []SourceArtifact, contract string) (SourceArtifact, error) {
	var found *SourceArtifact
	for index := range sources {
		if sources[index].Contract != contract {
			continue
		}
		if found != nil {
			return SourceArtifact{}, fmt.Errorf("multiple %s inputs", contract)
		}
		found = &sources[index]
	}
	if found == nil {
		return SourceArtifact{}, fmt.Errorf("required %s input is missing", contract)
	}
	return *found, nil
}

func marshalMaterializedArtifact(contract string, value any) ([]byte, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err := ValidateContract(contract, content); err != nil {
		return nil, fmt.Errorf("final artifact: %w", err)
	}
	return content, nil
}

func decodeCandidate(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(NormalizeCandidateJSON(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode candidate: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode candidate trailing data")
	}
	return nil
}

func NormalizeCandidateJSON(data []byte) []byte {
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

// DiagnoseMaterialization converts a materializer error into a stable retry
// code without including rejected model-authored content.
func DiagnoseMaterialization(err error) RejectionDiagnostic {
	message := err.Error()
	code := "CandidateRejected"
	switch {
	case strings.Contains(message, "recognized test path"):
		code = "TestPathNotRecognized"
	case strings.Contains(message, "affectedPaths") &&
		(strings.Contains(message, "forbidden path segment") || strings.Contains(message, "repository-relative path") ||
			strings.Contains(message, "workspace_tree") || strings.Contains(message, "already exists")):
		code = "InvalidRepositoryPath"
	case strings.Contains(message, "decode candidate"):
		code = "CandidateSchemaViolation"
	case strings.Contains(message, "patchLines"):
		code = "InvalidPatchLines"
	case strings.Contains(message, "patch line") || strings.Contains(message, "unified diff") || strings.Contains(message, "patch has") ||
		strings.Contains(message, "patch contains") || strings.Contains(message, "patch is missing") || strings.Contains(message, "patch must"):
		code = "InvalidUnifiedDiff"
	}
	return RejectionDiagnostic{Code: code, Message: message}
}
