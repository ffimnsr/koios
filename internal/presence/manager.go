package presence

import (
	"sync"
	"time"
)

// State captures peer presence and typing signals.
type State struct {
	PeerID    string    `json:"peer_id"`
	Status    string    `json:"status"`
	Typing    bool      `json:"typing"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    string    `json:"source,omitempty"`
}

// Manager tracks in-memory presence and notifies subscribers.
type Manager struct {
	mu          sync.RWMutex
	states      map[string]State
	subscribers map[uint64]func(State)
	nextSubID   uint64
	typingTTL   time.Duration
}

// NewManager creates a presence manager.
func NewManager(typingTTL time.Duration) *Manager {
	if typingTTL <= 0 {
		typingTTL = 8 * time.Second
	}
	return &Manager{
		states:      make(map[string]State),
		subscribers: make(map[uint64]func(State)),
		typingTTL:   typingTTL,
	}
}

// Set updates one peer's presence state.
func (m *Manager) Set(peerID, status string, typing bool, source string) State {
	if m == nil || peerID == "" {
		return State{}
	}
	if status == "" {
		status = "online"
	}
	now := time.Now().UTC()
	state := State{
		PeerID:    peerID,
		Status:    status,
		Typing:    typing,
		UpdatedAt: now,
		Source:    source,
	}

	m.mu.Lock()
	m.states[peerID] = state
	callbacks := m.subscriberSnapshotLocked()
	m.mu.Unlock()
	for _, cb := range callbacks {
		cb(state)
	}
	return state
}

// Get returns one peer's current state.
func (m *Manager) Get(peerID string) (State, bool) {
	if m == nil {
		return State{}, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	state, ok := m.states[peerID]
	return state, ok
}

// Snapshot returns a copy of all known states.
func (m *Manager) Snapshot() []State {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]State, 0, len(m.states))
	for _, state := range m.states {
		out = append(out, state)
	}
	return out
}

// Subscribe registers a callback for future state updates.
func (m *Manager) Subscribe(fn func(State)) func() {
	if m == nil || fn == nil {
		return func() {}
	}
	m.mu.Lock()
	m.nextSubID++
	id := m.nextSubID
	m.subscribers[id] = fn
	m.mu.Unlock()
	return func() {
		m.mu.Lock()
		delete(m.subscribers, id)
		m.mu.Unlock()
	}
}

// ClearExpiredTyping marks stale typing indicators as false.
func (m *Manager) ClearExpiredTyping(now time.Time) int {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	updated := make([]State, 0)
	for peerID, state := range m.states {
		if !state.Typing {
			continue
		}
		if now.Sub(state.UpdatedAt) < m.typingTTL {
			continue
		}
		state.Typing = false
		state.UpdatedAt = now.UTC()
		m.states[peerID] = state
		updated = append(updated, state)
	}
	callbacks := m.subscriberSnapshotLocked()
	m.mu.Unlock()
	for _, state := range updated {
		for _, cb := range callbacks {
			cb(state)
		}
	}
	return len(updated)
}

func (m *Manager) subscriberSnapshotLocked() []func(State) {
	callbacks := make([]func(State), 0, len(m.subscribers))
	for _, cb := range m.subscribers {
		callbacks = append(callbacks, cb)
	}
	return callbacks
}
