package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/ffimnsr/koios/internal/app"
	"github.com/ffimnsr/koios/internal/config"
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
}

func TestScaffoldWorkspaceCreatesBootstrapDocs(t *testing.T) {
	root := t.TempDir()
	if err := scaffoldWorkspace(root, false); err != nil {
		t.Fatal(err)
	}

	for _, subdir := range []string{"sessions", "cron", "agents", "memory"} {
		if info, err := os.Stat(filepath.Join(root, subdir)); err != nil || !info.IsDir() {
			t.Fatalf("expected subdir %q to exist, err=%v", subdir, err)
		}
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
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Fatalf("%s should not be empty", name)
		}
	}
}

func TestBackupCreateAndVerify(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(config.DefaultTOML()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("0.1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, "out.tar.gz")
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	create := newBackupCommand(cmdCtx)
	var out bytes.Buffer
	create.SetOut(&out)
	create.SetErr(&out)
	create.SetArgs([]string{"create", "--output", archive})
	if err := create.Execute(); err != nil {
		t.Fatal(err)
	}
	if !fileExists(archive) {
		t.Fatal("expected archive to exist")
	}
	verify := newBackupCommand(cmdCtx)
	verify.SetOut(&out)
	verify.SetErr(&out)
	verify.SetArgs([]string{"verify", archive})
	if err := verify.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestResetDryRun(t *testing.T) {
	dir := t.TempDir()
	toml := strings.Join([]string{
		"[llm]",
		"model = \"gpt-4o\"",
		"",
		"[session]",
		"dir = \"./data/sessions\"",
		"",
	}, "\n")
	if err := os.WriteFile(filepath.Join(dir, config.DefaultConfigFile), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	sessionsDir := filepath.Join(dir, "data", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cmdCtx := &commandContext{build: app.BuildInfo{Version: "test"}, cwd: dir}
	cmd := newResetCommand(cmdCtx)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--scope", "config+creds+sessions", "--dry-run"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !fileExists(filepath.Join(dir, config.DefaultConfigFile)) {
		t.Fatal("config should not be removed in dry-run")
	}
}

func TestHealthStatusAndCronOverTestServer(t *testing.T) {
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
