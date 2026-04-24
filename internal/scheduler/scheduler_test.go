package scheduler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/ffimnsr/koios/internal/presence"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/types"
)

var testParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

type stubProvider struct {
	complete func(context.Context, *types.ChatRequest) (*types.ChatResponse, error)
}

func (s *stubProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	return s.complete(ctx, req)
}

// ── calcNextRun ──────────────────────────────────────────────────────────────

func TestCalcNextRun_At(t *testing.T) {
	job := &Job{JobID: "at-1", Schedule: Schedule{Kind: KindAt, At: "2030-06-01T12:00:00Z"}}
	got, err := calcNextRun(job, time.Now().UTC(), &testParser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCalcNextRun_At_NoTimezone(t *testing.T) {
	job := &Job{JobID: "at-notz", Schedule: Schedule{Kind: KindAt, At: "2030-06-01T12:00:00"}}
	got, err := calcNextRun(job, time.Now().UTC(), &testParser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCalcNextRun_At_Invalid(t *testing.T) {
	job := &Job{JobID: "bad-at", Schedule: Schedule{Kind: KindAt, At: "not-a-date"}}
	if _, err := calcNextRun(job, time.Now().UTC(), &testParser); err == nil {
		t.Fatal("expected error for invalid timestamp")
	}
}

func TestCalcNextRun_Every(t *testing.T) {
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	job := &Job{JobID: "ev-1", Schedule: Schedule{Kind: KindEvery, EveryMs: 60_000}}
	got, err := calcNextRun(job, from, &testParser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(from.Add(time.Minute)) {
		t.Errorf("got %v, want %v", got, from.Add(time.Minute))
	}
}

func TestCalcNextRun_Every_ZeroMs(t *testing.T) {
	job := &Job{JobID: "ev-zero", Schedule: Schedule{Kind: KindEvery, EveryMs: 0}}
	if _, err := calcNextRun(job, time.Now().UTC(), &testParser); err == nil {
		t.Fatal("expected error for zero every_ms")
	}
}

func TestCalcNextRun_Cron(t *testing.T) {
	from := time.Date(2025, 1, 1, 0, 0, 30, 0, time.UTC)
	job := &Job{JobID: "cron-1", Schedule: Schedule{Kind: KindCron, Expr: "* * * * *"}}
	got, err := calcNextRun(job, from, &testParser)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := time.Date(2025, 1, 1, 0, 1, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCalcNextRun_Cron_InvalidExpr(t *testing.T) {
	job := &Job{JobID: "bad-expr", Schedule: Schedule{Kind: KindCron, Expr: "not-valid"}}
	if _, err := calcNextRun(job, time.Now().UTC(), &testParser); err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestCalcNextRun_Cron_InvalidTimezone(t *testing.T) {
	job := &Job{JobID: "bad-tz", Schedule: Schedule{Kind: KindCron, Expr: "* * * * *", Tz: "Not/ATimezone"}}
	if _, err := calcNextRun(job, time.Now().UTC(), &testParser); err == nil {
		t.Fatal("expected error for invalid timezone")
	}
}

func TestCalcNextRun_Stagger_Deterministic(t *testing.T) {
	from := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	job := &Job{JobID: "stagger", Schedule: Schedule{Kind: KindEvery, EveryMs: 60_000, StaggerMs: 5000}}
	a, _ := calcNextRun(job, from, &testParser)
	b, _ := calcNextRun(job, from, &testParser)
	if !a.Equal(b) {
		t.Errorf("stagger not deterministic: %v vs %v", a, b)
	}
	base := from.Add(time.Minute)
	if a.Before(base) || a.After(base.Add(5*time.Second)) {
		t.Errorf("got %v, expected in [%v, %v]", a, base, base.Add(5*time.Second))
	}
}

func TestSchedulerRunAgentTurn_UsesProfileStandingOrders(t *testing.T) {
	store, err := NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	sessionStore := session.New(10)
	standingStore, err := standing.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("standing.NewStore: %v", err)
	}
	if _, err := standingStore.SaveDocument(&standing.Document{
		PeerID:  "peer-1",
		Content: "Base standing order",
		Profiles: map[string]standing.Profile{
			"focus": {Content: "Focus profile standing order"},
		},
	}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	prov := &stubProvider{complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
		if len(req.Messages) == 0 || !strings.Contains(req.Messages[0].Content, "Focus profile standing order") {
			return nil, errors.New("profile standing order not injected")
		}
		return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}, nil
	}}
	sched := New(store, prov, sessionStore, standing.NewManager(standingStore, ""), "test-model", 1)
	job := &Job{
		JobID:   "job-1",
		PeerID:  "peer-1",
		Name:    "focus",
		Payload: Payload{Kind: PayloadAgentTurn, Message: "do work", Profile: "focus"},
	}
	if _, err := sched.runAgentTurn(context.Background(), job); err != nil {
		t.Fatalf("runAgentTurn: %v", err)
	}
}

func TestCalcNextRun_UnknownKind(t *testing.T) {
	job := &Job{JobID: "unk", Schedule: Schedule{Kind: "weekly"}}
	if _, err := calcNextRun(job, time.Now().UTC(), &testParser); err == nil {
		t.Fatal("expected error for unknown schedule kind")
	}
}

// ── backoffDuration ──────────────────────────────────────────────────────────

func TestBackoffDuration_Recurring(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{1, 30 * time.Second},
		{2, 1 * time.Minute},
		{3, 5 * time.Minute},
		{4, 15 * time.Minute},
		{5, 60 * time.Minute},
		{100, 60 * time.Minute},
	}
	for _, tc := range cases {
		if got := backoffDuration(tc.n, KindEvery); got != tc.want {
			t.Errorf("n=%d: got %v, want %v", tc.n, got, tc.want)
		}
	}
}

func TestBackoffDuration_OneShot(t *testing.T) {
	cases := []struct {
		n    int
		want time.Duration
	}{
		{1, 30 * time.Second},
		{2, 1 * time.Minute},
		{3, 5 * time.Minute},
		{10, 5 * time.Minute},
	}
	for _, tc := range cases {
		if got := backoffDuration(tc.n, KindAt); got != tc.want {
			t.Errorf("n=%d: got %v, want %v", tc.n, got, tc.want)
		}
	}
}

// ── isPermanentError ─────────────────────────────────────────────────────────

func TestIsPermanentError(t *testing.T) {
	cases := []struct {
		err       error
		permanent bool
	}{
		{nil, false},
		{errors.New("401 unauthorized"), true},
		{errors.New("403 forbidden"), true},
		{errors.New("invalid api key"), true},
		{errors.New("auth failed"), true},
		{errors.New("429 too many requests"), false},
		{errors.New("rate limit exceeded"), false},
		{errors.New("overloaded"), false},
		{errors.New("timeout"), false},
		{errors.New("connection refused"), false},
		{errors.New("503 server_error"), false},
		{errors.New("some completely unknown error"), true},
	}
	for _, tc := range cases {
		if got := isPermanentError(tc.err); got != tc.permanent {
			t.Errorf("err=%v: got permanent=%v, want %v", tc.err, got, tc.permanent)
		}
	}
}

// ── staggerOffset ────────────────────────────────────────────────────────────

func TestStaggerOffset_ZeroMs(t *testing.T) {
	if got := staggerOffset("job", 0); got != 0 {
		t.Errorf("expected 0 for zero staggerMs, got %v", got)
	}
}

func TestStaggerOffset_InRange(t *testing.T) {
	const ms = int64(5000)
	off := staggerOffset("some-job", ms)
	if off < 0 || off >= time.Duration(ms)*time.Millisecond {
		t.Errorf("offset %v out of range [0, %v)", off, time.Duration(ms)*time.Millisecond)
	}
}

// ── JobStore ─────────────────────────────────────────────────────────────────

func TestJobStore_AddListGetRemove(t *testing.T) {
	store, _ := NewJobStore(t.TempDir())
	job := &Job{
		PeerID:   "peer-1",
		Name:     "test job",
		Enabled:  true,
		Schedule: Schedule{Kind: KindEvery, EveryMs: 10_000},
		Payload:  Payload{Kind: PayloadSystemEvent, Text: "ping"},
	}
	if err := store.Add(job); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if job.JobID == "" {
		t.Fatal("JobID not set after Add")
	}
	list := store.List("peer-1")
	if len(list) != 1 || list[0].Name != "test job" {
		t.Fatalf("List returned unexpected: %v", list)
	}
	if store.Get(job.JobID) == nil {
		t.Fatal("Get returned nil")
	}
	if err := store.Remove(job.JobID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if store.Get(job.JobID) != nil {
		t.Fatal("job still present after Remove")
	}
}

func TestJobStore_Persistence(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewJobStore(dir)
	job := &Job{
		PeerID:   "peer-2",
		Name:     "persisted",
		Enabled:  true,
		Schedule: Schedule{Kind: KindEvery, EveryMs: 60_000},
		Payload:  Payload{Kind: PayloadAgentTurn, Message: "do a thing"},
	}
	if err := store.Add(job); err != nil {
		t.Fatalf("Add: %v", err)
	}
	store2, err := NewJobStore(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if r := store2.Get(job.JobID); r == nil || r.Name != "persisted" {
		t.Fatalf("reload: got %v", r)
	}
}

func TestJobStore_UpdateNotFound(t *testing.T) {
	store, _ := NewJobStore(t.TempDir())
	if err := store.Update(&Job{JobID: "nonexistent"}); err == nil {
		t.Fatal("expected error updating nonexistent job")
	}
}

func TestJobStore_RunRecordRoundTrip(t *testing.T) {
	store, _ := NewJobStore(t.TempDir())
	rec := RunRecord{
		RunID:     "run-1",
		JobID:     "job-1",
		StartedAt: time.Now().UTC().Truncate(time.Second),
		EndedAt:   time.Now().UTC().Truncate(time.Second),
		Status:    RunOK,
		Output:    "all good",
	}
	if err := store.AppendRunRecord(rec); err != nil {
		t.Fatalf("AppendRunRecord: %v", err)
	}
	records, err := store.ReadRunRecords("job-1", 10)
	if err != nil {
		t.Fatalf("ReadRunRecords: %v", err)
	}
	if len(records) != 1 || records[0].RunID != "run-1" {
		t.Fatalf("unexpected records: %v", records)
	}
}

func TestSchedulerDispatch_DefersActivePeerJobs(t *testing.T) {
	jobStore, err := NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	store := session.New(20)
	presenceMgr := presence.NewManager(time.Minute)
	presenceMgr.Set("peer-1", "online", false, "test")

	job := &Job{
		PeerID:    "peer-1",
		Name:      "active-aware",
		Enabled:   true,
		Schedule:  Schedule{Kind: KindEvery, EveryMs: 60_000},
		Payload:   Payload{Kind: PayloadSystemEvent, Text: "ping"},
		Dispatch:  DispatchPolicy{DeferIfActive: true},
		NextRunAt: time.Now().UTC().Add(-time.Second),
	}
	if err := jobStore.Add(job); err != nil {
		t.Fatalf("Add: %v", err)
	}

	sched := New(jobStore, &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			t.Fatal("provider should not be called when job is deferred")
			return nil, nil
		},
	}, store, nil, "model", 1)
	sched.SetPresence(presenceMgr)

	before := time.Now().UTC()
	sched.dispatch(context.Background())

	got := jobStore.Get(job.JobID)
	if got == nil {
		t.Fatal("expected job to remain present")
	}
	if !got.NextRunAt.After(before) {
		t.Fatalf("expected deferred next_run_at after %v, got %v", before, got.NextRunAt)
	}
	records, err := jobStore.ReadRunRecords(job.JobID, 10)
	if err != nil {
		t.Fatalf("ReadRunRecords: %v", err)
	}
	if len(records) != 1 || records[0].Status != RunSkip || !strings.Contains(records[0].Error, "active") {
		t.Fatalf("unexpected run records: %#v", records)
	}
}

func TestSchedulerRunAgentTurn_PreloadsExternalContent(t *testing.T) {
	var captured *types.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("latest external context"))
	}))
	defer server.Close()

	jobStore, err := NewJobStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJobStore: %v", err)
	}
	store := session.New(20)
	sched := New(jobStore, &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			captured = req
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "done"}}},
			}, nil
		},
	}, store, nil, "model", 1)

	job := &Job{
		JobID:   "job-1",
		PeerID:  "peer-1",
		Name:    "preload",
		Payload: Payload{Kind: PayloadAgentTurn, Message: "summarize it", PreloadURLs: []string{server.URL}},
	}
	out, err := sched.runAgentTurn(context.Background(), job)
	if err != nil {
		t.Fatalf("runAgentTurn: %v", err)
	}
	if out != "done" {
		t.Fatalf("unexpected output %q", out)
	}
	if captured == nil {
		t.Fatal("expected provider request to be captured")
	}
	found := false
	for _, msg := range captured.Messages {
		if msg.Role == "system" && strings.Contains(msg.Content, "latest external context") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected preloaded content in messages, got %#v", captured.Messages)
	}
}

func TestJobStore_ReadRunRecords_Missing(t *testing.T) {
	store, _ := NewJobStore(t.TempDir())
	records, err := store.ReadRunRecords("nonexistent", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if records != nil {
		t.Errorf("expected nil, got %v", records)
	}
}
