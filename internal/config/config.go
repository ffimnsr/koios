package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// SessionDir returns the path where session files are stored, derived from WorkspaceRoot.
func (c *Config) SessionDir() string { return filepath.Join(c.WorkspaceRoot, "sessions") }

// CronDir returns the path where cron/scheduler data is stored, derived from WorkspaceRoot.
func (c *Config) CronDir() string { return filepath.Join(c.WorkspaceRoot, "cron") }

// AgentDir returns the path where subagent registry data is stored, derived from WorkspaceRoot.
func (c *Config) AgentDir() string { return filepath.Join(c.WorkspaceRoot, "agents") }

// ExtensionsDir returns the path where user-installed extension manifests are stored.
func (c *Config) ExtensionsDir() string { return filepath.Join(c.WorkspaceRoot, "extensions") }

// WorkflowDir returns the path where workflow definitions and run records are
// stored, derived from WorkspaceRoot.
func (c *Config) WorkflowDir() string { return filepath.Join(c.WorkspaceRoot, "workflows") }

// RunsDir returns the path where the unified run ledger is stored, derived
// from WorkspaceRoot.
func (c *Config) RunsDir() string { return filepath.Join(c.WorkspaceRoot, "runs") }

// DBDir returns the path where durable SQLite databases are stored, derived
// from WorkspaceRoot.
func (c *Config) DBDir() string { return filepath.Join(c.WorkspaceRoot, "db") }

// MemoryDBPath returns the path for the long-term memory SQLite database, derived from WorkspaceRoot.
func (c *Config) MemoryDBPath() string { return filepath.Join(c.DBDir(), "memory.db") }

// TasksDBPath returns the path for the durable task SQLite database, derived from WorkspaceRoot.
func (c *Config) TasksDBPath() string { return filepath.Join(c.DBDir(), "tasks.db") }

// BookmarksDBPath returns the path for the durable bookmark SQLite database, derived from WorkspaceRoot.
func (c *Config) BookmarksDBPath() string { return filepath.Join(c.DBDir(), "bookmarks.db") }

// CalendarDBPath returns the path for the durable calendar SQLite database, derived from WorkspaceRoot.
func (c *Config) CalendarDBPath() string { return filepath.Join(c.DBDir(), "calendar.db") }

// NotesDBPath returns the path for the durable notes SQLite database, derived from WorkspaceRoot.
func (c *Config) NotesDBPath() string { return filepath.Join(c.DBDir(), "notes.db") }

// PlansDBPath returns the path for the durable plans SQLite database, derived from WorkspaceRoot.
func (c *Config) PlansDBPath() string { return filepath.Join(c.DBDir(), "plans.db") }

// ProjectsDBPath returns the path for the durable projects SQLite database, derived from WorkspaceRoot.
func (c *Config) ProjectsDBPath() string { return filepath.Join(c.DBDir(), "projects.db") }

func (c *Config) ArtifactsDBPath() string   { return filepath.Join(c.DBDir(), "artifacts.db") }
func (c *Config) DecisionsDBPath() string   { return filepath.Join(c.DBDir(), "decisions.db") }
func (c *Config) PreferencesDBPath() string { return filepath.Join(c.DBDir(), "preferences.db") }
func (c *Config) RemindersDBPath() string   { return filepath.Join(c.DBDir(), "reminders.db") }
func (c *Config) ToolResultsDBPath() string { return filepath.Join(c.DBDir(), "tool_results.db") }

const (
	// DefaultConfigFile is the default runtime config path in the repo root.
	DefaultConfigFile = "koios.config.toml"
)

// ModelProfile describes a named LLM model endpoint that can be referenced
// as a per-session override or a fallback chain entry.
type ModelProfile struct {
	// Name is the short identifier used in fallback_models and session overrides.
	Name     string `toml:"name"`
	Provider string `toml:"provider"`
	APIKey   string `toml:"api_key"`
	BaseURL  string `toml:"base_url"`
	Model    string `toml:"model"`
}

// MCPServerConfig describes a single MCP (Model Context Protocol) server
// that Koios should connect to at startup to discover and expose external tools.
type MCPServerConfig struct {
	// Name is a short identifier used to identify the MCP server in config,
	// logs, and startup dedupe checks.
	Name string `toml:"name"`
	// Transport is one of "stdio", "http", or "sse".
	Transport string `toml:"transport"`
	// Command is the executable to spawn for stdio transport.
	Command string `toml:"command"`
	// Args are the arguments passed to the Command for stdio transport.
	Args []string `toml:"args"`
	// Env holds extra environment variables for the subprocess (stdio only).
	Env map[string]string `toml:"env"`
	// URL is the endpoint for http and sse transports.
	URL string `toml:"url"`
	// Headers are HTTP headers added to every request for http/sse transports.
	Headers map[string]string `toml:"headers"`
	// Timeout is the per-request deadline, e.g. "30s". Defaults to "30s".
	Timeout string `toml:"timeout"`
	// Enabled, when false, skips this server on startup.
	Enabled bool `toml:"enabled"`
	// ToolNamePrefix, when set, overrides the runtime tool-name prefix used for
	// tools discovered from this server. It is internal-only and currently used
	// to scope manifest-backed extension tools under a plugin namespace.
	ToolNamePrefix string `toml:"-"`
	// HideTools suppresses this server from the agent-visible tool catalog while
	// still allowing internal runtime callers such as manifest hook bindings to
	// invoke its tools by full runtime name.
	HideTools bool `toml:"-"`
}

