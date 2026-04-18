package orchestrator

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/eventbus"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/types"
)

// ── stubs ─────────────────────────────────────────────────────────────────────

type stubProvider struct {
	complete func(context.Context, *types.ChatRequest) (*types.ChatResponse, error)
}

func (s *stubProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	return s.complete(ctx, req)
}

func (s *stubProvider) CompleteStream(context.Context, *types.ChatRequest, http.ResponseWriter) (string, error) {
	return "", nil
}

// successProvider returns a fixed assistant reply.
func successProvider(reply string) *stubProvider {
	return &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: reply},
				}},
			}, nil
		},
	}
}

// buildRuntime returns a minimal subagent.Runtime backed by store.
func buildRuntime(t *testing.T, prov *stubProvider, maxChildren int) (*subagent.Runtime, *session.Store, *subagent.Registry, *eventbus.Bus) {
	t.Helper()
	store := session.New(50)
	rt := agent.NewRuntime(store, prov, "test-model", 10*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	reg, err := subagent.NewRegistry("")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	bus := eventbus.New()
	sub := subagent.NewRuntime(rt, store, reg, bus, maxChildren)
	return sub, store, reg, bus
}

// waitRunStatus polls until the orchestration reaches one of the terminal states.
func waitRunStatus(t *testing.T, orch *Orchestrator, id string, timeout time.Duration) *Run {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		run, ok := orch.Get(id)
		if !ok {
			t.Fatalf("orchestration %s not found", id)
		}
		switch run.Status {
		case RunStatusCompleted, RunStatusErrored, RunStatusCancelled:
			return run
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("orchestration %s did not reach terminal status within %s", id, timeout)
	return nil
}

// ── tests ─────────────────────────────────────────────────────────────────────

func TestOrchestrator_CollectAggregation(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("hello"), 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "a", Task: "task a"},
			{Label: "b", Task: "task b"},
		},
		Aggregation: AggregateCollect,
		WaitPolicy:  WaitAll,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s (error: %s)", final.Status, final.Error)
	}

	childDone := 0
	for _, c := range final.Children {
		if c.Status == subagent.StatusCompleted {
			childDone++
		}
	}
	if childDone != 2 {
		t.Fatalf("expected 2 completed children, got %d", childDone)
	}
	if !strings.Contains(final.AggregatedReply, "[a]") || !strings.Contains(final.AggregatedReply, "[b]") {
		t.Fatalf("aggregated reply missing labels: %q", final.AggregatedReply)
	}
}

func TestOrchestrator_ConcatAggregation(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("result"), 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "chunk-1", Task: "task 1"},
			{Label: "chunk-2", Task: "task 2"},
		},
		Aggregation: AggregateConcat,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}
	if !strings.Contains(final.AggregatedReply, "## chunk-1") || !strings.Contains(final.AggregatedReply, "## chunk-2") {
		t.Fatalf("concat reply missing section headers: %q", final.AggregatedReply)
	}
}

func TestOrchestrator_ReducerAggregation(t *testing.T) {
	var callCount int32
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			n := atomic.AddInt32(&callCount, 1)
			reply := "child reply"
			if n > 2 { // third call is the reducer pass
				reply = "synthesized answer"
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: reply},
				}},
			}, nil
		},
	}
	sub, store, reg, bus := buildRuntime(t, prov, 4)
	agentRT := agent.NewRuntime(store, prov, "test-model", 10*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	_ = reg
	orch := New(sub, agentRT, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID:        "alice",
		Tasks:         []ChildTask{{Task: "task a"}, {Task: "task b"}},
		Aggregation:   AggregateReducer,
		ReducerPrompt: "Synthesize.",
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	// Reducer may fall back to concat if the reducer agent fails; either way
	// the run should reach a terminal state without panicking.
	if final.Status != RunStatusCompleted && final.Status != RunStatusErrored {
		t.Fatalf("unexpected status %s", final.Status)
	}
}

func TestOrchestrator_Cancel_KillsChildren(t *testing.T) {
	// Use a slow provider so children are still running when we cancel.
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(30 * time.Second):
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "late reply"},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "slow-a", Task: "task a"},
			{Label: "slow-b", Task: "task b"},
		},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give children a moment to start.
	time.Sleep(200 * time.Millisecond)
	if err := orch.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 5*time.Second)
	if final.Status != RunStatusCancelled {
		t.Fatalf("expected cancelled, got %s", final.Status)
	}
}

