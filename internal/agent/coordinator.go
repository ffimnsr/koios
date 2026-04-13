package agent

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// laneIdleTTL is how long a session lane goroutine waits with no activity
	// before it terminates and removes itself from the lanes map.  Isolated-scope
	// subagent sessions (each with a unique UUID key) would otherwise leak a
	// goroutine per run indefinitely.
	laneIdleTTL = 10 * time.Minute

	// runRecordTTL is how long a terminal async run record is kept before the
	// background sweeper removes it from memory.
	runRecordTTL = 2 * time.Hour
)

type RunStatus string

const (
	StatusQueued    RunStatus = "queued"
	StatusRunning   RunStatus = "running"
	StatusCompleted RunStatus = "completed"
	StatusErrored   RunStatus = "errored"
	StatusCanceled  RunStatus = "canceled"
)

type RunRecord struct {
	ID         string     `json:"id"`
	SessionKey string     `json:"session_key"`
	Status     RunStatus  `json:"status"`
	Error      string     `json:"error,omitempty"`
	Result     *Result    `json:"result,omitempty"`
	QueuedAt   time.Time  `json:"queued_at"`
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

type laneTask struct {
	ctx     context.Context
	exec    func(context.Context) (*Result, error)
	onStart func()
	onDone  func(*Result, error)
	done    chan taskResult
}

type taskResult struct {
	result *Result
	err    error
}

// laneEntry holds a session-scoped dispatch channel and a last-activity
// timestamp used to reclaim idle lanes.
type laneEntry struct {
	ch           chan *laneTask
	lastActivity time.Time
}

// Coordinator serializes runs per session key and supports queued async runs.
type Coordinator struct {
	runtime *Runtime

	mu    sync.Mutex
	lanes map[string]*laneEntry
	runs  map[string]*asyncRun

	stop chan struct{}
	wg   sync.WaitGroup
}

type asyncRun struct {
	record RunRecord
	done   chan struct{}
	cancel context.CancelFunc
}

func NewCoordinator(runtime *Runtime) *Coordinator {
	c := &Coordinator{
		runtime: runtime,
		lanes:   make(map[string]*laneEntry),
		runs:    make(map[string]*asyncRun),
		stop:    make(chan struct{}),
	}
	c.wg.Add(1)
	go c.sweepLoop()
	return c
}

// Stop signals the background sweeper goroutine to exit and waits for it.
func (c *Coordinator) Stop() {
	close(c.stop)
	c.wg.Wait()
}

// sweepLoop periodically removes terminal run records older than runRecordTTL.
func (c *Coordinator) sweepLoop() {
	defer c.wg.Done()
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-ticker.C:
			c.sweepRuns()
		}
	}
}

func (c *Coordinator) sweepRuns() {
	cutoff := time.Now().Add(-runRecordTTL)
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, run := range c.runs {
		switch run.record.Status {
		case StatusCompleted, StatusErrored, StatusCanceled:
			if run.record.FinishedAt != nil && run.record.FinishedAt.Before(cutoff) {
				delete(c.runs, id)
			}
		}
	}
}

func (c *Coordinator) Run(ctx context.Context, req RunRequest) (*Result, error) {
	return c.enqueue(ctx, req, func(runCtx context.Context) (*Result, error) {
		return c.runtime.Run(runCtx, req)
	})
}

func (c *Coordinator) RunStream(ctx context.Context, req RunRequest, w http.ResponseWriter) (*Result, error) {
	return c.enqueue(ctx, req, func(runCtx context.Context) (*Result, error) {
		return c.runtime.RunStream(runCtx, req, w)
	})
}

func (c *Coordinator) Start(req RunRequest) (*RunRecord, error) {
	if c.runtime == nil {
		return nil, fmt.Errorf("agent runtime is not enabled")
	}
	sessionKey := c.runtime.sessionKey(req)
	req.SessionKey = sessionKey
	id := uuid.NewString()
	now := time.Now().UTC()
	run := &asyncRun{
		record: RunRecord{
			ID:         id,
			SessionKey: sessionKey,
			Status:     StatusQueued,
			QueuedAt:   now,
		},
		done: make(chan struct{}),
	}

	c.mu.Lock()
	c.runs[id] = run
	entry := c.laneLocked(sessionKey)
	c.mu.Unlock()

	runCtx, cancel := context.WithCancel(context.Background())
	run.cancel = cancel

	entry.ch <- &laneTask{
		ctx: runCtx,
		exec: func(runCtx context.Context) (*Result, error) {
			return c.runtime.Run(runCtx, req)
		},
		onStart: func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			started := time.Now().UTC()
			run.record.Status = StatusRunning
			run.record.StartedAt = &started
		},
		onDone: func(result *Result, err error) {
			c.mu.Lock()
			defer c.mu.Unlock()
			finished := time.Now().UTC()
			if err != nil {
				if err == context.Canceled {
					run.record.Status = StatusCanceled
				} else {
					run.record.Status = StatusErrored
					run.record.Error = err.Error()
				}
			} else {
				run.record.Status = StatusCompleted
				run.record.Result = result
			}
			run.record.FinishedAt = &finished
			close(run.done)
		},
	}

	record := run.record
	return &record, nil
}