// Config holds all runtime configuration loaded from koios.config.toml.
type Config struct {
	ListenAddr     string
	Provider       string
	APIKey         string
	Model          string
	BaseURL        string
	LLMIdleTimeout time.Duration

	// DefaultProfile, when set, names the ModelProfile to use as the primary
	// LLM. Its Provider/APIKey/Model/BaseURL override the flat [llm] fields.
	DefaultProfile string
	// LightweightModel, when set, is used for simple/short queries instead of
	// the primary Model. It uses the same Provider, APIKey, and BaseURL.
	LightweightModel string
	// FallbackModels is an ordered list of model names (or ModelProfile names)
	// to try when the primary model request fails.
	FallbackModels []string
	// ModelProfiles holds named model endpoints that can differ in provider,
	// API key, and base URL from the primary [llm] block.
	ModelProfiles []ModelProfile
	// MCPServers holds the list of MCP servers to connect to at startup.
	MCPServers []MCPServerConfig

	MaxSessionMessages           int
	RequestTimeout               time.Duration
	SessionRetention             time.Duration
	SessionMaxEntries            int
	SessionIdleResetAfter        time.Duration
	SessionIdlePruneAfter        time.Duration
	SessionIdlePruneKeep         int
	SessionDailyResetTime        string
	CompactThreshold             int
	CompactReserve               int
	MemoryEmbedEnabled           bool
	MemoryEmbedModel             string
	MemoryInject                 bool
	MemoryTopK                   int
	MemoryLCMWindow              int      // LCM: inject N most-recent chunks unconditionally
	MemoryNamespaces             []string // additional peer IDs to also inject from
	MilvusURL                    string
	MilvusCollection             string
	MilvusEnabled                bool
	SessionPruneKeepToolMessages int

	CronMaxConcurrentRuns int
	HeartbeatEvery        time.Duration
	HeartbeatEnabled      bool

	AgentMaxChildren         int
	AgentRetryAttempts       int
	AgentRetryInitialBackoff time.Duration
	AgentRetryMaxBackoff     time.Duration
	AgentRetryStatusCodes    []int

	AllowedOrigins []string
	// OwnerPeerIDs restricts owner-only slash commands (e.g. /restart) to
	// the listed peer IDs. An empty list grants the commands to all peers.
	OwnerPeerIDs []string

	ToolProfile                   string
	ToolsAllow                    []string
	ToolsDeny                     []string
	ExecEnabled                   bool
	ExecEnableDenyPatterns        bool
	ExecCustomDenyPatterns        []string
	ExecCustomAllowPatterns       []string
	ExecDefaultTimeout            time.Duration
	ExecMaxTimeout                time.Duration
	ExecApprovalMode              string
	ExecApprovalTTL               time.Duration
	ExecIsolationEnabled          bool
	ExecIsolationPaths            []ExecIsolationPath
	CodeExecutionEnabled          bool
	CodeExecutionNetworkEnabled   bool
	CodeExecutionDefaultTimeout   time.Duration
	CodeExecutionMaxTimeout       time.Duration
	CodeExecutionMaxStdoutBytes   int
	CodeExecutionMaxStderrBytes   int
	CodeExecutionMaxArtifactBytes int64
	CodeExecutionMaxOpenFiles     int
	CodeExecutionMaxProcesses     int
	CodeExecutionCPUSeconds       int
	CodeExecutionMemoryBytes      int64
	ProcessEnabled                bool
	ProcessStopTimeout            time.Duration
	ProcessLogTailBytes           int
	ProcessMaxProcessesPerPeer    int
	ExtensionDirs                 []string
	ExtensionAllow                []string
	ExtensionDeny                 []string

	WorkspaceRoot     string
	WorkspacePerAgent bool
	WorkspaceMaxBytes int

	// LogLevel controls the minimum slog level at startup and after hot-reload.
	// Valid values: debug, info, warn, error. Defaults to info.
	LogLevel string
	// LogFile, when non-empty, writes logs to this path (rotated via lumberjack).
	LogFile       string
	LogMaxSizeMB  int  // max log file size in MB before rotation (default 20)
	LogMaxBackups int  // max rotated log files to keep (default 5)
	LogMaxAgeDays int  // max age in days for rotated files (default 14)
	LogCompress   bool // compress rotated log files (default true)

	// HookTimeout is the per-hook HTTP call deadline (default 2s).
	HookTimeout time.Duration
	// HookFailClosed causes a hook error to abort the triggering operation.
	HookFailClosed bool
	// HookWebhookURL, when set, registers an HTTP webhook on all event hooks.
	HookWebhookURL string
	// HookWebhookSecret is the HMAC secret used to sign webhook payloads.
	HookWebhookSecret string
	// HookInterceptorURL, when set, registers an HTTP interceptor on before-hooks.
	HookInterceptorURL string
	// WebhookToken is the bearer token required on POST /v1/webhooks/events.
	WebhookToken string

	// PresenceTypingTTL controls how long a "typing" indicator is held (default 8s).
	PresenceTypingTTL time.Duration

	// MonitorStaleThreshold is the maximum time of gateway inactivity before the
	// health monitor logs a warning. 0 disables stale detection.
	MonitorStaleThreshold time.Duration
	// MonitorMaxRestarts caps automatic subsystem restarts; 0 = unlimited.
	MonitorMaxRestarts int
}

// ExecIsolationPath is a host→sandbox bind-mount entry for bubblewrap isolation.
type ExecIsolationPath struct {
	Source string `toml:"source"`
	Target string `toml:"target"`
	Mode   string `toml:"mode"` // "ro" or "rw"
}

