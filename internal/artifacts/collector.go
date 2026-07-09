package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1alpha1"
)

type Collected struct {
	Spec v1alpha1.ArtifactSpec
}

func Collect(stagingRoot, artifactRoot, workflow, producer, sourceRevision string, outputs []agentcontract.ArtifactOutput) ([]Collected, error) {
	collected := make([]Collected, 0, len(outputs))
	for _, output := range outputs {
		source, err := containedPath(stagingRoot, output.Path)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(source)
		if err != nil {
			return nil, fmt.Errorf("inspect artifact %q: %w", output.Path, err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("artifact %q must be a regular file", output.Path)
		}
		digest, err := hashFile(source)
		if err != nil {
			return nil, err
		}
		destination := filepath.Join(artifactRoot, digest)
		if err := copyIfAbsent(source, destination); err != nil {
			return nil, err
		}
		contract, err := contractReference(output.Contract)
		if err != nil {
			return nil, err
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
	if !ok || name == "" || version == "" {
		return v1alpha1.ContractReference{}, fmt.Errorf("artifact contract %q must use name/version form", contract)
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

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open artifact: %w", err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash artifact: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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
