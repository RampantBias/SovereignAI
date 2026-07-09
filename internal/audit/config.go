package audit

import (
	"context"
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	DSN      string
	Required bool
}

func ConfigFromEnv() Config {
	return Config{
		DSN:      os.Getenv("SOVEREIGN_AUDIT_DSN"),
		Required: envBool("SOVEREIGN_AUDIT_REQUIRED", false),
	}
}

func OpenConfigured(ctx context.Context, config Config) (Recorder, func(), string, error) {
	if config.DSN == "" {
		if config.Required {
			return nil, nil, "", fmt.Errorf("SOVEREIGN_AUDIT_REQUIRED=true requires SOVEREIGN_AUDIT_DSN")
		}
		return NewMemoryRecorder(), func() {}, "memory", nil
	}

	postgres, err := OpenPostgres(ctx, config.DSN)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open PostgreSQL audit recorder: %w", err)
	}
	return postgres, postgres.Close, "postgres", nil
}

func envBool(name string, fallback bool) bool {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}
