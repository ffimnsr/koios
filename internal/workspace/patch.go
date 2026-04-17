package workspace

import (
	"fmt"
	"os"
	"strings"
)

// PatchFileResult describes one file touched by ApplyPatch.
type PatchFileResult struct {
	Path    string `json:"path"`
	Action  string `json:"action"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Bytes   int    `json:"bytes"`
	NewPath string `json:"new_path,omitempty"`
}

// PatchResult summarizes a patch application.
type PatchResult struct {
	Count int               `json:"count"`
	Files []PatchFileResult `json:"files"`
}

type patchOpKind string

const (
	patchOpAdd    patchOpKind = "add"
	patchOpDelete patchOpKind = "delete"
	patchOpUpdate patchOpKind = "update"
)

type patchOp struct {
	kind     patchOpKind
	path     string
	moveTo   string
	addLines []string
	hunks    []patchHunk
}

type patchHunk struct {
	lines []patchLine
}

type patchLine struct {
	kind byte
	text string
}

// ApplyPatch applies a multi-file patch expressed in the repository's patch
// format. Update hunks are matched against existing file content before any
// write occurs.
func (m *Manager) ApplyPatch(peerID, patchText string) (*PatchResult, error) {
	ops, err := parsePatch(patchText)
	if err != nil {
		return nil, err
	}
	if len(ops) == 0 {
		return nil, fmt.Errorf("patch contains no file changes")
	}

	result := &PatchResult{
		Count: len(ops),
		Files: make([]PatchFileResult, 0, len(ops)),
	}
	for _, op := range ops {
		switch op.kind {
		case patchOpAdd:
			if err := m.applyAdd(peerID, op, result); err != nil {
				return nil, err
			}
		case patchOpDelete:
			if err := m.applyDelete(peerID, op, result); err != nil {
				return nil, err
			}
		case patchOpUpdate:
			if err := m.applyUpdate(peerID, op, result); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported patch operation %q", op.kind)
		}
	}
	return result, nil
}

func (m *Manager) applyAdd(peerID string, op patchOp, result *PatchResult) error {
	content := joinContentLines(op.addLines, len(op.addLines) > 0)
	target, err := m.resolve(peerID, op.path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("path already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := m.Write(peerID, op.path, content, false); err != nil {
		return err
	}
	result.Files = append(result.Files, PatchFileResult{
		Path:    op.path,
		Action:  "add",
		Added:   len(op.addLines),
		Removed: 0,
		Bytes:   len(content),
	})
	return nil
}

func (m *Manager) applyDelete(peerID string, op patchOp, result *PatchResult) error {
	content, err := m.Read(peerID, op.path)
	if err != nil {
		return err
	}
	lines, _ := splitContentLines(content)
	if err := m.Delete(peerID, op.path, false); err != nil {
		return err
	}
	result.Files = append(result.Files, PatchFileResult{
		Path:    op.path,
		Action:  "delete",
		Added:   0,
		Removed: len(lines),
		Bytes:   0,
	})
	return nil
}

func (m *Manager) applyUpdate(peerID string, op patchOp, result *PatchResult) error {
	content, err := m.Read(peerID, op.path)
	if err != nil {
		return err
	}
	lines, trailing := splitContentLines(content)
	updatedLines, added, removed, err := applyPatchHunks(lines, op.hunks)
	if err != nil {
		return fmt.Errorf("apply patch to %s: %w", op.path, err)
	}
	updatedContent := joinContentLines(updatedLines, trailing)
	if len(updatedContent) > m.maxFileBytes {
		return fmt.Errorf("patched content exceeds max bytes %d", m.maxFileBytes)
	}

	action := "update"
	targetPath := op.path
	if strings.TrimSpace(op.moveTo) != "" && op.moveTo != op.path {
		action = "move"
		targetPath = op.moveTo
		destAbs, err := m.resolve(peerID, targetPath)
		if err != nil {
			return err
		}
		if _, err := os.Stat(destAbs); err == nil {
			return fmt.Errorf("destination path already exists")
		} else if !os.IsNotExist(err) {
			return err
		}
		if _, err := m.Write(peerID, targetPath, updatedContent, false); err != nil {
			return err
		}
		if err := m.Delete(peerID, op.path, false); err != nil {
			_ = m.Delete(peerID, targetPath, false)
			return err
		}
	} else {
		if _, err := m.Write(peerID, op.path, updatedContent, false); err != nil {
			return err
		}
	}

	result.Files = append(result.Files, PatchFileResult{
		Path:    targetPath,
		Action:  action,
		Added:   added,
		Removed: removed,
		Bytes:   len(updatedContent),
		NewPath: op.moveTo,
	})
	return nil
}

func parsePatch(patchText string) ([]patchOp, error) {
	patchText = strings.ReplaceAll(patchText, "\r\n", "\n")
	lines := strings.Split(patchText, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return nil, fmt.Errorf("patch must start with *** Begin Patch")
	}

	var ops []patchOp
	for i := 1; i < len(lines); {
		line := lines[i]
		switch {
		case line == "*** End Patch":
			return ops, nil
		case line == "":
			i++
		case strings.HasPrefix(line, "*** Add File: "):
			op := patchOp{
				kind: patchOpAdd,
				path: strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: ")),
			}
			if op.path == "" {
				return nil, fmt.Errorf("add file path is required")
			}
			i++
			for i < len(lines) {
				line = lines[i]
				if line == "*** End Patch" || strings.HasPrefix(line, "*** ") {
					break
				}
				if line == "" || line[0] != '+' {
					return nil, fmt.Errorf("add file lines must start with +")
				}
				op.addLines = append(op.addLines, line[1:])
				i++
			}
			ops = append(ops, op)
		case strings.HasPrefix(line, "*** Delete File: "):
			path := strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			if path == "" {
				return nil, fmt.Errorf("delete file path is required")
			}
			ops = append(ops, patchOp{kind: patchOpDelete, path: path})
			i++
		case strings.HasPrefix(line, "*** Update File: "):
			op := patchOp{
				kind: patchOpUpdate,
				path: strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: ")),
			}
			if op.path == "" {
				return nil, fmt.Errorf("update file path is required")
			}
			i++
			for i < len(lines) {
				line = lines[i]
				if line == "*** End Patch" || strings.HasPrefix(line, "*** Add File: ") || strings.HasPrefix(line, "*** Delete File: ") || strings.HasPrefix(line, "*** Update File: ") {
					break
				}
				if strings.HasPrefix(line, "*** Move to: ") {
					op.moveTo = strings.TrimSpace(strings.TrimPrefix(line, "*** Move to: "))
					i++
					continue
				}
				if strings.HasPrefix(line, "@@") {
					op.hunks = append(op.hunks, patchHunk{})
					i++
					continue
				}
				if len(op.hunks) == 0 {
					return nil, fmt.Errorf("update file requires a @@ hunk header")
				}
				if line == "" {
					return nil, fmt.Errorf("invalid empty patch line in update hunk")
				}
				switch line[0] {
				case ' ', '+', '-':
					h := &op.hunks[len(op.hunks)-1]
					h.lines = append(h.lines, patchLine{kind: line[0], text: line[1:]})
				default:
					return nil, fmt.Errorf("invalid patch line prefix %q", line[0])
				}
				i++
			}
			if len(op.hunks) == 0 {
				return nil, fmt.Errorf("update file must contain at least one hunk")
			}
			ops = append(ops, op)
		default:
			return nil, fmt.Errorf("unexpected patch directive %q", line)
		}
	}
	return nil, fmt.Errorf("patch missing *** End Patch")
}

func applyPatchHunks(lines []string, hunks []patchHunk) ([]string, int, int, error) {
	updated := append([]string(nil), lines...)
	cursor := 0
	added := 0
	removed := 0

	for _, hunk := range hunks {
		oldSeq := make([]string, 0, len(hunk.lines))
		newSeq := make([]string, 0, len(hunk.lines))
		for _, line := range hunk.lines {
			switch line.kind {
			case ' ':
				oldSeq = append(oldSeq, line.text)
				newSeq = append(newSeq, line.text)
			case '-':
				oldSeq = append(oldSeq, line.text)
				removed++
			case '+':
				newSeq = append(newSeq, line.text)
				added++
			default:
				return nil, 0, 0, fmt.Errorf("unsupported patch line prefix %q", line.kind)
			}
		}

		idx := cursor
		if len(oldSeq) > 0 {
			start := cursor - len(oldSeq)
			if start < 0 {
				start = 0
			}
			idx = indexOfSubslice(updated, oldSeq, start)
			if idx < 0 {
				return nil, 0, 0, fmt.Errorf("hunk context not found")
			}
		}

		updated = append(updated[:idx], append(newSeq, updated[idx+len(oldSeq):]...)...)
		cursor = idx + len(newSeq)
	}

	return updated, added, removed, nil
}

func indexOfSubslice(lines, sub []string, start int) int {
	if len(sub) == 0 {
		if start < 0 {
			return 0
		}
		if start > len(lines) {
			return len(lines)
		}
		return start
	}
	if start < 0 {
		start = 0
	}
	for i := start; i+len(sub) <= len(lines); i++ {
		match := true
		for j := range sub {
			if lines[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func splitContentLines(content string) ([]string, bool) {
	if content == "" {
		return nil, false
	}
	trailingNewline := strings.HasSuffix(content, "\n")
	if trailingNewline {
		content = strings.TrimSuffix(content, "\n")
		if content == "" {
			return []string{""}, true
		}
	}
	return strings.Split(content, "\n"), trailingNewline
}

func joinContentLines(lines []string, trailingNewline bool) string {
	if len(lines) == 0 {
		if trailingNewline {
			return "\n"
		}
		return ""
	}
	content := strings.Join(lines, "\n")
	if trailingNewline {
		content += "\n"
	}
	return content
}
