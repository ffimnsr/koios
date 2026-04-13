package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	// ListenAddr is the TCP address the HTTP server binds to (e.g. ":8080").
	ListenAddr string
	// Provider selects the LLM backend: "openai", "anthropic", or "openrouter".
	Provider string
	// APIKey is the LLM provider API key.
	APIKey string
	// Model is the model name passed to the provider.
	Model string
	// BaseURL overrides the provider base URL (useful for local proxies / testing).
	BaseURL string
	// MaxSessionMessages is the maximum number of messages kept per peer session.
	// Oldest non-system messages are pruned when the limit is exceeded.
	MaxSessionMessages int
	// RequestTimeout is the maximum time allowed for a single LLM round-trip.
	RequestTimeout time.Duration

	// ── Phase 1: JSONL session persistence ──────────────────────────────────

	// SessionDir, if non-empty, enables JSONL persistence. Each peer session is
	// stored as <SessionDir>/<url-escaped-peerID>.jsonl.
	SessionDir string

	// ── Phase 2: LLM-based context compaction ───────────────────────────────

	// CompactThreshold is the number of stored messages that triggers LLM
	// summarization. 0 disables compaction (naive pruning is used instead).
	CompactThreshold int
	// CompactReserve is the number of most-recent messages kept verbatim after
	// compaction; the rest are fed to the summarizer.
	CompactReserve int

	// ── Phase 3: Long-term semantic memory ──────────────────────────────────

	// MemoryDBPath, if non-empty, enables the SQLite long-term memory store at
	// the given file path.
	MemoryDBPath string
	// EmbedModel is the embedding model used for semantic memory search.
	// Defaults to "text-embedding-3-small". Requires an OpenAI-compatible
	// /v1/embeddings endpoint; falls back to BM25-only if unavailable.
	EmbedModel string
	// MemoryInject, when true, prepends top-K memory hits as a system message
	// on every chat and agent.run request.
	MemoryInject bool
	// MemoryTopK is the maximum number of memory chunks injected per request.
	MemoryTopK int
	// SessionPruneKeepToolMessages is the number of recent tool-related messages
	// kept in the model-visible request context. Older tool chatter stays in the
	// transcript but is pruned before inference.
	SessionPruneKeepToolMessages int

	// ── Phase 4: Cron scheduler and heartbeat ────────────────────────────────

	// CronDir, if non-empty, enables the cron scheduler. Job definitions are
	// stored at <CronDir>/jobs.json and run logs at <CronDir>/runs/<jobID>.jsonl.
	// Heartbeat config per peer lives at <CronDir>/heartbeat/<peerID>.json.
	// When empty, both the cron scheduler and heartbeats are disabled.
	CronDir string
	// CronMaxConcurrentRuns is the maximum number of cron jobs that may execute
	// simultaneously. Defaults to 1.
	CronMaxConcurrentRuns int
	// HeartbeatEvery is the global default interval between heartbeat runs.
	// Individual peers can override this via PUT /v1/heartbeat. Defaults to 30m.
	HeartbeatEvery time.Duration
	// HeartbeatEnabled globally enables or disables heartbeat runs. When false,
	// no heartbeat goroutines are started regardless of peer activity.
	HeartbeatEnabled bool

	// ── Phase 5: Agent runtime and subagent orchestration ─────────────────────

	// AgentDir, when non-empty, persists subagent run records to disk.
	AgentDir string
	// AgentMaxChildren limits concurrent child runs spawned from one parent.
	AgentMaxChildren int
	// AgentRetryAttempts controls how many provider attempts are made before failing.
	AgentRetryAttempts int
	// AgentRetryInitialBackoff is the initial delay between retries.
	AgentRetryInitialBackoff time.Duration
	// AgentRetryMaxBackoff caps the retry backoff.
	AgentRetryMaxBackoff time.Duration

	// ── Security ────────────────────────────────────────────────────────────────

	// AllowedOrigins is a comma-separated list of origins that are permitted to
	// open a WebSocket connection.  An empty value allows all origins (suitable
	// for environments behind a trusted reverse proxy).  Each entry is matched
	// against the request Origin header as an exact, case-insensitive string.
	AllowedOrigins []string
}

