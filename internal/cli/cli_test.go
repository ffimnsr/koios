package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ffimnsr/koios/internal/app"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/workflow"
	"github.com/ffimnsr/koios/internal/workspace"
)

func TestResolveRepoState(t *testing.T) {
	dir := t.TempDir()
	toml := strings.Join([]string{
		"[server]",
		"listen_addr = \":9090\"",
		"request_timeout = \"45s\"",
		"",
		"[llm]",
		"provider = \"openai\"",
		"model = \"gpt-4o\"",
		"api_key = \"test-key\"",
		"",
		"[workspace]",
		"root = \"./workspace\"",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := resolveRepoState(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !state.ConfigExists {
		t.Fatal("expected config to exist")
	}
	if state.ListenAddr != ":9090" {
		t.Fatalf("listen addr = %q", state.ListenAddr)
	}
	if got, want := state.sessionDir(), filepath.Join(dir, "workspace/sessions"); got != want {
		t.Fatalf("session dir = %q want %q", got, want)
	}
	if state.RequestTimeout != 45*time.Second {
		t.Fatalf("request timeout = %s", state.RequestTimeout)
	}
	if got, want := state.memoryDBPath(), filepath.Join(dir, "workspace/db/memory.db"); got != want {
		t.Fatalf("memory db path = %q want %q", got, want)
	}
}

func TestSetupCreatesEnvFromSample(t *testing.T) {
	dir := t.TempDir()
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newInitCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(dir, config.DefaultConfigFile)) {
		t.Fatal("expected koios.config.toml to be created")
	}
	for _, path := range expectedWorkspaceDBPaths(dir) {
		if !fileExists(path) {
			t.Fatalf("expected %s to be created by init", path)
		}
	}
	for _, name := range []string{"AGENTS.md", "SOUL.md", "USER.md", "IDENTITY.md", "BOOTSTRAP.md", "TOOLS.md", "HEARTBEAT.md"} {
		if !fileExists(filepath.Join(dir, "workspace", "peers", workspace.DefaultPeerID, name)) {
			t.Fatalf("expected %s to be created by init", name)
		}
	}
}

func TestInitCanSkipWorkspaceScaffolding(t *testing.T) {
	dir := t.TempDir()
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newInitCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--no-setup"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(dir, config.DefaultConfigFile)) {
		t.Fatal("expected koios.config.toml to be created")
	}
	for _, path := range expectedWorkspaceDBPaths(dir) {
		if !fileExists(path) {
			t.Fatalf("expected %s to be created by init --no-setup", path)
		}
	}
	if fileExists(filepath.Join(dir, "workspace", "peers", workspace.DefaultPeerID, "AGENTS.md")) {
		t.Fatal("expected init --no-setup to skip workspace scaffolding")
	}
}

func TestInitWizardRewritesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(strings.Join([]string{
		"[server]",
		"listen_addr = \":8080\"",
		"request_timeout = \"30s\"",
		"",
		"[llm]",
		"provider = \"openai\"",
		"model = \"old-model\"",
		"api_key = \"old-key\"",
		"",
		"[session]",
		"dir = \"./data/sessions\"",
		"",
		"[cron]",
		"dir = \"./data/cron\"",
		"",
		"[agent]",
		"dir = \"./data/agents\"",
		"",
		"[workspace]",
		"root = \"./workspace\"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newInitCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(strings.Join([]string{
		"new-key",
		"new-model",
		"anthropic",
		":9191",
		"./wizard-workspace",
	}, "\n") + "\n"))
	cmd.SetArgs([]string{"--wizard", "--no-setup"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "llm.provider [openai]:") {
		t.Fatalf("expected provider prompt to show current config value\n%s", out.String())
	}
	for _, unwanted := range []string{"session.dir", "cron.dir", "agent.dir"} {
		if strings.Contains(out.String(), unwanted) {
			t.Fatalf("did not expect %s prompt\n%s", unwanted, out.String())
		}
	}

	data, err := os.ReadFile(filepath.Join(dir, config.DefaultConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"provider = \"anthropic\"",
		"model = \"new-model\"",
		"api_key = \"new-key\"",
		"listen_addr = \":9191\"",
		"root = \"./wizard-workspace\"",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected config to contain %q\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"dir = \"./data/wizard-sessions\"", "dir = \"./data/wizard-cron\"", "dir = \"./data/wizard-agents\""} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("did not expect config to contain %q\n%s", unwanted, content)
		}
	}
}

