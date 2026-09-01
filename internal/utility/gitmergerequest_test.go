package utility

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

type mergeRequestTransport func(*http.Request) (*http.Response, error)

func (transport mergeRequestTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func mergeRequestResponse(status int, value any) *http.Response {
	data, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(data)), Header: make(http.Header)}
}

func mergeRequestInput(t *testing.T) (utilitycontract.Input, githubPullRequest) {
	t.Helper()
	workspace, staging := t.TempDir(), t.TempDir()
	input := utilitycontract.Input{
		SchemaVersion: utilitycontract.Version,
		WorkflowID:    "wf",
		StepName:      "request",
		Attempt:       1,
		Authority: utilitycontract.AuthorityReference{
			APIVersion: "aim.sovereign.io/v1alpha1",
			Kind:       "UtilityOperation",
			Namespace:  "wf",
			Name:       "request-001",
			UID:        "request-uid"},
		PolicyDecisionID: "policy",
		Operation:        OperationGitMergeRequest,
		IdempotencyKey:   "wf/request",
		Parameters:       map[string]string{"repositoryURL": "https://github.com/example/calculator.git"},
		WorkspacePath:    workspace, StagingPath: staging,
		Outputs: []utilitycontract.OutputObligation{{Name: "merge-request", Version: "v1", Required: true}},
	}
	data, err := os.ReadFile(filepath.Join("..", "artifactcontract", "testdata", "fixtures.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Contract string         `json:"contract"`
		Document map[string]any `json:"document"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	documents := map[string]map[string]any{}
	for _, fixture := range fixtures {
		documents[fixture.Contract] = fixture.Document
	}
	candidate := documents[artifactcontract.CandidateRevisionContract]
	candidate["repositoryURL"] = input.Parameters["repositoryURL"]
	addUtilityInputArtifact(t, &input, "candidate-revision", artifactcontract.CandidateRevisionContract, candidate)
	proof := documents[artifactcontract.CandidateRemoteProofContract]
	proof["repositoryURL"], proof["candidateRevisionDigest"] = candidate["repositoryURL"], input.Inputs[0].Digest
	proof["ref"], proof["observedCommit"] = "refs/heads/"+candidate["branch"].(string), candidate["commit"]
	addUtilityInputArtifact(t, &input, "candidate-remote-proof", artifactcontract.CandidateRemoteProofContract, proof)
	validation := documents[artifactcontract.ValidationResultContract]
	validation["candidateRevisionDigest"] = input.Inputs[0].Digest
	validation["candidateCommit"], validation["candidateTree"] = candidate["commit"], candidate["tree"]
	validation["observedSourceRevision"] = candidate["commit"]
	addUtilityInputArtifact(t, &input, "validation-result", artifactcontract.ValidationResultContract, validation)
	credentialFile := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(credentialFile, []byte("https://utility:test-token@github.com/example/calculator.git\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(RepositoryCredentialFileEnv, credentialFile)
	pr := githubPullRequest{Number: 42, HTMLURL: "https://github.com/example/calculator/pull/42", State: "open"}
	pr.Head.Ref, pr.Head.SHA, pr.Head.Repo.FullName = candidate["branch"].(string), candidate["commit"].(string), "example/calculator"
	pr.Base.Ref, pr.Base.Repo.FullName = "main", "example/calculator"
	return input, pr
}

func TestGitMergeRequestCreatesAndReusesWithoutChangingGit(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-create-response-%t", lostResponse), func(t *testing.T) {
			input, pr := mergeRequestInput(t)
			gitTestCommand(t, input.WorkspacePath, "init")
			gitTestCommand(t, input.WorkspacePath, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-m", "initial")
			gitTestCommand(t, input.WorkspacePath, "branch", "-M", "main")
			before := gitTestOutput(t, input.WorkspacePath, "rev-parse", "main")
			created, posts := false, 0
			client := &http.Client{Transport: mergeRequestTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Host != "api.github.com" || request.Header.Get("Authorization") != "Bearer test-token" {
					t.Fatal("request destination or credential is incorrect")
				}
				path := strings.TrimPrefix(request.URL.Path, "/repos/example/calculator")
				switch {
				case request.Method == http.MethodGet && path == "/pulls":
					if request.URL.Query().Get("state") != "all" || request.URL.Query().Get("base") != "main" ||
						request.URL.Query().Get("head") != "example:"+pr.Head.Ref {
						t.Fatal("incorrect reconciliation filters")
					}
					if created {
						return mergeRequestResponse(200, []githubPullRequest{pr}), nil
					}
					return mergeRequestResponse(200, []githubPullRequest{}), nil
				case request.Method == http.MethodGet && strings.HasPrefix(path, "/git/ref/heads/"):
					return mergeRequestResponse(200, map[string]any{"object": map[string]string{"sha": pr.Head.SHA}}), nil
				case request.Method == http.MethodPost && path == "/pulls":
					var body map[string]string
					if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if body["title"] != "change" || body["body"] != "" || body["head"] != pr.Head.Ref || body["base"] != "main" || len(body) != 4 {
						t.Fatalf("unexpected creation payload: %#v", body)
					}
					posts++
					created = true
					if lostResponse {
						return nil, fmt.Errorf("simulated lost response")
					}
					return mergeRequestResponse(201, pr), nil
				case request.Method == http.MethodGet && path == "/pulls/42":
					return mergeRequestResponse(200, pr), nil
				default:
					t.Fatalf("unexpected GitHub call: %s %s", request.Method, request.URL)
					return nil, nil
				}
			})}
			registry := NewRegistry(GitMergeRequest{client: client})
			first, err := registry.Run(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			firstBytes, err := os.ReadFile(first.Artifacts[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			second, err := registry.Run(context.Background(), input)
			if err != nil {
				t.Fatal(err)
			}
			secondBytes, err := os.ReadFile(second.Artifacts[0].Path)
			if err != nil {
				t.Fatal(err)
			}
			if posts != 1 || !bytes.Equal(firstBytes, secondBytes) {
				t.Fatal("retry did not reuse stable request evidence")
			}
			if err := artifactcontract.ValidateContract(artifactcontract.MergeRequestContract, firstBytes); err != nil {
				t.Fatal(err)
			}
			if after := gitTestOutput(t, input.WorkspacePath, "rev-parse", "main"); after != before {
				t.Fatal("target branch changed")
			}
			if gitTestOutput(t, input.WorkspacePath, "branch", "--show-current") != "main" {
				t.Fatal("checkout changed")
			}
		})
	}
}

func TestGitMergeRequestRejectsChangedRemoteAndUntrustedResponses(t *testing.T) {
	for _, scenario := range []string{"remote-advanced", "created-head-advanced", "existing-head-advanced", "closed", "wrong-repository", "wrong-target", "wrong-url", "authentication", "invalid-json", "null-list"} {
		t.Run(scenario, func(t *testing.T) {
			input, pr := mergeRequestInput(t)
			posts := 0
			client := &http.Client{Transport: mergeRequestTransport(func(request *http.Request) (*http.Response, error) {
				if scenario == "null-list" {
					return mergeRequestResponse(200, nil), nil
				}
				if scenario == "authentication" {
					return mergeRequestResponse(403, map[string]string{"message": "secret-test-token"}), nil
				}
				if scenario == "invalid-json" {
					return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("secret-test-token"))}, nil
				}
				if request.Method == http.MethodPost {
					posts++
					return mergeRequestResponse(201, pr), nil
				}
				if strings.Contains(request.URL.Path, "/git/ref/") {
					sha := pr.Head.SHA
					if scenario == "remote-advanced" {
						sha = strings.Repeat("f", 40)
					}
					return mergeRequestResponse(200, map[string]any{"object": map[string]string{"sha": sha}}), nil
				}
				changed := pr
				switch scenario {
				case "existing-head-advanced", "created-head-advanced":
					changed.Head.SHA = strings.Repeat("f", 40)
				case "closed":
					changed.State = "closed"
				case "wrong-repository":
					changed.Head.Repo.FullName = "attacker/calculator"
				case "wrong-target":
					changed.Base.Ref = "unapproved"
				case "wrong-url":
					changed.HTMLURL = "https://attacker.invalid/request/42"
				}
				if strings.HasSuffix(request.URL.Path, "/pulls/42") {
					return mergeRequestResponse(200, changed), nil
				}
				if scenario == "remote-advanced" || scenario == "created-head-advanced" {
					return mergeRequestResponse(200, []githubPullRequest{}), nil
				}
				return mergeRequestResponse(200, []githubPullRequest{changed}), nil
			})}
			_, err := NewRegistry(GitMergeRequest{client: client}).Run(context.Background(), input)
			if err == nil {
				t.Fatal("unsafe state was accepted")
			}
			if strings.Contains(err.Error(), "secret-test-token") {
				t.Fatal("API error exposed response contents")
			}
			wantPosts := 0
			if scenario == "created-head-advanced" {
				wantPosts = 1
			}
			if posts != wantPosts {
				t.Fatalf("POST count = %d, want %d", posts, wantPosts)
			}
		})
	}
}

func TestGitMergeRequestRejectsInvalidEvidenceBeforeHTTP(t *testing.T) {
	for _, scenario := range []string{"missing-input", "digest-mismatch", "wrong-proof", "wrong-validation", "same-branch", "unsupported-host", "wrong-output"} {
		t.Run(scenario, func(t *testing.T) {
			input, pr := mergeRequestInput(t)
			switch scenario {
			case "missing-input":
				input.Inputs = input.Inputs[:2]
			case "digest-mismatch":
				input.Inputs[0].Digest = "sha256:" + strings.Repeat("f", 64)
			case "wrong-proof", "wrong-validation":
				index, field := 1, "observedCommit"
				if scenario == "wrong-validation" {
					index, field = 2, "candidateRevisionDigest"
				}
				data, err := os.ReadFile(input.Inputs[index].Path)
				if err != nil {
					t.Fatal(err)
				}
				var doc map[string]any
				if err := json.Unmarshal(data, &doc); err != nil {
					t.Fatal(err)
				}
				doc[field] = strings.Repeat("f", 40)
				if index == 2 {
					doc[field] = "sha256:" + strings.Repeat("f", 64)
				}
				data, _ = json.Marshal(doc)
				if err := os.WriteFile(input.Inputs[index].Path, data, 0600); err != nil {
					t.Fatal(err)
				}
				input.Inputs[index].Digest = artifactcontract.DigestBytes(data)
			case "same-branch":
				input.Parameters["targetBranch"] = pr.Head.Ref
			case "unsupported-host":
				input.Parameters["repositoryURL"] = "https://gitlab.com/example/calculator.git"
			case "wrong-output":
				input.Outputs[0].Name = "merge-revision"
			}
			client := &http.Client{Transport: mergeRequestTransport(func(*http.Request) (*http.Response, error) { t.Fatal("invalid evidence reached HTTP"); return nil, nil })}
			if _, err := NewRegistry(GitMergeRequest{client: client}).Run(context.Background(), input); err == nil {
				t.Fatal("invalid input was accepted")
			}
		})
	}
}

func TestGitMergeRequestReusesMergedRequestAndPaginates(t *testing.T) {
	input, pr := mergeRequestInput(t)
	merged := "2026-08-31T12:00:00Z"
	pr.State, pr.MergedAt = "closed", &merged
	pages := 0
	client := &http.Client{Transport: mergeRequestTransport(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Fatal("merged request triggered a write")
		}
		if strings.HasSuffix(request.URL.Path, "/pulls/42") {
			return mergeRequestResponse(200, pr), nil
		}
		pages++
		if request.URL.Query().Get("page") == "1" {
			old := pr
			old.Head.SHA = strings.Repeat("f", 40)
			history := make([]githubPullRequest, 100)
			for i := range history {
				history[i] = old
			}
			return mergeRequestResponse(200, history), nil
		}
		return mergeRequestResponse(200, []githubPullRequest{pr}), nil
	})}
	result, err := NewRegistry(GitMergeRequest{client: client}).Run(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if pages != 2 {
		t.Fatalf("pages = %d", pages)
	}
	data, _ := os.ReadFile(result.Artifacts[0].Path)
	var evidence artifactcontract.MergeRequest
	if err := json.Unmarshal(data, &evidence); err != nil {
		t.Fatal(err)
	}
	if evidence.State != "merged" {
		t.Fatal("merged state was lost")
	}
}

func TestMergeRequestCredentialScopeAndRedirects(t *testing.T) {
	for _, line := range []string{"https://u:secret@attacker.invalid", "https://u:secret@github.com/other/repo.git", "http://u:secret@github.com", "invalid"} {
		path := filepath.Join(t.TempDir(), "credentials")
		if err := os.WriteFile(path, []byte(line), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := mergeRequestToken(path, "example/calculator"); err == nil {
			t.Fatalf("accepted out-of-scope credential %q", line)
		}
	}
	input, _ := mergeRequestInput(t)
	calls := 0
	client := &http.Client{Transport: mergeRequestTransport(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.URL.Host != "api.github.com" {
			t.Fatal("credential followed redirect")
		}
		response := mergeRequestResponse(307, nil)
		response.Header.Set("Location", "https://attacker.invalid/")
		return response, nil
	})}
	if _, err := NewRegistry(GitMergeRequest{client: client}).Run(context.Background(), input); err == nil {
		t.Fatal("redirect was accepted")
	}
	if calls != 1 {
		t.Fatalf("redirect made %d calls", calls)
	}
}