// Load reads configuration from environment variables and validates it.
// Before reading env vars it attempts to load a .env file from the current
// working directory. Variables already present in the environment take
// precedence over values in the file.
func Load() (*Config, error) {
	loadDotEnv(".env")
	cfg := &Config{
		ListenAddr:         getEnv("LISTEN_ADDR", ":8080"),
		Provider:           getEnv("LLM_PROVIDER", "openai"),
		APIKey:             os.Getenv("LLM_API_KEY"),
		Model:              os.Getenv("LLM_MODEL"),
		BaseURL:            os.Getenv("LLM_BASE_URL"),
		MaxSessionMessages: getEnvInt("SESSION_MAX_MESSAGES", 100),
		RequestTimeout:     getEnvDuration("REQUEST_TIMEOUT", 2*time.Minute),
		// Phase 1
		SessionDir: os.Getenv("SESSION_DIR"),
		// Phase 2
		CompactThreshold: getEnvInt("COMPACTION_THRESHOLD", 0),
		CompactReserve:   getEnvInt("COMPACTION_RESERVE", 20),
		// Phase 3
		MemoryDBPath:                 os.Getenv("MEMORY_DB_PATH"),
		EmbedModel:                   getEnv("EMBED_MODEL", "text-embedding-3-small"),
		MemoryInject:                 getEnvBool("MEMORY_INJECT", false),
		MemoryTopK:                   getEnvInt("MEMORY_TOP_K", 3),
		SessionPruneKeepToolMessages: getEnvInt("SESSION_PRUNE_KEEP_TOOL_MESSAGES", 8),
		// Phase 4: cron + heartbeat (disabled when CronDir is empty)
		CronDir:               os.Getenv("CRON_DIR"),
		CronMaxConcurrentRuns: getEnvInt("CRON_MAX_CONCURRENT", 1),
		HeartbeatEvery:        getEnvDuration("HEARTBEAT_EVERY", 30*time.Minute),
		HeartbeatEnabled:      getEnvBool("HEARTBEAT_ENABLED", true),
		// Phase 5: agent runtime and subagents
		AgentDir:                 os.Getenv("AGENT_DIR"),
		AgentMaxChildren:         getEnvInt("AGENT_MAX_CHILDREN", 4),
		AgentRetryAttempts:       getEnvInt("AGENT_RETRY_ATTEMPTS", 3),
		AgentRetryInitialBackoff: getEnvDuration("AGENT_RETRY_INITIAL_BACKOFF", 500*time.Millisecond),
		AgentRetryMaxBackoff:     getEnvDuration("AGENT_RETRY_MAX_BACKOFF", 5*time.Second),
		// Security
		AllowedOrigins: parseCSV(os.Getenv("WS_ALLOWED_ORIGINS")),
	}

	if cfg.APIKey == "" {
		return nil, fmt.Errorf("LLM_API_KEY is required")
	}
	if cfg.Model == "" {
		return nil, fmt.Errorf("LLM_MODEL is required")
	}
	switch cfg.Provider {
	case "openai", "anthropic", "openrouter", "nvidia":
	default:
		return nil, fmt.Errorf("unsupported LLM_PROVIDER %q: must be openai, anthropic, openrouter, or nvidia", cfg.Provider)
	}

	return cfg, nil
}

// parseCSV splits a comma-separated string into a trimmed, non-empty slice.
// Returns nil when the input is blank so callers can distinguish "unset" from
// "set to empty".
func parseCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

// loadDotEnv reads KEY=VALUE pairs from path and sets them via os.Setenv,
// skipping keys that are already present in the environment.
// The file is optional — a missing file is silently ignored.
// Supported syntax:
//   - blank lines and lines starting with # are ignored
//   - optional export prefix is stripped (export KEY=VALUE)
//   - values may be single- or double-quoted; quotes are stripped
//   - inline comments (#) are not supported inside values
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return // missing file is fine
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip optional "export " prefix.
		line = strings.TrimPrefix(line, "export ")
		eq := strings.IndexByte(line, '=')
		if eq < 1 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// Strip matching surrounding quotes.
		if len(val) >= 2 {
			if (val[0] == '\'' && val[len(val)-1] == '\'') ||
				(val[0] == '"' && val[len(val)-1] == '"') {
				val = val[1 : len(val)-1]
			}
		}
		// Real env vars take precedence over the file.
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}