func TestInitWizardBlankOptionalAPIKeyRetainsExistingValue(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(strings.Join([]string{
		"[server]",
		"listen_addr = \":8080\"",
		"request_timeout = \"30s\"",
		"",
		"[llm]",
		"provider = \"openai\"",
		"model = \"old-model\"",
		"api_key = \"old-key\"",
		"",
		"[session]",
		"dir = \"./data/sessions\"",
		"",
		"[cron]",
		"dir = \"./data/cron\"",
		"",
		"[agent]",
		"dir = \"./data/agents\"",
		"",
		"[workspace]",
		"root = \"./workspace\"",
	}, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newInitCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(strings.Join([]string{
		"",
		"new-model",
		"anthropic",
		":9191",
		"./wizard-workspace",
	}, "\n") + "\n"))
	cmd.SetArgs([]string{"--wizard", "--no-setup"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, config.DefaultConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "api_key = \"old-key\"") {
		t.Fatalf("expected blank api_key prompt to retain existing value\n%s", content)
	}
	if !strings.Contains(content, "model = \"new-model\"") {
		t.Fatalf("expected later wizard prompts to continue after blank api_key\n%s", content)
	}
}

func TestInitWizardNonInteractiveUsesExistingValues(t *testing.T) {
	dir := t.TempDir()
	initial := strings.Join([]string{
		"[server]",
		"listen_addr = \":8087\"",
		"request_timeout = \"30s\"",
		"",
		"[llm]",
		"provider = \"openai\"",
		"model = \"existing-model\"",
		"api_key = \"existing-key\"",
		"",
		"[session]",
		"dir = \"./data/sessions\"",
		"",
		"[cron]",
		"dir = \"./data/cron\"",
		"",
		"[agent]",
		"dir = \"./data/agents\"",
		"",
		"[workspace]",
		"root = \"./workspace\"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newInitCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs([]string{"--wizard", "--non-interactive", "--no-setup"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, config.DefaultConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	for _, want := range []string{
		"provider = \"openai\"",
		"model = \"existing-model\"",
		"api_key = \"existing-key\"",
		"listen_addr = \":8087\"",
		"root = \"./workspace\"",
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("expected config to contain %q\n%s", want, content)
		}
	}
}

func expectedWorkspaceDBPaths(root string) []string {
	return []string{
		filepath.Join(root, "workspace", "db", "memory.db"),
		filepath.Join(root, "workspace", "db", "tasks.db"),
		filepath.Join(root, "workspace", "db", "bookmarks.db"),
		filepath.Join(root, "workspace", "db", "calendar.db"),
		filepath.Join(root, "workspace", "db", "notes.db"),
		filepath.Join(root, "workspace", "db", "plans.db"),
		filepath.Join(root, "workspace", "db", "projects.db"),
		filepath.Join(root, "workspace", "db", "artifacts.db"),
		filepath.Join(root, "workspace", "db", "decisions.db"),
		filepath.Join(root, "workspace", "db", "preferences.db"),
		filepath.Join(root, "workspace", "db", "reminders.db"),
		filepath.Join(root, "workspace", "db", "tool_results.db"),
	}
}

func TestScaffoldWorkspaceCreatesBootstrapDocs(t *testing.T) {
	root := t.TempDir()
	if err := scaffoldWorkspace(root, false); err != nil {
		t.Fatal(err)
	}

	for _, subdir := range []string{"sessions", "cron", "agents", "memory", "db", "peers"} {
		if info, err := os.Stat(filepath.Join(root, subdir)); err != nil || !info.IsDir() {
			t.Fatalf("expected subdir %q to exist, err=%v", subdir, err)
		}
	}
	if info, err := os.Stat(filepath.Join(root, "peers", workspace.DefaultPeerID)); err != nil || !info.IsDir() {
		t.Fatalf("expected default peer subdir to exist, err=%v", err)
	}

	files := []string{
		"AGENTS.md",
		"SOUL.md",
		"USER.md",
		"IDENTITY.md",
		"BOOTSTRAP.md",
		"TOOLS.md",
		"HEARTBEAT.md",
	}
	for _, name := range files {
		data, err := os.ReadFile(filepath.Join(root, "peers", workspace.DefaultPeerID, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Fatalf("%s should not be empty", name)
		}
	}
}

func TestModelListDeduplicatesSelectedDefaultProfile(t *testing.T) {
	dir := t.TempDir()
	toml := strings.Join([]string{
		"[server]",
		"listen_addr = \":8080\"",
		"request_timeout = \"30s\"",
		"",
		"[llm]",
		"default_profile = \"default\"",
		"idle_timeout = \"30s\"",
		"",
		"[[llm.profiles]]",
		"name = \"default\"",
		"provider = \"nvidia\"",
		"model = \"moonshotai/kimi-k2-instruct\"",
		"api_key = \"test-key\"",
		"",
		"[workspace]",
		"root = \"./workspace\"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newModelCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--json", "list"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}

	var payload struct {
		Count        int    `json:"count"`
		DefaultModel string `json:"default_model"`
		Models       []struct {
			Name      string `json:"name"`
			Provider  string `json:"provider"`
			Model     string `json:"model"`
			IsDefault bool   `json:"is_default"`
			Role      string `json:"role"`
		} `json:"models"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal output: %v\n%s", err, out.String())
	}
	if payload.Count != 1 {
		t.Fatalf("count = %d want 1", payload.Count)
	}
	if payload.DefaultModel != "moonshotai/kimi-k2-instruct" {
		t.Fatalf("default_model = %q", payload.DefaultModel)
	}
	if len(payload.Models) != 1 {
		t.Fatalf("models len = %d want 1", len(payload.Models))
	}
	if got := payload.Models[0]; got.Name != "default" || got.Role != "profile (default)" || !got.IsDefault {
		t.Fatalf("unexpected model entry: %+v", got)
	}
}

func TestCompletionSuggestsKnownPeers(t *testing.T) {
	dir := writeCompletionConfig(t, config.Default())
	for _, peer := range []string{"alice", "bob"} {
		if err := os.MkdirAll(filepath.Join(dir, "workspace", "peers", peer), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "workspace", "sessions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "workspace", "sessions", "charlie.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	suggestions := completionSuggestions(t, dir, "tasks", "list", "--peer", "")
	for _, expected := range []string{"alice", "bob", "charlie"} {
		if !containsString(suggestions, expected) {
			t.Fatalf("expected peer completion %q in %v", expected, suggestions)
		}
	}
}

func TestCompletionSuggestsEnumFlags(t *testing.T) {
	dir := writeCompletionConfig(t, config.Default())

	resetScopes := completionSuggestions(t, dir, "reset", "--scope", "")
	for _, expected := range []string{"config", "config+creds+sessions", "full"} {
		if !containsString(resetScopes, expected) {
			t.Fatalf("expected reset scope %q in %v", expected, resetScopes)
		}
	}

	taskStatuses := completionSuggestions(t, dir, "tasks", "list", "--status", "")
	for _, expected := range []string{"open", "snoozed", "completed", "all"} {
		if !containsString(taskStatuses, expected) {
			t.Fatalf("expected task status %q in %v", expected, taskStatuses)
		}
	}

	agendaScopes := completionSuggestions(t, dir, "calendar", "agenda", "--scope", "")
	for _, expected := range []string{"today", "this_week", "next_conflict"} {
		if !containsString(agendaScopes, expected) {
			t.Fatalf("expected agenda scope %q in %v", expected, agendaScopes)
		}
	}
}

func TestCompletionSuggestsCronAndWorkflowIDs(t *testing.T) {
	dir := writeCompletionConfig(t, config.Default())
	cronStore, err := scheduler.NewJobStore(filepath.Join(dir, "workspace", "cron"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cronStore.Add(&scheduler.Job{JobID: "cron-1", PeerID: "alice", Name: "daily sync", Schedule: scheduler.Schedule{Kind: scheduler.KindEvery, EveryMs: 60000}, Payload: scheduler.Payload{Kind: scheduler.PayloadAgentTurn, Message: "sync"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	workflowStore, err := workflow.NewStore(filepath.Join(dir, "workspace", "workflows"))
	if err != nil {
		t.Fatal(err)
	}
	wf, err := workflowStore.Create(workflow.Workflow{PeerID: "alice", Name: "Deploy", Steps: []workflow.Step{{ID: "step-1", Kind: workflow.StepKindAgentTurn, Message: "deploy"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := workflowStore.SaveRun(&workflow.Run{ID: "run-1", WorkflowID: wf.ID, PeerID: "alice", Status: workflow.RunStatusRunning}); err != nil {
		t.Fatal(err)
	}

	cronIDs := completionSuggestions(t, dir, "cron", "delete", "--peer", "alice", "")
	if !containsString(cronIDs, "cron-1") {
		t.Fatalf("expected cron completion in %v", cronIDs)
	}
	workflowRunIDs := completionSuggestions(t, dir, "workflow", "status", "--peer", "alice", "")
	if !containsString(workflowRunIDs, "run-1") {
		t.Fatalf("expected workflow run completion in %v", workflowRunIDs)
	}
}

func TestCompletionSuggestsTaskCalendarAndModelValues(t *testing.T) {
	cfg := config.Default()
	cfg.ModelProfiles = []config.ModelProfile{{Name: "reasoning", Provider: "anthropic", Model: "claude-sonnet"}}
	cfg.FallbackModels = []string{"gpt-4.1-mini"}
	cfg.LightweightModel = "gpt-4.1-nano"
	dir := writeCompletionConfig(t, cfg)

	taskStore, err := tasks.New(filepath.Join(dir, "workspace", "db", "tasks.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer taskStore.Close()
	candidate, err := taskStore.QueueCandidate(context.Background(), "alice", tasks.CandidateInput{Title: "Review PR"})
	if err != nil {
		t.Fatal(err)
	}
	_, createdTask, err := taskStore.ApproveCandidate(context.Background(), "alice", candidate.ID, tasks.CandidatePatch{}, "")
	if err != nil {
		t.Fatal(err)
	}

	calendarStore, err := calendar.New(filepath.Join(dir, "workspace", "db", "calendar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer calendarStore.Close()
	source, err := calendarStore.CreateSource(context.Background(), "alice", calendar.SourceInput{Name: "Team", URL: "https://example.com/team.ics"})
	if err != nil {
		t.Fatal(err)
	}

	taskIDs := completionSuggestions(t, dir, "tasks", "complete", "--peer", "alice", "--id", "")
	if !containsString(taskIDs, createdTask.ID) {
		t.Fatalf("expected task ID completion %q in %v", createdTask.ID, taskIDs)
	}
	calendarIDs := completionSuggestions(t, dir, "calendar", "remove", "--peer", "alice", "--id", "")
	if !containsString(calendarIDs, source.ID) {
		t.Fatalf("expected calendar source completion %q in %v", source.ID, calendarIDs)
	}
	models := completionSuggestions(t, dir, "model", "set", "")
	for _, expected := range []string{"reasoning", "gpt-4o", "gpt-4.1-nano", "gpt-4.1-mini"} {
		if !containsString(models, expected) {
			t.Fatalf("expected model completion %q in %v", expected, models)
		}
	}
}

func writeCompletionConfig(t *testing.T, cfg *config.Config) string {
	t.Helper()
	dir := t.TempDir()
	cfgCopy := *cfg
	cfgCopy.WorkspaceRoot = "./workspace"
	if err := os.MkdirAll(filepath.Join(dir, "workspace", "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(config.EncodeTOML(&cfgCopy, false)), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func completionSuggestions(t *testing.T, dir string, args ...string) []string {
	t.Helper()
	prevWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(prevWD); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	}()
	root := NewRootCommand(app.BuildInfo{Version: "test"}, func(app.BuildInfo) error { return nil })
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"__completeNoDesc"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatalf("complete %v: %v\n%s", args, err, out.String())
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	outLines := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") || strings.HasPrefix(line, "Completion ended with directive") {
			continue
		}
		outLines = append(outLines, line)
	}
	return outLines
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestMemoryGetCommandFormatsProvenance(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()

			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			if got := req["method"]; got != "memory.get" {
				t.Errorf("method = %v", got)
			}
			_ = conn.WriteJSON(map[string]any{
				"id": req["id"],
				"result": map[string]any{
					"chunk": map[string]any{
						"id":                 "mem-1",
						"content":            "Remember the deployment checklist before every release.",
						"category":           "ops",
						"retention_class":    "pinned",
						"exposure_policy":    "auto",
						"capture_kind":       "manual",
						"capture_reason":     "confirmed in chat",
						"confidence":         0.95,
						"source_session_key": "alice::main",
					},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	toml := fmt.Sprintf(healthStatusConfigTemplate, server.URL)
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newMemoryCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"get", "--peer", "alice", "--id", "mem-1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "mem-1") {
		t.Fatalf("unexpected output: %s", text)
	}
	if !strings.Contains(text, "source: manual | reason=confirmed in chat | confidence=0.95 | session=alice::main") {
		t.Fatalf("unexpected output: %s", text)
	}
	if !strings.Contains(text, "Remember the deployment checklist") {
		t.Fatalf("unexpected output: %s", text)
	}
}

func TestHealthStatusAndCronCommands(t *testing.T) {
	oldUser := cliCurrentUser
	oldHostname := cliHostname
	t.Cleanup(func() {
		cliCurrentUser = oldUser
		cliHostname = oldHostname
	})
	cliCurrentUser = func() (*user.User, error) {
		return &user.User{Username: "alice.dev"}, nil
	}
	cliHostname = func() (string, error) { return "workstation-01", nil }

	expectedPeer, err := defaultCLIPeerID()
	if err != nil {
		t.Fatal(err)
	}

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.1.0", "git_hash": "abc", "build_time": "now"})
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			method, _ := req["method"].(string)
			var result any
			switch method {
			case "server.capabilities":
				result = map[string]any{"capabilities": map[string]bool{"cron": true}}
			case "cron.list":
				result = []map[string]any{{"job_id": "job-1", "peer_id": "alice", "name": "job"}}
			default:
				result = map[string]any{"ok": true}
			}
			_ = conn.WriteJSON(map[string]any{"id": "1", "result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	toml := fmt.Sprintf(healthStatusConfigTemplate, server.URL)
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}

	healthCmd := newHealthCommand(cmdCtx)
	var healthOut bytes.Buffer
	healthCmd.SetOut(&healthOut)
	healthCmd.SetErr(&healthOut)
	if err := healthCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(healthOut.String(), `"status": "ok"`) {
		t.Fatalf("unexpected health output: %s", healthOut.String())
	}
	statusCmd := newStatusCommand(cmdCtx)
	var statusOut bytes.Buffer
	statusCmd.SetOut(&statusOut)
	statusCmd.SetErr(&statusOut)
	statusCmd.SetArgs([]string{"--peer", "alice"})
	if err := statusCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusOut.String(), `"cron": true`) {
		t.Fatalf("unexpected status output: %s", statusOut.String())
	}
	if !strings.Contains(statusOut.String(), fmt.Sprintf(`"default_peer_id": %q`, expectedPeer)) {
		t.Fatalf("expected derived default peer in status output: %s", statusOut.String())
	}

	cronCmd := newCronCommand(cmdCtx)
	var cronOut bytes.Buffer
	cronCmd.SetOut(&cronOut)
	cronCmd.SetErr(&cronOut)
	cronCmd.SetArgs([]string{"list", "--peer", "alice"})
	if err := cronCmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cronOut.String(), `"job_id": "job-1"`) {
		t.Fatalf("unexpected cron output: %s", cronOut.String())
	}
}

func TestTasksListCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.1.0", "git_hash": "abc", "build_time": "now"})
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			method, _ := req["method"].(string)
			var result any
			switch method {
			case "server.capabilities":
				result = map[string]any{"capabilities": map[string]bool{"tasks": true}}
			case "task.list":
				result = map[string]any{"tasks": []map[string]any{{"id": "task-1", "title": "Ship release notes", "status": "open", "owner": "ops"}}}
			default:
				result = map[string]any{"ok": true}
			}
			_ = conn.WriteJSON(map[string]any{"id": req["id"], "result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(fmt.Sprintf(healthStatusConfigTemplate, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newTasksCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--peer", "alice"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "task-1") {
		t.Fatalf("expected task id in output: %s", out.String())
	}
	if !strings.Contains(out.String(), "Ship release notes") {
		t.Fatalf("expected task title in output: %s", out.String())
	}
}

func TestMemoryPreferenceListCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()

			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			if got := req["method"]; got != "memory.preference.list" {
				t.Errorf("method = %v", got)
			}
			params, _ := req["params"].(map[string]any)
			if got := params["kind"]; got != "decision" {
				t.Errorf("kind = %v", got)
			}
			if got := params["scope"]; got != "workspace" {
				t.Errorf("scope = %v", got)
			}
			if got := params["limit"]; got != float64(5) {
				t.Errorf("limit = %v", got)
			}
			_ = conn.WriteJSON(map[string]any{
				"id": req["id"],
				"result": map[string]any{
					"count": 1,
					"kind":  "decision",
					"scope": "workspace",
					"preferences": []map[string]any{{
						"id":                "pref-1",
						"kind":              "decision",
						"name":              "editor",
						"value":             "nvim",
						"category":          "tooling",
						"scope":             "workspace",
						"scope_ref":         "repo",
						"confidence":        0.8,
						"last_confirmed_at": 1704067200,
					}},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	toml := fmt.Sprintf(healthStatusConfigTemplate, server.URL)
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newMemoryCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"preference", "list", "--peer", "alice", "--kind", "decision", "--scope", "workspace", "--limit", "5"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Structured preferences: 1 total") {
		t.Fatalf("unexpected output: %s", text)
	}
	if !strings.Contains(text, "pref-1 [decision] editor = nvim") {
		t.Fatalf("unexpected output: %s", text)
	}
	if !strings.Contains(text, "scope=workspace/repo") {
		t.Fatalf("unexpected output: %s", text)
	}
}

func TestBriefCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.1.0", "git_hash": "abc", "build_time": "now"})
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			method, _ := req["method"].(string)
			result := map[string]any{"ok": true}
			if method == "brief.generate" {
				result = map[string]any{"text": "Daily brief\nSummary: 1 events"}
			}
			_ = conn.WriteJSON(map[string]any{"id": req["id"], "result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(fmt.Sprintf(healthStatusConfigTemplate, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newBriefCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--peer", "alice", "--kind", "daily"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Daily brief") {
		t.Fatalf("unexpected brief output: %s", out.String())
	}
}

func TestDashboardCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.1.0", "git_hash": "abc", "build_time": "now"})
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			method, _ := req["method"].(string)
			var result any
			switch method {
			case "brief.generate":
				result = map[string]any{
					"ok":   true,
					"kind": "daily",
					"report": map[string]any{
						"kind":         "daily",
						"peer_id":      "alice",
						"timezone":     "UTC",
						"generated_at": float64(1777320000),
						"agenda": map[string]any{
							"events": []map[string]any{{"summary": "Design review", "start_at": float64(1777323600), "end_at": float64(1777327200)}},
						},
						"recent_commitments": []map[string]any{{"title": "Send draft agenda", "source_excerpt": "I’ll send the draft agenda tonight."}},
						"active_projects":    []map[string]any{{"id": "project-1", "name": "Home office refresh", "last_seen_at": float64(1775600000), "linked_chunk_count": float64(2)}},
						"summary":            map[string]any{"event_count": float64(1), "active_project_count": float64(1), "commitment_count": float64(1)},
					},
				}
			case "task.list":
				result = map[string]any{"tasks": []map[string]any{{"id": "task-1", "title": "Ship release notes", "status": "open", "owner": "me", "due_at": float64(1777406400)}}}
			case "waiting.list":
				result = map[string]any{"waiting": []map[string]any{{"id": "wait-1", "title": "Vendor quote", "status": "open", "waiting_for": "vendor", "follow_up_at": float64(1777233600)}}}
			case "runs.list":
				result = map[string]any{"records": []map[string]any{{"id": "run-1", "kind": "cron", "status": "running", "steps": float64(2), "tool_calls": float64(1), "queued_at": "2026-04-27T08:00:00Z", "started_at": "2026-04-27T08:00:05Z"}}}
			default:
				result = map[string]any{"ok": true}
			}
			_ = conn.WriteJSON(map[string]any{"id": req["id"], "result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(fmt.Sprintf(healthStatusConfigTemplate, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newDashboardCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--peer", "alice", "--timezone", "UTC", "--stale-project-days", "14"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "Commitments dashboard") {
		t.Fatalf("unexpected dashboard output: %s", text)
	}
	if !strings.Contains(text, "Open tasks") || !strings.Contains(text, "Ship release notes") {
		t.Fatalf("missing tasks in dashboard output: %s", text)
	}
	if !strings.Contains(text, "Waiting-ons") || !strings.Contains(text, "Vendor quote") {
		t.Fatalf("missing waiting-ons in dashboard output: %s", text)
	}
	if !strings.Contains(text, "Upcoming events") || !strings.Contains(text, "Design review") {
		t.Fatalf("missing agenda in dashboard output: %s", text)
	}
	if !strings.Contains(text, "Recent promises") || !strings.Contains(text, "Send draft agenda") {
		t.Fatalf("missing commitments in dashboard output: %s", text)
	}
	if !strings.Contains(text, "Stale project threads") || !strings.Contains(text, "Home office refresh") {
		t.Fatalf("missing stale project threads in dashboard output: %s", text)
	}
	if !strings.Contains(text, "Runtime") || !strings.Contains(text, "run-1") {
		t.Fatalf("missing runtime section in dashboard output: %s", text)
	}
	if !strings.Contains(text, "Gateway: ok") {
		t.Fatalf("missing gateway status in dashboard output: %s", text)
	}

	out.Reset()
	cmd.SetArgs([]string{"--peer", "alice", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	jsonText := out.String()
	if !strings.Contains(jsonText, `"open_tasks"`) || !strings.Contains(jsonText, `"stale_projects"`) {
		t.Fatalf("unexpected dashboard json output: %s", jsonText)
	}
	if !strings.Contains(jsonText, `"peer_id": "alice"`) {
		t.Fatalf("unexpected dashboard json output: %s", jsonText)
	}
}

func TestWaitingListCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.1.0", "git_hash": "abc", "build_time": "now"})
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			method, _ := req["method"].(string)
			var result any
			switch method {
			case "server.capabilities":
				result = map[string]any{"capabilities": map[string]bool{"tasks": true}}
			case "waiting.list":
				result = map[string]any{"waiting": []map[string]any{{"id": "wait-1", "title": "Vendor reply", "status": "open", "waiting_for": "vendor"}}, "status": "open"}
			default:
				result = map[string]any{"ok": true}
			}
			_ = conn.WriteJSON(map[string]any{"id": req["id"], "result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(fmt.Sprintf(healthStatusConfigTemplate, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newWaitingCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--peer", "alice"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wait-1") {
		t.Fatalf("expected waiting-on id in output: %s", out.String())
	}
	if !strings.Contains(out.String(), "Vendor reply") {
		t.Fatalf("expected waiting-on title in output: %s", out.String())
	}
}

func TestCalendarAgendaCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.1.0", "git_hash": "abc", "build_time": "now"})
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			method, _ := req["method"].(string)
			var result any
			switch method {
			case "calendar.agenda":
				result = map[string]any{"agenda": map[string]any{"events": []map[string]any{{"summary": "Design review", "start_at": float64(1777021200), "end_at": float64(1777024800)}}}}
			default:
				result = map[string]any{"ok": true}
			}
			_ = conn.WriteJSON(map[string]any{"id": req["id"], "result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(fmt.Sprintf(healthStatusConfigTemplate, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newCalendarCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"agenda", "--peer", "alice", "--scope", "today"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Design review") {
		t.Fatalf("expected event summary in output: %s", out.String())
	}
}

func TestAgentOneShotCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			_ = conn.WriteJSON(map[string]any{"method": "stream.delta", "params": map[string]any{"req_id": "1", "content": "Hello"}})
			_ = conn.WriteJSON(map[string]any{"method": "stream.delta", "params": map[string]any{"req_id": "1", "content": " world"}})
			_ = conn.WriteJSON(map[string]any{"id": "1", "result": map[string]any{"session_key": "alice", "attempts": 1, "assistant_text": "Hello world", "steps": 1}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	toml := fmt.Sprintf(agentOneShotConfigTemplate, server.URL)
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newAgentCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--peer", "alice", "--message", "hi"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Hello world") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestAgentCommandDerivesPeerWhenMissing(t *testing.T) {
	oldUser := cliCurrentUser
	oldHostname := cliHostname
	t.Cleanup(func() {
		cliCurrentUser = oldUser
		cliHostname = oldHostname
	})
	cliCurrentUser = func() (*user.User, error) {
		return &user.User{Username: "alice.dev"}, nil
	}
	cliHostname = func() (string, error) { return "workstation-01", nil }

	expectedPeer, err := defaultCLIPeerID()
	if err != nil {
		t.Fatal(err)
	}

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ws":
			if got := r.URL.Query().Get("peer_id"); got != expectedPeer {
				t.Fatalf("peer_id = %q want %q", got, expectedPeer)
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			_ = conn.WriteJSON(map[string]any{"id": "1", "result": map[string]any{"session_key": expectedPeer, "attempts": 1, "assistant_text": "Hello world", "steps": 1}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	toml := fmt.Sprintf(agentOneShotConfigTemplate, server.URL)
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newAgentCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--message", "hi"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Hello world") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestBookmarkCommandDerivesPeerWhenMissing(t *testing.T) {
	oldUser := cliCurrentUser
	oldHostname := cliHostname
	t.Cleanup(func() {
		cliCurrentUser = oldUser
		cliHostname = oldHostname
	})
	cliCurrentUser = func() (*user.User, error) {
		return &user.User{Username: "alice.dev"}, nil
	}
	cliHostname = func() (string, error) { return "workstation-01", nil }

	expectedPeer, err := defaultCLIPeerID()
	if err != nil {
		t.Fatal(err)
	}

	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ws":
			if got := r.URL.Query().Get("peer_id"); got != expectedPeer {
				t.Fatalf("peer_id = %q want %q", got, expectedPeer)
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			if got := req["method"]; got != "bookmark.list" {
				t.Errorf("method = %v", got)
			}
			_ = conn.WriteJSON(map[string]any{"id": req["id"], "result": map[string]any{"count": 1, "bookmarks": []map[string]any{{"id": "bm-1", "title": "Release checklist"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	toml := fmt.Sprintf(agentOneShotConfigTemplate, server.URL)
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newBookmarkCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"list", "--limit", "1"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Release checklist") {
		t.Fatalf("unexpected output: %s", out.String())
	}
}

func TestMemoryQueueListCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			method, _ := req["method"].(string)
			var result any
			switch method {
			case "memory.candidate.list":
				result = map[string]any{
					"count":                1,
					"manual_count":         0,
					"auto_generated_count": 1,
					"capture_kinds": map[string]any{
						"auto_turn_extract": 1,
					},
					"candidates": []map[string]any{{
						"id":                 "cand-1",
						"content":            "remember deployment checklist",
						"status":             "pending",
						"capture_kind":       "auto_turn_extract",
						"source_session_key": "alice::main",
						"source_excerpt":     "Remember deployment checklist before each release.",
					}},
				}
			default:
				result = map[string]any{"ok": true}
			}
			_ = conn.WriteJSON(map[string]any{"id": "1", "result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	toml := fmt.Sprintf(agentOneShotConfigTemplate, server.URL)
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newMemoryCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"queue", "list", "--peer", "alice"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "1 auto-generated | 0 manual") {
		t.Fatalf("unexpected human memory queue output: %s", out.String())
	}
	if !strings.Contains(out.String(), "alice::main") {
		t.Fatalf("expected provenance in human output: %s", out.String())
	}

	out.Reset()
	cmd.SetArgs([]string{"queue", "list", "--peer", "alice", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"cand-1"`) {
		t.Fatalf("unexpected memory queue output: %s", out.String())
	}
}

func TestMemoryEntityListCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			method, _ := req["method"].(string)
			var result any
			switch method {
			case "memory.entity.list":
				result = map[string]any{
					"count": 1,
					"kind":  "project",
					"entities": []map[string]any{{
						"id":                 "entity-1",
						"kind":               "project",
						"name":               "Borealis Trip",
						"aliases":            []string{"summer trip"},
						"linked_chunk_count": 2,
					}},
				}
			default:
				result = map[string]any{"ok": true}
			}
			_ = conn.WriteJSON(map[string]any{"id": "1", "result": result})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	toml := fmt.Sprintf(agentOneShotConfigTemplate, server.URL)
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newMemoryCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"entity", "list", "--peer", "alice", "--kind", "project", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"entity-1"`) || !strings.Contains(out.String(), `"Borealis Trip"`) {
		t.Fatalf("unexpected memory entity output: %s", out.String())
	}
}

func TestMemoryEntityDeleteCommand(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/ws":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Errorf("upgrade: %v", err)
				return
			}
			defer conn.Close()
			var req map[string]any
			if err := conn.ReadJSON(&req); err != nil {
				t.Errorf("read json: %v", err)
				return
			}
			method, _ := req["method"].(string)
			if method != "memory.entity.delete" {
				t.Errorf("unexpected method %q", method)
			}
			_ = conn.WriteJSON(map[string]any{"id": "1", "result": map[string]any{"ok": true, "id": "entity-1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	toml := fmt.Sprintf(agentOneShotConfigTemplate, server.URL)
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newMemoryCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"entity", "delete", "--peer", "alice", "--id", "entity-1", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"entity-1"`) {
		t.Fatalf("unexpected memory entity delete output: %s", out.String())
	}
}

func TestDoctorRepairCreatesConfigAndStateDirs(t *testing.T) {
	dir := t.TempDir()
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newDoctorCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--repair", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor repair failed: %v\noutput:\n%s", err, out.String())
	}
	if !fileExists(filepath.Join(dir, config.DefaultConfigFile)) {
		t.Fatal("expected doctor repair to create koios.config.toml")
	}
	for _, path := range []string{
		filepath.Join(dir, "workspace"),
		filepath.Join(dir, "workspace", "sessions"),
		filepath.Join(dir, "workspace", "cron"),
		filepath.Join(dir, "workspace", "agents"),
		filepath.Join(dir, "workspace", "peers"),
		filepath.Join(dir, "workspace", "peers", workspace.DefaultPeerID),
		filepath.Join(dir, "workspace", "db"),
		filepath.Join(dir, "workspace", "workflows"),
		filepath.Join(dir, "workspace", "runs"),
		filepath.Join(dir, "workspace", "peers", workspace.DefaultPeerID, "AGENTS.md"),
		filepath.Join(dir, "workspace", "peers", workspace.DefaultPeerID, "BOOTSTRAP.md"),
	} {
		if strings.HasSuffix(path, ".md") {
			if !fileExists(path) {
				t.Fatalf("expected %s to exist after repair", path)
			}
			continue
		}
		if !dirExists(path) {
			t.Fatalf("expected %s to exist after repair", path)
		}
	}
	for _, path := range expectedWorkspaceDBPaths(dir) {
		if !fileExists(path) {
			t.Fatalf("expected %s to exist after repair", path)
		}
	}
	if !strings.Contains(out.String(), `"created workspace starter doc:`) {
		t.Fatalf("expected workspace doc creation in repair output: %s", out.String())
	}
	if !strings.Contains(out.String(), `"repairs": 28`) {
		t.Fatalf("unexpected doctor output: %s", out.String())
	}
	if !strings.Contains(out.String(), `"created koios.config.toml"`) {
		t.Fatalf("expected repair output to mention config creation: %s", out.String())
	}
}

