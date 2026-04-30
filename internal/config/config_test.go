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
	cfg.ExtensionDirs = []string{"./extensions-extra", "/opt/koios/extensions"}
	cfg.ExtensionAllow = []string{"filesystem", "demo.http"}
	cfg.ExtensionDeny = []string{"blocked"}
	cfg.ExecCustomDenyPatterns = []string{"rm -rf"}
	cfg.ExecCustomAllowPatterns = []string{"git status"}
	cfg.ExecIsolationEnabled = true
	cfg.ExecIsolationPaths = []ExecIsolationPath{{Source: "/tmp/src", Target: "/mnt/src", Mode: "ro"}}
	cfg.Telegram = TelegramChannelConfig{
		Enabled:          true,
		BotToken:         "123:token",
		Mode:             "webhook",
		WebhookURL:       "https://example.com/v1/channels/telegram/webhook",
		WebhookSecret:    "secret",
		InboxPeerID:      "default",
		PollTimeout:      45 * time.Second,
		TextChunkLimit:   2048,
		TextChunkMode:    "hard",
		StreamQueueMode:  "throttle",
		StreamThrottle:   1500 * time.Millisecond,
		AllowedChatIDs:   []int64{123, 456},
		AllowedSenderIDs: []int64{7, 8},
		CommandSenderIDs: []int64{7},
		ActivationMode:   "mention",
		ReplyActivation:  true,
		DMPolicy:         "pairing",
		PairingCodeTTL:   90 * time.Minute,
		ThreadMode:       "chat",
		GroupPolicies: []TelegramGroupPolicy{{
			ChatID:           123,
			ActivationMode:   "always",
			AllowedSenderIDs: []int64{7},
			CommandSenderIDs: []int64{8},
			AllowedTopicIDs:  []int64{99},
			ThreadMode:       "topic",
		}},
	}
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
		`dirs = ["./extensions-extra", "/opt/koios/extensions"]`,
		`allow = ["filesystem", "demo.http"]`,
		`deny = ["blocked"]`,
		`[channels.telegram]`,
		`bot_token = "123:token"`,
		`mode = "webhook"`,
		`inbox_peer_id = "default"`,
		`text_chunk_limit = 2048`,
		`text_chunk_mode = "hard"`,
		`stream_queue_mode = "throttle"`,
		`stream_throttle = "1.5s"`,
		`allowed_chat_ids = [123, 456]`,
		`allowed_sender_ids = [7, 8]`,
		`command_sender_ids = [7]`,
		`activation_mode = "mention"`,
		`reply_activation = true`,
		`dm_policy = "pairing"`,
		`pairing_code_ttl = "1h30m0s"`,
		`thread_mode = "chat"`,
		`[[channels.telegram.groups]]`,
		`command_sender_ids = [8]`,
		`allowed_topic_ids = [99]`,
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
	if len(loaded.ExtensionDirs) != 2 || loaded.ExtensionDirs[0] != filepath.Join(dir, "extensions-extra") || loaded.ExtensionDirs[1] != "/opt/koios/extensions" {
		t.Fatalf("unexpected extension dirs after round-trip: %#v", loaded.ExtensionDirs)
	}
	if len(loaded.ExtensionAllow) != 2 || loaded.ExtensionAllow[0] != "filesystem" || loaded.ExtensionAllow[1] != "demo.http" {
		t.Fatalf("unexpected extension allow list after round-trip: %#v", loaded.ExtensionAllow)
	}
	if len(loaded.ExtensionDeny) != 1 || loaded.ExtensionDeny[0] != "blocked" {
		t.Fatalf("unexpected extension deny list after round-trip: %#v", loaded.ExtensionDeny)
	}
	if len(loaded.ExecIsolationPaths) != 1 || loaded.ExecIsolationPaths[0].Source != "/tmp/src" || loaded.ExecIsolationPaths[0].Target != "/mnt/src" || loaded.ExecIsolationPaths[0].Mode != "ro" {
		t.Fatalf("unexpected isolation paths after round-trip: %#v", loaded.ExecIsolationPaths)
	}
	if !loaded.Telegram.Enabled || loaded.Telegram.BotToken != "123:token" || loaded.Telegram.Mode != "webhook" {
		t.Fatalf("unexpected telegram config after round-trip: %#v", loaded.Telegram)
	}
	if loaded.Telegram.InboxPeerID != "default" {
		t.Fatalf("unexpected telegram inbox peer after round-trip: %#v", loaded.Telegram)
	}
	if len(loaded.Telegram.AllowedChatIDs) != 2 || loaded.Telegram.AllowedChatIDs[0] != 123 || loaded.Telegram.AllowedChatIDs[1] != 456 {
		t.Fatalf("unexpected telegram allowlist after round-trip: %#v", loaded.Telegram.AllowedChatIDs)
	}
	if len(loaded.Telegram.AllowedSenderIDs) != 2 || loaded.Telegram.AllowedSenderIDs[0] != 7 || loaded.Telegram.AllowedSenderIDs[1] != 8 {
		t.Fatalf("unexpected telegram sender allowlist after round-trip: %#v", loaded.Telegram.AllowedSenderIDs)
	}
	if len(loaded.Telegram.CommandSenderIDs) != 1 || loaded.Telegram.CommandSenderIDs[0] != 7 {
		t.Fatalf("unexpected telegram command sender ids after round-trip: %#v", loaded.Telegram.CommandSenderIDs)
	}
	if loaded.Telegram.TextChunkLimit != 2048 || loaded.Telegram.TextChunkMode != "hard" || loaded.Telegram.StreamQueueMode != "throttle" || loaded.Telegram.StreamThrottle != 1500*time.Millisecond || loaded.Telegram.ActivationMode != "mention" || !loaded.Telegram.ReplyActivation || loaded.Telegram.DMPolicy != "pairing" || loaded.Telegram.PairingCodeTTL != 90*time.Minute || loaded.Telegram.ThreadMode != "chat" {
		t.Fatalf("unexpected telegram policies after round-trip: %#v", loaded.Telegram)
	}
	if len(loaded.Telegram.GroupPolicies) != 1 || loaded.Telegram.GroupPolicies[0].ChatID != 123 || loaded.Telegram.GroupPolicies[0].ThreadMode != "topic" || len(loaded.Telegram.GroupPolicies[0].CommandSenderIDs) != 1 || loaded.Telegram.GroupPolicies[0].CommandSenderIDs[0] != 8 {
		t.Fatalf("unexpected telegram group policies after round-trip: %#v", loaded.Telegram.GroupPolicies)
	}
	if loaded.Telegram.PollTimeout != 45*time.Second {
		t.Fatalf("unexpected telegram flags after round-trip: %#v", loaded.Telegram)
	}
}

