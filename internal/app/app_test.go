package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
)

func TestMigrateWorkspaceDatabasesMovesLegacyFiles(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root

	legacyFiles := []string{
		filepath.Join(root, "memory.db"),
		filepath.Join(root, "memory.db-shm"),
		filepath.Join(root, "memory.db-wal"),
	}
	for _, path := range legacyFiles {
		if err := os.WriteFile(path, []byte(path), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	if err := migrateWorkspaceDatabases(cfg); err != nil {
		t.Fatalf("migrateWorkspaceDatabases: %v", err)
	}

	for _, oldPath := range legacyFiles {
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Fatalf("expected %s to be moved, err=%v", oldPath, err)
		}
	}
	for _, newPath := range []string{cfg.MemoryDBPath(), cfg.MemoryDBPath() + "-shm", cfg.MemoryDBPath() + "-wal"} {
		if _, err := os.Stat(newPath); err != nil {
			t.Fatalf("expected %s to exist after migration: %v", newPath, err)
		}
	}
}

func TestMigrateWorkspaceDatabasesDoesNotOverwriteExistingTarget(t *testing.T) {
	root := t.TempDir()
	cfg := config.Default()
	cfg.WorkspaceRoot = root

	legacyPath := filepath.Join(root, "tasks.db")
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0o600); err != nil {
		t.Fatalf("write legacy db: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.TasksDBPath()), 0o755); err != nil {
		t.Fatalf("mkdir target dir: %v", err)
	}
	if err := os.WriteFile(cfg.TasksDBPath(), []byte("current"), 0o600); err != nil {
		t.Fatalf("write target db: %v", err)
	}

	if err := migrateWorkspaceDatabases(cfg); err != nil {
		t.Fatalf("migrateWorkspaceDatabases: %v", err)
	}

	if data, err := os.ReadFile(cfg.TasksDBPath()); err != nil || string(data) != "current" {
		t.Fatalf("target db changed unexpectedly, data=%q err=%v", string(data), err)
	}
	if data, err := os.ReadFile(legacyPath); err != nil || string(data) != "legacy" {
		t.Fatalf("legacy db should remain when target exists, data=%q err=%v", string(data), err)
	}
}
