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
	st := RunStatus(status)
	if st == "" {
		st = StatusCompleted
	}
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
