package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

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

func TestWebhookApply_CronScheduleCreatesOneShotJob(t *testing.T) {
	jobStore, err := scheduler.NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	store := session.New(20)
	sched := scheduler.New(jobStore, webhookStubProvider{}, store, nil, "model", 1)
	h := NewWebhookHTTPHandler(store, nil, nil, jobStore, sched, "token")

	err = h.apply(webhookEventRequest{
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

	if err := h.apply(webhookEventRequest{
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