func TestDoctorRepairNormalizesInvalidConfig(t *testing.T) {
	dir := t.TempDir()
	content := strings.Join([]string{
		"[server]",
		`request_timeout = "1s"`,
		"",
		"[llm]",
		`provider = "bogus"`,
		`model = ""`,
		"",
		"[session]",
		"max_messages = 0",
		"max_entries = -1",
		`daily_reset_time = "99:99"`,
		"",
		"[cron]",
		"max_concurrent = 0",
		"",
		"[agent]",
		"max_children = 0",
		"retry_attempts = 0",
		"retry_status_codes = [200, 999]",
		"",
		"[tools]",
		`profile = "broken"`,
		"",
		"[tools.exec]",
		`approval_mode = "sometimes"`,
		`default_timeout = "0s"`,
		`max_timeout = "0s"`,
		`approval_ttl = "0s"`,
		"",
		"[tools.process]",
		`stop_timeout = "0s"`,
		"log_tail_bytes = 0",
		"max_processes_per_peer = 0",
		"",
		"[workspace]",
		`root = ""`,
		"max_file_bytes = 0",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newDoctorCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--repair", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor repair failed: %v\noutput:\n%s", err, out.String())
	}
	data, err := os.ReadFile(filepath.Join(dir, config.DefaultConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, expected := range []string{
		`provider = "openai"`,
		`model = "gpt-4o"`,
		`max_messages = 100`,
		`max_entries = 0`,
		`max_concurrent = 1`,
		`max_children = 4`,
		`retry_attempts = 3`,
		`profile = "full"`,
		`approval_mode = "dangerous"`,
		`root = "./workspace"`,
		`max_file_bytes = 1048576`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected repaired config to contain %q, got:\n%s", expected, text)
		}
	}
	if !strings.Contains(out.String(), `rewrote koios.config.toml with normalized settings`) {
		t.Fatalf("expected rewrite repair note in output: %s", out.String())
	}
	if !strings.Contains(out.String(), `reset llm.provider to`) {
		t.Fatalf("expected detailed repair notes in output: %s", out.String())
	}
}

func TestDoctorRepairForceReplacesUnreadableConfig(t *testing.T) {
	dir := t.TempDir()
	brokenPath := filepath.Join(dir, config.DefaultConfigFile)
	broken := []byte("[llm\nprovider = \"openai\"\n")
	if err := os.WriteFile(brokenPath, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newDoctorCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--repair", "--force", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor force repair failed: %v\noutput:\n%s", err, out.String())
	}
	data, err := os.ReadFile(brokenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `[llm]`) || !strings.Contains(string(data), `provider = "openai"`) {
		t.Fatalf("expected broken config to be replaced with defaults, got:\n%s", string(data))
	}
	backup, err := os.ReadFile(brokenPath + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(broken) {
		t.Fatalf("expected backup to preserve broken config contents")
	}
	if !strings.Contains(out.String(), `replaced unreadable koios.config.toml with defaults`) {
		t.Fatalf("expected forced replacement repair note in output: %s", out.String())
	}
}

func TestDoctorReportsConfigParseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte("[llm\nprovider = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newDoctorCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--json"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected doctor to fail on parse error")
	}
	if !strings.Contains(out.String(), `"key": "config.parse"`) {
		t.Fatalf("expected parse finding in output: %s", out.String())
	}
	if !strings.Contains(out.String(), `parse config file`) {
		t.Fatalf("expected parse error details in output: %s", out.String())
	}
}

