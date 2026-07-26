package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/artifacts"
)

func main() {
	source := flag.String("source", "", "mounted bootstrap input file")
	store := flag.String("artifact-store", "", "content-addressed artifact directory")
	contract := flag.String("contract", "", "expected name/version artifact contract")
	expectedDigest := flag.String("expected-digest", "", "digest admitted by the API")
	flag.Parse()
	if err := bootstrap(*source, *store, *contract, *expectedDigest); err != nil {
		log.Fatal(err)
	}
}

func bootstrap(source, store, contract, expectedDigest string) error {
	if source == "" || store == "" || contract == "" || expectedDigest == "" {
		return fmt.Errorf("source, artifact-store, contract, and expected-digest are required")
	}
	// ConfigMap projected-volume keys are symlinks into Kubernetes' atomic
	// writer directory, so follow that link while still requiring its target
	// to be a regular file.
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("inspect bootstrap input: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("bootstrap input must be a regular file")
	}
	content, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read bootstrap input: %w", err)
	}
	if err := artifactcontract.ValidateContract(contract, content); err != nil {
		return fmt.Errorf("validate bootstrap input: %w", err)
	}
	actualDigest := artifactcontract.DigestBytes(content)
	if actualDigest != expectedDigest {
		return fmt.Errorf("bootstrap digest mismatch: expected %s, got %s", expectedDigest, actualDigest)
	}
	stored, err := artifacts.Put(store, content)
	if err != nil {
		return fmt.Errorf("store bootstrap input: %w", err)
	}
	if stored.Digest != expectedDigest {
		return fmt.Errorf("stored bootstrap digest mismatch: expected %s, got %s", expectedDigest, stored.Digest)
	}
	if err := artifactcontract.ValidateContract(contract, stored.Bytes); err != nil {
		return fmt.Errorf("validate stored bootstrap input: %w", err)
	}
	return nil
}
