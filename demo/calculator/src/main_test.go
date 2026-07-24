package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGetHealthz(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := newRouter()

	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	responseRecorder := httptest.NewRecorder()

	router.ServeHTTP(responseRecorder, request)

	if responseRecorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, responseRecorder.Code)
	}

	const expectedContentType = "application/json; charset=utf-8"
	if contentType := responseRecorder.Header().Get("Content-Type"); contentType != expectedContentType {
		t.Errorf("expected Content-Type %q, got %q", expectedContentType, contentType)
	}

	var response HealthResponse
	if err := json.Unmarshal(responseRecorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if response.Status != "ok" {
		t.Errorf("expected status body %q, got %q", "ok", response.Status)
	}
}

func TestPostCalculate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := newRouter()

	// Add/Subtract/Multiple expected tests
	tests := []struct {
		name           string
		equation       Equation
		expectedResult float64
	}{
		{
			name:           "add",
			equation:       Equation{Operation: Add, Left: 9, Right: 6},
			expectedResult: 15,
		},
		{
			name:           "subtract",
			equation:       Equation{Operation: Subtract, Left: 9, Right: 6},
			expectedResult: 3,
		},
		{
			name:           "multiply",
			equation:       Equation{Operation: Multiply, Left: 9, Right: 6},
			expectedResult: 54,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectedResult := test.expectedResult
			body, err := json.Marshal(test.equation)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}

			request := httptest.NewRequest(http.MethodPost, "/api/v1/calculate", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			responseRecorder := httptest.NewRecorder()

			router.ServeHTTP(responseRecorder, request)

			if responseRecorder.Code != http.StatusOK {
				t.Fatalf("expected status %d, got %d", http.StatusOK, responseRecorder.Code)
			}

			const expectedContentType = "application/json; charset=utf-8"
			if contentType := responseRecorder.Header().Get("Content-Type"); contentType != expectedContentType {
				t.Errorf("expected Content-Type %q, got %q", expectedContentType, contentType)
			}

			var response CalculateResponse
			if err := json.NewDecoder(responseRecorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode response body: %v", err)
			}

			if response.Result != expectedResult {
				t.Errorf("expected result %f, got %f", expectedResult, response.Result)
			}
		})
	}

	// Unsupported
	tests = []struct {
		name           string
		equation       Equation
		expectedResult float64
	}{
		{
			name:           "modulo",
			equation:       Equation{Operation: "modulo", Left: 9, Right: 6},
			expectedResult: 15,
		},
		{
			name:           "divide",
			equation:       Equation{Operation: "divide", Left: 9, Right: 6},
			expectedResult: 3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body, err := json.Marshal(test.equation)
			if err != nil {
				t.Fatalf("marshal payload: %v", err)
			}

			request := httptest.NewRequest(http.MethodPost, "/api/v1/calculate", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			responseRecorder := httptest.NewRecorder()

			router.ServeHTTP(responseRecorder, request)
			var response ErrorResponse
			if err := json.NewDecoder(responseRecorder.Body).Decode(&response); err != nil {
				t.Fatalf("decode response body: %v", err)
			}
			if response.Error.Code != "unsupported_operation" {
				t.Errorf("expected error code %q, got %q", "unsupported_operation", response.Error.Code)
			}
			if responseRecorder.Code != http.StatusBadRequest {
				t.Fatalf("expected status %d, got %d", http.StatusBadRequest, responseRecorder.Code)
			}
		})
	}

	t.Run("malformed", func(t *testing.T) {
		body, err := json.Marshal([]byte(`{"operation":`))
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}

		request := httptest.NewRequest(http.MethodPost, "/api/v1/calculate", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		responseRecorder := httptest.NewRecorder()

		router.ServeHTTP(responseRecorder, request)
		var response ErrorResponse
		if err := json.NewDecoder(responseRecorder.Body).Decode(&response); err != nil {
			t.Fatalf("decode response body: %v", err)
		}
		if response.Error.Code != "invalid_request" {
			t.Errorf("expected error code %q, got %q", "invalid_request", response.Error.Code)
		}
		if responseRecorder.Code != http.StatusBadRequest {
			t.Fatalf("expected status %d, got %d", http.StatusBadRequest, responseRecorder.Code)
		}
	})
}
