package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/sandbox"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/workspace"
)

type noopProvider struct{}

type queuedCodeExecutionRun struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	Async  bool   `json:"async"`
}

func (noopProvider) Complete(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{}, nil
}

func (noopProvider) CompleteStream(context.Context, *types.ChatRequest, http.ResponseWriter) (string, error) {
	return "", nil
}

func TestExecuteToolCodeExecutionAsyncPersistsRunLedgerResult(t *testing.T) {
	fakeBwrap := writeFakeBubblewrap(t)
	prevRunner := defaultSandboxRunner
	defaultSandboxRunner = &sandbox.Runner{ResolveBubblewrap: func() (string, error) { return fakeBwrap, nil }}
	t.Cleanup(func() {
		defaultSandboxRunner = prevRunner
	})

	store := session.New(10)
	wsStore, err := workspace.New(t.TempDir(), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() {
		_ = ledger.Close()
	})

	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		RunLedger:      ledger,
		CodeExecutionConfig: CodeExecutionConfig{
			Enabled: true,
		},
	})

	queuedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "code_execution",
		Arguments: json.RawMessage(`{"command":"printf 'artifact' > out.txt && printf 'done'","async":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(code_execution async): %v", err)
	}
	rawQueued, _ := json.Marshal(queuedAny)
	var queued struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		Status string `json:"status"`
		Async  bool   `json:"async"`
	}
	if err := json.Unmarshal(rawQueued, &queued); err != nil {
		t.Fatalf("unmarshal queued result: %v", err)
	}
	if queued.ID == "" || queued.Kind != string(runledger.KindCodeExecution) || queued.Status != string(runledger.StatusQueued) || !queued.Async {
		t.Fatalf("unexpected queued result: %#v", queued)
	}

	rec := waitForLedgerRecord(t, ledger, queued.ID)
	if rec.Kind != runledger.KindCodeExecution {
		t.Fatalf("unexpected run kind: %#v", rec)
	}
	if rec.Status != runledger.StatusCompleted {
		t.Fatalf("unexpected run status: %#v", rec)
	}
	if len(rec.Request) == 0 {
		t.Fatalf("expected persisted request payload: %#v", rec)
	}
	if len(rec.Result) == 0 {
		t.Fatalf("expected persisted result payload: %#v", rec)
	}

	var request struct {
		Command string `json:"command"`
		Async   bool   `json:"async"`
	}
	if err := json.Unmarshal(rec.Request, &request); err != nil {
		t.Fatalf("unmarshal request payload: %v", err)
	}
	if request.Command == "" || !request.Async {
		t.Fatalf("unexpected request payload: %#v", request)
	}

	var result struct {
		Status    string   `json:"status"`
		ExitCode  int      `json:"exit_code"`
		Stdout    string   `json:"stdout"`
		Artifacts []string `json:"artifacts"`
	}
	if err := json.Unmarshal(rec.Result, &result); err != nil {
		t.Fatalf("unmarshal result payload: %v", err)
	}
	if result.Status != "completed" || result.ExitCode != 0 || result.Stdout != "done" {
		t.Fatalf("unexpected result payload: %#v", result)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0] != "out.txt" {
		t.Fatalf("unexpected artifacts: %#v", result.Artifacts)
	}
}

func TestExecuteToolCodeExecutionStatusAndCancel(t *testing.T) {
	fakeBwrap := writeFakeBubblewrap(t)
	prevRunner := defaultSandboxRunner
	defaultSandboxRunner = &sandbox.Runner{ResolveBubblewrap: func() (string, error) { return fakeBwrap, nil }}
	t.Cleanup(func() {
		defaultSandboxRunner = prevRunner
	})

	store := session.New(10)
	wsStore, err := workspace.New(t.TempDir(), true, 1<<20)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() {
		_ = ledger.Close()
	})

	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		RunLedger:      ledger,
		CodeExecutionConfig: CodeExecutionConfig{
			Enabled: true,
		},
	})

	queued := startAsyncCodeExecution(t, h, `{"command":"sleep 30","async":true}`)

	statusAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "code_execution.status",
		Arguments: json.RawMessage(`{"id":"` + queued.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(code_execution.status): %v", err)
	}
	statusRaw, _ := json.Marshal(statusAny)
	var status struct {
		ID      string         `json:"id"`
		Status  string         `json:"status"`
		Request map[string]any `json:"request"`
	}
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		t.Fatalf("unmarshal status result: %v", err)
	}
	if status.ID != queued.ID {
		t.Fatalf("unexpected status id: %#v", status)
	}
	if status.Status != string(runledger.StatusQueued) && status.Status != string(runledger.StatusRunning) {
		t.Fatalf("unexpected in-flight status: %#v", status)
	}
	if status.Request["command"] != "sleep 30" {
		t.Fatalf("unexpected status request: %#v", status.Request)
	}

	cancelAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "code_execution.cancel",
		Arguments: json.RawMessage(`{"id":"` + queued.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(code_execution.cancel): %v", err)
	}
	cancelRaw, _ := json.Marshal(cancelAny)
	var cancel struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(cancelRaw, &cancel); err != nil {
		t.Fatalf("unmarshal cancel result: %v", err)
	}
	if cancel.ID != queued.ID || cancel.Status != "canceling" {
		t.Fatalf("unexpected cancel result: %#v", cancel)
	}

	rec := waitForLedgerRecord(t, ledger, queued.ID)
	if rec.Status != runledger.StatusCanceled {
		t.Fatalf("expected canceled status, got %#v", rec)
	}

	finalStatusAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "code_execution.status",
		Arguments: json.RawMessage(`{"id":"` + queued.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(code_execution.status final): %v", err)
	}
	finalStatusRaw, _ := json.Marshal(finalStatusAny)
	var finalStatus struct {
		Status string         `json:"status"`
		Result map[string]any `json:"result"`
	}
	if err := json.Unmarshal(finalStatusRaw, &finalStatus); err != nil {
		t.Fatalf("unmarshal final status result: %v", err)
	}
	if finalStatus.Status != string(runledger.StatusCanceled) {
		t.Fatalf("unexpected final status: %#v", finalStatus)
	}
	if timedOut, _ := finalStatus.Result["timed_out"].(bool); timedOut {
		t.Fatalf("expected canceled result, got timeout: %#v", finalStatus.Result)
	}
}

