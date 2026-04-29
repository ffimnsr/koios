package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEncodeTOMLRoundTripsRetryStatusCodes(t *testing.T) {
	cfg := Default()
	cfg.LLMIdleTimeout = 42 * time.Second
	cfg.AgentRetryStatusCodes = []int{429, 500, 503}
	cfg.ToolsAllow = []string{"read", "write"}
	cfg.ToolsDeny = []string{"exec"}
	cfg.ExecCustomDenyPatterns = []string{"rm -rf"}
	cfg.ExecCustomAllowPatterns = []string{"git status"}
	cfg.ExecIsolationEnabled = true
	cfg.ExecIsolationPaths = []ExecIsolationPath{{Source: "/tmp/src", Target: "/mnt/src", Mode: "ro"}}
	encoded := EncodeTOML(cfg, false)
	if !strings.Contains(encoded, "idle_timeout = \"42s\"") {
		t.Fatalf("expected idle_timeout in encoded config, got:\n%s", encoded)
	}
	if !strings.Contains(encoded, "retry_status_codes = [429, 500, 503]") {
		t.Fatalf("expected retry_status_codes array in encoded config, got:\n%s", encoded)
	}
	for _, expected := range []string{
		`allow = ["read", "write"]`,
		`deny = ["exec"]`,
		`custom_deny_patterns = ["rm -rf"]`,
		`custom_allow_patterns = ["git status"]`,
		`expose_paths = [{ source = "/tmp/src", target = "/mnt/src", mode = "ro" }]`,
	} {
		if !strings.Contains(encoded, expected) {
			t.Fatalf("expected encoded config to contain %q, got:\n%s", expected, encoded)
		}
	}

	dir := t.TempDir()
	path := filepath.Join(dir, DefaultConfigFile)
	if err := os.WriteFile(path, []byte(encoded), 0o644); err != nil {
		t.Fatalf("write encoded config: %v", err)
	}
	loaded, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if len(loaded.AgentRetryStatusCodes) != 3 || loaded.AgentRetryStatusCodes[0] != 429 || loaded.AgentRetryStatusCodes[1] != 500 || loaded.AgentRetryStatusCodes[2] != 503 {
		t.Fatalf("unexpected retry_status_codes after round-trip: %#v", loaded.AgentRetryStatusCodes)
	}
	if loaded.LLMIdleTimeout != 42*time.Second {
		t.Fatalf("unexpected llm.idle_timeout after round-trip: %s", loaded.LLMIdleTimeout)
	}
	if len(loaded.ToolsAllow) != 2 || loaded.ToolsAllow[0] != "read" || loaded.ToolsAllow[1] != "write" {
		t.Fatalf("unexpected tools.allow after round-trip: %#v", loaded.ToolsAllow)
	}
	if len(loaded.ToolsDeny) != 1 || loaded.ToolsDeny[0] != "exec" {
		t.Fatalf("unexpected tools.deny after round-trip: %#v", loaded.ToolsDeny)
	}
	if len(loaded.ExecCustomDenyPatterns) != 1 || loaded.ExecCustomDenyPatterns[0] != "rm -rf" {
		t.Fatalf("unexpected custom deny patterns after round-trip: %#v", loaded.ExecCustomDenyPatterns)
	}
	if len(loaded.ExecCustomAllowPatterns) != 1 || loaded.ExecCustomAllowPatterns[0] != "git status" {
		t.Fatalf("unexpected custom allow patterns after round-trip: %#v", loaded.ExecCustomAllowPatterns)
	}
	if len(loaded.ExecIsolationPaths) != 1 || loaded.ExecIsolationPaths[0].Source != "/tmp/src" || loaded.ExecIsolationPaths[0].Target != "/mnt/src" || loaded.ExecIsolationPaths[0].Mode != "ro" {
		t.Fatalf("unexpected isolation paths after round-trip: %#v", loaded.ExecIsolationPaths)
	}
}

func TestValidateRejectsInvalidCodeExecutionLimits(t *testing.T) {
	cfg := Default()
	cfg.CodeExecutionDefaultTimeout = 20 * time.Second
	cfg.CodeExecutionMaxTimeout = 10 * time.Second
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "tools.code_execution.max_timeout") {
		t.Fatalf("expected code_execution timeout validation error, got %v", err)
	}
}

func TestValidateRejectsNegativeLLMIdleTimeout(t *testing.T) {
	cfg := Default()
	cfg.LLMIdleTimeout = -1 * time.Second
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "llm.idle_timeout") {
		t.Fatalf("expected llm idle timeout validation error, got %v", err)
	}
}

func TestLoadFromPathParsesLLMIdleTimeout(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "koios.config.toml")
	content := `
[llm]
idle_timeout = "11s"
provider = "openai"
model = "gpt-4o"

[workspace]
root = "./workspace"
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if cfg.LLMIdleTimeout != 11*time.Second {
		t.Fatalf("unexpected llm idle timeout: %s", cfg.LLMIdleTimeout)
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
