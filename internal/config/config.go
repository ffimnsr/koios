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

// MemoryDBPath returns the path for the long-term memory SQLite database, derived from WorkspaceRoot.
func (c *Config) MemoryDBPath() string { return filepath.Join(c.WorkspaceRoot, "memory.db") }

const (
	// DefaultConfigFile is the default runtime config path in the repo root.
	DefaultConfigFile = "koios.config.toml"
)

// Config holds all runtime configuration loaded from koios.config.toml.
type Config struct {
	ListenAddr string
	Provider   string
	APIKey     string
	Model      string
	BaseURL    string

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
	EmbedModel                   string
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

	ToolProfile             string
	ToolsAllow              []string
	ToolsDeny               []string
	ExecEnabled             bool
	ExecEnableDenyPatterns  bool
	ExecCustomDenyPatterns  []string
	ExecCustomAllowPatterns []string
	ExecDefaultTimeout      time.Duration
	ExecMaxTimeout          time.Duration
	ExecApprovalMode        string
	ExecApprovalTTL         time.Duration
	ExecIsolationEnabled    bool
	ExecIsolationPaths      []ExecIsolationPath

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

	// MonitorStaleThreshold is the maximum time of daemon inactivity before the
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
	} `toml:"server"`
	LLM struct {
		Provider string `toml:"provider"`
		APIKey   string `toml:"api_key"`
		Model    string `toml:"model"`
		BaseURL  string `toml:"base_url"`
	} `toml:"llm"`
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
		EmbedModel string   `toml:"embed_model"`
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
	} `toml:"tools"`
	Workspace struct {
		Root     string `toml:"root"`
		PerAgent *bool  `toml:"per_agent"`
		MaxBytes *int   `toml:"max_file_bytes"`
	} `toml:"workspace"`
	Log struct {
		Level     string `toml:"level"`
		File      string `toml:"file"`
		MaxSizeMB *int   `toml:"max_size_mb"`
		MaxBackups *int  `toml:"max_backups"`
		MaxAgeDays *int  `toml:"max_age_days"`
		Compress  *bool  `toml:"compress"`
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
		ListenAddr:                   ":8080",
		Provider:                     "openai",
		Model:                        "gpt-4o",
		MaxSessionMessages:           100,
		RequestTimeout:               2 * time.Minute,
		SessionRetention:             0,
		SessionMaxEntries:            0,
		SessionIdleResetAfter:        0,
		SessionIdlePruneAfter:        0,
		SessionIdlePruneKeep:         0,
		SessionDailyResetTime:        "",
		CompactThreshold:             0,
		CompactReserve:               20,
		EmbedModel:                   "text-embedding-3-small",
		MemoryInject:                 false,
		MemoryTopK:                   3,
		MemoryLCMWindow:              0,
		MilvusURL:                    "localhost:19530",
		MilvusCollection:             "koios_memory",
		MilvusEnabled:                false,
		SessionPruneKeepToolMessages: 8,
		CronMaxConcurrentRuns:        1,
		HeartbeatEvery:               30 * time.Minute,
		HeartbeatEnabled:             true,
		AgentMaxChildren:             4,
		AgentRetryAttempts:           3,
		AgentRetryInitialBackoff:     500 * time.Millisecond,
		AgentRetryMaxBackoff:         5 * time.Second,
		AgentRetryStatusCodes:        []int{429, 500, 502, 503, 504},
		ToolProfile:                  "full",
		ExecEnabled:                  true,
		ExecEnableDenyPatterns:       true,
		ExecDefaultTimeout:           30 * time.Second,
		ExecMaxTimeout:               5 * time.Minute,
		ExecApprovalMode:             "dangerous",
		ExecApprovalTTL:              15 * time.Minute,
		WorkspaceRoot:                "./workspace",
		WorkspacePerAgent:            true,
		WorkspaceMaxBytes:            1 << 20,
		LogLevel:                     "info",
		LogMaxSizeMB:                 20,
		LogMaxBackups:                5,
		LogMaxAgeDays:                14,
		LogCompress:                  true,
		HookTimeout:                  2 * time.Second,
		HookFailClosed:               false,
		PresenceTypingTTL:            8 * time.Second,
		MonitorStaleThreshold:        0,
		MonitorMaxRestarts:           5,
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
	return `# Koios configuration file.
# Generated by: koios init

[server]
listen_addr = ":8080"
request_timeout = "2m"
allowed_origins = []

[llm]
provider = "openai"
model = "gpt-4o"
# api_key = ""
# base_url = ""

[session]
max_messages = 100
retention = "0s"
max_entries = 0
idle_reset_after = "0s"
idle_prune_after = "0s"
idle_prune_keep = 0
daily_reset_time = ""
prune_keep_tool_messages = 8

[compaction]
threshold = 0
reserve = 20

[memory]
embed_model = "text-embedding-3-small"
inject = false
top_k = 3

[memory.milvus]
address = "localhost:19530"
collection = "koios_memory"
enabled = false

[cron]
max_concurrent = 1

[heartbeat]
enabled = true
every = "30m"

[agent]
max_children = 4
retry_attempts = 3
retry_initial_backoff = "500ms"
retry_max_backoff = "5s"
retry_status_codes = [429, 500, 502, 503, 504]

[tools]
profile = "full"
allow = []
deny = []

[tools.exec]
enabled = true
enable_deny_patterns = true
custom_deny_patterns = []
custom_allow_patterns = []
default_timeout = "30s"
max_timeout = "5m"
approval_mode = "dangerous"
approval_ttl = "15m"

[tools.exec.isolation]
enabled = false
expose_paths = []

[workspace]
root = "./workspace"
per_agent = true
max_file_bytes = 1048576

[log]
level = "info"

[monitor]
stale_threshold = "0s"
max_restarts = 5
`
}

