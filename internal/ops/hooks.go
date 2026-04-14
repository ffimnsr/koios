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

	"github.com/ffimnsr/koios/internal/redact"
)

// HookName identifies a lifecycle hook event.
type HookName string

const (
	HookBeforeMessage    HookName = "before_message"
	HookAfterMessage     HookName = "after_message"
	HookMessageReceived  HookName = "message_received"
	HookBeforeLLM        HookName = "before_llm"
	HookAfterLLM         HookName = "after_llm"
	HookBeforeToolCall   HookName = "before_tool_call"
	HookAfterToolCall    HookName = "after_tool_call"
	HookBeforeCompaction HookName = "before_compaction"
	HookAfterCompaction  HookName = "after_compaction"
	HookCronApproval     HookName = "cron_approval"
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

// Interceptor can rewrite an event before execution continues.
type Interceptor func(context.Context, Event) (Event, error)

type registration struct {
	priority int
	handler  Handler
}

type interceptorRegistration struct {
	priority    int
	interceptor Interceptor
}

// Manager dispatches hook events to registered handlers.
type Manager struct {
	mu           sync.RWMutex
	handlers     map[HookName][]registration
	interceptors map[HookName][]interceptorRegistration
	timeout      time.Duration
	failClosed   bool
}

// NewManager creates a hook manager.
func NewManager(timeout time.Duration, failClosed bool) *Manager {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Manager{
		handlers:     make(map[HookName][]registration),
		interceptors: make(map[HookName][]interceptorRegistration),
		timeout:      timeout,
		failClosed:   failClosed,
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

// RegisterInterceptor adds an event interceptor. Lower priority values run first.
func (m *Manager) RegisterInterceptor(name HookName, priority int, interceptor Interceptor) {
	if m == nil || interceptor == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.interceptors[name] = append(m.interceptors[name], interceptorRegistration{
		priority:    priority,
		interceptor: interceptor,
	})
	sort.SliceStable(m.interceptors[name], func(i, j int) bool {
		return m.interceptors[name][i].priority < m.interceptors[name][j].priority
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

// Intercept applies registered interceptors and returns the rewritten event.
func (m *Manager) Intercept(ctx context.Context, ev Event) (Event, error) {
	if m == nil {
		return ev, nil
	}
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	m.mu.RLock()
	regs := append([]interceptorRegistration(nil), m.interceptors[ev.Name]...)
	m.mu.RUnlock()
	current := ev
	for _, reg := range regs {
		hCtx, cancel := context.WithTimeout(ctx, m.timeout)
		next, err := reg.interceptor(hCtx, current)
		cancel()
		if err != nil {
			if m.failClosed {
				return current, fmt.Errorf("hook interceptor %s failed: %w", ev.Name, err)
			}
			continue
		}
		if next.Timestamp.IsZero() {
			next.Timestamp = current.Timestamp
		}
		current = next
	}
	return current, nil
}

// HTTPWebhookHandler returns a hook handler that POSTs events to an endpoint.
// If secret is set, an HMAC-SHA256 signature is sent in X-Koios-Signature.
func HTTPWebhookHandler(endpoint, secret string, client *http.Client) Handler {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return func(ctx context.Context, ev Event) error {
		body, err := json.Marshal(redactedEvent(ev))
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

// HTTPWebhookInterceptor POSTs one event to an endpoint and accepts an optional
// rewritten event in the response body. Empty responses leave the event
// unchanged.
func HTTPWebhookInterceptor(endpoint, secret string, client *http.Client) Interceptor {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return func(ctx context.Context, ev Event) (Event, error) {
		body, err := json.Marshal(redactedEvent(ev))
		if err != nil {
			return ev, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return ev, err
		}
		req.Header.Set("Content-Type", "application/json")
		if secret != "" {
			mac := hmac.New(sha256.New, []byte(secret))
			_, _ = mac.Write(body)
			req.Header.Set("X-Koios-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		}
		resp, err := client.Do(req)
		if err != nil {
			return ev, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return ev, fmt.Errorf("webhook returned status %d", resp.StatusCode)
		}
		var out Event
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			if err.Error() == "EOF" {
				return ev, nil
			}
			return ev, err
		}
		if out.Name == "" {
			out.Name = ev.Name
		}
		if out.PeerID == "" {
			out.PeerID = ev.PeerID
		}
		if out.SessionKey == "" {
			out.SessionKey = ev.SessionKey
		}
		if out.Timestamp.IsZero() {
			out.Timestamp = ev.Timestamp
		}
		return out, nil
	}
}

func redactedEvent(ev Event) Event {
	copyEv := ev
	copyEv.PeerID = redact.String(copyEv.PeerID)
	copyEv.SessionKey = redact.String(copyEv.SessionKey)
	copyEv.Data = redact.Map(copyEv.Data)
	return copyEv
}
