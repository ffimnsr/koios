package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/workspace"
)

type doctorSummary struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Info     int `json:"info"`
	Repairs  int `json:"repairs"`
}

type doctorReport struct {
	Summary  doctorSummary     `json:"summary"`
	Findings []doctorFinding   `json:"findings"`
	Repairs  []string          `json:"repairs,omitempty"`
	Paths    map[string]string `json:"paths"`
	Gateway  map[string]any    `json:"gateway,omitempty"`
}

func runDoctorCommand(cmdCtx *commandContext, cmd *cobra.Command, repair, force, nonInteractive, deep, jsonOut bool) error {
	if nonInteractive {
		// Accepted for compatibility; doctor currently never prompts.
	}
	state, err := resolveDoctorState(cmdCtx.cwdOrDefault())
	if err != nil {
		return err
	}
	repairs := []string{}
	if repair {
		repairs, err = applyDoctorRepairs(state, force)
		if err != nil {
			return err
		}
		state, err = resolveDoctorState(cmdCtx.cwdOrDefault())
		if err != nil {
			return err
		}
	}
	findings, gateway := collectDoctorFindings(cmd.Context(), state, deep)
	report := doctorReport{
		Summary:  summarizeDoctorFindings(findings, repairs),
		Findings: findings,
		Repairs:  repairs,
		Paths:    statePaths(state),
		Gateway:  gateway,
	}
	emit(cmd, jsonOut, report)
	if report.Summary.Errors > 0 {
		return errors.New("doctor found configuration errors")
	}
	return nil
}

func collectDoctorFindings(ctx context.Context, state *repoState, deep bool) ([]doctorFinding, map[string]any) {
	findings := append([]doctorFinding(nil), state.validate()...)
	if state.ConfigLoadError == "" {
		findings = append(findings, collectDoctorRuntimeFindings(state, deep)...)
	}
	gateway := map[string]any{}
	if deep {
		live, gatewayPayload := collectDoctorGatewayFindings(ctx, state)
		findings = append(findings, live...)
		gateway = gatewayPayload
	}
	return sortDoctorFindings(findings), gateway
}

