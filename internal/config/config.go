package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
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

// WorkspaceSkillsDir returns the path where workspace-local skills are stored.
func (c *Config) WorkspaceSkillsDir() string { return filepath.Join(c.WorkspaceRoot, "skills") }

// ManagedSkillsDir returns the path where runtime-managed skills are stored.
func (c *Config) ManagedSkillsDir() string {
	return filepath.Join(c.WorkspaceRoot, "managed", "skills")
}

// PersonalSkillsDir returns the user-local skills directory.
func (c *Config) PersonalSkillsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return ""
	}
	return filepath.Join(home, ".koios", "workspace", "skills")
}

// BrowserDir returns the path where managed browser profile data is stored.
func (c *Config) BrowserDir() string { return filepath.Join(c.WorkspaceRoot, "browser") }

// WorkflowDir returns the path where workflow definitions and run records are
// stored, derived from WorkspaceRoot.
func (c *Config) WorkflowDir() string { return filepath.Join(c.WorkspaceRoot, "workflows") }

// RunsDir returns the path where the unified run ledger is stored, derived
// from WorkspaceRoot.
func (c *Config) RunsDir() string { return filepath.Join(c.WorkspaceRoot, "runs") }

// DBDir returns the path where durable SQLite databases are stored, derived
// from WorkspaceRoot.
func (c *Config) DBDir() string { return filepath.Join(c.WorkspaceRoot, "db") }

// ChannelBindingsPath returns the path for channel binding approvals.
func (c *Config) ChannelBindingsPath() string {
	return filepath.Join(c.DBDir(), "channel_bindings.json")
}

// MemoryDBPath returns the path for the long-term memory SQLite database, derived from WorkspaceRoot.
func (c *Config) MemoryDBPath() string      { return filepath.Join(c.DBDir(), "memory.db") }
func (c *Config) MCPRegistryDBPath() string { return filepath.Join(c.DBDir(), "mcp_registry.db") }

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
func (c *Config) PeerLLMDBPath() string     { return filepath.Join(c.DBDir(), "peer_llm.db") }

// LockFilePath returns the path of the single-instance gateway lock file.
func (c *Config) LockFilePath() string { return filepath.Join(c.WorkspaceRoot, "koios.lock") }

const (
	// DefaultConfigFile is the default runtime config path in the repo root.
	DefaultConfigFile = "koios.config.toml"
)

var supportedLLMProviders = []string{
	"anthropic",
	"gemini",
	"litellm",
	"nvidia",
	"ollama",
	"openai",
	"openrouter",
	"vllm",
}

var localLLMProviders = map[string]struct{}{
	"litellm": {},
	"ollama":  {},
	"vllm":    {},
}

var supportedWebSearchProviders = []string{
	"brave",
	"exa",
	"tavily",
}

// SupportedLLMProviders returns the canonical set of runtime-supported LLM providers.
func SupportedLLMProviders() []string {
	providers := make([]string, len(supportedLLMProviders))
	copy(providers, supportedLLMProviders)
	return providers
}

// IsSupportedLLMProvider reports whether name is a runtime-supported provider.
func IsSupportedLLMProvider(name string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	return slices.Contains(supportedLLMProviders, trimmed)
}

// SupportedLLMProvidersHint returns the supported providers as human-readable text.
func SupportedLLMProvidersHint() string {
	return strings.Join(supportedLLMProviders, ", ")
}

