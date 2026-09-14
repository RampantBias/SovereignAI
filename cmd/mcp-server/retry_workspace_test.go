package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/workspaceeditor"
)

func TestRepairOverlayRetainsPriorTestsButUsesOriginalBase(t *testing.T) {
	workspace := t.TempDir()
	commit := strings.Repeat("a", 40)
	clean := workspaceeditor.SourceRoot(workspace, commit)
	if err := os.MkdirAll(clean, 0o750); err != nil {
		t.Fatal(err)
	}
	original := "package main\nimport \"testing\"\nfunc TestExisting(t *testing.T) {}\n"
	broken := strings.Replace(original, "func TestExisting(t *testing.T) {", "func TestExisting(t *testing.T) {} {", 1)
	for path, content := range map[string]string{filepath.Join(clean, "main_test.go"): original, filepath.Join(workspace, "main_test.go"): broken} {
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	request, plan := "sha256:"+strings.Repeat("b", 64), "sha256:"+strings.Repeat("c", 64)
	prior := artifactcontract.TestChangeSet{Summary: "prior tests", BaseCommit: commit, ChangeRequestDigest: request, ImplementationPlanDigest: plan, Files: []artifactcontract.ChangedFile{{Path: "main_test.go", Action: "modify", BaseDigest: artifactcontract.DigestBytes([]byte(original)), ResultDigest: artifactcontract.DigestBytes([]byte(broken)), ResultContent: &broken}}}
	repository := artifactcontract.RepositoryRevision{RepositoryURL: "https://example.invalid/repo.git", RequestedRevision: "main", ResolvedCommit: commit, UtilityOperation: artifactcontract.ObjectIdentity{Namespace: "wf", Name: "init", UID: "init-uid"}}
	priorBytes, _ := json.Marshal(prior)
	repositoryBytes, _ := json.Marshal(repository)
	sources := []artifactcontract.SourceArtifact{{Contract: artifactcontract.TestChangeSetContract, Content: priorBytes}, {Contract: artifactcontract.RepositoryRevisionContract, Content: repositoryBytes}, {Contract: artifactcontract.ChangeRequestContract, Digest: request}, {Contract: artifactcontract.ImplementationPlanContract, Digest: plan}}

	for _, attempt := range []string{"w001-r001", "w001-r002"} {
		t.Run(attempt, func(t *testing.T) {
			editor := &workspaceeditor.Editor{BaseRoot: workspace, OverlayRoot: filepath.Join(workspace, "attempts", attempt)}
			if err := prepareRefinementWorkspace(editor, workspace, sources); err != nil {
				t.Fatal(err)
			}
			current, err := editor.Read("main_test.go", 0)
			if err != nil || current.Content != broken {
				t.Fatalf("prior tests not seeded: %#v %v", current, err)
			}
			changes, err := editor.Changes()
			if err != nil {
				t.Fatal(err)
			}
			if changes.Files[0].BaseDigest != prior.Files[0].BaseDigest {
				t.Fatal("manifest bound to failed candidate instead of original source")
			}
			if err := validateRefinementTestPreservation(editor, changes.Files); err == nil {
				t.Fatal("malformed retry accepted")
			}
			repaired := original + "\nfunc TestNewBehavior(t *testing.T) {}\n"
			if _, err := editor.Write("main_test.go", repaired, current.Digest); err != nil {
				t.Fatal(err)
			}
			if err := prepareRefinementWorkspace(editor, workspace, sources); err != nil {
				t.Fatal(err)
			}
			current, _ = editor.Read("main_test.go", 0)
			if current.Content != repaired {
				t.Fatal("sidecar restart overwrote repair")
			}
			changes, err = editor.Changes()
			if err != nil {
				t.Fatal(err)
			}
			if err := validateRefinementTestPreservation(editor, changes.Files); err != nil {
				t.Fatal(err)
			}
			if changes.Files[0].BaseDigest != prior.Files[0].BaseDigest {
				t.Fatal("repair changed source identity")
			}
		})
	}
	source, _ := os.ReadFile(filepath.Join(clean, "main_test.go"))
	if string(source) != original {
		t.Fatal("modified immutable source")
	}
	prior.Files[0].BaseDigest = prior.Files[0].ResultDigest
	sources[0].Content, _ = json.Marshal(prior)
	editor := &workspaceeditor.Editor{BaseRoot: workspace, OverlayRoot: filepath.Join(workspace, "bad-base")}
	if err := prepareRefinementWorkspace(editor, workspace, sources); err == nil {
		t.Fatal("accepted prior manifest with contaminated baseDigest")
	}
}
