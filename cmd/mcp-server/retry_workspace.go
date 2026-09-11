package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SovereignAI/internal/artifactcontract"
	"github.com/SovereignAI/internal/workspaceeditor"
)

func prepareRefinementWorkspace(editor *workspaceeditor.Editor, repositoryRoot string, sources []artifactcontract.SourceArtifact) error {
	var prior *artifactcontract.TestChangeSet
	var repository artifactcontract.RepositoryRevision
	var requestDigest, planDigest string
	for _, source := range sources {
		switch source.Contract {
		case artifactcontract.TestChangeSetContract:
			prior = &artifactcontract.TestChangeSet{}
			if err := json.Unmarshal(source.Content, prior); err != nil {
				return err
			}
			if err := prior.Validate(); err != nil {
				return err
			}
		case artifactcontract.RepositoryRevisionContract:
			if err := json.Unmarshal(source.Content, &repository); err != nil {
				return err
			}
		case artifactcontract.ChangeRequestContract:
			requestDigest = source.Digest
		case artifactcontract.ImplementationPlanContract:
			planDigest = source.Digest
		}
	}
	if prior == nil {
		return nil
	}
	if editor == nil {
		return fmt.Errorf("test repair requires a workspace editor")
	}
	if err := repository.Validate(); err != nil {
		return err
	}
	if prior.BaseCommit != repository.ResolvedCommit || prior.ChangeRequestDigest != requestDigest || prior.ImplementationPlanDigest != planDigest {
		return fmt.Errorf("prior tests do not match accepted repair inputs")
	}
	editor.BaseRoot = workspaceeditor.SourceRoot(repositoryRoot, repository.ResolvedCommit)
	if err := editor.Validate(); err != nil {
		return err
	}
	marker := filepath.Join(editor.OverlayRoot, ".repair-seeded")
	if _, err := os.Stat(marker); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	// The server has not started yet. Repeat partial seeding safely, but preserve
	// the agent's edits on any later sidecar restart.
	base := *editor
	base.OverlayRoot = filepath.Join(editor.OverlayRoot, ".base-only")
	for _, file := range prior.Files {
		original, err := base.Read(file.Path, 0)
		digest := workspaceeditor.AbsentDigest
		if err == nil {
			digest = original.Digest
		} else if !os.IsNotExist(err) {
			return err
		}
		if digest != file.BaseDigest {
			return fmt.Errorf("prior test %q baseDigest does not match accepted source commit", file.Path)
		}
		current, err := editor.Read(file.Path, 0)
		expected := workspaceeditor.AbsentDigest
		if err == nil {
			expected = current.Digest
		} else if !os.IsNotExist(err) {
			return err
		}
		if file.Action == "delete" {
			if expected != workspaceeditor.AbsentDigest {
				if err := editor.Delete(file.Path, expected); err != nil {
					return err
				}
			}
		} else {
			if _, err := editor.Write(file.Path, *file.ResultContent, expected); err != nil {
				return err
			}
		}
	}
	if err := os.MkdirAll(editor.OverlayRoot, 0o750); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte("seeded\n"), 0o640)
}