func collectDoctorRuntimeFindings(state *repoState, deep bool) []doctorFinding {
	findings := []doctorFinding{}
	if state.WorkspaceRoot != "" && dirExists(state.WorkspaceRoot) {
		for _, path := range doctorWorkspaceDocPaths(state.WorkspaceRoot) {
			if !fileExists(path) {
				findings = append(findings, doctorFinding{
					Level:      "warn",
					Key:        "workspace.doc",
					Message:    fmt.Sprintf("workspace starter document is missing: %s", filepath.Base(path)),
					Path:       path,
					Hint:       "run koios doctor --repair to scaffold missing workspace documents",
					Repairable: true,
				})
			}
		}
	}
	if state.CodeExecutionEnabled || state.ExecIsolationEnabled {
		bwrapPath, err := exec.LookPath("bwrap")
		if err != nil {
			findings = append(findings, doctorFinding{
				Level:   "error",
				Key:     "sandbox.bwrap",
				Message: "bubblewrap (bwrap) is not installed",
				Hint:    "install bubblewrap (for example: apt install bubblewrap) before using sandboxed execution",
			})
		} else if deep {
			findings = append(findings, doctorFinding{Level: "info", Key: "sandbox.bwrap", Message: fmt.Sprintf("bubblewrap available at %s", bwrapPath), Path: bwrapPath})
		}
	}
	for index, server := range state.MCPServers {
		if !server.Enabled {
			continue
		}
		prefix := fmt.Sprintf("mcp.servers[%d]", index)
		name := strings.TrimSpace(server.Name)
		probeReady := true
		if name == "" {
			findings = append(findings, doctorFinding{Level: "error", Key: prefix + ".name", Message: "enabled MCP server is missing a name", Path: state.ConfigPath})
			continue
		}
		transport := strings.ToLower(strings.TrimSpace(server.Transport))
		if transport == "" {
			transport = "http"
		}
		if server.Timeout != "" {
			if _, err := time.ParseDuration(server.Timeout); err != nil {
				findings = append(findings, doctorFinding{Level: "error", Key: prefix + ".timeout", Message: fmt.Sprintf("invalid MCP timeout for %s: %v", name, err), Path: state.ConfigPath})
				probeReady = false
			}
		}
		switch transport {
		case "stdio":
			command := strings.TrimSpace(server.Command)
			if command == "" {
				findings = append(findings, doctorFinding{Level: "error", Key: prefix + ".command", Message: fmt.Sprintf("MCP server %q uses stdio transport but has no command", name), Path: state.ConfigPath})
				probeReady = false
				continue
			}
			resolved, err := exec.LookPath(command)
			if err != nil {
				findings = append(findings, doctorFinding{Level: "error", Key: prefix + ".command", Message: fmt.Sprintf("MCP server %q command not found: %s", name, command), Path: state.ConfigPath, Hint: "install the MCP server binary or fix mcp.servers.command"})
				probeReady = false
			} else if deep {
				findings = append(findings, doctorFinding{Level: "info", Key: prefix + ".command", Message: fmt.Sprintf("MCP server %q command resolved", name), Path: resolved})
			}
		case "http", "sse":
			if strings.TrimSpace(server.URL) == "" {
				findings = append(findings, doctorFinding{Level: "error", Key: prefix + ".url", Message: fmt.Sprintf("MCP server %q requires a URL", name), Path: state.ConfigPath})
				probeReady = false
				continue
			}
			if _, err := url.ParseRequestURI(server.URL); err != nil {
				findings = append(findings, doctorFinding{Level: "error", Key: prefix + ".url", Message: fmt.Sprintf("invalid MCP URL for %q: %v", name, err), Path: state.ConfigPath})
				probeReady = false
			}
		default:
			findings = append(findings, doctorFinding{Level: "error", Key: prefix + ".transport", Message: fmt.Sprintf("unsupported MCP transport %q for %q", server.Transport, name), Path: state.ConfigPath, Hint: "use stdio, http, or sse"})
			probeReady = false
		}
		if deep && probeReady {
			findings = append(findings, probeDoctorMCPServer(server, prefix, name, transport))
		}
	}
	if state.MilvusEnabled {
		if strings.TrimSpace(state.MilvusURL) == "" {
			findings = append(findings, doctorFinding{Level: "error", Key: "memory.milvus.address", Message: "memory.milvus.address must not be empty when Milvus is enabled", Path: state.ConfigPath})
		}
		if strings.TrimSpace(state.MilvusCollection) == "" {
			findings = append(findings, doctorFinding{Level: "error", Key: "memory.milvus.collection", Message: "memory.milvus.collection must not be empty when Milvus is enabled", Path: state.ConfigPath})
		}
		if deep && strings.TrimSpace(state.MilvusURL) != "" {
			conn, err := net.DialTimeout("tcp", state.MilvusURL, 2*time.Second)
			if err != nil {
				findings = append(findings, doctorFinding{Level: "warn", Key: "memory.milvus", Message: fmt.Sprintf("Milvus is enabled but not reachable at %s: %v", state.MilvusURL, err), Path: state.ConfigPath})
			} else {
				_ = conn.Close()
				findings = append(findings, doctorFinding{Level: "info", Key: "memory.milvus", Message: fmt.Sprintf("Milvus reachable at %s", state.MilvusURL), Path: state.ConfigPath})
			}
		}
	}
	return findings
}

