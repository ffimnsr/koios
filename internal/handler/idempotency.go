package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const idempotencyTTL = 24 * time.Hour

type idempotencyReservation struct {
	wait     bool
	conflict bool
	record   *idempotencyRecord
}

type idempotencyRecord struct {
	paramsHash string
	createdAt  time.Time
	done       chan struct{}
	response   rpcResponse
}

type idempotencyStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]*idempotencyRecord
}

func newIdempotencyStore(ttl time.Duration) *idempotencyStore {
	if ttl <= 0 {
		ttl = idempotencyTTL
	}
	return &idempotencyStore{
		ttl:     ttl,
		entries: make(map[string]*idempotencyRecord),
	}
}

func (s *idempotencyStore) reserve(scope, paramsHash string, now time.Time) idempotencyReservation {
	s.mu.Lock()
	defer s.mu.Unlock()

	for key, rec := range s.entries {
		if now.Sub(rec.createdAt) > s.ttl {
			delete(s.entries, key)
		}
	}

	if rec, ok := s.entries[scope]; ok {
		if rec.paramsHash != paramsHash {
			return idempotencyReservation{conflict: true}
		}
		return idempotencyReservation{wait: true, record: rec}
	}

	rec := &idempotencyRecord{
		paramsHash: paramsHash,
		createdAt:  now,
		done:       make(chan struct{}),
	}
	s.entries[scope] = rec
	return idempotencyReservation{record: rec}
}

func (s *idempotencyStore) finish(rec *idempotencyRecord, resp rpcResponse) {
	s.mu.Lock()
	rec.response = resp
	close(rec.done)
	s.mu.Unlock()
}

func (s *idempotencyStore) wait(ctx context.Context, rec *idempotencyRecord) (rpcResponse, error) {
	select {
	case <-ctx.Done():
		return rpcResponse{}, ctx.Err()
	case <-rec.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return rec.response, nil
	}
}

func idempotencyKeyFromParams(params json.RawMessage) (string, error) {
	if len(params) == 0 || string(params) == "null" {
		return "", nil
	}
	var envelope struct {
		Key *string `json:"idempotency_key"`
	}
	if err := json.Unmarshal(params, &envelope); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	if envelope.Key == nil {
		return "", nil
	}
	key := strings.TrimSpace(*envelope.Key)
	if key == "" {
		return "", fmt.Errorf("idempotency_key must not be empty")
	}
	return key, nil
}

func canonicalParamsHash(params json.RawMessage) (string, error) {
	if len(params) == 0 || string(params) == "null" {
		sum := sha256.Sum256([]byte("{}"))
		return hex.EncodeToString(sum[:]), nil
	}
	var v any
	if err := json.Unmarshal(params, &v); err != nil {
		return "", fmt.Errorf("invalid params: %w", err)
	}
	buf, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("canonicalize params: %w", err)
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}

func isIdempotentRPCMethod(method string) bool {
	switch method {
	case "chat",
		"session.reset",
		"presence.set",
		"standing.set",
		"standing.clear",
		"standing.profile.set",
		"standing.profile.delete",
		"standing.profile.activate",
		"agent.run",
		"agent.start",
		"agent.cancel",
		"agent.steer",
		"subagent.spawn",
		"subagent.kill",
		"subagent.steer",
		"memory.insert",
		"memory.delete",
		"memory.entity.create",
		"memory.entity.update",
		"memory.entity.link_chunk",
		"memory.entity.relate",
		"memory.entity.touch",
		"memory.entity.delete",
		"memory.entity.unlink_chunk",
		"memory.entity.unrelate",
		"contact.alias",
		"contact.link_channel_identity",
		"memory.candidate.create",
		"memory.candidate.edit",
		"memory.candidate.approve",
		"memory.candidate.merge",
		"memory.candidate.reject",
		"task.candidate.create",
		"task.candidate.extract",
		"task.candidate.edit",
		"task.candidate.approve",
		"task.candidate.reject",
		"task.update",
		"task.assign",
		"task.snooze",
		"task.complete",
		"task.reopen",
		"waiting.create",
		"waiting.update",
		"waiting.snooze",
		"waiting.resolve",
		"waiting.reopen",
		"workspace.write",
		"workspace.edit",
		"workspace.mkdir",
		"workspace.delete",
		"approval.approve",
		"approval.reject",
		"exec",
		"exec.approve",
		"exec.reject",
		"cron.create",
		"cron.update",
		"cron.delete",
		"cron.trigger",
		"heartbeat.set",
		"heartbeat.wake",
		"server.set_log_level":
		return true
	default:
		return false
	}
}

func idempotentRPCMethods() []string {
	return []string{
		"chat",
		"session.reset",
		"presence.set",
		"standing.set",
		"standing.clear",
		"standing.profile.set",
		"standing.profile.delete",
		"standing.profile.activate",
		"agent.run",
		"agent.start",
		"agent.cancel",
		"agent.steer",
		"subagent.spawn",
		"subagent.kill",
		"subagent.steer",
		"memory.insert",
		"memory.delete",
		"memory.entity.create",
		"memory.entity.update",
		"memory.entity.link_chunk",
		"memory.entity.relate",
		"memory.entity.touch",
		"memory.entity.delete",
		"memory.entity.unlink_chunk",
		"memory.entity.unrelate",
		"contact.alias",
		"contact.link_channel_identity",
		"memory.candidate.create",
		"memory.candidate.edit",
		"memory.candidate.approve",
		"memory.candidate.merge",
		"memory.candidate.reject",
		"task.candidate.create",
		"task.candidate.extract",
		"task.candidate.edit",
		"task.candidate.approve",
		"task.candidate.reject",
		"task.update",
		"task.assign",
		"task.snooze",
		"task.complete",
		"task.reopen",
		"waiting.create",
		"waiting.update",
		"waiting.snooze",
		"waiting.resolve",
		"waiting.reopen",
		"workspace.write",
		"workspace.edit",
		"workspace.mkdir",
		"workspace.delete",
		"approval.approve",
		"approval.reject",
		"exec",
		"exec.approve",
		"exec.reject",
		"cron.create",
		"cron.update",
		"cron.delete",
		"cron.trigger",
		"heartbeat.set",
		"heartbeat.wake",
		"server.set_log_level",
	}
}
