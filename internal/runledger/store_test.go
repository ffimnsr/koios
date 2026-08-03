package runledger_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/runledger"
)

func TestAddGetUpdate(t *testing.T) {
	dir := t.TempDir()
	s, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC().Truncate(time.Second)
	rec := runledger.Record{
		ID:       "run-1",
		Kind:     runledger.KindAgent,
		PeerID:   "alice",
		Status:   runledger.StatusQueued,
		QueuedAt: now,
	}
	if err := s.Add(rec); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, ok := s.Get("run-1")
	if !ok {
		t.Fatal("Get: not found after Add")
	}
	if got.Status != runledger.StatusQueued {
		t.Errorf("status: want queued, got %s", got.Status)
	}

	// Duplicate Add should be a no-op.
	rec.Status = runledger.StatusRunning
	if err := s.Add(rec); err != nil {
		t.Fatalf("duplicate Add: %v", err)
	}
	got, _ = s.Get("run-1")
	if got.Status != runledger.StatusQueued {
		t.Error("duplicate Add should not change status")
	}

	// Update to running.
	started := now.Add(time.Second)
	if err := s.Update("run-1", func(r *runledger.Record) {
		r.Status = runledger.StatusRunning
		r.StartedAt = &started
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got, _ = s.Get("run-1")
	if got.Status != runledger.StatusRunning {
		t.Errorf("after Update: want running, got %s", got.Status)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(started) {
		t.Error("StartedAt not updated correctly")
	}

	// Update to completed.
	finished := started.Add(500 * time.Millisecond)
	if err := s.Update("run-1", func(r *runledger.Record) {
		r.Status = runledger.StatusCompleted
		r.FinishedAt = &finished
		r.Steps = 3
		r.PromptTokens = 100
		r.CompletionTokens = 50
	}); err != nil {
		t.Fatalf("Update to completed: %v", err)
	}
	got, _ = s.Get("run-1")
	if got.Status != runledger.StatusCompleted {
		t.Errorf("want completed, got %s", got.Status)
	}
	if got.Steps != 3 || got.PromptTokens != 100 || got.CompletionTokens != 50 {
		t.Errorf("steps/tokens not persisted: %+v", got)
	}
}

func TestListFilter(t *testing.T) {
	dir := t.TempDir()
	s, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	add := func(id, peer string, kind runledger.RunKind, status runledger.RunStatus) {
		t.Helper()
		if err := s.Add(runledger.Record{
			ID:       id,
			Kind:     kind,
			PeerID:   peer,
			Status:   status,
			QueuedAt: now,
		}); err != nil {
			t.Fatalf("Add %s: %v", id, err)
		}
	}
	add("a1", "alice", runledger.KindAgent, runledger.StatusCompleted)
	add("a2", "alice", runledger.KindSubagent, runledger.StatusCompleted)
	add("b1", "bob", runledger.KindAgent, runledger.StatusRunning)
	add("b2", "bob", runledger.KindCron, runledger.StatusCompleted)

	all := s.List(runledger.Filter{}, 0)
	if len(all) != 4 {
		t.Errorf("all: want 4, got %d", len(all))
	}

	alice := s.List(runledger.Filter{PeerID: "alice"}, 0)
	if len(alice) != 2 {
		t.Errorf("alice filter: want 2, got %d", len(alice))
	}

	agentOnly := s.List(runledger.Filter{Kind: runledger.KindAgent}, 0)
	if len(agentOnly) != 2 {
		t.Errorf("agent kind filter: want 2, got %d", len(agentOnly))
	}

	running := s.List(runledger.Filter{Status: runledger.StatusRunning}, 0)
	if len(running) != 1 || running[0].ID != "b1" {
		t.Errorf("running filter: want [b1], got %v", running)
	}

	limited := s.List(runledger.Filter{Limit: 2}, 0)
	if len(limited) != 2 {
		t.Errorf("limit filter: want 2, got %d", len(limited))
	}
}

func TestListRetainFor(t *testing.T) {
	dir := t.TempDir()
	s, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	old := time.Now().UTC().Add(-48 * time.Hour)
	new_ := time.Now().UTC()

	oldFinished := old.Add(time.Minute)
	if err := s.Add(runledger.Record{
		ID:         "old",
		Kind:       runledger.KindCron,
		PeerID:     "p",
		Status:     runledger.StatusCompleted,
		QueuedAt:   old,
		FinishedAt: &oldFinished,
	}); err != nil {
		t.Fatalf("Add old: %v", err)
	}
	if err := s.Add(runledger.Record{
		ID:       "new",
		Kind:     runledger.KindCron,
		PeerID:   "p",
		Status:   runledger.StatusCompleted,
		QueuedAt: new_,
	}); err != nil {
		t.Fatalf("Add new: %v", err)
	}

	// retain 24h: old record should be excluded.
	got := s.List(runledger.Filter{}, 24*time.Hour)
	if len(got) != 1 || got[0].ID != "new" {
		t.Errorf("retainFor 24h: want [new], got %v", got)
	}
}

func TestListOrdersByLatestActivity(t *testing.T) {
	dir := t.TempDir()
	s, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	base := time.Now().UTC().Truncate(time.Second)
	if err := s.Add(runledger.Record{
		ID:       "older-created",
		Kind:     runledger.KindAgent,
		PeerID:   "alice",
		Status:   runledger.StatusQueued,
		QueuedAt: base,
	}); err != nil {
		t.Fatalf("Add older-created: %v", err)
	}
	if err := s.Add(runledger.Record{
		ID:       "newer-created",
		Kind:     runledger.KindAgent,
		PeerID:   "alice",
		Status:   runledger.StatusQueued,
		QueuedAt: base.Add(10 * time.Second),
	}); err != nil {
		t.Fatalf("Add newer-created: %v", err)
	}

	finished := base.Add(20 * time.Second)
	if err := s.Update("older-created", func(r *runledger.Record) {
		r.Status = runledger.StatusCompleted
		r.FinishedAt = &finished
	}); err != nil {
		t.Fatalf("Update older-created: %v", err)
	}

	got := s.List(runledger.Filter{}, 0)
	if len(got) != 2 {
		t.Fatalf("want 2 records, got %d", len(got))
	}
	if got[0].ID != "older-created" {
		t.Fatalf("expected most recently updated record first, got %q", got[0].ID)
	}
}

func TestPersistenceReload(t *testing.T) {
	dir := t.TempDir()
	s, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	now := time.Now().UTC()
	if err := s.Add(runledger.Record{
		ID:       "persist-1",
		Kind:     runledger.KindCodeExecution,
		PeerID:   "alice",
		Status:   runledger.StatusQueued,
		Request:  []byte(`{"command":"go test ./...","async":true}`),
		QueuedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	started := now.Add(time.Second)
	if err := s.Update("persist-1", func(r *runledger.Record) {
		r.Status = runledger.StatusCompleted
		r.StartedAt = &started
		r.Result = []byte(`{"status":"completed","exit_code":0}`)
	}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Verify the JSONL file was written.
	data, err := os.ReadFile(filepath.Join(dir, "ledger.jsonl"))
	if err != nil {
		t.Fatalf("read ledger file: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ledger file is empty")
	}

	// Reopen and check the index reflects the latest state.
	s2, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, ok := s2.Get("persist-1")
	if !ok {
		t.Fatal("reopen: record not found")
	}
	if got.Status != runledger.StatusCompleted {
		t.Errorf("reopen: want completed, got %s", got.Status)
	}
	if got.Kind != runledger.KindCodeExecution {
		t.Errorf("reopen: want code_execution kind, got %s", got.Kind)
	}
	if string(got.Request) == "" || string(got.Result) == "" {
		t.Fatalf("reopen: expected request/result payloads, got %#v", got)
	}
}

func TestUpdateNotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()
	if err := s.Update("no-such-id", func(r *runledger.Record) {}); err == nil {
		t.Error("Update on missing ID should return error")
	}
}

func TestCompleteTiming(t *testing.T) {
	queued := time.Now().UTC().Truncate(time.Millisecond)
	started := queued.Add(250 * time.Millisecond)
	finished := started.Add(2 * time.Second)
	rec := runledger.Record{
		QueuedAt:   queued,
		StartedAt:  &started,
		FinishedAt: &finished,
	}

	// Caller-measured phases only; queue/total/finalize are derived.
	got := runledger.CompleteTiming(rec, runledger.Timing{ModelMs: 1500, ToolMs: 200, Retries: 1})
	if got == nil {
		t.Fatal("CompleteTiming: expected non-nil timing")
	}
	if got.QueueMs != 250 {
		t.Errorf("queue_ms: want 250, got %d", got.QueueMs)
	}
	if got.ModelMs != 1500 || got.ToolMs != 200 {
		t.Errorf("model/tool not preserved: %+v", got)
	}
	if got.TotalMs != 2250 {
		t.Errorf("total_ms: want 2250, got %d", got.TotalMs)
	}
	// exec (2000) - model (1500) - tool (200) = 300ms of finalize overhead.
	if got.FinalizeMs != 300 {
		t.Errorf("finalize_ms: want 300, got %d", got.FinalizeMs)
	}
	if got.Retries != 1 {
		t.Errorf("retries: want 1, got %d", got.Retries)
	}

	// Without caller-measured model/tool phases, finalize is not derived so
	// unknown execution is not mislabeled as finalization overhead.
	got = runledger.CompleteTiming(rec, runledger.Timing{})
	if got == nil {
		t.Fatal("CompleteTiming: expected non-nil timing")
	}
	if got.QueueMs != 250 || got.TotalMs != 2250 {
		t.Errorf("derived queue/total wrong: %+v", got)
	}
	if got.FinalizeMs != 0 || got.ModelMs != 0 || got.ToolMs != 0 {
		t.Errorf("finalize/model/tool should stay zero: %+v", got)
	}

	// Explicitly measured fields win over derivation.
	got = runledger.CompleteTiming(rec, runledger.Timing{QueueMs: 10, TotalMs: 500, ModelMs: 100})
	if got == nil {
		t.Fatal("CompleteTiming: expected non-nil timing")
	}
	if got.QueueMs != 10 || got.TotalMs != 500 {
		t.Errorf("explicit fields should win: %+v", got)
	}

	// No timestamps and no measurements → nil (legacy/short-lived runs).
	if got := runledger.CompleteTiming(runledger.Record{}, runledger.Timing{}); got != nil {
		t.Errorf("empty record should yield nil timing, got %+v", got)
	}
}

func TestAdapterPersistsTimingBreakdown(t *testing.T) {
	dir := t.TempDir()
	s, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	adapter := runledger.NewCoordinatorAdapter(s)
	queued := time.Now().UTC().Truncate(time.Millisecond)
	adapter.LedgerQueued("run-timing", "alice", "sess", "model", queued)
	started := queued.Add(time.Second)
	adapter.LedgerStarted("run-timing", started)
	finished := started.Add(3 * time.Second)
	adapter.LedgerFinished("run-timing", finished, "completed", "", 2, 100, 50, runledger.Timing{ModelMs: 2000, ToolMs: 500, Retries: 1})

	got, ok := s.Get("run-timing")
	if !ok {
		t.Fatal("record not found")
	}
	if got.Timing == nil {
		t.Fatal("expected timing on record")
	}
	if got.Timing.QueueMs != 1000 {
		t.Errorf("queue_ms: want 1000, got %d", got.Timing.QueueMs)
	}
	if got.Timing.ModelMs != 2000 || got.Timing.ToolMs != 500 {
		t.Errorf("model/tool not persisted: %+v", got.Timing)
	}
	if got.Timing.TotalMs != 4000 {
		t.Errorf("total_ms: want 4000, got %d", got.Timing.TotalMs)
	}
	// exec (3000) - model (2000) - tool (500) = 500ms finalize overhead.
	if got.Timing.FinalizeMs != 500 {
		t.Errorf("finalize_ms: want 500, got %d", got.Timing.FinalizeMs)
	}
	if got.Timing.Retries != 1 {
		t.Errorf("retries: want 1, got %d", got.Timing.Retries)
	}

	// Legacy records written without timing stay nil.
	if err := s.Add(runledger.Record{ID: "legacy", Kind: runledger.KindCron, PeerID: "alice", Status: runledger.StatusCompleted, QueuedAt: queued}); err != nil {
		t.Fatalf("Add legacy: %v", err)
	}
	legacy, ok := s.Get("legacy")
	if !ok {
		t.Fatal("legacy record not found")
	}
	if legacy.Timing != nil {
		t.Errorf("legacy record should have nil timing, got %+v", legacy.Timing)
	}
}

func TestTimingSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	s, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	queued := time.Now().UTC().Truncate(time.Millisecond)
	if err := s.Add(runledger.Record{
		ID:       "persist-timing",
		Kind:     runledger.KindAgent,
		PeerID:   "alice",
		Status:   runledger.StatusCompleted,
		QueuedAt: queued,
		Timing:   &runledger.Timing{QueueMs: 5, ModelMs: 1200, ToolMs: 300, TotalMs: 2000, Retries: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	s2, err := runledger.New(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()
	got, ok := s2.Get("persist-timing")
	if !ok {
		t.Fatal("reopen: record not found")
	}
	if got.Timing == nil || got.Timing.ModelMs != 1200 || got.Timing.TotalMs != 2000 || got.Timing.Retries != 2 {
		t.Errorf("timing not preserved across reload: %+v", got.Timing)
	}
}
