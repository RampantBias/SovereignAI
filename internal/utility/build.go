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

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

type BuildImage struct{}

func (BuildImage) Name() string { return OperationBuildImage }

func (BuildImage) Validate(input utilitycontract.Input) error {
	if err := EnsureWorkspace(input); err != nil {
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
	if OutputContract(input) == artifactcontract.ImageDigestContract {
		for _, contract := range []string{artifactcontract.CandidateRevisionContract, artifactcontract.CandidateRemoteProofContract} {
			if _, err := RequiredInput(input, contract); err != nil {
				return err
			}
		}
		if _, err := parameter(input, "builderImageDigest"); err != nil {
			return err
		}
	}
	return nil
}

var imageDigestPattern = regexp.MustCompile(`sha256:[a-fA-F0-9]{64}`)

func (BuildImage) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	if err := verifyAdmittedRepository(ctx, input); err != nil {
		return utilitycontract.Result{}, err
	}
	commit, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	tree, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if OutputContract(input) == artifactcontract.ImageDigestContract {
		candidateRef, candidate, err := ReadInputArtifact[artifactcontract.CandidateRevision](input, artifactcontract.CandidateRevisionContract)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		_, proof, err := ReadInputArtifact[artifactcontract.CandidateRemoteProof](input, artifactcontract.CandidateRemoteProofContract)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		if err := candidate.Validate(); err != nil {
			return utilitycontract.Result{}, fmt.Errorf("invalid candidate-revision input: %w", err)
		}
		if err := proof.Validate(); err != nil {
			return utilitycontract.Result{}, fmt.Errorf("invalid candidate-remote-proof input: %w", err)
		}
		if candidate.Commit != commit || candidate.Tree != tree || proof.CandidateRevisionDigest != candidateRef.Digest ||
			proof.ObservedCommit != commit || proof.RepositoryURL != candidate.RepositoryURL {
			return utilitycontract.Result{}, fmt.Errorf("build inputs do not identify the checked-out pushed candidate")
		}
	}
	imageName, _ := parameter(input, "imageName")
	commandIdentity := sha256String(strings.Join(input.Command, "\x00") + "\x00" + imageName + "\x00" + commit + "\x00" + tree)
	markerPath := filepath.Join(input.WorkspacePath, ".git", "sovereign", "builds", sha256String(input.IdempotencyKey)+".json")
	if marker, found, markerErr := readBuildMarker(markerPath); markerErr != nil {
		return utilitycontract.Result{}, markerErr
	} else if found {
		if marker.Image != imageName || marker.Commit != commit || marker.Tree != tree || marker.CommandIdentity != commandIdentity {
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
	marker := buildMarker{Image: imageName, Digest: strings.ToLower(digest), Commit: commit, Tree: tree, CommandIdentity: commandIdentity}
	if err := writeBuildMarker(markerPath, marker); err != nil {
		return utilitycontract.Result{}, err
	}
	return buildImageResult(input, marker, output)
}

type buildMarker struct {
	Image           string `json:"image"`
	Digest          string `json:"digest"`
	Commit          string `json:"commit"`
	Tree            string `json:"tree"`
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
	if marker.Image == "" || marker.Digest == "" || marker.Commit == "" || marker.Tree == "" || marker.CommandIdentity == "" {
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
	if err := os.WriteFile(temporary, data, 0o640); err != nil {
		return fmt.Errorf("write build idempotency marker: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("publish build idempotency marker: %w", err)
	}
	return nil
}

func buildImageResult(input utilitycontract.Input, marker buildMarker, output commandOutput) (utilitycontract.Result, error) {
	metadata := map[string]string{"image": marker.Image, "digest": marker.Digest, "commit": marker.Commit, "tree": marker.Tree}
	payload := any(map[string]any{
		"operation": input.Operation, "idempotencyKey": input.IdempotencyKey,
		"image": marker.Image, "digest": marker.Digest, "commit": marker.Commit, "tree": marker.Tree,
		"stdout": output.Stdout, "stderr": output.Stderr,
	})
	if OutputContract(input) == artifactcontract.ImageDigestContract {
		candidateRef, candidate, err := ReadInputArtifact[artifactcontract.CandidateRevision](input, artifactcontract.CandidateRevisionContract)
		if err != nil {
			return utilitycontract.Result{}, err
		}
		dockerfile := OptionalParameter(input, "dockerfile", "Dockerfile")
		dockerfilePath := filepath.Join(input.WorkspacePath, filepath.Clean(dockerfile))
		relative, err := filepath.Rel(input.WorkspacePath, dockerfilePath)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return utilitycontract.Result{}, fmt.Errorf("dockerfile escapes workspace")
		}
		dockerfileContent, err := os.ReadFile(dockerfilePath)
		if err != nil {
			return utilitycontract.Result{}, fmt.Errorf("read Dockerfile: %w", err)
		}
		image := artifactcontract.ImageDigest{
			CandidateRevisionDigest: candidateRef.Digest,
			ImageRepository:         marker.Image,
			Digest:                  marker.Digest,
			CandidateCommit:         candidate.Commit,
			CandidateTree:           candidate.Tree,
			BuilderImageDigest:      input.Parameters["builderImageDigest"],
			BuildCommandDigest:      artifactcontract.DigestBytes([]byte(strings.Join(input.Command, "\x00"))),
			DockerfileDigest:        artifactcontract.DigestBytes(dockerfileContent),
			ContextTree:             marker.Tree,
		}
		if err := image.Validate(); err != nil {
			return utilitycontract.Result{}, fmt.Errorf("validate image digest: %w", err)
		}
		payload = image
	}
	artifacts, err := writeOperationArtifact(input, payload)
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
