package artifactcontract

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

var (
	digestPattern        = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	gitOIDPattern        = regexp.MustCompile(`^[0-9a-f]{40}$`)
	ociRepositoryPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)+$`)
	branchPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)
)

func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (v ChangeRequest) Validate() error {
	if err := boundedText("summary", v.Summary, 1024); err != nil {
		return err
	}
	if err := boundedText("description", v.Description, 16<<10); err != nil {
		return err
	}
	if err := ValidateAcceptanceCriteria(v.AcceptanceCriteria, v.AcceptanceCriteriaSetDigest); err != nil {
		return err
	}
	if err := validateRepositoryURL(v.RepositoryURL); err != nil {
		return fieldError("repositoryURL", err)
	}
	return validateGitOIDField("sourceCommit", v.SourceCommit)
}

func (v RepositoryRevision) Validate() error {
	if err := validateRepositoryURL(v.RepositoryURL); err != nil {
		return fieldError("repositoryURL", err)
	}
	if err := boundedText("requestedRevision", v.RequestedRevision, 255); err != nil {
		return err
	}
	if err := validateGitOIDField("resolvedCommit", v.ResolvedCommit); err != nil {
		return err
	}
	return validateObjectIdentity("utilityOperation", v.UtilityOperation)
}

func (v BranchReference) Validate() error {
	if err := validateRepositoryURL(v.RepositoryURL); err != nil {
		return fieldError("repositoryURL", err)
	}
	if err := validateBranch(v.Branch); err != nil {
		return fieldError("branch", err)
	}
	for field, value := range map[string]string{"baseCommit": v.BaseCommit, "commit": v.Commit} {
		if err := validateGitOIDField(field, value); err != nil {
			return err
		}
	}
	if err := validateObjectIdentity("utilityOperation", v.UtilityOperation); err != nil {
		return err
	}
	return boundedText("idempotencyKey", v.IdempotencyKey, 512)
}

func (v ImplementationPlan) Validate() error {
	if err := boundedText("summary", v.Summary, 1024); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"changeRequestDigest":      v.ChangeRequestDigest,
		"repositoryRevisionDigest": v.RepositoryRevisionDigest,
	} {
		if err := validateDigestField(field, value); err != nil {
			return err
		}
	}
	if err := validateGitOIDField("sourceCommit", v.SourceCommit); err != nil {
		return err
	}
	if len(v.AffectedPaths) == 0 || len(v.AffectedPaths) > 128 {
		return fmt.Errorf("affectedPaths must contain 1 through 128 entries")
	}
	paths := make([]string, 0, len(v.AffectedPaths))
	for index, affected := range v.AffectedPaths {
		if err := validateRepositoryPath(affected.Path); err != nil {
			return fieldError(fmt.Sprintf("affectedPaths[%d].path", index), err)
		}
		if !slices.Contains([]string{"add", "modify", "delete"}, affected.Action) {
			return fmt.Errorf("affectedPaths[%d].action is invalid", index)
		}
		paths = append(paths, affected.Path)
	}
	if err := requireSortedUnique("affectedPaths", paths); err != nil {
		return err
	}
	if err := validateStringList("implementationSteps", v.ImplementationSteps, 1, 64, 4<<10, true); err != nil {
		return err
	}
	if err := validateStringList("testStrategy", v.TestStrategy, 1, 32, 4<<10, true); err != nil {
		return err
	}
	if len(v.AcceptanceMapping) == 0 || len(v.AcceptanceMapping) > 32 {
		return fmt.Errorf("acceptanceMapping must contain 1 through 32 entries")
	}
	seenCriteria := map[int]struct{}{}
	for index, mapping := range v.AcceptanceMapping {
		if mapping.CriterionIndex < 1 {
			return fmt.Errorf("acceptanceMapping[%d].criterionIndex must be positive", index)
		}
		if _, exists := seenCriteria[mapping.CriterionIndex]; exists {
			return fmt.Errorf("acceptanceMapping criterionIndex %d is duplicated", mapping.CriterionIndex)
		}
		seenCriteria[mapping.CriterionIndex] = struct{}{}
		if err := boundedText(fmt.Sprintf("acceptanceMapping[%d].verification", index), mapping.Verification, 4<<10); err != nil {
			return err
		}
	}
	if v.Risks == nil || v.Assumptions == nil {
		return fmt.Errorf("risks and assumptions are required arrays")
	}
	if err := validateStringList("risks", v.Risks, 0, 32, 4<<10, true); err != nil {
		return err
	}
	return validateStringList("assumptions", v.Assumptions, 0, 32, 4<<10, true)
}

func (v TestChangeSet) Validate() error {
	if err := ChangeSet(v).Validate(); err != nil {
		return err
	}
	for _, file := range v.Files {
		if !isTestPath(file.Path) {
			return fmt.Errorf("test change set file %q is not a recognized test path", file.Path)
		}
	}
	return nil
}

func (v ChangeSet) Validate() error {
	if err := boundedText("summary", v.Summary, 1024); err != nil {
		return err
	}
	if err := validateGitOIDField("baseCommit", v.BaseCommit); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"changeRequestDigest":      v.ChangeRequestDigest,
		"implementationPlanDigest": v.ImplementationPlanDigest,
	} {
		if err := validateDigestField(field, value); err != nil {
			return err
		}
	}
	return validateChangedFiles(v.Files)
}

func validateChangedFiles(files []ChangedFile) error {
	if len(files) == 0 || len(files) > 128 {
		return fmt.Errorf("files must contain 1 through 128 changed files")
	}
	paths := make([]string, 0, len(files))
	for index, file := range files {
		field := fmt.Sprintf("files[%d]", index)
		if err := validateRepositoryPath(file.Path); err != nil {
			return fieldError(field+".path", err)
		}
		if !slices.Contains([]string{"add", "modify", "delete"}, file.Action) {
			return fmt.Errorf("%s.action is invalid", field)
		}
		switch file.Action {
		case "add":
			if file.BaseDigest != "absent" {
				return fmt.Errorf("%s.baseDigest must be absent for an added file", field)
			}
		case "modify", "delete":
			if err := validateDigestField(field+".baseDigest", file.BaseDigest); err != nil {
				return err
			}
		}
		if file.Action == "delete" {
			if file.ResultContent != nil || file.ResultDigest != "" {
				return fmt.Errorf("%s delete must omit resultContent and resultDigest", field)
			}
		} else {
			if file.ResultContent == nil {
				return fmt.Errorf("%s.resultContent is required", field)
			}
			content := []byte(*file.ResultContent)
			if len(content) > MaxChangedFileBytes || !utf8.Valid(content) || strings.ContainsRune(*file.ResultContent, '\x00') {
				return fmt.Errorf("%s.resultContent must be UTF-8 text without NUL and at most %d bytes", field, MaxChangedFileBytes)
			}
			if err := validateDigestField(field+".resultDigest", file.ResultDigest); err != nil {
				return err
			}
			if file.ResultDigest != DigestBytes(content) {
				return fmt.Errorf("%s.resultDigest does not match resultContent", field)
			}
			if file.Action == "modify" && file.ResultDigest == file.BaseDigest {
				return fmt.Errorf("%s modify does not change file content", field)
			}
		}
		paths = append(paths, file.Path)
	}
	return requireSortedUnique("files", paths)
}

func (v PreparedCandidate) Validate() error {
	for field, value := range map[string]string{
		"repositoryRevisionDigest": v.RepositoryRevisionDigest,
		"testChangeSetDigest":      v.TestChangeSetDigest,
		"changeSetDigest":          v.ChangeSetDigest,
		"preparationCommandDigest": v.PreparationCommandDigest,
	} {
		if err := validateDigestField(field, value); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{"baseCommit": v.BaseCommit, "candidateTree": v.CandidateTree} {
		if err := validateGitOIDField(field, value); err != nil {
			return err
		}
	}
	if err := validateSovereignBranch(v.Branch); err != nil {
		return fieldError("branch", err)
	}
	return validatePathSet("changedPaths", v.ChangedPaths, 1, 128)
}

func (v StreamEvidence) Validate() error {
	if err := validateDigestField("digest", v.Digest); err != nil {
		return err
	}
	if v.CapturedBytes < 0 {
		return fmt.Errorf("capturedBytes must be non-negative")
	}
	if !utf8.ValidString(v.Excerpt) || len([]byte(v.Excerpt)) > MaxTestOutputExcerptBytes {
		return fmt.Errorf("excerpt must be valid UTF-8 and at most %d bytes", MaxTestOutputExcerptBytes)
	}
	if !v.Truncated && v.CapturedBytes != len([]byte(v.Excerpt)) {
		return fmt.Errorf("capturedBytes must equal excerpt bytes when truncated is false")
	}
	if v.Truncated && v.CapturedBytes <= len([]byte(v.Excerpt)) {
		return fmt.Errorf("truncated requires omitted captured bytes")
	}
	return nil
}

func (v TestReport) Validate() error {
	if err := validateDigestField("preparedCandidateDigest", v.PreparedCandidateDigest); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"baseCommit": v.BaseCommit, "candidateTree": v.CandidateTree, "observedTreeAfter": v.ObservedTreeAfter,
	} {
		if err := validateGitOIDField(field, value); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"commandDigest": v.CommandDigest, "environmentImageDigest": v.EnvironmentImageDigest,
	} {
		if err := validateDigestField(field, value); err != nil {
			return err
		}
	}
	if !slices.Contains([]string{"passed", "failed", "timed-out", "environment-error", "tree-drift"}, v.Outcome) {
		return fmt.Errorf("outcome is invalid")
	}
	if v.DurationMilliseconds < 0 {
		return fmt.Errorf("durationMilliseconds must be non-negative")
	}
	if err := v.Stdout.Validate(); err != nil {
		return fieldError("stdout", err)
	}
	if err := v.Stderr.Validate(); err != nil {
		return fieldError("stderr", err)
	}
	if v.Stdout.CapturedBytes+v.Stderr.CapturedBytes > MaxCapturedTestOutputBytes {
		return fmt.Errorf("captured test output exceeds %d bytes", MaxCapturedTestOutputBytes)
	}
	if v.TestCount != nil && *v.TestCount < 0 {
		return fmt.Errorf("testCount must be non-negative")
	}
	switch v.Outcome {
	case "passed":
		if v.ExitCode == nil || *v.ExitCode != 0 || !v.WorkspaceClean || v.CandidateTree != v.ObservedTreeAfter {
			return fmt.Errorf("passed outcome requires exitCode zero, a clean workspace, and an unchanged candidate tree")
		}
	case "failed":
		if v.ExitCode == nil || *v.ExitCode == 0 {
			return fmt.Errorf("failed outcome requires a non-zero exitCode")
		}
	case "tree-drift":
		if v.WorkspaceClean && v.CandidateTree == v.ObservedTreeAfter {
			return fmt.Errorf("tree-drift requires a dirty workspace or changed tree")
		}
	}
	return nil
}

func (v CandidateRevision) Validate() error {
	if err := validateRepositoryURL(v.RepositoryURL); err != nil {
		return fieldError("repositoryURL", err)
	}
	for field, value := range map[string]string{
		"preparedCandidateDigest": v.PreparedCandidateDigest,
		"testReportDigest":        v.TestReportDigest,
		"changeSetDigest":         v.ChangeSetDigest,
		"commitMessageDigest":     v.CommitMessageDigest,
	} {
		if err := validateDigestField(field, value); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"sourceCommit": v.SourceCommit, "commit": v.Commit, "tree": v.Tree,
	} {
		if err := validateGitOIDField(field, value); err != nil {
			return err
		}
	}
	return fieldError("branch", validateSovereignBranch(v.Branch))
}

func (v ImageDigest) Validate() error {
	for field, value := range map[string]string{
		"candidateRevisionDigest": v.CandidateRevisionDigest,
		"digest":                  v.Digest,
		"builderImageDigest":      v.BuilderImageDigest,
		"buildCommandDigest":      v.BuildCommandDigest,
		"dockerfileDigest":        v.DockerfileDigest,
	} {
		if err := validateDigestField(field, value); err != nil {
			return err
		}
	}
	if !ociRepositoryPattern.MatchString(v.ImageRepository) {
		return fmt.Errorf("imageRepository is not a canonical OCI repository")
	}
	for field, value := range map[string]string{
		"candidateCommit": v.CandidateCommit, "candidateTree": v.CandidateTree, "contextTree": v.ContextTree,
	} {
		if err := validateGitOIDField(field, value); err != nil {
			return err
		}
	}
	if v.ContextTree != v.CandidateTree {
		return fmt.Errorf("contextTree must equal candidateTree")
	}
	return nil
}

func (v EndpointCheck) Validate(expectedName string) error {
	if v.Name != expectedName {
		return fmt.Errorf("name must be %q", expectedName)
	}
	if !slices.Contains([]string{"passed", "failed"}, v.Outcome) {
		return fmt.Errorf("outcome is invalid")
	}
	if v.HTTPStatus < 100 || v.HTTPStatus > 599 {
		return fmt.Errorf("httpStatus must be from 100 through 599")
	}
	return validateDigestField("responseDigest", v.ResponseDigest)
}

func (v ReviewInstructions) Validate() error {
	if errors := k8svalidation.IsDNS1123Label(v.Namespace); len(errors) > 0 {
		return fmt.Errorf("namespace is invalid: %s", strings.Join(errors, "; "))
	}
	if errors := k8svalidation.IsDNS1035Label(v.Service); len(errors) > 0 {
		return fmt.Errorf("service is invalid: %s", strings.Join(errors, "; "))
	}
	if v.LocalPort < 1024 || v.LocalPort > 65535 {
		return fmt.Errorf("localPort must be from 1024 through 65535")
	}
	if v.HealthPath != "/healthz" || v.CalculationPath != "/api/v1/calculate" {
		return fmt.Errorf("review paths do not match the frozen calculator API")
	}
	return nil
}

func (v ValidationResult) Validate() error {
	if err := validateObjectIdentity("validationRun", v.ValidationRun); err != nil {
		return err
	}
	if err := validateObjectIdentity("application", v.Application); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"candidateRevisionDigest": v.CandidateRevisionDigest,
		"imageArtifactDigest":     v.ImageArtifactDigest,
		"imageDigest":             v.ImageDigest,
		"observedImageDigest":     v.ObservedImageDigest,
	} {
		if err := validateDigestField(field, value); err != nil {
			return err
		}
	}
	for field, value := range map[string]string{
		"candidateCommit": v.CandidateCommit, "candidateTree": v.CandidateTree, "observedSourceRevision": v.ObservedSourceRevision,
	} {
		if err := validateGitOIDField(field, value); err != nil {
			return err
		}
	}
	if !ociRepositoryPattern.MatchString(v.ImageRepository) {
		return fmt.Errorf("imageRepository is not a canonical OCI repository")
	}
	if err := boundedText("infrastructureRevision", v.InfrastructureRevision, 255); err != nil {
		return err
	}
	if v.Provider != "argocd/kustomize" {
		return fmt.Errorf("provider must be argocd/kustomize")
	}
	if !slices.Contains([]string{"Synced", "OutOfSync", "Unknown"}, v.SyncStatus) {
		return fmt.Errorf("syncStatus is invalid")
	}
	if !slices.Contains([]string{"Healthy", "Progressing", "Degraded", "Missing", "Unknown"}, v.HealthStatus) {
		return fmt.Errorf("healthStatus is invalid")
	}
	expectedChecks := []string{"healthz", "add", "subtract", "multiply", "divide", "division-by-zero"}
	if len(v.EndpointChecks) != len(expectedChecks) {
		return fmt.Errorf("endpointChecks must contain exactly six checks")
	}
	for index, expected := range expectedChecks {
		if err := v.EndpointChecks[index].Validate(expected); err != nil {
			return fieldError(fmt.Sprintf("endpointChecks[%d]", index), err)
		}
	}
	if err := v.ReviewInstructions.Validate(); err != nil {
		return fieldError("reviewInstructions", err)
	}
	started, err := parseTimestamp(v.StartedAt)
	if err != nil {
		return fieldError("startedAt", err)
	}
	completed, err := parseTimestamp(v.CompletedAt)
	if err != nil {
		return fieldError("completedAt", err)
	}
	if completed.Before(started) {
		return fmt.Errorf("completedAt cannot precede startedAt")
	}
	if !slices.Contains([]string{"passed", "failed"}, v.Outcome) {
		return fmt.Errorf("outcome is invalid")
	}
	if v.Outcome == "passed" {
		if v.ObservedSourceRevision != v.CandidateCommit || v.ObservedImageDigest != v.ImageDigest ||
			v.SyncStatus != "Synced" || v.HealthStatus != "Healthy" {
			return fmt.Errorf("passed outcome requires matching observed identities and Synced/Healthy state")
		}
		expectedStatuses := []int{200, 200, 200, 200, 200, 422}
		for index, check := range v.EndpointChecks {
			if check.Outcome != "passed" || check.HTTPStatus != expectedStatuses[index] {
				return fmt.Errorf("passed outcome requires every endpoint check to pass with its expected HTTP status")
			}
		}
	}
	return nil
}

func (v RemoteProof) Validate(targetBranch, mergeCommit string) error {
	if v.Ref != "refs/heads/"+targetBranch {
		return fmt.Errorf("ref must identify the target branch")
	}
	if err := validateGitOIDField("observedCommit", v.ObservedCommit); err != nil {
		return err
	}
	if v.ObservedCommit != mergeCommit {
		return fmt.Errorf("observedCommit must equal mergeCommit")
	}
	_, err := parseTimestamp(v.VerifiedAt)
	return fieldError("verifiedAt", err)
}

func (v CandidateRemoteProof) Validate() error {
	if err := validateDigestField("candidateRevisionDigest", v.CandidateRevisionDigest); err != nil {
		return err
	}
	if err := validateRepositoryURL(v.RepositoryURL); err != nil {
		return fieldError("repositoryURL", err)
	}
	branch, ok := strings.CutPrefix(v.Ref, "refs/heads/")
	if !ok {
		return fmt.Errorf("ref must identify a branch")
	}
	if err := validateBranch(branch); err != nil {
		return fieldError("ref", err)
	}
	if err := validateGitOIDField("observedCommit", v.ObservedCommit); err != nil {
		return err
	}
	_, err := parseTimestamp(v.VerifiedAt)
	return fieldError("verifiedAt", err)
}

func (v MergeRevision) Validate() error {
	for field, value := range map[string]string{
		"candidateRevisionDigest": v.CandidateRevisionDigest,
		"validationResultDigest":  v.ValidationResultDigest,
	} {
		if err := validateDigestField(field, value); err != nil {
			return err
		}
	}
	if err := validateRepositoryURL(v.RepositoryURL); err != nil {
		return fieldError("repositoryURL", err)
	}
	if err := validateBranch(v.TargetBranch); err != nil {
		return fieldError("targetBranch", err)
	}
	for field, value := range map[string]string{
		"previousTargetCommit": v.PreviousTargetCommit,
		"candidateCommit":      v.CandidateCommit,
		"candidateTree":        v.CandidateTree,
		"mergeCommit":          v.MergeCommit,
		"mergeTree":            v.MergeTree,
	} {
		if err := validateGitOIDField(field, value); err != nil {
			return err
		}
	}
	for field, value := range map[string]ObjectIdentity{
		"approvalRequest": v.ApprovalRequest, "approvalDecision": v.ApprovalDecision, "validationRun": v.ValidationRun,
	} {
		if err := validateObjectIdentity(field, value); err != nil {
			return err
		}
	}
	return fieldError("remoteProof", v.RemoteProof.Validate(v.TargetBranch, v.MergeCommit))
}

func boundedText(field, value string, maximum int) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s is required", field)
	}
	if len([]byte(value)) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", field, maximum)
	}
	return nil
}

func validateStringList(field string, values []string, minimum, maximum, itemMaximum int, ordered bool) error {
	if len(values) < minimum || len(values) > maximum {
		return fmt.Errorf("%s must contain %d through %d entries", field, minimum, maximum)
	}
	seen := map[string]struct{}{}
	for index, value := range values {
		if err := boundedText(fmt.Sprintf("%s[%d]", field, index), value, itemMaximum); err != nil {
			return err
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate entry %q", field, value)
		}
		seen[value] = struct{}{}
	}
	_ = ordered
	return nil
}

func validatePathSet(field string, values []string, minimum, maximum int) error {
	if len(values) < minimum || len(values) > maximum {
		return fmt.Errorf("%s must contain %d through %d entries", field, minimum, maximum)
	}
	for index, value := range values {
		if err := validateRepositoryPath(value); err != nil {
			return fieldError(fmt.Sprintf("%s[%d]", field, index), err)
		}
	}
	return requireSortedUnique(field, values)
}

func requireSortedUnique(field string, values []string) error {
	for index := range values {
		if index > 0 && values[index-1] >= values[index] {
			return fmt.Errorf("%s must be sorted and unique", field)
		}
	}
	return nil
}

func validateDigestField(field, value string) error {
	if !digestPattern.MatchString(value) {
		return fmt.Errorf("%s must be sha256 followed by 64 lowercase hexadecimal characters", field)
	}
	return nil
}

func validateGitOIDField(field, value string) error {
	if !gitOIDPattern.MatchString(value) {
		return fmt.Errorf("%s must be 40 lowercase hexadecimal characters", field)
	}
	return nil
}

func validateRepositoryURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path == "" || parsed.Path == "/" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an absolute credential-free HTTPS URL with a host and path")
	}
	return nil
}

func validateObjectIdentity(field string, value ObjectIdentity) error {
	if errors := k8svalidation.IsDNS1123Label(value.Namespace); len(errors) > 0 {
		return fmt.Errorf("%s.namespace is invalid: %s", field, strings.Join(errors, "; "))
	}
	if errors := k8svalidation.IsDNS1123Subdomain(value.Name); len(errors) > 0 {
		return fmt.Errorf("%s.name is invalid: %s", field, strings.Join(errors, "; "))
	}
	if err := boundedText(field+".uid", value.UID, 128); err != nil {
		return err
	}
	return nil
}

func validateRepositoryPath(value string) error {
	if value == "" || len([]byte(value)) > 512 || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return fmt.Errorf("must be a non-empty repository-relative path using /")
	}
	segments := strings.Split(value, "/")
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." || segment == ".git" {
			return fmt.Errorf("contains a forbidden path segment")
		}
	}
	return nil
}

func isTestPath(value string) bool {
	segments := strings.Split(value, "/")
	for _, segment := range segments[:len(segments)-1] {
		directory := strings.ToLower(segment)
		if directory == "test" || directory == "tests" || directory == "testdata" || directory == "test_data" ||
			directory == "__tests__" || strings.HasSuffix(directory, ".tests") {
			return true
		}
	}

	base := segments[len(segments)-1]
	lowerBase := strings.ToLower(base)
	if strings.HasPrefix(lowerBase, "test_") || lowerBase == "conftest.py" {
		return true
	}
	extension := strings.LastIndexByte(base, '.')
	if extension <= 0 {
		return false
	}
	stem := base[:extension]
	lowerStem := strings.ToLower(stem)
	return strings.HasSuffix(lowerStem, "_test") || strings.HasSuffix(lowerStem, ".test") ||
		strings.HasSuffix(lowerStem, ".spec") || strings.HasSuffix(stem, "Test") || strings.HasSuffix(stem, "Tests")
}

func validateBranch(value string) error {
	if value == "" || len([]byte(value)) > 255 || !branchPattern.MatchString(value) ||
		strings.Contains(value, "..") || strings.Contains(value, "//") || strings.HasSuffix(value, "/") ||
		strings.HasSuffix(value, ".") || strings.HasSuffix(value, ".lock") || strings.ContainsAny(value, " ~^:?*[\\") {
		return fmt.Errorf("is not a valid Git branch name")
	}
	return nil
}

func validateSovereignBranch(value string) error {
	if err := validateBranch(value); err != nil {
		return err
	}
	if !strings.HasPrefix(value, "sovereign/") || len(strings.TrimPrefix(value, "sovereign/")) == 0 {
		return fmt.Errorf("must match the frozen sovereign/<workflow-identity> form")
	}
	return nil
}

func parseTimestamp(value string) (time.Time, error) {
	if !strings.HasSuffix(value, "Z") || strings.Contains(value, ".") {
		return time.Time{}, fmt.Errorf("must be UTC RFC3339 with seconds precision")
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Nanosecond() != 0 {
		return time.Time{}, fmt.Errorf("must be UTC RFC3339 with seconds precision")
	}
	return parsed, nil
}

func fieldError(field string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", field, err)
}