// IsLocalLLMProvider reports whether the provider typically runs locally and can omit an API key.
func IsLocalLLMProvider(name string) bool {
	_, ok := localLLMProviders[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// SupportedWebSearchProviders returns the canonical set of runtime-supported web search providers.
func SupportedWebSearchProviders() []string {
	providers := make([]string, len(supportedWebSearchProviders))
	copy(providers, supportedWebSearchProviders)
	return providers
}

// IsSupportedWebSearchProvider reports whether name is a runtime-supported web search provider.
func IsSupportedWebSearchProvider(name string) bool {
	trimmed := strings.ToLower(strings.TrimSpace(name))
	return slices.Contains(supportedWebSearchProviders, trimmed)
}

// SupportedWebSearchProvidersHint returns the supported web search providers as human-readable text.
func SupportedWebSearchProvidersHint() string {
	return strings.Join(supportedWebSearchProviders, ", ")
}

type WebSearchProviderConfig struct {
	APIKey         string `toml:"api_key"`
	BaseURL        string `toml:"base_url"`
	DefaultTimeout string `toml:"default_timeout"`
}

type RuntimeWebSearchProviderConfig struct {
	APIKey         string
	BaseURL        string
	DefaultTimeout time.Duration
}

type BrowserRunConfig struct {
	Enabled        bool
	AccountID      string
	APIToken       string
	BaseURL        string
	DefaultTimeout time.Duration
}

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
	// Kind carries internal server classification such as "browser".
	Kind string `toml:"-"`
	// ProfileName carries an internal profile selector for kind-specific routing.
	ProfileName string `toml:"-"`
}

// BrowserProfileConfig describes one named Chrome DevTools MCP-backed browser profile.
type BrowserProfileConfig struct {
	Name                   string            `toml:"name"`
	Mode                   string            `toml:"mode"`
	Enabled                bool              `toml:"enabled"`
	HostAllowlist          []string          `toml:"host_allowlist"`
	HostDenylist           []string          `toml:"host_denylist"`
	Headless               bool              `toml:"headless"`
	Isolated               bool              `toml:"isolated"`
	Slim                   bool              `toml:"slim"`
	Channel                string            `toml:"channel"`
	ExecutablePath         string            `toml:"executable_path"`
	UserDataDir            string            `toml:"user_data_dir"`
	Viewport               string            `toml:"viewport"`
	BrowserURL             string            `toml:"browser_url"`
	WSEndpoint             string            `toml:"ws_endpoint"`
	WSHeaders              map[string]string `toml:"ws_headers"`
	ChromeArgs             []string          `toml:"chrome_args"`
	AcceptInsecureCerts    bool              `toml:"accept_insecure_certs"`
	ExperimentalVision     bool              `toml:"experimental_vision"`
	ExperimentalScreencast bool              `toml:"experimental_screencast"`
	ExperimentalFfmpegPath string            `toml:"experimental_ffmpeg_path"`
	PerformanceCrux        *bool             `toml:"performance_crux"`
	UsageStatistics        *bool             `toml:"usage_statistics"`
	RedactNetworkHeaders   *bool             `toml:"redact_network_headers"`
	MCPCommand             string            `toml:"mcp_command"`
	MCPPackage             string            `toml:"mcp_package"`
}

type BrowserConfig struct {
	DefaultProfile string                 `toml:"default_profile"`
	Profiles       []BrowserProfileConfig `toml:"profiles"`
}

type SkillCommandOverride struct {
	Name          string `toml:"name"`
	Description   string `toml:"description"`
	AssistantText string `toml:"assistant_text"`
}

type SkillOverride struct {
	Enabled     *bool                  `toml:"enabled"`
	Name        string                 `toml:"name"`
	Description string                 `toml:"description"`
	Agents      []string               `toml:"agents"`
	Trust       string                 `toml:"trust"`
	Commands    []SkillCommandOverride `toml:"commands"`
}

type SkillSearchPath struct {
	Kind string
	Path string
	Rank int
}

type TelegramChannelConfig struct {
	Enabled          bool
	BotToken         string
	Mode             string
	WebhookURL       string
	WebhookSecret    string
	InboxPeerID      string
	PollTimeout      time.Duration
	TextChunkLimit   int
	TextChunkMode    string
	StreamQueueMode  string
	StreamThrottle   time.Duration
	AllowedChatIDs   []int64
	AllowedSenderIDs []int64
	CommandSenderIDs []int64
	ActivationMode   string
	ReplyActivation  bool
	DMPolicy         string
	PairingCodeTTL   time.Duration
	ThreadMode       string
	GroupPolicies    []TelegramGroupPolicy
}

type TelegramGroupPolicy struct {
	ChatID           int64
	ActivationMode   string
	AllowedSenderIDs []int64
	CommandSenderIDs []int64
	ReplyActivation  *bool
	AllowedTopicIDs  []int64
	ThreadMode       string
}

type HookFieldTransform struct {
	To       string `toml:"to"`
	From     string `toml:"from"`
	Value    string `toml:"value"`
	Template string `toml:"template"`
	Required *bool  `toml:"required"`
}

type HookMapping struct {
	Name    string               `toml:"name"`
	Path    string               `toml:"path"`
	Type    string               `toml:"type"`
	Enabled *bool                `toml:"enabled"`
	Fields  []HookFieldTransform `toml:"fields"`
}

// Config holds all runtime configuration loaded from koios.config.toml.
type Config struct {
	ListenAddr     string
	Provider       string
	APIKey         string
	Model          string
	BaseURL        string
	LLMIdleTimeout time.Duration
	// LLMContextWindowTokens is the approximate prompt+completion budget used for
	// local context trimming before dispatch. Zero disables request-side budgeting.
	LLMContextWindowTokens int
	// LLMPromptReserveTokens reserves completion/tool-call headroom inside the
	// context window when MaxTokens is not explicitly set on the request.
	LLMPromptReserveTokens int
	// LLMMaxToolDefinitions caps how many native tool schemas are sent on one
	// request after relevance ranking. Zero disables schema capping.
	LLMMaxToolDefinitions int
	// LLMMaxToolResultChars caps one tool-result payload kept in active run
	// context. Zero disables tool-result truncation.
	LLMMaxToolResultChars int

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
	// Browser holds Chrome DevTools MCP-backed browser profiles.
	Browser BrowserConfig

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
	AgentMaxSteps            int
	AgentRetryAttempts       int
	AgentRetryInitialBackoff time.Duration
	AgentRetryMaxBackoff     time.Duration
	AgentRetryStatusCodes    []int

	AllowedOrigins []string
	// OwnerPeerIDs restricts owner-only slash commands (e.g. /restart) to
	// the listed peer IDs. An empty list grants the commands to all peers.
	OwnerPeerIDs []string

	ToolProfile string
	ToolsAllow  []string
	ToolsDeny   []string
	// SandboxToolProfile is the base tool profile applied to sessions whose
	// SessionKind is "sandbox". Defaults to "sandbox" when empty.
	SandboxToolProfile string
	// SandboxToolsAllow are additional tools allowed on top of the sandbox profile.
	SandboxToolsAllow []string
	// SandboxToolsDeny are tools denied for sandbox sessions (merged last, wins over allow).
	SandboxToolsDeny              []string
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
	WebSearchEnabled              bool
	WebSearchProviders            []string
	WebSearchBrave                RuntimeWebSearchProviderConfig
	WebSearchExa                  RuntimeWebSearchProviderConfig
	WebSearchTavily               RuntimeWebSearchProviderConfig
	BrowserRun                    BrowserRunConfig
	SkillDirs                     []string
	SkillOverrides                map[string]SkillOverride
	ExtensionDirs                 []string
	ExtensionAllow                []string
	ExtensionDeny                 []string
	Telegram                      TelegramChannelConfig

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
	// WebhookToken is the bearer token required on POST /v1/webhooks/events and
	// POST /v1/hooks/{name}.
	WebhookToken string
	// HookMappings declares named inbound webhook routes under /v1/hooks/{name}
	// that transform arbitrary request payloads into existing webhook event
	// requests such as session.wake or cron.schedule.
	HookMappings []HookMapping

	// PresenceTypingTTL controls how long a "typing" indicator is held (default 8s).
	PresenceTypingTTL time.Duration

	// MonitorStaleThreshold is the maximum time of gateway inactivity before the
	// health monitor logs a warning. 0 disables stale detection.
	MonitorStaleThreshold time.Duration
	// MonitorMaxRestarts caps automatic subsystem restarts; 0 = unlimited.
	MonitorMaxRestarts int

	// hiddenSecrets preserves encoded secret literals so config rewrites do not
	// leak decrypted values back into koios.config.toml.
	hiddenSecrets map[string]string
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
		DefaultProfile      string         `toml:"default_profile"`
		IdleTimeout         string         `toml:"idle_timeout"`
		ContextWindowTokens *int           `toml:"context_window_tokens"`
		PromptReserveTokens *int           `toml:"prompt_reserve_tokens"`
		MaxToolDefinitions  *int           `toml:"max_tool_definitions"`
		MaxToolResultChars  *int           `toml:"max_tool_result_chars"`
		LightweightModel    string         `toml:"lightweight_model"`
		FallbackModels      []string       `toml:"fallback_models"`
		Profiles            []ModelProfile `toml:"profiles"`
	} `toml:"llm"`
	MCP struct {
		Servers []MCPServerConfig `toml:"servers"`
	} `toml:"mcp"`
	Extensions struct {
		Dirs  []string `toml:"dirs"`
		Allow []string `toml:"allow"`
		Deny  []string `toml:"deny"`
	} `toml:"extensions"`
	Skills struct {
		Dirs      []string                 `toml:"dirs"`
		Overrides map[string]SkillOverride `toml:"overrides"`
	} `toml:"skills"`
	Browser  BrowserConfig `toml:"browser"`
	Channels struct {
		Telegram struct {
			Enabled          *bool   `toml:"enabled"`
			BotToken         string  `toml:"bot_token"`
			Mode             string  `toml:"mode"`
			WebhookURL       string  `toml:"webhook_url"`
			WebhookSecret    string  `toml:"webhook_secret"`
			InboxPeerID      string  `toml:"inbox_peer_id"`
			PollTimeout      string  `toml:"poll_timeout"`
			TextChunkLimit   *int    `toml:"text_chunk_limit"`
			TextChunkMode    string  `toml:"text_chunk_mode"`
			StreamQueueMode  string  `toml:"stream_queue_mode"`
			StreamThrottle   string  `toml:"stream_throttle"`
			AllowedChatIDs   []int64 `toml:"allowed_chat_ids"`
			AllowedSenderIDs []int64 `toml:"allowed_sender_ids"`
			CommandSenderIDs []int64 `toml:"command_sender_ids"`
			ActivationMode   string  `toml:"activation_mode"`
			ReplyActivation  *bool   `toml:"reply_activation"`
			DMPolicy         string  `toml:"dm_policy"`
			PairingCodeTTL   string  `toml:"pairing_code_ttl"`
			ThreadMode       string  `toml:"thread_mode"`
			Groups           []struct {
				ChatID           int64   `toml:"chat_id"`
				ActivationMode   string  `toml:"activation_mode"`
				AllowedSenderIDs []int64 `toml:"allowed_sender_ids"`
				CommandSenderIDs []int64 `toml:"command_sender_ids"`
				ReplyActivation  *bool   `toml:"reply_activation"`
				AllowedTopicIDs  []int64 `toml:"allowed_topic_ids"`
				ThreadMode       string  `toml:"thread_mode"`
			} `toml:"groups"`
		} `toml:"telegram"`
	} `toml:"channels"`
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
		MaxSteps            *int   `toml:"max_steps"`
		RetryAttempts       *int   `toml:"retry_attempts"`
		RetryInitialBackoff string `toml:"retry_initial_backoff"`
		RetryMaxBackoff     string `toml:"retry_max_backoff"`
		RetryStatusCodes    []int  `toml:"retry_status_codes"`
	} `toml:"agent"`
	Tools struct {
		Profile string   `toml:"profile"`
		Allow   []string `toml:"allow"`
		Deny    []string `toml:"deny"`
		Sandbox struct {
			Profile string   `toml:"profile"`
			Allow   []string `toml:"allow"`
			Deny    []string `toml:"deny"`
		} `toml:"sandbox"`
		Exec struct {
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
		WebSearch struct {
			Enabled   *bool                   `toml:"enabled"`
			Providers []string                `toml:"providers"`
			Brave     WebSearchProviderConfig `toml:"brave"`
			Exa       WebSearchProviderConfig `toml:"exa"`
			Tavily    WebSearchProviderConfig `toml:"tavily"`
		} `toml:"web_search"`
		BrowserRun struct {
			Enabled        *bool  `toml:"enabled"`
			AccountID      string `toml:"account_id"`
			APIToken       string `toml:"api_token"`
			BaseURL        string `toml:"base_url"`
			DefaultTimeout string `toml:"default_timeout"`
		} `toml:"browser_run"`
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
		WebhookURL     string        `toml:"webhook_url"`
		WebhookSecret  string        `toml:"webhook_secret"`
		InterceptorURL string        `toml:"interceptor_url"`
		WebhookToken   string        `toml:"webhook_token"`
		Timeout        string        `toml:"timeout"`
		FailClosed     *bool         `toml:"fail_closed"`
		Mappings       []HookMapping `toml:"mappings"`
	} `toml:"hooks"`
	Presence struct {
		TypingTTL string `toml:"typing_ttl"`
	} `toml:"presence"`
}

