package util

import (
	"context"
	"fmt"
	"math/rand"
	"time"
)

// Retry executes the provided function with an exponential backoff.
func Retry[T any](ctx context.Context, maxRetries int, baseDelay time.Duration, operation func() (T, error)) (T, error) {
	var result T
	var err error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// 1. Execute the operation
		result, err = operation()
		if err == nil {
			return result, nil // Success!
		}

		// 2. Check if context is cancelled (don't wait if shutting down)
		if ctx.Err() != nil {
			return result, fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		}

		// 3. Calculate Exponential Backoff with Jitter
		// Formula: baseDelay * (2^attempt) + jitter
		backoff := float64(baseDelay) * float64(int(1)<<attempt)
		jitter := backoff * 0.2 * (rand.Float64()*2 - 1) // +/- 20% jitter
		sleepDuration := time.Duration(backoff + jitter)

		// 4. Wait for the backoff duration
		select {
		case <-time.After(sleepDuration):
			// Continue to next attempt
		case <-ctx.Done():
			return result, ctx.Err()
		}
	}

	return result, fmt.Errorf("operation failed after %d attempts: %w", maxRetries, err)
}
