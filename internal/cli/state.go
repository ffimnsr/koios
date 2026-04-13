package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/config"
)

type repoState struct {
	Root         string
	ConfigPath   string
	ConfigExists bool
	Config       *config.Config

	ListenAddr               string
	Provider                 string
	APIKey                   string
	Model                    string
	BaseURL                  string
	SessionRetention         time.Duration
	SessionMaxEntries        int
	SessionIdleResetAfter    time.Duration
	SessionDailyResetTime    string
	HeartbeatEnabled         bool
	HeartbeatEvery           time.Duration
	RequestTimeout           time.Duration
	MaxSessionMessages       int
	CompactThreshold         int
	CompactReserve           int
	CronMaxConcurrent        int
	AgentMaxChildren         int
	AgentRetryAttempts       int
	AgentRetryInitialBackoff time.Duration
	AgentRetryMaxBackoff     time.Duration
	SessionPruneKeepToolMsgs int
	MemoryInject             bool
	MemoryTopK               int
	WorkspaceRoot            string
	WorkspacePerAgent        bool
	WorkspaceMaxBytes        int
}

type doctorFinding struct {
	Level   string `json:"level"`
	Key     string `json:"key,omitempty"`
	Message string `json:"message"`
}

func resolveRepoState(cwd string) (*repoState, error) {
	root, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	configPath := filepath.Join(root, config.DefaultConfigFile)
	cfg, exists, err := config.LoadOptionalFromPath(configPath)
	if err != nil {
		return nil, err
	}
	rs := &repoState{
		Root:                     root,
		ConfigPath:               configPath,
		ConfigExists:             exists,
		Config:                   cfg,
		ListenAddr:               cfg.ListenAddr,
		Provider:                 cfg.Provider,
		APIKey:                   cfg.APIKey,
		Model:                    cfg.Model,
		BaseURL:                  cfg.BaseURL,
		SessionRetention:         cfg.SessionRetention,
		SessionMaxEntries:        cfg.SessionMaxEntries,
		SessionIdleResetAfter:    cfg.SessionIdleResetAfter,
		SessionDailyResetTime:    cfg.SessionDailyResetTime,
		HeartbeatEnabled:         cfg.HeartbeatEnabled,
		HeartbeatEvery:           cfg.HeartbeatEvery,
		RequestTimeout:           cfg.RequestTimeout,
		MaxSessionMessages:       cfg.MaxSessionMessages,
		CompactThreshold:         cfg.CompactThreshold,
		CompactReserve:           cfg.CompactReserve,
		CronMaxConcurrent:        cfg.CronMaxConcurrentRuns,
		AgentMaxChildren:         cfg.AgentMaxChildren,
		AgentRetryAttempts:       cfg.AgentRetryAttempts,
		AgentRetryInitialBackoff: cfg.AgentRetryInitialBackoff,
		AgentRetryMaxBackoff:     cfg.AgentRetryMaxBackoff,
		SessionPruneKeepToolMsgs: cfg.SessionPruneKeepToolMessages,
		MemoryInject:             cfg.MemoryInject,
		MemoryTopK:               cfg.MemoryTopK,
		WorkspaceRoot:            cfg.WorkspaceRoot,
		WorkspacePerAgent:        cfg.WorkspacePerAgent,
		WorkspaceMaxBytes:        cfg.WorkspaceMaxBytes,
	}
	return rs, nil
}

func (s *repoState) baseHTTPURL() string {
	addr := s.ListenAddr
	switch {
	case strings.HasPrefix(addr, "http://"), strings.HasPrefix(addr, "https://"):
		return strings.TrimRight(addr, "/")
	case strings.HasPrefix(addr, ":"):
		return "http://127.0.0.1" + addr
	default:
		return "http://" + addr
	}
}

func (s *repoState) baseWSURL() string {
	base := s.baseHTTPURL()
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://")
	}
	return "ws://" + strings.TrimPrefix(base, "http://")
}

func (s *repoState) validate() []doctorFinding {
	findings := []doctorFinding{}
	if !s.ConfigExists {
		findings = append(findings, doctorFinding{Level: "warn", Key: "config", Message: fmt.Sprintf("%s is missing", config.DefaultConfigFile)})
	}
	if s.Model == "" {
		findings = append(findings, doctorFinding{Level: "error", Key: "llm.model", Message: "llm.model is required"})
	}
	switch s.Provider {
	case "openai", "anthropic", "openrouter", "nvidia":
	default:
		findings = append(findings, doctorFinding{Level: "error", Key: "llm.provider", Message: "unsupported provider"})
	}
	if s.APIKey == "" {
		findings = append(findings, doctorFinding{Level: "warn", Key: "llm.api_key", Message: "llm.api_key is empty"})
	}
	dirs := map[string]string{
		"workspace.root": s.WorkspaceRoot,
	}
	for key, path := range dirs {
		if path == "" {
			continue
		}
		parent := filepath.Dir(path)
		if !fileExists(path) && !dirExists(path) && !dirExists(parent) {
			findings = append(findings, doctorFinding{Level: "warn", Key: key, Message: fmt.Sprintf("path does not exist yet: %s", path)})
		}
	}
	if s.SessionMaxEntries < 0 {
		findings = append(findings, doctorFinding{Level: "error", Key: "session.max_entries", Message: "session.max_entries must be >= 0"})
	}
	return findings
}

func (s *repoState) createStateDirs() []string {
	created := []string{}
	if s.WorkspaceRoot != "" {
		if err := os.MkdirAll(s.WorkspaceRoot, 0o755); err == nil {
			created = append(created, s.WorkspaceRoot)
		}
	}
	return created
}

// sessionDir returns the derived session storage path.
func (s *repoState) sessionDir() string {
	if s.Config == nil || s.WorkspaceRoot == "" {
		return ""
	}
	return s.Config.SessionDir()
}

// cronDir returns the derived cron/scheduler storage path.
func (s *repoState) cronDir() string {
	if s.Config == nil || s.WorkspaceRoot == "" {
		return ""
	}
	return s.Config.CronDir()
}

// agentDir returns the derived subagent registry storage path.
func (s *repoState) agentDir() string {
	if s.Config == nil || s.WorkspaceRoot == "" {
		return ""
	}
	return s.Config.AgentDir()
}

// memoryDBPath returns the derived memory database path.
func (s *repoState) memoryDBPath() string {
	if s.Config == nil || s.WorkspaceRoot == "" {
		return ""
	}
	return s.Config.MemoryDBPath()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func decodeSessionName(fileName string) string {
	name := strings.TrimSuffix(fileName, filepath.Ext(fileName))
	decoded, err := url.PathUnescape(name)
	if err != nil {
		return name
	}
	return decoded
}
