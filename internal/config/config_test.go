package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsInvalidCodeExecutionLimits(t *testing.T) {
	cfg := Default()
	cfg.CodeExecutionDefaultTimeout = 20 * time.Second
	cfg.CodeExecutionMaxTimeout = 10 * time.Second
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "tools.code_execution.max_timeout") {
		t.Fatalf("expected code_execution timeout validation error, got %v", err)
	}
}

func TestLoadFromPathParsesCodeExecutionConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "koios.config.toml")
	content := `
[llm]
provider = "openai"
model = "gpt-4o"

[workspace]
root = "./workspace"

[tools.code_execution]
enabled = true
network_enabled = true
default_timeout = "7s"
max_timeout = "9s"
max_stdout_bytes = 123
max_stderr_bytes = 456
max_artifact_bytes = 789
max_open_files = 11
max_processes = 12
cpu_seconds = 13
memory_bytes = 1048576
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if !cfg.CodeExecutionEnabled || !cfg.CodeExecutionNetworkEnabled {
		t.Fatalf("unexpected code_execution booleans: %#v", cfg)
	}
	if cfg.CodeExecutionDefaultTimeout != 7*time.Second || cfg.CodeExecutionMaxTimeout != 9*time.Second {
		t.Fatalf("unexpected code_execution timeouts: %#v", cfg)
	}
	if cfg.CodeExecutionMaxStdoutBytes != 123 || cfg.CodeExecutionMaxStderrBytes != 456 || cfg.CodeExecutionMaxArtifactBytes != 789 {
		t.Fatalf("unexpected code_execution byte limits: %#v", cfg)
	}
	if cfg.CodeExecutionMaxOpenFiles != 11 || cfg.CodeExecutionMaxProcesses != 12 || cfg.CodeExecutionCPUSeconds != 13 || cfg.CodeExecutionMemoryBytes != 1048576 {
		t.Fatalf("unexpected code_execution resource limits: %#v", cfg)
	}
}

func TestLoadFromPathParsesProcessConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "koios.config.toml")
	content := `
[llm]
provider = "openai"
model = "gpt-4o"

[workspace]
root = "./workspace"

[tools.process]
enabled = true
stop_timeout = "9s"
log_tail_bytes = 2048
max_processes_per_peer = 3
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if !cfg.ProcessEnabled {
		t.Fatalf("expected process tool enabled: %#v", cfg)
	}
	if cfg.ProcessStopTimeout != 9*time.Second || cfg.ProcessLogTailBytes != 2048 || cfg.ProcessMaxProcessesPerPeer != 3 {
		t.Fatalf("unexpected process config: %#v", cfg)
	}
}

func TestValidateRejectsInvalidProcessLimits(t *testing.T) {
	cfg := Default()
	cfg.ProcessLogTailBytes = 0
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "tools.process.log_tail_bytes") {
		t.Fatalf("expected process log-tail validation error, got %v", err)
	}
}
