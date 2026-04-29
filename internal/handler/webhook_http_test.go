package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

type webhookStubProvider struct{}

func (webhookStubProvider) Complete(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
	return &types.ChatResponse{
		Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}},
	}, nil
}

func (webhookStubProvider) CompleteStream(_ context.Context, _ *types.ChatRequest, _ http.ResponseWriter) (string, error) {
	return "ok", nil
}

func TestWebhookApply_CronScheduleCreatesOneShotJob(t *testing.T) {
	jobStore, err := scheduler.NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	store := session.New(20)
	sched := scheduler.New(jobStore, webhookStubProvider{}, store, nil, "model", 1)
	h := NewWebhookHTTPHandler(store, nil, nil, jobStore, sched, "token")

	_, err = h.apply(webhookEventRequest{
		Type:   "cron.schedule",
		PeerID: "peer-1",
		Name:   "webhook-job",
		Schedule: &scheduler.Schedule{
			Kind: scheduler.KindAt,
			At:   time.Now().UTC().Add(time.Minute).Format(time.RFC3339),
		},
		Payload: &scheduler.Payload{
			Kind: scheduler.PayloadSystemEvent,
			Text: "remember this",
		},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	jobs := jobStore.List("peer-1")
	if len(jobs) != 1 {
		t.Fatalf("expected one job, got %d", len(jobs))
	}
	if jobs[0].Name != "webhook-job" {
		t.Fatalf("unexpected job %#v", jobs[0])
	}
}

func TestWebhookApply_CronTriggerRunsJob(t *testing.T) {
	jobStore, err := scheduler.NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	store := session.New(20)
	sched := scheduler.New(jobStore, webhookStubProvider{}, store, nil, "model", 1)
	h := NewWebhookHTTPHandler(store, nil, nil, jobStore, sched, "token")

	job := &scheduler.Job{
		PeerID:    "peer-1",
		Name:      "trigger-me",
		Enabled:   true,
		Schedule:  scheduler.Schedule{Kind: scheduler.KindEvery, EveryMs: 60_000},
		Payload:   scheduler.Payload{Kind: scheduler.PayloadSystemEvent, Text: "triggered"},
		NextRunAt: time.Now().UTC().Add(time.Hour),
	}
	if err := jobStore.Add(job); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if _, err := h.apply(webhookEventRequest{
		Type:   "cron.trigger",
		PeerID: "peer-1",
		JobID:  job.JobID,
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		history := store.Get("peer-1").History()
		for _, msg := range history {
			if msg.Role == "system" && msg.Content != "" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected triggered cron job to append a session message, got %#v", store.Get("peer-1").History())
}

func TestWebhookAuthorized_Bearer(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "/v1/webhooks/events", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Authorization", "Bearer secret")
	if !webhookAuthorized(req, "secret") {
		t.Fatal("expected bearer authorization to succeed")
	}
}

func TestWebhookApply_AgentRunReturnsRunID(t *testing.T) {
	store := session.New(20)
	rt := agent.NewRuntime(store, webhookStubProvider{}, "model", 10*time.Second, agent.RetryPolicy{})
	coord := agent.NewCoordinator(rt)
	h := NewWebhookHTTPHandler(store, nil, nil, nil, nil, "token")
	h.SetAgentCoordinator(coord, nil)

	runID, err := h.apply(webhookEventRequest{
		Type:   "agent.run",
		PeerID: "peer-1",
		Prompt: "say hello",
	})
	if err != nil {
		t.Fatalf("apply agent.run: %v", err)
	}
	if runID == "" {
		t.Fatal("expected a non-empty run_id from agent.run")
	}
}

func TestWebhookApply_AgentRunRequiresPrompt(t *testing.T) {
	store := session.New(20)
	rt := agent.NewRuntime(store, webhookStubProvider{}, "model", 10*time.Second, agent.RetryPolicy{})
	coord := agent.NewCoordinator(rt)
	h := NewWebhookHTTPHandler(store, nil, nil, nil, nil, "token")
	h.SetAgentCoordinator(coord, nil)

	_, err := h.apply(webhookEventRequest{
		Type:   "agent.run",
		PeerID: "peer-1",
		Prompt: "",
	})
	if err == nil {
		t.Fatal("expected error when prompt is empty")
	}
}

func TestWebhookApply_AgentRunRequiresCoordinator(t *testing.T) {
	store := session.New(20)
	h := NewWebhookHTTPHandler(store, nil, nil, nil, nil, "token")
	// no coordinator set

	_, err := h.apply(webhookEventRequest{
		Type:   "agent.run",
		PeerID: "peer-1",
		Prompt: "hello",
	})
	if err == nil {
		t.Fatal("expected error when coordinator is nil")
	}
}

func TestWebhookApply_SessionWakeReturnsRunID(t *testing.T) {
	store := session.New(20)
	rt := agent.NewRuntime(store, webhookStubProvider{}, "model", 10*time.Second, agent.RetryPolicy{})
	coord := agent.NewCoordinator(rt)
	h := NewWebhookHTTPHandler(store, nil, nil, nil, nil, "token")
	h.SetAgentCoordinator(coord, nil)

	runID, err := h.apply(webhookEventRequest{
		Type:   "session.wake",
		PeerID: "peer-1",
		Prompt: "reminder: check your schedule",
	})
	if err != nil {
		t.Fatalf("apply session.wake: %v", err)
	}
	if runID == "" {
		t.Fatal("expected a non-empty run_id from session.wake")
	}
}

func TestWebhookApply_SessionWakePersistsToMainSession(t *testing.T) {
	store := session.New(20)
	rt := agent.NewRuntime(store, webhookStubProvider{}, "model", 10*time.Second, agent.RetryPolicy{})
	coord := agent.NewCoordinator(rt)
	h := NewWebhookHTTPHandler(store, nil, nil, nil, nil, "token")
	h.SetAgentCoordinator(coord, nil)

	runID, err := h.apply(webhookEventRequest{
		Type:   "session.wake",
		PeerID: "peer-1",
		Prompt: "wake up and check the news",
	})
	if err != nil {
		t.Fatalf("apply session.wake: %v", err)
	}
	if runID == "" {
		t.Fatal("expected a non-empty run_id from session.wake")
	}

	// Wait for the async run to complete and verify the assistant reply
	// was persisted to the main session history (not an isolated session).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		history := store.Get("peer-1::main").History()
		for _, msg := range history {
			if msg.Role == "assistant" && msg.Content != "" {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected session.wake to persist assistant reply to main session, got %#v", store.Get("peer-1::main").History())
}

func TestWebhookApply_SessionWakeRequiresPrompt(t *testing.T) {
	store := session.New(20)
	rt := agent.NewRuntime(store, webhookStubProvider{}, "model", 10*time.Second, agent.RetryPolicy{})
	coord := agent.NewCoordinator(rt)
	h := NewWebhookHTTPHandler(store, nil, nil, nil, nil, "token")
	h.SetAgentCoordinator(coord, nil)

	_, err := h.apply(webhookEventRequest{
		Type:   "session.wake",
		PeerID: "peer-1",
		Prompt: "",
	})
	if err == nil {
		t.Fatal("expected error when prompt is empty for session.wake")
	}
}

func TestWebhookApply_SessionWakeRequiresCoordinator(t *testing.T) {
	store := session.New(20)
	h := NewWebhookHTTPHandler(store, nil, nil, nil, nil, "token")
	// no coordinator set

	_, err := h.apply(webhookEventRequest{
		Type:   "session.wake",
		PeerID: "peer-1",
		Prompt: "hello",
	})
	if err == nil {
		t.Fatal("expected error when coordinator is nil for session.wake")
	}
}
