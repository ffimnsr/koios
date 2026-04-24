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
		filepath.Join(dir, "workspace", "workflows"),
		filepath.Join(dir, "workspace", "runs"),
		filepath.Join(dir, "workspace", "AGENTS.md"),
		filepath.Join(dir, "workspace", "BOOTSTRAP.md"),
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
	if !strings.Contains(out.String(), `"created workspace starter doc:`) {
		t.Fatalf("expected workspace doc creation in repair output: %s", out.String())
	}
	if !strings.Contains(out.String(), `"repairs": 14`) {
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
