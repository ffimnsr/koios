package eventbus

import (
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/types"
)

// Event is a generic bus payload used for decoupled cross-agent coordination.
type Event struct {
	Kind       string         `json:"kind"`
	PeerID     string         `json:"peer_id,omitempty"`
	SessionKey string         `json:"session_key,omitempty"`
	Source     string         `json:"source,omitempty"`
	RunID      string         `json:"run_id,omitempty"`
	Message    *types.Message `json:"message,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
	At         time.Time      `json:"at"`
}

// Bus provides in-memory publish/subscribe fan-out for runtime events.
type Bus struct {
	mu          sync.RWMutex
	subscribers map[uint64]func(Event)
	nextID      uint64
}

// New creates an in-memory event bus.
func New() *Bus {
	return &Bus{subscribers: make(map[uint64]func(Event))}
}

// Publish fan-outs one event to all current subscribers.
func (b *Bus) Publish(ev Event) {
	if b == nil {
		return
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	b.mu.RLock()
	callbacks := make([]func(Event), 0, len(b.subscribers))
	for _, fn := range b.subscribers {
		callbacks = append(callbacks, fn)
	}
	b.mu.RUnlock()
	for _, fn := range callbacks {
		fn(ev)
	}
}

// Subscribe registers a callback and returns an unsubscribe function.
func (b *Bus) Subscribe(fn func(Event)) func() {
	if b == nil || fn == nil {
		return func() {}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	b.subscribers[id] = fn
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subscribers, id)
	}
}
