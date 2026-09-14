package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/scanner"
	"go/token"
	"strings"

	"github.com/SovereignAI/internal/workspaceeditor"
)

func unchangedWorkspaceError(file workspaceeditor.File) error {
	message := fmt.Sprintf("edit leaves %q unchanged; no repair was made. Choose an exact, unique snippet and provide different replacement text", file.Path)
	return errors.New(message + currentGoSyntaxDiagnostic(file))
}

// Include a small, numbered source window so the model can locate a parser
// diagnostic without counting lines or repeatedly rereading the entire file.
func goSyntaxExcerpt(content []byte, err error) string {
	var diagnostics scanner.ErrorList
	if !errors.As(err, &diagnostics) || len(diagnostics) == 0 {
		return ""
	}
	line := diagnostics[0].Pos.Line
	lines := strings.Split(string(content), "\n")
	if line < 1 || line > len(lines) {
		return ""
	}
	var out strings.Builder
	out.WriteString("\nCurrent source around the syntax error (line numbers are not file content):\n")
	for i := max(1, line-3); i <= min(len(lines), line+3); i++ {
		text := []rune(lines[i-1])
		if len(text) > 240 {
			text = append(text[:240], []rune("...")...)
		}
		fmt.Fprintf(&out, "%d: %s\n", i, string(text))
	}
	start, end := max(0, line-4), min(len(lines), line+3)
	anchor := strings.Join(lines[start:end], "\n")
	if len(anchor) <= 1024 && strings.Count(string(content), anchor) == 1 {
		encoded, _ := json.Marshal(anchor)
		fmt.Fprintf(&out, "Exact unique workspace_replace oldText anchor (JSON string; no line numbers): %s\nProvide newText with the syntax repaired and surrounding code preserved.", encoded)
	}
	return out.String()
}

func currentGoSyntaxDiagnostic(file workspaceeditor.File) string {
	if !strings.HasSuffix(file.Path, ".go") {
		return ""
	}
	_, err := parser.ParseFile(token.NewFileSet(), file.Path, file.Content, 0)
	if err == nil {
		return ""
	}
	return fmt.Sprintf(". Current syntax error: %v%s", err, goSyntaxExcerpt([]byte(file.Content), err))
}
