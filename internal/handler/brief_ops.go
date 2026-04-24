package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/briefing"
)

func (h *Handler) briefGenerate(peerID string, opts briefing.Options, ctx context.Context) (map[string]any, error) {
	sessionKey := strings.TrimSpace(opts.SessionKey)
	if sessionKey == "" {
		sessionKey = peerID
	} else if sessionKey != peerID && !strings.HasPrefix(sessionKey, peerID+"::") {
		return nil, fmt.Errorf("session_key %q is not accessible to peer %q", sessionKey, peerID)
	}
	builder := briefing.Builder{CalendarStore: h.calendarStore, MemoryStore: h.memStore, TaskStore: h.taskStore}
	report, err := builder.Generate(ctx, peerID, h.store.Get(sessionKey).History(), briefing.Options{
		Kind:           opts.Kind,
		Timezone:       opts.Timezone,
		Now:            opts.Now,
		HistoryLimit:   opts.HistoryLimit,
		EventLimit:     opts.EventLimit,
		WaitingLimit:   opts.WaitingLimit,
		TaskLimit:      opts.TaskLimit,
		ProjectLimit:   opts.ProjectLimit,
		CandidateLimit: opts.CandidateLimit,
		SessionKey:     sessionKey,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "report": report, "text": briefing.RenderText(report), "kind": report.Kind}, nil
}
