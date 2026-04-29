package handler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/artifacts"
	"github.com/ffimnsr/koios/internal/briefing"
	"github.com/ffimnsr/koios/internal/scheduler"
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

// briefPreview generates a brief and returns only the rendered text, without
// saving or delivering anything. Useful for reviewing before committing.
func (h *Handler) briefPreview(peerID string, opts briefing.Options, ctx context.Context) (map[string]any, error) {
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
	return map[string]any{"ok": true, "text": briefing.RenderText(report), "kind": report.Kind}, nil
}

// briefSave generates a brief and saves it as an artifact.
func (h *Handler) briefSave(peerID string, opts briefing.Options, title string, ctx context.Context) (map[string]any, error) {
	if h.artifactStore == nil {
		return nil, fmt.Errorf("artifact store is not enabled")
	}
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
	text := briefing.RenderText(report)
	if strings.TrimSpace(title) == "" {
		kind := report.Kind
		if kind == "" {
			kind = "daily"
		}
		title = strings.ToUpper(kind[:1]) + kind[1:] + " brief – " + time.Unix(report.GeneratedAt, 0).UTC().Format("2006-01-02")
	}
	a, err := h.artifactStore.Create(ctx, peerID, artifacts.Input{
		Kind:    "brief",
		Title:   title,
		Content: text,
		Labels:  []string{report.Kind},
	})
	if err != nil {
		return nil, fmt.Errorf("brief save: %w", err)
	}
	return map[string]any{"ok": true, "artifact_id": a.ID, "kind": report.Kind, "text": text}, nil
}

// briefSend generates a brief and delivers it as a system notification.
func (h *Handler) briefSend(peerID string, opts briefing.Options, notifTitle string, ctx context.Context) (map[string]any, error) {
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
	text := briefing.RenderText(report)
	if strings.TrimSpace(notifTitle) == "" {
		kind := report.Kind
		if kind == "" {
			kind = "daily"
		}
		notifTitle = strings.ToUpper(kind[:1]) + kind[1:] + " brief"
	}
	if _, notifyErr := h.runSystemNotifyTool(ctx, notifTitle, text); notifyErr != nil {
		return map[string]any{"ok": false, "error": notifyErr.Error(), "kind": report.Kind, "text": text}, nil
	}
	return map[string]any{"ok": true, "kind": report.Kind, "text": text}, nil
}

// briefSchedule creates a cron job that will generate a brief on the given
// schedule and deliver it as a system notification.
func (h *Handler) briefSchedule(peerID string, name string, opts briefing.Options, sched scheduler.Schedule, ctx context.Context) (map[string]any, error) {
	if h.jobStore == nil || h.sched == nil {
		return nil, fmt.Errorf("cron is not enabled")
	}
	if strings.TrimSpace(name) == "" {
		name = "brief: " + opts.Kind
		if opts.Kind == "" {
			name = "brief: daily"
		}
	}
	// Trigger an agentTurn that asks the agent to send the brief.
	briefKind := opts.Kind
	if briefKind == "" {
		briefKind = "daily"
	}
	message := fmt.Sprintf("Please generate and send my %s brief now.", briefKind)
	payload := scheduler.Payload{
		Kind:    scheduler.PayloadAgentTurn,
		Message: message,
	}
	job, err := h.createCronJob(peerID, cronCreateParams{
		Name:        name,
		Description: "Scheduled brief delivery",
		Schedule:    sched,
		Payload:     payload,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "job_id": job.JobID, "name": job.Name}, nil
}
