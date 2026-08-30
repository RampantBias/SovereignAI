package utility

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

func TestTestRunReturnsCodeDiagnosticAndPreservesFailedReport(t *testing.T) {
	remote, base := seedBareRepository(t)
	for _, tt := range []struct{ name, source, want string }{
		{"assertion", "package calculator\nimport \"testing\"\nfunc TestDivide(t *testing.T) { t.Fatal(\"expected 2, got 3\") }\n", "expected 2, got 3"},
		{"compiler", "package calculator\nimport \"testing\"\nfunc TestDivide(t *testing.T) { Divide() }\n", "undefined: Divide"},
		{"truncated test", "package calculator\nimport \"testing\"\nfunc TestDivide(t *testing.T) {\n", "expected '}', found 'EOF'"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			gitTestCommand(t, workspace, "clone", remote, ".")
			gitTestCommand(t, workspace, "checkout", "-b", "sovereign/diagnostics", base)
			for path, content := range map[string]string{"go.mod": "module example.invalid/calculator\n\ngo 1.26.2\n", "main_test.go": tt.source} {
				if err := os.WriteFile(filepath.Join(workspace, path), []byte(content), 0o640); err != nil {
					t.Fatal(err)
				}
			}
			gitTestCommand(t, workspace, "add", "go.mod", "main_test.go")
			digest := artifactcontract.DigestBytes([]byte("fixture"))
			prepared := artifactcontract.PreparedCandidate{
				RepositoryRevisionDigest: digest, TestChangeSetDigest: digest, ChangeSetDigest: digest, PreparationCommandDigest: digest,
				BaseCommit: base, Branch: "sovereign/diagnostics", CandidateTree: gitTestOutput(t, workspace, "write-tree"), ChangedPaths: []string{"go.mod", "main_test.go"},
			}
			input := utilityInput(t, workspace, "test-candidate", OperationTestRun, map[string]string{"environmentImageDigest": digest}, "test-report")
			input.Command = []string{"go", "test", "./..."}
			addUtilityInputArtifact(t, &input, "prepared-candidate", artifactcontract.PreparedCandidateContract, prepared)
			result := runUtility(t, input)
			if result.Outcome != "Failed" || result.Error == nil || result.Error.Code != utilitycontract.TestRunCodeError ||
				!strings.Contains(result.Error.Message, tt.want) || !strings.Contains(result.Error.Message, "main_test.go:") || strings.Contains(result.Error.Message, "exit status") {
				t.Fatalf("test.run did not return the actual Go diagnostic: %#v", result)
			}
			report := decodeArtifact[artifactcontract.TestReport](t, result, artifactcontract.TestReportContract)
			if report.Outcome != "failed" || report.ExitCode == nil || *report.ExitCode == 0 || report.CandidateTree != prepared.CandidateTree {
				t.Fatalf("failed test evidence was changed: %#v", report)
			}
		})
	}
}
