// Package scratchpad provides an ephemeral per-session working surface.
// Scratchpads help agents track intermediate reasoning and local state without
// polluting long-term memory, bookmarks, notes, or artifacts. All data is
// stored in memory and does not survive process restart.
package scratchpad

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Scratchpad holds the current ephemeral content for one session.
type Scratchpad struct {
	SessionKey string `json:"session_key"`
	Content    string `json:"content"`
	CreatedAt  int64  `json:"created_at"`
	UpdatedAt  int64  `json:"updated_at"`
}

// Store is a concurrency-safe in-memory scratchpad store keyed by session key.
type Store struct {
	mu   sync.RWMutex
	pads map[string]*Scratchpad // key: "<peerID>::<sessionKey>"
}

// New returns an empty Store.
func New() *Store {
	return &Store{pads: make(map[string]*Scratchpad)}
}

// Create initialises a scratchpad for the given session. If one already exists
// for this session the call is idempotent and the existing pad is returned.
func (s *Store) Create(peerID, sessionKey, content string) *Scratchpad {
	key := storeKey(peerID, sessionKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	if pad, ok := s.pads[key]; ok {
		return pad
	}
	now := time.Now().Unix()
	pad := &Scratchpad{
		SessionKey: sessionKey,
		Content:    strings.TrimSpace(content),
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	s.pads[key] = pad
	return pad
}

// Get returns the scratchpad for the given session, or an error if none exists.
func (s *Store) Get(peerID, sessionKey string) (*Scratchpad, error) {
	key := storeKey(peerID, sessionKey)
	s.mu.RLock()
	defer s.mu.RUnlock()
	pad, ok := s.pads[key]
	if !ok {
		return nil, fmt.Errorf("no scratchpad for session %s", sessionKey)
	}
	// Return a copy so the caller cannot mutate internal state.
	cp := *pad
	return &cp, nil
}

// Update replaces the content of the scratchpad for the given session.
// If no pad exists yet one is created with the given content.
func (s *Store) Update(peerID, sessionKey, content string) *Scratchpad {
	key := storeKey(peerID, sessionKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	pad, ok := s.pads[key]
	if !ok {
		now := time.Now().Unix()
		pad = &Scratchpad{
			SessionKey: sessionKey,
			CreatedAt:  now,
			UpdatedAt:  now,
		}
		s.pads[key] = pad
	}
	pad.Content = strings.TrimSpace(content)
	pad.UpdatedAt = time.Now().Unix()
	cp := *pad
	return &cp
}

// Clear removes the scratchpad for the given session.
func (s *Store) Clear(peerID, sessionKey string) {
	key := storeKey(peerID, sessionKey)
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pads, key)
}

func storeKey(peerID, sessionKey string) string {
	p := strings.TrimSpace(peerID)
	k := strings.TrimSpace(sessionKey)
	if k == "" {
		return p
	}
	return p + "::" + k
}