func collectDoctorGatewayFindings(ctx context.Context, state *repoState) ([]doctorFinding, map[string]any) {
	findings := []doctorFinding{}
	gateway := map[string]any{"base_url": state.baseHTTPURL()}
	client := newGatewayClient(state, 3*time.Second)
	reqCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	health, status, err := client.health(reqCtx)
	if err != nil {
		findings = append(findings, doctorFinding{Level: "warn", Key: "gateway.health", Message: "gateway health probe failed: " + err.Error(), Hint: "start the gateway with koios serve if you expect local control-plane APIs to be live"})
		return findings, gateway
	}
	gateway["health"] = health
	gateway["health_http_status"] = status
	findings = append(findings, doctorFinding{Level: "info", Key: "gateway.health", Message: fmt.Sprintf("gateway reachable at %s/healthz", state.baseHTTPURL())})
	if version, status, err := client.version(reqCtx); err == nil {
		gateway["version"] = version
		gateway["version_http_status"] = status
	}
	monitorPayload, status, err := client.monitor(reqCtx)
	if err != nil {
		findings = append(findings, doctorFinding{Level: "warn", Key: "gateway.monitor", Message: "gateway monitor probe failed: " + err.Error(), Hint: "upgrade or restart the gateway if /v1/monitor should be available"})
		return findings, gateway
	}
	gateway["monitor"] = monitorPayload
	gateway["monitor_http_status"] = status
	if stale, _ := monitorPayload["stale"].(bool); stale {
		findings = append(findings, doctorFinding{Level: "warn", Key: "gateway.monitor.stale", Message: "gateway monitor reports stale activity", Hint: "inspect recent traffic or restart the gateway if it is unexpectedly idle"})
	} else {
		findings = append(findings, doctorFinding{Level: "info", Key: "gateway.monitor", Message: "gateway monitor endpoint reachable"})
	}
	if subsystems, ok := monitorPayload["subsystems"].(map[string]any); ok {
		for name, raw := range subsystems {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if restarts := doctorInt(entry["restarts"]); restarts > 0 {
				findings = append(findings, doctorFinding{Level: "warn", Key: "gateway.monitor.subsystem", Message: fmt.Sprintf("subsystem %s has restarted %d time(s)", name, restarts)})
			}
		}
	}
	return findings, gateway
}

func applyDoctorRepairs(state *repoState, force bool) ([]string, error) {
	repairs := []string{}
	if !state.ConfigExists {
		if err := os.WriteFile(state.ConfigPath, []byte(config.DefaultTOML()), 0o600); err != nil {
			return nil, fmt.Errorf("write starter config: %w", err)
		}
		repairs = append(repairs, "created koios.config.toml")
	}
	if state.ConfigLoadError != "" {
		return applyDoctorBrokenConfigRepair(state, repairs, force)
	}
	configRepairs, err := repairDoctorConfig(state)
	if err != nil {
		return nil, err
	}
	repairs = append(repairs, configRepairs...)
	repairs = append(repairs, mapPaths(state.createStateDirs(), "created directory: ")...)
	dbRepairs, err := bootstrapWorkspaceDBs(state)
	if err != nil {
		return nil, err
	}
	repairs = append(repairs, mapPaths(dbRepairs, "initialized database: ")...)
	workspaceRepairs, err := repairDoctorWorkspace(state)
	if err != nil {
		return nil, err
	}
	repairs = append(repairs, workspaceRepairs...)
	return repairs, nil
}

func summarizeDoctorFindings(findings []doctorFinding, repairs []string) doctorSummary {
	summary := doctorSummary{Repairs: len(repairs)}
	for _, finding := range findings {
		switch finding.Level {
		case "error":
			summary.Errors++
		case "warn":
			summary.Warnings++
		default:
			summary.Info++
		}
	}
	return summary
}

func sortDoctorFindings(findings []doctorFinding) []doctorFinding {
	sorted := append([]doctorFinding(nil), findings...)
	levelRank := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.SliceStable(sorted, func(i, j int) bool {
		left := levelRank[sorted[i].Level]
		right := levelRank[sorted[j].Level]
		if left != right {
			return left < right
		}
		return sorted[i].Key < sorted[j].Key
	})
	return sorted
}

func doctorInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	default:
		return 0
	}
}

func applyDoctorBrokenConfigRepair(state *repoState, repairs []string, force bool) ([]string, error) {
	if !force {
		return repairs, nil
	}
	if !fileExists(state.ConfigPath) {
		return repairs, nil
	}
	backupPath := state.ConfigPath + ".bak"
	data, err := os.ReadFile(state.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("read unreadable config for backup: %w", err)
	}
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("backup unreadable config: %w", err)
	}
	if err := os.WriteFile(state.ConfigPath, []byte(config.DefaultTOML()), 0o600); err != nil {
		return nil, fmt.Errorf("replace unreadable config with defaults: %w", err)
	}
	repairs = append(repairs,
		"backed up unreadable koios.config.toml to "+backupPath,
		"replaced unreadable koios.config.toml with defaults",
	)
	return repairs, nil
}

