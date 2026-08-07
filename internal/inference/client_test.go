package inference

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestChatRejectsMalformedJSON(t *testing.T) {
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

func TestChatSendsWellFormedRequestAndDecodesSuccessfulResponse(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			requests.Add(1)
			if request.Method != http.MethodPost {
				t.Errorf("method = %s, need POST", request.Method)
			}
			if request.URL.Path != "/v1/chat/completions" {
				t.Errorf(
					"path = %q, want /v1/chat/completions",
					request.URL.Path,
				)
			}
			if contentType := request.Header.Get("Content-Type"); contentType != "application/json" {
				t.Errorf("Content-Type is %q, want application/json", contentType)
			}

			var payload chatCompletionPayload
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Errorf("decode request failed: %v", err)
			}

			if payload.Model != "ex" {
				t.Errorf("model = %q, want ex", payload.Model)
			}
			if payload.MaxTokens != 128 {
				t.Errorf("max tokens = %d, want 128", payload.MaxTokens)
			}
			if len(payload.Messages) != 2 {
				t.Errorf("message count = %d, expected 2", len(payload.Messages))
			}
			if payload.ResponseFormat.Type != "json_schema" {
				t.Errorf("payload response formatt = %q, want json_schema", payload.ResponseFormat.Type)
			}
			if payload.ResponseFormat.JSONSchema.Name != "implementation-plan-v1" {
				t.Errorf(
					"json schema name = %v, expected implementation-plan-v1",
					payload.ResponseFormat.JSONSchema.Name)
			}

			writer.Header().Set("Content-Type", "application/json")
			_, err := io.WriteString(writer, `{
				"choices": [{
					"message": {
						"content": "{\"plan\":[]}"
					},
					"finish_reason": "stop"
				}]
			}`)
			if err != nil {
				t.Errorf("failed to write to io: %v", err)
			}
		}))
	defer server.Close()

	client, err := NewClient(server.URL, time.Second, 1024)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	response, err := client.Chat(context.Background(), validChatRequest())
	if err != nil {
		t.Fatalf("chat error: %v", err)
	}
	if response.Content != `{"plan":[]}` {
		t.Errorf("content = %q, want JSON plan", response.Content)
	}
	if response.FinishReason != "stop" {
		t.Errorf("finished reason = %q, expected stop", response.FinishReason)
	}
	if count := requests.Load(); count != 1 {
		t.Errorf("request count = %d, expected just 1", count)
	}
}

func TestChatRejectsOversizedResponse(t *testing.T) {
	const maxResponseBytes = int64(32)

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = io.WriteString(
			writer,
			strings.Repeat("x", int(maxResponseBytes)+1),
		)
	}))
	defer server.Close()

	client, err := NewClient(
		server.URL,
		time.Second,
		maxResponseBytes,
	)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = client.Chat(context.Background(), validChatRequest())
	if err == nil {
		t.Fatal("Chat accepted an oversized response")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Chat error = %q, want response-size error", err)
	}
}

func validChatRequest() ChatRequest {
	return ChatRequest{
		Model: "ex",
		Messages: []Message{
			{Role: "system", Content: "return structured json"},
			{Role: "architect", Content: "produce implementation plan"},
		},
		MaxOutputTokens: 128,
		OutputSchema: &JSONSchema{
			Name:   "implementation-plan-v1",
			Schema: json.RawMessage(`{"type":"object"}`),
		},
	}
}
