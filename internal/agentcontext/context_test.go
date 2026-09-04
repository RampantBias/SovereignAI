package agentcontext

import (
	"strings"
	"testing"
)

func TestAssembleKeepsArtifactsBoundedAndResponsibilitySingular(t *testing.T) {
	prompt := Assemble(Context{
		Role: "test author", Responsibility: "cover every acceptance criterion",
		Capabilities:     []string{"workspace_read"},
		Artifacts:        []Artifact{{Name: "developer-output", Contract: "change-set/v1", Digest: "sha256:value", Content: "[Changed Files]\n1. Changed File"}},
		DurableActions:   []string{"Write tests through workspace tools."},
		FinalInstruction: "Complete the required output through MCP actions.",
	})
	if strings.Count(prompt, "cover every acceptance criterion") != 1 {
		t.Fatalf("responsibility should appear once:\n%s", prompt)
	}
	for _, expected := range []string{"# INPUT ARTIFACTS", "bounded artifact data", "[Changed Files]", "# DURABLE OUTPUT ACTIONS"} {
		if !strings.Contains(prompt, expected) {
			t.Errorf("prompt does not contain %q", expected)
		}
	}
}
