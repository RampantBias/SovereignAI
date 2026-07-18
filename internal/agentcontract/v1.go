package agentcontract

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const Version = "sovereign.ai/agent-contract/v1"

type ArtifactInput struct {
	Name     string `json:"name"`
	Contract string `json:"contract"`
	Digest   string `json:"digest"`
	Path     string `json:"path"`
}

type OutputObligation struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Required  bool   `json:"required,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
}

// WorkspaceWriteAuthority is the fencing identity granted by the workflow's
// Kubernetes Lease. The epoch changes every time write authority changes
// hands, so runtime output can be attributed to one exclusive writer term.
type WorkspaceWriteAuthority struct {
	LeaseName      string `json:"leaseName"`
	HolderIdentity string `json:"holderIdentity"`
	WriterEpoch    int32  `json:"writerEpoch"`
}

type Input struct {
	SchemaVersion     string                  `json:"schemaVersion"`
	WorkflowID        string                  `json:"workflowId"`
	StepName          string                  `json:"stepName"`
	Attempt           int32                   `json:"attempt"`
	Role              string                  `json:"role"`
	Responsibility    string                  `json:"responsibility"`
	Inputs            []ArtifactInput         `json:"inputs,omitempty"`
	Outputs           []OutputObligation      `json:"outputs,omitempty"`
	Capabilities      []string                `json:"capabilities,omitempty"`
	InferenceEndpoint string                  `json:"inferenceEndpoint,omitempty"`
	MCPServer         string                  `json:"mcpServer,omitempty"`
	WorkspacePath     string                  `json:"workspacePath"`
	StagingPath       string                  `json:"stagingPath"`
	ControlPath       string                  `json:"controlPath,omitempty"`
	ResultPath        string                  `json:"resultPath,omitempty"`
	AuditEventsPath   string                  `json:"auditEventsPath,omitempty"`
	WorkspaceWrite    WorkspaceWriteAuthority `json:"workspaceWrite"`
}

type ArtifactOutput struct {
	Contract  string `json:"contract"`
	Path      string `json:"path"`
	MediaType string `json:"mediaType,omitempty"`
}

type Result struct {
	SchemaVersion string           `json:"schemaVersion"`
	Outcome       string           `json:"outcome"`
	Message       string           `json:"message,omitempty"`
	Error         *ResultError     `json:"error,omitempty"`
	Artifacts     []ArtifactOutput `json:"artifacts,omitempty"`
}

type ResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func ReadInput(path string) (Input, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Input{}, fmt.Errorf("read input contract: %w", err)
	}
	var input Input
	if err := json.Unmarshal(data, &input); err != nil {
		return Input{}, fmt.Errorf("decode input contract: %w", err)
	}
	if err := input.Validate(); err != nil {
		return Input{}, err
	}
	return input, nil
}

func ReadResult(path, stagingRoot string) (Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{}, fmt.Errorf("read result contract: %w", err)
	}
	var result Result
	if err := json.Unmarshal(data, &result); err != nil {
		return Result{}, fmt.Errorf("decode result contract: %w", err)
	}
	if err := result.Validate(stagingRoot); err != nil {
		return Result{}, err
	}
	return result, nil
}

func ReadResultForInput(path string, input Input) (Result, error) {
	result, err := ReadResult(path, input.StagingPath)
	if err != nil {
		return Result{}, err
	}
	if err := result.ValidateAgainst(input); err != nil {
		return Result{}, err
	}
	if err := result.ValidateArtifactFiles(input.StagingPath); err != nil {
		return Result{}, err
	}
	return result, nil
}

func WriteResult(path string, result Result) error {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("encode result contract: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, data, 0o640); err != nil {
		return fmt.Errorf("write result contract: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish result contract: %w", err)
	}
	return nil
}

func (i Input) Validate() error {
	if i.SchemaVersion != Version {
		return fmt.Errorf("unsupported input schema %q", i.SchemaVersion)
	}
	if i.WorkflowID == "" || i.StepName == "" || i.Attempt < 1 || i.Responsibility == "" {
		return fmt.Errorf("workflowId, stepName, positive attempt, and responsibility are required")
	}
	if !filepath.IsAbs(i.WorkspacePath) || !filepath.IsAbs(i.StagingPath) {
		return fmt.Errorf("workspacePath and stagingPath must be absolute")
	}
	if i.WorkspaceWrite.LeaseName == "" || i.WorkspaceWrite.HolderIdentity == "" || i.WorkspaceWrite.WriterEpoch < 1 {
		return fmt.Errorf("workspaceWrite must identify a lease holder and positive writer epoch")
	}
	for name, path := range map[string]string{
		"controlPath":     i.ControlPath,
		"resultPath":      i.ResultPath,
		"auditEventsPath": i.AuditEventsPath,
	} {
		if path != "" && !filepath.IsAbs(path) {
			return fmt.Errorf("%s must be absolute when provided", name)
		}
	}
	seen := map[string]struct{}{}
	for _, output := range i.Outputs {
		if output.Name == "" || output.Version == "" {
			return fmt.Errorf("output obligation name and version are required")
		}
		contract := output.contract()
		if _, ok := seen[contract]; ok {
			return fmt.Errorf("duplicate output obligation %q", contract)
		}
		seen[contract] = struct{}{}
	}
	return nil
}

func (r Result) Validate(stagingRoot string) error {
	if r.SchemaVersion != Version {
		return fmt.Errorf("unsupported result schema %q", r.SchemaVersion)
	}
	if r.Outcome != "Succeeded" && r.Outcome != "Failed" && r.Outcome != "Intervention" {
		return fmt.Errorf("invalid result outcome %q", r.Outcome)
	}
	if r.Error != nil && (r.Error.Code == "" || r.Error.Message == "") {
		return fmt.Errorf("result error code and message are required when error is provided")
	}
	for _, artifact := range r.Artifacts {
		if artifact.Contract == "" || artifact.Path == "" {
			return fmt.Errorf("artifact contract and path are required")
		}
		if _, err := artifactPath(stagingRoot, artifact.Path); err != nil {
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
		declared[output.contract()] = output
	}
	seen := make(map[string]struct{}, len(r.Artifacts))
	for _, artifact := range r.Artifacts {
		obligation, ok := declared[artifact.Contract]
		if len(declared) > 0 && !ok {
			return fmt.Errorf("artifact contract %q was not declared by input obligations", artifact.Contract)
		}
		if ok && obligation.MediaType != "" && artifact.MediaType != obligation.MediaType {
			return fmt.Errorf("artifact contract %q mediaType %q does not satisfy required mediaType %q", artifact.Contract, artifact.MediaType, obligation.MediaType)
		}
		seen[artifact.Contract] = struct{}{}
	}
	for contract, obligation := range declared {
		if !obligation.Required {
			continue
		}
		if _, ok := seen[contract]; !ok {
			return fmt.Errorf("required output contract %q was not produced", contract)
		}
	}
	return nil
}

func (r Result) ValidateArtifactFiles(stagingRoot string) error {
	for _, artifact := range r.Artifacts {
		path, err := artifactPath(stagingRoot, artifact.Path)
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

func artifactPath(stagingRoot, candidate string) (string, error) {
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

func (o OutputObligation) contract() string {
	if o.Version == "" {
		return o.Name
	}
	return o.Name + "/" + o.Version
}
