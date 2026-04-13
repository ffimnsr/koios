package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/ffimnsr/koios/internal/ops"
	"github.com/ffimnsr/koios/internal/presence"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

// WebhookHTTPHandler accepts authenticated external trigger events.
type WebhookHTTPHandler struct {
	store    *session.Store
	hooks    *ops.Manager
	presence *presence.Manager
	token    string
}

func NewWebhookHTTPHandler(store *session.Store, hooks *ops.Manager, p *presence.Manager, token string) *WebhookHTTPHandler {
	return &WebhookHTTPHandler{store: store, hooks: hooks, presence: p, token: strings.TrimSpace(token)}
}

type webhookEventRequest struct {
	Type    string         `json:"type"`
	PeerID  string         `json:"peer_id"`
	Source  string         `json:"source,omitempty"`
	Message *types.Message `json:"message,omitempty"`
	Status  string         `json:"status,omitempty"`
	Typing  *bool          `json:"typing,omitempty"`
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
	if err := h.apply(req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func (h *WebhookHTTPHandler) apply(req webhookEventRequest) error {
	source := strings.TrimSpace(req.Source)
	if source == "" {
		source = "webhook"
	}
	switch strings.TrimSpace(req.Type) {
	case "message.append":
		if req.Message == nil {
			return errors.New("message is required for message.append")
		}
		msg := *req.Message
		if strings.TrimSpace(msg.Role) == "" {
			return errors.New("message.role is required")
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
		}
		return nil
	case "presence.set":
		if h.presence == nil {
			return errors.New("presence is not enabled")
		}
		typing := false
		if req.Typing != nil {
			typing = *req.Typing
		}
		h.presence.Set(req.PeerID, req.Status, typing, source)
		return nil
	default:
		return errors.New("unsupported webhook type")
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
