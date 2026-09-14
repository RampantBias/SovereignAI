package contextrepo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryRejectsTraversalAndBoundsReads(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\nfunc main() {}"), 0o640); err != nil {
		t.Fatal(err)
	}
	repository := Repository{Root: root, BundleDir: filepath.Join(root, ".bundles"), Revision: "abc"}
	if _, err := repository.Read("../secret", 100); err == nil {
		t.Fatal("path traversal was accepted")
	}
	if _, err := repository.Read("main.go", 2); err == nil {
		t.Fatal("oversized file was accepted")
	}
	matches, err := repository.Search(".", "func main", 10)
	if err != nil || len(matches) != 1 {
		t.Fatalf("search = %#v, %v", matches, err)
	}
	bundle, err := repository.CreateBundle([]string{"main.go"})
	if err != nil || bundle.ID == "" || len(bundle.Files) != 1 {
		t.Fatalf("bundle = %#v, %v", bundle, err)
	}
	second, err := repository.CreateBundle([]string{"main.go"})
	if err != nil || second.ID != bundle.ID {
		t.Fatalf("bundle creation is not idempotent: %#v, %v", second, err)
	}
}

func TestSnapshotIsBoundedDeterministicSourceContext(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"z.go": "package z\n", "a.go": "package a\n",
		".git/config": "ignored", "node_modules/module.js": "ignored", "attempts/result.json": "ignored",
	} {
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	repository := Repository{Root: root, Revision: "abc"}
	snapshot, err := repository.Snapshot(SnapshotLimits{MaxFiles: 2, MaxFileBytes: 100, MaxTotalBytes: 100})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snapshot.Revision != "abc" || len(snapshot.Files) != 2 || snapshot.Files[0].Path != "a.go" || snapshot.Files[1].Path != "z.go" {
		t.Fatalf("unexpected snapshot: %#v", snapshot)
	}
	if snapshot.Files[0].Digest != "sha256:"+mustDigest(t, filepath.Join(root, "a.go")) {
		t.Fatalf("unexpected digest: %s", snapshot.Files[0].Digest)
	}
	if _, err := repository.Snapshot(SnapshotLimits{MaxFiles: 1, MaxFileBytes: 100, MaxTotalBytes: 100}); err == nil {
		t.Fatal("file limit was not enforced")
	}
}

func mustDigest(t *testing.T, path string) string {
	t.Helper()
	digest, err := fileDigest(path)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}
