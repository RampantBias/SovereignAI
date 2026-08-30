package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

const terminationMessagePath = "/dev/termination-log"

func writeUtilityFailureTerminationMessage(path string, result utilitycontract.Result) error {
	detail := utilitycontract.ResultError{Code: "UtilityReportedFailure", Message: result.Message}
	if result.Error != nil {
		detail = *result.Error
	}
	detail.Message = agentcontract.SanitizeRetryFeedbackMessage(detail.Message)
	if detail.Message == "" {
		detail.Message = "utility reported a failed outcome"
	}
	feedback := agentcontract.RetryFeedback{PreviousAttemptRef: "current-attempt", Code: detail.Code, Message: detail.Message}
	if err := feedback.Validate(); err != nil {
		detail.Code = "UtilityReportedFailure"
	}
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(detail); err != nil {
		return fmt.Errorf("encode utility termination message: %w", err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write utility termination message: %w", err)
	}
	return nil
}
