package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/workspace"
)

func TestBackgroundProcessLifecycle(t *testing.T) {
	store := session.New(10)
	wsStore, err := workspace.New(t.TempDir(), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		RunLedger:      ledger,
		BackgroundProcessConfig: BackgroundProcessConfig{
			Enabled: true,
		},
	})

	startedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "process.start",
		Arguments: json.RawMessage(`{"command":"printf 'hello stdout'; printf 'hello stderr' 1>&2","workdir":"."}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(process.start): %v", err)
	}
	startedRaw, _ := json.Marshal(startedAny)
	var started struct {
		ID         string `json:"id"`
		Kind       string `json:"kind"`
		Status     string `json:"status"`
		StdoutPath string `json:"stdout_path"`
		StderrPath string `json:"stderr_path"`
	}
	if err := json.Unmarshal(startedRaw, &started); err != nil {
		t.Fatalf("unmarshal start result: %v", err)
	}
	if started.ID == "" || started.Kind != string(runledger.KindProcess) || started.Status != string(runledger.StatusRunning) {
		t.Fatalf("unexpected start result: %#v", started)
	}
	rec := waitForBackgroundProcessRecord(t, ledger, started.ID)
	if rec.Status != runledger.StatusCompleted {
		t.Fatalf("expected completed process, got %#v", rec)
	}
	logsAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "process.logs",
		Arguments: json.RawMessage(`{"id":"` + started.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(process.logs): %v", err)
	}
	logsRaw, _ := json.Marshal(logsAny)
	var logs struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if err := json.Unmarshal(logsRaw, &logs); err != nil {
		t.Fatalf("unmarshal logs: %v", err)
	}
	if logs.Stdout != "hello stdout" || logs.Stderr != "hello stderr" {
		t.Fatalf("unexpected logs: %#v", logs)
	}
	statusAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "process.status",
		Arguments: json.RawMessage(`{"id":"` + started.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(process.status): %v", err)
	}
	statusRaw, _ := json.Marshal(statusAny)
	var status struct {
		Status string `json:"status"`
		Active bool   `json:"active"`
	}
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	if status.Status != string(runledger.StatusCompleted) || status.Active {
		t.Fatalf("unexpected process status: %#v", status)
	}
	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "process.list",
		Arguments: json.RawMessage(`{"limit":5}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(process.list): %v", err)
	}
	listRaw, _ := json.Marshal(listAny)
	if !strings.Contains(string(listRaw), started.ID) {
		t.Fatalf("expected process in list, got %s", string(listRaw))
	}
}

func TestBackgroundProcessStop(t *testing.T) {
	store := session.New(10)
	wsStore, err := workspace.New(t.TempDir(), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		RunLedger:      ledger,
		BackgroundProcessConfig: BackgroundProcessConfig{
			Enabled:     true,
			StopTimeout: 200 * time.Millisecond,
		},
	})

	startedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "process.start",
		Arguments: json.RawMessage(`{"command":"sleep 30"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(process.start): %v", err)
	}
	startedRaw, _ := json.Marshal(startedAny)
	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(startedRaw, &started); err != nil {
		t.Fatalf("unmarshal start result: %v", err)
	}
	stopAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "process.stop",
		Arguments: json.RawMessage(`{"id":"` + started.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(process.stop): %v", err)
	}
	stopRaw, _ := json.Marshal(stopAny)
	var stopped struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(stopRaw, &stopped); err != nil {
		t.Fatalf("unmarshal stop result: %v", err)
	}
	if stopped.Status != "stopping" {
		t.Fatalf("unexpected stop result: %#v", stopped)
	}
	rec := waitForBackgroundProcessRecord(t, ledger, started.ID)
	if rec.Status != runledger.StatusCanceled {
		t.Fatalf("expected canceled process, got %#v", rec)
	}
}

func TestBackgroundProcessStartUsesApprovalLifecycle(t *testing.T) {
	store := session.New(10)
	wsStore, err := workspace.New(t.TempDir(), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		RunLedger:      ledger,
		ExecConfig: ExecConfig{
			Enabled:      true,
			ApprovalMode: "always",
		},
		BackgroundProcessConfig: BackgroundProcessConfig{
			Enabled: true,
		},
	})

	requestedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "process.start",
		Arguments: json.RawMessage(`{"command":"printf 'hello approval'"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(process.start approval): %v", err)
	}
	requestedRaw, _ := json.Marshal(requestedAny)
	var requested struct {
		Status   string `json:"status"`
		Approval struct {
			ID string `json:"id"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(requestedRaw, &requested); err != nil {
		t.Fatalf("unmarshal approval request: %v", err)
	}
	if requested.Status != "approval_required" || requested.Approval.ID == "" {
		t.Fatalf("expected approval_required result, got %#v", requestedAny)
	}

	approvedAny, err := h.approvePendingAction(context.Background(), "alice", requested.Approval.ID, nil)
	if err != nil {
		t.Fatalf("approvePendingAction(process.start): %v", err)
	}
	approvedRaw, _ := json.Marshal(approvedAny)
	var approved struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(approvedRaw, &approved); err != nil {
		t.Fatalf("unmarshal approved start result: %v", err)
	}
	if approved.ID == "" || approved.Status != string(runledger.StatusRunning) {
		t.Fatalf("unexpected approved start result: %#v", approvedAny)
	}
	rec := waitForBackgroundProcessRecord(t, ledger, approved.ID)
	if rec.Status != runledger.StatusCompleted {
		t.Fatalf("expected completed approved process, got %#v", rec)
	}
}

func TestBackgroundProcessToolDefinitions(t *testing.T) {
	store := session.New(10)
	wsStore, err := workspace.New(t.TempDir(), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		RunLedger:      ledger,
		BackgroundProcessConfig: BackgroundProcessConfig{
			Enabled: true,
		},
		ToolPolicy: ToolPolicy{Profile: "coding"},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	for _, want := range []string{"process.start", "process.status", "process.stop", "process.list", "process.logs"} {
		if !containsString(names, want) {
			t.Fatalf("expected %s in tool definitions, got %#v", want, names)
		}
	}
}

func waitForBackgroundProcessRecord(t *testing.T, ledger *runledger.Store, id string) runledger.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok := ledger.Get(id)
		if ok && isTerminalRunStatus(rec.Status) {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for background process record %q", id)
	return runledger.Record{}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
