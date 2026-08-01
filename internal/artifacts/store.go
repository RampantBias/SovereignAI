package artifacts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SovereignAI/internal/artifactcontract"
)

type StoredContent struct {
	Digest string
	Path   string
	Bytes  []byte
}

// Put writes exact bytes into the content-addressed artifact store. Existing
// content is accepted only after it is read back and verified.
func Put(root string, content []byte) (StoredContent, error) {
	if len(content) > artifactcontract.MaxArtifactBytes {
		return StoredContent{}, fmt.Errorf("artifact content exceeds %d-byte limit", artifactcontract.MaxArtifactBytes)
	}
	digest := artifactcontract.DigestBytes(content)
	destination, err := ContentPath(root, digest)
	if err != nil {
		return StoredContent{}, err
	}
	if err := os.MkdirAll(root, 0o750); err != nil {
		return StoredContent{}, fmt.Errorf("create artifact store: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return Verify(root, digest)
	} else if !os.IsNotExist(err) {
		return StoredContent{}, fmt.Errorf("inspect stored artifact: %w", err)
	}

	temporary, err := os.CreateTemp(root, ".artifact-*")
	if err != nil {
		return StoredContent{}, fmt.Errorf("create temporary artifact: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o440); err != nil {
		_ = temporary.Close()
		return StoredContent{}, fmt.Errorf("set temporary artifact permissions: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return StoredContent{}, fmt.Errorf("write temporary artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return StoredContent{}, fmt.Errorf("close temporary artifact: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		if _, statErr := os.Lstat(destination); statErr != nil {
			return StoredContent{}, fmt.Errorf("publish stored artifact: %w", err)
		}
	}
	return Verify(root, digest)
}

// Verify reads a stored Artifact by digest and proves its type, size, and
// byte-level digest.
func Verify(root, digest string) (StoredContent, error) {
	path, err := ContentPath(root, digest)
	if err != nil {
		return StoredContent{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return StoredContent{}, fmt.Errorf("inspect stored artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return StoredContent{}, fmt.Errorf("stored artifact must be a regular file")
	}
	if info.Size() > artifactcontract.MaxArtifactBytes {
		return StoredContent{}, fmt.Errorf("stored artifact exceeds %d-byte limit", artifactcontract.MaxArtifactBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return StoredContent{}, fmt.Errorf("read stored artifact: %w", err)
	}
	actual := artifactcontract.DigestBytes(content)
	if actual != digest {
		return StoredContent{}, fmt.Errorf("stored artifact digest mismatch: expected %s, got %s", digest, actual)
	}
	return StoredContent{Digest: digest, Path: path, Bytes: content}, nil
}

func ContentPath(root, digest string) (string, error) {
	if !strings.HasPrefix(digest, "sha256:") {
		return "", fmt.Errorf("artifact digest %q must use sha256", digest)
	}
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	if len(hexDigest) != 64 {
		return "", fmt.Errorf("artifact digest %q must contain 64 hexadecimal characters", digest)
	}
	for _, character := range hexDigest {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return "", fmt.Errorf("artifact digest %q must contain lowercase hexadecimal characters", digest)
			}
		}
	}
	return filepath.Join(root, hexDigest), nil
}
