package workspaceeditor

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const AbsentDigest = "absent"
const defaultMaxFileBytes = int64(256 << 10)

type Editor struct {
	BaseRoot     string // immutable original checkout
	OverlayRoot  string // agent modifications
	MaxFileBytes int64
}

type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Digest  string `json:"digest"`
	Bytes   int64  `json:"bytes"`
	Source  string `json:"source"`
}

type SearchMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type Changes struct {
	Patch       string   `json:"patch"`
	PatchDigest string   `json:"patchDigest"`
	Files       []string `json:"files"`
	ByteCount   int      `json:"byteCount"`
	Added       int      `json:"added"`
	Deleted     int      `json:"deleted"`
}

type TreeListing struct {
	Entries      []string
	TotalEntries int
	Truncated    bool
}

// Ensure base & overlay directories are semantically correct, distinct, and exist.
func (e Editor) Validate() error {
	if e.BaseRoot == "" || e.OverlayRoot == "" {
		return fmt.Errorf("base and overlay roots are required")
	}
	base, err := filepath.Abs(e.BaseRoot)
	if err != nil {
		return err
	}
	overlay, err := filepath.Abs(e.OverlayRoot)
	if err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(base), filepath.Clean(overlay)) {
		return fmt.Errorf("base and overlay roots must be different")
	}
	info, err := os.Stat(base)
	if err != nil {
		return fmt.Errorf("inspect base root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("base root must be a directory")
	}
	return nil
}

func (e Editor) Read(path string, maxBytes int64) (File, error) {
	relative, err := normalizePath(path)
	if err != nil {
		return File{}, err
	}
	if e.deleted(relative) {
		return File{}, fs.ErrNotExist
	}
	if content, exists, err := e.readAt(e.filesRoot(), relative, e.limit(maxBytes)); err != nil {
		return File{}, err
	} else if exists {
		return newFile(relative, content, "overlay"), nil
	}
	content, exists, err := e.readAt(e.BaseRoot, relative, e.limit(maxBytes))
	if err != nil {
		return File{}, err
	}
	if !exists {
		return File{}, fs.ErrNotExist
	}
	return newFile(relative, content, "base"), nil
}

func (e Editor) Write(path, content, expectedDigest string) (File, error) {
	relative, err := normalizePath(path)
	if err != nil {
		return File{}, err
	}
	lineEnding, err := e.lineEndingForWrite(relative, expectedDigest)
	if err != nil {
		return File{}, err
	}
	normalized := normalizeLineEndings(content)
	if lineEnding == "\r\n" {
		normalized = strings.ReplaceAll(normalized, "\n", "\r\n")
	}
	data := []byte(normalized)
	if err := e.validateContent(data); err != nil {
		return File{}, err
	}
	if err := e.checkBasePath(relative); err != nil {
		return File{}, err
	}
	if err := atomicWrite(filepath.Join(e.filesRoot(), filepath.FromSlash(relative)), data); err != nil {
		return File{}, fmt.Errorf("write overlay file: %w", err)
	}
	if err := os.Remove(e.tombstonePath(relative)); err != nil && !os.IsNotExist(err) {
		return File{}, err
	}
	return newFile(relative, data, "overlay"), nil
}

func (e Editor) Replace(path, oldText, newText, expectedDigest string, expectedOccurrences int) (File, error) {
	oldText = normalizeLineEndings(oldText)
	newText = normalizeLineEndings(newText)
	if oldText == "" {
		return File{}, fmt.Errorf("oldText is required")
	}
	if expectedOccurrences <= 0 {
		expectedOccurrences = 1
	}
	current, err := e.Read(path, 0)
	if err != nil {
		return File{}, err
	}
	if current.Digest != expectedDigest {
		return File{}, staleError(current.Path, expectedDigest, current.Digest)
	}
	currentContent := normalizeLineEndings(current.Content)
	if count := strings.Count(currentContent, oldText); count != expectedOccurrences {
		return File{}, fmt.Errorf("oldText occurs %d times in %q, expected %d", count, current.Path, expectedOccurrences)
	}
	return e.Write(current.Path, strings.Replace(currentContent, oldText, newText, expectedOccurrences), expectedDigest)
}

func (e Editor) Delete(path, expectedDigest string) error {
	relative, err := normalizePath(path)
	if err != nil {
		return err
	}
	current, err := e.Read(relative, 0)
	if err != nil {
		return err
	}
	if current.Digest != expectedDigest {
		return staleError(relative, expectedDigest, current.Digest)
	}
	_, baseExists, err := e.readAt(e.BaseRoot, relative, e.limit(0))
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(e.filesRoot(), filepath.FromSlash(relative))); err != nil && !os.IsNotExist(err) {
		return err
	}
	if !baseExists {
		_ = os.Remove(e.tombstonePath(relative))
		return nil
	}
	return atomicWrite(e.tombstonePath(relative), []byte(relative+"\n"))
}