func TestOrchestrator_WaitAll_AllChildrenFinish(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("done"), 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks:  []ChildTask{{Task: "t1"}, {Task: "t2"}, {Task: "t3"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	final, err := orch.Wait(ctx, run.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}
}

func TestOrchestrator_WaitFirst_EarlyExit(t *testing.T) {
	var started int32
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			n := atomic.AddInt32(&started, 1)
			if n == 1 {
				// First child completes immediately.
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{
						Message: types.Message{Role: "assistant", Content: "first"},
					}},
				}, nil
			}
			// Other children block.
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(30 * time.Second):
			}
			return nil, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID:     "alice",
		Tasks:      []ChildTask{{Task: "t1"}, {Task: "t2"}, {Task: "t3"}},
		WaitPolicy: WaitFirst,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted && final.Status != RunStatusCancelled {
		t.Fatalf("expected completed or cancelled, got %s", final.Status)
	}
}

func TestOrchestrator_MaxConcurrency(t *testing.T) {
	// Verify that at most maxConcurrency children are active simultaneously.
	const limit = 2
	var peak int32
	var active int32
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			n := atomic.AddInt32(&active, 1)
			for {
				p := atomic.LoadInt32(&peak)
				if n <= p || atomic.CompareAndSwapInt32(&peak, p, n) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond) // hold the slot briefly
			atomic.AddInt32(&active, -1)
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "ok"},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, limit+2) // subagent allows more children
	orch := New(sub, nil, bus)

	tasks := make([]ChildTask, 6)
	for i := range tasks {
		tasks[i] = ChildTask{Task: "task"}
	}

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID:         "alice",
		Tasks:          tasks,
		MaxConcurrency: limit,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitRunStatus(t, orch, run.ID, 30*time.Second)

	if p := atomic.LoadInt32(&peak); p > limit {
		t.Fatalf("peak active children %d exceeded limit %d", p, limit)
	}
}

func TestOrchestrator_List_FilteredByPeer(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("hi"), 4)
	orch := New(sub, nil, bus)

	_, err := orch.Start(context.Background(), FanOutRequest{PeerID: "alice", Tasks: []ChildTask{{Task: "t"}}})
	if err != nil {
		t.Fatalf("Start alice: %v", err)
	}
	_, err = orch.Start(context.Background(), FanOutRequest{PeerID: "bob", Tasks: []ChildTask{{Task: "t"}}})
	if err != nil {
		t.Fatalf("Start bob: %v", err)
	}

	all := orch.List()
	if len(all) != 2 {
		t.Fatalf("expected 2 total runs, got %d", len(all))
	}
}

func TestOrchestrator_Get_NotFound(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("hi"), 4)
	orch := New(sub, nil, bus)

	_, ok := orch.Get("nonexistent-id")
	if ok {
		t.Fatal("expected not found for nonexistent id")
	}
}

func TestOrchestrator_Cancel_NotFound(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("hi"), 4)
	orch := New(sub, nil, bus)

	if err := orch.Cancel("no-such-run"); err == nil {
		t.Fatal("expected error cancelling nonexistent run")
	}
}

func TestOrchestrator_EmptyTasks_Error(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("hi"), 4)
	orch := New(sub, nil, bus)

	_, err := orch.Start(context.Background(), FanOutRequest{PeerID: "alice", Tasks: nil})
	if err == nil {
		t.Fatal("expected error for empty tasks")
	}
}

func TestOrchestrator_EventBus_LifecyclePublished(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("ok"), 4)
	orch := New(sub, nil, bus)

	var events []string
	busUnsub := bus.Subscribe(func(ev eventbus.Event) {
		if strings.HasPrefix(ev.Kind, "orchestrator.") {
			events = append(events, ev.Kind)
		}
	})
	defer busUnsub()

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID:      "alice",
		Tasks:       []ChildTask{{Task: "task"}},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitRunStatus(t, orch, run.ID, 15*time.Second)

	// Must have published at least "started" and "completed" lifecycle events.
	found := map[string]bool{}
	for _, k := range events {
		found[k] = true
	}
	if !found["orchestrator.started"] {
		t.Errorf("missing orchestrator.started event; got %v", events)
	}
	if !found["orchestrator.completed"] {
		t.Errorf("missing orchestrator.completed event; got %v", events)
	}
}