func TestDoctorDeepProbesGatewayAndMonitor(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.1.0", "git_hash": "abc", "build_time": "now"})
		case "/v1/monitor":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"stale": false,
				"subsystems": map[string]any{
					"scheduler": map[string]any{"restarts": 1},
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(fmt.Sprintf(healthStatusConfigTemplate, server.URL)), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newDoctorCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--deep", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"gateway"`) {
		t.Fatalf("expected gateway payload in output: %s", out.String())
	}
	if !strings.Contains(out.String(), `"key": "gateway.monitor.subsystem"`) {
		t.Fatalf("expected monitor restart finding in output: %s", out.String())
	}
	if !strings.Contains(out.String(), `gateway reachable at`) {
		t.Fatalf("expected gateway reachability finding in output: %s", out.String())
	}
}

func TestDoctorDeepProbesMCPHTTPServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "0.1.0", "git_hash": "abc", "build_time": "now"})
		case "/v1/monitor":
			_ = json.NewEncoder(w).Encode(map[string]any{"stale": false, "subsystems": map[string]any{}})
		case "/mcp":
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode mcp request: %v", err)
			}
			method, _ := req["method"].(string)
			switch method {
			case "initialize":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req["id"],
					"result": map[string]any{
						"protocolVersion": "2024-11-05",
						"capabilities":    map[string]any{},
						"serverInfo":      map[string]any{"name": "demo", "version": "1.0.0"},
					},
				})
			case "tools/list":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req["id"],
					"result": map[string]any{
						"tools": []map[string]any{{
							"name":        "echo",
							"description": "echo input",
							"inputSchema": map[string]any{"type": "object"},
						}},
					},
				})
			default:
				_ = json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req["id"],
					"error":   map[string]any{"code": -32601, "message": "method not found"},
				})
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	configText := strings.Join([]string{
		fmt.Sprintf("[server]\nlisten_addr = %q\n", server.URL),
		"[llm]",
		`provider = "openai"`,
		`model = "gpt-4o"`,
		`api_key = "test-key"`,
		"",
		"[workspace]",
		`root = "./workspace"`,
		"",
		"[[mcp.servers]]",
		`name = "demo"`,
		`transport = "http"`,
		fmt.Sprintf("url = %q", server.URL+"/mcp"),
		`timeout = "2s"`,
		"enabled = true",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(configText), 0o600); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newDoctorCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--deep", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"key": "mcp.servers[0].probe"`) {
		t.Fatalf("expected MCP probe finding in output: %s", out.String())
	}
	if !strings.Contains(out.String(), `reachable over http with 1 tool(s)`) {
		t.Fatalf("expected successful MCP probe output: %s", out.String())
	}
}

func TestRootNoArgsShowsHelp(t *testing.T) {
	called := false
	root := NewRootCommand(app.BuildInfo{Version: "test"}, func(app.BuildInfo) error {
		called = true
		return nil
	})
	var out strings.Builder
	root.SetOut(&out)
	root.SetArgs(nil)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("expected help to be shown, not daemon to start")
	}
	if !strings.Contains(out.String(), "Usage:") {
		t.Fatalf("expected help output, got: %s", out.String())
	}
}
