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
		Messages:        []Message{Message{Role: "user", Content: "run something"}},
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
			if payload.Temperature != 0.2 {
				t.Errorf("temperature = %v, want 0.2", payload.Temperature)
			}
			if payload.RepetitionPenalty != 1.1 {
				t.Errorf("repetition penalty = %v, want 1.1", payload.RepetitionPenalty)
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

func TestChatSendsToolsAndDecodesToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var payload chatCompletionPayload
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Tools) != 1 || payload.Tools[0].Function.Name != "workspace_read" {
			t.Fatalf("tools = %#v", payload.Tools)
		}
		if len(payload.Messages) != 1 || payload.Messages[0].Role != "user" {
			t.Fatalf("messages = %#v", payload.Messages)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, `{"choices":[{"message":{"tool_calls":[{"id":"call-1","type":"function","function":{"name":"workspace_read","arguments":"{\"path\":\"main.go\"}"}}]},"finish_reason":"tool_calls"}]}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, time.Second, 4096)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Chat(context.Background(), ChatRequest{
		Model: "ex", Messages: []Message{{Role: "user", Content: "edit"}},
		MaxOutputTokens: 128,
		OutputSchema:    &JSONSchema{Name: "completion", Schema: json.RawMessage(`{"type":"object"}`)},
		Tools: []ToolDefinition{{Type: "function", Function: ToolFunctionDefinition{
			Name: "workspace_read", Parameters: json.RawMessage(`{"type":"object"}`),
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ToolCalls) != 1 || response.ToolCalls[0].Function.Name != "workspace_read" || response.FinishReason != "tool_calls" {
		t.Fatalf("response = %#v", response)
	}
}

func validChatRequest() ChatRequest {
	return ChatRequest{
		Model: "ex",
		Messages: []Message{
			{Role: "system", Content: "return structured json"},
			{Role: "architect", Content: "produce implementation plan"},
		},
		MaxOutputTokens:   128,
		Temperature:       0.2,
		RepetitionPenalty: 1.1,
		OutputSchema: &JSONSchema{
			Name:   "implementation-plan-v1",
			Schema: json.RawMessage(`{"type":"object"}`),
		},
	}
}

// Need tests for
/*
	- non-2xx rejection
	- ctx cancellation/timeout
	- response size check
	- server connection failure
	- incorporate trailing slash checks
	-

*/
