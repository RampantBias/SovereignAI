package utility

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

const RepositoryCredentialFileEnv = "SOVEREIGN_REPOSITORY_CREDENTIAL_FILE"

// GitMergeRequest creates review evidence without modifying local Git state or
// merging/pushing the target. Initial implementation is github only;
type GitMergeRequest struct {
	client *http.Client // transport injection for offline tests
}

type githubMergeRequestAPI struct {
	client     *http.Client
	token      string
	repository string
}

type githubPullRequest struct {
	Number   int           `json:"number"`
	HTMLURL  string        `json:"html_url"`
	State    string        `json:"state"`
	MergedAt *string       `json:"merged_at"`
	Head     githubPullRef `json:"head"`
	Base     githubPullRef `json:"base"`
}

type githubPullRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

func (GitMergeRequest) Name() string { return OperationGitMergeRequest }

func (GitMergeRequest) Validate(input utilitycontract.Input) error {
	if err := EnsureWorkspace(input); err != nil {
		return err
	}
	if _, err := githubRepository(input.Parameters["repositoryURL"]); err != nil {
		return err
	}
	if input.IdempotencyKey == "" {
		return fmt.Errorf("git.mergeRequest requires an idempotency key")
	}
	if len(input.Outputs) != 1 || OutputContract(input) != artifactcontract.MergeRequestContract {
		return fmt.Errorf("git.mergeRequest requires exactly one merge-request/v1 output")
	}
	for _, contract := range []string{artifactcontract.CandidateRevisionContract, artifactcontract.CandidateRemoteProofContract, artifactcontract.ValidationResultContract} {
		if _, err := RequiredInput(input, contract); err != nil {
			return err
		}
	}
	return nil
}

func (operation GitMergeRequest) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	candidateRef, candidate, err := ReadInputArtifact[artifactcontract.CandidateRevision](input, artifactcontract.CandidateRevisionContract)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	proofRef, proof, err := ReadInputArtifact[artifactcontract.CandidateRemoteProof](input, artifactcontract.CandidateRemoteProofContract)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	validationRef, validation, err := ReadInputArtifact[artifactcontract.ValidationResult](input, artifactcontract.ValidationResultContract)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	for _, value := range []interface{ Validate() error }{candidate, proof, validation} {
		if err := value.Validate(); err != nil {
			return utilitycontract.Result{}, fmt.Errorf("invalid merge request evidence: %w", err)
		}
	}
	repository := input.Parameters["repositoryURL"]
	if candidate.RepositoryURL != repository || proof.RepositoryURL != repository ||
		proof.CandidateRevisionDigest != candidateRef.Digest || proof.Ref != "refs/heads/"+candidate.Branch ||
		proof.ObservedCommit != candidate.Commit || validation.CandidateRevisionDigest != candidateRef.Digest ||
		validation.CandidateCommit != candidate.Commit || validation.CandidateTree != candidate.Tree || validation.Outcome != "passed" {
		return utilitycontract.Result{}, fmt.Errorf("merge request inputs do not identify one pushed and validated candidate revision")
	}
	target := OptionalParameter(input, "targetBranch", "main")
	// Validate output identity before any network side effect.
	evidence := artifactcontract.MergeRequest{
		CandidateRevisionDigest:    candidateRef.Digest,
		CandidateRemoteProofDigest: proofRef.Digest,
		ValidationResultDigest:     validationRef.Digest,
		RepositoryURL:              repository,
		Provider:                   "github", Number: 1,
		URL:             strings.TrimSuffix(repository, ".git") + "/pull/1",
		SourceBranch:    candidate.Branch,
		TargetBranch:    target,
		CandidateCommit: candidate.Commit,
		State:           "open",
		UtilityOperation: artifactcontract.ObjectIdentity{
			Namespace: input.Authority.Namespace,
			Name:      input.Authority.Name,
			UID:       input.Authority.UID},
		IdempotencyKey: input.IdempotencyKey,
	}
	if err := evidence.Validate(); err != nil {
		return utilitycontract.Result{}, err
	}
	repo, err := githubRepository(repository)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	token, err := mergeRequestToken(os.Getenv(RepositoryCredentialFileEnv), repo)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if operation.client != nil {
		*client = *operation.client
	}
	// Never forward repository credentials to a redirect destination.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	api := githubMergeRequestAPI{client: client, token: token, repository: repo}
	request, found, err := api.find(ctx, candidate.Branch, target, candidate.Commit)
	if err != nil {
		return utilitycontract.Result{}, err
	}
	if !found {
		var ref struct {
			Object struct {
				SHA string `json:"sha"`
			} `json:"object"`
		}
		if err := api.call(ctx, http.MethodGet, "/git/ref/heads/"+url.PathEscape(candidate.Branch), nil, &ref); err != nil {
			return utilitycontract.Result{}, err
		}
		if ref.Object.SHA != candidate.Commit {
			return utilitycontract.Result{}, fmt.Errorf("remote source branch no longer identifies the validated candidate")
		}
		// Keep user-visible text intentionally minimal. Identity lives in typed
		// evidence, and remote retries reconcile repository/head/base/commit.
		body := map[string]string{"title": "change", "body": "", "head": candidate.Branch, "base": target}
		if err := api.call(ctx, http.MethodPost, "/pulls", body, &request); err != nil {
			// A timed-out create or a competing create can have succeeded.
			// Observe again; never blindly repeat POST within this invocation.
			observed, exists, observeErr := api.find(ctx, candidate.Branch, target, candidate.Commit)
			if observeErr != nil || !exists {
				return utilitycontract.Result{}, err
			}
			request = observed
		}
	}
	if request.Number < 1 {
		return utilitycontract.Result{}, fmt.Errorf("GitHub returned a merge request without a number")
	}
	// Confirm the server's current state, including the head SHA, after create
	// or reuse. A branch advancing during the request must not be accepted.
	var confirmed githubPullRequest
	if err := api.call(ctx, http.MethodGet, "/pulls/"+strconv.Itoa(request.Number), nil, &confirmed); err != nil {
		return utilitycontract.Result{}, err
	}
	if confirmed.Number != request.Number {
		return utilitycontract.Result{}, fmt.Errorf("GitHub returned a different merge request number")
	}
	if err := confirmed.validate(repo, candidate.Branch, target, candidate.Commit); err != nil {
		return utilitycontract.Result{}, err
	}
	evidence.Number, evidence.URL = confirmed.Number, confirmed.HTMLURL
	evidence.State = "open"
	if confirmed.MergedAt != nil {
		evidence.State = "merged"
	}
	if err := evidence.Validate(); err != nil {
		return utilitycontract.Result{}, err
	}
	return GitArtifactResult(input, "merge request ready", map[string]string{
		"number": strconv.Itoa(evidence.Number), "url": evidence.URL, "source": candidate.Branch,
		"target": target, "candidateRevision": candidate.Commit, "idempotencyKey": input.IdempotencyKey,
	}, evidence)
}

