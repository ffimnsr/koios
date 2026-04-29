package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/presence"
	"github.com/ffimnsr/koios/internal/scheduler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

// WebhookHTTPHandler accepts authenticated external trigger events.
type WebhookHTTPHandler struct {
	store      *session.Store
	hooks      *ops.Manager
	presence   *presence.Manager
	jobStore   *scheduler.JobStore
	sched      *scheduler.Scheduler
	token      string
	agentCoord *agent.Coordinator
	toolExec   agent.ToolExecutor
}

func NewWebhookHTTPHandler(store *session.Store, hooks *ops.Manager, p *presence.Manager, jobStore *scheduler.JobStore, sched *scheduler.Scheduler, token string) *WebhookHTTPHandler {
	return &WebhookHTTPHandler{store: store, hooks: hooks, presence: p, jobStore: jobStore, sched: sched, token: strings.TrimSpace(token)}
}

// SetAgentCoordinator wires in the agent coordinator and tool executor so that
// the "agent.run" webhook event type can dispatch isolated agent runs directly.
func (h *WebhookHTTPHandler) SetAgentCoordinator(coord *agent.Coordinator, exec agent.ToolExecutor) {
	h.agentCoord = coord
	h.toolExec = exec
}

type webhookEventRequest struct {
	Type           string              `json:"type"`
	PeerID         string              `json:"peer_id"`
	Source         string              `json:"source,omitempty"`
	Message        *types.Message      `json:"message,omitempty"`
	Status         string              `json:"status,omitempty"`
	Typing         *bool               `json:"typing,omitempty"`
	JobID          string              `json:"job_id,omitempty"`
	Name           string              `json:"name,omitempty"`
	Schedule       *scheduler.Schedule `json:"schedule,omitempty"`
	Payload        *scheduler.Payload  `json:"payload,omitempty"`
	Enabled        *bool               `json:"enabled,omitempty"`
	DeleteAfterRun *bool               `json:"delete_after_run,omitempty"`
	Description    string              `json:"description,omitempty"`
	// agent.run fields
	Prompt         string `json:"prompt,omitempty"`
	Model          string `json:"model,omitempty"`
	Profile        string `json:"profile,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
}

func (h *WebhookHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if h == nil || h.store == nil {
		http.Error(w, "webhooks unavailable", http.StatusServiceUnavailable)
		return
	}
	if h.token == "" {
		http.Error(w, "webhooks disabled", http.StatusNotFound)
		return
	}
	if !webhookAuthorized(r, h.token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	var req webhookEventRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if !IsValidPeerID(req.PeerID) {
		http.Error(w, "invalid peer_id", http.StatusBadRequest)
		return
	}
	runID, err := h.apply(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	resp := map[string]any{"ok": true}
	if runID != "" {
		resp["run_id"] = runID
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func (h *WebhookHTTPHandler) apply(req webhookEventRequest) (string, error) {
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "webhook"
	}
	switch strings.TrimSpace(req.Type) {
	case "message.append":
		if req.Message == nil {
			return "", errors.New("message is required for message.append")
		}
		msg := *req.Message
		if strings.TrimSpace(msg.Role) == "" {
			return "", errors.New("message.role is required")
		}
		if h.hooks != nil {
			if err := h.hooks.Emit(context.Background(), ops.Event{
				Name:   ops.HookBeforeMessage,
				PeerID: req.PeerID,
				Data: map[string]any{
					"method": "webhook.message.append",
					"source": source,
				},
			}); err != nil {
				return "", err
			}
		}
		h.store.AppendWithSource(req.PeerID, source, msg)
		if h.hooks != nil {
			_ = h.hooks.Emit(context.Background(), ops.Event{
				Name:   ops.HookMessageReceived,
				PeerID: req.PeerID,
				Data: map[string]any{
					"method": "webhook.message.append",
					"source": source,
				},
			})
			_ = h.hooks.Emit(context.Background(), ops.Event{
				Name:   ops.HookAfterMessage,
				PeerID: req.PeerID,
				Data: map[string]any{
					"method": "webhook.message.append",
					"source": source,
				},
			})
		}
		return "", nil
	case "presence.set":
		if h.presence == nil {
			return "", errors.New("presence is not enabled")
		}
		typing := false
		if req.Typing != nil {
			typing = *req.Typing
		}
		h.presence.Set(req.PeerID, req.Status, typing, source)
		return "", nil
	case "cron.trigger":
		if h.sched == nil || h.jobStore == nil {
			return "", errors.New("cron is not enabled")
		}
		if strings.TrimSpace(req.JobID) == "" {
			return "", errors.New("job_id is required for cron.trigger")
		}
		job := h.jobStore.Get(req.JobID)
		if job == nil || job.PeerID != req.PeerID {
			return "", errors.New("job not found")
		}
		_, err := h.sched.TriggerRun(req.JobID)
		return "", err
	case "cron.schedule":
		if h.sched == nil || h.jobStore == nil {
			return "", errors.New("cron is not enabled")
		}
		if strings.TrimSpace(req.Name) == "" {
			req.Name = "webhook-scheduled"
		}
		if req.Schedule == nil {
			now := time.Now().UTC().Format(time.RFC3339)
			req.Schedule = &scheduler.Schedule{Kind: scheduler.KindAt, At: now}
		}
		if req.Payload == nil {
			return "", errors.New("payload is required for cron.schedule")
		}
		tmp := &Handler{jobStore: h.jobStore, sched: h.sched}
		_, err := tmp.createCronJob(req.PeerID, cronCreateParams{
			Name:           req.Name,
			Description:    req.Description,
			Schedule:       *req.Schedule,
			Payload:        *req.Payload,
			Enabled:        req.Enabled,
			DeleteAfterRun: req.DeleteAfterRun,
		})
		return "", err
	case "agent.run":
		if h.agentCoord == nil {
			return "", errors.New("agent coordinator is not available for agent.run")
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			return "", errors.New("prompt is required for agent.run")
		}
		timeout := time.Duration(req.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		runReq := agent.RunRequest{
			PeerID:        req.PeerID,
			Scope:         agent.ScopeIsolated,
			Messages:      []types.Message{{Role: "user", Content: prompt}},
			ToolExecutor:  h.toolExec,
			Model:         strings.TrimSpace(req.Model),
			ActiveProfile: strings.TrimSpace(req.Profile),
			Timeout:       timeout,
		}
		rec, err := h.agentCoord.Start(runReq)
		if err != nil {
			return "", fmt.Errorf("agent.run: %w", err)
		}
		return rec.ID, nil
	case "session.wake":
		// session.wake dispatches a non-isolated agent run against the main
		// peer session so that external events (cron callbacks, incoming
		// notifications, etc.) can wake the primary context and inject a
		// message that persists in the session history.
		if h.agentCoord == nil {
			return "", errors.New("agent coordinator is not available for session.wake")
		}
		prompt := strings.TrimSpace(req.Prompt)
		if prompt == "" {
			return "", errors.New("prompt is required for session.wake")
		}
		timeout := time.Duration(req.TimeoutSeconds) * time.Second
		if timeout <= 0 {
			timeout = 5 * time.Minute
		}
		runReq := agent.RunRequest{
			PeerID:        req.PeerID,
			Scope:         agent.ScopeMain,
			Messages:      []types.Message{{Role: "user", Content: prompt}},
			ToolExecutor:  h.toolExec,
			Model:         strings.TrimSpace(req.Model),
			ActiveProfile: strings.TrimSpace(req.Profile),
			Timeout:       timeout,
		}
		rec, err := h.agentCoord.Start(runReq)
		if err != nil {
			return "", fmt.Errorf("session.wake: %w", err)
		}
		return rec.ID, nil
	default:
		return "", errors.New("unsupported webhook type")
	}
}

func webhookAuthorized(r *http.Request, token string) bool {
	if strings.TrimSpace(token) == "" {
		return false
	}
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("Bearer "):]) == token
	}
	if v := strings.TrimSpace(r.Header.Get("X-Koios-Webhook-Token")); v != "" {
		return v == token
	}
	return false
}
