package utility

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/utilitycontract"
)

// RepositoryInitialize materializes the Project-admitted repository and
// revision in the workflow's shared workspace.
type RepositoryInitialize struct{}

func (RepositoryInitialize) Name() string { return OperationRepositoryInitialize }

func (RepositoryInitialize) Validate(input utilitycontract.Input) error {
	if err := ensureWorkspace(input); err != nil {
		return err
	}
	if _, err := parameter(input, "repositoryURL"); err != nil {
		return err
	}
	_, err := parameter(input, "revision")
	return err
}

func (RepositoryInitialize) Run(ctx context.Context, input utilitycontract.Input) (utilitycontract.Result, error) {
	repositoryURL, _ := parameter(input, "repositoryURL")
	revision, _ := parameter(input, "revision")

	if _, err := runGit(ctx, input.WorkspacePath, "rev-parse", "--git-dir"); err != nil {
		// A new workflow PVC starts as an ordinary directory. Initializing in
		// place preserves the mounted workspace rather than replacing it.
		if _, err := runGit(ctx, input.WorkspacePath, "init"); err != nil {
			return utilitycontract.Result{}, err
		}
	}
	if err := ensureControlPlanePathsExcluded(input.WorkspacePath); err != nil {
		return utilitycontract.Result{}, err
	}
	remoteURL, err := gitOutput(ctx, input.WorkspacePath, "remote", "get-url", "origin")
	if err != nil {
		if _, addErr := runGit(ctx, input.WorkspacePath, "remote", "add", "origin", repositoryURL); addErr != nil {
			return utilitycontract.Result{}, addErr
		}
	} else if remoteURL != repositoryURL {
		return utilitycontract.Result{}, fmt.Errorf("workspace origin %q does not match admitted repository %q", remoteURL, repositoryURL)
	}
	if _, err := runGit(ctx, input.WorkspacePath, "fetch", "--prune", "origin", "+refs/heads/*:refs/remotes/origin/*"); err != nil {
		return utilitycontract.Result{}, err
	}
	// Prefer an already-resolved local commit/ref, then interpret a branch name
	// as the freshly fetched origin branch. Checkout remains detached so the
	// initialized workspace is pinned to one observed commit.
	resolvedRevision := revision
	if _, err := runGit(ctx, input.WorkspacePath, "rev-parse", "--verify", resolvedRevision+"^{commit}"); err != nil {
		resolvedRevision = "origin/" + revision
	}
	if _, err := runGit(ctx, input.WorkspacePath, "checkout", "--detach", "--force", resolvedRevision); err != nil {
		return utilitycontract.Result{}, err
	}
	commit, err := gitOutput(ctx, input.WorkspacePath, "rev-parse", "HEAD")
	if err != nil {
		return utilitycontract.Result{}, err
	}
	metadata := map[string]string{
		"repository": repositoryURL, "requestedRevision": revision, "revision": commit, "idempotencyKey": input.IdempotencyKey,
	}
	return gitArtifactResult(input, "repository snapshot initialized", metadata, artifactcontract.RepositoryRevision{
		RepositoryURL: repositoryURL, RequestedRevision: revision, ResolvedCommit: commit,
		UtilityOperation: artifactcontract.ObjectIdentity{Namespace: input.Authority.Namespace, Name: input.Authority.Name, UID: input.Authority.UID},
	})
}

func ensureControlPlanePathsExcluded(workspace string) error {
	// .git/info/exclude is local to this workspace. It prevents runner control
	// data and per-attempt staging files from entering a user commit without
	// changing the repository's tracked .gitignore.
	path := filepath.Join(workspace, ".git", "info", "exclude")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read Git exclude file: %w", err)
	}
	contents := string(data)
	changed := false
	for _, pattern := range []string{"/attempts/", "/.sovereign/"} {
		if !strings.Contains("\n"+contents+"\n", "\n"+pattern+"\n") {
			contents += "\n" + pattern
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create Git info directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(strings.TrimSpace(contents)+"\n"), 0o640); err != nil {
		return fmt.Errorf("write Git exclude file: %w", err)
	}
	return nil
}