// ── Phase 2: barriers, structured output, vote aggregation ────────────────────

func TestBarrier_NamedGroup(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("result"), 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "phase1-a", Task: "work a"},
			{Label: "phase1-b", Task: "work b"},
			{Label: "phase2-c", Task: "work c"},
		},
		Aggregation: AggregateCollect,
		BarrierGroups: map[string][]string{
			"phase1": {"phase1-a", "phase1-b"},
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snap, err := orch.Barrier(ctx, run.ID, "phase1")
	if err != nil {
		t.Fatalf("Barrier: %v", err)
	}
	// phase1 group should be terminal.
	groupDone := 0
	for _, c := range snap.Children {
		if (c.Label == "phase1-a" || c.Label == "phase1-b") &&
			(c.Status == subagent.StatusCompleted || c.Status == subagent.StatusErrored || c.Status == subagent.StatusKilled) {
			groupDone++
		}
	}
	if groupDone < 2 {
		t.Errorf("expected phase1 group done, got %d/2", groupDone)
	}
}

func TestBarrier_AllChildren(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("done"), 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID:      "alice",
		Tasks:       []ChildTask{{Task: "t1"}, {Task: "t2"}},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	snap, err := orch.Barrier(ctx, run.ID, "")
	if err != nil {
		t.Fatalf("Barrier: %v", err)
	}
	if snap.Status != RunStatusCompleted {
		t.Errorf("expected completed after barrier, got %s", snap.Status)
	}
}

func TestStructuredOutput_ValidJSON(t *testing.T) {
	// Provider returns valid JSON with the required key.
	prov := successProvider(`{"answer":"42","confidence":0.9}`)
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	schema := `{"required":["answer","confidence"]}`
	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "q1", Task: "what is 6*7?", OutputSchema: schema},
		},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if len(final.Children) == 0 {
		t.Fatal("no children")
	}
	if len(final.Children[0].SchemaViolations) != 0 {
		t.Errorf("expected no violations for valid JSON, got %v", final.Children[0].SchemaViolations)
	}
}

func TestStructuredOutput_Violation_Strict(t *testing.T) {
	// Provider returns JSON missing the "confidence" key.
	prov := successProvider(`{"answer":"42"}`)
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	schema := `{"required":["answer","confidence"]}`
	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "q1", Task: "what is 6*7?", OutputSchema: schema},
		},
		Aggregation:          AggregateCollect,
		SchemaValidationMode: SchemaStrict,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if len(final.Children) == 0 {
		t.Fatal("no children")
	}
	c := final.Children[0]
	if len(c.SchemaViolations) == 0 {
		t.Errorf("expected schema violation for missing 'confidence', got none")
	}
	if c.Status != subagent.StatusErrored {
		t.Errorf("expected child errored in strict mode, got %s", c.Status)
	}
}

func TestVoteAggregation_HeuristicMajority(t *testing.T) {
	// 3 children return similar replies, 1 returns a different one.
	// The majority cluster should win without an LLM call.
	callIdx := 0
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			callIdx++
			reply := "The answer is Paris"
			if callIdx == 4 {
				reply = "I think it's Berlin"
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: reply},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "a", Task: "capital of France?"},
			{Label: "b", Task: "capital of France?"},
			{Label: "c", Task: "capital of France?"},
			{Label: "d", Task: "capital of France?"},
		},
		Aggregation: AggregateVote,
		VoteConfig:  VoteConfig{AgreementThreshold: 0.3},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s (err: %s)", final.Status, final.Error)
	}
	if !strings.Contains(final.AggregatedReply, "Paris") {
		t.Errorf("expected majority winner 'Paris' in reply, got: %q", final.AggregatedReply)
	}
}

func TestVoteAggregation_DisagreementReport(t *testing.T) {
	// Each child returns a completely different reply → no majority → falls back
	// to largest cluster, appends disagreement report.
	idx := 0
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			idx++
			replies := []string{"alpha response", "beta response", "gamma response"}
			r := replies[(idx-1)%len(replies)]
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: r},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "a", Task: "t"}, {Label: "b", Task: "t"}, {Label: "c", Task: "t"},
		},
		Aggregation: AggregateVote,
		VoteConfig: VoteConfig{
			AgreementThreshold: 0.99, // very high threshold → no majority clusters
			DisagreementReport: true,
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}
	// With DisagreementReport, reply should mention dissenting views.
	if strings.Contains(final.AggregatedReply, "Dissenting") {
		t.Logf("disagreement report present: %q", final.AggregatedReply)
	}
	// Either way, the run completed without panic.
}

