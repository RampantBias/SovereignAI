package workspaceeditor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
)

func TestEditorWritesOverlayWithoutMutatingBase(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	original := []byte("package calculator\n\nfunc Add(a, b int) int { return a + b }\n")
	if err := os.WriteFile(filepath.Join(base, "calculator.go"), original, 0o640); err != nil {
		t.Fatal(err)
	}
	editor := Editor{BaseRoot: base, OverlayRoot: overlay}
	current, err := editor.Read("calculator.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := editor.Replace("calculator.go", "return a + b", "return a - b", current.Digest, 1)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Source != "overlay" || !strings.Contains(updated.Content, "return a - b") {
		t.Fatalf("unexpected update: %#v", updated)
	}
	baseAfter, _ := os.ReadFile(filepath.Join(base, "calculator.go"))
	if !bytesEqual(baseAfter, original) {
		t.Fatal("base checkout was mutated")
	}
	changes, err := editor.Changes()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 1 || changes.Added != 1 || changes.Deleted != 1 {
		t.Fatalf("unexpected changes: %#v", changes)
	}
	if _, _, err := artifactcontract.DerivePatchMetadata(changes.Patch); err != nil {
		t.Fatalf("invalid derived patch: %v\n%s", err, changes.Patch)
	}
}

func TestEditorRejectsTraversalAndStaleWrites(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "main.go"), []byte("package main\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	editor := Editor{BaseRoot: base, OverlayRoot: overlay}
	if _, err := editor.Write("../escape", "bad\n", AbsentDigest); err == nil {
		t.Fatal("traversal was accepted")
	}
	if _, err := editor.Write("main.go", "package changed\n", "sha256:stale"); err == nil {
		t.Fatal("stale digest was accepted")
	}
	created, err := editor.Write("new.go", "package newfile\n", AbsentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.Delete("new.go", created.Digest); err != nil {
		t.Fatal(err)
	}
	changes, err := editor.Changes()
	if err != nil || len(changes.Files) != 0 {
		t.Fatalf("create/delete did not converge: %#v, %v", changes, err)
	}
}

func TestEditorDeletionPreservesMissingNewlineMetadata(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "old.txt"), []byte("old without newline"), 0o640); err != nil {
		t.Fatal(err)
	}
	editor := Editor{BaseRoot: base, OverlayRoot: overlay}
	current, _ := editor.Read("old.txt", 0)
	if err := editor.Delete("old.txt", current.Digest); err != nil {
		t.Fatal(err)
	}
	changes, err := editor.Changes()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(changes.Patch, "+++ /dev/null\n") || !strings.Contains(changes.Patch, "\\ No newline at end of file\n") {
		t.Fatalf("unexpected deletion patch:\n%s", changes.Patch)
	}
	if _, _, err := artifactcontract.DerivePatchMetadata(changes.Patch); err != nil {
		t.Fatalf("invalid deletion patch: %v", err)
	}
}

func TestEditorTreeAndSearchUseOverlay(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "src"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "src", "main.go"), []byte("package old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	editor := Editor{BaseRoot: base, OverlayRoot: overlay}
	current, _ := editor.Read("src/main.go", 0)
	_, _ = editor.Write("src/main.go", "package current\n", current.Digest)
	_, _ = editor.Write("src/new.go", "package current\n", AbsentDigest)
	entries, err := editor.Tree("src", 10)
	if err != nil || strings.Join(entries, ",") != "src/main.go,src/new.go" {
		t.Fatalf("tree = %v, %v", entries, err)
	}
	matches, err := editor.Search("src", "package current", 10)
	if err != nil || len(matches) != 2 {
		t.Fatalf("search = %#v, %v", matches, err)
	}
}

func bytesEqual(first, second []byte) bool { return string(first) == string(second) }
