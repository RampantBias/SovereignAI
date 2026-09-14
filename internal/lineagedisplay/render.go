// Package lineagedisplay renders the read-only workflow projection offline.
package lineagedisplay

import (
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io"

	"github.com/SovereignAI/internal/audit"
)

// Source files stay separate for editing; the exported HTML inlines all assets.
//
//go:embed workflow.html workflow.css workflow.js explanations.js timestamps.js
var assets embed.FS

var document = template.Must(template.New("workflow.html").ParseFS(assets, "workflow.html", "workflow.css", "workflow.js", "explanations.js", "timestamps.js"))

func Render(out io.Writer, view audit.WorkflowView) error {
	if view.View != "workflow" || view.SchemaVersion != audit.PayloadSchemaVersionV1 || view.Workflow.UID == "" {
		return fmt.Errorf("server did not return a supported workflow view; update the API server before exporting HTML")
	}
	data, err := json.Marshal(view)
	if err != nil {
		return err
	}
	// html/template encodes a JS string; JSON.parse avoids executable data and
	// safely handles script terminators in user-authored reasons and metadata.
	return document.Execute(out, string(data))
}
