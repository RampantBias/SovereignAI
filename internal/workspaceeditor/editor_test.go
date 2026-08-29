package workspaceeditor

import (
	"os"
	"os/exec"
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
	if !strings.Contains(changes.Patch, "deleted file mode 100644\n") || !strings.Contains(changes.Patch, "+++ /dev/null\n") || !strings.Contains(changes.Patch, "\\ No newline at end of file\n") {
		t.Fatalf("unexpected deletion patch:\n%s", changes.Patch)
	}
	if _, _, err := artifactcontract.DerivePatchMetadata(changes.Patch); err != nil {
		t.Fatalf("invalid deletion patch: %v", err)
	}
}

func TestEditorCreateAndDeletePatchesPassGitApplyCheck(t *testing.T) {
	base, overlay := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "old.txt"), []byte("old\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	runGit(t, base, "init", "--quiet")
	runGit(t, base, "add", "old.txt")

	editor := Editor{BaseRoot: base, OverlayRoot: overlay}
	if _, err := editor.Write("new.txt", "new\n", AbsentDigest); err != nil {
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
	if !strings.Contains(changes.Patch, "new file mode 100644\n") || !strings.Contains(changes.Patch, "deleted file mode 100644\n") {
		t.Fatalf("patch is missing create/delete mode metadata:\n%s", changes.Patch)
	}
	command := exec.Command("git", "-C", base, "apply", "--check", "--index", "-")
	command.Stdin = strings.NewReader(changes.Patch)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated patch failed git apply --check --index: %v: %s\n%s", err, output, changes.Patch)
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
	if changes.Added != 3 || changes.Deleted != 1 {
		t.Fatalf("unexpected line counts: %#v\n%s", changes, changes.Patch)
	}
	if !strings.Contains(changes.Patch, "-func main() {}\r\n") || !strings.Contains(changes.Patch, "+func main() {\r\n") {
		t.Fatalf("patch did not preserve CRLF hunk content:\n%q", changes.Patch)
	}
	if _, _, err := artifactcontract.DerivePatchMetadata(changes.Patch); err != nil {
		t.Fatalf("CRLF hunk patch was rejected: %v\n%q", err, changes.Patch)
	}
}

func bytesEqual(first, second []byte) bool { return string(first) == string(second) }

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v: %s", strings.Join(arguments, " "), err, output)
	}
}
