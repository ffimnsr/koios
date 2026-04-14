package subagent

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/eventbus"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

type stubProvider struct {
	complete func(context.Context, *types.ChatRequest) (*types.ChatResponse, error)
}

func (s *stubProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	return s.complete(ctx, req)
}

func (s *stubProvider) CompleteStream(context.Context, *types.ChatRequest, http.ResponseWriter) (string, error) {
	return "", nil
}

func TestRuntime_SpawnCreatesStructuredSubTurn(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "done"},
				}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	reg, err := NewRegistry("")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	bus := eventbus.New()
	parent := reg.Spawn(SpawnRequest{
		PeerID:           "alice",
		ParentSessionKey: "alice",
		SourceSessionKey: "alice",
		Task:             "parent task",
	}, "alice::subagent::parent")
	subrt := NewRuntime(rt, store, reg, bus, 2)

	rec, err := subrt.Spawn(context.Background(), SpawnRequest{
		PeerID:           "alice",
		ParentRunID:      parent.ID,
		ParentSessionKey: "alice",
		SourceSessionKey: "alice",
		Task:             "child task",
		MaxChildren:      2,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	if rec.SubTurn.State != SubTurnQueued {
		t.Fatalf("expected queued subturn state, got %#v", rec.SubTurn)
	}
	if rec.SubTurn.ParentRunID != parent.ID || rec.SubTurn.ParentSessionKey != "alice" {
		t.Fatalf("unexpected subturn parent linkage: %#v", rec.SubTurn)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := subrt.Get(rec.ID)
		if ok && current.Status == StatusRunning && (!current.SubTurn.ReservedSlot || current.SubTurn.ActiveChildren < 1) {
			t.Fatalf("expected running subturn to reserve a parent slot, got %#v", current.SubTurn)
		}
		if ok && current.Status == StatusCompleted {
			if current.SubTurn.State != SubTurnCompleted {
				t.Fatalf("expected completed subturn state, got %#v", current.SubTurn)
			}
			if current.SubTurn.Steps < 1 {
				t.Fatalf("expected SubTurn to record at least one step, got %#v", current.SubTurn)
			}
			foundAgentEvent := false
			for _, ev := range current.Events {
				if strings.HasPrefix(ev.Type, "agent.") {
					foundAgentEvent = true
					break
				}
			}
			if !foundAgentEvent {
				t.Fatalf("expected agent lifecycle events, got %#v", current.Events)
			}
			updatedParent, ok := reg.Get(parent.ID)
			if !ok {
				t.Fatalf("expected parent record %q", parent.ID)
			}
			if len(updatedParent.Children) != 1 || updatedParent.Children[0] != rec.ID {
				t.Fatalf("expected parent to track child linkage, got %#v", updatedParent.Children)
			}
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("subagent %q did not complete in time", rec.ID)
}

func TestRuntime_SpawnEnforcesParentSessionConcurrency(t *testing.T) {
	store := session.New(20)
	gate := make(chan struct{})
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "done"},
				}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	reg, err := NewRegistry("")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	subrt := NewRuntime(rt, store, reg, nil, 1)

	first, err := subrt.Spawn(context.Background(), SpawnRequest{
		PeerID:           "alice",
		ParentSessionKey: "alice",
		SourceSessionKey: "alice",
		Task:             "child task 1",
		MaxChildren:      1,
	})
	if err != nil {
		t.Fatalf("Spawn(first): %v", err)
	}
	if first.SubTurn.ConcurrencyKey != "alice" {
		t.Fatalf("expected parent session concurrency key, got %#v", first.SubTurn)
	}

	second, err := subrt.Spawn(context.Background(), SpawnRequest{
		PeerID:           "alice",
		ParentSessionKey: "alice",
		SourceSessionKey: "alice",
		Task:             "child task 2",
		MaxChildren:      1,
	})
	if err != nil {
		t.Fatalf("Spawn(second): %v", err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	queued := false
	for time.Now().Before(deadline) {
		current, ok := subrt.Get(second.ID)
		if ok && current.Status == StatusQueued && !current.SubTurn.ReservedSlot {
			queued = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !queued {
		t.Fatalf("expected second subagent to remain queued while waiting for semaphore slot")
	}
	close(gate)

	waitDeadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(waitDeadline) {
		firstRec, firstOK := subrt.Get(first.ID)
		secondRec, secondOK := subrt.Get(second.ID)
		if firstOK && secondOK && firstRec.Status == StatusCompleted && secondRec.Status == StatusCompleted {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected queued subagents to complete after semaphore release")
}

func TestRuntime_AnnouncementAndReplyFlagsPublishViaBus(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "final child reply"},
				}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	reg, err := NewRegistry("")
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	bus := eventbus.New()
	var (
		mu            sync.Mutex
		sessionEvents []eventbus.Event
	)
	bus.Subscribe(func(ev eventbus.Event) {
		if ev.Kind != "session.message" {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		sessionEvents = append(sessionEvents, ev)
	})
	subrt := NewRuntime(rt, store, reg, bus, 2)

	rec, err := subrt.Spawn(context.Background(), SpawnRequest{
		PeerID:           "alice",
		ParentSessionKey: "alice",
		SourceSessionKey: "alice",
		Task:             "child task",
		ReplyBack:        true,
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := subrt.Get(rec.ID)
		if ok && current.Status == StatusCompleted {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	got := append([]eventbus.Event(nil), sessionEvents...)
	mu.Unlock()
	if len(got) < 2 {
		t.Fatalf("expected announcement and reply events, got %#v", got)
	}
	if got[0].Message == nil || !strings.Contains(got[0].Message.Content, "queued: child task") {
		t.Fatalf("expected announcement event first, got %#v", got)
	}
	if got[1].Message == nil || !strings.Contains(got[1].Message.Content, "final child reply") {
		t.Fatalf("expected reply event second, got %#v", got)
	}

	skipped, err := subrt.Spawn(context.Background(), SpawnRequest{
		PeerID:           "alice",
		ParentSessionKey: "alice",
		SourceSessionKey: "alice",
		Task:             "ANNOUNCE_SKIP REPLY_SKIP child task 2",
		ReplyBack:        true,
	})
	if err != nil {
		t.Fatalf("Spawn(skipped): %v", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := subrt.Get(skipped.ID)
		if ok && current.Status == StatusCompleted {
			if !current.AnnounceSkip || !current.ReplySkip {
				t.Fatalf("expected skip flags to persist on run record, got %#v", current)
			}
			if current.Task != "child task 2" {
				t.Fatalf("expected task flags to be stripped before execution, got %q", current.Task)
			}
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	mu.Lock()
	finalCount := len(sessionEvents)
	mu.Unlock()
	if finalCount != len(got) {
		t.Fatalf("expected skip flags to suppress announcement/reply events, got %#v", sessionEvents)
	}
}
