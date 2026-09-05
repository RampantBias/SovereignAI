package artifactcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"unicode/utf8"
)

type AcceptanceCriterionV1 struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Digest string `json:"digest"`
}

type CriterionIdentityV1 struct {
	ID     string `json:"id"`
	Digest string `json:"digest"`
}

type ChangeRequestDraft struct {
	Summary                     string            `json:"summary"`
	Description                 string            `json:"description"`
	AcceptanceCriteria          []json.RawMessage `json:"acceptanceCriteria"`
	AcceptanceCriteriaSetDigest string            `json:"acceptanceCriteriaSetDigest,omitempty"`
	RepositoryURL               string            `json:"repositoryURL"`
	SourceCommit                string            `json:"sourceCommit"`
}

var criterionIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)

// CriterionDigest hashes versioned, fixed-field JSON. Text is preserved exactly:
// no trimming, line-ending conversion, Unicode normalization, or case folding.
// The digest field itself is excluded.
func CriterionDigest(id, text string) string {
	data, _ := json.Marshal(struct {
		SchemaVersion string `json:"schemaVersion"`
		ID            string `json:"id"`
		Text          string `json:"text"`
	}{"acceptance-criterion/v1", id, text})
	return DigestBytes(data)
}

// CriteriaSetDigest preserves order and binds each ID to its content digest.
func CriteriaSetDigest(criteria []CriterionIdentityV1) string {
	data, _ := json.Marshal(struct {
		SchemaVersion string                `json:"schemaVersion"`
		Criteria      []CriterionIdentityV1 `json:"criteria"`
	}{"acceptance-criteria-set/v1", criteria})
	return DigestBytes(data)
}

func CriterionIdentities(criteria []AcceptanceCriterionV1) []CriterionIdentityV1 {
	result := make([]CriterionIdentityV1, len(criteria))
	for i, criterion := range criteria {
		result[i] = CriterionIdentityV1{criterion.ID, criterion.Digest}
	}
	return result
}

// ValidateCriterionIdentities validates the controller-readable projection.
// Content-digest agreement is checked against exact text before projection.
func ValidateCriterionIdentities(criteria []CriterionIdentityV1, setDigest string) error {
	if len(criteria) < 1 || len(criteria) > 32 {
		return fmt.Errorf("acceptanceCriteria must contain 1 through 32 entries")
	}
	seen := map[string]bool{}
	for i, criterion := range criteria {
		if !criterionIDPattern.MatchString(criterion.ID) {
			return fmt.Errorf("acceptanceCriteria[%d].id must be a letter followed by at most 63 letters, digits, dots, underscores or hyphens", i)
		}
		if seen[criterion.ID] {
			return fmt.Errorf("acceptanceCriteria id %q is duplicated", criterion.ID)
		}
		seen[criterion.ID] = true
		if err := validateDigestField(fmt.Sprintf("acceptanceCriteria[%d].digest", i), criterion.Digest); err != nil {
			return err
		}
	}
	if err := validateDigestField("acceptanceCriteriaSetDigest", setDigest); err != nil {
		return err
	}
	if CriteriaSetDigest(criteria) != setDigest {
		return fmt.Errorf("acceptanceCriteriaSetDigest does not match ordered criteria")
	}
	return nil
}

func ValidateAcceptanceCriteria(criteria []AcceptanceCriterionV1, setDigest string) error {
	if err := ValidateCriterionIdentities(CriterionIdentities(criteria), setDigest); err != nil {
		return err
	}
	seenText := map[string]bool{}
	for i, criterion := range criteria {
		if !utf8.ValidString(criterion.Text) {
			return fmt.Errorf("acceptanceCriteria[%d].text must be UTF-8", i)
		}
		if err := boundedText(fmt.Sprintf("acceptanceCriteria[%d].text", i), criterion.Text, 4<<10); err != nil {
			return err
		}
		if seenText[criterion.Text] {
			return fmt.Errorf("acceptanceCriteria text is duplicated")
		}
		seenText[criterion.Text] = true
		if criterion.Digest != CriterionDigest(criterion.ID, criterion.Text) {
			return fmt.Errorf("acceptanceCriteria[%d].digest does not match canonical criterion", i)
		}
	}
	return nil
}

