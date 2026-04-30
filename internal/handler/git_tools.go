package handler

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

const maxGitPatchBytes = 256 * 1024

type gitRepoContext struct {
	peerRoot    string
	repoRootAbs string
	repoRootRel string
}

func (h *Handler) runGitStatusTool(ctx context.Context, peerID, repoPath string) (map[string]any, error) {
	repo, err := h.resolveGitRepo(ctx, peerID, repoPath)
	if err != nil {
		return nil, err
	}
	out, err := runGitCommand(ctx, repo.repoRootAbs, "git status", nil, "status", "--porcelain=1", "--branch")
	if err != nil {
		return nil, err
	}
	return parseGitStatus(repo.repoRootRel, out)
}

func (h *Handler) runGitDiffTool(ctx context.Context, peerID, repoPath, path, base, head string, staged bool, contextLines int) (map[string]any, error) {
	repo, err := h.resolveGitRepo(ctx, peerID, repoPath)
	if err != nil {
		return nil, err
	}
	if contextLines <= 0 {
		contextLines = 3
	}
	args := []string{"diff", "--no-ext-diff", fmt.Sprintf("--unified=%d", contextLines)}
	if staged {
		args = append(args, "--cached")
	}
	if strings.TrimSpace(base) != "" && strings.TrimSpace(head) != "" {
		args = append(args, strings.TrimSpace(base), strings.TrimSpace(head))
	} else if strings.TrimSpace(base) != "" {
		args = append(args, strings.TrimSpace(base))
	}
	if strings.TrimSpace(path) != "" {
		args = append(args, "--", strings.TrimSpace(path))
	}
	out, err := runGitCommand(ctx, repo.repoRootAbs, "git diff", nil, args...)
	if err != nil {
		return nil, err
	}
	files := extractPatchedFiles(out)
	return map[string]any{
		"repo_root": repo.repoRootRel,
		"staged":    staged,
		"context":   contextLines,
		"has_diff":  strings.TrimSpace(out) != "",
		"diff":      out,
		"count":     len(files),
		"files":     files,
	}, nil
}

func (h *Handler) runGitLogTool(ctx context.Context, peerID, repoPath, ref, path string, limit int) (map[string]any, error) {
	repo, err := h.resolveGitRepo(ctx, peerID, repoPath)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	format := "%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%cn%x1f%ce%x1f%cI%x1f%s%x1e"
	args := []string{"log", fmt.Sprintf("-n%d", limit), "--format=" + format}
	if strings.TrimSpace(ref) != "" {
		args = append(args, strings.TrimSpace(ref))
	}
	if strings.TrimSpace(path) != "" {
		args = append(args, "--", strings.TrimSpace(path))
	}
	out, err := runGitCommand(ctx, repo.repoRootAbs, "git log", nil, args...)
	if err != nil {
		return nil, err
	}
	commits := parseGitLog(out)
	return map[string]any{
		"repo_root": repo.repoRootRel,
		"count":     len(commits),
		"commits":   commits,
	}, nil
}

func (h *Handler) runGitBranchTool(ctx context.Context, peerID, repoPath, action, name, target, startPoint string, all, force bool) (map[string]any, error) {
	repo, err := h.resolveGitRepo(ctx, peerID, repoPath)
	if err != nil {
		return nil, err
	}
	action = strings.ToLower(strings.TrimSpace(action))
	if action == "" {
		action = "list"
	}
	switch action {
	case "list":
		branches, current, detached, err := listGitBranches(ctx, repo.repoRootAbs, all)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"repo_root": repo.repoRootRel,
			"current":   current,
			"detached":  detached,
			"count":     len(branches),
			"branches":  branches,
		}, nil
	case "create":
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("name is required")
		}
		args := []string{"branch"}
		if force {
			args = append(args, "-f")
		}
		args = append(args, name)
		if strings.TrimSpace(startPoint) != "" {
			args = append(args, strings.TrimSpace(startPoint))
		}
		if _, err := runGitCommand(ctx, repo.repoRootAbs, "git branch create", nil, args...); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "action": action, "repo_root": repo.repoRootRel, "branch": name}, nil
	case "switch":
		target = strings.TrimSpace(target)
		if target == "" {
			target = strings.TrimSpace(name)
		}
		if target == "" {
			return nil, fmt.Errorf("target is required")
		}
		args := []string{"checkout"}
		if force {
			args = append(args, "--force")
		}
		args = append(args, target)
		if _, err := runGitCommand(ctx, repo.repoRootAbs, "git branch switch", nil, args...); err != nil {
			return nil, err
		}
		current, detached, err := gitCurrentBranch(ctx, repo.repoRootAbs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "action": action, "repo_root": repo.repoRootRel, "current": current, "detached": detached}, nil
	case "delete":
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, fmt.Errorf("name is required")
		}
		flag := "-d"
		if force {
			flag = "-D"
		}
		if _, err := runGitCommand(ctx, repo.repoRootAbs, "git branch delete", nil, "branch", flag, name); err != nil {
			return nil, err
		}
		return map[string]any{"ok": true, "action": action, "repo_root": repo.repoRootRel, "branch": name}, nil
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
}

