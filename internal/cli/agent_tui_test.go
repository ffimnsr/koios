package cli

import (
	"fmt"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAgentTUIScrollPreservesViewportUntilFollowResumes(t *testing.T) {
	m := newAgentTUIModel(nil, agentOptions{Peer: "peer-1", Scope: "main"})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 18})
	for i := 0; i < 24; i++ {
		m.lines = append(m.lines, chatLine{at: time.Now(), role: "meta", content: fmt.Sprintf("line %02d", i)})
	}
	m.syncViewport()
	if !m.viewport.AtBottom() {
		t.Fatal("expected viewport to start at bottom")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.viewport.AtBottom() {
		t.Fatal("expected page up to move away from bottom")
	}
	if m.follow {
		t.Fatal("expected manual scroll to disable follow mode")
	}
	previousOffset := m.viewport.YOffset

	m.Update(agentEventMsg{"kind": "progress"})
	if m.viewport.YOffset != previousOffset {
		t.Fatalf("expected viewport offset to stay at %d while browsing history, got %d", previousOffset, m.viewport.YOffset)
	}
	if m.viewport.AtBottom() {
		t.Fatal("expected viewport to remain away from bottom while follow mode is disabled")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if !m.viewport.AtBottom() {
		t.Fatal("expected End to jump to the latest message")
	}
	if !m.follow {
		t.Fatal("expected End to re-enable follow mode")
	}

	m.Update(agentEventMsg{"kind": "done"})
	if !m.viewport.AtBottom() {
		t.Fatal("expected viewport to stay pinned to bottom after follow mode resumes")
	}
}

func TestAgentTUIFormatsToolEvents(t *testing.T) {
	m := newAgentTUIModel(nil, agentOptions{Peer: "peer-1", Scope: "main"})

	m.Update(agentEventMsg{"kind": "tool_call", "tool_name": "read_file", "summary": "path=/tmp/demo.txt"})
	if got := m.lines[len(m.lines)-1].content; got != "event: tool_call read_file - path=/tmp/demo.txt" {
		t.Fatalf("tool call event = %q", got)
	}

	m.Update(agentEventMsg{"kind": "tool_result", "tool_name": "read_file", "ok": true, "summary": "212 bytes"})
	if got := m.lines[len(m.lines)-1].content; got != "event: tool_result read_file ok - 212 bytes" {
		t.Fatalf("tool result event = %q", got)
	}
}