func (e Editor) Tree(path string, maxEntries int) ([]string, error) {
	listing, err := e.TreeListing(path, maxEntries)
	return listing.Entries, err
}

func (e Editor) TreeListing(path string, maxEntries int) (TreeListing, error) {
	prefix := ""
	if path != "" && path != "." {
		var err error
		prefix, err = normalizePath(path)
		if err != nil {
			return TreeListing{}, err
		}
	}
	if maxEntries <= 0 || maxEntries > 5000 {
		maxEntries = 500
	}
	all := map[string]struct{}{}
	if err := e.walk(e.BaseRoot, func(path string) { all[path] = struct{}{} }); err != nil {
		return TreeListing{}, err
	}
	if err := e.walk(e.filesRoot(), func(path string) { all[path] = struct{}{} }); err != nil && !os.IsNotExist(err) {
		return TreeListing{}, err
	}
	entries := make([]string, 0, len(all))
	for current := range all {
		if e.deleted(current) {
			continue
		}
		if prefix == "" || current == prefix || strings.HasPrefix(current, prefix+"/") {
			entries = append(entries, current)
		}
	}
	sort.Strings(entries)
	listing := TreeListing{TotalEntries: len(entries)}
	if len(entries) > maxEntries {
		listing.Truncated = true
		entries = entries[:maxEntries]
	}
	listing.Entries = entries
	return listing, nil
}

func (e Editor) Search(path, query string, maxResults int) ([]SearchMatch, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	if maxResults <= 0 || maxResults > 500 {
		maxResults = 100
	}
	entries, err := e.Tree(path, 5000)
	if err != nil {
		return nil, err
	}
	var matches []SearchMatch
	for _, path := range entries {
		file, err := e.Read(path, 1<<20)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(strings.NewReader(file.Content))
		for line := 1; scanner.Scan(); line++ {
			if strings.Contains(scanner.Text(), query) {
				matches = append(matches, SearchMatch{Path: path, Line: line, Text: scanner.Text()})
				if len(matches) == maxResults {
					return matches, nil
				}
			}
		}
	}
	return matches, nil
}

func (e Editor) Changes() (Changes, error) {
	changed := map[string]struct{}{}
	if err := e.walk(e.filesRoot(), func(path string) { changed[path] = struct{}{} }); err != nil && !os.IsNotExist(err) {
		return Changes{}, err
	}
	markers, err := os.ReadDir(e.tombstonesRoot())
	if err != nil && !os.IsNotExist(err) {
		return Changes{}, err
	}
	for _, marker := range markers {
		if marker.IsDir() || marker.Type()&os.ModeSymlink != 0 {
			return Changes{}, fmt.Errorf("invalid deletion marker %q", marker.Name())
		}
		data, err := os.ReadFile(filepath.Join(e.tombstonesRoot(), marker.Name()))
		if err != nil {
			return Changes{}, err
		}
		path, err := normalizePath(strings.TrimSuffix(string(data), "\n"))
		if err != nil {
			return Changes{}, err
		}
		changed[path] = struct{}{}
	}
	paths := make([]string, 0, len(changed))
	for path := range changed {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var patch strings.Builder
	var files []string
	for _, path := range paths {
		oldContent, oldExists, err := e.readAt(e.BaseRoot, path, e.limit(0))
		if err != nil {
			return Changes{}, err
		}
		newContent, newExists, err := e.readAt(e.filesRoot(), path, e.limit(0))
		if err != nil {
			return Changes{}, err
		}
		if e.deleted(path) {
			newContent, newExists = nil, false
		} else if !newExists {
			newContent, newExists = oldContent, oldExists
		}
		if oldExists == newExists && bytes.Equal(oldContent, newContent) {
			continue
		}
		filePatch, err := unifiedFileDiff(path, oldContent, oldExists, newContent, newExists)
		if err != nil {
			return Changes{}, err
		}
		if filePatch != "" {
			patch.WriteString(filePatch)
			files = append(files, path)
		}
	}
	result := Changes{Patch: patch.String(), Files: files}
	if result.Patch != "" {
		result.PatchDigest = digest([]byte(result.Patch))
		result.ByteCount = len([]byte(result.Patch))
		result.Added, result.Deleted = countPatchLines(result.Patch)
	}
	return result, nil
}

func (e Editor) filesRoot() string      { return filepath.Join(e.OverlayRoot, "files") }
func (e Editor) tombstonesRoot() string { return filepath.Join(e.OverlayRoot, "deletions") }
func (e Editor) tombstonePath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return filepath.Join(e.tombstonesRoot(), hex.EncodeToString(sum[:])+".path")
}

