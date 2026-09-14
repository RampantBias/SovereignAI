package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/api/v1/pb"
	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/audit"
	"github.com/SovereignAI/internal/inference"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Called exactly once, after the first request's actual tools have been selected.
// A configured upload must be acknowledged before any inference request is sent.
func saveInitialContext(ctx context.Context, input agentcontract.Input, artifacts []loadedArtifact, request inference.ChatRequest) error {
	capture := input.ContextCapture
	if capture == nil {
		return nil
	} // Standalone runtimes may run without durable audit configured.
	body := audit.InitialContext{SchemaVersion: audit.ContextFormat, Request: request, RetryFeedback: input.RetryFeedback}
	for _, artifact := range artifacts {
		body.Artifacts = append(body.Artifacts, audit.ContextArtifact{Metadata: artifact.Metadata, Content: string(artifact.Content)})
	}
	content, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("serialize initial context: %w", err)
	}
	if len(content) > audit.MaxContextBytes {
		return fmt.Errorf("initial context exceeds storage size limit")
	}
	tls, err := credentials.NewClientTLSFromFile(capture.CAPath, "")
	if err != nil {
		return fmt.Errorf("load context API trust: %w", err)
	}
	token, err := os.ReadFile(capture.CredentialPath)
	if err != nil {
		return fmt.Errorf("read context upload credential: %w", err)
	}
	conn, err := grpc.NewClient(capture.Endpoint, grpc.WithTransportCredentials(tls))
	if err != nil {
		return fmt.Errorf("configure context upload: %w", err)
	}
	defer conn.Close()
	uploadCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	uploadCtx = metadata.AppendToOutgoingContext(uploadCtx, "x-sovereign-context-token", strings.TrimSpace(string(token)))
	client := pb.NewOrchestratorServiceClient(conn)
	req := &pb.StoreContextSnapshotRequest{Namespace: capture.Namespace, AgentRun: capture.AgentRun, AgentRunUid: capture.AgentRunUID, Snapshot: content}
	for attempt := 0; attempt < 3; attempt++ {
		receipt, err := client.StoreContextSnapshot(uploadCtx, req)
		if err == nil {
			expected := audit.DeterministicID("initial-context/v1", capture.AgentRunUID)
			if receipt.Id != expected || receipt.Digest != artifactcontract.DigestBytes(content) || receipt.EventId != audit.ContextEventID(expected) {
				return fmt.Errorf("context upload receipt does not match saved bytes")
			}
			return nil
		}
		if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
			return fmt.Errorf("save initial context: %w", err)
		}
		if attempt == 2 {
			return fmt.Errorf("save initial context: %w", err)
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 250 * time.Millisecond)
		select {
		case <-uploadCtx.Done():
			timer.Stop()
			return uploadCtx.Err()
		case <-timer.C:
		}
	}
	return fmt.Errorf("context upload did not complete")
}
