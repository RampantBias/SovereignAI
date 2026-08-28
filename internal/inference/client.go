package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolDefinition struct {
	Type     string                 `json:"type"`
	Function ToolFunctionDefinition `json:"function"`
}

type ToolFunctionDefinition struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type JSONSchema struct {
	Name   string
	Schema json.RawMessage
}

type ChatRequest struct {
	Model             string
	Messages          []Message
	MaxOutputTokens   int
	Temperature       float64
	RepetitionPenalty float64
	OutputSchema      *JSONSchema
	Tools             []ToolDefinition
}

type ChatResponse struct {
	Content          string
	FinishReason     string
	PromptTokens     int
	CompletionTokens int
	ToolCalls        []ToolCall
}

type chatCompletionPayload struct {
	Model             string           `json:"model"`
	Messages          []Message        `json:"messages"`
	MaxTokens         int              `json:"max_tokens"`
	Temperature       float64          `json:"temperature,omitempty"`
	RepetitionPenalty float64          `json:"repetition_penalty,omitempty"`
	ResponseFormat    *responseFormat  `json:"response_format,omitempty"`
	Tools             []ToolDefinition `json:"tools,omitempty"`
}

// OpenAI response format
type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content   string     `json:"content"`
			ToolCalls []ToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`

	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

type responseJSONSchema struct {
	Name   string          `json:"name"`
	Schema json.RawMessage `json:"schema"`
	Strict bool            `json:"strict"`
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
	chatEndpoint, err := validateEndpoint(inferenceURL)
	if err != nil {
		return nil, fmt.Errorf("inference url format is invalid: %w", err)
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("timeout must be greater than 0")
	}
	if maxResponseBytes <= 0 {
		return nil, fmt.Errorf("maxResponseBytes must be greater than 0")
	}
	return &Client{
		endpoint: chatEndpoint.String(),
		httpClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(request *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		maxResponseBytes: maxResponseBytes,
	}, nil
}

// Submits POST to inference endpoint with given chat request content
func (c *Client) Chat(ctx context.Context, request ChatRequest) (ChatResponse, error) {
	if request.OutputSchema == nil {
		return ChatResponse{}, fmt.Errorf("output schema is required")
	}
	if !json.Valid(request.OutputSchema.Schema) {
		return ChatResponse{}, fmt.Errorf("output schema is invalid JSON")
	}
	// Process chat request to request payload
	payload := chatCompletionPayload{
		Model:             request.Model,
		Messages:          request.Messages,
		MaxTokens:         request.MaxOutputTokens,
		Temperature:       request.Temperature,
		RepetitionPenalty: request.RepetitionPenalty,
	}
	if request.OutputSchema != nil {
		payload.ResponseFormat = &responseFormat{
			Type: "json_schema",
			JSONSchema: responseJSONSchema{
				Name:   request.OutputSchema.Name,
				Schema: request.OutputSchema.Schema,
				Strict: true,
			},
		}
	}
	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return ChatResponse{}, fmt.Errorf("failed to marshal payload to json: %w", err)
	}

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
		Content:          decoded.Choices[0].Message.Content,
		FinishReason:     decoded.Choices[0].FinishReason,
		PromptTokens:     decoded.Usage.PromptTokens,
		CompletionTokens: decoded.Usage.CompletionTokens,
	}, nil
}

func validateEndpoint(inferenceURL string) (*url.URL, error) {
	inferenceURL = strings.TrimSpace(inferenceURL)
	if len(inferenceURL) == 0 {
		return nil, fmt.Errorf("inference endpoint is empty")
	}

	endpoint, err := url.Parse(inferenceURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse inference endpoint: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("inference url must be http or https")
	}

	if len(endpoint.Hostname()) == 0 {
		return nil, fmt.Errorf("inference url requires a hostname")
	}
	if endpoint.User != nil {
		return nil, fmt.Errorf("inference endpoint must not be credentialed")
	}
	if len(endpoint.RawQuery) > 0 || len(endpoint.Fragment) > 0 {
		return nil, fmt.Errorf("inference endpoint cannot contain raw query or fragment")
	}
	if len(endpoint.Path) != 0 && endpoint.Path != "/" {
		return nil, fmt.Errorf("inference endpoint must be the base url without a path")
	}

	// OpenAI standard
	endpoint.Path = "/v1/chat/completions"
	return endpoint, nil
}
