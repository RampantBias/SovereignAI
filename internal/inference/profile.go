package inference

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Snapshot inference profile
type Profile struct {
	RuntimeImage      string
	ModelID           string
	ModelRevision     string
	ServedModelName   string
	CachePVCName      string
	CachePath         string
	GPUNodeLabelKey   string
	GPUNodeLabelValue string
	StartupTimeout    time.Duration
	RequestTimeout    time.Duration
	MaxOutputTokens   int
	MaxResponseBytes  int64
}

func (p Profile) Validate() error {
	image, digest, found := strings.Cut(p.RuntimeImage, "@sha256:")
	if !found || image == "" || !validHex(digest, 64) {
		return fmt.Errorf("runtime image must use a valid sha256 digest: %q", p.RuntimeImage)
	}
	if !validHex(p.ModelRevision, 40) {
		return fmt.Errorf("invalid model revision, must be 40-character commit: %q", p.ModelRevision)
	}
	if len(strings.TrimSpace(p.ModelID)) == 0 {
		return fmt.Errorf("model id must not be empty or blank")
	}
	if len(strings.TrimSpace(p.GPUNodeLabelKey)) == 0 || len(strings.TrimSpace(p.GPUNodeLabelValue)) == 0 {
		return fmt.Errorf("gpu node label key selector and label value must not be empty or blank")
	}
	if len(strings.TrimSpace(p.CachePVCName)) == 0 {
		return fmt.Errorf("cache pvc name must not be empty or blank")
	}
	if !strings.HasPrefix(p.CachePath, "/") {
		return fmt.Errorf("cache path must be absolute: %q", p.CachePath)
	}

	if p.RequestTimeout <= 0 {
		return fmt.Errorf("request timeout must be > 0")
	}
	if p.StartupTimeout <= 0 {
		return fmt.Errorf("startup timeout must be > 0")
	}
	if p.MaxOutputTokens <= 0 {
		return fmt.Errorf("max output tokens must be > 0")
	}
	if p.MaxResponseBytes <= 0 {
		return fmt.Errorf("max response bytes must be > 0")
	}
	return nil
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
