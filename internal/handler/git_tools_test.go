package handler_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/workspace"
)

func TestToolDefinitionsIncludeGitTools(t *testing.T) {
	ensureGitAvailable(t)
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})

	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	for _, want := range []string{"git.status", "git.diff", "git.log", "git.branch", "git.commit", "git.apply_patch"} {
		if !containsString(names, want) {
			t.Fatalf("expected %s in tool definitions, got %#v", want, names)
		}
	}
}

func TestExecuteTool_GitStatusDiffLog(t *testing.T) {
	ensureGitAvailable(t)
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	repoRoot := initPeerGitRepo(t, wsStore, "alice")
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		ToolPolicy:     handler.ToolPolicy{Profile: "coding"},
	})

	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatalf("write modified readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "new.txt"), []byte("untracked\n"), 0o644); err != nil {
		t.Fatalf("write untracked file: %v", err)
	}

	statusAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.status", Arguments: json.RawMessage(`{"repo_path":"."}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(git.status): %v", err)
	}
	statusRaw, _ := json.Marshal(statusAny)
	var status struct {
		Branch  string `json:"branch"`
		Count   int    `json:"count"`
		Entries []struct {
			Path      string `json:"path"`
			Untracked bool   `json:"untracked"`
			Unstaged  bool   `json:"unstaged"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		t.Fatalf("unmarshal git.status result: %v", err)
	}
	if status.Branch == "" || status.Count != 2 {
		t.Fatalf("unexpected git.status result: %s", string(statusRaw))
	}
	if !hasGitStatusEntry(status.Entries, "README.md", false, true) {
		t.Fatalf("expected unstaged README.md entry, got %s", string(statusRaw))
	}
	if !hasGitStatusEntry(status.Entries, "new.txt", true, false) {
		t.Fatalf("expected untracked new.txt entry, got %s", string(statusRaw))
	}

	diffAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.diff", Arguments: json.RawMessage(`{"repo_path":".","path":"README.md"}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(git.diff): %v", err)
	}
	diffRaw, _ := json.Marshal(diffAny)
	var diff struct {
		HasDiff bool   `json:"has_diff"`
		Diff    string `json:"diff"`
	}
	if err := json.Unmarshal(diffRaw, &diff); err != nil {
		t.Fatalf("unmarshal git.diff result: %v", err)
	}
	if !diff.HasDiff || !strings.Contains(diff.Diff, "+world") {
		t.Fatalf("unexpected git.diff result: %s", string(diffRaw))
	}

	logAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.log", Arguments: json.RawMessage(`{"repo_path":".","limit":1}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(git.log): %v", err)
	}
	logRaw, _ := json.Marshal(logAny)
	var logRes struct {
		Count   int `json:"count"`
		Commits []struct {
			Subject string `json:"subject"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(logRaw, &logRes); err != nil {
		t.Fatalf("unmarshal git.log result: %v", err)
	}
	if logRes.Count != 1 || len(logRes.Commits) != 1 || logRes.Commits[0].Subject != "initial commit" {
		t.Fatalf("unexpected git.log result: %s", string(logRaw))
	}
}

func TestExecuteTool_GitBranchCommitApplyPatch(t *testing.T) {
	ensureGitAvailable(t)
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	repoRoot := initPeerGitRepo(t, wsStore, "alice")
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		ToolPolicy:     handler.ToolPolicy{Profile: "coding"},
	})

	if _, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.branch", Arguments: json.RawMessage(`{"repo_path":".","action":"create","name":"feature"}`)}); err != nil {
		t.Fatalf("ExecuteTool(git.branch create): %v", err)
	}
	switchedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.branch", Arguments: json.RawMessage(`{"repo_path":".","action":"switch","target":"feature"}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(git.branch switch): %v", err)
	}
	switchedRaw, _ := json.Marshal(switchedAny)
	if !strings.Contains(string(switchedRaw), `"current":"feature"`) {
		t.Fatalf("unexpected git.branch switch result: %s", string(switchedRaw))
	}

	patch := "diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1,2 @@\n hello\n+feature\n"
	checkedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.apply_patch", Arguments: json.RawMessage(`{"repo_path":".","patch":"diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1,2 @@\n hello\n+feature\n","check_only":true}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(git.apply_patch check): %v", err)
	}
	checkedRaw, _ := json.Marshal(checkedAny)
	if !strings.Contains(string(checkedRaw), `"applicable":true`) {
		t.Fatalf("unexpected git.apply_patch check result: %s", string(checkedRaw))
	}
	if _, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.apply_patch", Arguments: json.RawMessage(`{"repo_path":".","patch":"diff --git a/README.md b/README.md\n--- a/README.md\n+++ b/README.md\n@@ -1 +1,2 @@\n hello\n+feature\n"}`)}); err != nil {
		t.Fatalf("ExecuteTool(git.apply_patch): %v", err)
	}

	content, err := os.ReadFile(filepath.Join(repoRoot, "README.md"))
	if err != nil {
		t.Fatalf("read patched readme: %v", err)
	}
	if string(content) != "hello\nfeature\n" {
		t.Fatalf("unexpected patched content: %q", string(content))
	}

	commitAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.commit", Arguments: json.RawMessage(`{"repo_path":".","message":"feature change","all":true}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(git.commit): %v", err)
	}
	commitRaw, _ := json.Marshal(commitAny)
	if !strings.Contains(string(commitRaw), `"subject":"feature change"`) {
		t.Fatalf("unexpected git.commit result: %s", string(commitRaw))
	}

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.branch", Arguments: json.RawMessage(`{"repo_path":".","action":"list"}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(git.branch list): %v", err)
	}
	listRaw, _ := json.Marshal(listAny)
	if !strings.Contains(string(listRaw), `"current":"feature"`) {
		t.Fatalf("unexpected git.branch list result: %s", string(listRaw))
	}

	_ = patch
}