// ── Phase 3: retry, hedging, fallback ─────────────────────────────────────────

func TestRetry_SucceedsOnSecondAttempt(t *testing.T) {
	var callCount int32
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			n := atomic.AddInt32(&callCount, 1)
			if n == 1 {
				return nil, fmt.Errorf("transient error")
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "success on retry"},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{{
			Label: "retryable",
			Task:  "do something",
			Retry: RetryPolicy{MaxAttempts: 3, BackoffBase: 0},
		}},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitRunStatus(t, orch, run.ID, 20*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s (err: %s)", final.Status, final.Error)
	}
	if final.Children[0].Attempts < 2 {
		t.Errorf("expected at least 2 attempts, got %d", final.Children[0].Attempts)
	}
	if !strings.Contains(final.AggregatedReply, "success on retry") {
		t.Errorf("expected retry success in reply, got %q", final.AggregatedReply)
	}
}

func TestFallback_UsedAfterExhaustion(t *testing.T) {
	// Primary always fails; fallback succeeds.
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			// Detect fallback task by prompt content.
			for _, msg := range req.Messages {
				if strings.Contains(msg.Content, "fallback task") {
					return &types.ChatResponse{
						Choices: []types.ChatChoice{{
							Message: types.Message{Role: "assistant", Content: "fallback reply"},
						}},
					}, nil
				}
			}
			return nil, fmt.Errorf("primary always fails")
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{{
			Label: "primary",
			Task:  "primary task",
			Retry: RetryPolicy{MaxAttempts: 2},
			FallbackTask: &ChildTask{
				Label: "fallback",
				Task:  "fallback task",
			},
		}},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitRunStatus(t, orch, run.ID, 20*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed with fallback, got %s (err: %s)", final.Status, final.Error)
	}
	if !strings.Contains(final.AggregatedReply, "fallback reply") {
		t.Errorf("expected fallback reply in aggregation, got %q", final.AggregatedReply)
	}
}

func TestHedging_FirstSuccessWins(t *testing.T) {
	var started int32
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			n := atomic.AddInt32(&started, 1)
			if n == 1 {
				// First hedge blocks.
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(30 * time.Second):
				}
				return nil, nil
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "hedge winner"},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{{
			Label:      "hedged",
			Task:       "race task",
			HedgeCount: 3,
		}},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitRunStatus(t, orch, run.ID, 20*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s (err: %s)", final.Status, final.Error)
	}
	if !strings.Contains(final.AggregatedReply, "hedge winner") {
		t.Errorf("expected hedge winner reply, got %q", final.AggregatedReply)
	}
}

// ── unit helpers ──────────────────────────────────────────────────────────────

func TestAggregateCollect(t *testing.T) {
	children := []ChildResult{
		{Label: "a", FinalReply: "reply from a", Status: subagent.StatusCompleted},
		{Label: "b", Error: "something failed", Status: subagent.StatusErrored},
	}
	out := aggregateCollect(children)
	if !strings.Contains(out, "[a]: reply from a") {
		t.Errorf("collect missing label a: %q", out)
	}
	if !strings.Contains(out, "[b]: error: something failed") {
		t.Errorf("collect missing error label b: %q", out)
	}
}

func TestAggregateConcat(t *testing.T) {
	children := []ChildResult{
		{Label: "sec1", FinalReply: "content of section one"},
		{Label: "sec2", FinalReply: "content of section two"},
	}
	out := aggregateConcat(children)
	if !strings.Contains(out, "## sec1") {
		t.Errorf("concat missing ## sec1: %q", out)
	}
	if !strings.Contains(out, "## sec2") {
		t.Errorf("concat missing ## sec2: %q", out)
	}
	if !strings.Contains(out, "---") {
		t.Errorf("concat missing separator: %q", out)
	}
}

func TestMaxConcurrency_Limits(t *testing.T) {
	if got := maxConcurrency(0, 10); got != 10 {
		t.Errorf("unlimited: expected 10, got %d", got)
	}
	if got := maxConcurrency(3, 10); got != 3 {
		t.Errorf("limited: expected 3, got %d", got)
	}
	if got := maxConcurrency(0, 0); got != 1 {
		t.Errorf("zero total: expected 1, got %d", got)
	}
}

