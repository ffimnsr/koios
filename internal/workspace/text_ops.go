package workspace

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// TextResult describes a read-only text transformation result for a workspace file.
type TextResult struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	LineCount  int    `json:"line_count"`
	TotalLines int    `json:"total_lines"`
}

func (m *Manager) Head(peerID, relPath string, lines int) (*ReadResult, error) {
	if lines <= 0 {
		return nil, fmt.Errorf("lines must be >= 1")
	}
	content, err := m.Read(peerID, relPath)
	if err != nil {
		return nil, err
	}
	chunks := splitLinesKeepNewline(content)
	totalLines := len(chunks)
	if totalLines == 0 {
		return &ReadResult{Path: relPath, TotalLines: 0}, nil
	}
	if lines > totalLines {
		lines = totalLines
	}
	return &ReadResult{
		Path:       relPath,
		Content:    strings.Join(chunks[:lines], ""),
		StartLine:  1,
		EndLine:    lines,
		TotalLines: totalLines,
	}, nil
}

func (m *Manager) Tail(peerID, relPath string, lines int) (*ReadResult, error) {
	if lines <= 0 {
		return nil, fmt.Errorf("lines must be >= 1")
	}
	content, err := m.Read(peerID, relPath)
	if err != nil {
		return nil, err
	}
	chunks := splitLinesKeepNewline(content)
	totalLines := len(chunks)
	if totalLines == 0 {
		return &ReadResult{Path: relPath, TotalLines: 0}, nil
	}
	if lines > totalLines {
		lines = totalLines
	}
	start := totalLines - lines
	return &ReadResult{
		Path:       relPath,
		Content:    strings.Join(chunks[start:], ""),
		StartLine:  start + 1,
		EndLine:    totalLines,
		TotalLines: totalLines,
	}, nil
}

func (m *Manager) SortLines(peerID, relPath string, reverse, caseSensitive bool) (*TextResult, error) {
	content, err := m.Read(peerID, relPath)
	if err != nil {
		return nil, err
	}
	lines, trailing := splitContentLines(content)
	sorted := append([]string(nil), lines...)
	sort.SliceStable(sorted, func(i, j int) bool {
		left := sorted[i]
		right := sorted[j]
		if !caseSensitive {
			left = strings.ToLower(left)
			right = strings.ToLower(right)
		}
		if reverse {
			return left > right
		}
		return left < right
	})
	return &TextResult{
		Path:       relPath,
		Content:    joinContentLines(sorted, trailing),
		LineCount:  len(sorted),
		TotalLines: len(lines),
	}, nil
}

func (m *Manager) UniqLines(peerID, relPath string, count, caseSensitive bool) (*TextResult, error) {
	content, err := m.Read(peerID, relPath)
	if err != nil {
		return nil, err
	}
	lines, trailing := splitContentLines(content)
	if len(lines) == 0 {
		return &TextResult{Path: relPath}, nil
	}
	out := make([]string, 0, len(lines))
	current := lines[0]
	runCount := 1
	flush := func() {
		if count {
			out = append(out, strconv.Itoa(runCount)+" "+current)
			return
		}
		out = append(out, current)
	}
	for i := 1; i < len(lines); i++ {
		if uniqEqual(current, lines[i], caseSensitive) {
			runCount++
			continue
		}
		flush()
		current = lines[i]
		runCount = 1
	}
	flush()
	return &TextResult{
		Path:       relPath,
		Content:    joinContentLines(out, trailing),
		LineCount:  len(out),
		TotalLines: len(lines),
	}, nil
}

func uniqEqual(left, right string, caseSensitive bool) bool {
	if caseSensitive {
		return left == right
	}
	return strings.EqualFold(left, right)
}
