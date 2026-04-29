package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/usage"
	"github.com/ffimnsr/koios/internal/workflow"
	"github.com/ffimnsr/koios/internal/workspace"
)

type capabilityTestProvider struct {
	defaultCaps types.ProviderCapabilities
	byModel     map[string]types.ProviderCapabilities
}

func (p capabilityTestProvider) Complete(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{}, nil
}

func (p capabilityTestProvider) CompleteStream(context.Context, *types.ChatRequest, http.ResponseWriter) (string, error) {
	return "", nil
}

func (p capabilityTestProvider) Capabilities(model string) types.ProviderCapabilities {
	if caps, ok := p.byModel[model]; ok {
		return caps
	}
	return p.defaultCaps
}

type workflowAgentStub struct {
	reply string
}

func (s workflowAgentStub) Run(_ context.Context, _ agent.RunRequest) (*agent.Result, error) {
	return &agent.Result{AssistantText: s.reply}, nil
}

func (s workflowAgentStub) Model() string { return "workflow-model" }

func TestRunToolsExposeUnifiedLedgerForProcess(t *testing.T) {
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

	statusAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "run.status",
		Arguments: json.RawMessage(`{"id":"` + started.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(run.status): %v", err)
	}
	statusRaw, _ := json.Marshal(statusAny)
	if !strings.Contains(string(statusRaw), `"kind":"background_process"`) {
		t.Fatalf("expected process kind in run.status, got %s", string(statusRaw))
	}

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "run.list",
		Arguments: json.RawMessage(`{"kind":"background_process","limit":5}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(run.list): %v", err)
	}
	listRaw, _ := json.Marshal(listAny)
	if !strings.Contains(string(listRaw), started.ID) {
		t.Fatalf("expected started process in run.list, got %s", string(listRaw))
	}

	cancelAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "run.cancel",
		Arguments: json.RawMessage(`{"id":"` + started.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(run.cancel): %v", err)
	}
	cancelRaw, _ := json.Marshal(cancelAny)
	if !strings.Contains(string(cancelRaw), `"status":"stopping"`) {
		t.Fatalf("expected stopping status from run.cancel, got %s", string(cancelRaw))
	}

	rec := waitForBackgroundProcessRecord(t, ledger, started.ID)
	if rec.Status != runledger.StatusCanceled {
		t.Fatalf("expected canceled process, got %#v", rec)
	}

	logsAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "run.logs",
		Arguments: json.RawMessage(`{"id":"` + started.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(run.logs): %v", err)
	}
	logsRaw, _ := json.Marshal(logsAny)
	if !strings.Contains(string(logsRaw), `"stdout"`) || !strings.Contains(string(logsRaw), `"stderr"`) {
		t.Fatalf("expected log payload from run.logs, got %s", string(logsRaw))
	}
}

func TestRunToolsExposeWorkflowRuns(t *testing.T) {
	store := session.New(10)
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })

	wfStore, err := workflow.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("workflow.NewStore: %v", err)
	}
	runner := workflow.NewRunner(wfStore)
	runner.SetLedger(ledger)
	runner.SetAgentRuntime(workflowAgentStub{reply: "done"}, nil)

	created, err := wfStore.Create(workflow.Workflow{
		Name:      "wf",
		PeerID:    "alice",
		FirstStep: "step-1",
		Steps: []workflow.Step{{
			ID:        "step-1",
			Kind:      workflow.StepKindAgentTurn,
			Message:   "hello",
			OnSuccess: "",
		}},
	})
	if err != nil {
		t.Fatalf("workflow create: %v", err)
	}
	run, err := runner.Start(context.Background(), created.ID, "alice")
	if err != nil {
		t.Fatalf("workflow start: %v", err)
	}
	waited := waitForWorkflowRun(t, wfStore, run.ID)
	if waited.Status != workflow.RunStatusCompleted {
		t.Fatalf("expected completed workflow run, got %#v", waited)
	}

	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		RunLedger:      ledger,
		WorkflowRunner: runner,
	})

	statusAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "run.status",
		Arguments: json.RawMessage(`{"id":"` + run.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(run.status workflow): %v", err)
	}
	statusRaw, _ := json.Marshal(statusAny)
	if !strings.Contains(string(statusRaw), `"kind":"workflow"`) {
		t.Fatalf("expected workflow kind in run.status, got %s", string(statusRaw))
	}

	logsAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "run.logs",
		Arguments: json.RawMessage(`{"id":"` + run.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(run.logs workflow): %v", err)
	}
	logsRaw, _ := json.Marshal(logsAny)
	if !strings.Contains(string(logsRaw), `"workflow_id":"`+created.ID+`"`) {
		t.Fatalf("expected workflow run payload in run.logs, got %s", string(logsRaw))
	}
}

