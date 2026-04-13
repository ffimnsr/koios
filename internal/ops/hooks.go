package ops

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// HookName identifies a lifecycle hook event.
type HookName string

const (
	HookMessageReceived  HookName = "message_received"
	HookBeforeLLM        HookName = "before_llm"
	HookAfterLLM         HookName = "after_llm"
	HookBeforeToolCall   HookName = "before_tool_call"
	HookAfterToolCall    HookName = "after_tool_call"
	HookBeforeCompaction HookName = "before_compaction"
	HookAfterCompaction  HookName = "after_compaction"
)

// Event is a typed hook event payload.
type Event struct {
	Name       HookName       `json:"name"`
	Timestamp  time.Time      `json:"timestamp"`
	PeerID     string         `json:"peer_id,omitempty"`
	SessionKey string         `json:"session_key,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

// Handler processes one hook event.
type Handler func(context.Context, Event) error

type registration struct {
	priority int
	handler  Handler
}

// Manager dispatches hook events to registered handlers.
type Manager struct {
	mu         sync.RWMutex
	handlers   map[HookName][]registration
	timeout    time.Duration
	failClosed bool
}

// NewManager creates a hook manager.
func NewManager(timeout time.Duration, failClosed bool) *Manager {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Manager{
		handlers:   make(map[HookName][]registration),
		timeout:    timeout,
		failClosed: failClosed,
	}
}

// Register adds a hook handler. Lower priority values run first.
func (m *Manager) Register(name HookName, priority int, handler Handler) {
	if m == nil || handler == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.handlers[name] = append(m.handlers[name], registration{priority: priority, handler: handler})
	sort.SliceStable(m.handlers[name], func(i, j int) bool {
		return m.handlers[name][i].priority < m.handlers[name][j].priority
	})
}

// Emit dispatches one event to handlers for that event name.
func (m *Manager) Emit(ctx context.Context, ev Event) error {
	if m == nil {
		return nil
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	m.mu.RLock()
	regs := append([]registration(nil), m.handlers[ev.Name]...)
	m.mu.RUnlock()
	for _, reg := range regs {
		hCtx, cancel := context.WithTimeout(ctx, m.timeout)
		err := reg.handler(hCtx, ev)
		cancel()
		if err != nil {
			if m.failClosed {
				return fmt.Errorf("hook %s failed: %w", ev.Name, err)
			}
		}
	}
	return nil
}

// HTTPWebhookHandler returns a hook handler that POSTs events to an endpoint.
// If secret is set, an HMAC-SHA256 signature is sent in X-Koios-Signature.
func HTTPWebhookHandler(endpoint, secret string, client *http.Client) Handler {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return func(ctx context.Context, ev Event) error {
		body, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write(body)
			req.Header.Set("X-Koios-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("webhook returned status %d", resp.StatusCode)
		}
		return nil
	}
}
