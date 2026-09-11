package validationrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/SovereignAI/internal/artifactcontract"
)

func (r *ValidationRunReconciler) checkCalculatorEndpoints(ctx context.Context, baseURL string) ([]artifactcontract.EndpointCheck, error) {
	httpClient := r.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	// Never follow a preview response to an unrelated endpoint.
	bounded := *httpClient
	bounded.Timeout = 5 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	probes := []struct {
		name, operation   string
		left, right, want float64
		status            int
	}{
		{name: "healthz", status: 200},
		{"add", "add", 8, 2, 10, 200}, {"subtract", "subtract", 8, 2, 6, 200},
		{"multiply", "multiply", 8, 2, 16, 200}, {"divide", "divide", 84, 2, 42, 200},
		{"division-by-zero", "divide", 8, 0, 0, 422},
	}
	checks := make([]artifactcontract.EndpointCheck, 0, len(probes))
	for _, probe := range probes {
		method, path := http.MethodGet, "/healthz"
		var body []byte
		if probe.operation != "" {
			method, path = http.MethodPost, "/api/v1/calculate"
			body, _ = json.Marshal(map[string]any{"operation": probe.operation, "left": probe.left, "right": probe.right})
		}
		req, err := http.NewRequestWithContext(ctx, method, baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		response, err := bounded.Do(req)
		if err != nil {
			return nil, fmt.Errorf("validation endpoint %s: %w", probe.name, err)
		}
		content, readErr := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
		response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(content) > 1024*1024 {
			return nil, fmt.Errorf("validation endpoint %s response exceeds limit", probe.name)
		}
		passed := response.StatusCode == probe.status
		switch probe.name {
		case "healthz":
			var value struct {
				Status string `json:"status"`
			}
			passed = passed && json.Unmarshal(content, &value) == nil && value.Status == "ok"
		case "division-by-zero":
			var value struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			passed = passed && json.Unmarshal(content, &value) == nil && value.Error.Code == "division_by_zero"
		default:
			var value struct {
				Result *float64 `json:"result"`
			}
			passed = passed && json.Unmarshal(content, &value) == nil && value.Result != nil && *value.Result == probe.want
		}
		outcome := "failed"
		if passed {
			outcome = "passed"
		}
		checks = append(checks, artifactcontract.EndpointCheck{Name: probe.name, Outcome: outcome, HTTPStatus: response.StatusCode, ResponseDigest: artifactcontract.DigestBytes(content)})
	}
	return checks, nil
}