func TestValidateRejectsBlankExtensionDirs(t *testing.T) {
	cfg := Default()
	cfg.ExtensionDirs = []string{"/ok", "   "}
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "extensions.dirs[1]") {
		t.Fatalf("expected extension dir validation error, got %v", err)
	}
}

func TestValidateRejectsInvalidTelegramInboxPeerID(t *testing.T) {
	cfg := Default()
	cfg.Telegram.Enabled = true
	cfg.Telegram.BotToken = "token"
	cfg.Telegram.InboxPeerID = "bad peer"
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "channels.telegram.inbox_peer_id") {
		t.Fatalf("expected telegram inbox peer validation error, got %v", err)
	}
}

func TestValidateRejectsBlankExtensionAllowEntries(t *testing.T) {
	cfg := Default()
	cfg.ExtensionAllow = []string{"ok", "   "}
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "extensions.allow[1]") {
		t.Fatalf("expected extension allow validation error, got %v", err)
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
default_profile = "default"
idle_timeout = "11s"

[[llm.profiles]]
name = "default"
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
default_profile = "default"

[[llm.profiles]]
name = "default"
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

func TestLoadFromPathAllowsDisablingMemoryEmbeddings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "koios.config.toml")
	content := `
[llm]
default_profile = "default"

[[llm.profiles]]
name = "default"
provider = "openai"
model = "gpt-4o"

[workspace]
root = "./workspace"

[memory.embed]
enabled = false
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if cfg.MemoryEmbedEnabled {
		t.Fatalf("expected memory embeddings disabled, got enabled")
	}
}

func TestLoadFromPathParsesProcessConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "koios.config.toml")
	content := `
[llm]
default_profile = "default"

[[llm.profiles]]
name = "default"
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