func (h *Handler) runGitCommitTool(ctx context.Context, peerID, repoPath, message string, all bool) (map[string]any, error) {
	repo, err := h.resolveGitRepo(ctx, peerID, repoPath)
	if err != nil {
		return nil, err
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	args := []string{"commit", "--message", message}
	if all {
		args = append(args, "--all")
	}
	if _, err := runGitCommand(ctx, repo.repoRootAbs, "git commit", nil, args...); err != nil {
		return nil, err
	}
	commit, err := gitHeadCommit(ctx, repo.repoRootAbs)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":        true,
		"repo_root": repo.repoRootRel,
		"commit":    commit,
	}, nil
}

func (h *Handler) runGitApplyPatchTool(ctx context.Context, peerID, repoPath, patch string, checkOnly, index bool) (map[string]any, error) {
	repo, err := h.resolveGitRepo(ctx, peerID, repoPath)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(patch) == "" {
		return nil, fmt.Errorf("patch is required")
	}
	if len(patch) > maxGitPatchBytes {
		return nil, fmt.Errorf("patch exceeds %d byte limit", maxGitPatchBytes)
	}
	checkArgs := []string{"apply", "--check", "-"}
	if index {
		checkArgs = []string{"apply", "--check", "--index", "-"}
	}
	if _, err := runGitCommand(ctx, repo.repoRootAbs, "git apply --check", []byte(patch), checkArgs...); err != nil {
		return nil, err
	}
	files := extractPatchedFiles(patch)
	if checkOnly {
		return map[string]any{
			"ok":         true,
			"repo_root":  repo.repoRootRel,
			"check_only": true,
			"applicable": true,
			"count":      len(files),
			"files":      files,
		}, nil
	}
	applyArgs := []string{"apply"}
	if index {
		applyArgs = append(applyArgs, "--index")
	}
	applyArgs = append(applyArgs, "-")
	if _, err := runGitCommand(ctx, repo.repoRootAbs, "git apply", []byte(patch), applyArgs...); err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":         true,
		"repo_root":  repo.repoRootRel,
		"check_only": false,
		"applied":    true,
		"index":      index,
		"count":      len(files),
		"files":      files,
	}, nil
}

func (h *Handler) resolveGitRepo(ctx context.Context, peerID, repoPath string) (*gitRepoContext, error) {
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("git tools are not enabled")
	}
	peerRoot, err := h.workspaceStore.EnsurePeer(peerID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(repoPath) == "" {
		repoPath = "."
	}
	target, err := h.workspaceStore.Resolve(peerID, repoPath)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(target)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		target = filepath.Dir(target)
	}
	out, err := runGitCommand(ctx, target, "git rev-parse", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("path %q is not inside a git repository: %w", repoPath, err)
	}
	repoRootAbs := filepath.Clean(strings.TrimSpace(out))
	if repoRootAbs == "" {
		return nil, fmt.Errorf("could not determine repository root")
	}
	rel, err := filepath.Rel(peerRoot, repoRootAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, fmt.Errorf("repository root escapes workspace boundary")
	}
	if rel == "" {
		rel = "."
	}
	return &gitRepoContext{peerRoot: peerRoot, repoRootAbs: repoRootAbs, repoRootRel: filepath.ToSlash(rel)}, nil
}

func runGitCommand(ctx context.Context, cwd, display string, stdin []byte, args ...string) (string, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return "", fmt.Errorf("git is not installed")
	}
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s failed: %s", display, msg)
	}
	return stdout.String(), nil
}

func parseGitStatus(repoRoot, out string) (map[string]any, error) {
	branch := ""
	head := ""
	upstream := ""
	ahead := 0
	behind := 0
	detached := false
	entries := make([]map[string]any, 0)
	stagedCount := 0
	unstagedCount := 0
	untrackedCount := 0
	ignoredCount := 0
	for _, raw := range strings.Split(strings.ReplaceAll(out, "\r\n", "\n"), "\n") {
		line := strings.TrimRight(raw, "\r")
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "## ") {
			head, upstream, ahead, behind, detached = parseGitBranchSummary(strings.TrimPrefix(line, "## "))
			if !detached {
				branch = head
			}
			continue
		}
		if len(line) < 3 {
			continue
		}
		status := line[:2]
		rest := strings.TrimSpace(line[3:])
		entry := map[string]any{
			"path":            rest,
			"status":          status,
			"index_status":    string(status[0]),
			"worktree_status": string(status[1]),
		}
		if status == "??" {
			entry["untracked"] = true
			untrackedCount++
		} else if status == "!!" {
			entry["ignored"] = true
			ignoredCount++
		} else {
			staged := status[0] != ' ' && status[0] != '?'
			unstaged := status[1] != ' '
			if staged {
				stagedCount++
			}
			if unstaged {
				unstagedCount++
			}
			entry["staged"] = staged
			entry["unstaged"] = unstaged
		}
		if before, after, ok := strings.Cut(rest, " -> "); ok {
			entry["old_path"] = before
			entry["path"] = after
			entry["renamed"] = true
		}
		entries = append(entries, entry)
	}
	return map[string]any{
		"repo_root":       repoRoot,
		"branch":          branch,
		"head":            head,
		"upstream":        upstream,
		"ahead":           ahead,
		"behind":          behind,
		"detached":        detached,
		"clean":           len(entries) == 0,
		"count":           len(entries),
		"entries":         entries,
		"staged_count":    stagedCount,
		"unstaged_count":  unstagedCount,
		"untracked_count": untrackedCount,
		"ignored_count":   ignoredCount,
	}, nil
}