func repairDoctorConfig(state *repoState) ([]string, error) {
	if state.Config == nil {
		return nil, nil
	}
	defaultCfg := config.Default()
	cfg := cloneDoctorConfig(state)
	repairs := []string{}
	changed := false
	fixString := func(current *string, key, value string) {
		if *current == value {
			return
		}
		*current = value
		changed = true
		repairs = append(repairs, fmt.Sprintf("reset %s to %q", key, value))
	}
	fixDuration := func(current *time.Duration, key string, value time.Duration) {
		if *current == value {
			return
		}
		*current = value
		changed = true
		repairs = append(repairs, fmt.Sprintf("reset %s to %s", key, value))
	}
	fixInt := func(current *int, key string, value int) {
		if *current == value {
			return
		}
		*current = value
		changed = true
		repairs = append(repairs, fmt.Sprintf("reset %s to %d", key, value))
	}
	if strings.TrimSpace(cfg.Model) == "" {
		fixString(&cfg.Model, "llm.model", defaultCfg.Model)
	}
	switch cfg.Provider {
	case "openai", "anthropic", "openrouter", "nvidia":
	default:
		fixString(&cfg.Provider, "llm.provider", defaultCfg.Provider)
	}
	if cfg.RequestTimeout <= 0 {
		fixDuration(&cfg.RequestTimeout, "server.request_timeout", defaultCfg.RequestTimeout)
	}
	if cfg.MaxSessionMessages < 1 {
		fixInt(&cfg.MaxSessionMessages, "session.max_messages", defaultCfg.MaxSessionMessages)
	}
	if cfg.SessionRetention < 0 {
		fixDuration(&cfg.SessionRetention, "session.retention", defaultCfg.SessionRetention)
	}
	if cfg.SessionMaxEntries < 0 {
		fixInt(&cfg.SessionMaxEntries, "session.max_entries", defaultCfg.SessionMaxEntries)
	}
	if cfg.SessionIdleResetAfter < 0 {
		fixDuration(&cfg.SessionIdleResetAfter, "session.idle_reset_after", defaultCfg.SessionIdleResetAfter)
	}
	if cfg.SessionIdlePruneAfter < 0 {
		fixDuration(&cfg.SessionIdlePruneAfter, "session.idle_prune_after", defaultCfg.SessionIdlePruneAfter)
	}
	if cfg.SessionIdlePruneKeep < 0 {
		fixInt(&cfg.SessionIdlePruneKeep, "session.idle_prune_keep", defaultCfg.SessionIdlePruneKeep)
	}
	if _, err := config.ParseDailyResetMinutes(cfg.SessionDailyResetTime); err != nil {
		fixString(&cfg.SessionDailyResetTime, "session.daily_reset_time", defaultCfg.SessionDailyResetTime)
	}
	if cfg.CronMaxConcurrentRuns < 1 {
		fixInt(&cfg.CronMaxConcurrentRuns, "cron.max_concurrent", defaultCfg.CronMaxConcurrentRuns)
	}
	if cfg.AgentMaxChildren < 1 {
		fixInt(&cfg.AgentMaxChildren, "agent.max_children", defaultCfg.AgentMaxChildren)
	}
	if cfg.AgentRetryAttempts < 1 {
		fixInt(&cfg.AgentRetryAttempts, "agent.retry_attempts", defaultCfg.AgentRetryAttempts)
	}
	validStatusCodes := make([]int, 0, len(cfg.AgentRetryStatusCodes))
	for _, code := range cfg.AgentRetryStatusCodes {
		if code >= 100 && code <= 599 {
			validStatusCodes = append(validStatusCodes, code)
		}
	}
	if len(validStatusCodes) != len(cfg.AgentRetryStatusCodes) {
		changed = true
		if len(validStatusCodes) == 0 {
			cfg.AgentRetryStatusCodes = append([]int(nil), defaultCfg.AgentRetryStatusCodes...)
			repairs = append(repairs, "reset agent.retry_status_codes to defaults")
		} else {
			cfg.AgentRetryStatusCodes = validStatusCodes
			repairs = append(repairs, "removed invalid entries from agent.retry_status_codes")
		}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.ToolProfile)) {
	case "", "full", "coding", "messaging", "minimal":
	default:
		fixString(&cfg.ToolProfile, "tools.profile", defaultCfg.ToolProfile)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.ExecApprovalMode)) {
	case "", "off", "never", "dangerous", "always":
	default:
		fixString(&cfg.ExecApprovalMode, "tools.exec.approval_mode", defaultCfg.ExecApprovalMode)
	}
	if cfg.ExecDefaultTimeout <= 0 {
		fixDuration(&cfg.ExecDefaultTimeout, "tools.exec.default_timeout", defaultCfg.ExecDefaultTimeout)
	}
	if cfg.ExecMaxTimeout < cfg.ExecDefaultTimeout {
		fixDuration(&cfg.ExecMaxTimeout, "tools.exec.max_timeout", maxDoctorDuration(defaultCfg.ExecMaxTimeout, cfg.ExecDefaultTimeout))
	}
	if cfg.ExecApprovalTTL <= 0 {
		fixDuration(&cfg.ExecApprovalTTL, "tools.exec.approval_ttl", defaultCfg.ExecApprovalTTL)
	}
	if cfg.CodeExecutionDefaultTimeout <= 0 {
		fixDuration(&cfg.CodeExecutionDefaultTimeout, "tools.code_execution.default_timeout", defaultCfg.CodeExecutionDefaultTimeout)
	}
	if cfg.CodeExecutionMaxTimeout < cfg.CodeExecutionDefaultTimeout {
		fixDuration(&cfg.CodeExecutionMaxTimeout, "tools.code_execution.max_timeout", maxDoctorDuration(defaultCfg.CodeExecutionMaxTimeout, cfg.CodeExecutionDefaultTimeout))
	}
	if cfg.CodeExecutionMaxStdoutBytes < 1 {
		fixInt(&cfg.CodeExecutionMaxStdoutBytes, "tools.code_execution.max_stdout_bytes", defaultCfg.CodeExecutionMaxStdoutBytes)
	}
	if cfg.CodeExecutionMaxStderrBytes < 1 {
		fixInt(&cfg.CodeExecutionMaxStderrBytes, "tools.code_execution.max_stderr_bytes", defaultCfg.CodeExecutionMaxStderrBytes)
	}
	if cfg.CodeExecutionMaxArtifactBytes < 1 {
		cfg.CodeExecutionMaxArtifactBytes = defaultCfg.CodeExecutionMaxArtifactBytes
		changed = true
		repairs = append(repairs, fmt.Sprintf("reset tools.code_execution.max_artifact_bytes to %d", defaultCfg.CodeExecutionMaxArtifactBytes))
	}
	if cfg.CodeExecutionMaxOpenFiles < 1 {
		fixInt(&cfg.CodeExecutionMaxOpenFiles, "tools.code_execution.max_open_files", defaultCfg.CodeExecutionMaxOpenFiles)
	}
	if cfg.CodeExecutionMaxProcesses < 1 {
		fixInt(&cfg.CodeExecutionMaxProcesses, "tools.code_execution.max_processes", defaultCfg.CodeExecutionMaxProcesses)
	}
	if cfg.CodeExecutionCPUSeconds < 1 {
		fixInt(&cfg.CodeExecutionCPUSeconds, "tools.code_execution.cpu_seconds", defaultCfg.CodeExecutionCPUSeconds)
	}
	if cfg.CodeExecutionMemoryBytes < 1 {
		cfg.CodeExecutionMemoryBytes = defaultCfg.CodeExecutionMemoryBytes
		changed = true
		repairs = append(repairs, fmt.Sprintf("reset tools.code_execution.memory_bytes to %d", defaultCfg.CodeExecutionMemoryBytes))
	}
	if cfg.ProcessStopTimeout <= 0 {
		fixDuration(&cfg.ProcessStopTimeout, "tools.process.stop_timeout", defaultCfg.ProcessStopTimeout)
	}
	if cfg.ProcessLogTailBytes < 1 {
		fixInt(&cfg.ProcessLogTailBytes, "tools.process.log_tail_bytes", defaultCfg.ProcessLogTailBytes)
	}
	if cfg.ProcessMaxProcessesPerPeer < 1 {
		fixInt(&cfg.ProcessMaxProcessesPerPeer, "tools.process.max_processes_per_peer", defaultCfg.ProcessMaxProcessesPerPeer)
	}
	if strings.TrimSpace(cfg.WorkspaceRoot) == "" {
		fixString(&cfg.WorkspaceRoot, "workspace.root", "./workspace")
	}
	if cfg.WorkspaceMaxBytes < 1 {
		fixInt(&cfg.WorkspaceMaxBytes, "workspace.max_file_bytes", defaultCfg.WorkspaceMaxBytes)
	}
	if cfg.MilvusEnabled && strings.TrimSpace(cfg.MilvusURL) == "" {
		fixString(&cfg.MilvusURL, "memory.milvus.address", defaultCfg.MilvusURL)
	}
	if cfg.MilvusEnabled && strings.TrimSpace(cfg.MilvusCollection) == "" {
		fixString(&cfg.MilvusCollection, "memory.milvus.collection", defaultCfg.MilvusCollection)
	}
	filteredIsolation := make([]config.ExecIsolationPath, 0, len(cfg.ExecIsolationPaths))
	for _, mount := range cfg.ExecIsolationPaths {
		if strings.TrimSpace(mount.Source) == "" {
			changed = true
			repairs = append(repairs, "removed tools.exec.isolation entry with empty source")
			continue
		}
		if mount.Mode != "ro" && mount.Mode != "rw" {
			changed = true
			repairs = append(repairs, fmt.Sprintf("reset tools.exec.isolation mode for %s to ro", mount.Source))
			mount.Mode = "ro"
		}
		filteredIsolation = append(filteredIsolation, mount)
	}
	if len(filteredIsolation) != len(cfg.ExecIsolationPaths) {
		cfg.ExecIsolationPaths = filteredIsolation
	}
	if !changed {
		return nil, nil
	}
	if err := os.WriteFile(state.ConfigPath, []byte(config.EncodeTOML(cfg, true)), 0o600); err != nil {
		return nil, fmt.Errorf("rewrite repaired config: %w", err)
	}
	repairs = append(repairs, "rewrote koios.config.toml with normalized settings")
	return repairs, nil
}

