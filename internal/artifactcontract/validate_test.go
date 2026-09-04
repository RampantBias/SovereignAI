package artifactcontract

import (
	"strings"
	"testing"
)

func TestChangedFileValidation(t *testing.T) {
	base := []byte("old\n")
	result := "new\n"
	valid := ChangeSet{
		Summary: "change", BaseCommit: strings.Repeat("a", 40),
		ChangeRequestDigest:      "sha256:" + strings.Repeat("b", 64),
		ImplementationPlanDigest: "sha256:" + strings.Repeat("c", 64),
		Files: []ChangedFile{{
			Path: "main.go", Action: "modify", BaseDigest: DigestBytes(base),
			ResultDigest: DigestBytes([]byte(result)), ResultContent: &result,
		}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid changed file rejected: %v", err)
	}

	badDigest := valid
	badDigest.Files = append([]ChangedFile(nil), valid.Files...)
	badDigest.Files[0].ResultDigest = "sha256:" + strings.Repeat("0", 64)
	if err := badDigest.Validate(); err == nil || !strings.Contains(err.Error(), "resultDigest") {
		t.Fatalf("mismatched result digest returned %v", err)
	}

	deletion := valid
	deletion.Files = []ChangedFile{{Path: "main.go", Action: "delete", BaseDigest: DigestBytes(base), ResultContent: &result}}
	if err := deletion.Validate(); err == nil || !strings.Contains(err.Error(), "must omit") {
		t.Fatalf("delete with result content returned %v", err)
	}

	addition := valid
	addition.Files = []ChangedFile{{Path: "main.go", Action: "add", BaseDigest: DigestBytes(base), ResultDigest: DigestBytes([]byte(result)), ResultContent: &result}}
	if err := addition.Validate(); err == nil || !strings.Contains(err.Error(), "must be absent") {
		t.Fatalf("add with existing base returned %v", err)
	}
}
