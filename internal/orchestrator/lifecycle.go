package orchestrator

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/eventbus"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/subagent"
)

// New creates an Orchestrator. agentRT is only required when reducer aggregation
// is used; it may be nil if only collect/concat modes are needed.
func New(subRuntime *subagent.Runtime, agentRT *agent.Runtime, bus *eventbus.Bus) *Orchestrator {
	return &Orchestrator{
		subRuntime: subRuntime,
		agentRT:    agentRT,
		bus:        bus,
		runs:       make(map[string]*Run),
		cancels:    make(map[string]context.CancelFunc),
	}
}

// SetLedger attaches the unified run ledger sink used for orchestration runs.
func (o *Orchestrator) SetLedger(ledger OrchestratorLedger) {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.ledger = ledger
	o.mu.Unlock()
}

// Start spawns a new fan-out orchestration and returns the run ID immediately.
// The orchestration executes asynchronously. The supplied ctx is used only as
// the parent for the orchestration's own context; individual child contexts are
// derived from it.
func (o *Orchestrator) Start(ctx context.Context, req FanOutRequest) (*Run, error) {
	if req.PeerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}

	isMultiStage := len(req.Stages) > 0 || req.VerifierTask != nil || req.ArbiterTask != nil
	if len(req.Tasks) == 0 && !isMultiStage {
		return nil, fmt.Errorf("tasks must not be empty")
	}
	if len(req.Stages) > 0 {
		for i, s := range req.Stages {
			if len(s.Tasks) == 0 {
				return nil, fmt.Errorf("stage %d has no tasks", i)
			}
		}
	}

	if req.WaitPolicy == "" {
		req.WaitPolicy = WaitAll
	}
	if req.Aggregation == "" {
		req.Aggregation = AggregateCollect
	}
	if req.Aggregation == AggregateReducer && o.agentRT == nil {
		return nil, fmt.Errorf("reducer aggregation requires an agent runtime")
	}

	if len(req.Tasks) > 0 {
		labelSet := make(map[string]bool, len(req.Tasks))
		for i, t := range req.Tasks {
			lbl := t.Label
			if lbl == "" {
				lbl = fmt.Sprintf("child-%d", i)
			}
			if labelSet[lbl] {
				return nil, fmt.Errorf("duplicate task label %q", lbl)
			}
			labelSet[lbl] = true
		}
		labelToIdx := make(map[string]int, len(req.Tasks))
		for i, t := range req.Tasks {
			lbl := t.Label
			if lbl == "" {
				lbl = fmt.Sprintf("child-%d", i)
			}
			labelToIdx[lbl] = i
		}
		for _, t := range req.Tasks {
			for _, dep := range t.DependsOn {
				if !labelSet[dep] {
					return nil, fmt.Errorf("task %q depends on unknown label %q", t.Label, dep)
				}
			}
		}
		adj := make([][]int, len(req.Tasks))
		for i, t := range req.Tasks {
			for _, dep := range t.DependsOn {
				if j, ok := labelToIdx[dep]; ok {
					adj[i] = append(adj[i], j)
				}
			}
		}
		if detectCycle(len(req.Tasks), adj) {
			return nil, fmt.Errorf("dependency cycle detected in tasks")
		}
	}

	runID := uuid.NewString()
	childRunIDs := make(map[string]int, len(req.Tasks))

	var children []ChildResult
	if !isMultiStage {
		children = make([]ChildResult, len(req.Tasks))
		for i, t := range req.Tasks {
			label := t.Label
			if label == "" {
				label = fmt.Sprintf("child-%d", i)
			}
			children[i] = ChildResult{Label: label}
		}
	}

	run := &Run{
		ID:               runID,
		PeerID:           req.PeerID,
		ParentSessionKey: req.ParentSessionKey,
		ParentRunID:      req.ParentRunID,
		Status:           RunStatusStarting,
		WaitPolicy:       req.WaitPolicy,
		Aggregation:      req.Aggregation,
		ReducerPrompt:    req.ReducerPrompt,
		MaxConcurrency:   req.MaxConcurrency,
		Children:         children,
		childRunIDs:      childRunIDs,
		BarrierGroups:    req.BarrierGroups,
		CreatedAt:        time.Now().UTC(),
	}

	o.mu.Lock()
	o.runs[runID] = run
	ledger := o.ledger
	o.mu.Unlock()
	if ledger != nil {
		sessionKey := req.ParentSessionKey
		if sessionKey == "" {
			sessionKey = req.PeerID
		}
		ledger.LedgerQueued(run.ID, req.PeerID, sessionKey, req.Model, run.CreatedAt)
		ledger.LedgerMetadata(run.ID, req.ParentRunID, 0)
	}

	var orchCtx context.Context
	var cancel context.CancelFunc
	if req.Timeout > 0 {
		orchCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	} else {
		orchCtx, cancel = context.WithCancel(ctx)
	}
	o.mu.Lock()
	o.cancels[runID] = cancel
	o.mu.Unlock()

	if req.AnnounceStart && req.ParentSessionKey != "" {
		taskCount := len(req.Tasks)
		if taskCount == 0 {
			for _, s := range req.Stages {
				taskCount += len(s.Tasks)
			}
		}
		o.publishSession(req.PeerID, req.ParentSessionKey, runID, fmt.Sprintf(
			"[orchestrator:%s] starting fan-out: %d tasks, aggregation=%s",
			runID, taskCount, req.Aggregation,
		))
	}

	go o.execute(orchCtx, cancel, run, req)
	return run, nil
}

