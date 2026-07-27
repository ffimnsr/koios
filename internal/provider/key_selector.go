package provider

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/types"
)

const (
	assignmentTTL       = 30 * time.Minute
	rateLimitCooldown   = 1 * time.Minute
	authFailureCooldown = 5 * time.Minute
)

type credentialSelector struct {
	mu          sync.Mutex
	provider    string
	keys        []string
	usage       []int
	unhealthy   []time.Time
	assignments map[string]keyAssignment
}

type keyAssignment struct {
	index    int
	lastSeen time.Time
}

func newCredentialSelector(provider string, keys []string) *credentialSelector {
	trimmed := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		trimmed = append(trimmed, key)
	}
	return &credentialSelector{
		provider:    strings.TrimSpace(provider),
		keys:        trimmed,
		usage:       make([]int, len(trimmed)),
		unhealthy:   make([]time.Time, len(trimmed)),
		assignments: make(map[string]keyAssignment),
	}
}

func (s *credentialSelector) Select(ctx context.Context, req *types.ChatRequest) (string, func(error)) {
	if s == nil || len(s.keys) == 0 {
		return "", func(error) {}
	}
	identity := selectorIdentity(ctx, req)
	idx := s.selectIndex(identity)
	if idx < 0 || idx >= len(s.keys) {
		return "", func(error) {}
	}
	key := s.keys[idx]
	return key, func(err error) {
		s.reportResult(idx, err)
	}
}

func selectorIdentity(ctx context.Context, req *types.ChatRequest) string {
	ri := types.RequestIdentityFromContext(ctx)
	if strings.TrimSpace(ri.SessionKey) != "" {
		return "session:" + strings.TrimSpace(ri.SessionKey)
	}
	if strings.TrimSpace(ri.PeerID) != "" {
		return "peer:" + strings.TrimSpace(ri.PeerID)
	}
	if req != nil && strings.TrimSpace(req.User) != "" {
		return "user:" + strings.TrimSpace(req.User)
	}
	return "hash:" + deterministicRequestHash(req)
}

func deterministicRequestHash(req *types.ChatRequest) string {
	h := fnv.New64a()
	if req != nil {
		_, _ = h.Write([]byte(strings.TrimSpace(req.Model)))
		_, _ = h.Write([]byte("|"))
		_, _ = h.Write([]byte(strings.TrimSpace(req.User)))
		for _, msg := range req.Messages {
			_, _ = h.Write([]byte("|"))
			_, _ = h.Write([]byte(strings.TrimSpace(msg.Role)))
			_, _ = h.Write([]byte("="))
			_, _ = h.Write([]byte(strings.TrimSpace(msg.Content)))
		}
	}
	return fmt.Sprintf("%x", h.Sum64())
}

func (s *credentialSelector) selectIndex(identity string) int {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredAssignmentsLocked(now)
	if assignment, ok := s.assignments[identity]; ok {
		if assignment.index >= 0 && assignment.index < len(s.keys) && !s.isUnhealthyLocked(assignment.index, now) {
			assignment.lastSeen = now
			s.assignments[identity] = assignment
			return assignment.index
		}
		s.releaseAssignmentLocked(identity, assignment)
	}
	idx := s.leastUsedHealthyIndexLocked(now)
	if idx < 0 {
		idx = s.leastUsedIndexLocked()
	}
	if idx < 0 {
		return -1
	}
	s.assignments[identity] = keyAssignment{index: idx, lastSeen: now}
	s.usage[idx]++
	return idx
}

func (s *credentialSelector) pruneExpiredAssignmentsLocked(now time.Time) {
	for identity, assignment := range s.assignments {
		if assignment.lastSeen.Add(assignmentTTL).After(now) {
			continue
		}
		s.releaseAssignmentLocked(identity, assignment)
	}
}

func (s *credentialSelector) releaseAssignmentLocked(identity string, assignment keyAssignment) {
	delete(s.assignments, identity)
	if assignment.index >= 0 && assignment.index < len(s.usage) && s.usage[assignment.index] > 0 {
		s.usage[assignment.index]--
	}
}

func (s *credentialSelector) leastUsedHealthyIndexLocked(now time.Time) int {
	best := -1
	for idx := range s.keys {
		if s.isUnhealthyLocked(idx, now) {
			continue
		}
		if best == -1 || s.usage[idx] < s.usage[best] {
			best = idx
		}
	}
	return best
}

func (s *credentialSelector) leastUsedIndexLocked() int {
	best := -1
	for idx := range s.keys {
		if best == -1 || s.usage[idx] < s.usage[best] {
			best = idx
		}
	}
	return best
}

func (s *credentialSelector) isUnhealthyLocked(idx int, now time.Time) bool {
	if idx < 0 || idx >= len(s.unhealthy) {
		return false
	}
	until := s.unhealthy[idx]
	return !until.IsZero() && until.After(now)
}

func (s *credentialSelector) reportResult(idx int, err error) {
	if s == nil || err == nil {
		return
	}
	cooldown := unhealthyCooldown(err)
	if cooldown <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if idx < 0 || idx >= len(s.unhealthy) {
		return
	}
	until := time.Now().Add(cooldown)
	if until.After(s.unhealthy[idx]) {
		s.unhealthy[idx] = until
	}
}

func unhealthyCooldown(err error) time.Duration {
	if err == nil {
		return 0
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "403"), strings.Contains(msg, "unauthorized"), strings.Contains(msg, "forbidden"), strings.Contains(msg, "invalid api key"), strings.Contains(msg, "authentication"):
		return authFailureCooldown
	case strings.Contains(msg, "429"), strings.Contains(msg, "rate limit"), strings.Contains(msg, "too many requests"), strings.Contains(msg, "quota"):
		return rateLimitCooldown
	default:
		return 0
	}
}
