package workspaceeditor

import "path/filepath"

// SourceRoot is the trusted utility's clean checkout of the accepted commit.
func SourceRoot(workspace, commit string) string {
	return filepath.Join(workspace, ".sovereign", "source", commit)
}