func TestDatabasePathsUseWorkspaceDBDir(t *testing.T) {
	cfg := Default()
	cfg.WorkspaceRoot = "/tmp/koios-workspace"

	if got, want := cfg.DBDir(), "/tmp/koios-workspace/db"; got != want {
		t.Fatalf("DBDir() = %q want %q", got, want)
	}
	if got, want := filepath.Base(cfg.ChannelBindingsPath()), "channel_bindings.json"; got != want {
		t.Fatalf("ChannelBindingsPath() base = %q want %q", got, want)
	}

	checks := map[string]string{
		"ChannelBindingsPath": cfg.ChannelBindingsPath(),
		"MemoryDBPath":        cfg.MemoryDBPath(),
		"TasksDBPath":         cfg.TasksDBPath(),
		"BookmarksDBPath":     cfg.BookmarksDBPath(),
		"CalendarDBPath":      cfg.CalendarDBPath(),
		"NotesDBPath":         cfg.NotesDBPath(),
		"PlansDBPath":         cfg.PlansDBPath(),
		"ProjectsDBPath":      cfg.ProjectsDBPath(),
		"ArtifactsDBPath":     cfg.ArtifactsDBPath(),
		"DecisionsDBPath":     cfg.DecisionsDBPath(),
		"PreferencesDBPath":   cfg.PreferencesDBPath(),
		"RemindersDBPath":     cfg.RemindersDBPath(),
	}
	for name, path := range checks {
		if got, want := filepath.Dir(path), cfg.DBDir(); got != want {
			t.Fatalf("%s() dir = %q want %q", name, got, want)
		}
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

func TestLoadFromPathParsesTelegramChannelConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "koios.config.toml")
	content := `
[llm]
default_profile = "default"

[[llm.profiles]]
name = "default"
provider = "openai"
model = "gpt-4o"

[workspace]
root = "./workspace"

[channels.telegram]
enabled = true
bot_token = "abc"
mode = "polling"
poll_timeout = "33s"
text_chunk_limit = 3000
text_chunk_mode = "word"
stream_queue_mode = "throttle"
stream_throttle = "900ms"
allowed_chat_ids = [1, 2]
allowed_sender_ids = [7]
command_sender_ids = [9]
activation_mode = "mention"
reply_activation = false
dm_policy = "pairing"
pairing_code_ttl = "2h"
thread_mode = "chat"

[[channels.telegram.groups]]
chat_id = 2
activation_mode = "always"
command_sender_ids = [11]
reply_activation = true
allowed_topic_ids = [11]
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := LoadFromPath(path)
	if err != nil {
		t.Fatalf("LoadFromPath: %v", err)
	}
	if !cfg.Telegram.Enabled || cfg.Telegram.BotToken != "abc" || cfg.Telegram.Mode != "polling" {
		t.Fatalf("unexpected telegram config: %#v", cfg.Telegram)
	}
	if cfg.Telegram.PollTimeout != 33*time.Second || cfg.Telegram.TextChunkLimit != 3000 || cfg.Telegram.TextChunkMode != "word" || cfg.Telegram.StreamQueueMode != "throttle" || cfg.Telegram.StreamThrottle != 900*time.Millisecond || cfg.Telegram.ActivationMode != "mention" {
		t.Fatalf("unexpected telegram polling config: %#v", cfg.Telegram)
	}
	if len(cfg.Telegram.AllowedChatIDs) != 2 || cfg.Telegram.AllowedChatIDs[0] != 1 || cfg.Telegram.AllowedChatIDs[1] != 2 {
		t.Fatalf("unexpected telegram allowlist: %#v", cfg.Telegram.AllowedChatIDs)
	}
	if len(cfg.Telegram.AllowedSenderIDs) != 1 || cfg.Telegram.AllowedSenderIDs[0] != 7 {
		t.Fatalf("unexpected telegram sender allowlist: %#v", cfg.Telegram.AllowedSenderIDs)
	}
	if len(cfg.Telegram.CommandSenderIDs) != 1 || cfg.Telegram.CommandSenderIDs[0] != 9 {
		t.Fatalf("unexpected telegram command sender ids: %#v", cfg.Telegram.CommandSenderIDs)
	}
	if cfg.Telegram.ReplyActivation || cfg.Telegram.DMPolicy != "pairing" || cfg.Telegram.PairingCodeTTL != 2*time.Hour || cfg.Telegram.ThreadMode != "chat" {
		t.Fatalf("unexpected telegram policy config: %#v", cfg.Telegram)
	}
	if len(cfg.Telegram.GroupPolicies) != 1 || cfg.Telegram.GroupPolicies[0].ChatID != 2 || len(cfg.Telegram.GroupPolicies[0].AllowedTopicIDs) != 1 || cfg.Telegram.GroupPolicies[0].AllowedTopicIDs[0] != 11 || len(cfg.Telegram.GroupPolicies[0].CommandSenderIDs) != 1 || cfg.Telegram.GroupPolicies[0].CommandSenderIDs[0] != 11 || cfg.Telegram.GroupPolicies[0].ReplyActivation == nil || !*cfg.Telegram.GroupPolicies[0].ReplyActivation {
		t.Fatalf("unexpected telegram group policy config: %#v", cfg.Telegram.GroupPolicies)
	}
}

func TestValidateRejectsInvalidTelegramTextChunkLimit(t *testing.T) {
	cfg := Default()
	cfg.Telegram.Enabled = true
	cfg.Telegram.BotToken = "token"
	cfg.Telegram.TextChunkLimit = 0
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "channels.telegram.text_chunk_limit") {
		t.Fatalf("expected telegram text_chunk_limit validation error, got %v", err)
	}
}

func TestValidateRejectsInvalidTelegramTextChunkMode(t *testing.T) {
	cfg := Default()
	cfg.Telegram.Enabled = true
	cfg.Telegram.BotToken = "token"
	cfg.Telegram.TextChunkMode = "invalid"
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "channels.telegram.text_chunk_mode") {
		t.Fatalf("expected telegram text_chunk_mode validation error, got %v", err)
	}
}

func TestValidateRejectsInvalidTelegramStreamQueueMode(t *testing.T) {
	cfg := Default()
	cfg.Telegram.Enabled = true
	cfg.Telegram.BotToken = "token"
	cfg.Telegram.StreamQueueMode = "later"
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "channels.telegram.stream_queue_mode") {
		t.Fatalf("expected telegram stream_queue_mode validation error, got %v", err)
	}
}

func TestValidateRejectsInvalidTelegramStreamThrottle(t *testing.T) {
	cfg := Default()
	cfg.Telegram.Enabled = true
	cfg.Telegram.BotToken = "token"
	cfg.Telegram.StreamThrottle = 0
	err := validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "channels.telegram.stream_throttle") {
		t.Fatalf("expected telegram stream_throttle validation error, got %v", err)
	}
}
