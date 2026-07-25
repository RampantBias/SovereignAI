package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SovereignAI/internal/agentcontract"
	"github.com/SovereignAI/internal/artifactcontract"
)

func main() {
	mode := flag.String("mode", "success", "behavior to exercise: success, missing-artifact, invalid-result, fail")
	sleep := flag.Duration("sleep", 3*time.Second, "time to sleep before writing the result")
	flag.Parse()

	inputPath := env("SOVEREIGN_INPUT_PATH", "/control/input.json")
	resultPath := env("SOVEREIGN_RESULT_PATH", "")

	input, err := agentcontract.ReadInput(inputPath)
	if err != nil {
		log.Fatalf("read input: %v", err)
	}
	if resultPath == "" {
		resultPath = input.ResultPath
	}
	if resultPath == "" {
		resultPath = filepath.Join(input.ControlPath, "result.json")
	}

	time.Sleep(*sleep)

	switch *mode {
	case "success":
		if err := writeSuccess(input, resultPath); err != nil {
			log.Fatalf("write success result: %v", err)
		}
	case "missing-artifact":
		if err := writeMissingArtifact(input, resultPath); err != nil {
			log.Fatalf("write missing-artifact result: %v", err)
		}
	case "invalid-result":
		if err := writeInvalidResult(resultPath); err != nil {
			log.Fatalf("write invalid result: %v", err)
		}
	case "fail":
		if err := agentcontract.WriteResult(resultPath, agentcontract.Result{
			SchemaVersion: agentcontract.Version,
			Outcome:       "Failed",
			Message:       "smoke agent intentionally failed",
			Error:         &agentcontract.ResultError{Code: "SmokeAgentFailed", Message: "smoke agent intentionally failed"},
		}); err != nil {
			log.Fatalf("write failure result: %v", err)
		}
		os.Exit(1)
	default:
		log.Fatalf("unsupported smoke mode %q", *mode)
	}
}

func writeSuccess(input agentcontract.Input, resultPath string) error {
	if err := os.MkdirAll(input.StagingPath, 0o750); err != nil {
		return err
	}
	artifacts := make([]agentcontract.ArtifactOutput, 0, len(input.Outputs))
	for _, output := range input.Outputs {
		path := filepath.Join(input.StagingPath, artifactFileName(output))
		body, err := smokeArtifactContent(output.Name + "/" + output.Version)
		if err != nil {
			return err
		}
		if err := os.WriteFile(path, body, 0o640); err != nil {
			return err
		}
		artifacts = append(artifacts, agentcontract.ArtifactOutput{
			Contract:  output.Name + "/" + output.Version,
			Path:      path,
			MediaType: output.MediaType,
		})
	}
	return agentcontract.WriteResult(resultPath, agentcontract.Result{
		SchemaVersion: agentcontract.Version,
		Outcome:       "Succeeded",
		Message:       "smoke agent produced declared artifacts",
		Artifacts:     artifacts,
	})
}

func writeMissingArtifact(input agentcontract.Input, resultPath string) error {
	artifacts := make([]agentcontract.ArtifactOutput, 0, len(input.Outputs))
	for _, output := range input.Outputs {
		artifacts = append(artifacts, agentcontract.ArtifactOutput{
			Contract: output.Name + "/" + output.Version,
			Path:     filepath.Join(input.StagingPath, artifactFileName(output)),
		})
	}
	return agentcontract.WriteResult(resultPath, agentcontract.Result{
		SchemaVersion: agentcontract.Version,
		Outcome:       "Succeeded",
		Message:       "smoke agent intentionally referenced missing artifacts",
		Artifacts:     artifacts,
	})
}

func writeInvalidResult(resultPath string) error {
	if err := os.MkdirAll(filepath.Dir(resultPath), 0o750); err != nil {
		return err
	}
	return os.WriteFile(resultPath, []byte(`{"schemaVersion":"invalid","outcome":"Succeeded"}`), 0o640)
}

func artifactFileName(output agentcontract.OutputObligation) string {
	name := strings.ToLower(output.Name + "-" + output.Version)
	replacer := strings.NewReplacer("/", "-", "_", "-", " ", "-")
	return strings.Trim(replacer.Replace(name), "-") + ".json"
}

func smokeArtifactContent(contract string) ([]byte, error) {
	switch contract {
	case artifactcontract.ImplementationPlanContract:
		return json.Marshal(artifactcontract.ImplementationPlan{
			Summary:                  "Contract-validation smoke plan",
			ChangeRequestDigest:      "sha256:" + strings.Repeat("a", 64),
			RepositoryRevisionDigest: "sha256:" + strings.Repeat("b", 64),
			SourceCommit:             strings.Repeat("a", 40),
			AffectedPaths:            []artifactcontract.AffectedPath{{Path: "README.md", Action: "modify"}},
			ImplementationSteps:      []string{"Exercise typed Artifact collection"},
			TestStrategy:             []string{"Observe accepted Artifact status"},
			AcceptanceMapping:        []artifactcontract.AcceptanceMapping{{CriterionIndex: 1, Verification: "Collector and controller accept the exact bytes"}},
			Risks:                    []string{},
			Assumptions:              []string{},
		})
	default:
		return nil, fmt.Errorf("smoke agent has no fixture for registered contract %q", contract)
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
