package artifactcontract

import (
	"strings"
	"testing"
)

func TestDerivePatchMetadataValidatesHunkLineCounts(t *testing.T) {
	valid := strings.Join([]string{
		"diff --git a/main_test.go b/main_test.go",
		"--- a/main_test.go",
		"+++ b/main_test.go",
		"@@ -1,2 +1,3 @@",
		" context",
		"-old",
		"+new",
		"+extra",
	}, "\n") + "\n"
	files, counts, err := DerivePatchMetadata(valid)
	if err != nil {
		t.Fatalf("valid patch rejected: %v", err)
	}
	if len(files) != 1 || files[0] != "main_test.go" || counts.Added != 2 || counts.Deleted != 1 {
		t.Fatalf("unexpected metadata: files=%v counts=%#v", files, counts)
	}

	for name, patch := range map[string]string{
		"too few lines":    strings.Replace(valid, "@@ -1,2 +1,3 @@", "@@ -1,3 +1,4 @@", 1),
		"too many lines":   strings.Replace(valid, "@@ -1,2 +1,3 @@", "@@ -1 +1 @@", 1),
		"malformed header": strings.Replace(valid, "@@ -1,2 +1,3 @@", "@@ old new @@", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DerivePatchMetadata(patch); err == nil || !strings.Contains(err.Error(), "patch line") {
				t.Fatalf("corrupt patch was not rejected with a line diagnostic: %v", err)
			}
		})
	}
}

func TestDerivePatchMetadataAcceptsNewFileAndNoNewlineMarker(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/main_test.go b/main_test.go",
		"new file mode 100644",
		"--- /dev/null",
		"+++ b/main_test.go",
		"@@ -0,0 +1,2 @@",
		"+package main",
		"+// test",
		`\ No newline at end of file`,
	}, "\n") + "\n"
	if _, _, err := DerivePatchMetadata(patch); err != nil {
		t.Fatalf("valid new-file patch rejected: %v", err)
	}
}

func TestDerivePatchMetadataAcceptsCRLFOnlyInHunkContent(t *testing.T) {
	patch := strings.Join([]string{
		"diff --git a/main.go b/main.go",
		"--- a/main.go",
		"+++ b/main.go",
		"@@ -1 +1 @@",
		"-package old\r",
		"+package new\r",
	}, "\n") + "\n"
	files, counts, err := DerivePatchMetadata(patch)
	if err != nil {
		t.Fatalf("valid CRLF hunk content rejected: %v", err)
	}
	if len(files) != 1 || files[0] != "main.go" || counts.Added != 1 || counts.Deleted != 1 {
		t.Fatalf("unexpected metadata: files=%v counts=%#v", files, counts)
	}

	for name, invalid := range map[string]string{
		"CRLF metadata": strings.Replace(patch, "diff --git a/main.go b/main.go\n", "diff --git a/main.go b/main.go\r\n", 1),
		"bare CR":       strings.Replace(patch, "-package old\r\n", "-package old\roops\n", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := DerivePatchMetadata(invalid); err == nil {
				t.Fatal("invalid carriage return placement was accepted")
			}
		})
	}
}