func repairDoctorWorkspace(state *repoState) ([]string, error) {
	if state.WorkspaceRoot == "" {
		return nil, nil
	}
	missingDocs := make([]string, 0)
	for _, path := range doctorWorkspaceDocPaths(state.WorkspaceRoot) {
		if !fileExists(path) {
			missingDocs = append(missingDocs, path)
		}
	}
	if len(missingDocs) == 0 {
		return nil, nil
	}
	if err := scaffoldWorkspace(state.WorkspaceRoot, false); err != nil {
		return nil, err
	}
	repairs := make([]string, 0, len(missingDocs))
	for _, path := range missingDocs {
		repairs = append(repairs, "created workspace starter doc: "+path)
	}
	return repairs, nil
}

func doctorWorkspaceDocs() []string {
	return workspace.PeerDocumentNames()

}

func doctorWorkspaceDocPaths(root string) []string {
	paths := make([]string, 0, len(doctorWorkspaceDocs()))
	defaultRoot := workspace.DefaultPeerRoot(root)
	for _, doc := range doctorWorkspaceDocs() {
		defaultPath := filepath.Join(defaultRoot, doc)
		paths = append(paths, defaultPath)
	}
	return paths
}

func cloneDoctorConfig(state *repoState) *config.Config {
	clone := *state.Config
	clone.FallbackModels = append([]string(nil), state.Config.FallbackModels...)
	clone.ModelProfiles = append([]config.ModelProfile(nil), state.Config.ModelProfiles...)
	clone.MCPServers = append([]config.MCPServerConfig(nil), state.Config.MCPServers...)
	clone.MemoryNamespaces = append([]string(nil), state.Config.MemoryNamespaces...)
	clone.AgentRetryStatusCodes = append([]int(nil), state.Config.AgentRetryStatusCodes...)
	clone.AllowedOrigins = append([]string(nil), state.Config.AllowedOrigins...)
	clone.OwnerPeerIDs = append([]string(nil), state.Config.OwnerPeerIDs...)
	clone.ToolsAllow = append([]string(nil), state.Config.ToolsAllow...)
	clone.ToolsDeny = append([]string(nil), state.Config.ToolsDeny...)
	clone.ExecCustomDenyPatterns = append([]string(nil), state.Config.ExecCustomDenyPatterns...)
	clone.ExecCustomAllowPatterns = append([]string(nil), state.Config.ExecCustomAllowPatterns...)
	clone.ExecIsolationPaths = append([]config.ExecIsolationPath(nil), state.Config.ExecIsolationPaths...)
	clone.WorkspaceRoot = doctorWritableWorkspaceRoot(state.Root, state.Config.WorkspaceRoot)
	return &clone
}

