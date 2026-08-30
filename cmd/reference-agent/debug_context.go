package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/inference"
)

// TEMPORARY: capture context for every reference-agent role so failing attempts
// can be compared. Remove this file and the two call sites when debugging ends.
// The dump contains unredacted input data; never publish it as an artifact.
func writeDebugContext(input agentcontract.Input, artifacts []loadedArtifact, round int, request inference.ChatRequest) {
	path := filepath.Join(input.StagingPath, "debug-context.json")
	if err := saveDebugContext(path, input, artifacts, round, request); err != nil {
		log.Printf("warning: write temporary agent context dump %s: %v", path, err)
	} else if round == 0 {
		log.Printf("temporary agent context dump: %s (updated before each inference request; contains unredacted input data)", path)
	}
}

func saveDebugContext(path string, input agentcontract.Input, artifacts []loadedArtifact, round int, request inference.ChatRequest) error {
	type debugArtifact struct {
		Metadata agentcontract.ArtifactInput `json:"metadata"`
		Content  string                      `json:"content"`
	}
	snapshot := struct {
		Input     agentcontract.Input   `json:"input"`
		Artifacts []debugArtifact       `json:"loadedArtifacts"`
		Round     int                   `json:"inferenceRound"`
		Request   inference.ChatRequest `json:"inferenceRequest"`
	}{Input: input, Round: round, Request: request}
	for _, artifact := range artifacts {
		// Use text, not []byte (which encoding/json would encode as base64).
		// Keep raw artifacts alongside their possibly projected prompt contents.
		snapshot.Artifacts = append(snapshot.Artifacts, debugArtifact{
			Metadata: artifact.Metadata, Content: string(artifact.Content),
		})
	}
	if input.StagingPath == "" {
		return fmt.Errorf("staging path is empty")
	}
	if err := os.MkdirAll(input.StagingPath, 0o750); err != nil {
		return err
	}
	// Replace atomically so a failed write leaves the previous round readable.
	// CreateTemp uses 0600: the context may contain private repository content.
	file, err := os.CreateTemp(input.StagingPath, ".debug-context-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(snapshot); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