func githubRepository(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", fmt.Errorf("git.mergeRequest currently supports credential-free HTTPS github.com repository URLs only")
	}

	var githubRepositoryPart = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)
	parts := strings.Split(strings.TrimPrefix(strings.TrimSuffix(u.Path, ".git"), "/"), "/")
	if len(parts) != 2 || !githubRepositoryPart.MatchString(parts[0]) || !githubRepositoryPart.MatchString(parts[1]) {
		return "", fmt.Errorf("git.mergeRequest repository URL must identify a GitHub owner and repository")
	}
	return strings.Join(parts, "/"), nil
}

// Read only the controller-mounted credential store. Do not invoke workspace
// credential helpers or accept a token/API endpoint in operation parameters.
func mergeRequestToken(path, repository string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("git.mergeRequest requires a mounted repository credential file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("cannot read merge request repository credential")
	}
	defer file.Close()
	scanner := bufio.NewScanner(io.LimitReader(file, 1<<20))
	for scanner.Scan() {
		u, err := url.Parse(strings.TrimSpace(scanner.Text()))
		if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User == nil || u.RawQuery != "" || u.Fragment != "" {
			continue
		}
		credentialRepo := strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		if credentialRepo != "" && !strings.EqualFold(credentialRepo, repository) {
			continue
		}
		if password, ok := u.User.Password(); ok && password != "" {
			return password, nil
		}
	}
	return "", fmt.Errorf("repository credential store has no matching GitHub token")
}

func (pr githubPullRequest) validate(repository, source, target, commit string) error {
	if pr.Number < 1 || !strings.EqualFold(pr.Head.Repo.FullName, repository) ||
		!strings.EqualFold(pr.Base.Repo.FullName, repository) || pr.Head.Ref != source ||
		pr.Base.Ref != target || pr.Head.SHA != commit {
		return fmt.Errorf("existing merge request does not identify the requested repository, branches, and candidate")
	}
	if pr.State != "open" && !(pr.State == "closed" && pr.MergedAt != nil) {
		return fmt.Errorf("existing merge request is closed without merging; refusing to create a duplicate")
	}
	expectedURL := "https://github.com/" + repository + "/pull/" + strconv.Itoa(pr.Number)
	if !strings.EqualFold(pr.HTMLURL, expectedURL) {
		return fmt.Errorf("GitHub returned an unexpected merge request URL")
	}
	return nil
}

func (api githubMergeRequestAPI) find(ctx context.Context, source, target, commit string) (githubPullRequest, bool, error) {
	owner := strings.SplitN(api.repository, "/", 2)[0]
	// Query all states so a retry cannot silently replace a closed request.
	for page := 1; page <= 100; page++ {
		query := url.Values{"state": {"all"}, "head": {owner + ":" + source}, "base": {target}, "per_page": {"100"}, "page": {strconv.Itoa(page)}}
		var requests []githubPullRequest
		if err := api.call(ctx, http.MethodGet, "/pulls?"+query.Encode(), nil, &requests); err != nil {
			return githubPullRequest{}, false, err
		}
		if requests == nil {
			return githubPullRequest{}, false, fmt.Errorf("GitHub returned a null merge request list")
		}
		for _, pr := range requests {
			if err := pr.validate(api.repository, source, target, commit); err != nil {
				// Historical requests for earlier candidates can be ignored,
				// but an open request must never be mistaken for this candidate.
				if pr.State == "closed" && pr.Head.SHA != commit {
					continue
				}
				return githubPullRequest{}, false, err
			}
			return pr, true, nil
		}
		if len(requests) < 100 {
			return githubPullRequest{}, false, nil
		}
	}
	return githubPullRequest{}, false, fmt.Errorf("merge request search exceeded pagination limit")
}

func (api githubMergeRequestAPI) call(ctx context.Context, method, path string, body, result any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "https://api.github.com/repos/"+api.repository+path, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("cannot construct GitHub merge request API call")
	}
	request.Header.Set("Authorization", "Bearer "+api.token)
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2026-03-10")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := api.client.Do(request)
	if err != nil {
		// Transport errors and response bodies can contain sensitive content.
		return fmt.Errorf("GitHub merge request API transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusCreated {
		return fmt.Errorf("GitHub merge request API returned HTTP %d", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(result); err != nil {
		return fmt.Errorf("GitHub merge request API returned invalid JSON")
	}
	return nil
}
