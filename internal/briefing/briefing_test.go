package briefing_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/briefing"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/types"
)

func TestBuilderGenerateDailyBrief(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	calendarStore, err := calendar.New(filepath.Join(dir, "calendar.db"))
	if err != nil {
		t.Fatalf("calendar.New: %v", err)
	}
	defer calendarStore.Close()
	memoryStore, err := memory.New(filepath.Join(dir, "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	defer memoryStore.Close()
	taskStore, err := tasks.New(filepath.Join(dir, "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	defer taskStore.Close()

	icsPath := filepath.Join(dir, "agenda.ics")
	ics := strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:-//Koios//EN",
		"BEGIN:VEVENT",
		"UID:event-1",
		"DTSTAMP:20260424T090000Z",
		"DTSTART:20260424T150000Z",
		"DTEND:20260424T153000Z",
		"SUMMARY:Planning Sync",
		"END:VEVENT",
		"END:VCALENDAR",
	}, "\n")
	if err := os.WriteFile(icsPath, []byte(ics), 0o600); err != nil {
		t.Fatalf("write agenda.ics: %v", err)
	}
	enabled := true
	if _, err := calendarStore.CreateSource(ctx, "alice", calendar.SourceInput{Name: "Work", Path: icsPath, Enabled: &enabled}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	if _, err := taskStore.CreateWaitingOn(ctx, "alice", tasks.WaitingOnInput{Title: "Vendor reply on contract", WaitingFor: "vendor", FollowUpAt: time.Date(2026, 4, 23, 10, 0, 0, 0, time.UTC).Unix()}); err != nil {
		t.Fatalf("CreateWaitingOn: %v", err)
	}
	candidate, err := taskStore.QueueCandidate(ctx, "alice", tasks.CandidateInput{Title: "Ship the summary email"})
	if err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}
	if _, _, err := taskStore.ApproveCandidate(ctx, "alice", candidate.ID, tasks.CandidatePatch{}, "confirmed"); err != nil {
		t.Fatalf("ApproveCandidate: %v", err)
	}
	if _, err := memoryStore.QueueCandidate(ctx, "alice", "Remember: prefers short weekly writeups", memory.ChunkOptions{}); err != nil {
		t.Fatalf("memory QueueCandidate: %v", err)
	}
	if _, err := memoryStore.CreateEntity(ctx, "alice", memory.EntityKindProject, "Summer Launch", nil, "Blocked on vendor feedback", time.Date(2026, 4, 24, 8, 0, 0, 0, time.UTC).Unix()); err != nil {
		t.Fatalf("CreateEntity: %v", err)
	}

	history := []types.Message{{Role: "user", Content: "We need to send the vendor follow-up email today."}, {Role: "assistant", Content: "I should draft the project summary after the sync."}}
	builder := briefing.Builder{CalendarStore: calendarStore, MemoryStore: memoryStore, TaskStore: taskStore}
	report, err := builder.Generate(ctx, "alice", history, briefing.Options{Kind: "daily", Timezone: "UTC", Now: time.Date(2026, 4, 24, 9, 0, 0, 0, time.UTC).Unix()})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if report.Kind != "daily" {
		t.Fatalf("unexpected kind: %s", report.Kind)
	}
	if len(report.Agenda.Events) != 1 {
		t.Fatalf("expected one agenda event, got %d", len(report.Agenda.Events))
	}
	if len(report.StaleWaiting) != 1 {
		t.Fatalf("expected one stale waiting item, got %d", len(report.StaleWaiting))
	}
	if len(report.PendingMemoryCandidates) != 1 {
		t.Fatalf("expected one pending memory candidate, got %d", len(report.PendingMemoryCandidates))
	}
	if len(report.ActiveProjects) != 1 {
		t.Fatalf("expected one active project, got %d", len(report.ActiveProjects))
	}
	if len(report.RecentCommitments) == 0 {
		t.Fatal("expected recent commitments to be extracted")
	}
	if len(report.SuggestedActions) == 0 {
		t.Fatal("expected suggested actions")
	}
	text := briefing.RenderText(report)
	if !strings.Contains(text, "Daily brief") {
		t.Fatalf("expected daily brief heading, got: %s", text)
	}
	if !strings.Contains(text, "Planning Sync") {
		t.Fatalf("expected event in rendered text, got: %s", text)
	}
	if !strings.Contains(text, "Vendor reply on contract") {
		t.Fatalf("expected waiting item in rendered text, got: %s", text)
	}
}

func TestNormalizeKind(t *testing.T) {
	if _, err := briefing.NormalizeKind("monthly"); err == nil {
		t.Fatal("expected invalid kind error")
	}
	if kind, err := briefing.NormalizeKind("weekly"); err != nil || kind != briefing.KindWeekly {
		t.Fatalf("NormalizeKind weekly = %q, %v", kind, err)
	}
}
