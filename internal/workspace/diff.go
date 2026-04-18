package workspace

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

// DiffResult describes a unified diff between two workspace texts.
type DiffResult struct {
	Path      string `json:"path"`
	OtherPath string `json:"other_path,omitempty"`
	HasDiff   bool   `json:"has_diff"`
	Diff      string `json:"diff"`
}

// Diff compares one workspace file against another workspace file or against
// an in-memory content string and returns a unified diff.
func (m *Manager) Diff(peerID, path, otherPath, content string, contextLines int) (*DiffResult, error) {
	path = strings.TrimSpace(path)
	otherPath = strings.TrimSpace(otherPath)
	if path == "" {
		return nil, fmt.Errorf("path is required")
	}
	if otherPath == "" && content == "" {
		return nil, fmt.Errorf("other_path or content is required")
	}
	if otherPath != "" && content != "" {
		return nil, fmt.Errorf("only one of other_path or content may be set")
	}
	if contextLines < 0 {
		return nil, fmt.Errorf("context must be >= 0")
	}
	left, err := m.Read(peerID, path)
	if err != nil {
		return nil, err
	}

	right := content
	toFile := "proposed"
	if otherPath != "" {
		right, err = m.Read(peerID, otherPath)
		if err != nil {
			return nil, err
		}
		toFile = otherPath
	}

	diffText, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(left),
		B:        difflib.SplitLines(right),
		FromFile: path,
		ToFile:   toFile,
		Context:  contextLines,
	})
	if err != nil {
		return nil, fmt.Errorf("render diff: %w", err)
	}
	return &DiffResult{
		Path:      path,
		OtherPath: otherPath,
		HasDiff:   left != right,
		Diff:      diffText,
	}, nil
}
