package utility

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SovereignAI/internal/workspaceeditor"
)

// Preserve the accepted source before candidate.prepare stages any changes.
// Git constructs the snapshot from the commit, never from the mutable checkout.
func ensureSourceSnapshot(ctx context.Context, workspace, base string) error {
	root := workspaceeditor.SourceRoot(workspace, base)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(root), 0o750); err != nil {
			return err
		}
		if _, err := runGit(ctx, workspace, "worktree", "add", "--detach", root, base); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	head, err := gitOutput(ctx, root, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return err
	}
	status, err := gitOutput(ctx, root, "status", "--porcelain")
	if err != nil {
		return err
	}
	if head != base || status != "" {
		return fmt.Errorf("source snapshot does not match clean accepted commit %s", base)
	}
	return nil
}

// Restore only the changed paths of a recorded, unchanged candidate. Additional
// staged, unstaged, or untracked edits remain errors and are never discarded.
func restoreCandidateBase(ctx context.Context, workspace, base string) error {
	if err := requireCleanBaseWorkspace(ctx, workspace, base); err == nil {
		return nil
	}
	head, err := gitOutput(ctx, workspace, "rev-parse", "HEAD^{commit}")
	if err != nil {
		return err
	}
	if head != base {
		return fmt.Errorf("candidate recovery requires HEAD at accepted source commit %s", base)
	}
	markers, err := filepath.Glob(filepath.Join(workspace, ".git", "sovereign", "candidates", "*.json"))
	if err != nil {
		return err
	}
	for _, path := range markers {
		previous, found, err := readPreparedCandidateMarker(path)
		if err != nil {
			return err
		}
		if !found || previous.BaseCommit != base {
			continue
		}
		if err := verifyPreparedWorkspace(ctx, workspace, previous); err != nil {
			continue
		}
		if len(previous.ChangedPaths) == 0 {
			continue
		}
		for _, changed := range previous.ChangedPaths {
			if _, err := safeCandidatePath(workspace, changed); err != nil {
				return err
			}
		}
		args := append([]string{"--literal-pathspecs", "restore", "--source=" + base, "--staged", "--worktree", "--"}, previous.ChangedPaths...)
		if _, err := runGit(ctx, workspace, args...); err != nil {
			return err
		}
		return requireCleanBaseWorkspace(ctx, workspace, base)
	}
	return fmt.Errorf("candidate recovery requires an unchanged, recorded prepared candidate")
}
