package utility

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

var goFailedTest = regexp.MustCompile(`^--- FAIL: (\S+)`)

// goTestDiagnostic removes known progress noise from a failed go test command.
// Keep unfamiliar output: setup, dependency, and syntax failures can all carry
// useful diagnostics. Filter before applying the existing feedback size limit.
func goTestDiagnostic(input utilitycontract.Input, output commandOutput) string {
	if len(input.Command) < 2 || input.Command[1] != "test" {
		return ""
	}
	executable := filepath.Base(input.Command[0])
	if executable != "go" && executable != "go.exe" {
		return ""
	}
	var selected []string
	for _, stream := range []string{output.Stderr, output.Stdout} {
		for _, raw := range strings.Split(stream, "\n") {
			line := strings.TrimSpace(raw)
			if line == "" || goTestProgress(line) {
				continue
			}
			if match := goFailedTest.FindStringSubmatch(line); match != nil {
				line = "--- FAIL: " + match[1]
			}
			selected = append(selected, line)
		}
	}
	return agentcontract.SanitizeRetryFeedbackMessage(strings.Join(selected, " | "))
}

func goTestProgress(line string) bool {
	if line == "PASS" || line == "FAIL" {
		return true
	}
	for _, prefix := range []string{
		"go: downloading ", "=== RUN ", "=== PAUSE ", "=== CONT ", "=== NAME ",
		"--- PASS: ", "--- SKIP: ", "ok\t", "ok ", "?\t", "? ",
	} {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}
