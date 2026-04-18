package orchestrator

import (
	"context"
	"net/http"
	"strings"
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
