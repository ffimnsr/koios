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
	Root            string
	ConfigPath      string
	ConfigExists    bool
	Config          *config.Config
	ConfigLoadError string

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
	MilvusEnabled            bool
	MilvusURL                string
	MilvusCollection         string
	ExecIsolationEnabled     bool
	CodeExecutionEnabled     bool
	WorkspaceRoot            string
	WorkspacePerAgent        bool
	WorkspaceMaxBytes        int
	MCPServers               []config.MCPServerConfig
}

type doctorFinding struct {
	Level      string `json:"level"`
	Key        string `json:"key,omitempty"`
	Message    string `json:"message"`
	Hint       string `json:"hint,omitempty"`
	Path       string `json:"path,omitempty"`
	Repairable bool   `json:"repairable,omitempty"`
}

func resolveRepoState(cwd string) (*repoState, error) {
	return resolveRepoStateForDoctor(cwd, false)
}

func resolveDoctorState(cwd string) (*repoState, error) {
	return resolveRepoStateForDoctor(cwd, true)
}

func resolveRepoStateForDoctor(cwd string, allowConfigError bool) (*repoState, error) {
	root, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve root: %w", err)
	}
	configPath := filepath.Join(root, config.DefaultConfigFile)
	cfg, exists, err := config.LoadOptionalFromPath(configPath)
	if err != nil {
		if !allowConfigError {
			return nil, err
		}
		cfg = config.Default()
		exists = fileExists(configPath)
	}
	if cfg == nil {
		cfg = config.Default()
	}
	if cfg.WorkspaceRoot != "" && !filepath.IsAbs(cfg.WorkspaceRoot) {
		cfg.WorkspaceRoot = filepath.Join(root, cfg.WorkspaceRoot)
	}
	rs := &repoState{
		Root:                     root,
		ConfigPath:               configPath,
		ConfigExists:             exists,
		Config:                   cfg,
		ConfigLoadError:          errorString(err),
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
		MilvusEnabled:            cfg.MilvusEnabled,
		MilvusURL:                cfg.MilvusURL,
		MilvusCollection:         cfg.MilvusCollection,
		ExecIsolationEnabled:     cfg.ExecIsolationEnabled,
		CodeExecutionEnabled:     cfg.CodeExecutionEnabled,
		WorkspaceRoot:            cfg.WorkspaceRoot,
		WorkspacePerAgent:        cfg.WorkspacePerAgent,
		WorkspaceMaxBytes:        cfg.WorkspaceMaxBytes,
		MCPServers:               append([]config.MCPServerConfig(nil), cfg.MCPServers...),
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
		findings = append(findings, doctorFinding{Level: "warn", Key: "config", Message: fmt.Sprintf("%s is missing", config.DefaultConfigFile), Path: s.ConfigPath, Hint: "run koios init or koios doctor --repair to generate a starter config", Repairable: true})
	}
	if s.ConfigLoadError != "" {
		findings = append(findings, doctorFinding{Level: "error", Key: "config.parse", Message: s.ConfigLoadError, Path: s.ConfigPath, Hint: "fix the TOML syntax or regenerate the file with koios init"})
		return findings
	}
	if s.Model == "" {
		findings = append(findings, doctorFinding{Level: "error", Key: "llm.model", Message: "llm.model is required", Path: s.ConfigPath})
	}
	switch s.Provider {
	case "openai", "anthropic", "openrouter", "nvidia":
	default:
		findings = append(findings, doctorFinding{Level: "error", Key: "llm.provider", Message: fmt.Sprintf("unsupported provider %q", s.Provider), Path: s.ConfigPath, Hint: "use openai, anthropic, openrouter, or nvidia"})
	}
	if s.APIKey == "" {
		findings = append(findings, doctorFinding{Level: "warn", Key: "llm.api_key", Message: "llm.api_key is empty", Path: s.ConfigPath, Hint: "set llm.api_key or a profile-specific api_key before using remote providers"})
	}
	if s.RequestTimeout <= 0 {
		findings = append(findings, doctorFinding{Level: "error", Key: "server.request_timeout", Message: "server.request_timeout must be > 0", Path: s.ConfigPath})
	}
	if s.MaxSessionMessages < 1 {
		findings = append(findings, doctorFinding{Level: "error", Key: "session.max_messages", Message: "session.max_messages must be >= 1", Path: s.ConfigPath})
	}
	if s.SessionMaxEntries < 0 {
		findings = append(findings, doctorFinding{Level: "error", Key: "session.max_entries", Message: "session.max_entries must be >= 0", Path: s.ConfigPath})
	}
	if s.CronMaxConcurrent < 1 {
		findings = append(findings, doctorFinding{Level: "error", Key: "cron.max_concurrent", Message: "cron.max_concurrent must be >= 1", Path: s.ConfigPath})
	}
	if s.AgentMaxChildren < 1 {
		findings = append(findings, doctorFinding{Level: "error", Key: "agent.max_children", Message: "agent.max_children must be >= 1", Path: s.ConfigPath})
	}
	if s.AgentRetryAttempts < 1 {
		findings = append(findings, doctorFinding{Level: "error", Key: "agent.retry_attempts", Message: "agent.retry_attempts must be >= 1", Path: s.ConfigPath})
	}
	if s.WorkspaceRoot == "" {
		findings = append(findings, doctorFinding{Level: "error", Key: "workspace.root", Message: "workspace.root must not be empty", Path: s.ConfigPath})
	}
	dirs := map[string]string{
		"workspace.root": s.WorkspaceRoot,
		"session.dir":    s.sessionDir(),
		"cron.dir":       s.cronDir(),
		"agent.dir":      s.agentDir(),
		"workflow.dir":   s.workflowDir(),
		"runs.dir":       s.runsDir(),
	}
	for key, path := range dirs {
		if path == "" {
			continue
		}
		if !dirExists(path) {
			findings = append(findings, doctorFinding{Level: "warn", Key: key, Message: fmt.Sprintf("directory does not exist yet: %s", path), Path: path, Hint: "run koios doctor --repair to create the local state directories", Repairable: true})
		}
	}
	if dbPath := s.memoryDBPath(); dbPath != "" && !dirExists(filepath.Dir(dbPath)) {
		findings = append(findings, doctorFinding{Level: "warn", Key: "memory.db", Message: fmt.Sprintf("memory database parent directory does not exist yet: %s", filepath.Dir(dbPath)), Path: filepath.Dir(dbPath), Hint: "run koios doctor --repair to create the local state directories", Repairable: true})
	}
	return findings
}

func (s *repoState) createStateDirs() []string {
	created := []string{}
	for _, path := range uniqueNonEmptyPaths([]string{s.WorkspaceRoot, s.sessionDir(), s.cronDir(), s.agentDir(), s.workflowDir(), s.runsDir()}) {
		if dirExists(path) {
			continue
		}
		if err := os.MkdirAll(path, 0o755); err == nil {
			created = append(created, path)
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

// workflowDir returns the derived workflow storage path.
func (s *repoState) workflowDir() string {
	if s.Config == nil || s.WorkspaceRoot == "" {
		return ""
	}
	return s.Config.WorkflowDir()
}

// runsDir returns the derived run-ledger storage path.
func (s *repoState) runsDir() string {
	if s.Config == nil || s.WorkspaceRoot == "" {
		return ""
	}
	return s.Config.RunsDir()
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

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func uniqueNonEmptyPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	return out
}