func doctorWritableWorkspaceRoot(repoRoot, workspaceRoot string) string {
	if workspaceRoot == "" || !filepath.IsAbs(workspaceRoot) {
		return workspaceRoot
	}
	rel, err := filepath.Rel(repoRoot, workspaceRoot)
	if err != nil {
		return workspaceRoot
	}
	if rel == "." {
		return "./"
	}
	if strings.HasPrefix(rel, "..") {
		return workspaceRoot
	}
	if !strings.HasPrefix(rel, ".") {
		return "./" + filepath.ToSlash(rel)
	}
	return filepath.ToSlash(rel)
}

func maxDoctorDuration(defaultValue, minimum time.Duration) time.Duration {
	if defaultValue > minimum {
		return defaultValue
	}
	return minimum
}

func probeDoctorMCPServer(server config.MCPServerConfig, prefix, name, transport string) doctorFinding {
	timeout := 5 * time.Second
	if server.Timeout != "" {
		if parsed, err := time.ParseDuration(server.Timeout); err == nil && parsed > 0 {
			timeout = parsed
		}
	}
	client := newDoctorMCPClient(server, transport, timeout)
	if client == nil {
		return doctorFinding{Level: "warn", Key: prefix + ".probe", Message: fmt.Sprintf("MCP server %q could not be probed", name), Hint: "check the MCP transport settings"}
	}
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := client.Initialize(ctx); err != nil {
		return doctorFinding{
			Level:   "warn",
			Key:     prefix + ".probe",
			Message: fmt.Sprintf("MCP server %q probe failed: %v", name, err),
			Path:    doctorMCPPath(server, transport),
			Hint:    "verify the server is running and speaks the MCP initialize/tools/list handshake",
		}
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		return doctorFinding{
			Level:   "warn",
			Key:     prefix + ".probe",
			Message: fmt.Sprintf("MCP server %q initialized but tools/list failed: %v", name, err),
			Path:    doctorMCPPath(server, transport),
			Hint:    "verify the server can advertise tools after initialization",
		}
	}
	return doctorFinding{
		Level:   "info",
		Key:     prefix + ".probe",
		Message: fmt.Sprintf("MCP server %q reachable over %s with %d tool(s)", name, transport, len(tools)),
		Path:    doctorMCPPath(server, transport),
	}
}

func newDoctorMCPClient(server config.MCPServerConfig, transport string, timeout time.Duration) mcp.Client {
	switch transport {
	case "stdio":
		return mcp.NewStdioClient(server.Name, server.Command, server.Args, server.Env)
	case "sse":
		return mcp.NewSSEClient(server.Name, server.URL, server.Headers, timeout)
	default:
		return mcp.NewHTTPClient(server.Name, server.URL, server.Headers, timeout)
	}
}

func doctorMCPPath(server config.MCPServerConfig, transport string) string {
	if transport == "stdio" {
		return server.Command
	}
	return server.URL
}