func (c *Coordinator) Cancel(id string) (*RunRecord, error) {
	c.mu.Lock()
	run, ok := c.runs[id]
	if !ok {
		c.mu.Unlock()
		return nil, fmt.Errorf("run %s not found", id)
	}
	cancel := run.cancel
	c.mu.Unlock()
	if cancel == nil {
		return nil, fmt.Errorf("run %s cannot be canceled", id)
	}
	cancel()
	record, err := c.Wait(context.Background(), id)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (c *Coordinator) Get(id string) (*RunRecord, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	run, ok := c.runs[id]
	if !ok {
		return nil, false
	}
	record := run.record
	return &record, true
}

func (c *Coordinator) Wait(ctx context.Context, id string) (*RunRecord, error) {
	c.mu.Lock()
	run, ok := c.runs[id]
	c.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("run %s not found", id)
	}
	select {
	case <-run.done:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	record := run.record
	return &record, nil
}

func (c *Coordinator) enqueue(ctx context.Context, req RunRequest, exec func(context.Context) (*Result, error)) (*Result, error) {
	if c.runtime == nil {
		return nil, fmt.Errorf("agent runtime is not enabled")
	}
	sessionKey := c.runtime.sessionKey(req)
	ch := func() chan *laneTask {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.laneLocked(sessionKey).ch
	}()
	done := make(chan taskResult, 1)
	task := &laneTask{
		ctx:  ctx,
		exec: exec,
		done: done,
	}

	select {
	case ch <- task:
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	select {
	case out := <-done:
		return out.result, out.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// laneLocked returns the laneEntry for sessionKey, creating it if needed.
// Updates lastActivity so the idle sweeper doesn't reclaim an in-use lane.
// Must be called with c.mu held.
func (c *Coordinator) laneLocked(sessionKey string) *laneEntry {
	entry, ok := c.lanes[sessionKey]
	if ok {
		entry.lastActivity = time.Now()
		return entry
	}
	entry = &laneEntry{
		ch:           make(chan *laneTask, 64),
		lastActivity: time.Now(),
	}
	c.lanes[sessionKey] = entry
	go c.runLane(sessionKey, entry)
	return entry
}

func (c *Coordinator) runLane(key string, e *laneEntry) {
	idleTimer := time.NewTimer(laneIdleTTL)
	defer idleTimer.Stop()

	processTask := func(task *laneTask) {
		if task.onStart != nil {
			task.onStart()
		}
		if task.ctx != nil && task.ctx.Err() != nil {
			err := task.ctx.Err()
			if task.onDone != nil {
				task.onDone(nil, err)
			}
			if task.done != nil {
				task.done <- taskResult{err: err}
			}
			return
		}
		result, err := task.exec(task.ctx)
		if task.onDone != nil {
			task.onDone(result, err)
		}
		if task.done != nil {
			task.done <- taskResult{result: result, err: err}
		}
	}

	for {
		select {
		case task := <-e.ch:
			if !idleTimer.Stop() {
				select {
				case <-idleTimer.C:
				default:
				}
			}
			idleTimer.Reset(laneIdleTTL)
			processTask(task)

		case <-idleTimer.C:
			// Only remove from the map if there are no pending tasks and the
			// lastActivity timestamp hasn't been refreshed (meaning no caller
			// has just borrowed this lane and is about to send).
			c.mu.Lock()
			if current, ok := c.lanes[key]; ok && current == e &&
				len(e.ch) == 0 && time.Since(e.lastActivity) >= laneIdleTTL {
				delete(c.lanes, key)
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			// There are pending tasks or the lane was recently re-acquired;
			// reset the timer and keep running.
			idleTimer.Reset(laneIdleTTL)
		}
	}
}
