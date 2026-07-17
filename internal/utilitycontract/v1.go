package utilitycontract

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const Version = "sovereign.ai/utility-contract/v1"

type ArtifactInput struct {
	Name     string `json:"name"`
	Contract string `json:"contract"`
	Digest   string `json:"digest,omitempty"`
	Path     string `json:"path,omitempty"`
}

type OutputObligation struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Required  bool   `json:"required,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
}

// AuthorityReference identifies the control-plane primitive whose admitted
// desired state authorized this runtime invocation.
type AuthorityReference struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	UID        string `json:"uid"`
}

type Input struct {
	SchemaVersion    string             `json:"schemaVersion"`
	WorkflowID       string             `json:"workflowId"`
	StepName         string             `json:"stepName"`
	Attempt          int32              `json:"attempt"`
	Authority        AuthorityReference `json:"authority"`
	PolicyDecisionID string             `json:"policyDecisionId"`
	Operation        string             `json:"operation"`
	IdempotencyKey   string             `json:"idempotencyKey"`
	CredentialClass  string             `json:"credentialClass,omitempty"`
	Parameters       map[string]string  `json:"parameters,omitempty"`
	Command          []string           `json:"command,omitempty"`
	Inputs           []ArtifactInput    `json:"inputs,omitempty"`
	Outputs          []OutputObligation `json:"outputs,omitempty"`
	WorkspacePath    string             `json:"workspacePath"`
	StagingPath      string             `json:"stagingPath"`
	ControlPath      string             `json:"controlPath,omitempty"`
	ResultPath       string             `json:"resultPath,omitempty"`
	AuditEventsPath  string             `json:"auditEventsPath,omitempty"`
}

type ArtifactOutput struct {
	Contract  string `json:"contract"`
	Path      string `json:"path"`
	MediaType string `json:"mediaType,omitempty"`
}

type Result struct {
	SchemaVersion string            `json:"schemaVersion"`
	Outcome       string            `json:"outcome"`
	Message       string            `json:"message,omitempty"`
	Error         *ResultError      `json:"error,omitempty"`
	Artifacts     []ArtifactOutput  `json:"artifacts,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

type ResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func ReadInput(path string) (Input, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Input{}, fmt.Errorf("read utility input contract: %w", err)
	}
	var input Input
	if err := json.Unmarshal(data, &input); err != nil {
		return Input{}, fmt.Errorf("decode utility input contract: %w", err)
	}
	if err := input.Validate(); err != nil {
		return Input{}, err
	}
	return input, nil
}

func ReadResult(path, stagingRoot string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("read utility result contract: %w", err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return Result{}, fmt.Errorf("decode utility result contract: %w", err)
	}
	if err := result.Validate(stagingRoot); err != nil {
		return Result{}, err
	}
	return result, nil
}

func WriteResult(path string, result Result) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode utility result contract: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create utility result directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o640); err != nil {
		return fmt.Errorf("write utility result contract: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish utility result contract: %w", err)
	}
	return nil
}

