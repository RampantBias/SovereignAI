package inference

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestChatMalformedJSON(t *testing.T) {
	client, err := NewClient("http://ex.invalid", time.Second, 1024)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	chatRequest := ChatRequest{
		Model:           "ex",
		Messages:        []Message{Message{Role: "tester", Content: "run something"}},
		MaxOutputTokens: 100,
		OutputSchema:    &JSONSchema{Name: "abc", Schema: json.RawMessage(`{"contract":true`)},
	}

	_, err = client.Chat(context.Background(), chatRequest)
	if err == nil {
		t.Fatalf("failed to detect bad output schema json")
	}
	if !strings.Contains(err.Error(), "invalid JSON") {
		t.Fatalf("Chat error= %q, want invalid JSON error", err)
	}
}