type fileConfig struct {
	Server struct {
		ListenAddr     string   `toml:"listen_addr"`
		RequestTimeout string   `toml:"request_timeout"`
		AllowedOrigins []string `toml:"allowed_origins"`
		OwnerPeerIDs   []string `toml:"owner_peer_ids"`
	} `toml:"server"`
	LLM struct {
		// DefaultProfile selects a named profile as the primary LLM.
		DefaultProfile string `toml:"default_profile"`
		IdleTimeout    string `toml:"idle_timeout"`
		// Legacy flat fields — kept for backward compatibility.
		Provider         string         `toml:"provider"`
		APIKey           string         `toml:"api_key"`
		Model            string         `toml:"model"`
		BaseURL          string         `toml:"base_url"`
		LightweightModel string         `toml:"lightweight_model"`
		FallbackModels   []string       `toml:"fallback_models"`
		Profiles         []ModelProfile `toml:"profiles"`
	} `toml:"llm"`
	MCP struct {
		Servers []MCPServerConfig `toml:"servers"`
	} `toml:"mcp"`
	Extensions struct {
		Dirs  []string `toml:"dirs"`
		Allow []string `toml:"allow"`
		Deny  []string `toml:"deny"`
	} `toml:"extensions"`
	Session struct {
		MaxMessages           *int   `toml:"max_messages"`
		Retention             string `toml:"retention"`
		MaxEntries            *int   `toml:"max_entries"`
		IdleResetAfter        string `toml:"idle_reset_after"`
		IdlePruneAfter        string `toml:"idle_prune_after"`
		IdlePruneKeep         *int   `toml:"idle_prune_keep"`
		DailyResetTime        string `toml:"daily_reset_time"`
		PruneKeepToolMessages *int   `toml:"prune_keep_tool_messages"`
	} `toml:"session"`
	Compaction struct {
		Threshold *int `toml:"threshold"`
		Reserve   *int `toml:"reserve"`
	} `toml:"compaction"`
	Memory struct {
		Embed struct {
			Enabled *bool   `toml:"enabled"`
			Model   *string `toml:"model"`
		} `toml:"embed"`
		Inject     *bool    `toml:"inject"`
		TopK       *int     `toml:"top_k"`
		LCMWindow  *int     `toml:"lcm_window"`
		Namespaces []string `toml:"namespaces"`
		Milvus     struct {
			Address    string `toml:"address"`
			Collection string `toml:"collection"`
			Enabled    *bool  `toml:"enabled"`
		} `toml:"milvus"`
	} `toml:"memory"`
	Cron struct {
		MaxConcurrent *int `toml:"max_concurrent"`
	} `toml:"cron"`
	Heartbeat struct {
		Enabled *bool  `toml:"enabled"`
		Every   string `toml:"every"`
	} `toml:"heartbeat"`
	Agent struct {
		MaxChildren         *int   `toml:"max_children"`
		RetryAttempts       *int   `toml:"retry_attempts"`
		RetryInitialBackoff string `toml:"retry_initial_backoff"`
		RetryMaxBackoff     string `toml:"retry_max_backoff"`
		RetryStatusCodes    []int  `toml:"retry_status_codes"`
	} `toml:"agent"`
	Tools struct {
		Profile string   `toml:"profile"`
		Allow   []string `toml:"allow"`
		Deny    []string `toml:"deny"`
		Exec    struct {
			Enabled             *bool    `toml:"enabled"`
			EnableDenyPatterns  *bool    `toml:"enable_deny_patterns"`
			CustomDenyPatterns  []string `toml:"custom_deny_patterns"`
			CustomAllowPatterns []string `toml:"custom_allow_patterns"`
			DefaultTimeout      string   `toml:"default_timeout"`
			MaxTimeout          string   `toml:"max_timeout"`
			ApprovalMode        string   `toml:"approval_mode"`
			ApprovalTTL         string   `toml:"approval_ttl"`
			Isolation           struct {
				Enabled     bool                `toml:"enabled"`
				ExposePaths []ExecIsolationPath `toml:"expose_paths"`
			} `toml:"isolation"`
		} `toml:"exec"`
		CodeExecution struct {
			Enabled          *bool  `toml:"enabled"`
			NetworkEnabled   *bool  `toml:"network_enabled"`
			DefaultTimeout   string `toml:"default_timeout"`
			MaxTimeout       string `toml:"max_timeout"`
			MaxStdoutBytes   *int   `toml:"max_stdout_bytes"`
			MaxStderrBytes   *int   `toml:"max_stderr_bytes"`
			MaxArtifactBytes *int64 `toml:"max_artifact_bytes"`
			MaxOpenFiles     *int   `toml:"max_open_files"`
			MaxProcesses     *int   `toml:"max_processes"`
			CPUSeconds       *int   `toml:"cpu_seconds"`
			MemoryBytes      *int64 `toml:"memory_bytes"`
		} `toml:"code_execution"`
		Process struct {
			Enabled             *bool  `toml:"enabled"`
			StopTimeout         string `toml:"stop_timeout"`
			LogTailBytes        *int   `toml:"log_tail_bytes"`
			MaxProcessesPerPeer *int   `toml:"max_processes_per_peer"`
		} `toml:"process"`
	} `toml:"tools"`
	Workspace struct {
		Root     string `toml:"root"`
		PerAgent *bool  `toml:"per_agent"`
		MaxBytes *int   `toml:"max_file_bytes"`
	} `toml:"workspace"`
	Log struct {
		Level      string `toml:"level"`
		File       string `toml:"file"`
		MaxSizeMB  *int   `toml:"max_size_mb"`
		MaxBackups *int   `toml:"max_backups"`
		MaxAgeDays *int   `toml:"max_age_days"`
		Compress   *bool  `toml:"compress"`
	} `toml:"log"`
	Monitor struct {
		StaleThreshold string `toml:"stale_threshold"`
		MaxRestarts    *int   `toml:"max_restarts"`
	} `toml:"monitor"`
	Hooks struct {
		WebhookURL     string `toml:"webhook_url"`
		WebhookSecret  string `toml:"webhook_secret"`
		InterceptorURL string `toml:"interceptor_url"`
		WebhookToken   string `toml:"webhook_token"`
		Timeout        string `toml:"timeout"`
		FailClosed     *bool  `toml:"fail_closed"`
	} `toml:"hooks"`
	Presence struct {
		TypingTTL string `toml:"typing_ttl"`
	} `toml:"presence"`
}

// Default returns sane defaults for a local-first Koios setup.
func Default() *Config {
	return &Config{
		ListenAddr:                    ":8080",
		Provider:                      "openai",
		Model:                         "gpt-4o",
		LLMIdleTimeout:                30 * time.Second,
		MaxSessionMessages:            100,
		RequestTimeout:                2 * time.Minute,
		SessionRetention:              0,
		SessionMaxEntries:             0,
		SessionIdleResetAfter:         0,
		SessionIdlePruneAfter:         0,
		SessionIdlePruneKeep:          0,
		SessionDailyResetTime:         "",
		CompactThreshold:              0,
		CompactReserve:                20,
		MemoryEmbedEnabled:            false,
		MemoryEmbedModel:              "text-embedding-3-small",
		MemoryInject:                  false,
		MemoryTopK:                    3,
		MemoryLCMWindow:               0,
		MilvusURL:                     "localhost:19530",
		MilvusCollection:              "koios_memory",
		MilvusEnabled:                 false,
		SessionPruneKeepToolMessages:  8,
		CronMaxConcurrentRuns:         1,
		HeartbeatEvery:                30 * time.Minute,
		HeartbeatEnabled:              true,
		AgentMaxChildren:              4,
		AgentRetryAttempts:            3,
		AgentRetryInitialBackoff:      500 * time.Millisecond,
		AgentRetryMaxBackoff:          5 * time.Second,
		AgentRetryStatusCodes:         []int{429, 500, 502, 503, 504},
		ToolProfile:                   "full",
		ExecEnabled:                   true,
		ExecEnableDenyPatterns:        true,
		ExecDefaultTimeout:            30 * time.Second,
		ExecMaxTimeout:                5 * time.Minute,
		ExecApprovalMode:              "dangerous",
		ExecApprovalTTL:               15 * time.Minute,
		CodeExecutionEnabled:          false,
		CodeExecutionNetworkEnabled:   false,
		CodeExecutionDefaultTimeout:   10 * time.Second,
		CodeExecutionMaxTimeout:       30 * time.Second,
		CodeExecutionMaxStdoutBytes:   64 * 1024,
		CodeExecutionMaxStderrBytes:   64 * 1024,
		CodeExecutionMaxArtifactBytes: 1 << 20,
		CodeExecutionMaxOpenFiles:     64,
		CodeExecutionMaxProcesses:     32,
		CodeExecutionCPUSeconds:       10,
		CodeExecutionMemoryBytes:      256 << 20,
		ProcessEnabled:                false,
		ProcessStopTimeout:            5 * time.Second,
		ProcessLogTailBytes:           64 * 1024,
		ProcessMaxProcessesPerPeer:    8,
		ExtensionDirs:                 nil,
		ExtensionAllow:                nil,
		ExtensionDeny:                 nil,
		WorkspaceRoot:                 "./workspace",
		WorkspacePerAgent:             true,
		WorkspaceMaxBytes:             1 << 20,
		LogLevel:                      "info",
		LogMaxSizeMB:                  20,
		LogMaxBackups:                 5,
		LogMaxAgeDays:                 14,
		LogCompress:                   true,
		HookTimeout:                   2 * time.Second,
		HookFailClosed:                false,
		PresenceTypingTTL:             8 * time.Second,
		MonitorStaleThreshold:         0,
		MonitorMaxRestarts:            5,
	}
}

