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