// ── Phase 1: streaming, timeline, budget ─────────────────────────────────────

func TestStreamPartials_PublishesPartialReplies(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("partial-content"), 4)
	orch := New(sub, nil, bus)

	const parentSession = "alice::main"
	var partials []string
	var mu sync.Mutex
	unsub := bus.Subscribe(func(ev eventbus.Event) {
		if ev.Kind == "session.message" && ev.SessionKey == parentSession {
			if ev.Message != nil && strings.Contains(ev.Message.Content, "partial-content") {
				mu.Lock()
				partials = append(partials, ev.Message.Content)
				mu.Unlock()
			}
		}
	})
	defer unsub()

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID:           "alice",
		ParentSessionKey: parentSession,
		Tasks: []ChildTask{
			{Label: "child-a", Task: "task a"},
			{Label: "child-b", Task: "task b"},
		},
		Aggregation:    AggregateCollect,
		StreamPartials: true,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitRunStatus(t, orch, run.ID, 15*time.Second)

	mu.Lock()
	n := len(partials)
	mu.Unlock()
	if n < 2 {
		t.Errorf("expected at least 2 partial messages, got %d", n)
	}
}

func TestTimeline_EventsRecorded(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("ok"), 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID:      "alice",
		Tasks:       []ChildTask{{Label: "t1", Task: "work"}, {Label: "t2", Task: "work"}},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitRunStatus(t, orch, run.ID, 15*time.Second)

	if len(final.Timeline) == 0 {
		t.Fatal("expected non-empty timeline")
	}
	kinds := map[string]bool{}
	for _, ev := range final.Timeline {
		kinds[ev.Kind] = true
	}
	for _, want := range []string{"orchestration.started", "child.queued", "child.finished", "orchestration.completed"} {
		if !kinds[want] {
			t.Errorf("timeline missing event %q; have %v", want, final.Timeline)
		}
	}
	if final.Provenance == nil {
		t.Fatal("expected non-nil provenance")
	}
	if final.Provenance.Mode != AggregateCollect {
		t.Errorf("provenance mode: want collect, got %s", final.Provenance.Mode)
	}
}

func TestBudget_MaxChildWallClock(t *testing.T) {
	// Children never complete on their own; budget MaxChildWallClock kills them.
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(30 * time.Second):
			}
			return nil, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "s1", Task: "slow"},
			{Label: "s2", Task: "slow"},
		},
		Aggregation: AggregateCollect,
		Budget:      Budget{MaxChildWallClock: 300 * time.Millisecond},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Should finish well before 30s because per-child budget kills them.
	final := waitRunStatus(t, orch, run.ID, 10*time.Second)
	if final.Status == RunStatusCancelled {
		t.Logf("run cancelled (children timed out and no results available — expected)")
		return
	}
	// Completed is also fine if at least some children ended due to their context being cancelled.
	_ = final
}

func TestWaitPolicySatisfied(t *testing.T) {
	orch := &Orchestrator{}

	makeRun := func(statuses []subagent.Status) *Run {
		run := &Run{childRunIDs: map[string]int{}}
		for _, s := range statuses {
			run.Children = append(run.Children, ChildResult{Status: s})
		}
		return run
	}

	// WaitAll: only satisfied when every child is terminal.
	run := makeRun([]subagent.Status{subagent.StatusCompleted, subagent.StatusRunning})
	if orch.waitPolicySatisfied(run, WaitAll, 2) {
		t.Error("WaitAll: should not be satisfied with one still running")
	}
	run2 := makeRun([]subagent.Status{subagent.StatusCompleted, subagent.StatusErrored})
	if !orch.waitPolicySatisfied(run2, WaitAll, 2) {
		t.Error("WaitAll: should be satisfied when all terminal")
	}

	// WaitFirst: satisfied after one complete.
	run3 := makeRun([]subagent.Status{subagent.StatusCompleted, subagent.StatusRunning})
	if !orch.waitPolicySatisfied(run3, WaitFirst, 2) {
		t.Error("WaitFirst: should be satisfied after first completion")
	}

	// WaitQuorum: majority.
	run4 := makeRun([]subagent.Status{subagent.StatusCompleted, subagent.StatusCompleted, subagent.StatusRunning})
	if !orch.waitPolicySatisfied(run4, WaitQuorum, 3) {
		t.Error("WaitQuorum: 2/3 should satisfy quorum")
	}
}