// PrepareChangeRequest is an authoring boundary, not an artifact validator.
// Text-only lists are new requests with locally scoped RQ-001... IDs. Structured
// drafts retain explicit IDs; omitted digests are computed, supplied ones must
// agree. Fully valid structured documents retain their exact submitted bytes.
func PrepareChangeRequest(data []byte) ([]byte, error) {
	// general validity
	if len(data) > MaxArtifactBytes {
		return nil, fmt.Errorf("change request exceeds %d-byte limit", MaxArtifactBytes)
	}
	if err := uniqueJSONFields(data); err != nil {
		return nil, err
	}

	var draft ChangeRequestDraft
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&draft); err != nil {
		return nil, fmt.Errorf("decode change request: %w", err)
	}
	if len(draft.AcceptanceCriteria) < 1 || len(draft.AcceptanceCriteria) > 32 {
		return nil, fmt.Errorf("acceptanceCriteria must contain 1 through 32 entries")
	}

	// convert CR draft --> hydrated domain change request
	isComplete, request, err := hydrateChangeRequest(draft)
	if err != nil {
		return nil, err
	}

	if err := request.Validate(); err != nil {
		return nil, err
	}

	// preserves complete and valid submitted request, avoiding digest differences
	if isComplete {
		return append([]byte(nil), data...), nil
	}
	result, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if err := ValidateContract(ChangeRequestContract, result); err != nil {
		return nil, err
	}
	return result, nil
}

func hydrateChangeRequest(draft ChangeRequestDraft) (bool, *ChangeRequest, error) {
	request := ChangeRequest{
		Summary:       draft.Summary,
		Description:   draft.Description,
		RepositoryURL: draft.RepositoryURL,
		SourceCommit:  draft.SourceCommit}

	isTextOnly := len(bytes.TrimSpace(draft.AcceptanceCriteria[0])) > 0 && bytes.TrimSpace(draft.AcceptanceCriteria[0])[0] == '"'
	isComplete := !isTextOnly && draft.AcceptanceCriteriaSetDigest != ""
	for i, raw := range draft.AcceptanceCriteria {
		var criterion AcceptanceCriterionV1
		if isTextOnly {
			if err := json.Unmarshal(raw, &criterion.Text); err != nil {
				return isComplete, nil, fmt.Errorf("acceptanceCriteria must not mix text and structured entries: %w", err)
			}
			criterion.ID = fmt.Sprintf("RQ-%03d", i+1)
		} else {
			d := json.NewDecoder(bytes.NewReader(raw))
			d.DisallowUnknownFields()
			if err := d.Decode(&criterion); err != nil {
				return isComplete, nil, fmt.Errorf("decode acceptanceCriteria[%d]: %w", i, err)
			}
		}
		if criterion.Digest == "" {
			isComplete = false
			criterion.Digest = CriterionDigest(criterion.ID, criterion.Text)
		}
		request.AcceptanceCriteria = append(request.AcceptanceCriteria, criterion)
	}
	if isTextOnly && draft.AcceptanceCriteriaSetDigest != "" {
		return isComplete, nil, fmt.Errorf("text-only authoring must omit acceptanceCriteriaSetDigest")
	}
	request.AcceptanceCriteriaSetDigest = draft.AcceptanceCriteriaSetDigest
	if request.AcceptanceCriteriaSetDigest == "" {
		request.AcceptanceCriteriaSetDigest = CriteriaSetDigest(CriterionIdentities(request.AcceptanceCriteria))
	}
	return isComplete, &request, nil
}

// Reject non-schema field spellings, duplicate keys, and trailing values so decoders cannot assign
// different meanings to the request
func uniqueJSONFields(data []byte) error {
	if !utf8.Valid(data) {
		return fmt.Errorf("change request must be valid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		if delimiter == '{' {
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok {
					return fmt.Errorf("invalid object key")
				}

				switch name {
				case "summary", "description", "acceptanceCriteria", "acceptanceCriteriaSetDigest", "repositoryURL", "sourceCommit", "id", "text", "digest":
				default:
					return fmt.Errorf("unknown field %q", name)
				}
				if seen[name] {
					return fmt.Errorf("duplicate JSON field %q", name)
				}
				seen[name] = true
				if err := value(); err != nil {
					return err
				}
			}
		} else if delimiter == '[' {
			for d.More() {
				if err := value(); err != nil {
					return err
				}
			}
		} else {
			return fmt.Errorf("unexpected JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing or invalid JSON data")
	}
	return nil
}
