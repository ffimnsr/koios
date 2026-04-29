// Package orchestrator implements multi-session fan-out orchestration with
// reply aggregation for Koios.
//
// An orchestration run spawns N child subagent sessions (via subagent.Runtime),
// tracks their lifecycle, and aggregates their replies once all (or a policy-
// defined subset) have finished. Three aggregation modes are supported:
//
//   - collect  - raw child outputs returned as a list (no LLM pass)
//   - concat   - labeled child replies joined into a single text block
//   - reducer  - a final LLM agent pass over labeled child results; both the
//     raw child outputs and the synthesised reply are stored
//
// # Concurrency safety
//
// All mutable state is protected by the run's mu mutex. The subagent semaphore
// is the sole concurrency rate-limiter for child execution; the orchestrator
// does not introduce an additional semaphore so the two systems cannot deadlock
// against each other.
//
// # Cancellation and orphan prevention
//
// When a run is cancelled (via Cancel or a parent context deadline), the
// orchestrator calls Kill on every active child before marking itself done.
// Children that are already finished are unaffected.
package orchestrator

import (
	"context"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/eventbus"
	"github.com/ffimnsr/koios/internal/subagent"
)

// Orchestrator manages fan-out orchestration runs.
type Orchestrator struct {
	subRuntime *subagent.Runtime
	agentRT    *agent.Runtime
	bus        *eventbus.Bus
	ledger     OrchestratorLedger

	mu   sync.Mutex
	runs map[string]*Run
	// cancels maps orchestration run ID -> cancel function.
	cancels map[string]context.CancelFunc
}

// OrchestratorLedger persists orchestration lifecycle events into the unified
// run ledger. Implementations must be safe for concurrent use.
type OrchestratorLedger interface {
	LedgerQueued(id, peerID, sessionKey, model string, queuedAt time.Time)
	LedgerStarted(id string, startedAt time.Time)
	LedgerFinished(id string, finishedAt time.Time, status, errMsg string, steps, promptTokens, completionTokens int)
	LedgerMetadata(id, parentID string, toolCalls int)
}
