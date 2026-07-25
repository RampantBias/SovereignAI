package artifacts

import (
	"fmt"
	"io"
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

func Collect(stagingRoot, artifactRoot, workflow string, producer v1alpha1.TypedLocalReference, sourceRevision string, outputs []agentcontract.ArtifactOutput) ([]Collected, error) {
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
		digest := strings.TrimPrefix(artifactcontract.DigestBytes(content), "sha256:")
		destination := filepath.Join(artifactRoot, digest)
		if err := copyIfAbsent(source, destination); err != nil {
			return nil, err
		}
		storedContent, err := os.ReadFile(destination)
		if err != nil {
			return nil, fmt.Errorf("read stored artifact %q: %w", destination, err)
		}
		if storedDigest := strings.TrimPrefix(artifactcontract.DigestBytes(storedContent), "sha256:"); storedDigest != digest {
			return nil, fmt.Errorf("stored artifact %q digest mismatch: expected sha256:%s, got sha256:%s", destination, digest, storedDigest)
		}
		if err := artifactcontract.DefaultRegistry().Validate(contract.Name, contract.Version, storedContent); err != nil {
			return nil, fmt.Errorf("validate stored artifact %q: %w", destination, err)
		}
		collected = append(collected, Collected{Spec: v1alpha1.ArtifactSpec{
			WorkflowRef:    workflow,
			ProducerRef:    producer,
			Contract:       contract,
			Digest:         "sha256:" + digest,
			Path:           destination,
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

func copyIfAbsent(source, destination string) error {
	if _, err := os.Stat(destination); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return fmt.Errorf("create artifact store: %w", err)
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	temporary := destination + ".tmp"
	output, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o440)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		_ = os.Remove(temporary)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(temporary)
		return closeErr
	}
	if err := os.Rename(temporary, destination); err != nil {
		_ = os.Remove(temporary)
		if _, statErr := os.Stat(destination); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}