// Default returns sane defaults for a local-first Koios setup.
func Default() *Config {
	return &Config{
		ListenAddr:             ":8080",
		Provider:               "openai",
		Model:                  "gpt-4o",
		LLMIdleTimeout:         30 * time.Second,
		LLMContextWindowTokens: 32000,
		LLMPromptReserveTokens: 4000,
		LLMMaxToolDefinitions:  24,
		LLMMaxToolResultChars:  4000,
		DefaultProfile:         "default",
		ModelProfiles: []ModelProfile{{
			Name:     "default",
			Provider: "openai",
			Model:    "gpt-4o",
		}},
		Browser: BrowserConfig{
			Profiles: nil,
		},
		MaxSessionMessages:            100,
		RequestTimeout:                2 * time.Minute,
		SessionRetention:              0,
		SessionMaxEntries:             0,
		SessionIdleResetAfter:         0,
		SessionIdlePruneAfter:         0,
		SessionIdlePruneKeep:          0,
		SessionDailyResetTime:         "",
		CompactThreshold:              60,
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
		AgentMaxSteps:                 80,
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
		WebSearchEnabled:              false,
		WebSearchProviders:            []string{"brave"},
		WebSearchBrave: RuntimeWebSearchProviderConfig{
			BaseURL:        "https://api.search.brave.com/res/v1/web/search",
			DefaultTimeout: 15 * time.Second,
		},
		WebSearchExa: RuntimeWebSearchProviderConfig{
			BaseURL:        "https://api.exa.ai/search",
			DefaultTimeout: 15 * time.Second,
		},
		WebSearchTavily: RuntimeWebSearchProviderConfig{
			BaseURL:        "https://api.tavily.com/search",
			DefaultTimeout: 15 * time.Second,
		},
		BrowserRun: BrowserRunConfig{
			Enabled:        false,
			BaseURL:        "https://api.cloudflare.com/client/v4",
			DefaultTimeout: 30 * time.Second,
		},
		SkillDirs:      nil,
		SkillOverrides: nil,
		ExtensionDirs:  nil,
		ExtensionAllow: nil,
		ExtensionDeny:  nil,
		Telegram: TelegramChannelConfig{
			Enabled:         false,
			Mode:            "polling",
			PollTimeout:     25 * time.Second,
			TextChunkLimit:  4096,
			TextChunkMode:   "paragraph",
			StreamQueueMode: "burst",
			StreamThrottle:  750 * time.Millisecond,
			ActivationMode:  "always",
			ReplyActivation: true,
			DMPolicy:        "open",
			PairingCodeTTL:  24 * time.Hour,
			ThreadMode:      "topic",
		},
		WorkspaceRoot:         "./workspace",
		WorkspacePerAgent:     true,
		WorkspaceMaxBytes:     1 << 20,
		LogLevel:              "info",
		LogMaxSizeMB:          20,
		LogMaxBackups:         5,
		LogMaxAgeDays:         14,
		LogCompress:           true,
		HookTimeout:           2 * time.Second,
		HookFailClosed:        false,
		HookMappings:          nil,
		PresenceTypingTTL:     8 * time.Second,
		MonitorStaleThreshold: 0,
		MonitorMaxRestarts:    5,
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
	if err := decodeHiddenSecrets(cfg, &fileCfg); err != nil {
		return nil, true, fmt.Errorf("decode hidden secrets in config file %s: %w", path, err)
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
	browserSection := encodeBrowserSection(cfg)
	webSearchSection := encodeWebSearchSection(cfg, includeAPIKey)
	browserRunSection := encodeBrowserRunSection(cfg, includeAPIKey)
	hooksSection := encodeHooksSection(cfg)
	channelsSection := encodeChannelsSection(cfg)
	skillOverridesSection := encodeSkillOverridesSection(cfg)

	return fmt.Sprintf(encodedTOMLTemplate,
		strconv.Quote(cfg.ListenAddr),
		strconv.Quote(cfg.RequestTimeout.String()),
		allowedOrigins,
		llmSection,
		mcpSection,
		browserSection,
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
		cfg.AgentMaxSteps,
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
		webSearchSection,
		browserRunSection,
		inlineQuotedStringSlice(cfg.ExtensionDirs),
		inlineQuotedStringSlice(cfg.ExtensionAllow),
		inlineQuotedStringSlice(cfg.ExtensionDeny),
		inlineQuotedStringSlice(cfg.SkillDirs),
		skillOverridesSection,
		hooksSection,
		channelsSection,
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

func encodeWebSearchSection(cfg *Config, includeAPIKey bool) string {
	if cfg == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("[tools.web_search]\n")
	fmt.Fprintf(&b, "enabled = %t\n", cfg.WebSearchEnabled)
	fmt.Fprintf(&b, "providers = [%s]\n\n", inlineQuotedStringSlice(cfg.WebSearchProviders))
	encodeWebSearchProviderSection := func(name string, provider RuntimeWebSearchProviderConfig) {
		fmt.Fprintf(&b, "[tools.web_search.%s]\n", name)
		if provider.APIKey != "" || includeAPIKey {
			fmt.Fprintf(&b, "api_key = %s\n", encodeHiddenSecretLiteral(cfg, hiddenSecretPathWebSearchAPIKey(name), provider.APIKey))
		}
		fmt.Fprintf(&b, "base_url = %s\n", strconv.Quote(strings.TrimSpace(provider.BaseURL)))
		fmt.Fprintf(&b, "default_timeout = %s\n\n", strconv.Quote(provider.DefaultTimeout.String()))
	}
	encodeWebSearchProviderSection("brave", cfg.WebSearchBrave)
	encodeWebSearchProviderSection("exa", cfg.WebSearchExa)
	encodeWebSearchProviderSection("tavily", cfg.WebSearchTavily)
	return b.String()
}

func encodeBrowserRunSection(cfg *Config, includeAPIKey bool) string {
	if cfg == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("[tools.browser_run]\n")
	fmt.Fprintf(&b, "enabled = %t\n", cfg.BrowserRun.Enabled)
	fmt.Fprintf(&b, "account_id = %s\n", strconv.Quote(strings.TrimSpace(cfg.BrowserRun.AccountID)))
	if cfg.BrowserRun.APIToken != "" || includeAPIKey {
		fmt.Fprintf(&b, "api_token = %s\n", encodeHiddenSecretLiteral(cfg, hiddenSecretPathBrowserRunAPIToken, cfg.BrowserRun.APIToken))
	}
	fmt.Fprintf(&b, "base_url = %s\n", strconv.Quote(strings.TrimSpace(cfg.BrowserRun.BaseURL)))
	fmt.Fprintf(&b, "default_timeout = %s\n\n", strconv.Quote(cfg.BrowserRun.DefaultTimeout.String()))
	return b.String()
}

func applyWebSearchProviderFileConfig(dst *RuntimeWebSearchProviderConfig, src WebSearchProviderConfig) {
	if dst == nil {
		return
	}
	if src.APIKey != "" {
		dst.APIKey = src.APIKey
	}
	if src.BaseURL != "" {
		dst.BaseURL = strings.TrimSpace(src.BaseURL)
	}
	if src.DefaultTimeout != "" {
		if d, err := time.ParseDuration(src.DefaultTimeout); err == nil {
			dst.DefaultTimeout = d
		}
	}
}

func encodeSkillOverridesSection(cfg *Config) string {
	if cfg == nil || len(cfg.SkillOverrides) == 0 {
		return ""
	}
	keys := make([]string, 0, len(cfg.SkillOverrides))
	for key := range cfg.SkillOverrides {
		if strings.TrimSpace(key) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		override := cfg.SkillOverrides[key]
		fmt.Fprintf(&b, "\n[skills.overrides.%s]\n", strconv.Quote(key))
		if override.Enabled != nil {
			fmt.Fprintf(&b, "enabled = %t\n", *override.Enabled)
		}
		if strings.TrimSpace(override.Name) != "" {
			fmt.Fprintf(&b, "name = %s\n", strconv.Quote(override.Name))
		}
		if strings.TrimSpace(override.Description) != "" {
			fmt.Fprintf(&b, "description = %s\n", strconv.Quote(override.Description))
		}
		if len(override.Agents) > 0 {
			fmt.Fprintf(&b, "agents = [%s]\n", inlineQuotedStringSlice(override.Agents))
		}
		if strings.TrimSpace(override.Trust) != "" {
			fmt.Fprintf(&b, "trust = %s\n", strconv.Quote(override.Trust))
		}
		for _, command := range override.Commands {
			b.WriteString("[[skills.overrides.")
			b.WriteString(strconv.Quote(key))
			b.WriteString(".commands]]\n")
			fmt.Fprintf(&b, "name = %s\n", strconv.Quote(command.Name))
			if strings.TrimSpace(command.Description) != "" {
				fmt.Fprintf(&b, "description = %s\n", strconv.Quote(command.Description))
			}
			if strings.TrimSpace(command.AssistantText) != "" {
				fmt.Fprintf(&b, "assistant_text = %s\n", strconv.Quote(command.AssistantText))
			}
		}
	}
	return strings.TrimPrefix(b.String(), "\n")
}

func (c *Config) hiddenSecret(path string) string {
	if c == nil || c.hiddenSecrets == nil {
		return ""
	}
	return c.hiddenSecrets[path]
}

func (c *Config) setHiddenSecret(path, raw string) {
	if c == nil || strings.TrimSpace(path) == "" {
		return
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if c.hiddenSecrets != nil {
			delete(c.hiddenSecrets, path)
		}
		return
	}
	if c.hiddenSecrets == nil {
		c.hiddenSecrets = make(map[string]string)
	}
	c.hiddenSecrets[path] = trimmed
}

func encodeHooksSection(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	if strings.TrimSpace(cfg.HookWebhookURL) == "" && strings.TrimSpace(cfg.HookWebhookSecret) == "" && strings.TrimSpace(cfg.HookInterceptorURL) == "" && strings.TrimSpace(cfg.WebhookToken) == "" && cfg.HookTimeout <= 0 && !cfg.HookFailClosed && len(cfg.HookMappings) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[hooks]\n")
	fmt.Fprintf(&b, "webhook_url = %s\n", strconv.Quote(strings.TrimSpace(cfg.HookWebhookURL)))
	fmt.Fprintf(&b, "webhook_secret = %s\n", encodeHiddenSecretLiteral(cfg, hiddenSecretPathHooksWebhookSecret, cfg.HookWebhookSecret))
	fmt.Fprintf(&b, "interceptor_url = %s\n", strconv.Quote(strings.TrimSpace(cfg.HookInterceptorURL)))
	fmt.Fprintf(&b, "webhook_token = %s\n", encodeHiddenSecretLiteral(cfg, hiddenSecretPathHooksWebhookToken, strings.TrimSpace(cfg.WebhookToken)))
	fmt.Fprintf(&b, "timeout = %s\n", strconv.Quote(cfg.HookTimeout.String()))
	fmt.Fprintf(&b, "fail_closed = %t\n", cfg.HookFailClosed)
	b.WriteString("\n")
	for _, mapping := range cfg.HookMappings {
		b.WriteString("[[hooks.mappings]]\n")
		fmt.Fprintf(&b, "name = %s\n", strconv.Quote(strings.TrimSpace(mapping.Name)))
		if strings.TrimSpace(mapping.Path) != "" {
			fmt.Fprintf(&b, "path = %s\n", strconv.Quote(strings.TrimSpace(mapping.Path)))
		}
		fmt.Fprintf(&b, "type = %s\n", strconv.Quote(strings.TrimSpace(mapping.Type)))
		if mapping.Enabled != nil {
			fmt.Fprintf(&b, "enabled = %t\n", *mapping.Enabled)
		}
		b.WriteString("\n")
		for _, field := range mapping.Fields {
			b.WriteString("[[hooks.mappings.fields]]\n")
			fmt.Fprintf(&b, "to = %s\n", strconv.Quote(strings.TrimSpace(field.To)))
			if strings.TrimSpace(field.From) != "" {
				fmt.Fprintf(&b, "from = %s\n", strconv.Quote(strings.TrimSpace(field.From)))
			}
			if field.Value != "" {
				fmt.Fprintf(&b, "value = %s\n", strconv.Quote(field.Value))
			}
			if field.Template != "" {
				fmt.Fprintf(&b, "template = %s\n", strconv.Quote(field.Template))
			}
			if field.Required != nil {
				fmt.Fprintf(&b, "required = %t\n", *field.Required)
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// encodeLLMSection builds the canonical [llm] and [[llm.profiles]] config.
func encodeLLMSection(cfg *Config, includeAPIKey bool) string {
	var b strings.Builder
	defaultProfile := strings.TrimSpace(cfg.DefaultProfile)
	profiles := append([]ModelProfile(nil), cfg.ModelProfiles...)
	if len(profiles) == 0 {
		if defaultProfile == "" {
			defaultProfile = "default"
		}
		profiles = []ModelProfile{{
			Name:     defaultProfile,
			Provider: cfg.Provider,
			APIKey:   cfg.APIKey,
			BaseURL:  cfg.BaseURL,
			Model:    cfg.Model,
		}}
	} else if defaultProfile == "" {
		defaultProfile = profiles[0].Name
	}
	selectedIndex := -1
	for i, profile := range profiles {
		if profile.Name == defaultProfile {
			selectedIndex = i
			break
		}
	}
	if selectedIndex == -1 {
		profiles = append([]ModelProfile{{
			Name:     defaultProfile,
			Provider: cfg.Provider,
			APIKey:   cfg.APIKey,
			BaseURL:  cfg.BaseURL,
			Model:    cfg.Model,
		}}, profiles...)
		selectedIndex = 0
	}
	profiles[selectedIndex].Provider = cfg.Provider
	profiles[selectedIndex].APIKey = cfg.APIKey
	profiles[selectedIndex].BaseURL = cfg.BaseURL
	profiles[selectedIndex].Model = cfg.Model
	b.WriteString("[llm]\n")
	fmt.Fprintf(&b, "idle_timeout = %s\n", strconv.Quote(cfg.LLMIdleTimeout.String()))
	fmt.Fprintf(&b, "context_window_tokens = %d\n", cfg.LLMContextWindowTokens)
	fmt.Fprintf(&b, "prompt_reserve_tokens = %d\n", cfg.LLMPromptReserveTokens)
	fmt.Fprintf(&b, "max_tool_definitions = %d\n", cfg.LLMMaxToolDefinitions)
	fmt.Fprintf(&b, "max_tool_result_chars = %d\n", cfg.LLMMaxToolResultChars)
	fmt.Fprintf(&b, "default_profile = %s\n", strconv.Quote(defaultProfile))
	if cfg.LightweightModel != "" {
		fmt.Fprintf(&b, "lightweight_model = %s\n", strconv.Quote(cfg.LightweightModel))
	}
	if len(cfg.FallbackModels) > 0 {
		fmt.Fprintf(&b, "fallback_models = %s\n", quoteStringSlice(cfg.FallbackModels))
	}
	b.WriteString("\n")
	for _, p := range profiles {
		b.WriteString("[[llm.profiles]]\n")
		fmt.Fprintf(&b, "name = %s\n", strconv.Quote(p.Name))
		fmt.Fprintf(&b, "provider = %s\n", strconv.Quote(p.Provider))
		fmt.Fprintf(&b, "model = %s\n", strconv.Quote(p.Model))
		if p.APIKey != "" || includeAPIKey {
			fmt.Fprintf(&b, "api_key = %s\n", encodeHiddenSecretLiteral(cfg, hiddenSecretPathModelProfileAPIKey(p.Name), p.APIKey))
		}
		if p.BaseURL != "" {
			fmt.Fprintf(&b, "base_url = %s\n", strconv.Quote(p.BaseURL))
		}
		b.WriteString("\n")
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
		fmt.Fprintf(&b, "name = %s\n", strconv.Quote(s.Name))
		fmt.Fprintf(&b, "transport = %s\n", strconv.Quote(s.Transport))
		if s.Command != "" {
			fmt.Fprintf(&b, "command = %s\n", strconv.Quote(s.Command))
		}
		if len(s.Args) > 0 {
			fmt.Fprintf(&b, "args = %s\n", quoteStringSlice(s.Args))
		}
		if s.URL != "" {
			fmt.Fprintf(&b, "url = %s\n", strconv.Quote(s.URL))
		}
		if s.Timeout != "" {
			fmt.Fprintf(&b, "timeout = %s\n", strconv.Quote(s.Timeout))
		}
		fmt.Fprintf(&b, "enabled = %t\n", s.Enabled)
		b.WriteString("\n")
	}
	return b.String()
}

func encodeChannelsSection(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	tg := cfg.Telegram
	if !tg.Enabled && strings.TrimSpace(tg.BotToken) == "" && strings.TrimSpace(tg.WebhookURL) == "" && strings.TrimSpace(tg.InboxPeerID) == "" && len(tg.AllowedChatIDs) == 0 && len(tg.AllowedSenderIDs) == 0 && len(tg.GroupPolicies) == 0 {
		return ""
	}
	mode := strings.TrimSpace(tg.Mode)
	if mode == "" {
		mode = "polling"
	}
	activationMode := normalizeTelegramActivationMode(tg.ActivationMode)
	dmPolicy := normalizeTelegramDMPolicy(tg.DMPolicy)
	threadMode := normalizeTelegramThreadMode(tg.ThreadMode)
	var b strings.Builder
	b.WriteString("[channels.telegram]\n")
	fmt.Fprintf(&b, "enabled = %t\n", tg.Enabled)
	fmt.Fprintf(&b, "bot_token = %s\n", encodeHiddenSecretLiteral(cfg, hiddenSecretPathTelegramBotToken, tg.BotToken))
	fmt.Fprintf(&b, "mode = %s\n", strconv.Quote(mode))
	fmt.Fprintf(&b, "poll_timeout = %s\n", strconv.Quote(tg.PollTimeout.String()))
	fmt.Fprintf(&b, "webhook_url = %s\n", strconv.Quote(tg.WebhookURL))
	fmt.Fprintf(&b, "webhook_secret = %s\n", encodeHiddenSecretLiteral(cfg, hiddenSecretPathTelegramWebhookSecret, tg.WebhookSecret))
	fmt.Fprintf(&b, "inbox_peer_id = %s\n", strconv.Quote(strings.TrimSpace(tg.InboxPeerID)))
	fmt.Fprintf(&b, "text_chunk_limit = %s\n", strconv.Itoa(tg.TextChunkLimit))
	fmt.Fprintf(&b, "text_chunk_mode = %s\n", strconv.Quote(normalizeTelegramTextChunkMode(tg.TextChunkMode)))
	fmt.Fprintf(&b, "stream_queue_mode = %s\n", strconv.Quote(normalizeTelegramStreamQueueMode(tg.StreamQueueMode)))
	fmt.Fprintf(&b, "stream_throttle = %s\n", strconv.Quote(tg.StreamThrottle.String()))
	fmt.Fprintf(&b, "allowed_chat_ids = [%s]\n", quoteInt64Slice(tg.AllowedChatIDs))
	fmt.Fprintf(&b, "allowed_sender_ids = [%s]\n", quoteInt64Slice(tg.AllowedSenderIDs))
	fmt.Fprintf(&b, "command_sender_ids = [%s]\n", quoteInt64Slice(tg.CommandSenderIDs))
	fmt.Fprintf(&b, "activation_mode = %s\n", strconv.Quote(activationMode))
	fmt.Fprintf(&b, "reply_activation = %t\n", tg.ReplyActivation)
	fmt.Fprintf(&b, "dm_policy = %s\n", strconv.Quote(dmPolicy))
	fmt.Fprintf(&b, "pairing_code_ttl = %s\n", strconv.Quote(tg.PairingCodeTTL.String()))
	fmt.Fprintf(&b, "thread_mode = %s\n", strconv.Quote(threadMode))
	b.WriteString("\n")
	for _, group := range tg.GroupPolicies {
		b.WriteString("[[channels.telegram.groups]]\n")
		fmt.Fprintf(&b, "chat_id = %s\n", strconv.FormatInt(group.ChatID, 10))
		if group.ActivationMode != "" {
			fmt.Fprintf(&b, "activation_mode = %s\n", strconv.Quote(normalizeTelegramActivationMode(group.ActivationMode)))
		}
		if len(group.AllowedSenderIDs) > 0 {
			fmt.Fprintf(&b, "allowed_sender_ids = [%s]\n", quoteInt64Slice(group.AllowedSenderIDs))
		}
		if len(group.CommandSenderIDs) > 0 {
			fmt.Fprintf(&b, "command_sender_ids = [%s]\n", quoteInt64Slice(group.CommandSenderIDs))
		}
		if group.ReplyActivation != nil {
			fmt.Fprintf(&b, "reply_activation = %t\n", *group.ReplyActivation)
		}
		if len(group.AllowedTopicIDs) > 0 {
			fmt.Fprintf(&b, "allowed_topic_ids = [%s]\n", quoteInt64Slice(group.AllowedTopicIDs))
		}
		if group.ThreadMode != "" {
			fmt.Fprintf(&b, "thread_mode = %s\n", strconv.Quote(normalizeTelegramThreadMode(group.ThreadMode)))
		}
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

	if src.LLM.IdleTimeout != "" {
		if d, err := time.ParseDuration(src.LLM.IdleTimeout); err == nil {
			dst.LLMIdleTimeout = d
		}
	}
	if src.LLM.ContextWindowTokens != nil && *src.LLM.ContextWindowTokens >= 0 {
		dst.LLMContextWindowTokens = *src.LLM.ContextWindowTokens
	}
	if src.LLM.PromptReserveTokens != nil && *src.LLM.PromptReserveTokens >= 0 {
		dst.LLMPromptReserveTokens = *src.LLM.PromptReserveTokens
	}
	if src.LLM.MaxToolDefinitions != nil && *src.LLM.MaxToolDefinitions >= 0 {
		dst.LLMMaxToolDefinitions = *src.LLM.MaxToolDefinitions
	}
	if src.LLM.MaxToolResultChars != nil && *src.LLM.MaxToolResultChars >= 0 {
		dst.LLMMaxToolResultChars = *src.LLM.MaxToolResultChars
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
	if src.LLM.DefaultProfile != "" {
		dst.DefaultProfile = src.LLM.DefaultProfile
	}
	if dst.DefaultProfile != "" {
		for _, p := range dst.ModelProfiles {
			if p.Name == dst.DefaultProfile {
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
	if src.Browser.DefaultProfile != "" {
		dst.Browser.DefaultProfile = strings.TrimSpace(src.Browser.DefaultProfile)
	}
	if src.Browser.Profiles != nil {
		dst.Browser.Profiles = append([]BrowserProfileConfig(nil), src.Browser.Profiles...)
	}
	if src.Extensions.Dirs != nil {
		dst.ExtensionDirs = append([]string(nil), src.Extensions.Dirs...)
	}
	if src.Skills.Dirs != nil {
		dst.SkillDirs = append([]string(nil), src.Skills.Dirs...)
	}
	if src.Skills.Overrides != nil {
		dst.SkillOverrides = make(map[string]SkillOverride, len(src.Skills.Overrides))
		for key, value := range src.Skills.Overrides {
			normalizedKey := strings.ToLower(strings.TrimSpace(key))
			if normalizedKey == "" {
				continue
			}
			dst.SkillOverrides[normalizedKey] = value
		}
	}
	if src.Extensions.Allow != nil {
		dst.ExtensionAllow = append([]string(nil), src.Extensions.Allow...)
	}
	if src.Extensions.Deny != nil {
		dst.ExtensionDeny = append([]string(nil), src.Extensions.Deny...)
	}
	if src.Channels.Telegram.Enabled != nil {
		dst.Telegram.Enabled = *src.Channels.Telegram.Enabled
	}
	if src.Channels.Telegram.BotToken != "" {
		dst.Telegram.BotToken = src.Channels.Telegram.BotToken
	}
	if src.Channels.Telegram.Mode != "" {
		dst.Telegram.Mode = src.Channels.Telegram.Mode
	}
	if src.Channels.Telegram.WebhookURL != "" {
		dst.Telegram.WebhookURL = src.Channels.Telegram.WebhookURL
	}
	if src.Channels.Telegram.WebhookSecret != "" {
		dst.Telegram.WebhookSecret = src.Channels.Telegram.WebhookSecret
	}
	if src.Channels.Telegram.InboxPeerID != "" {
		dst.Telegram.InboxPeerID = strings.TrimSpace(src.Channels.Telegram.InboxPeerID)
	}
	if src.Channels.Telegram.PollTimeout != "" {
		if d, err := time.ParseDuration(src.Channels.Telegram.PollTimeout); err == nil && d > 0 {
			dst.Telegram.PollTimeout = d
		}
	}
	if src.Channels.Telegram.TextChunkLimit != nil && *src.Channels.Telegram.TextChunkLimit > 0 {
		dst.Telegram.TextChunkLimit = *src.Channels.Telegram.TextChunkLimit
	}
	if src.Channels.Telegram.TextChunkMode != "" {
		dst.Telegram.TextChunkMode = normalizeTelegramTextChunkMode(src.Channels.Telegram.TextChunkMode)
	}
	if src.Channels.Telegram.StreamQueueMode != "" {
		dst.Telegram.StreamQueueMode = normalizeTelegramStreamQueueMode(src.Channels.Telegram.StreamQueueMode)
	}
	if src.Channels.Telegram.StreamThrottle != "" {
		if d, err := time.ParseDuration(src.Channels.Telegram.StreamThrottle); err == nil && d > 0 {
			dst.Telegram.StreamThrottle = d
		}
	}
	if src.Channels.Telegram.AllowedChatIDs != nil {
		dst.Telegram.AllowedChatIDs = append([]int64(nil), src.Channels.Telegram.AllowedChatIDs...)
	}
	if src.Channels.Telegram.AllowedSenderIDs != nil {
		dst.Telegram.AllowedSenderIDs = append([]int64(nil), src.Channels.Telegram.AllowedSenderIDs...)
	}
	if src.Channels.Telegram.CommandSenderIDs != nil {
		dst.Telegram.CommandSenderIDs = append([]int64(nil), src.Channels.Telegram.CommandSenderIDs...)
	}
	if src.Channels.Telegram.ActivationMode != "" {
		dst.Telegram.ActivationMode = normalizeTelegramActivationMode(src.Channels.Telegram.ActivationMode)
	}
	if src.Channels.Telegram.ReplyActivation != nil {
		dst.Telegram.ReplyActivation = *src.Channels.Telegram.ReplyActivation
	}
	if src.Channels.Telegram.DMPolicy != "" {
		dst.Telegram.DMPolicy = src.Channels.Telegram.DMPolicy
	}
	if src.Channels.Telegram.PairingCodeTTL != "" {
		if d, err := time.ParseDuration(src.Channels.Telegram.PairingCodeTTL); err == nil && d > 0 {
			dst.Telegram.PairingCodeTTL = d
		}
	}
	if src.Channels.Telegram.ThreadMode != "" {
		dst.Telegram.ThreadMode = src.Channels.Telegram.ThreadMode
	}
	if len(src.Channels.Telegram.Groups) > 0 {
		dst.Telegram.GroupPolicies = make([]TelegramGroupPolicy, 0, len(src.Channels.Telegram.Groups))
		for _, group := range src.Channels.Telegram.Groups {
			dst.Telegram.GroupPolicies = append(dst.Telegram.GroupPolicies, TelegramGroupPolicy{
				ChatID:           group.ChatID,
				ActivationMode:   group.ActivationMode,
				AllowedSenderIDs: append([]int64(nil), group.AllowedSenderIDs...),
				CommandSenderIDs: append([]int64(nil), group.CommandSenderIDs...),
				ReplyActivation:  group.ReplyActivation,
				AllowedTopicIDs:  append([]int64(nil), group.AllowedTopicIDs...),
				ThreadMode:       group.ThreadMode,
			})
		}
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
	if src.Agent.MaxSteps != nil && *src.Agent.MaxSteps > 0 {
		dst.AgentMaxSteps = *src.Agent.MaxSteps
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
	if src.Tools.Sandbox.Profile != "" {
		dst.SandboxToolProfile = src.Tools.Sandbox.Profile
	}
	if src.Tools.Sandbox.Allow != nil {
		dst.SandboxToolsAllow = src.Tools.Sandbox.Allow
	}
	if src.Tools.Sandbox.Deny != nil {
		dst.SandboxToolsDeny = src.Tools.Sandbox.Deny
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
	if src.Tools.WebSearch.Enabled != nil {
		dst.WebSearchEnabled = *src.Tools.WebSearch.Enabled
	}
	if len(src.Tools.WebSearch.Providers) > 0 {
		providers := make([]string, 0, len(src.Tools.WebSearch.Providers))
		for _, provider := range src.Tools.WebSearch.Providers {
			trimmed := strings.ToLower(strings.TrimSpace(provider))
			if trimmed != "" {
				providers = append(providers, trimmed)
			}
		}
		if len(providers) > 0 {
			dst.WebSearchProviders = providers
		}
	}
	applyWebSearchProviderFileConfig(&dst.WebSearchBrave, src.Tools.WebSearch.Brave)
	applyWebSearchProviderFileConfig(&dst.WebSearchExa, src.Tools.WebSearch.Exa)
	applyWebSearchProviderFileConfig(&dst.WebSearchTavily, src.Tools.WebSearch.Tavily)
	if src.Tools.BrowserRun.Enabled != nil {
		dst.BrowserRun.Enabled = *src.Tools.BrowserRun.Enabled
	}
	if src.Tools.BrowserRun.AccountID != "" {
		dst.BrowserRun.AccountID = strings.TrimSpace(src.Tools.BrowserRun.AccountID)
	}
	if src.Tools.BrowserRun.APIToken != "" {
		dst.BrowserRun.APIToken = src.Tools.BrowserRun.APIToken
	}
	if src.Tools.BrowserRun.BaseURL != "" {
		dst.BrowserRun.BaseURL = strings.TrimSpace(src.Tools.BrowserRun.BaseURL)
	}
	if src.Tools.BrowserRun.DefaultTimeout != "" {
		if d, err := time.ParseDuration(src.Tools.BrowserRun.DefaultTimeout); err == nil {
			dst.BrowserRun.DefaultTimeout = d
		}
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
	if src.Hooks.Mappings != nil {
		dst.HookMappings = normalizeHookMappings(src.Hooks.Mappings)
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

func normalizeHookMappings(mappings []HookMapping) []HookMapping {
	if len(mappings) == 0 {
		return nil
	}
	normalized := make([]HookMapping, 0, len(mappings))
	for _, mapping := range mappings {
		mapping.Name = strings.TrimSpace(mapping.Name)
		mapping.Path = normalizeHookMappingPath(mapping.Path)
		mapping.Type = strings.ToLower(strings.TrimSpace(mapping.Type))
		if mapping.Path == "" {
			mapping.Path = normalizeHookMappingPath(mapping.Name)
		}
		if len(mapping.Fields) > 0 {
			fields := make([]HookFieldTransform, 0, len(mapping.Fields))
			for _, field := range mapping.Fields {
				field.To = strings.ToLower(strings.TrimSpace(field.To))
				field.From = strings.TrimSpace(field.From)
				field.Template = strings.TrimSpace(field.Template)
				fields = append(fields, field)
			}
			mapping.Fields = fields
		}
		normalized = append(normalized, mapping)
	}
	return normalized
}

func normalizeHookMappingPath(raw string) string {
	path := strings.Trim(strings.TrimSpace(raw), "/")
	return strings.ToLower(path)
}

func validHookMappingType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "message.append", "presence.set", "cron.trigger", "cron.schedule", "agent.run", "session.wake":
		return true
	default:
		return false
	}
}

func validHookMappingFieldTarget(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "peer_id", "source", "prompt", "model", "profile", "timeout_seconds", "status", "typing", "job_id", "name", "description", "enabled", "delete_after_run", "message.role", "message.content", "schedule.kind", "schedule.at", "schedule.every_ms", "schedule.expr", "schedule.tz", "schedule.stagger_ms", "payload.kind", "payload.text", "payload.message", "payload.include_history", "payload.profile", "payload.preload_urls":
		return true
	default:
		return false
	}
}

func encodeBrowserSection(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	profiles := cfg.Browser.Profiles
	defaultProfile := strings.TrimSpace(cfg.Browser.DefaultProfile)
	if defaultProfile == "" && len(profiles) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[browser]\n")
	fmt.Fprintf(&b, "default_profile = %s\n\n", strconv.Quote(defaultProfile))
	for _, profile := range profiles {
		b.WriteString("[[browser.profiles]]\n")
		fmt.Fprintf(&b, "name = %s\n", strconv.Quote(profile.Name))
		fmt.Fprintf(&b, "mode = %s\n", strconv.Quote(normalizeBrowserProfileMode(profile.Mode)))
		fmt.Fprintf(&b, "enabled = %t\n", profile.Enabled)
		if len(profile.HostAllowlist) > 0 {
			fmt.Fprintf(&b, "host_allowlist = %s\n", quoteStringSlice(profile.HostAllowlist))
		}
		if len(profile.HostDenylist) > 0 {
			fmt.Fprintf(&b, "host_denylist = %s\n", quoteStringSlice(profile.HostDenylist))
		}
		fmt.Fprintf(&b, "headless = %t\n", profile.Headless)
		fmt.Fprintf(&b, "isolated = %t\n", profile.Isolated)
		fmt.Fprintf(&b, "slim = %t\n", profile.Slim)
		if profile.Channel != "" {
			fmt.Fprintf(&b, "channel = %s\n", strconv.Quote(strings.TrimSpace(profile.Channel)))
		}
		if profile.ExecutablePath != "" {
			fmt.Fprintf(&b, "executable_path = %s\n", strconv.Quote(profile.ExecutablePath))
		}
		if profile.UserDataDir != "" {
			fmt.Fprintf(&b, "user_data_dir = %s\n", strconv.Quote(profile.UserDataDir))
		}
		if profile.Viewport != "" {
			fmt.Fprintf(&b, "viewport = %s\n", strconv.Quote(profile.Viewport))
		}
		if profile.BrowserURL != "" {
			fmt.Fprintf(&b, "browser_url = %s\n", strconv.Quote(profile.BrowserURL))
		}
		if profile.WSEndpoint != "" {
			fmt.Fprintf(&b, "ws_endpoint = %s\n", strconv.Quote(profile.WSEndpoint))
		}
		if len(profile.WSHeaders) > 0 {
			fmt.Fprintf(&b, "ws_headers = %s\n", quoteStringMap(profile.WSHeaders))
		}
		if len(profile.ChromeArgs) > 0 {
			fmt.Fprintf(&b, "chrome_args = %s\n", quoteStringSlice(profile.ChromeArgs))
		}
		if profile.AcceptInsecureCerts {
			b.WriteString("accept_insecure_certs = true\n")
		}
		if profile.ExperimentalVision {
			b.WriteString("experimental_vision = true\n")
		}
		if profile.ExperimentalScreencast {
			b.WriteString("experimental_screencast = true\n")
		}
		if profile.ExperimentalFfmpegPath != "" {
			fmt.Fprintf(&b, "experimental_ffmpeg_path = %s\n", strconv.Quote(profile.ExperimentalFfmpegPath))
		}
		if profile.PerformanceCrux != nil {
			fmt.Fprintf(&b, "performance_crux = %t\n", *profile.PerformanceCrux)
		}
		if profile.UsageStatistics != nil {
			fmt.Fprintf(&b, "usage_statistics = %t\n", *profile.UsageStatistics)
		}
		if profile.RedactNetworkHeaders != nil {
			fmt.Fprintf(&b, "redact_network_headers = %t\n", *profile.RedactNetworkHeaders)
		}
		if profile.MCPCommand != "" {
			fmt.Fprintf(&b, "mcp_command = %s\n", strconv.Quote(profile.MCPCommand))
		}
		if profile.MCPPackage != "" {
			fmt.Fprintf(&b, "mcp_package = %s\n", strconv.Quote(profile.MCPPackage))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func resolveRelativePaths(cfg *Config, root string) {
	cfg.WorkspaceRoot = makeAbs(root, cfg.WorkspaceRoot)
	for i, dir := range cfg.ExtensionDirs {
		cfg.ExtensionDirs[i] = makeAbs(root, dir)
	}
	for i, dir := range cfg.SkillDirs {
		cfg.SkillDirs[i] = makeAbs(root, dir)
	}
	for i := range cfg.Browser.Profiles {
		cfg.Browser.Profiles[i].UserDataDir = makeAbs(root, cfg.Browser.Profiles[i].UserDataDir)
		cfg.Browser.Profiles[i].ExecutablePath = makeAbs(root, cfg.Browser.Profiles[i].ExecutablePath)
		cfg.Browser.Profiles[i].ExperimentalFfmpegPath = makeAbs(root, cfg.Browser.Profiles[i].ExperimentalFfmpegPath)
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

// SkillSearchPaths returns skill discovery roots in deterministic precedence order.
// Later entries override earlier ones when the same skill id is defined multiple times.
func (c *Config) SkillSearchPaths(projectRoot string) []SkillSearchPath {
	paths := []SkillSearchPath{
		{Kind: "bundled", Path: filepath.Join(projectRoot, "internal", "skills", "bundled"), Rank: 10},
		{Kind: "managed", Path: c.ManagedSkillsDir(), Rank: 20},
	}
	for _, dir := range c.SkillDirs {
		if strings.TrimSpace(dir) != "" {
			paths = append(paths, SkillSearchPath{Kind: "extra", Path: dir, Rank: 30})
		}
	}
	if dir := c.PersonalSkillsDir(); strings.TrimSpace(dir) != "" {
		paths = append(paths, SkillSearchPath{Kind: "personal", Path: dir, Rank: 40})
	}
	if strings.TrimSpace(projectRoot) != "" {
		paths = append(paths, SkillSearchPath{Kind: "project", Path: filepath.Join(projectRoot, "skills"), Rank: 50})
	}
	paths = append(paths, SkillSearchPath{Kind: "workspace", Path: c.WorkspaceSkillsDir(), Rank: 60})
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

func quoteStringMap(values map[string]string) string {
	if len(values) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, strconv.Quote(strings.TrimSpace(key))+" = "+strconv.Quote(values[key]))
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func normalizeBrowserProfileMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "managed":
		return "managed"
	case "auto_connect", "existing_session":
		return "existing_session"
	case "browser_url":
		return "browser_url"
	case "ws_endpoint":
		return "ws_endpoint"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
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

func quoteInt64Slice(values []int64) string {
	if len(values) == 0 {
		return ""
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.FormatInt(value, 10))
	}
	return strings.Join(parts, ", ")
}

func normalizeTelegramActivationMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "inherit":
		return "always"
	case "mention", "always":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func normalizeTelegramDMPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", "open":
		return "open"
	case "pairing", "closed":
		return strings.ToLower(strings.TrimSpace(policy))
	default:
		return strings.ToLower(strings.TrimSpace(policy))
	}
}

func normalizeTelegramTextChunkMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "paragraph":
		return "paragraph"
	case "word", "hard":
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func normalizeTelegramStreamQueueMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "burst":
		return "burst"
	case "throttle":
		return "throttle"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func normalizeTelegramThreadMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "topic":
		return "topic"
	case "chat":
		return "chat"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
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
	if strings.TrimSpace(cfg.DefaultProfile) == "" {
		return fmt.Errorf("llm.default_profile is required")
	}
	if len(cfg.ModelProfiles) == 0 {
		return fmt.Errorf("llm.profiles must contain at least one profile")
	}
	selectedProfile := false
	for i, profile := range cfg.ModelProfiles {
		if strings.TrimSpace(profile.Name) == "" {
			return fmt.Errorf("llm.profiles[%d].name must not be empty", i)
		}
		if strings.TrimSpace(profile.Provider) == "" {
			return fmt.Errorf("llm.profiles[%d].provider must not be empty", i)
		}
		if !IsSupportedLLMProvider(profile.Provider) {
			return fmt.Errorf("unsupported llm.profiles[%d].provider %q: must be one of %s", i, profile.Provider, SupportedLLMProvidersHint())
		}
		if strings.TrimSpace(profile.Model) == "" {
			return fmt.Errorf("llm.profiles[%d].model must not be empty", i)
		}
		if profile.Name == cfg.DefaultProfile {
			selectedProfile = true
		}
	}
	if !selectedProfile {
		return fmt.Errorf("llm.default_profile %q must match one of llm.profiles.name", cfg.DefaultProfile)
	}
	if cfg.Model == "" {
		return fmt.Errorf("llm.model is required")
	}
	if cfg.LLMIdleTimeout < 0 {
		return fmt.Errorf("llm.idle_timeout must be >= 0")
	}
	if cfg.LLMContextWindowTokens < 0 {
		return fmt.Errorf("llm.context_window_tokens must be >= 0")
	}
	if cfg.LLMPromptReserveTokens < 0 {
		return fmt.Errorf("llm.prompt_reserve_tokens must be >= 0")
	}
	if cfg.LLMContextWindowTokens > 0 && cfg.LLMPromptReserveTokens >= cfg.LLMContextWindowTokens {
		return fmt.Errorf("llm.prompt_reserve_tokens must be < llm.context_window_tokens when budgeting is enabled")
	}
	if cfg.LLMMaxToolDefinitions < 0 {
		return fmt.Errorf("llm.max_tool_definitions must be >= 0")
	}
	if cfg.LLMMaxToolResultChars < 0 {
		return fmt.Errorf("llm.max_tool_result_chars must be >= 0")
	}
	if !IsSupportedLLMProvider(cfg.Provider) {
		return fmt.Errorf("unsupported llm.provider %q: must be one of %s", cfg.Provider, SupportedLLMProvidersHint())
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
	if cfg.AgentMaxSteps < 1 {
		return fmt.Errorf("agent.max_steps must be >= 1")
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
	case "", "full", "coding", "messaging", "minimal", "sandbox":
	default:
		return fmt.Errorf("tools.profile must be one of full, coding, messaging, minimal, or sandbox")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.SandboxToolProfile)) {
	case "", "full", "coding", "messaging", "minimal", "sandbox":
	default:
		return fmt.Errorf("tools.sandbox.profile must be one of full, coding, messaging, minimal, or sandbox")
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
	for i, pattern := range cfg.ExecCustomAllowPatterns {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("tools.exec.custom_allow_patterns[%d] is not a valid regex: %w", i, err)
		}
	}
	for i, pattern := range cfg.ExecCustomDenyPatterns {
		if _, err := regexp.Compile(pattern); err != nil {
			return fmt.Errorf("tools.exec.custom_deny_patterns[%d] is not a valid regex: %w", i, err)
		}
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
	if len(cfg.WebSearchProviders) == 0 {
		return fmt.Errorf("tools.web_search.providers must not be empty")
	}
	seenWebSearchProviders := make(map[string]struct{}, len(cfg.WebSearchProviders))
	for i, provider := range cfg.WebSearchProviders {
		if strings.TrimSpace(provider) == "" {
			return fmt.Errorf("tools.web_search.providers[%d] must not be empty", i)
		}
		if !IsSupportedWebSearchProvider(provider) {
			return fmt.Errorf("unsupported tools.web_search.providers[%d] %q: must be one of %s", i, provider, SupportedWebSearchProvidersHint())
		}
		if _, exists := seenWebSearchProviders[provider]; exists {
			return fmt.Errorf("tools.web_search.providers[%d] duplicates provider %q", i, provider)
		}
		seenWebSearchProviders[provider] = struct{}{}
	}
	for _, provider := range []struct {
		name string
		cfg  RuntimeWebSearchProviderConfig
	}{
		{name: "brave", cfg: cfg.WebSearchBrave},
		{name: "exa", cfg: cfg.WebSearchExa},
		{name: "tavily", cfg: cfg.WebSearchTavily},
	} {
		if provider.cfg.DefaultTimeout <= 0 {
			return fmt.Errorf("tools.web_search.%s.default_timeout must be > 0", provider.name)
		}
		if strings.TrimSpace(provider.cfg.BaseURL) == "" {
			return fmt.Errorf("tools.web_search.%s.base_url must not be empty", provider.name)
		}
		parsedURL, err := url.ParseRequestURI(provider.cfg.BaseURL)
		if err != nil {
			return fmt.Errorf("tools.web_search.%s.base_url must be a valid absolute URL: %w", provider.name, err)
		}
		if parsedURL.Scheme == "" || parsedURL.Host == "" {
			return fmt.Errorf("tools.web_search.%s.base_url must be a valid absolute URL", provider.name)
		}
	}
	if cfg.BrowserRun.DefaultTimeout <= 0 {
		return fmt.Errorf("tools.browser_run.default_timeout must be > 0")
	}
	if strings.TrimSpace(cfg.BrowserRun.BaseURL) == "" {
		return fmt.Errorf("tools.browser_run.base_url must not be empty")
	}
	parsedBrowserRunURL, err := url.ParseRequestURI(cfg.BrowserRun.BaseURL)
	if err != nil {
		return fmt.Errorf("tools.browser_run.base_url must be a valid absolute URL: %w", err)
	}
	if parsedBrowserRunURL.Scheme == "" || parsedBrowserRunURL.Host == "" {
		return fmt.Errorf("tools.browser_run.base_url must be a valid absolute URL")
	}
	if cfg.BrowserRun.Enabled {
		if strings.TrimSpace(cfg.BrowserRun.AccountID) == "" {
			return fmt.Errorf("tools.browser_run.account_id must not be empty when enabled")
		}
		if strings.TrimSpace(cfg.BrowserRun.APIToken) == "" {
			return fmt.Errorf("tools.browser_run.api_token must not be empty when enabled")
		}
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
	for i, dir := range cfg.SkillDirs {
		if strings.TrimSpace(dir) == "" {
			return fmt.Errorf("skills.dirs[%d] must not be empty", i)
		}
	}
	for key, override := range cfg.SkillOverrides {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("skills.overrides contains an empty key")
		}
		for index, agent := range override.Agents {
			if strings.TrimSpace(agent) == "" {
				return fmt.Errorf("skills.overrides.%s.agents[%d] must not be empty", key, index)
			}
		}
		for index, command := range override.Commands {
			if strings.TrimSpace(command.Name) == "" {
				return fmt.Errorf("skills.overrides.%s.commands[%d].name must not be empty", key, index)
			}
		}
	}
	if len(cfg.HookMappings) > 0 && strings.TrimSpace(cfg.WebhookToken) == "" {
		return fmt.Errorf("hooks.webhook_token is required when hooks.mappings are configured")
	}
	hookMappingNames := make(map[string]struct{}, len(cfg.HookMappings))
	hookMappingPaths := make(map[string]struct{}, len(cfg.HookMappings))
	for i, mapping := range cfg.HookMappings {
		if strings.TrimSpace(mapping.Name) == "" {
			return fmt.Errorf("hooks.mappings[%d].name is required", i)
		}
		if _, exists := hookMappingNames[strings.ToLower(mapping.Name)]; exists {
			return fmt.Errorf("hooks.mappings[%d].name %q is duplicated", i, mapping.Name)
		}
		hookMappingNames[strings.ToLower(mapping.Name)] = struct{}{}
		if !validHookMappingType(mapping.Type) {
			return fmt.Errorf("hooks.mappings[%d].type %q is unsupported", i, mapping.Type)
		}
		path := normalizeHookMappingPath(mapping.Path)
		if path == "" {
			path = normalizeHookMappingPath(mapping.Name)
		}
		if path == "" {
			return fmt.Errorf("hooks.mappings[%d].path resolved empty", i)
		}
		if strings.Contains(path, "/") {
			return fmt.Errorf("hooks.mappings[%d].path must be a single path segment", i)
		}
		if _, exists := hookMappingPaths[path]; exists {
			return fmt.Errorf("hooks.mappings[%d].path %q is duplicated", i, path)
		}
		hookMappingPaths[path] = struct{}{}
		for j, field := range mapping.Fields {
			if !validHookMappingFieldTarget(field.To) {
				return fmt.Errorf("hooks.mappings[%d].fields[%d].to %q is unsupported", i, j, field.To)
			}
			sourceCount := 0
			if strings.TrimSpace(field.From) != "" {
				sourceCount++
			}
			if field.Value != "" {
				sourceCount++
			}
			if strings.TrimSpace(field.Template) != "" {
				sourceCount++
			}
			if sourceCount != 1 {
				return fmt.Errorf("hooks.mappings[%d].fields[%d] must set exactly one of from, value, or template", i, j)
			}
		}
	}
	if strings.TrimSpace(cfg.Browser.DefaultProfile) != "" {
		selected := false
		for i, profile := range cfg.Browser.Profiles {
			if strings.TrimSpace(profile.Name) == "" {
				return fmt.Errorf("browser.profiles[%d].name must not be empty", i)
			}
			if strings.EqualFold(profile.Name, cfg.Browser.DefaultProfile) {
				selected = true
			}
		}
		if !selected {
			return fmt.Errorf("browser.default_profile %q must match one of browser.profiles.name", cfg.Browser.DefaultProfile)
		}
	}
	browserNames := make(map[string]struct{}, len(cfg.Browser.Profiles))
	for i, profile := range cfg.Browser.Profiles {
		name := strings.TrimSpace(profile.Name)
		if name == "" {
			return fmt.Errorf("browser.profiles[%d].name must not be empty", i)
		}
		if _, exists := browserNames[strings.ToLower(name)]; exists {
			return fmt.Errorf("browser.profiles[%d].name %q is duplicated", i, name)
		}
		browserNames[strings.ToLower(name)] = struct{}{}
		switch normalizeBrowserProfileMode(profile.Mode) {
		case "managed", "existing_session", "browser_url", "ws_endpoint":
		default:
			return fmt.Errorf("browser.profiles[%d].mode must be one of managed, existing_session, browser_url, or ws_endpoint", i)
		}
		if strings.TrimSpace(profile.Channel) != "" {
			switch strings.ToLower(strings.TrimSpace(profile.Channel)) {
			case "stable", "beta", "dev", "canary":
			default:
				return fmt.Errorf("browser.profiles[%d].channel must be one of stable, beta, dev, or canary", i)
			}
		}
		for hostIndex, host := range profile.HostAllowlist {
			if strings.TrimSpace(host) == "" {
				return fmt.Errorf("browser.profiles[%d].host_allowlist[%d] must not be empty", i, hostIndex)
			}
		}
		for hostIndex, host := range profile.HostDenylist {
			if strings.TrimSpace(host) == "" {
				return fmt.Errorf("browser.profiles[%d].host_denylist[%d] must not be empty", i, hostIndex)
			}
		}
		switch normalizeBrowserProfileMode(profile.Mode) {
		case "existing_session":
			if strings.TrimSpace(profile.UserDataDir) == "" && strings.TrimSpace(profile.ExecutablePath) == "" {
				return fmt.Errorf("browser.profiles[%d] in existing_session mode should set user_data_dir or executable_path", i)
			}
		case "browser_url":
			if strings.TrimSpace(profile.BrowserURL) == "" {
				return fmt.Errorf("browser.profiles[%d].browser_url is required in browser_url mode", i)
			}
			if _, err := url.ParseRequestURI(profile.BrowserURL); err != nil {
				return fmt.Errorf("browser.profiles[%d].browser_url is invalid: %w", i, err)
			}
		case "ws_endpoint":
			if strings.TrimSpace(profile.WSEndpoint) == "" {
				return fmt.Errorf("browser.profiles[%d].ws_endpoint is required in ws_endpoint mode", i)
			}
			if _, err := url.ParseRequestURI(profile.WSEndpoint); err != nil {
				return fmt.Errorf("browser.profiles[%d].ws_endpoint is invalid: %w", i, err)
			}
		}
		if strings.TrimSpace(profile.MCPCommand) == "" {
			profile.MCPCommand = "npx"
		}
		if strings.TrimSpace(profile.MCPPackage) == "" {
			profile.MCPPackage = "chrome-devtools-mcp@latest"
		}
	}
	if cfg.Telegram.Enabled {
		if strings.TrimSpace(cfg.Telegram.BotToken) == "" {
			return fmt.Errorf("channels.telegram.bot_token is required when Telegram is enabled")
		}
		switch strings.ToLower(strings.TrimSpace(cfg.Telegram.Mode)) {
		case "", "polling", "webhook":
		default:
			return fmt.Errorf("channels.telegram.mode must be one of polling or webhook")
		}
		if strings.EqualFold(strings.TrimSpace(cfg.Telegram.Mode), "webhook") && strings.TrimSpace(cfg.Telegram.WebhookURL) == "" {
			return fmt.Errorf("channels.telegram.webhook_url is required in webhook mode")
		}
		if inboxPeerID := strings.TrimSpace(cfg.Telegram.InboxPeerID); inboxPeerID != "" && !isValidPeerID(inboxPeerID) {
			return fmt.Errorf("channels.telegram.inbox_peer_id must be a valid peer id")
		}
		if cfg.Telegram.PollTimeout <= 0 {
			return fmt.Errorf("channels.telegram.poll_timeout must be > 0")
		}
		if cfg.Telegram.TextChunkLimit < 1 {
			return fmt.Errorf("channels.telegram.text_chunk_limit must be >= 1")
		}
		switch normalizeTelegramTextChunkMode(cfg.Telegram.TextChunkMode) {
		case "paragraph", "word", "hard":
		default:
			return fmt.Errorf("channels.telegram.text_chunk_mode must be one of paragraph, word, or hard")
		}
		switch normalizeTelegramStreamQueueMode(cfg.Telegram.StreamQueueMode) {
		case "burst", "throttle":
		default:
			return fmt.Errorf("channels.telegram.stream_queue_mode must be one of burst or throttle")
		}
		if cfg.Telegram.StreamThrottle <= 0 {
			return fmt.Errorf("channels.telegram.stream_throttle must be > 0")
		}
		switch normalizeTelegramActivationMode(cfg.Telegram.ActivationMode) {
		case "mention", "always":
		default:
			return fmt.Errorf("channels.telegram.activation_mode must be one of mention or always")
		}
		switch normalizeTelegramDMPolicy(cfg.Telegram.DMPolicy) {
		case "open", "pairing", "closed":
		default:
			return fmt.Errorf("channels.telegram.dm_policy must be one of open, pairing, or closed")
		}
		switch normalizeTelegramThreadMode(cfg.Telegram.ThreadMode) {
		case "topic", "chat":
		default:
			return fmt.Errorf("channels.telegram.thread_mode must be one of topic or chat")
		}
		if cfg.Telegram.PairingCodeTTL <= 0 {
			return fmt.Errorf("channels.telegram.pairing_code_ttl must be > 0")
		}
		for i, group := range cfg.Telegram.GroupPolicies {
			if group.ChatID == 0 {
				return fmt.Errorf("channels.telegram.groups[%d].chat_id must not be 0", i)
			}
			switch normalizeTelegramActivationMode(group.ActivationMode) {
			case "", "mention", "always":
			default:
				return fmt.Errorf("channels.telegram.groups[%d].activation_mode must be one of mention or always", i)
			}
			switch normalizeTelegramThreadMode(group.ThreadMode) {
			case "topic", "chat":
			default:
				if strings.TrimSpace(group.ThreadMode) != "" {
					return fmt.Errorf("channels.telegram.groups[%d].thread_mode must be one of topic or chat", i)
				}
			}
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

func isValidPeerID(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '@' || c == ':' {
			continue
		}
		return false
	}
	return true
}
