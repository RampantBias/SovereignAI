package utility

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/SovereignAI/internal/utilitycontract"
)

type BuildImage struct{}

func (BuildImage) Name() string {
	return OperationBuildImage
}

func (BuildImage) Validate(input utilitycontract.Input) error {
	// Workspace must exist/be accessible
	if err := ensureWorkspace(input); err != nil {
		return err
	}
	if _, err := parameter(input, "imageName"); err != nil {
		return err
	}
	if _, err := parameter(input, "repositoryURL"); err != nil {
		return err
	}
	if len(input.Command) == 0 {
		return fmt.Errorf("build.image requires a Project-owned build command")
	}
	return nil
}

var imageDigestPattern = regexp.MustCompile(`sha256:[a-fA-F0-9]{64}`)

func (BuildImage) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	// Need to ensure repository is accessible & matches
	if err := verifyAdmittedRepository(ctx, input); err != nil {
		return utilitycontract.Result{}, err
	}
	commit, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	imageName, _ := parameter(input, "imageName")
	// A build idempotency key may only be reused for the same Project-owned
	// command, image target, and source commit. The hash provides a stable,
	// compact comparison without copying the raw command into result metadata.
	commandIdentity := sha256String(strings.Join(input.Command, "\x00") + "\x00" + imageName + "\x00" + commit)
	// The marker lives on the shared workflow workspace so reconciliation can
	// recover the accepted digest after the utility Job itself has disappeared.
	markerPath := filepath.Join(input.WorkspacePath, ".sovereign", "builds", sha256String(input.IdempotencyKey)+".json")
	if marker, found, markerErr := readBuildMarker(markerPath); markerErr != nil {
		return utilitycontract.Result{}, markerErr
	} else if found {
		if marker.Image != imageName || marker.Commit != commit || marker.CommandIdentity != commandIdentity {
			return utilitycontract.Result{}, fmt.Errorf("idempotency key %q already belongs to different build inputs", input.IdempotencyKey)
		}
		return buildImageResult(input, marker, commandOutput{})
	}
	output, err := runTemplateCommand(ctx, input)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	digest := imageDigestPattern.FindString(output.Stdout + "\n" + output.Stderr)
	if digest == "" {
		// Some build tools write their resulting digest to a file instead of
		// stdout. The file is allowed only when it resolves inside the workspace.
		if digestFile := strings.TrimSpace(input.Parameters["digestFile"]); digestFile != "" {
			candidate := filepath.Join(input.WorkspacePath, filepath.Clean(digestFile))
			relative, relErr := filepath.Rel(input.WorkspacePath, candidate)
			if relErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return utilitycontract.Result{}, fmt.Errorf("digestFile escapes workspace")
			}
			data, readErr := os.ReadFile(candidate)
			if readErr != nil {
				return utilitycontract.Result{}, fmt.Errorf("read build digest: %w", readErr)
			}
			digest = imageDigestPattern.FindString(string(data))
		}
	}
	if digest == "" {
		return utilitycontract.Result{}, fmt.Errorf("build command did not report an immutable sha256 image digest")
	}
	marker := buildMarker{Image: imageName, Digest: strings.ToLower(digest), Commit: commit, CommandIdentity: commandIdentity}
	if err := writeBuildMarker(markerPath, marker); err != nil {
		return utilitycontract.Result{}, err
	}
	return buildImageResult(input, marker, output)
}

type buildMarker struct {
	Image           string `json:"image"`
	Digest          string `json:"digest"`
	Commit          string `json:"commit"`
	CommandIdentity string `json:"commandIdentity"`
}

func readBuildMarker(path string) (buildMarker, bool, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return buildMarker{}, false, nil
	}
	if err != nil {
		return buildMarker{}, false, fmt.Errorf("read build idempotency marker: %w", err)
	}
	var marker buildMarker
	if err := json.Unmarshal(data, &marker); err != nil {
		return buildMarker{}, false, fmt.Errorf("decode build idempotency marker: %w", err)
	}
	if marker.Image == "" || marker.Digest == "" || marker.Commit == "" || marker.CommandIdentity == "" {
		return buildMarker{}, false, fmt.Errorf("build idempotency marker is incomplete")
	}
	return marker, true, nil
}

func writeBuildMarker(path string, marker buildMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("encode build idempotency marker: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create build marker directory: %w", err)
	}
	temporary := path + ".tmp"
	// Publish through rename so a restarted runner observes either no marker or
	// one complete marker, never a partially written idempotency record.
	if err := os.WriteFile(temporary, data, 0o640); err != nil {
		return fmt.Errorf("write build idempotency marker: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish build idempotency marker: %w", err)
	}
	return nil
}

func buildImageResult(input utilitycontract.Input, marker buildMarker, output commandOutput) (utilitycontract.Result, error) {
	metadata := map[string]string{
		"image": marker.Image, "digest": marker.Digest, "commit": marker.Commit,
	}
	artifacts, err := writeOperationArtifact(input, map[string]any{
		"operation":      input.Operation,
		"idempotencyKey": input.IdempotencyKey,
		"image":          marker.Image,
		"digest":         marker.Digest,
		"commit":         marker.Commit,
		"stdout":         output.Stdout,
		"stderr":         output.Stderr,
	})
	if err != nil {
		return utilitycontract.Result{}, err
	}
	return utilitycontract.Result{
		SchemaVersion: utilitycontract.Version,
		Outcome:       "Succeeded",
		Message:       "build digest generated",
		Artifacts:     artifacts,
		Metadata:      metadata,
	}, nil
}

func sha256String(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