func parseGitBranchSummary(summary string) (branch string, upstream string, ahead int, behind int, detached bool) {
	headPart := summary
	statusPart := ""
	if idx := strings.Index(summary, "["); idx >= 0 {
		headPart = strings.TrimSpace(summary[:idx])
		statusPart = strings.TrimSuffix(strings.TrimSpace(summary[idx+1:]), "]")
	}
	if idx := strings.Index(headPart, "..."); idx >= 0 {
		branch = strings.TrimSpace(headPart[:idx])
		upstream = strings.TrimSpace(headPart[idx+3:])
	} else {
		branch = strings.TrimSpace(headPart)
	}
	if branch == "HEAD" || strings.HasPrefix(branch, "HEAD ") {
		detached = true
	}
	for _, item := range strings.Split(statusPart, ",") {
		part := strings.TrimSpace(item)
		if strings.HasPrefix(part, "ahead ") {
			ahead, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(part, "ahead ")))
		}
		if strings.HasPrefix(part, "behind ") {
			behind, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(part, "behind ")))
		}
	}
	return branch, upstream, ahead, behind, detached
}

func parseGitLog(out string) []map[string]any {
	items := make([]map[string]any, 0)
	for _, raw := range strings.Split(out, "\x1e") {
		record := strings.TrimSpace(raw)
		if record == "" {
			continue
		}
		parts := strings.Split(record, "\x1f")
		if len(parts) < 9 {
			continue
		}
		items = append(items, map[string]any{
			"hash":            parts[0],
			"short_hash":      parts[1],
			"author_name":     parts[2],
			"author_email":    parts[3],
			"authored_at":     parts[4],
			"committer_name":  parts[5],
			"committer_email": parts[6],
			"committed_at":    parts[7],
			"subject":         parts[8],
		})
	}
	return items
}

func listGitBranches(ctx context.Context, repoRoot string, all bool) ([]map[string]any, string, bool, error) {
	current, detached, err := gitCurrentBranch(ctx, repoRoot)
	if err != nil {
		return nil, "", false, err
	}
	args := []string{"for-each-ref", "--format=%(refname:short)%x1f%(upstream:short)%x1f%(objectname)", "refs/heads"}
	if all {
		args = append(args, "refs/remotes")
	}
	out, err := runGitCommand(ctx, repoRoot, "git branch list", nil, args...)
	if err != nil {
		return nil, "", false, err
	}
	branches := make([]map[string]any, 0)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "\x1f")
		if len(parts) < 3 {
			continue
		}
		name := parts[0]
		branches = append(branches, map[string]any{
			"name":     name,
			"upstream": parts[1],
			"head":     parts[2],
			"current":  name == current,
			"remote":   strings.HasPrefix(name, "origin/") || strings.HasPrefix(name, "remotes/"),
		})
	}
	return branches, current, detached, nil
}

func gitCurrentBranch(ctx context.Context, repoRoot string) (string, bool, error) {
	out, err := runGitCommand(ctx, repoRoot, "git branch current", nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err == nil {
		return strings.TrimSpace(out), false, nil
	}
	if strings.Contains(err.Error(), "ref HEAD is not a symbolic ref") {
		return "", true, nil
	}
	return "", false, err
}

func gitHeadCommit(ctx context.Context, repoRoot string) (map[string]any, error) {
	out, err := runGitCommand(ctx, repoRoot, "git log -1", nil, "log", "-1", "--format=%H%x1f%h%x1f%s")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSpace(out), "\x1f")
	if len(parts) < 3 {
		return nil, fmt.Errorf("could not parse HEAD commit")
	}
	return map[string]any{
		"hash":       parts[0],
		"short_hash": parts[1],
		"subject":    parts[2],
	}, nil
}

func extractPatchedFiles(patch string) []string {
	seen := make(map[string]struct{})
	files := make([]string, 0)
	appendPath := func(path string) {
		path = strings.TrimSpace(path)
		path = strings.TrimPrefix(path, "a/")
		path = strings.TrimPrefix(path, "b/")
		if path == "" || path == "/dev/null" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		files = append(files, path)
	}
	for _, line := range strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				appendPath(parts[3])
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			appendPath(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")))
		}
	}
	return files
}