func TestExecuteTool_GitRejectsRepoOutsidePeerWorkspace(t *testing.T) {
	ensureGitAvailable(t)
	baseDir := t.TempDir()
	runGitOrFail(t, baseDir, "init")
	wsStore, err := workspace.New(filepath.Join(baseDir, "workspace"), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		ToolPolicy:     handler.ToolPolicy{Profile: "coding"},
	})

	_, err = h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "git.status", Arguments: json.RawMessage(`{"repo_path":"."}`)})
	if err == nil {
		t.Fatal("expected git.status to reject repository root outside peer workspace")
	}
	if !strings.Contains(err.Error(), "workspace boundary") {
		t.Fatalf("expected workspace boundary error, got %v", err)
	}
}

func ensureGitAvailable(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func initPeerGitRepo(t *testing.T, wsStore *workspace.Manager, peerID string) string {
	t.Helper()
	repoRoot, err := wsStore.EnsurePeer(peerID)
	if err != nil {
		t.Fatalf("EnsurePeer: %v", err)
	}
	runGitOrFail(t, repoRoot, "init")
	runGitOrFail(t, repoRoot, "config", "user.name", "Koios Test")
	runGitOrFail(t, repoRoot, "config", "user.email", "koios@example.test")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatalf("write initial readme: %v", err)
	}
	runGitOrFail(t, repoRoot, "add", "README.md")
	runGitOrFail(t, repoRoot, "commit", "-m", "initial commit")
	return repoRoot
}

func runGitOrFail(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "LC_ALL=C", "LANG=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, string(out))
	}
	return strings.TrimSpace(string(out))
}

func hasGitStatusEntry(entries []struct {
	Path      string `json:"path"`
	Untracked bool   `json:"untracked"`
	Unstaged  bool   `json:"unstaged"`
}, path string, untracked, unstaged bool) bool {
	for _, entry := range entries {
		if entry.Path == path && entry.Untracked == untracked && entry.Unstaged == unstaged {
			return true
		}
	}
	return false
}
