package workspaceeditor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if len(changes.Files) != 1 || changes.EvidenceDigest == "" {
		t.Fatalf("unexpected changes: %#v", changes)
	}
	file := changes.Files[0]
	if file.Path != "calculator.go" || file.Action != "modify" || file.BaseDigest != current.Digest ||
		file.ResultDigest != updated.Digest || file.ResultContent == nil || *file.ResultContent != updated.Content {
		t.Fatalf("unexpected changed file: %#v", file)
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
	if err != nil || len(changes.Files) != 0 || changes.EvidenceDigest != "" {
		t.Fatalf("create/delete did not converge: %#v, %v", changes, err)
	}
}

func TestEditorDeletionOmitsResultContent(t *testing.T) {
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
	if len(changes.Files) != 1 {
		t.Fatalf("unexpected changes: %#v", changes)
	}
	file := changes.Files[0]
	if file.Action != "delete" || file.BaseDigest != current.Digest || file.ResultDigest != "" || file.ResultContent != nil {
		t.Fatalf("unexpected deletion: %#v", file)
	}
}

func TestEditorCreateAndDeleteProduceSortedManifest(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "old.txt"), []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	editor := Editor{BaseRoot: base, OverlayRoot: overlay}
	created, err := editor.Write("new.txt", "new\n", AbsentDigest)
	if err != nil {
		t.Fatal(err)
	}
	current, err := editor.Read("old.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := editor.Delete("old.txt", current.Digest); err != nil {
		t.Fatal(err)
	}
	changes, err := editor.Changes()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 2 || changes.Files[0].Path != "new.txt" || changes.Files[1].Path != "old.txt" {
		t.Fatalf("manifest is not sorted: %#v", changes)
	}
	if changes.Files[0].Action != "add" || changes.Files[0].BaseDigest != AbsentDigest ||
		changes.Files[0].ResultContent == nil || *changes.Files[0].ResultContent != "new\n" || changes.Files[0].ResultDigest != created.Digest {
		t.Fatalf("unexpected addition: %#v", changes.Files[0])
	}
	if changes.Files[1].Action != "delete" || changes.EvidenceDigest == "" {
		t.Fatalf("unexpected manifest: %#v", changes)
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

func TestEditorPreservesExistingLineEndingsAndUsesLFForNewFiles(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "main.go"), []byte("package main\r\n\r\nfunc main() {}\r\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	editor := Editor{BaseRoot: base, OverlayRoot: overlay}
	current, err := editor.Read("main.go", 0)
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := editor.Replace("main.go", "func main() {}\r\n", "func main() {\r\n}\r\n", current.Digest, 1)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Content != "package main\r\n\r\nfunc main() {\r\n}\r\n" {
		t.Fatalf("replacement did not preserve CRLF: %q", replaced.Content)
	}
	created, err := editor.Write("created.go", "package main\r\n", AbsentDigest)
	if err != nil {
		t.Fatal(err)
	}
	if created.Content != "package main\n" {
		t.Fatalf("write was not normalized: %q", created.Content)
	}
	changes, err := editor.Changes()
	if err != nil {
		t.Fatal(err)
	}
	if len(changes.Files) != 2 || changes.Files[0].ResultContent == nil || *changes.Files[0].ResultContent != "package main\n" ||
		changes.Files[1].ResultContent == nil || *changes.Files[1].ResultContent != replaced.Content {
		t.Fatalf("manifest did not preserve complete file content: %#v", changes)
	}
}

func bytesEqual(first, second []byte) bool { return string(first) == string(second) }