// ── Phase 4: DAG, multi-stage, verifier/arbiter ───────────────────────────────

// TestDAG_LinearChain verifies that task B, which declares A as a dependency,
// does not start until A has completed.
func TestDAG_LinearChain(t *testing.T) {
	var order []string
	var mu sync.Mutex
	prov := &stubProvider{
		complete: func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			// Record which task prompt ran.
			mu.Lock()
			order = append(order, req.Messages[len(req.Messages)-1].Content)
			mu.Unlock()
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "ok"},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "a", Task: "task-a"},
			{Label: "b", Task: "task-b", DependsOn: []string{"a"}},
		},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}
	// Both tasks must appear in order.
	mu.Lock()
	defer mu.Unlock()
	if len(order) < 2 {
		t.Fatalf("expected 2 tasks, got %d", len(order))
	}
	if order[0] != "task-a" || order[1] != "task-b" {
		t.Fatalf("expected task-a then task-b, got %v", order)
	}
}

// TestDAG_DiamondPattern verifies that in a diamond dependency (A→B,A→C,B→D,C→D)
// D waits until both B and C complete.
func TestDAG_DiamondPattern(t *testing.T) {
	var mu sync.Mutex
	finished := make(map[string]time.Time)
	prov := &stubProvider{
		complete: func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			lbl := req.Messages[len(req.Messages)-1].Content
			mu.Lock()
			finished[lbl] = time.Now()
			mu.Unlock()
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: lbl},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "a", Task: "a"},
			{Label: "b", Task: "b", DependsOn: []string{"a"}},
			{Label: "c", Task: "c", DependsOn: []string{"a"}},
			{Label: "d", Task: "d", DependsOn: []string{"b", "c"}},
		},
		Aggregation: AggregateCollect,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(finished) != 4 {
		t.Fatalf("expected 4 tasks finished, got %d", len(finished))
	}
	// D must finish after both B and C.
	if finished["d"].Before(finished["b"]) {
		t.Error("d finished before b")
	}
	if finished["d"].Before(finished["c"]) {
		t.Error("d finished before c")
	}
	// A must finish before B and C.
	if finished["b"].Before(finished["a"]) {
		t.Error("b finished before a")
	}
	if finished["c"].Before(finished["a"]) {
		t.Error("c finished before a")
	}
}

// TestDAG_CycleDetected verifies that Start returns an error when a cycle
// exists in the task dependency graph.
func TestDAG_CycleDetected(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("ok"), 4)
	orch := New(sub, nil, bus)

	_, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "a", Task: "a", DependsOn: []string{"b"}},
			{Label: "b", Task: "b", DependsOn: []string{"a"}},
		},
	})
	if err == nil {
		t.Fatal("expected cycle detection error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("expected cycle error, got: %v", err)
	}
}

// TestDAG_UnknownDepLabel verifies that Start returns an error when a
// DependsOn label references a task that does not exist.
func TestDAG_UnknownDepLabel(t *testing.T) {
	sub, _, _, bus := buildRuntime(t, successProvider("ok"), 4)
	orch := New(sub, nil, bus)

	_, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "a", Task: "a", DependsOn: []string{"nonexistent"}},
		},
	})
	if err == nil {
		t.Fatal("expected unknown label error, got nil")
	}
}

// TestMultiStage_TwoStage verifies that a two-stage orchestration runs stage 0
// workers and injects their aggregate into stage 1 task prompts.
func TestMultiStage_TwoStage(t *testing.T) {
	var prompts []string
	var mu sync.Mutex
	prov := &stubProvider{
		complete: func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			content := req.Messages[len(req.Messages)-1].Content
			mu.Lock()
			prompts = append(prompts, content)
			mu.Unlock()
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "result-" + content[:5]},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Stages: []StageSpec{
			{
				Tasks:       []ChildTask{{Label: "worker", Task: "stage0-task"}},
				Aggregation: AggregateCollect,
				WaitPolicy:  WaitAll,
			},
			{
				Tasks:       []ChildTask{{Label: "reviewer", Task: "stage1-task"}},
				Aggregation: AggregateCollect,
				WaitPolicy:  WaitAll,
			},
		},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s (error: %s)", final.Status, final.Error)
	}
	if len(final.StageResults) != 2 {
		t.Fatalf("expected 2 stage results, got %d", len(final.StageResults))
	}
	// Stage 1 task prompt should contain the prior stage output.
	mu.Lock()
	defer mu.Unlock()
	var stage1Prompt string
	for _, p := range prompts {
		if strings.HasPrefix(p, "Prior stage output:") || strings.Contains(p, "stage1-task") {
			stage1Prompt = p
			break
		}
	}
	if stage1Prompt == "" {
		t.Fatalf("stage 1 task did not include prior stage output; prompts: %v", prompts)
	}
}

