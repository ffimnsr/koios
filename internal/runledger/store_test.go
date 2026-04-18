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
		Kind:     runledger.KindAgent,
		PeerID:   "alice",
		Status:   runledger.StatusQueued,
		QueuedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	started := now.Add(time.Second)
	if err := s.Update("persist-1", func(r *runledger.Record) {
		r.Status = runledger.StatusCompleted
		r.StartedAt = &started
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
