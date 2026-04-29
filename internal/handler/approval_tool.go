package handler

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type approvalExecutor func(context.Context, string, pendingApproval) (map[string]any, error)

type pendingApproval struct {
	ID        string         `json:"id"`
	PeerID    string         `json:"peer_id"`
	Kind      string         `json:"kind"`
	Action    string         `json:"action"`
	Summary   string         `json:"summary,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Command   string         `json:"command,omitempty"`
	Workdir   string         `json:"workdir,omitempty"`
	Timeout   int            `json:"timeout_seconds,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	ExpiresAt time.Time      `json:"expires_at"`
}

type approvalEntry struct {
	approval pendingApproval
	execute  approvalExecutor
}

type approvalStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	pending map[string]approvalEntry
}

func newApprovalStore(ttl time.Duration) *approvalStore {
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	return &approvalStore{
		ttl:     ttl,
		pending: make(map[string]approvalEntry),
	}
}

func (s *approvalStore) create(approval pendingApproval, execute approvalExecutor) pendingApproval {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	now := time.Now().UTC()
	approval.ID = uuid.NewString()
	approval.PeerID = strings.TrimSpace(approval.PeerID)
	approval.Kind = strings.TrimSpace(approval.Kind)
	approval.Action = strings.TrimSpace(approval.Action)
	approval.Summary = strings.TrimSpace(approval.Summary)
	approval.Reason = strings.TrimSpace(approval.Reason)
	approval.Command = strings.TrimSpace(approval.Command)
	approval.Workdir = strings.TrimSpace(approval.Workdir)
	approval.CreatedAt = now
	approval.ExpiresAt = now.Add(s.ttl)
	if len(approval.Metadata) == 0 {
		approval.Metadata = nil
	}
	s.pending[approval.ID] = approvalEntry{approval: approval, execute: execute}
	return approval
}

func (s *approvalStore) list(peerID string, filter func(pendingApproval) bool) []pendingApproval {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	peerID = strings.TrimSpace(peerID)
	out := make([]pendingApproval, 0, len(s.pending))
	for _, entry := range s.pending {
		if entry.approval.PeerID != peerID {
			continue
		}
		if filter != nil && !filter(entry.approval) {
			continue
		}
		out = append(out, entry.approval)
	}
	return out
}

func (s *approvalStore) take(peerID, id string, filter func(pendingApproval) bool) (approvalEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry, ok := s.pending[id]
	if !ok {
		return approvalEntry{}, fmt.Errorf("approval %s not found", id)
	}
	if entry.approval.PeerID != strings.TrimSpace(peerID) {
		return approvalEntry{}, fmt.Errorf("approval %s does not belong to peer", id)
	}
	if filter != nil && !filter(entry.approval) {
		return approvalEntry{}, fmt.Errorf("approval %s is not available for this action", id)
	}
	delete(s.pending, id)
	return entry, nil
}

func (s *approvalStore) reject(peerID, id string, filter func(pendingApproval) bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	entry, ok := s.pending[id]
	if !ok {
		return fmt.Errorf("approval %s not found", id)
	}
	if entry.approval.PeerID != strings.TrimSpace(peerID) {
		return fmt.Errorf("approval %s does not belong to peer", id)
	}
	if filter != nil && !filter(entry.approval) {
		return fmt.Errorf("approval %s is not available for this action", id)
	}
	delete(s.pending, id)
	return nil
}

func (s *approvalStore) pruneLocked() {
	now := time.Now().UTC()
	for id, entry := range s.pending {
		if !entry.approval.ExpiresAt.After(now) {
			delete(s.pending, id)
		}
	}
}

func (h *Handler) requestApproval(peerID string, approval pendingApproval, execute approvalExecutor) map[string]any {
	approval.PeerID = peerID
	created := h.approvals.create(approval, execute)
	return map[string]any{
		"status":   "approval_required",
		"approval": created,
	}
}

func (h *Handler) approvePendingAction(ctx context.Context, peerID, approvalID string, filter func(pendingApproval) bool) (map[string]any, error) {
	entry, err := h.approvals.take(peerID, approvalID, filter)
	if err != nil {
		return nil, err
	}
	if entry.execute == nil {
		return map[string]any{
			"ok":       true,
			"status":   "approved",
			"approval": entry.approval,
		}, nil
	}
	result, err := entry.execute(ctx, peerID, entry.approval)
	if err != nil {
		return nil, err
	}
	result["approval_id"] = entry.approval.ID
	return result, nil
}

func (h *Handler) rejectPendingAction(peerID, approvalID string, filter func(pendingApproval) bool) error {
	return h.approvals.reject(peerID, approvalID, filter)
}

func shellApprovalFilter(approval pendingApproval) bool {
	return approval.Kind == "shell_execution"
}

func (h *Handler) rpcApprovalPending(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Kind string `json:"kind"`
	}
	if len(req.Params) > 0 {
		if err := decodeParams(req.Params, &p); err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
			return
		}
	}
	kind := strings.TrimSpace(p.Kind)
	filter := func(approval pendingApproval) bool {
		if kind == "" {
			return true
		}
		return approval.Kind == kind
	}
	wsc.reply(req.ID, map[string]any{"pending": h.approvals.list(wsc.peerID, filter)})
}

func (h *Handler) rpcApprovalApprove(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.ID) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	result, err := h.approvePendingAction(ctx, wsc.peerID, p.ID, nil)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcApprovalReject(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.ID) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	if err := h.rejectPendingAction(wsc.peerID, p.ID, nil); err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}
