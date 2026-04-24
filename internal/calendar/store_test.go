package calendar

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAgendaToday_LocalAndRemoteSources(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "calendar.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	todayICS := "BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nUID:local-1\nSUMMARY:Local planning block\nDTSTART:20260424T090000Z\nDTEND:20260424T100000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	localPath := filepath.Join(t.TempDir(), "local.ics")
	if err := os.WriteFile(localPath, []byte(todayICS), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	remoteICS := "BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nUID:remote-1\nSUMMARY:Remote standup\nDTSTART:20260424T110000Z\nDTEND:20260424T113000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(remoteICS))
	}))
	t.Cleanup(server.Close)

	if _, err := store.CreateSource(context.Background(), "alice", SourceInput{Name: "Local", Path: localPath}); err != nil {
		t.Fatalf("CreateSource local: %v", err)
	}
	if _, err := store.CreateSource(context.Background(), "alice", SourceInput{Name: "Remote", URL: server.URL + "/agenda.ics"}); err != nil {
		t.Fatalf("CreateSource remote: %v", err)
	}

	agenda, err := store.Agenda(context.Background(), "alice", AgendaQuery{Scope: string(AgendaScopeToday), Timezone: "UTC", Now: time.Date(2026, 4, 24, 8, 0, 0, 0, time.UTC).Unix(), Limit: 10}, server.Client())
	if err != nil {
		t.Fatalf("Agenda: %v", err)
	}
	if agenda.Count != 2 {
		t.Fatalf("count = %d, want 2", agenda.Count)
	}
	if agenda.Events[0].Summary != "Local planning block" {
		t.Fatalf("first summary = %q", agenda.Events[0].Summary)
	}
	if agenda.Events[1].Summary != "Remote standup" {
		t.Fatalf("second summary = %q", agenda.Events[1].Summary)
	}
	if len(agenda.Warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", agenda.Warnings)
	}
}

func TestAgendaNextConflictWithRecurringEvent(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "calendar.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	ics := fmt.Sprintf("BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nUID:daily-focus\nSUMMARY:Daily focus\nDTSTART:%s\nDTEND:%s\nRRULE:FREQ=DAILY;COUNT=5\nEND:VEVENT\nBEGIN:VEVENT\nUID:manager-sync\nSUMMARY:Manager sync\nDTSTART:%s\nDTEND:%s\nEND:VEVENT\nEND:VCALENDAR\n",
		"20260425T090000Z",
		"20260425T100000Z",
		"20260426T093000Z",
		"20260426T101500Z",
	)
	localPath := filepath.Join(t.TempDir(), "recurring.ics")
	if err := os.WriteFile(localPath, []byte(ics), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := store.CreateSource(context.Background(), "alice", SourceInput{Name: "Work", Path: localPath}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}

	agenda, err := store.Agenda(context.Background(), "alice", AgendaQuery{Scope: string(AgendaScopeNextConflict), Timezone: "UTC", Now: time.Date(2026, 4, 25, 8, 0, 0, 0, time.UTC).Unix()}, nil)
	if err != nil {
		t.Fatalf("Agenda: %v", err)
	}
	if agenda.Conflict == nil {
		t.Fatalf("expected conflict, got nil")
	}
	if len(agenda.Conflict.Events) != 2 {
		t.Fatalf("conflict events = %d, want 2", len(agenda.Conflict.Events))
	}
	if agenda.Conflict.Events[0].Summary != "Daily focus" {
		t.Fatalf("first conflict summary = %q", agenda.Conflict.Events[0].Summary)
	}
	if agenda.Conflict.Events[1].Summary != "Manager sync" {
		t.Fatalf("second conflict summary = %q", agenda.Conflict.Events[1].Summary)
	}
}