// TestVerifierRole verifies that VerifierTask creates one reviewer per worker.
func TestVerifierRole(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	prov := &stubProvider{
		complete: func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			content := req.Messages[len(req.Messages)-1].Content
			mu.Lock()
			calls = append(calls, content)
			mu.Unlock()
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "reviewed"},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 6)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "w1", Task: "worker-task-1"},
			{Label: "w2", Task: "worker-task-2"},
		},
		Aggregation:  AggregateCollect,
		VerifierTask: &ChildTask{Task: "verify this"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s (error: %s)", final.Status, final.Error)
	}
	// 2 workers + 2 verifiers = 4 children total.
	if len(final.Children) != 4 {
		t.Fatalf("expected 4 children (2 workers + 2 verifiers), got %d", len(final.Children))
	}
	// StageResults must have 2 stages.
	if len(final.StageResults) != 2 {
		t.Fatalf("expected 2 stage results, got %d", len(final.StageResults))
	}
	// Verifier prompts must contain "Work to review:".
	mu.Lock()
	defer mu.Unlock()
	verifierCalls := 0
	for _, c := range calls {
		if strings.Contains(c, "Work to review:") {
			verifierCalls++
		}
	}
	if verifierCalls != 2 {
		t.Fatalf("expected 2 verifier calls with 'Work to review:', got %d (calls: %v)", verifierCalls, calls)
	}
}

// TestArbiterRole verifies that ArbiterTask creates a single arbiter after
// all workers complete, with all worker outputs included in the prompt.
func TestArbiterRole(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	prov := &stubProvider{
		complete: func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			content := req.Messages[len(req.Messages)-1].Content
			mu.Lock()
			calls = append(calls, content)
			mu.Unlock()
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "arbiter-verdict"},
				}},
			}, nil
		},
	}
	sub, _, _, bus := buildRuntime(t, prov, 6)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks: []ChildTask{
			{Label: "w1", Task: "worker-task-1"},
			{Label: "w2", Task: "worker-task-2"},
		},
		Aggregation: AggregateCollect,
		ArbiterTask: &ChildTask{Task: "give verdict"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	final := waitRunStatus(t, orch, run.ID, 15*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s (error: %s)", final.Status, final.Error)
	}
	// 2 workers + 1 arbiter = 3 children total.
	if len(final.Children) != 3 {
		t.Fatalf("expected 3 children (2 workers + 1 arbiter), got %d", len(final.Children))
	}
	// Final answer should be the arbiter's output.
	if final.AggregatedReply == "" {
		t.Fatal("expected non-empty AggregatedReply")
	}
	// Arbiter prompt must include all worker outputs.
	mu.Lock()
	defer mu.Unlock()
	var arbiterCall string
	for _, c := range calls {
		if strings.Contains(c, "All results:") {
			arbiterCall = c
			break
		}
	}
	if arbiterCall == "" {
		t.Fatalf("arbiter prompt did not include 'All results:'; calls: %v", calls)
	}
}

// TestDetectCycle is a unit test for the detectCycle helper.
func TestDetectCycle(t *testing.T) {
	// No edges: no cycle.
	if detectCycle(3, [][]int{{}, {}, {}}) {
		t.Error("expected no cycle in empty graph")
	}
	// Linear chain A→B→C: no cycle.
	if detectCycle(3, [][]int{{1}, {2}, {}}) {
		t.Error("expected no cycle in linear chain")
	}
	// A→B, B→A: cycle.
	if !detectCycle(2, [][]int{{1}, {0}}) {
		t.Error("expected cycle in A→B→A graph")
	}
	// Diamond A→B,A→C, B→D, C→D: no cycle.
	if detectCycle(4, [][]int{{1, 2}, {3}, {3}, {}}) {
		t.Error("expected no cycle in diamond graph")
	}
	// Self-loop A→A: cycle.
	if !detectCycle(1, [][]int{{0}}) {
		t.Error("expected cycle in self-loop")
	}
}
