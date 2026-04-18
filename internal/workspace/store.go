package workspace

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Entry describes one file or directory in a workspace listing.
type Entry struct {
	Path      string    `json:"path"`
	IsDir     bool      `json:"is_dir"`
	Size      int64     `json:"size"`
	UpdatedAt time.Time `json:"updated_at"`
}

// EditResult describes a text replacement applied to a workspace file.
type EditResult struct {
	Path         string `json:"path"`
	Replacements int    `json:"replacements"`
	Bytes        int    `json:"bytes"`
}

// ReadResult describes the content returned from a workspace read.
type ReadResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	TotalLines int    `json:"total_lines"`
}

// GrepMatch describes one matched line returned from a workspace grep query.
type GrepMatch struct {
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Column int    `json:"column"`
	Text   string `json:"text"`
}

// Manager provides safe, peer-scoped filesystem operations.
type Manager struct {
	root         string
	perAgent     bool
	maxFileBytes int
}

func New(root string, perAgent bool, maxFileBytes int) (*Manager, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	if maxFileBytes < 1 {
		return nil, fmt.Errorf("workspace max file bytes must be >= 1")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	if err := os.MkdirAll(absRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create workspace root: %w", err)
	}
	return &Manager{root: absRoot, perAgent: perAgent, maxFileBytes: maxFileBytes}, nil
}

func (m *Manager) Root() string { return m.root }

// Resolve converts a peer-scoped relative path into an absolute path inside the
// workspace, rejecting traversal outside the sandbox.
func (m *Manager) Resolve(peerID, relPath string) (string, error) {
	return m.resolve(peerID, relPath)
}

func (m *Manager) PeerRoot(peerID string) string {
	if !m.perAgent {
		return m.root
	}
	safe := sanitizePeerID(peerID)
	return filepath.Join(m.root, safe)
}

func (m *Manager) EnsurePeer(peerID string) (string, error) {
	dir := m.PeerRoot(peerID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create peer workspace: %w", err)
	}
	return dir, nil
}

func (m *Manager) Read(peerID, relPath string) (string, error) {
	result, err := m.ReadRange(peerID, relPath, 0, 0)
	if err != nil {
		return "", err
	}
	return result.Content, nil
}

func (m *Manager) ReadRange(peerID, relPath string, startLine, endLine int) (*ReadResult, error) {
	target, err := m.resolve(peerID, relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	if info.Size() > int64(m.maxFileBytes) {
		return nil, fmt.Errorf("file exceeds max bytes %d", m.maxFileBytes)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	content := string(b)
	lines := splitLinesKeepNewline(content)
	totalLines := len(lines)
	if totalLines == 0 {
		return &ReadResult{
			Path:       relPath,
			Content:    "",
			StartLine:  0,
			EndLine:    0,
			TotalLines: 0,
		}, nil
	}
	if startLine < 0 {
		return nil, fmt.Errorf("start_line must be >= 1")
	}
	if endLine < 0 {
		return nil, fmt.Errorf("end_line must be >= 1")
	}
	if startLine == 0 {
		startLine = 1
	}
	if endLine == 0 || endLine > totalLines {
		endLine = totalLines
	}
	if startLine > totalLines {
		return nil, fmt.Errorf("start_line %d exceeds total lines %d", startLine, totalLines)
	}
	if endLine < startLine {
		return nil, fmt.Errorf("end_line must be >= start_line")
	}
	return &ReadResult{
		Path:       relPath,
		Content:    strings.Join(lines[startLine-1:endLine], ""),
		StartLine:  startLine,
		EndLine:    endLine,
		TotalLines: totalLines,
	}, nil
}

func (m *Manager) Write(peerID, relPath, content string, appendMode bool) (string, error) {
	target, err := m.resolve(peerID, relPath)
	if err != nil {
		return "", err
	}
	if len(content) > m.maxFileBytes {
		return "", fmt.Errorf("content exceeds max bytes %d", m.maxFileBytes)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}
	if appendMode {
		f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if _, err := f.WriteString(content); err != nil {
			return "", err
		}
		return target, nil
	}
	if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
		return "", err
	}
	return target, nil
}

func (m *Manager) Edit(peerID, relPath, oldText, newText string, replaceAll bool) (*EditResult, error) {
	if oldText == "" {
		return nil, fmt.Errorf("old_text must not be empty")
	}
	target, err := m.resolve(peerID, relPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, fmt.Errorf("path is a directory")
	}
	if info.Size() > int64(m.maxFileBytes) {
		return nil, fmt.Errorf("file exceeds max bytes %d", m.maxFileBytes)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return nil, err
	}
	original := string(b)
	replacements := 0
	updated := original
	if replaceAll {
		replacements = strings.Count(original, oldText)
		if replacements == 0 {
			return nil, fmt.Errorf("old_text not found")
		}
		updated = strings.ReplaceAll(original, oldText, newText)
	} else {
		idx := strings.Index(original, oldText)
		if idx < 0 {
			return nil, fmt.Errorf("old_text not found")
		}
		replacements = 1
		updated = original[:idx] + newText + original[idx+len(oldText):]
	}
	if len(updated) > m.maxFileBytes {
		return nil, fmt.Errorf("edited content exceeds max bytes %d", m.maxFileBytes)
	}
	if err := os.WriteFile(target, []byte(updated), 0o644); err != nil {
		return nil, err
	}
	return &EditResult{
		Path:         relPath,
		Replacements: replacements,
		Bytes:        len(updated),
	}, nil
}

func (m *Manager) Delete(peerID, relPath string, recursive bool) error {
	target, err := m.resolve(peerID, relPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	if info.IsDir() && !recursive {
		return fmt.Errorf("path is a directory; set recursive=true")
	}
	if recursive {
		return os.RemoveAll(target)
	}
	return os.Remove(target)
}

func (m *Manager) Mkdir(peerID, relPath string) (string, error) {
	target, err := m.resolve(peerID, relPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	return target, nil
}

func (m *Manager) Grep(peerID, relPath, pattern string, recursive bool, limit int, caseSensitive, useRegexp bool) ([]GrepMatch, error) {
	target, err := m.resolve(peerID, relPath)
	if err != nil {
		return nil, err
	}
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if limit <= 0 {
		limit = 100
	}

	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}

	matcher, err := compileGrepMatcher(pattern, caseSensitive, useRegexp)
	if err != nil {
		return nil, err
	}

	matches := make([]GrepMatch, 0, min(limit, 16))
	processFile := func(path string, info fs.FileInfo) error {
		if info.IsDir() || len(matches) >= limit {
			return nil
		}
		if info.Size() > int64(m.maxFileBytes) {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(m.PeerRoot(peerID), path)
		if err != nil {
			rel = info.Name()
		}
		scanner := bufio.NewScanner(strings.NewReader(string(b)))
		scanner.Buffer(make([]byte, 0, 64*1024), m.maxFileBytes)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			column, ok := matcher(line)
			if !ok {
				continue
			}
			matches = append(matches, GrepMatch{
				Path:   filepath.ToSlash(rel),
				Line:   lineNo,
				Column: column,
				Text:   line,
			})
			if len(matches) >= limit {
				return nil
			}
		}
		return nil
	}

	if !info.IsDir() {
		if err := processFile(target, info); err != nil {
			return nil, err
		}
		return matches, nil
	}

	if !recursive {
		entries, err := os.ReadDir(target)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if len(matches) >= limit {
				break
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			if err := processFile(filepath.Join(target, entry.Name()), info); err != nil {
				return nil, err
			}
		}
		return matches, nil
	}

	walkErr := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if len(matches) >= limit {
			return errors.New("grep limit reached")
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		return processFile(path, info)
	})
	if walkErr != nil && walkErr.Error() != "grep limit reached" {
		return nil, walkErr
	}
	return matches, nil
}

func (m *Manager) List(peerID, relPath string, recursive bool, limit int) ([]Entry, error) {
	target, err := m.resolve(peerID, relPath)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	entries := make([]Entry, 0, 16)
	addEntry := func(path string, info fs.FileInfo) {
		rel, err := filepath.Rel(m.PeerRoot(peerID), path)
		if err != nil {
			rel = info.Name()
		}
		entries = append(entries, Entry{
			Path:      filepath.ToSlash(rel),
			IsDir:     info.IsDir(),
			Size:      info.Size(),
			UpdatedAt: info.ModTime().UTC(),
		})
	}

	if !recursive {
		list, err := os.ReadDir(target)
		if err != nil {
			return nil, err
		}
		for _, item := range list {
			if len(entries) >= limit {
				break
			}
			info, err := item.Info()
			if err != nil {
				continue
			}
			addEntry(filepath.Join(target, item.Name()), info)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		return entries, nil
	}

	walkErr := filepath.WalkDir(target, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == target {
			return nil
		}
		if len(entries) >= limit {
			return errors.New("list limit reached")
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		addEntry(path, info)
		return nil
	})
	if walkErr != nil && walkErr.Error() != "list limit reached" {
		return nil, walkErr
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func (m *Manager) resolve(peerID, relPath string) (string, error) {
	base, err := m.EnsurePeer(peerID)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(relPath)
	if clean == "." {
		return base, nil
	}
	target := filepath.Join(base, clean)
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	if absTarget != base && !strings.HasPrefix(absTarget, base+string(os.PathSeparator)) {
		return "", fmt.Errorf("path escapes workspace")
	}
	return absTarget, nil
}

func sanitizePeerID(peerID string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_", "..", "_").Replace(peerID)
	if strings.TrimSpace(safe) == "" {
		return "default"
	}
	return safe
}

func splitLinesKeepNewline(content string) []string {
	if content == "" {
		return nil
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func compileGrepMatcher(pattern string, caseSensitive, useRegexp bool) (func(string) (int, bool), error) {
	if useRegexp {
		expr := pattern
		if !caseSensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid pattern: %w", err)
		}
		return func(line string) (int, bool) {
			loc := re.FindStringIndex(line)
			if loc == nil {
				return 0, false
			}
			return loc[0] + 1, true
		}, nil
	}

	needle := pattern
	if !caseSensitive {
		needle = strings.ToLower(pattern)
	}
	return func(line string) (int, bool) {
		haystack := line
		if !caseSensitive {
			haystack = strings.ToLower(line)
		}
		idx := strings.Index(haystack, needle)
		if idx < 0 {
			return 0, false
		}
		return idx + 1, true
	}, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
