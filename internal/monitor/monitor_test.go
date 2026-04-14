package monitor_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/monitor"
)

func TestTouchActivity(t *testing.T) {
	m := monitor.New(5*time.Second, 3)
	status := m.Status()
	stale, _ := status["stale"].(bool)
	if stale {
		t.Error("expected not stale immediately after creation")
	}
}

func TestRestartSubsystem(t *testing.T) {
	restarted := 0
	m := monitor.New(0, 2)
	m.RegisterSubsystem("sched", func(_ context.Context) error {
		restarted++
		return nil
	})

	if err := m.RestartSubsystem(context.Background(), "sched"); err != nil {
		t.Fatalf("unexpected restart error: %v", err)
	}
	if err := m.RestartSubsystem(context.Background(), "sched"); err != nil {
		t.Fatalf("unexpected restart error: %v", err)
	}
	// third restart should fail due to max cap
	if err := m.RestartSubsystem(context.Background(), "sched"); err == nil {
		t.Error("expected error after max_restarts exceeded")
	}
	if restarted != 2 {
		t.Errorf("expected 2 restart calls, got %d", restarted)
	}
}

func TestRestartSubsystemUnknown(t *testing.T) {
	m := monitor.New(0, 3)
	if err := m.RestartSubsystem(context.Background(), "unknown"); err == nil {
		t.Error("expected error for unregistered subsystem")
	}
}

func TestRestartSubsystemError(t *testing.T) {
	m := monitor.New(0, 3)
	m.RegisterSubsystem("bad", func(_ context.Context) error {
		return errors.New("boom")
	})
	if err := m.RestartSubsystem(context.Background(), "bad"); err == nil {
		t.Error("expected error when restarter fails")
	}
	// restart count should not increment on failure
	status := m.Status()
	subs, _ := status["subsystems"].(map[string]any)
	info, _ := subs["bad"].(map[string]any)
	count, _ := info["restarts"].(int)
	if count != 0 {
		t.Errorf("expected 0 restarts on failure, got %d", count)
	}
}

func TestRunCancelledImmediately(t *testing.T) {
	m := monitor.New(time.Hour, 5)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Error("Run did not exit after ctx cancellation")
	}
}
