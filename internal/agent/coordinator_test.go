package agent_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

func TestCoordinator_StartAndWait(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}},
			}, nil
		},
		stream: func(_ context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
			return "", nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)

	record, err := coord.Start(agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if record.Status != agent.StatusQueued {
		t.Fatalf("expected queued status, got %s", record.Status)
	}
	final, err := coord.Wait(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.Status != agent.StatusCompleted {
		t.Fatalf("expected completed status, got %s", final.Status)
	}
	if final.Result == nil || final.Result.AssistantText != "ok" {
		t.Fatalf("unexpected result: %#v", final.Result)
	}
}

func TestCoordinator_SerializesRunsPerSession(t *testing.T) {
	store := session.New(20)
	var (
		mu      sync.Mutex
		running int
		maxSeen int
	)
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			mu.Lock()
			running++
			if running > maxSeen {
				maxSeen = running
			}
			mu.Unlock()
			time.Sleep(50 * time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}},
			}, nil
		},
		stream: func(_ context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
			return "", nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)

	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			_, err := coord.Run(context.Background(), agent.RunRequest{
				PeerID:   "peer",
				Scope:    agent.ScopeMain,
				Messages: []types.Message{{Role: "user", Content: "hello"}},
			})
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		})
	}
	wg.Wait()
	if maxSeen != 1 {
		t.Fatalf("expected serialized execution, max concurrent runs seen=%d", maxSeen)
	}
}

func TestCoordinator_Cancel(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(2 * time.Second):
				return &types.ChatResponse{
					Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "late"}}},
				}, nil
			}
		},
		stream: func(_ context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
			return "", nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", 3*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)

	record, err := coord.Start(agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	canceled, err := coord.Cancel(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if canceled.Status != agent.StatusCanceled {
		t.Fatalf("expected canceled status, got %s", canceled.Status)
	}
}

func TestCoordinator_LedgerReceivesRunTiming(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			time.Sleep(10 * time.Millisecond)
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}},
				Usage:   types.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
			}, nil
		},
		stream: func(_ context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
			return "", nil
		},
	}
	rt := agent.NewRuntime(store, prov, "model", time.Second, agent.RetryPolicy{MaxAttempts: 1})
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	defer ledger.Close()
	coord := agent.NewCoordinator(rt)
	coord.SetLedger(runledger.NewCoordinatorAdapter(ledger))

	record, err := coord.Start(agent.RunRequest{
		PeerID:   "peer",
		Scope:    agent.ScopeMain,
		Messages: []types.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	final, err := coord.Wait(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if final.Status != agent.StatusCompleted {
		t.Fatalf("expected completed status, got %s", final.Status)
	}

	got, ok := ledger.Get(record.ID)
	if !ok {
		t.Fatal("expected ledger record")
	}
	if got.Timing == nil {
		t.Fatal("expected timing breakdown on ledger record")
	}
	if got.Timing.ModelMs <= 0 {
		t.Errorf("model_ms: want > 0, got %d", got.Timing.ModelMs)
	}
	if got.Timing.TotalMs <= 0 {
		t.Errorf("total_ms: want > 0, got %d", got.Timing.TotalMs)
	}
	if got.Status != runledger.StatusCompleted {
		t.Errorf("unexpected ledger status: %s", got.Status)
	}

	// The coordinator's in-memory record mirrors the same breakdown.
	if final.Timing == nil {
		t.Fatal("expected timing breakdown on coordinator run record")
	}
	if final.Timing.ModelMs <= 0 || final.Timing.TotalMs <= 0 {
		t.Errorf("unexpected coordinator run timing: %+v", final.Timing)
	}
}
