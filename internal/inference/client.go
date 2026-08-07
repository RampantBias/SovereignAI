package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type JSONSchema struct {
	Name   string
	Schema json.RawMessage
}

type ChatRequest struct {
	Model           string
	Messages        []Message
	MaxOutputTokens int
	OutputSchema    *JSONSchema
}

type ChatResponse struct {
	Content      string
	FinishReason string
}

type chatCompletionPayload struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	MaxTokens      int             `json:"max_tokens"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

// OpenAI response format
type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

type responseJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
}

type responseFormat struct {
	Type       string             `json:"type"`
	JSONSchema responseJSONSchema `json:"json_schema"`
}

type Client struct {
	endpoint         string
	httpClient       *http.Client
	maxResponseBytes int64
}

func NewClient(
	inferenceURL string,
	timeout time.Duration,
	maxResponseBytes int64,
) (*Client, error) {
	// validate endpoint, timeout, maxresponsebytes

	return &Client{
		endpoint:         inferenceURL,
		httpClient:       &http.Client{Timeout: timeout},
		maxResponseBytes: maxResponseBytes,
	}, nil
}

// Submits POST to inference endpoint with given chat request content
func (c *Client) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	// Process chat request to request payload
	payload := chatCompletionPayload{
		Model:     request.Model,
		Messages:  request.Messages,
		MaxTokens: request.MaxOutputTokens,
	}
	if request.OutputSchema != nil {
		payload.ResponseFormat = &responseFormat{
			Type: "json_schema",
			JSONSchema: responseJSONSchema{
				Name:   request.OutputSchema.Name,
				Schema: request.OutputSchema.Schema,
			},
		}
	}
	jsonBytes, err := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(jsonBytes))
	if err != nil {
		return ChatResponse{}, fmt.Errorf("error creating inference request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("inference request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(
		io.LimitReader(resp.Body, c.maxResponseBytes+1), // Read 1 extra, if filled we are past max
	)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("failed to parse inference response body: %w", err)
	}

	if int64(len(body)) > c.maxResponseBytes {
		return ChatResponse{}, fmt.Errorf("inference response exceeds max bytes allowed %d", c.maxResponseBytes)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResponse{}, fmt.Errorf("inference request returned unexpected response %s: %s", resp.Status, string(body))
	}

	var decoded chatCompletionResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ChatResponse{}, fmt.Errorf("failed to decode inference response: %w", err)
	}
	if len(decoded.Choices) == 0 {
		return ChatResponse{}, fmt.Errorf("inference response contains no choices")
	}
	if len(decoded.Choices[0].Message.Content) == 0 {
		return ChatResponse{}, fmt.Errorf("inference response has no content")
	}
	return ChatResponse{
		Content:      decoded.Choices[0].Message.Content,
		FinishReason: decoded.Choices[0].FinishReason,
	}, nil

}
