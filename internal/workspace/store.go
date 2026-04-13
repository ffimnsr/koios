package workspace

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	target, err := m.resolve(peerID, relPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory")
	}
	if info.Size() > int64(m.maxFileBytes) {
		return "", fmt.Errorf("file exceeds max bytes %d", m.maxFileBytes)
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	return string(b), nil
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
