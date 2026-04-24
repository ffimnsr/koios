package tasks_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/tasks"
)

func newTestStore(t *testing.T) *tasks.Store {
	t.Helper()
	store, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestExtractCandidateInputs(t *testing.T) {
	inputs := tasks.ExtractCandidateInputs("Remember to send the release notes.\nAction item: follow up with design about the API mockups.\nPlease book the room.")
	if len(inputs) != 3 {
		t.Fatalf("inputs = %d, want 3", len(inputs))
	}
	if inputs[0].Title != "Send the release notes" {
		t.Fatalf("first title = %q", inputs[0].Title)
	}
	if inputs[1].Title != "Follow up with design about the API mockups" {
		t.Fatalf("second title = %q", inputs[1].Title)
	}
	if inputs[2].Title != "Book the room" {
		t.Fatalf("third title = %q", inputs[2].Title)
	}
}

func TestCandidateApprovalAndLifecycle(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	dueAt := time.Now().Add(2 * time.Hour).Unix()
	candidate, err := store.QueueCandidate(ctx, "alice", tasks.CandidateInput{Title: "Pay contractor", Details: "Send April invoice payment", Owner: "alice", DueAt: dueAt})
	if err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}
	approved, task, err := store.ApproveCandidate(ctx, "alice", candidate.ID, tasks.CandidatePatch{}, "confirmed")
	if err != nil {
		t.Fatalf("ApproveCandidate: %v", err)
	}
	if approved.Status != tasks.CandidateStatusApproved {
		t.Fatalf("candidate status = %q, want approved", approved.Status)
	}
	if task.Status != tasks.TaskStatusOpen {
		t.Fatalf("task status = %q, want open", task.Status)
	}
	if task.Owner != "alice" {
		t.Fatalf("task owner = %q, want alice", task.Owner)
	}

	assigned, err := store.AssignTask(ctx, "alice", task.ID, "finance")
	if err != nil {
		t.Fatalf("AssignTask: %v", err)
	}
	if assigned.Owner != "finance" {
		t.Fatalf("assigned owner = %q, want finance", assigned.Owner)
	}

	snoozedUntil := time.Now().Add(24 * time.Hour).Unix()
	snoozed, err := store.SnoozeTask(ctx, "alice", task.ID, snoozedUntil)
	if err != nil {
		t.Fatalf("SnoozeTask: %v", err)
	}
	if snoozed.Status != tasks.TaskStatusSnoozed {
		t.Fatalf("snoozed status = %q, want snoozed", snoozed.Status)
	}

	completed, err := store.CompleteTask(ctx, "alice", task.ID)
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}
	if completed.Status != tasks.TaskStatusCompleted {
		t.Fatalf("completed status = %q, want completed", completed.Status)
	}
	if completed.CompletedAt == 0 {
		t.Fatal("expected completed_at to be set")
	}

	reopened, err := store.ReopenTask(ctx, "alice", task.ID)
	if err != nil {
		t.Fatalf("ReopenTask: %v", err)
	}
	if reopened.Status != tasks.TaskStatusOpen {
		t.Fatalf("reopened status = %q, want open", reopened.Status)
	}
	if reopened.CompletedAt != 0 {
		t.Fatalf("reopened completed_at = %d, want 0", reopened.CompletedAt)
	}
}

func TestExtractAndQueueDeduplicatesExistingTasks(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	candidate, err := store.QueueCandidate(ctx, "alice", tasks.CandidateInput{Title: "Review the launch checklist"})
	if err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}
	if _, _, err := store.ApproveCandidate(ctx, "alice", candidate.ID, tasks.CandidatePatch{}, "confirmed"); err != nil {
		t.Fatalf("ApproveCandidate: %v", err)
	}

	queued, err := store.ExtractAndQueue(ctx, "alice", "Remember to review the launch checklist.\nRemember to book travel.", tasks.CandidateProvenance{CaptureKind: tasks.CandidateCaptureExternalExtract})
	if err != nil {
		t.Fatalf("ExtractAndQueue: %v", err)
	}
	if len(queued) != 1 {
		t.Fatalf("queued = %d, want 1", len(queued))
	}
	if queued[0].Title != "Book travel" {
		t.Fatalf("queued title = %q, want Book travel", queued[0].Title)
	}
}
