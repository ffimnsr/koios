// Package monitor provides the gateway health monitor, which periodically
// checks for stale activity and tracks subsystem restart counts. It also
// exposes helpers for the /healthz extended payload.
package monitor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// ActivityTracker is satisfied by any component that can report its last
// activity time (e.g. the session store or HTTP request logger).
type ActivityTracker interface {
	LastActivity() time.Time
}

// SubsystemRestarter is a function that can restart a named subsystem.
// It returns an error if the restart fails.
type SubsystemRestarter func(ctx context.Context) error

// entry tracks restart state for one subsystem.
type entry struct {
	mu          sync.Mutex
	name        string
	restarts    int
	maxRestarts int
	restarter   SubsystemRestarter
	lastRestart time.Time
}

// Monitor watches the gateway for stale activity and manages restarts.
type Monitor struct {
	staleThreshold time.Duration
	maxRestarts    int
	trackers       []ActivityTracker
	subsystems     map[string]*entry
	mu             sync.Mutex

	// lastRequestTime is updated by TouchActivity; it is the primary stale check.
	lastRequestTime atomic.Value // stores time.Time

	warnCount int64 // total stale-warnings emitted
}

// New creates a Monitor. staleThreshold ≤ 0 disables stale checking;
// maxRestarts ≤ 0 means unlimited per subsystem.
func New(staleThreshold time.Duration, maxRestarts int) *Monitor {
	m := &Monitor{
		staleThreshold: staleThreshold,
		maxRestarts:    maxRestarts,
		subsystems:     make(map[string]*entry),
	}
	m.lastRequestTime.Store(time.Now())
	return m
}

// TouchActivity records that the gateway received external activity right now.
// Call this from HTTP middleware or the WebSocket connection handler.
func (m *Monitor) TouchActivity() {
	m.lastRequestTime.Store(time.Now())
}

// RegisterSubsystem registers a named subsystem with an optional restart
// function. Sub-zero maxRestarts inherits the monitor's global cap.
func (m *Monitor) RegisterSubsystem(name string, restarter SubsystemRestarter) {
	e := &entry{
		name:        name,
		maxRestarts: m.maxRestarts,
		restarter:   restarter,
	}
	m.mu.Lock()
	m.subsystems[name] = e
	m.mu.Unlock()
}

// RestartSubsystem attempts to restart the named subsystem.
// Returns an error when: the subsystem is not registered, has no restarter,
// or the restart cap has been reached.
func (m *Monitor) RestartSubsystem(ctx context.Context, name string) error {
	m.mu.Lock()
	e, ok := m.subsystems[name]
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("monitor: subsystem %q is not registered", name)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.restarter == nil {
		return fmt.Errorf("monitor: subsystem %q has no restarter", name)
	}
	if e.maxRestarts > 0 && e.restarts >= e.maxRestarts {
		return fmt.Errorf("monitor: subsystem %q has reached max restarts (%d)", name, e.maxRestarts)
	}
	if err := e.restarter(ctx); err != nil {
		return fmt.Errorf("monitor: restart %q: %w", name, err)
	}
	e.restarts++
	e.lastRestart = time.Now()
	slog.Info("monitor: subsystem restarted", "name", name, "restarts", e.restarts)
	return nil
}

// Status returns a snapshot of the monitor's current state for /healthz.
func (m *Monitor) Status() map[string]any {
	last, _ := m.lastRequestTime.Load().(time.Time)
	idle := time.Since(last).Round(time.Second)
	stale := false
	if m.staleThreshold > 0 && idle >= m.staleThreshold {
		stale = true
	}
	m.mu.Lock()
	subs := make(map[string]any, len(m.subsystems))
	for name, e := range m.subsystems {
		e.mu.Lock()
		subs[name] = map[string]any{
			"restarts":     e.restarts,
			"max_restarts": e.maxRestarts,
			"last_restart": e.lastRestart,
		}
		e.mu.Unlock()
	}
	m.mu.Unlock()
	return map[string]any{
		"last_activity":   last,
		"idle_for":        idle.String(),
		"stale":           stale,
		"stale_threshold": m.staleThreshold.String(),
		"warn_count":      m.warnCount,
		"subsystems":      subs,
	}
}

// Run starts the monitor loop and blocks until ctx is cancelled.
// It logs stale-activity warnings when the gateway has been idle longer
// than the configured threshold.
func (m *Monitor) Run(ctx context.Context) {
	if m.staleThreshold <= 0 {
		// Stale detection disabled; block until cancelled.
		<-ctx.Done()
		return
	}
	tick := time.NewTicker(m.staleThreshold / 2)
	if m.staleThreshold/2 < time.Second {
		tick = time.NewTicker(time.Second)
	}
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			last, _ := m.lastRequestTime.Load().(time.Time)
			if now.Sub(last) >= m.staleThreshold {
				atomic.AddInt64(&m.warnCount, 1)
				slog.Warn("monitor: gateway has been idle",
					"idle_for", now.Sub(last).Round(time.Second).String(),
					"threshold", m.staleThreshold.String(),
				)
			}
		}
	}
}