// Load reads and validates koios.config.toml from the current working directory.
func Load() (*Config, error) {
	return LoadFromPath(DefaultConfigFile)
}

// LoadOptionalFromPath parses config when present, returning defaults otherwise.
func LoadOptionalFromPath(path string) (*Config, bool, error) {
	cfg := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, false, nil
		}
		return nil, false, fmt.Errorf("read config file %s: %w", path, err)
	}
	fileCfg := fileConfig{}
	if err := toml.Unmarshal(data, &fileCfg); err != nil {
		return nil, true, fmt.Errorf("parse config file %s: %w", path, err)
	}
	applyFileConfig(cfg, &fileCfg)
	resolveRelativePaths(cfg, filepath.Dir(path))
	return cfg, true, nil
}

// LoadFromPath parses and validates a TOML config file.
func LoadFromPath(path string) (*Config, error) {
	cfg, exists, err := LoadOptionalFromPath(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, fmt.Errorf("config file %s not found; run `koios init`", path)
	}
	if err := validate(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

// DefaultTOML returns a generated starter config without private credentials.
func DefaultTOML() string {
	return defaultTOMLTemplate
}

// EncodeTOML renders a config in the current canonical schema.
// When includeAPIKey is false and cfg.APIKey is empty, the key is omitted as a comment.
// When ModelProfiles are set, emits the profiles-based [llm] format; otherwise
// falls back to the legacy flat provider/model fields for backward compatibility.
func EncodeTOML(cfg *Config, includeAPIKey bool) string {
	allowedOrigins := "[]"
	if len(cfg.AllowedOrigins) > 0 {
		quoted := make([]string, 0, len(cfg.AllowedOrigins))
		for _, origin := range cfg.AllowedOrigins {
			quoted = append(quoted, strconv.Quote(origin))
		}
		allowedOrigins = "[" + strings.Join(quoted, ", ") + "]"
	}

	// Build the [llm] section. Prefer profiles-based format when profiles exist
	// or a default_profile is set; fall back to legacy flat fields otherwise.
	llmSection := encodeLLMSection(cfg, includeAPIKey)

	// Build [[mcp.servers]] sections if any are configured.
	mcpSection := encodeMCPSection(cfg)

	return fmt.Sprintf(encodedTOMLTemplate,
		strconv.Quote(cfg.ListenAddr),
		strconv.Quote(cfg.RequestTimeout.String()),
		allowedOrigins,
		llmSection,
		mcpSection,
		cfg.MaxSessionMessages,
		strconv.Quote(cfg.SessionRetention.String()),
		cfg.SessionMaxEntries,
		strconv.Quote(cfg.SessionIdleResetAfter.String()),
		strconv.Quote(cfg.SessionIdlePruneAfter.String()),
		cfg.SessionIdlePruneKeep,
		strconv.Quote(cfg.SessionDailyResetTime),
		cfg.SessionPruneKeepToolMessages,
		cfg.CompactThreshold,
		cfg.CompactReserve,
		cfg.MemoryInject,
		cfg.MemoryTopK,
		cfg.MemoryEmbedEnabled,
		strconv.Quote(cfg.MemoryEmbedModel),
		strconv.Quote(cfg.MilvusURL),
		strconv.Quote(cfg.MilvusCollection),
		cfg.MilvusEnabled,
		cfg.CronMaxConcurrentRuns,
		cfg.HeartbeatEnabled,
		strconv.Quote(cfg.HeartbeatEvery.String()),
		cfg.AgentMaxChildren,
		cfg.AgentRetryAttempts,
		strconv.Quote(cfg.AgentRetryInitialBackoff.String()),
		strconv.Quote(cfg.AgentRetryMaxBackoff.String()),
		quoteIntSlice(cfg.AgentRetryStatusCodes),
		strconv.Quote(cfg.ToolProfile),
		inlineQuotedStringSlice(cfg.ToolsAllow),
		inlineQuotedStringSlice(cfg.ToolsDeny),
		cfg.CodeExecutionEnabled,
		cfg.CodeExecutionNetworkEnabled,
		strconv.Quote(cfg.CodeExecutionDefaultTimeout.String()),
		strconv.Quote(cfg.CodeExecutionMaxTimeout.String()),
		cfg.CodeExecutionMaxStdoutBytes,
		cfg.CodeExecutionMaxStderrBytes,
		cfg.CodeExecutionMaxArtifactBytes,
		cfg.CodeExecutionMaxOpenFiles,
		cfg.CodeExecutionMaxProcesses,
		cfg.CodeExecutionCPUSeconds,
		cfg.CodeExecutionMemoryBytes,
		cfg.ProcessEnabled,
		strconv.Quote(cfg.ProcessStopTimeout.String()),
		cfg.ProcessLogTailBytes,
		cfg.ProcessMaxProcessesPerPeer,
		inlineQuotedStringSlice(cfg.ExtensionDirs),
		inlineQuotedStringSlice(cfg.ExtensionAllow),
		inlineQuotedStringSlice(cfg.ExtensionDeny),
		cfg.ExecEnabled,
		cfg.ExecEnableDenyPatterns,
		inlineQuotedStringSlice(cfg.ExecCustomDenyPatterns),
		inlineQuotedStringSlice(cfg.ExecCustomAllowPatterns),
		strconv.Quote(cfg.ExecDefaultTimeout.String()),
		strconv.Quote(cfg.ExecMaxTimeout.String()),
		strconv.Quote(cfg.ExecApprovalMode),
		strconv.Quote(cfg.ExecApprovalTTL.String()),
		cfg.ExecIsolationEnabled,
		inlineExecIsolationPaths(cfg.ExecIsolationPaths),
		strconv.Quote(cfg.WorkspaceRoot),
		cfg.WorkspacePerAgent,
		cfg.WorkspaceMaxBytes,
		strconv.Quote(cfg.LogLevel),
		func() string {
			if cfg.MonitorStaleThreshold <= 0 {
				return strconv.Quote("0s")
			}
			return strconv.Quote(cfg.MonitorStaleThreshold.String())
		}(),
		cfg.MonitorMaxRestarts,
	)
}

// encodeLLMSection builds the [llm] (and [[llm.profiles]]) portion of the
// config. Profiles-based format is used when profiles exist or DefaultProfile
// is set; legacy flat format is used otherwise for backward compatibility.
func encodeLLMSection(cfg *Config, includeAPIKey bool) string {
	var b strings.Builder
	if len(cfg.ModelProfiles) > 0 || cfg.DefaultProfile != "" {
		b.WriteString("[llm]\n")
		b.WriteString("idle_timeout = " + strconv.Quote(cfg.LLMIdleTimeout.String()) + "\n")
		if cfg.DefaultProfile != "" {
			b.WriteString("default_profile = " + strconv.Quote(cfg.DefaultProfile) + "\n")
		}
		if cfg.LightweightModel != "" {
			b.WriteString("lightweight_model = " + strconv.Quote(cfg.LightweightModel) + "\n")
		}
		if len(cfg.FallbackModels) > 0 {
			b.WriteString("fallback_models = " + quoteStringSlice(cfg.FallbackModels) + "\n")
		}
		b.WriteString("\n")
		for _, p := range cfg.ModelProfiles {
			b.WriteString("[[llm.profiles]]\n")
			b.WriteString("name = " + strconv.Quote(p.Name) + "\n")
			b.WriteString("provider = " + strconv.Quote(p.Provider) + "\n")
			b.WriteString("model = " + strconv.Quote(p.Model) + "\n")
			if p.APIKey != "" || includeAPIKey {
				b.WriteString("api_key = " + strconv.Quote(p.APIKey) + "\n")
			}
			if p.BaseURL != "" {
				b.WriteString("base_url = " + strconv.Quote(p.BaseURL) + "\n")
			}
			b.WriteString("\n")
		}
	} else {
		// Legacy flat format for backward compatibility.
		apiKeyLine := "# api_key = \"\""
		if includeAPIKey || strings.TrimSpace(cfg.APIKey) != "" {
			apiKeyLine = "api_key = " + strconv.Quote(cfg.APIKey)
		}
		baseURLLine := "# base_url = \"\""
		if strings.TrimSpace(cfg.BaseURL) != "" {
			baseURLLine = "base_url = " + strconv.Quote(cfg.BaseURL)
		}
		b.WriteString(fmt.Sprintf("[llm]\nidle_timeout = %s\nprovider = %s\nmodel = %s\n%s\n%s\n\n",
			strconv.Quote(cfg.LLMIdleTimeout.String()),
			strconv.Quote(cfg.Provider),
			strconv.Quote(cfg.Model),
			apiKeyLine,
			baseURLLine,
		))
	}
	return b.String()
}

// encodeMCPSection builds the [[mcp.servers]] portion of the config.
// Returns an empty string when no MCP servers are configured.
func encodeMCPSection(cfg *Config) string {
	if len(cfg.MCPServers) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range cfg.MCPServers {
		b.WriteString("[[mcp.servers]]\n")
		b.WriteString("name = " + strconv.Quote(s.Name) + "\n")
		b.WriteString("transport = " + strconv.Quote(s.Transport) + "\n")
		if s.Command != "" {
			b.WriteString("command = " + strconv.Quote(s.Command) + "\n")
		}
		if len(s.Args) > 0 {
			b.WriteString("args = " + quoteStringSlice(s.Args) + "\n")
		}
		if s.URL != "" {
			b.WriteString("url = " + strconv.Quote(s.URL) + "\n")
		}
		if s.Timeout != "" {
			b.WriteString("timeout = " + strconv.Quote(s.Timeout) + "\n")
		}
		b.WriteString(fmt.Sprintf("enabled = %t\n", s.Enabled))
		b.WriteString("\n")
	}
	return b.String()
}

func applyFileConfig(dst *Config, src *fileConfig) {
	if src.Server.ListenAddr != "" {
		dst.ListenAddr = src.Server.ListenAddr
	}
	if src.Server.RequestTimeout != "" {
		if d, err := time.ParseDuration(src.Server.RequestTimeout); err == nil {
			dst.RequestTimeout = d
		}
	}
	if src.Server.AllowedOrigins != nil {
		dst.AllowedOrigins = src.Server.AllowedOrigins
	}
	if src.Server.OwnerPeerIDs != nil {
		dst.OwnerPeerIDs = src.Server.OwnerPeerIDs
	}

	// Apply legacy flat LLM fields first; default_profile overrides them below.
	if src.LLM.Provider != "" {
		dst.Provider = src.LLM.Provider
	}
	if src.LLM.IdleTimeout != "" {
		if d, err := time.ParseDuration(src.LLM.IdleTimeout); err == nil && d >= 0 {
			dst.LLMIdleTimeout = d
		}
	}
	if src.LLM.APIKey != "" {
		dst.APIKey = src.LLM.APIKey
	}
	if src.LLM.Model != "" {
		dst.Model = src.LLM.Model
	}
	if src.LLM.BaseURL != "" {
		dst.BaseURL = src.LLM.BaseURL
	}
	if src.LLM.LightweightModel != "" {
		dst.LightweightModel = src.LLM.LightweightModel
	}
	if src.LLM.FallbackModels != nil {
		dst.FallbackModels = append([]string(nil), src.LLM.FallbackModels...)
	}
	if src.LLM.Profiles != nil {
		dst.ModelProfiles = append([]ModelProfile(nil), src.LLM.Profiles...)
	}
	// default_profile, when set, resolves the named profile and overrides the
	// primary provider/model fields, taking precedence over legacy flat fields.
	if src.LLM.DefaultProfile != "" {
		dst.DefaultProfile = src.LLM.DefaultProfile
		for _, p := range src.LLM.Profiles {
			if p.Name == src.LLM.DefaultProfile {
				if p.Provider != "" {
					dst.Provider = p.Provider
				}
				if p.APIKey != "" {
					dst.APIKey = p.APIKey
				}
				if p.Model != "" {
					dst.Model = p.Model
				}
				if p.BaseURL != "" {
					dst.BaseURL = p.BaseURL
				}
				break
			}
		}
	}
	if src.MCP.Servers != nil {
		dst.MCPServers = append([]MCPServerConfig(nil), src.MCP.Servers...)
	}
	if src.Extensions.Dirs != nil {
		dst.ExtensionDirs = append([]string(nil), src.Extensions.Dirs...)
	}
	if src.Extensions.Allow != nil {
		dst.ExtensionAllow = append([]string(nil), src.Extensions.Allow...)
	}
	if src.Extensions.Deny != nil {
		dst.ExtensionDeny = append([]string(nil), src.Extensions.Deny...)
	}

	if src.Session.MaxMessages != nil && *src.Session.MaxMessages > 0 {
		dst.MaxSessionMessages = *src.Session.MaxMessages
	}
	if src.Session.Retention != "" {
		if d, err := time.ParseDuration(src.Session.Retention); err == nil {
			dst.SessionRetention = d
		}
	}
	if src.Session.MaxEntries != nil && *src.Session.MaxEntries >= 0 {
		dst.SessionMaxEntries = *src.Session.MaxEntries
	}
	if src.Session.IdleResetAfter != "" {
		if d, err := time.ParseDuration(src.Session.IdleResetAfter); err == nil {
			dst.SessionIdleResetAfter = d
		}
	}
	if src.Session.IdlePruneAfter != "" {
		if d, err := time.ParseDuration(src.Session.IdlePruneAfter); err == nil {
			dst.SessionIdlePruneAfter = d
		}
	}
	if src.Session.IdlePruneKeep != nil && *src.Session.IdlePruneKeep >= 0 {
		dst.SessionIdlePruneKeep = *src.Session.IdlePruneKeep
	}
	dst.SessionDailyResetTime = src.Session.DailyResetTime
	if src.Session.PruneKeepToolMessages != nil && *src.Session.PruneKeepToolMessages >= 0 {
		dst.SessionPruneKeepToolMessages = *src.Session.PruneKeepToolMessages
	}

	if src.Compaction.Threshold != nil && *src.Compaction.Threshold >= 0 {
		dst.CompactThreshold = *src.Compaction.Threshold
	}
	if src.Compaction.Reserve != nil && *src.Compaction.Reserve > 0 {
		dst.CompactReserve = *src.Compaction.Reserve
	}

	if src.Memory.Embed.Enabled != nil {
		dst.MemoryEmbedEnabled = *src.Memory.Embed.Enabled
	}
	if src.Memory.Embed.Model != nil {
		dst.MemoryEmbedModel = *src.Memory.Embed.Model
	}
	if src.Memory.Inject != nil {
		dst.MemoryInject = *src.Memory.Inject
	}
	if src.Memory.TopK != nil && *src.Memory.TopK > 0 {
		dst.MemoryTopK = *src.Memory.TopK
	}
	if src.Memory.LCMWindow != nil && *src.Memory.LCMWindow >= 0 {
		dst.MemoryLCMWindow = *src.Memory.LCMWindow
	}
	if src.Memory.Namespaces != nil {
		dst.MemoryNamespaces = src.Memory.Namespaces
	}
	if src.Memory.Milvus.Address != "" {
		dst.MilvusURL = src.Memory.Milvus.Address
	}
	if src.Memory.Milvus.Collection != "" {
		dst.MilvusCollection = src.Memory.Milvus.Collection
	}
	if src.Memory.Milvus.Enabled != nil {
		dst.MilvusEnabled = *src.Memory.Milvus.Enabled
	}

	if src.Cron.MaxConcurrent != nil && *src.Cron.MaxConcurrent > 0 {
		dst.CronMaxConcurrentRuns = *src.Cron.MaxConcurrent
	}

	if src.Heartbeat.Enabled != nil {
		dst.HeartbeatEnabled = *src.Heartbeat.Enabled
	}
	if src.Heartbeat.Every != "" {
		if d, err := time.ParseDuration(src.Heartbeat.Every); err == nil {
			dst.HeartbeatEvery = d
		}
	}

	if src.Agent.MaxChildren != nil && *src.Agent.MaxChildren > 0 {
		dst.AgentMaxChildren = *src.Agent.MaxChildren
	}
	if src.Agent.RetryAttempts != nil && *src.Agent.RetryAttempts > 0 {
		dst.AgentRetryAttempts = *src.Agent.RetryAttempts
	}
	if src.Agent.RetryInitialBackoff != "" {
		if d, err := time.ParseDuration(src.Agent.RetryInitialBackoff); err == nil {
			dst.AgentRetryInitialBackoff = d
		}
	}
	if src.Agent.RetryMaxBackoff != "" {
		if d, err := time.ParseDuration(src.Agent.RetryMaxBackoff); err == nil {
			dst.AgentRetryMaxBackoff = d
		}
	}
	if src.Agent.RetryStatusCodes != nil {
		dst.AgentRetryStatusCodes = append([]int(nil), src.Agent.RetryStatusCodes...)
	}
	if src.Tools.Profile != "" {
		dst.ToolProfile = src.Tools.Profile
	}
	if src.Tools.Allow != nil {
		dst.ToolsAllow = src.Tools.Allow
	}
	if src.Tools.Deny != nil {
		dst.ToolsDeny = src.Tools.Deny
	}
	if src.Tools.Exec.Enabled != nil {
		dst.ExecEnabled = *src.Tools.Exec.Enabled
	}
	if src.Tools.Exec.EnableDenyPatterns != nil {
		dst.ExecEnableDenyPatterns = *src.Tools.Exec.EnableDenyPatterns
	}
	if src.Tools.Exec.CustomDenyPatterns != nil {
		dst.ExecCustomDenyPatterns = src.Tools.Exec.CustomDenyPatterns
	}
	if src.Tools.Exec.CustomAllowPatterns != nil {
		dst.ExecCustomAllowPatterns = src.Tools.Exec.CustomAllowPatterns
	}
	if src.Tools.Exec.DefaultTimeout != "" {
		if d, err := time.ParseDuration(src.Tools.Exec.DefaultTimeout); err == nil {
			dst.ExecDefaultTimeout = d
		}
	}
	if src.Tools.Exec.MaxTimeout != "" {
		if d, err := time.ParseDuration(src.Tools.Exec.MaxTimeout); err == nil {
			dst.ExecMaxTimeout = d
		}
	}
	if src.Tools.Exec.ApprovalMode != "" {
		dst.ExecApprovalMode = src.Tools.Exec.ApprovalMode
	}
	if src.Tools.Exec.ApprovalTTL != "" {
		if d, err := time.ParseDuration(src.Tools.Exec.ApprovalTTL); err == nil {
			dst.ExecApprovalTTL = d
		}
	}
	if src.Tools.Exec.Isolation.Enabled {
		dst.ExecIsolationEnabled = true
	}
	if len(src.Tools.Exec.Isolation.ExposePaths) > 0 {
		dst.ExecIsolationPaths = src.Tools.Exec.Isolation.ExposePaths
	}
	if src.Tools.CodeExecution.Enabled != nil {
		dst.CodeExecutionEnabled = *src.Tools.CodeExecution.Enabled
	}
	if src.Tools.CodeExecution.NetworkEnabled != nil {
		dst.CodeExecutionNetworkEnabled = *src.Tools.CodeExecution.NetworkEnabled
	}
	if src.Tools.CodeExecution.DefaultTimeout != "" {
		if d, err := time.ParseDuration(src.Tools.CodeExecution.DefaultTimeout); err == nil {
			dst.CodeExecutionDefaultTimeout = d
		}
	}
	if src.Tools.CodeExecution.MaxTimeout != "" {
		if d, err := time.ParseDuration(src.Tools.CodeExecution.MaxTimeout); err == nil {
			dst.CodeExecutionMaxTimeout = d
		}
	}
	if src.Tools.CodeExecution.MaxStdoutBytes != nil && *src.Tools.CodeExecution.MaxStdoutBytes > 0 {
		dst.CodeExecutionMaxStdoutBytes = *src.Tools.CodeExecution.MaxStdoutBytes
	}
	if src.Tools.CodeExecution.MaxStderrBytes != nil && *src.Tools.CodeExecution.MaxStderrBytes > 0 {
		dst.CodeExecutionMaxStderrBytes = *src.Tools.CodeExecution.MaxStderrBytes
	}
	if src.Tools.CodeExecution.MaxArtifactBytes != nil && *src.Tools.CodeExecution.MaxArtifactBytes > 0 {
		dst.CodeExecutionMaxArtifactBytes = *src.Tools.CodeExecution.MaxArtifactBytes
	}
	if src.Tools.CodeExecution.MaxOpenFiles != nil && *src.Tools.CodeExecution.MaxOpenFiles > 0 {
		dst.CodeExecutionMaxOpenFiles = *src.Tools.CodeExecution.MaxOpenFiles
	}
	if src.Tools.CodeExecution.MaxProcesses != nil && *src.Tools.CodeExecution.MaxProcesses > 0 {
		dst.CodeExecutionMaxProcesses = *src.Tools.CodeExecution.MaxProcesses
	}
	if src.Tools.CodeExecution.CPUSeconds != nil && *src.Tools.CodeExecution.CPUSeconds > 0 {
		dst.CodeExecutionCPUSeconds = *src.Tools.CodeExecution.CPUSeconds
	}
	if src.Tools.CodeExecution.MemoryBytes != nil && *src.Tools.CodeExecution.MemoryBytes > 0 {
		dst.CodeExecutionMemoryBytes = *src.Tools.CodeExecution.MemoryBytes
	}
	if src.Tools.Process.Enabled != nil {
		dst.ProcessEnabled = *src.Tools.Process.Enabled
	}
	if src.Tools.Process.StopTimeout != "" {
		if d, err := time.ParseDuration(src.Tools.Process.StopTimeout); err == nil {
			dst.ProcessStopTimeout = d
		}
	}
	if src.Tools.Process.LogTailBytes != nil && *src.Tools.Process.LogTailBytes > 0 {
		dst.ProcessLogTailBytes = *src.Tools.Process.LogTailBytes
	}
	if src.Tools.Process.MaxProcessesPerPeer != nil && *src.Tools.Process.MaxProcessesPerPeer > 0 {
		dst.ProcessMaxProcessesPerPeer = *src.Tools.Process.MaxProcessesPerPeer
	}
	if src.Workspace.Root != "" {
		dst.WorkspaceRoot = src.Workspace.Root
	}
	if src.Workspace.PerAgent != nil {
		dst.WorkspacePerAgent = *src.Workspace.PerAgent
	}
	if src.Workspace.MaxBytes != nil && *src.Workspace.MaxBytes > 0 {
		dst.WorkspaceMaxBytes = *src.Workspace.MaxBytes
	}
	if src.Log.Level != "" {
		dst.LogLevel = src.Log.Level
	}
	if src.Log.File != "" {
		dst.LogFile = src.Log.File
	}
	if src.Log.MaxSizeMB != nil && *src.Log.MaxSizeMB > 0 {
		dst.LogMaxSizeMB = *src.Log.MaxSizeMB
	}
	if src.Log.MaxBackups != nil && *src.Log.MaxBackups >= 0 {
		dst.LogMaxBackups = *src.Log.MaxBackups
	}
	if src.Log.MaxAgeDays != nil && *src.Log.MaxAgeDays >= 0 {
		dst.LogMaxAgeDays = *src.Log.MaxAgeDays
	}
	if src.Log.Compress != nil {
		dst.LogCompress = *src.Log.Compress
	}
	if src.Monitor.StaleThreshold != "" {
		if d, err := time.ParseDuration(src.Monitor.StaleThreshold); err == nil {
			dst.MonitorStaleThreshold = d
		}
	}
	if src.Monitor.MaxRestarts != nil && *src.Monitor.MaxRestarts >= 0 {
		dst.MonitorMaxRestarts = *src.Monitor.MaxRestarts
	}
	if src.Hooks.WebhookURL != "" {
		dst.HookWebhookURL = src.Hooks.WebhookURL
	}
	if src.Hooks.WebhookSecret != "" {
		dst.HookWebhookSecret = src.Hooks.WebhookSecret
	}
	if src.Hooks.InterceptorURL != "" {
		dst.HookInterceptorURL = src.Hooks.InterceptorURL
	}
	if src.Hooks.WebhookToken != "" {
		dst.WebhookToken = src.Hooks.WebhookToken
	}
	if src.Hooks.Timeout != "" {
		if d, err := time.ParseDuration(src.Hooks.Timeout); err == nil && d > 0 {
			dst.HookTimeout = d
		}
	}
	if src.Hooks.FailClosed != nil {
		dst.HookFailClosed = *src.Hooks.FailClosed
	}
	if src.Presence.TypingTTL != "" {
		if d, err := time.ParseDuration(src.Presence.TypingTTL); err == nil && d > 0 {
			dst.PresenceTypingTTL = d
		}
	}
}

func resolveRelativePaths(cfg *Config, root string) {
	cfg.WorkspaceRoot = makeAbs(root, cfg.WorkspaceRoot)
	for i, dir := range cfg.ExtensionDirs {
		cfg.ExtensionDirs[i] = makeAbs(root, dir)
	}
}

// ExtensionSearchPaths returns the manifest discovery paths in precedence order.
func (c *Config) ExtensionSearchPaths() []string {
	paths := []string{c.ExtensionsDir()}
	for _, dir := range c.ExtensionDirs {
		if strings.TrimSpace(dir) != "" {
			paths = append(paths, dir)
		}
	}
	return paths
}

func makeAbs(root, path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func quoteStringSlice(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			quoted = append(quoted, strconv.Quote(trimmed))
		}
	}
	if len(quoted) == 0 {
		return "[]"
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func inlineQuotedStringSlice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			quoted = append(quoted, strconv.Quote(trimmed))
		}
	}
	return strings.Join(quoted, ", ")
}