func (e Editor) deleted(path string) bool {
	data, err := os.ReadFile(e.tombstonePath(path))
	return err == nil && strings.TrimSuffix(string(data), "\n") == path
}

func (e Editor) limit(requested int64) int64 {
	limit := e.MaxFileBytes
	if limit <= 0 {
		limit = defaultMaxFileBytes
	}
	if requested > 0 && requested < limit {
		return requested
	}
	return limit
}

func (e Editor) validateContent(content []byte) error {
	if int64(len(content)) > e.limit(0) {
		return fmt.Errorf("file content exceeds %d-byte limit", e.limit(0))
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return fmt.Errorf("file content must be UTF-8 text without NUL")
	}
	if _, err := detectLineEnding(string(content)); err != nil {
		return err
	}
	return nil
}

func (e Editor) lineEndingForWrite(path, expected string) (string, error) {
	if expected == "" {
		return "", fmt.Errorf("expectedDigest is required")
	}
	current, err := e.Read(path, 0)
	if os.IsNotExist(err) {
		if expected != AbsentDigest {
			return "", staleError(path, expected, AbsentDigest)
		}
		return "\n", nil
	}
	if err != nil {
		return "", err
	}
	if current.Digest != expected {
		return "", staleError(path, expected, current.Digest)
	}
	return detectLineEnding(current.Content)
}

func (e Editor) readAt(root, relative string, limit int64) ([]byte, bool, error) {
	path, err := safePath(root, relative)
	if err != nil {
		return nil, false, err
	}
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !info.Mode().IsRegular() {
		return nil, false, fmt.Errorf("path %q is not a regular file", relative)
	}
	if info.Size() > limit {
		return nil, false, fmt.Errorf("file %q exceeds %d-byte limit", relative, limit)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, false, fmt.Errorf("file %q is not UTF-8 text", relative)
	}
	return content, true, nil
}

func (e Editor) walk(root string, visit func(string)) error {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	return filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absolute {
			return nil
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if excludedDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q is a symbolic link", filepath.ToSlash(relative))
		}
		visit(filepath.ToSlash(relative))
		return nil
	})
}

func (e Editor) checkBasePath(relative string) error {
	current := filepath.Clean(e.BaseRoot)
	for index, part := range strings.Split(relative, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("path %q traverses a symbolic link", relative)
		}
		if index < len(strings.Split(relative, "/"))-1 && !info.IsDir() {
			return fmt.Errorf("path %q traverses a non-directory", relative)
		}
	}
	return nil
}

func excludedDirectory(name string) bool {
	switch name {
	case ".git", ".sovereign", "attempts", "node_modules", "vendor", ".venv", "venv", "__pycache__":
		return true
	default:
		return false
	}
}

func normalizePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" || filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return "", fmt.Errorf("path must be a non-empty repository-relative path")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("path %q escapes the repository", path)
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".git" || part == ".sovereign" || part == "attempts" {
			return "", fmt.Errorf("path %q contains a reserved segment", path)
		}
	}
	return cleaned, nil
}

func safePath(root, relative string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	path, err := filepath.Abs(filepath.Join(absolute, filepath.FromSlash(relative)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(absolute, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes root", relative)
	}
	return path, nil
}

func atomicWrite(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".workspace-edit-*.tmp")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o640); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func normalizeLineEndings(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	return strings.ReplaceAll(value, "\r", "\n")
}

func detectLineEnding(value string) (string, error) {
	withoutCRLF := strings.ReplaceAll(value, "\r\n", "")
	if strings.ContainsRune(withoutCRLF, '\r') {
		return "", fmt.Errorf("file content must use consistent LF or CRLF line endings without bare CR")
	}
	if strings.Contains(value, "\r\n") {
		if strings.ContainsRune(withoutCRLF, '\n') {
			return "", fmt.Errorf("file content must use consistent LF or CRLF line endings without bare CR")
		}
		return "\r\n", nil
	}
	return "\n", nil
}

func newFile(path string, content []byte, source string) File {
	return File{Path: path, Content: string(content), Digest: digest(content), Bytes: int64(len(content)), Source: source}
}

func digest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func staleError(path, expected, current string) error {
	return fmt.Errorf("stale file digest for %q: expected %s, current %s", path, expected, current)
}

func countPatchLines(patch string) (added, deleted int) {
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "@@ "):
			inHunk = true
		case strings.HasPrefix(line, "diff --git "):
			inHunk = false
		case inHunk && strings.HasPrefix(line, "+"):
			added++
		case inHunk && strings.HasPrefix(line, "-"):
			deleted++
		}
	}
	return added, deleted
}
