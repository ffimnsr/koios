package orchestrator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/types"
)

// TestOrchestratorSurvivesParentContextCancel verifies orchestration runs are
// detached from the invoking context: canceling the parent context after Start
// (as happens when the agent turn that called orchestrator.start ends) must
// not cancel the run — it completes normally and remains addressable by run ID.
func TestOrchestratorSurvivesParentContextCancel(t *testing.T) {
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			// Keep children in flight long enough that the parent cancel lands
			// while the orchestration is still running.
			time.Sleep(300 * time.Millisecond)
			return &types.ChatResponse{Choices: []types.ChatChoice{{
				Message: types.Message{Role: "assistant", Content: "done"},
			}}}, nil
		},
	}
	sub, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	parentCtx, cancelParent := context.WithCancel(context.Background())
	run, err := orch.Start(parentCtx, FanOutRequest{
		PeerID: "alice",
		Tasks:  []ChildTask{{Label: "a", Task: "task a"}, {Label: "b", Task: "task b"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	cancelParent() // the invoking agent turn ends

	final := waitRunStatus(t, orch, run.ID, 10*time.Second)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed after parent context cancel, got %s", final.Status)
	}
	if len(final.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(final.Children))
	}
	for _, c := range final.Children {
		if c.Status != subagent.StatusCompleted {
			t.Fatalf("expected child to complete, got %s", c.Status)
		}
	}
}

// TestOrchestratorRejectsExcessiveDepth verifies the explicit recursion bound:
// runs beyond MaxOrchestrationDepth are rejected at Start, while the maximum
// allowed depth is accepted. Children and reducer passes are tool-less agent
// runs today (they cannot call orchestrator.start), so this guard is the
// explicit cap that keeps any future tool-carrying child bounded.
func TestOrchestratorRejectsExcessiveDepth(t *testing.T) {
	sub, _, bus := buildRuntime(t, successProvider("done"), 4)
	orch := New(sub, nil, bus)

	_, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks:  []ChildTask{{Task: "task a"}},
		Depth:  MaxOrchestrationDepth + 1,
	})
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("expected a depth rejection, got %v", err)
	}

	// The maximum allowed depth still starts a run.
	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks:  []ChildTask{{Task: "task a"}},
		Depth:  MaxOrchestrationDepth,
	})
	if err != nil {
		t.Fatalf("max-depth Start: %v", err)
	}
	if err := orch.Cancel(run.ID); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
}

// TestOrchestratorDetectsChildCompletionViaEvents verifies child completion is
// observed through the event bus rather than the old 500ms polling loop: a
// child finishing at ~1.2s completes the run shortly after, well before the
// next poll boundary the old loop would have needed.
func TestOrchestratorDetectsChildCompletionViaEvents(t *testing.T) {
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			time.Sleep(1200 * time.Millisecond)
			return &types.ChatResponse{Choices: []types.ChatChoice{{
				Message: types.Message{Role: "assistant", Content: "done"},
			}}}, nil
		},
	}
	sub, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID: "alice",
		Tasks:  []ChildTask{{Task: "slow"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	start := time.Now()
	final := waitRunStatus(t, orch, run.ID, 5*time.Second)
	elapsed := time.Since(start)
	if final.Status != RunStatusCompleted {
		t.Fatalf("expected completed, got %s", final.Status)
	}
	// The child finishes at ~1.2s; event delivery should observe it within a
	// small overhead, while the old polling loop could not observe it before
	// ~1.5s.
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("child completion observed too late (%s): event waiting is not working", elapsed)
	}
}

// TestOrchestratorParentTimeoutStillBoundsRun verifies the explicit wall-clock
// timeout is still honored after detachment: a short Timeout cancels the run
// even though the parent context is alive.
func TestOrchestratorParentTimeoutStillBoundsRun(t *testing.T) {
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(30 * time.Second):
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{
				Message: types.Message{Role: "assistant", Content: "late reply"},
			}}}, nil
		},
	}
	sub, _, bus := buildRuntime(t, prov, 4)
	orch := New(sub, nil, bus)

	run, err := orch.Start(context.Background(), FanOutRequest{
		PeerID:     "alice",
		Tasks:      []ChildTask{{Task: "slow"}},
		Timeout:    200 * time.Millisecond,
		WaitPolicy: WaitAll,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final := waitRunStatus(t, orch, run.ID, 5*time.Second)
	if final.Status != RunStatusCancelled {
		t.Fatalf("expected cancelled after wall-clock timeout, got %s", final.Status)
	}
}
