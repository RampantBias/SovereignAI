package audit

import (
	"context"
	"testing"
)

func TestOpenConfiguredFallsBackToMemoryWhenAuditIsNotRequired(t *testing.T) {
	recorder, cleanup, mode, err := OpenConfigured(context.Background(), Config{})
	if err != nil {
		t.Fatalf("OpenConfigured() error = %v", err)
	}
	defer cleanup()
	if mode != "memory" {
		t.Fatalf("mode = %q, want memory", mode)
	}
	if _, ok := recorder.(*MemoryRecorder); !ok {
		t.Fatalf("recorder = %T, want *MemoryRecorder", recorder)
	}
}

func TestOpenConfiguredRequiresDSNWhenAuditIsRequired(t *testing.T) {
	recorder, cleanup, mode, err := OpenConfigured(context.Background(), Config{Required: true})
	if err == nil {
		t.Fatal("OpenConfigured() error = nil, want error")
	}
	if recorder != nil || cleanup != nil || mode != "" {
		t.Fatalf("recorder=%T cleanupNil=%t mode=%q, want zero values", recorder, cleanup == nil, mode)
	}
}

func TestConfigFromEnvReadsAuditRequired(t *testing.T) {
	t.Setenv("SOVEREIGN_AUDIT_REQUIRED", "true")
	t.Setenv("SOVEREIGN_AUDIT_DSN", "postgres://example")
	config := ConfigFromEnv()
	if !config.Required {
		t.Fatal("Required = false, want true")
	}
	if config.DSN != "postgres://example" {
		t.Fatalf("DSN = %q, want postgres://example", config.DSN)
	}
}
