package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/SovereignAI/internal/agentcontract"
)

func TestWriteArtifactPublishesFile(t *testing.T) {
	stagingPath := t.TempDir()
	content := []byte(`{"summary":"test plan"}`)
	output := agentcontract.OutputObligation{
		Name:      "implementation-plan",
		Version:   "v1",
		MediaType: "application/json",
	}

	artifact, err := writeArtifact(stagingPath, output, content)
	if err != nil {
		t.Fatalf("writeArtifact: %v", err)
	}

	wantPath := filepath.Join(stagingPath, "implementation-plan-v1.json")
	if artifact.Path != wantPath {
		t.Fatalf("artifact path = %q, want %q", artifact.Path, wantPath)
	}
	if artifact.Contract != "implementation-plan/v1" {
		t.Fatalf("artifact contract = %q, want implementation-plan/v1", artifact.Contract)
	}
	if artifact.MediaType != output.MediaType {
		t.Fatalf("artifact media type = %q, want %q", artifact.MediaType, output.MediaType)
	}

	written, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if !bytes.Equal(written, content) {
		t.Fatalf("artifact content = %q, want %q", written, content)
	}

	temporaryFiles, err := filepath.Glob(filepath.Join(stagingPath, ".implementation-plan-v1.json-*.tmp"))
	if err != nil {
		t.Fatalf("find temporary artifacts: %v", err)
	}
	if len(temporaryFiles) != 0 {
		t.Fatalf("temporary artifacts remain after publish: %v", temporaryFiles)
	}
}
