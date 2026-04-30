package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireGatewayLock_Success(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "koios.lock")
	f, err := acquireGatewayLock(lockPath)
	if err != nil {
		t.Fatalf("expected no error acquiring fresh lock, got: %v", err)
	}
	defer releaseGatewayLock(f, lockPath)

	// Lock file should exist and contain a PID.
	data, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("lock file not readable: %v", err)
	}
	if len(data) == 0 {
		t.Error("lock file should contain a PID but is empty")
	}
}

func TestAcquireGatewayLock_DuplicateBlocked(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "koios.lock")

	// First acquisition must succeed.
	f1, err := acquireGatewayLock(lockPath)
	if err != nil {
		t.Fatalf("first lock acquire failed: %v", err)
	}
	defer releaseGatewayLock(f1, lockPath)

	// Second acquisition from the same process must fail because we already hold
	// the exclusive lock (flock is per open-file-description, so a second
	// OpenFile on the same path gives a new description that is not yet locked).
	// On Linux, flock with LOCK_NB on a file already locked by a different fd
	// in the same process fails with EWOULDBLOCK.
	f2, err := acquireGatewayLock(lockPath)
	if err == nil {
		releaseGatewayLock(f2, lockPath)
		t.Fatal("expected error acquiring lock while already held, got nil")
	}
}

func TestReleaseGatewayLock_RemovesFile(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "koios.lock")
	f, err := acquireGatewayLock(lockPath)
	if err != nil {
		t.Fatalf("acquire failed: %v", err)
	}
	releaseGatewayLock(f, lockPath)

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Error("lock file should be removed after release")
	}
}
