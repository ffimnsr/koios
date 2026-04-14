// Package usage tracks per-session LLM token consumption in memory.
// Counts are accumulated across all turns for a peer and exposed via the
// HTTP /v1/usage endpoint and the usage.get / usage.list WS RPC methods.
package usage

import (
	"sort"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/types"
)

// SessionUsage holds cumulative token usage for a single peer/session.
type SessionUsage struct {
	PeerID           string    `json:"peer_id"`
	PromptTokens     int64     `json:"prompt_tokens"`
	CompletionTokens int64     `json:"completion_tokens"`
	TotalTokens      int64     `json:"total_tokens"`
	Turns            int       `json:"turns"`
	LastUpdated      time.Time `json:"last_updated"`
}

// Store accumulates token usage in memory per peer ID.
// All methods are safe for concurrent use.
type Store struct {
	mu   sync.RWMutex
	data map[string]*SessionUsage
}

// New returns an empty in-memory usage store.
func New() *Store {
	return &Store{data: make(map[string]*SessionUsage)}
}

// Add accumulates usage for the given peer. No-op when peerID is empty or
// both token counts are zero.
func (s *Store) Add(peerID string, u types.Usage) {
	if peerID == "" || (u.PromptTokens == 0 && u.CompletionTokens == 0) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.data[peerID]
	if entry == nil {
		entry = &SessionUsage{PeerID: peerID}
		s.data[peerID] = entry
	}
	entry.PromptTokens += int64(u.PromptTokens)
	entry.CompletionTokens += int64(u.CompletionTokens)
	entry.TotalTokens += int64(u.TotalTokens)
	if entry.TotalTokens == 0 {
		// TotalTokens may be absent in provider responses; derive it.
		entry.TotalTokens = entry.PromptTokens + entry.CompletionTokens
	}
	entry.Turns++
	entry.LastUpdated = time.Now().UTC()
}

// Get returns a copy of the usage for the given peer.
// Returns (zero, false) when the peer has no recorded usage.
func (s *Store) Get(peerID string) (SessionUsage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	entry, ok := s.data[peerID]
	if !ok {
		return SessionUsage{}, false
	}
	return *entry, true
}

// All returns a snapshot of all sessions' usage sorted by total tokens
// descending.
func (s *Store) All() []SessionUsage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SessionUsage, 0, len(s.data))
	for _, v := range s.data {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalTokens != out[j].TotalTokens {
			return out[i].TotalTokens > out[j].TotalTokens
		}
		return out[i].PeerID < out[j].PeerID
	})
	return out
}

// Totals returns aggregate token counts across all sessions.
func (s *Store) Totals() SessionUsage {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var agg SessionUsage
	for _, v := range s.data {
		agg.PromptTokens += v.PromptTokens
		agg.CompletionTokens += v.CompletionTokens
		agg.TotalTokens += v.TotalTokens
		agg.Turns += v.Turns
	}
	return agg
}