func (i Input) Validate() error {
	if i.SchemaVersion != Version {
		return fmt.Errorf("unsupported utility input schema %q", i.SchemaVersion)
	}
	if i.WorkflowID == "" || i.StepName == "" || i.Attempt < 1 {
		return fmt.Errorf("workflowId, stepName, and positive attempt are required")
	}
	if i.Operation == "" || i.IdempotencyKey == "" {
		return fmt.Errorf("operation and idempotencyKey are required")
	}
	if i.Authority.APIVersion == "" || i.Authority.Kind != "UtilityOperation" || i.Authority.Namespace == "" || i.Authority.Name == "" || i.Authority.UID == "" {
		return fmt.Errorf("authority must identify an immutable UtilityOperation")
	}
	if i.PolicyDecisionID == "" {
		return fmt.Errorf("policyDecisionId is required")
	}
	if i.CredentialClass != "" && i.CredentialClass != "repository" && i.CredentialClass != "registry" {
		return fmt.Errorf("unsupported credentialClass %q", i.CredentialClass)
	}
	for name := range i.Parameters {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("utility parameter names cannot be empty")
		}
	}
	for _, command := range i.Command {
		if command == "" {
			return fmt.Errorf("utility command entries cannot be empty")
		}
	}
	if !contractPathIsAbs(i.WorkspacePath) || !contractPathIsAbs(i.StagingPath) {
		return fmt.Errorf("workspacePath and stagingPath must be absolute")
	}
	for name, path := range map[string]string{
		"controlPath":     i.ControlPath,
		"resultPath":      i.ResultPath,
		"auditEventsPath": i.AuditEventsPath,
	} {
		if path != "" && !contractPathIsAbs(path) {
			return fmt.Errorf("%s must be absolute when provided", name)
		}
	}
	seen := map[string]struct{}{}
	for _, output := range i.Outputs {
		if output.Name == "" || output.Version == "" {
			return fmt.Errorf("output obligation name and version are required")
		}
		contract := output.Contract()
		if _, ok := seen[contract]; ok {
			return fmt.Errorf("duplicate output obligation %q", contract)
		}
		seen[contract] = struct{}{}
	}
	return nil
}

func contractPathIsAbs(value string) bool {
	// Contracts describe paths inside Linux workloads even when controllers and
	// unit tests execute on Windows.
	return filepath.IsAbs(value) || path.IsAbs(value)
}

func (r Result) Validate(stagingRoot string) error {
	if r.SchemaVersion != Version {
		return fmt.Errorf("unsupported utility result schema %q", r.SchemaVersion)
	}
	if r.Outcome != "Succeeded" && r.Outcome != "Failed" && r.Outcome != "Intervention" {
		return fmt.Errorf("invalid utility result outcome %q", r.Outcome)
	}
	if r.Error != nil && (r.Error.Code == "" || r.Error.Message == "") {
		return fmt.Errorf("utility result error code and message are required when error is provided")
	}
	for _, artifact := range r.Artifacts {
		if artifact.Contract == "" || artifact.Path == "" {
			return fmt.Errorf("artifact contract and path are required")
		}
		if _, err := ArtifactPath(stagingRoot, artifact.Path); err != nil {
			return err
		}
	}
	return nil
}

func (r Result) ValidateAgainst(input Input) error {
	if r.Outcome != "Succeeded" {
		return nil
	}
	declared := make(map[string]OutputObligation, len(input.Outputs))
	for _, output := range input.Outputs {
		declared[output.Contract()] = output
	}
	seen := make(map[string]struct{}, len(r.Artifacts))
	for _, artifact := range r.Artifacts {
		obligation, ok := declared[artifact.Contract]
		if len(declared) > 0 && !ok {
			return fmt.Errorf("artifact contract %q was not declared by utility output obligations", artifact.Contract)
		}
		if ok && obligation.MediaType != "" && artifact.MediaType != obligation.MediaType {
			return fmt.Errorf("artifact contract %q mediaType %q does not satisfy required mediaType %q", artifact.Contract, artifact.MediaType, obligation.MediaType)
		}
		seen[artifact.Contract] = struct{}{}
	}
	for contract, obligation := range declared {
		if obligation.Required {
			if _, ok := seen[contract]; !ok {
				return fmt.Errorf("required output contract %q was not produced", contract)
			}
		}
	}
	return nil
}

func (r Result) ValidateArtifactFiles(stagingRoot string) error {
	for _, artifact := range r.Artifacts {
		path, err := ArtifactPath(stagingRoot, artifact.Path)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("inspect artifact %q: %w", artifact.Path, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("artifact %q must be a regular file", artifact.Path)
		}
	}
	return nil
}

func ArtifactPath(stagingRoot, candidate string) (string, error) {
	root, err := filepath.Abs(stagingRoot)
	if err != nil {
		return "", fmt.Errorf("resolve staging root: %w", err)
	}
	path, err := filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve artifact path: %w", err)
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("artifact path %q escapes staging root", candidate)
	}
	return path, nil
}

func (o OutputObligation) Contract() string {
	if o.Version == "" {
		return o.Name
	}
	return o.Name + "/" + o.Version
}