// EncodeTOML renders a config in the current canonical schema.
// When includeAPIKey is false and cfg.APIKey is empty, the key is omitted as a comment.
func EncodeTOML(cfg *Config, includeAPIKey bool) string {
	apiKeyLine := "# api_key = \"\""
	if includeAPIKey || strings.TrimSpace(cfg.APIKey) != "" {
		apiKeyLine = "api_key = " + strconv.Quote(cfg.APIKey)
	}
	baseURLLine := "# base_url = \"\""
	if strings.TrimSpace(cfg.BaseURL) != "" {
		baseURLLine = "base_url = " + strconv.Quote(cfg.BaseURL)
	}
	allowedOrigins := "[]"
	if len(cfg.AllowedOrigins) > 0 {
		quoted := make([]string, 0, len(cfg.AllowedOrigins))
		for _, origin := range cfg.AllowedOrigins {
			quoted = append(quoted, strconv.Quote(origin))
		}
		allowedOrigins = "[" + strings.Join(quoted, ", ") + "]"
	}
	return fmt.Sprintf(`# Koios configuration file.
# Generated by: koios init

[server]
listen_addr = %s
request_timeout = %s
allowed_origins = %s

[llm]
provider = %s
model = %s
%s
%s

[session]
max_messages = %d
retention = %s
max_entries = %d
idle_reset_after = %s
idle_prune_after = %s
idle_prune_keep = %d
daily_reset_time = %s
prune_keep_tool_messages = %d

[compaction]
threshold = %d
reserve = %d

[memory]
embed_model = %s
inject = %t
top_k = %d

[memory.milvus]
address = %s
collection = %s
enabled = %t

[cron]
max_concurrent = %d

[heartbeat]
enabled = %t
every = %s

[agent]
max_children = %d
retry_attempts = %d
retry_initial_backoff = %s
retry_max_backoff = %s
retry_status_codes = %s

[tools]
profile = %s
allow = %s
deny = %s

[tools.exec]
default_timeout = %s
max_timeout = %s
approval_mode = %s
approval_ttl = %s

[tools.exec.isolation]
enabled = %t
expose_paths = []

[workspace]
root = %s
per_agent = %t
max_file_bytes = %d

[log]
level = %s

[monitor]
stale_threshold = %s
max_restarts = %d
`,
		strconv.Quote(cfg.ListenAddr),
		strconv.Quote(cfg.RequestTimeout.String()),
		allowedOrigins,
		strconv.Quote(cfg.Provider),
		strconv.Quote(cfg.Model),
		apiKeyLine,
		baseURLLine,
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
		strconv.Quote(cfg.EmbedModel),
		cfg.MemoryInject,
		cfg.MemoryTopK,
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
		quoteStringSlice(cfg.ToolsAllow),
		quoteStringSlice(cfg.ToolsDeny),
		strconv.Quote(cfg.ExecDefaultTimeout.String()),
		strconv.Quote(cfg.ExecMaxTimeout.String()),
		strconv.Quote(cfg.ExecApprovalMode),
		strconv.Quote(cfg.ExecApprovalTTL.String()),
		cfg.ExecIsolationEnabled,
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

	if src.LLM.Provider != "" {
		dst.Provider = src.LLM.Provider
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

	if src.Memory.EmbedModel != "" {
		dst.EmbedModel = src.Memory.EmbedModel
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

func quoteIntSlice(values []int) string {
	if len(values) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
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
