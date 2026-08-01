package artifacts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
)

func TestPutPreservesAndReusesExactContent(t *testing.T) {
	root := t.TempDir()
	content := []byte("{\n  \"exact\": true\n}\n")
	first, err := Put(root, content)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Put(root, content)
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != artifactcontract.DigestBytes(content) || first.Digest != second.Digest || first.Path != second.Path {
		t.Fatalf("content-addressed result is unstable: %#v %#v", first, second)
	}
	if string(first.Bytes) != string(content) || string(second.Bytes) != string(content) {
		t.Fatal("stored content did not preserve exact bytes")
	}
}

func TestPutRejectsTamperedExistingContent(t *testing.T) {
	root := t.TempDir()
	content := []byte(`{"expected":true}`)
	digest := artifactcontract.DigestBytes(content)
	path, err := ContentPath(root, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"tampered":true}`), 0o440); err != nil {
		t.Fatal(err)
	}
	if _, err := Put(root, content); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest mismatch, got %v", err)
	}
}

func TestVerifyRejectsNonRegularAndMalformedDigest(t *testing.T) {
	root := t.TempDir()
	digest := "sha256:" + strings.Repeat("a", 64)
	path, err := ContentPath(root, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(root, digest); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("expected regular-file rejection, got %v", err)
	}
	if _, err := ContentPath(root, "sha256:"+strings.Repeat("A", 64)); err == nil {
		t.Fatal("uppercase digest was accepted")
	}
	if filepath.Base(path) != strings.Repeat("a", 64) {
		t.Fatalf("unexpected content path %q", path)
	}
}
