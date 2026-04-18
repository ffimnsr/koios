package runledger

import (
	"log/slog"
	"time"
)

// CoordinatorAdapter wraps Store to implement agent.RunLedger and
// subagent.SubagentLedger for a given run kind.  It translates the
// primitive lifecycle callbacks into Store operations.
type CoordinatorAdapter struct {
	store *Store
	kind  RunKind
}

// NewCoordinatorAdapter returns an adapter that records agent coordinator runs
// into the ledger.
func NewCoordinatorAdapter(s *Store) *CoordinatorAdapter {
	return &CoordinatorAdapter{store: s, kind: KindAgent}
}

// NewSubagentAdapter returns an adapter that records subagent registry runs
// into the ledger.
func NewSubagentAdapter(s *Store) *CoordinatorAdapter {
	return &CoordinatorAdapter{store: s, kind: KindSubagent}
}

// NewOrchestratorAdapter returns an adapter that records orchestrator runs
// into the ledger.
func NewOrchestratorAdapter(s *Store) *CoordinatorAdapter {
	return &CoordinatorAdapter{store: s, kind: KindOrchestrator}
}

// LedgerQueued satisfies agent.RunLedger and subagent.SubagentLedger.
func (a *CoordinatorAdapter) LedgerQueued(id, peerID, sessionKey, model string, queuedAt time.Time) {
	if err := a.store.Add(Record{
		ID:         id,
		Kind:       a.kind,
		PeerID:     peerID,
		SessionKey: sessionKey,
		Model:      model,
		Status:     StatusQueued,
		QueuedAt:   queuedAt,
	}); err != nil {
		slog.Debug("runledger: add failed", "id", id, "err", err)
	}
}

// LedgerStarted satisfies agent.RunLedger and subagent.SubagentLedger.
func (a *CoordinatorAdapter) LedgerStarted(id string, startedAt time.Time) {
	if err := a.store.Update(id, func(r *Record) {
		r.Status = StatusRunning
		r.StartedAt = &startedAt
	}); err != nil {
		slog.Debug("runledger: update started failed", "id", id, "err", err)
	}
}

// LedgerFinished satisfies agent.RunLedger and subagent.SubagentLedger.
func (a *CoordinatorAdapter) LedgerFinished(id string, finishedAt time.Time, status, errMsg string, steps, promptTokens, completionTokens int) {
	st := normalizeStatus(status)
	if err := a.store.Update(id, func(r *Record) {
		r.Status = st
		r.Error = errMsg
		r.Steps = steps
		r.PromptTokens = promptTokens
		r.CompletionTokens = completionTokens
		r.FinishedAt = &finishedAt
	}); err != nil {
		slog.Debug("runledger: update finished failed", "id", id, "err", err)
	}
}

// LedgerMetadata records supplemental fields that are not part of the basic
// queued/started/finished lifecycle callbacks.
func (a *CoordinatorAdapter) LedgerMetadata(id, parentID string, toolCalls int) {
	if err := a.store.Update(id, func(r *Record) {
		r.ParentID = parentID
		r.ToolCalls = toolCalls
	}); err != nil {
		slog.Debug("runledger: metadata update failed", "id", id, "err", err)
	}
}

func normalizeStatus(status string) RunStatus {
	switch RunStatus(status) {
	case StatusQueued:
		return StatusQueued
	case StatusRunning:
		return StatusRunning
	case StatusCompleted:
		return StatusCompleted
	case StatusErrored:
		return StatusErrored
	case StatusCanceled:
		return StatusCanceled
	case StatusSkipped:
		return StatusSkipped
	}
	switch status {
	case "", "completed":
		return StatusCompleted
	case "running":
		return StatusRunning
	case "queued":
		return StatusQueued
	case "errored":
		return StatusErrored
	case "cancelled", "killed", "session-reset", "session-delete":
		return StatusCanceled
	case "skipped":
		return StatusSkipped
	default:
		return StatusErrored
	}
}
