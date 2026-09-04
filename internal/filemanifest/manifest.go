package filemanifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const AbsentDigest = "absent"

// File is the contract-neutral representation of one complete workspace
// result. Artifact contracts can embed or alias it.
type File struct {
	Path          string  `json:"path"`
	Action        string  `json:"action"`
	BaseDigest    string  `json:"baseDigest"`
	ResultDigest  string  `json:"resultDigest,omitempty"`
	ResultContent *string `json:"resultContent,omitempty"`
}

func Digest(files []File) (string, error) {
	content, err := json.Marshal(files)
	if err != nil {
		return "", fmt.Errorf("encode file manifest: %w", err)
	}
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