func startAsyncCodeExecution(t *testing.T, h *Handler, args string) queuedCodeExecutionRun {
	t.Helper()
	queuedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "code_execution",
		Arguments: json.RawMessage(args),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(code_execution async): %v", err)
	}
	rawQueued, _ := json.Marshal(queuedAny)
	var queued queuedCodeExecutionRun
	if err := json.Unmarshal(rawQueued, &queued); err != nil {
		t.Fatalf("unmarshal queued result: %v", err)
	}
	if queued.ID == "" || queued.Kind != string(runledger.KindCodeExecution) || queued.Status != string(runledger.StatusQueued) || !queued.Async {
		t.Fatalf("unexpected queued result: %#v", queued)
	}
	return queued
}

func waitForLedgerRecord(t *testing.T, ledger *runledger.Store, id string) runledger.Record {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok := ledger.Get(id)
		if ok && (rec.Status == runledger.StatusCompleted || rec.Status == runledger.StatusErrored || rec.Status == runledger.StatusCanceled) {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for run ledger record %q", id)
	return runledger.Record{}
}

func writeFakeBubblewrap(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-bwrap.sh")
	content := `#!/bin/sh
workspace_src=""
tmp_src=""
chdir_path=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--bind)
		src="$2"
		dst="$3"
		if [ "$dst" = "/workspace" ]; then workspace_src="$src"; fi
		if [ "$dst" = "/tmp" ]; then tmp_src="$src"; fi
		shift 3
		;;
	--ro-bind)
		shift 3
		;;
	--setenv)
		export "$2=$3"
		shift 3
		;;
	--chdir)
		chdir_path="$2"
		shift 2
		;;
	--)
		shift
		if [ -n "$chdir_path" ]; then
			case "$chdir_path" in
			/workspace)
				cd "$workspace_src" || exit 91
				;;
			/workspace/*)
				rel="${chdir_path#/workspace/}"
				cd "$workspace_src/$rel" || exit 92
				;;
			/tmp)
				cd "$tmp_src" || exit 93
				;;
			esac
		fi
		exec "$@"
		;;
	*)
		shift
		;;
	esac
done
echo "missing -- terminator" >&2
exit 99
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake bubblewrap: %v", err)
	}
	return path
}