// Get returns a snapshot of a run by ID.
func (o *Orchestrator) Get(id string) (*Run, bool) {
	o.mu.Lock()
	r, ok := o.runs[id]
	o.mu.Unlock()
	if !ok {
		return nil, false
	}
	snap := r.snapshot()
	return &snap, true
}

// List returns snapshots of all runs ordered by creation time.
func (o *Orchestrator) List() []*Run {
	o.mu.Lock()
	all := make([]*Run, 0, len(o.runs))
	for _, r := range o.runs {
		all = append(all, r)
	}
	o.mu.Unlock()

	out := make([]*Run, 0, len(all))
	for _, r := range all {
		snap := r.snapshot()
		out = append(out, &snap)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Cancel stops a running orchestration and kills all active children.
func (o *Orchestrator) Cancel(id string) error {
	o.mu.Lock()
	cancel, ok := o.cancels[id]
	o.mu.Unlock()
	if !ok {
		o.mu.Lock()
		_, exists := o.runs[id]
		o.mu.Unlock()
		if !exists {
			return fmt.Errorf("orchestration %s not found", id)
		}
		return fmt.Errorf("orchestration %s is not running", id)
	}
	cancel()
	return nil
}

// Wait blocks until the orchestration with the given ID finishes or ctx expires.
// Returns the final run snapshot.
func (o *Orchestrator) Wait(ctx context.Context, id string) (*Run, error) {
	for {
		o.mu.Lock()
		r, ok := o.runs[id]
		o.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("orchestration %s not found", id)
		}
		r.mu.Lock()
		st := r.Status
		r.mu.Unlock()
		switch st {
		case RunStatusCompleted, RunStatusErrored, RunStatusCancelled:
			snap := r.snapshot()
			return &snap, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// Barrier blocks until all children belonging to the named group reach a
// terminal state, or until ctx expires. Groups are declared via
// FanOutRequest.BarrierGroups. If the group name is empty, all children are
// waited on (equivalent to Wait).
func (o *Orchestrator) Barrier(ctx context.Context, orchID, group string) (*Run, error) {
	for {
		o.mu.Lock()
		r, ok := o.runs[orchID]
		o.mu.Unlock()
		if !ok {
			return nil, fmt.Errorf("orchestration %s not found", orchID)
		}

		r.mu.Lock()
		st := r.Status
		var labels []string
		if group != "" {
			labels = r.BarrierGroups[group]
		}
		r.mu.Unlock()

		switch st {
		case RunStatusCompleted, RunStatusErrored, RunStatusCancelled:
			snap := r.snapshot()
			return &snap, nil
		}

		if group == "" {
			r.mu.Lock()
			all := true
			for _, c := range r.Children {
				switch c.Status {
				case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
				default:
					all = false
				}
			}
			r.mu.Unlock()
			if all {
				snap := r.snapshot()
				return &snap, nil
			}
		} else if len(labels) > 0 {
			labelSet := make(map[string]bool, len(labels))
			for _, l := range labels {
				labelSet[l] = true
			}
			r.mu.Lock()
			groupDone := true
			for _, c := range r.Children {
				if !labelSet[c.Label] {
					continue
				}
				switch c.Status {
				case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
				default:
					groupDone = false
				}
			}
			r.mu.Unlock()
			if groupDone {
				snap := r.snapshot()
				return &snap, nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (o *Orchestrator) currentLedger() OrchestratorLedger {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.ledger
}

func (o *Orchestrator) recordTerminal(run *Run, status runledger.RunStatus, errMsg string) {
	ledger := o.currentLedger()
	if ledger == nil {
		return
	}
	snapshot := run.snapshot()
	finishedAt := snapshot.FinishedAt
	if finishedAt.IsZero() {
		finishedAt = time.Now().UTC()
	}
	ledger.LedgerMetadata(snapshot.ID, snapshot.ParentRunID, totalToolCalls(snapshot.Children))
	ledger.LedgerFinished(snapshot.ID, finishedAt, string(status), errMsg, totalSteps(snapshot.Children), 0, 0)
}

func totalSteps(children []ChildResult) int {
	total := 0
	for _, child := range children {
		total += child.Steps
	}
	return total
}

func totalToolCalls(children []ChildResult) int {
	total := 0
	for _, child := range children {
		total += child.ToolCalls
	}
	return total
}

// maxConcurrency returns a channel capacity for the semaphore. When limit is
// 0 or negative, total is used so there is effectively no artificial cap.
func maxConcurrency(limit, total int) int {
	if limit > 0 && limit < total {
		return limit
	}
	if total <= 0 {
		return 1
	}
	return total
}

// waitPolicySatisfied returns true when the configured wait policy threshold
// has been met.
func (o *Orchestrator) waitPolicySatisfied(run *Run, policy WaitPolicy, total int) bool {
	if total == 0 {
		return true
	}
	run.mu.Lock()
	defer run.mu.Unlock()

	completed := 0
	for _, c := range run.Children {
		if c.Status == subagent.StatusCompleted {
			completed++
		}
	}
	switch policy {
	case WaitFirst:
		return completed >= 1
	case WaitQuorum:
		return completed >= (total/2 + 1)
	default:
		finished := 0
		for _, c := range run.Children {
			switch c.Status {
			case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
				finished++
			}
		}
		return finished >= total
	}
}

func (o *Orchestrator) markCancelled(run *Run) {
	now := time.Now().UTC()
	run.mu.Lock()
	defer run.mu.Unlock()
	run.Status = RunStatusCancelled
	run.FinishedAt = now
	for i := range run.Children {
		if run.Children[i].Status == "" ||
			run.Children[i].Status == subagent.StatusQueued ||
			run.Children[i].Status == subagent.StatusRunning {
			run.Children[i].Status = subagent.StatusKilled
		}
	}
}

func detectCycle(n int, adj [][]int) bool {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make([]int, n)
	var dfs func(v int) bool
	dfs = func(v int) bool {
		color[v] = gray
		for _, w := range adj[v] {
			if color[w] == gray {
				return true
			}
			if color[w] == white && dfs(w) {
				return true
			}
		}
		color[v] = black
		return false
	}
	for i := 0; i < n; i++ {
		if color[i] == white && dfs(i) {
			return true
		}
	}
	return false
}

func stageWaitSatisfied(children []ChildResult, policy WaitPolicy) bool {
	total := len(children)
	if total == 0 {
		return true
	}
	completed := 0
	finished := 0
	for _, c := range children {
		if c.Status == subagent.StatusCompleted {
			completed++
		}
		switch c.Status {
		case subagent.StatusCompleted, subagent.StatusErrored, subagent.StatusKilled:
			finished++
		}
	}
	switch policy {
	case WaitFirst:
		return completed >= 1
	case WaitQuorum:
		return completed >= (total/2 + 1)
	default:
		return finished >= total
	}
}
