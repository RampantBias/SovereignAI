package artifactcontract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const CandidateDigestAbsent = "absent"

// CandidateEvidence identifies durable model-authored evidence. It is not an
// accepted artifact and must be passed through its registered materializer.
type CandidateEvidence struct {
	Contract string `json:"contract"`
	Path     string `json:"path"`
	Digest   string `json:"digest"`
}

func WriteCandidate(stagingRoot, contract string, content []byte, expectedDigest string) (CandidateEvidence, error) {
	path, err := candidatePath(stagingRoot, contract)
	if err != nil {
		return CandidateEvidence{}, err
	}
	if len(content) == 0 || len(content) > MaxArtifactBytes {
		return CandidateEvidence{}, fmt.Errorf("candidate content must contain 1 through %d bytes", MaxArtifactBytes)
	}
	current, err := os.ReadFile(path)
	switch {
	case err == nil:
		currentDigest := DigestBytes(current)
		if expectedDigest != currentDigest {
			return CandidateEvidence{}, fmt.Errorf("candidate digest conflict: expected %q, current %q", expectedDigest, currentDigest)
		}
	case os.IsNotExist(err):
		if expectedDigest != CandidateDigestAbsent {
			return CandidateEvidence{}, fmt.Errorf("candidate does not exist; expectedDigest must be %q", CandidateDigestAbsent)
		}
	case err != nil:
		return CandidateEvidence{}, fmt.Errorf("read current candidate: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return CandidateEvidence{}, fmt.Errorf("create candidate directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".candidate-*.tmp")
	if err != nil {
		return CandidateEvidence{}, fmt.Errorf("create candidate temporary file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o640); err != nil {
		_ = temporary.Close()
		return CandidateEvidence{}, fmt.Errorf("set candidate permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return CandidateEvidence{}, fmt.Errorf("write candidate: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return CandidateEvidence{}, fmt.Errorf("close candidate: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return CandidateEvidence{}, fmt.Errorf("publish candidate evidence: %w", err)
	}
	return CandidateEvidence{Contract: contract, Path: path, Digest: DigestBytes(content)}, nil
}

func ReadCandidate(stagingRoot, contract string) (CandidateEvidence, []byte, error) {
	path, err := candidatePath(stagingRoot, contract)
	if err != nil {
		return CandidateEvidence{}, nil, err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return CandidateEvidence{}, nil, fmt.Errorf("read %s candidate evidence: %w", contract, err)
	}
	if len(content) == 0 || len(content) > MaxArtifactBytes {
		return CandidateEvidence{}, nil, fmt.Errorf("candidate content must contain 1 through %d bytes", MaxArtifactBytes)
	}
	return CandidateEvidence{Contract: contract, Path: path, Digest: DigestBytes(content)}, content, nil
}

func candidatePath(stagingRoot, contract string) (string, error) {
	root, err := filepath.Abs(stagingRoot)
	if err != nil {
		return "", fmt.Errorf("resolve staging root: %w", err)
	}
	name, version, ok := strings.Cut(contract, "/")
	if !ok || name == "" || version == "" || strings.Contains(version, "/") ||
		strings.ContainsAny(name+version, `\\:`) || strings.Contains(name+version, "..") {
		return "", fmt.Errorf("candidate contract %q must use safe name/version form", contract)
	}
	return filepath.Join(root, ".candidates", name+"-"+version+".json"), nil
}
