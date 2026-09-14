package utility

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/utilitycontract"
)

func TestBuildImageDigestPrefersBuildKitMetadata(t *testing.T) {
	workspace := t.TempDir()
	want := "sha256:" + strings.Repeat("b", 64)
	writeBuildKitMetadata(t, workspace, "image-metadata.json", want)
	input := utilitycontract.Input{
		WorkspacePath: workspace,
		Parameters:    map[string]string{"digestFile": "image-metadata.json"},
	}
	output := commandOutput{
		Stdout: "resolved base sha256:" + strings.Repeat("a", 64),
		Stderr: "exported config sha256:" + strings.Repeat("c", 64),
	}

	got, err := buildImageDigest(input, output)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("build digest = %q, want metadata manifest digest %q", got, want)
	}
}

func TestBuildImageDigestRejectsInvalidBuildKitMetadata(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "malformed JSON", content: "{"},
		{name: "missing manifest digest", content: `{}`},
		{name: "invalid manifest digest", content: `{"containerimage.digest":"sha256:short"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			if err := os.WriteFile(filepath.Join(workspace, "image-metadata.json"), []byte(test.content), 0o640); err != nil {
				t.Fatal(err)
			}
			input := utilitycontract.Input{
				WorkspacePath: workspace,
				Parameters:    map[string]string{"digestFile": "image-metadata.json"},
			}
			output := commandOutput{Stdout: "sha256:" + strings.Repeat("a", 64)}
			if _, err := buildImageDigest(input, output); err == nil {
				t.Fatal("invalid BuildKit metadata was accepted through the log fallback")
			}
		})
	}
}

func TestBuildImageDigestUsesLogsWithoutDigestFile(t *testing.T) {
	want := "sha256:" + strings.Repeat("d", 64)
	got, err := buildImageDigest(utilitycontract.Input{}, commandOutput{Stdout: "pushed " + want})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("legacy build digest = %q, want %q", got, want)
	}
}
