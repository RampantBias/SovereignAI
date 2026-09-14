package lineagedisplay

import (
	"bytes"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/audit"
)

func TestRenderTreatsEvidenceAsData(t *testing.T) {
	const hostile = `</script><script>globalThis.injected=true</script>`
	v := audit.WorkflowView{SchemaVersion: "v1", View: "workflow", Workflow: audit.ResourceRef{UID: "wf-uid", Name: hostile}}
	var out bytes.Buffer
	if err := Render(&out, v); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), hostile) || strings.Count(out.String(), "</script>") != 1 {
		t.Fatal("evidence can escape the data string")
	}
	if !strings.Contains(out.String(), "JSON.parse(") {
		t.Fatal("missing embedded data")
	}
	if err := Render(&out, audit.WorkflowView{}); err == nil {
		t.Fatal("old server response rendered as an empty workflow")
	}
}
