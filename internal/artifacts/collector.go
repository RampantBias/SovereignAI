package artifacts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
	"github.com/SovereignAI/internal/artifactcontract"
)

type Collected struct {
	Spec v1alpha1.ArtifactSpec
}

func Collect(stagingRoot, artifactRoot string, workflow v1alpha1.UIDReference, producer v1alpha1.TypedLocalReference, sourceRevision string, outputs []agentcontract.ArtifactOutput) ([]Collected, error) {
	collected := make([]Collected, 0, len(outputs))
	for _, output := range outputs {
		if output.MediaType != "" && output.MediaType != "application/json" {
			return nil, fmt.Errorf("artifact contract %q requires application/json, got %q", output.Contract, output.MediaType)
		}
		contract, err := contractReference(output.Contract)
		if err != nil {
			return nil, err
		}
		source, err := containedPath(stagingRoot, output.Path)
		if err != nil {
			return nil, err
		}
		info, err := os.Lstat(source)
		if err != nil {
			return nil, fmt.Errorf("inspect artifact %q: %w", output.Path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("artifact %q must be a regular file", output.Path)
		}
		if info.Size() > artifactcontract.MaxArtifactBytes {
			return nil, fmt.Errorf("artifact %q exceeds %d-byte limit", output.Path, artifactcontract.MaxArtifactBytes)
		}
		content, err := os.ReadFile(source)
		if err != nil {
			return nil, fmt.Errorf("read artifact %q: %w", output.Path, err)
		}
		if err := artifactcontract.DefaultRegistry().Validate(contract.Name, contract.Version, content); err != nil {
			return nil, fmt.Errorf("validate artifact %q: %w", output.Path, err)
		}
		stored, err := Put(artifactRoot, content)
		if err != nil {
			return nil, fmt.Errorf("store artifact %q: %w", output.Path, err)
		}
		if err := artifactcontract.DefaultRegistry().Validate(contract.Name, contract.Version, stored.Bytes); err != nil {
			return nil, fmt.Errorf("validate stored artifact %q: %w", stored.Path, err)
		}
		collected = append(collected, Collected{Spec: v1alpha1.ArtifactSpec{
			WorkflowRef:    workflow,
			ProducerRef:    producer,
			Contract:       contract,
			Digest:         stored.Digest,
			Path:           stored.Path,
			SourceRevision: sourceRevision,
		}})
	}
	return collected, nil
}

func contractReference(contract string) (v1alpha1.ContractReference, error) {
	name, version, ok := strings.Cut(contract, "/")
	if !ok || name == "" || version == "" || strings.Contains(version, "/") {
		return v1alpha1.ContractReference{}, fmt.Errorf("artifact contract %q must use exact name/version form", contract)
	}
	return v1alpha1.ContractReference{Name: name, Version: version}, nil
}

func containedPath(root, candidate string) (string, error) {
	rootPath, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidatePath, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(rootPath, candidatePath)
	if err != nil || relative == ".." || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", fmt.Errorf("artifact path %q escapes staging root", candidate)
	}
	return candidatePath, nil
}