func TestUsageAndModelTools(t *testing.T) {
	store := session.New(10)
	if err := store.SetPolicy("alice", session.SessionPolicy{ModelOverride: "review"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	usageStore := usage.New()
	usageStore.Add("alice", types.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15})
	usageStore.Add("bob", types.Usage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7})

	provider := capabilityTestProvider{
		defaultCaps: types.ProviderCapabilities{Name: "openai", SupportsStreaming: true, SupportsNativeTools: true, OpenAICompatibleWire: true},
		byModel: map[string]types.ProviderCapabilities{
			"fast-model":   {Name: "openai", SupportsStreaming: true, SupportsNativeTools: false, OpenAICompatibleWire: true},
			"review-model": {Name: "anthropic", SupportsStreaming: true, SupportsNativeTools: true, RequiresMaxTokens: true},
		},
	}
	h := NewHandler(store, provider, HandlerOptions{
		Model:      "default-model",
		Timeout:    5 * time.Second,
		UsageStore: usageStore,
		ToolPolicy: ToolPolicy{Profile: "coding"},
		ModelCatalog: ModelCatalog{
			Provider:                 "openai",
			BaseURL:                  "https://api.example.test",
			DefaultProfile:           "default",
			LightweightModel:         "fast-model",
			FallbackModels:           []string{"review", "backup-model"},
			LightweightWordThreshold: 15,
			Profiles: []ModelProfileInfo{{
				Name:     "review",
				Provider: "anthropic",
				Model:    "review-model",
				BaseURL:  "https://anthropic.example.test",
			}},
		},
	})

	currentAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "usage.current", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(usage.current): %v", err)
	}
	currentRaw, _ := json.Marshal(currentAny)
	if !strings.Contains(string(currentRaw), `"total_tokens":15`) {
		t.Fatalf("expected alice usage in usage.current, got %s", string(currentRaw))
	}

	historyAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "usage.history", Arguments: json.RawMessage(`{"limit":1}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(usage.history): %v", err)
	}
	historyRaw, _ := json.Marshal(historyAny)
	if !strings.Contains(string(historyRaw), `"count":1`) {
		t.Fatalf("expected limited usage history, got %s", string(historyRaw))
	}

	estimateAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "usage.estimate", Arguments: json.RawMessage(`{"text":"Summarize the build failure","include_tools":true,"expected_completion_tokens":64}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(usage.estimate): %v", err)
	}
	estimateRaw, _ := json.Marshal(estimateAny)
	if !strings.Contains(string(estimateRaw), `"estimated_total_tokens"`) {
		t.Fatalf("expected token estimate payload, got %s", string(estimateRaw))
	}

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "model.list", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(model.list): %v", err)
	}
	listRaw, _ := json.Marshal(listAny)
	if !strings.Contains(string(listRaw), `"fast-model"`) || !strings.Contains(string(listRaw), `"review-model"`) {
		t.Fatalf("expected configured models in model.list, got %s", string(listRaw))
	}

	capsAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "model.capabilities", Arguments: json.RawMessage(`{"model":"review"}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(model.capabilities): %v", err)
	}
	capsRaw, _ := json.Marshal(capsAny)
	if !strings.Contains(string(capsRaw), `"resolved_model":"review-model"`) || !strings.Contains(string(capsRaw), `"requires_max_tokens":true`) {
		t.Fatalf("expected review profile capabilities, got %s", string(capsRaw))
	}

	routeAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "model.route", Arguments: json.RawMessage(`{"text":"Ping","session_key":"alice"}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(model.route): %v", err)
	}
	routeRaw, _ := json.Marshal(routeAny)
	if !strings.Contains(string(routeRaw), `"selected_model":"review-model"`) || !strings.Contains(string(routeRaw), `"reason":"session_override"`) {
		t.Fatalf("expected session override routing, got %s", string(routeRaw))
	}
}

func waitForWorkflowRun(t *testing.T, store *workflow.Store, runID string) *workflow.Run {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		run, err := store.LoadRun(runID)
		if err == nil && run.Status != workflow.RunStatusPending && run.Status != workflow.RunStatusRunning {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for workflow run %q", runID)
	return nil
}
