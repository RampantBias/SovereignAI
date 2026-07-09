package orchestration

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GenerateResourceName creates a K8s-compliant name: {task-name}-{step}-{random}
func GenerateResourceName(name string) string {
	// Sanitize input (K8s names must be lowercase, no underscores)
	base := strings.ToLower(name)
	base = strings.ReplaceAll(base, " ", "-")

	// For MVP 1, we can just use the task name + step.
	// If you want actual randomness, use a short hash or timestamp suffix.
	return fmt.Sprintf("sovereign-orchestrator-%s-%s", base, strconv.FormatInt(time.Now().Unix(), 10))
}
