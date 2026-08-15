package contextrepo

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

type Repository struct {
	Root      string
	BundleDir string
	Revision  string
}

type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type Bundle struct {
	ID       string       `json:"id"`
	Revision string       `json:"revision"`
	Files    []BundleFile `json:"files"`
}

type BundleFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type SnapshotLimits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

type Snapshot struct {
	Revision   string
	Files      []SnapshotFile
	TotalBytes int64
}

type SnapshotFile struct {
	Path    string
	Digest  string
	Bytes   int64
	Content string
}

// Snapshot returns bounded, deterministic source context for one inference request.
func (r Repository) Snapshot(limits SnapshotLimits) (Snapshot, error) {
	if limits.MaxFiles <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 {
		return Snapshot{}, fmt.Errorf("snapshot limits must be positive")
	}
	root, err := r.resolve(".")
	if err != nil {
		return Snapshot{}, err
	}
	excluded := map[string]bool{
		".git": true, ".sovereign": true, "node_modules": true, "vendor": true,
		".venv": true, "venv": true, "__pycache__": true,
	}
	snapshot := Snapshot{Revision: r.Revision}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if excluded[entry.Name()] || relative == "attempts" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("repository path %q is a symbolic link", filepath.ToSlash(relative))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("repository path %q is not a regular file", filepath.ToSlash(relative))
		}
		if info.Size() > limits.MaxFileBytes {
			return fmt.Errorf("repository file %q exceeds %d-byte limit", filepath.ToSlash(relative), limits.MaxFileBytes)
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if int64(len(content)) > limits.MaxFileBytes {
			return fmt.Errorf("repository file %q exceeds %d-byte limit", filepath.ToSlash(relative), limits.MaxFileBytes)
		}
		if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
			return fmt.Errorf("repository file %q is not UTF-8 text", filepath.ToSlash(relative))
		}
		if len(snapshot.Files) == limits.MaxFiles {
			return fmt.Errorf("repository exceeds %d-file limit", limits.MaxFiles)
		}
		if snapshot.TotalBytes+int64(len(content)) > limits.MaxTotalBytes {
			return fmt.Errorf("repository exceeds %d-byte total limit", limits.MaxTotalBytes)
		}
		hash := sha256.Sum256(content)
		snapshot.Files = append(snapshot.Files, SnapshotFile{
			Path: filepath.ToSlash(relative), Digest: "sha256:" + hex.EncodeToString(hash[:]),
			Bytes: int64(len(content)), Content: string(content),
		})
		snapshot.TotalBytes += int64(len(content))
		return nil
	})
	if err != nil {
		return Snapshot{}, err
	}
	sort.Slice(snapshot.Files, func(i, j int) bool { return snapshot.Files[i].Path < snapshot.Files[j].Path })
	return snapshot, nil
}

func (r Repository) Tree(path string, limit int) ([]string, error) {
	if limit <= 0 || limit > 5000 {
		limit = 500
	}
	root, err := r.resolve(path)
	if err != nil {
		return nil, err
	}
	entries := make([]string, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(r.Root, path)
		if err != nil {
			return err
		}
		entries = append(entries, filepath.ToSlash(relative))
		if len(entries) >= limit {
			return fs.SkipAll
		}
		return nil
	})
	return entries, err
}

func (r Repository) Read(path string, maxBytes int64) (string, error) {
	if maxBytes <= 0 || maxBytes > 1<<20 {
		maxBytes = 256 << 10
	}
	resolved, err := r.resolve(path)
	if err != nil {
		return "", err
	}
	file, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxBytes {
		return "", fmt.Errorf("file exceeds %d-byte read limit", maxBytes)
	}
	return string(data), nil
}

func (r Repository) Search(path, query string, maxResults int) ([]SearchMatch, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if maxResults <= 0 || maxResults > 500 {
		maxResults = 100
	}
	root, err := r.resolve(path)
	if err != nil {
		return nil, err
	}
	matches := make([]SearchMatch, 0)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || info.Size() > 1<<20 {
			return nil
		}
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		line := 0
		for scanner.Scan() {
			line++
			if strings.Contains(scanner.Text(), query) {
				relative, _ := filepath.Rel(r.Root, path)
				matches = append(matches, SearchMatch{Path: filepath.ToSlash(relative), Line: line, Text: scanner.Text()})
				if len(matches) >= maxResults {
					_ = file.Close()
					return fs.SkipAll
				}
			}
		}
		_ = file.Close()
		return nil
	})
	return matches, err
}

func (r Repository) CreateBundle(paths []string) (Bundle, error) {
	if len(paths) == 0 || len(paths) > 100 {
		return Bundle{}, fmt.Errorf("bundle requires 1 to 100 paths")
	}
	bundle := Bundle{Revision: r.Revision}
	for _, path := range paths {
		resolved, err := r.resolve(path)
		if err != nil {
			return Bundle{}, err
		}
		digest, err := fileDigest(resolved)
		if err != nil {
			return Bundle{}, err
		}
		relative, _ := filepath.Rel(r.Root, resolved)
		bundle.Files = append(bundle.Files, BundleFile{Path: filepath.ToSlash(relative), Digest: "sha256:" + digest})
	}
	sort.Slice(bundle.Files, func(i, j int) bool { return bundle.Files[i].Path < bundle.Files[j].Path })
	canonical, _ := json.Marshal(bundle)
	hash := sha256.Sum256(canonical)
	bundle.ID = hex.EncodeToString(hash[:])
	if r.BundleDir != "" {
		data, _ := json.MarshalIndent(bundle, "", "  ")
		if err := os.MkdirAll(r.BundleDir, 0o750); err != nil {
			return Bundle{}, err
		}
		path := filepath.Join(r.BundleDir, bundle.ID+".json")
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o440)
		if err != nil {
			if !os.IsExist(err) {
				return Bundle{}, err
			}
		} else {
			if _, err := file.Write(data); err != nil {
				_ = file.Close()
				return Bundle{}, err
			}
			if err := file.Close(); err != nil {
				return Bundle{}, err
			}
		}
	}
	return bundle, nil
}

func (r Repository) resolve(path string) (string, error) {
	root, err := filepath.Abs(r.Root)
	if err != nil {
		return "", err
	}
	candidate, err := filepath.Abs(filepath.Join(root, filepath.Clean(path)))
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes repository root", path)
	}
	return candidate, nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
