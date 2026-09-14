package utility

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

func TestGoTestDiagnostic(t *testing.T) {
	tests := []struct {
		name   string
		output commandOutput
		want   string
	}{
		{
			name: "compiler error after download noise",
			output: commandOutput{Stderr: strings.Repeat("go: downloading example.org/module v1.0.0\n", artifactcontract.MaxTestOutputExcerptBytes) +
				"# example.org/calculator\n./main.go:12:3: undefined: Divide\n", Stdout: "FAIL\texample.org/calculator [build failed]\nFAIL\n"},
			want: "# example.org/calculator | ./main.go:12:3: undefined: Divide | FAIL example.org/calculator [build failed]",
		},
		{
			name: "failed subtest and assertion",
			output: commandOutput{Stderr: "go: downloading example.org/module v1.0.0\n",
				Stdout: "--- FAIL: TestCalculate (0.00s)\n    --- FAIL: TestCalculate/divide (0.00s)\n        main_test.go:42: expected 2, got error: unsupported operation divide\nFAIL\nFAIL\texample.org/calculator\t0.01s\n"},
			want: "--- FAIL: TestCalculate | --- FAIL: TestCalculate/divide | main_test.go:42: expected 2, got error: unsupported operation divide | FAIL example.org/calculator 0.01s",
		},
		{
			name:   "compiler continuations",
			output: commandOutput{Stderr: "./main.go:8:12: not enough arguments in call to Divide\n\thave (int)\n\twant (int, int)\n"},
			want:   "./main.go:8:12: not enough arguments in call to Divide | have (int) | want (int, int)",
		},
		{
			name:   "multiline assertion",
			output: commandOutput{Stdout: "--- FAIL: TestDivide (0.00s)\n    main_test.go:8: mismatch:\n        expected: 2\n        actual: 3\nFAIL\n"},
			want:   "--- FAIL: TestDivide | main_test.go:8: mismatch: | expected: 2 | actual: 3",
		},
		{
			name:   "panic preserves stack context",
			output: commandOutput{Stdout: "panic: runtime error: integer divide by zero\n\ngoroutine 1 [running]:\nruntime.goexit()\n\t/usr/local/go/src/runtime/asm_amd64.s:1700\ncalculator.Divide()\n\t/workspace/main.go:9 +0x12\n"},
			want:   "panic: runtime error: integer divide by zero | goroutine 1 [running]: | runtime.goexit() | /usr/local/go/src/runtime/asm_amd64.s:1700 | calculator.Divide() | /workspace/main.go:9 +0x12",
		},
		{name: "known progress only", output: commandOutput{Stderr: "go: downloading example.org/module v1.0.0\n", Stdout: "=== RUN   TestDivide\n=== PAUSE TestDivide\n=== CONT TestDivide\n--- PASS: TestDivide (0.00s)\nPASS\nok  \texample.org/calculator\t0.01s\n?   \texample.org/other\t[no test files]\n"}},
		{
			name:   "download failure is not download progress",
			output: commandOutput{Stderr: "go: downloading example.org/module v1.0.0\ngo: example.org/module: Get \"https://proxy.golang.org/module\": dial tcp: lookup proxy.golang.org: no such host\n"},
			want:   "go: example.org/module: Get \"https://proxy.golang.org/module\": dial tcp: lookup proxy.golang.org: no such host",
		},
		{
			name:   "dependency failure with source location",
			output: commandOutput{Stderr: "main.go:4:2: example.org/module: Get \"https://proxy.golang.org/module\": dial tcp: lookup proxy.golang.org: no such host\n"},
			want:   "main.go:4:2: example.org/module: Get \"https://proxy.golang.org/module\": dial tcp: lookup proxy.golang.org: no such host",
		},
		{
			name:   "syntax error despite setup failed summary",
			output: commandOutput{Stdout: "FAIL\texample.org/calculator [setup failed]\nFAIL\n", Stderr: "# example.org/calculator\nmain_test.go:5:33: expected '}', found 'EOF'\n"},
			want:   "# example.org/calculator | main_test.go:5:33: expected '}', found 'EOF' | FAIL example.org/calculator [setup failed]",
		},
		{
			name:   "unrecognized stdout and stderr are retained",
			output: commandOutput{Stdout: "additional assertion details\nmain_test.go:8: expected 2, got 3\n", Stderr: "permission denied\n"},
			want:   "permission denied | additional assertion details | main_test.go:8: expected 2, got 3",
		},
		{
			name:   "runtime environment failure is retained",
			output: commandOutput{Stderr: "runtime: failed to create new OS thread\nfatal error: newosproc\n"},
			want:   "runtime: failed to create new OS thread | fatal error: newosproc",
		},
	}
	input := utilitycontract.Input{Command: []string{"go", "test", "./..."}, WorkspacePath: "/workspace"}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := goTestDiagnostic(input, tt.output); got != tt.want {
				t.Fatalf("diagnostic = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGoTestDiagnosticIsBoundedAndCommandSpecific(t *testing.T) {
	input := utilitycontract.Input{Command: []string{"go", "test", "./..."}}
	output := commandOutput{Stderr: "main.go:1:1: " + strings.Repeat("界", 1000)}
	got := goTestDiagnostic(input, output)
	if got == "" || len(got) > agentcontract.MaxRetryFeedbackMessageBytes || !utf8.ValidString(got) {
		t.Fatalf("diagnostic is not bounded UTF-8: %q", got)
	}
	for _, command := range [][]string{nil, {"go", "version"}, {"sh", "-c", "go test ./..."}, {"other", "test"}} {
		input.Command = command
		if got := goTestDiagnostic(input, output); got != "" {
			t.Fatalf("non-Go-test command %v received diagnostics: %q", command, got)
		}
	}
}