func quoteIntSlice(values []int) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ", ")
}

func inlineExecIsolationPaths(values []ExecIsolationPath) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf(`{ source = %q, target = %q, mode = %q }`, value.Source, value.Target, value.Mode))
	}
	return strings.Join(parts, ", ")
}

func ParseDailyResetMinutes(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return -1, nil
	}
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return 0, fmt.Errorf("session.daily_reset_time must be in HH:MM format")
	}
	hour, err := strconv.Atoi(parts[0])
	if err != nil || hour < 0 || hour > 23 {
		return 0, fmt.Errorf("session.daily_reset_time hour must be between 00 and 23")
	}
	minute, err := strconv.Atoi(parts[1])
	if err != nil || minute < 0 || minute > 59 {
		return 0, fmt.Errorf("session.daily_reset_time minute must be between 00 and 59")
	}
	return hour*60 + minute, nil
}

func validate(cfg *Config) error {
	if cfg.Model == "" {
		return fmt.Errorf("llm.model is required")
	}
	if cfg.LLMIdleTimeout < 0 {
		return fmt.Errorf("llm.idle_timeout must be >= 0")
	}
	switch cfg.Provider {
	case "openai", "anthropic", "openrouter", "nvidia":
	default:
		return fmt.Errorf("unsupported llm.provider %q: must be openai, anthropic, openrouter, or nvidia", cfg.Provider)
	}
	if cfg.MaxSessionMessages < 1 {
		return fmt.Errorf("session.max_messages must be >= 1")
	}
	if cfg.SessionRetention < 0 {
		return fmt.Errorf("session.retention must be >= 0")
	}
	if cfg.SessionMaxEntries < 0 {
		return fmt.Errorf("session.max_entries must be >= 0")
	}
	if cfg.SessionIdleResetAfter < 0 {
		return fmt.Errorf("session.idle_reset_after must be >= 0")
	}
	if cfg.SessionIdlePruneAfter < 0 {
		return fmt.Errorf("session.idle_prune_after must be >= 0")
	}
	if cfg.SessionIdlePruneKeep < 0 {
		return fmt.Errorf("session.idle_prune_keep must be >= 0")
	}
	if _, err := ParseDailyResetMinutes(cfg.SessionDailyResetTime); err != nil {
		return err
	}
	if cfg.CronMaxConcurrentRuns < 1 {
		return fmt.Errorf("cron.max_concurrent must be >= 1")
	}
	if cfg.AgentMaxChildren < 1 {
		return fmt.Errorf("agent.max_children must be >= 1")
	}
	if cfg.AgentRetryAttempts < 1 {
		return fmt.Errorf("agent.retry_attempts must be >= 1")
	}
	for _, code := range cfg.AgentRetryStatusCodes {
		if code < 100 || code > 599 {
			return fmt.Errorf("agent.retry_status_codes entries must be between 100 and 599")
		}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.ToolProfile)) {
	case "", "full", "coding", "messaging", "minimal":
	default:
		return fmt.Errorf("tools.profile must be one of full, coding, messaging, or minimal")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.ExecApprovalMode)) {
	case "", "off", "never", "dangerous", "always":
	default:
		return fmt.Errorf("tools.exec.approval_mode must be one of off, never, dangerous, or always")
	}
	if cfg.ExecDefaultTimeout <= 0 {
		return fmt.Errorf("tools.exec.default_timeout must be > 0")
	}
	if cfg.ExecMaxTimeout < cfg.ExecDefaultTimeout {
		return fmt.Errorf("tools.exec.max_timeout must be >= tools.exec.default_timeout")
	}
	if cfg.ExecApprovalTTL <= 0 {
		return fmt.Errorf("tools.exec.approval_ttl must be > 0")
	}
	if cfg.CodeExecutionDefaultTimeout <= 0 {
		return fmt.Errorf("tools.code_execution.default_timeout must be > 0")
	}
	if cfg.CodeExecutionMaxTimeout < cfg.CodeExecutionDefaultTimeout {
		return fmt.Errorf("tools.code_execution.max_timeout must be >= tools.code_execution.default_timeout")
	}
	if cfg.CodeExecutionMaxStdoutBytes < 1 {
		return fmt.Errorf("tools.code_execution.max_stdout_bytes must be >= 1")
	}
	if cfg.CodeExecutionMaxStderrBytes < 1 {
		return fmt.Errorf("tools.code_execution.max_stderr_bytes must be >= 1")
	}
	if cfg.CodeExecutionMaxArtifactBytes < 1 {
		return fmt.Errorf("tools.code_execution.max_artifact_bytes must be >= 1")
	}
	if cfg.CodeExecutionMaxOpenFiles < 1 {
		return fmt.Errorf("tools.code_execution.max_open_files must be >= 1")
	}
	if cfg.CodeExecutionMaxProcesses < 1 {
		return fmt.Errorf("tools.code_execution.max_processes must be >= 1")
	}
	if cfg.CodeExecutionCPUSeconds < 1 {
		return fmt.Errorf("tools.code_execution.cpu_seconds must be >= 1")
	}
	if cfg.CodeExecutionMemoryBytes < 1 {
		return fmt.Errorf("tools.code_execution.memory_bytes must be >= 1")
	}
	if cfg.ProcessStopTimeout <= 0 {
		return fmt.Errorf("tools.process.stop_timeout must be > 0")
	}
	if cfg.ProcessLogTailBytes < 1 {
		return fmt.Errorf("tools.process.log_tail_bytes must be >= 1")
	}
	if cfg.ProcessMaxProcessesPerPeer < 1 {
		return fmt.Errorf("tools.process.max_processes_per_peer must be >= 1")
	}
	for i, dir := range cfg.ExtensionDirs {
		if strings.TrimSpace(dir) == "" {
			return fmt.Errorf("extensions.dirs[%d] must not be empty", i)
		}
	}
	for i, value := range cfg.ExtensionAllow {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("extensions.allow[%d] must not be empty", i)
		}
	}
	for i, value := range cfg.ExtensionDeny {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("extensions.deny[%d] must not be empty", i)
		}
	}
	if cfg.WorkspaceRoot == "" {
		return fmt.Errorf("workspace.root must not be empty")
	}
	if cfg.WorkspaceMaxBytes < 1 {
		return fmt.Errorf("workspace.max_file_bytes must be >= 1")
	}
	for i, p := range cfg.ExecIsolationPaths {
		if p.Source == "" {
			return fmt.Errorf("tools.exec.isolation.expose_paths[%d].source must not be empty", i)
		}
		if p.Mode != "ro" && p.Mode != "rw" {
			return fmt.Errorf("tools.exec.isolation.expose_paths[%d].mode must be ro or rw", i)
		}
	}
	return nil
}
