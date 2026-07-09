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

type Input struct {
	SchemaVersion     string          `json:"schemaVersion"`
	WorkflowID        string          `json:"workflowId"`
	StepName          string          `json:"stepName"`
	Attempt           int32           `json:"attempt"`
	Role              string          `json:"role"`
	Goal              string          `json:"goal"`
	Inputs            []ArtifactInput `json:"inputs,omitempty"`
	Capabilities      []string        `json:"capabilities,omitempty"`
	InferenceEndpoint string          `json:"inferenceEndpoint,omitempty"`
	MCPServer         string          `json:"mcpServer,omitempty"`
	WorkspacePath     string          `json:"workspacePath"`
	StagingPath       string          `json:"stagingPath"`
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
	Artifacts     []ArtifactOutput `json:"artifacts,omitempty"`
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
	if i.WorkflowID == "" || i.StepName == "" || i.Attempt < 1 || i.Goal == "" {
		return fmt.Errorf("workflowId, stepName, positive attempt, and goal are required")
	}
	if !filepath.IsAbs(i.WorkspacePath) || !filepath.IsAbs(i.StagingPath) {
		return fmt.Errorf("workspacePath and stagingPath must be absolute")
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
	root, err := filepath.Abs(stagingRoot)
	if err != nil {
		return fmt.Errorf("resolve staging root: %w", err)
	}
	for _, artifact := range r.Artifacts {
		if artifact.Contract == "" || artifact.Path == "" {
			return fmt.Errorf("artifact contract and path are required")
		}
		path, err := filepath.Abs(artifact.Path)
		if err != nil {
			return fmt.Errorf("resolve artifact path: %w", err)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("artifact path %q escapes staging root", artifact.Path)
		}
	}
	return nil
}
