package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/sandbox"
)

type ExecConfig struct {
	Enabled             bool
	EnableDenyPatterns  bool
	CustomDenyPatterns  []*regexp.Regexp
	CustomAllowPatterns []*regexp.Regexp
	DefaultTimeout      time.Duration
	MaxTimeout          time.Duration
	ApprovalMode        string
	ApprovalTTL         time.Duration
	IsolationEnabled    bool
	IsolationPaths      []ExecIsolationPath
}

// ExecIsolationPath mirrors config.ExecIsolationPath so the handler package
// does not import config directly.
type ExecIsolationPath struct {
	Source string
	Target string
	Mode   string // "ro" or "rw"
}

type execParams struct {
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
	Timeout int    `json:"timeout_seconds,omitempty"`
}

type execApprovalDecision struct {
	reason         string
	policy         string
	matchedPattern string
}

var dangerousExecPatterns = []struct {
	re     *regexp.Regexp
	reason string
}{
	{re: regexp.MustCompile(`(?i)\brm\s+-[^\n]*r[^\n]*f\b`), reason: "bulk deletion"},
	{re: regexp.MustCompile(`(?i)\bdel\s+/f\b`), reason: "bulk deletion"},
	{re: regexp.MustCompile(`(?i)\brmdir\s+/s\b`), reason: "bulk deletion"},
	{re: regexp.MustCompile(`(?i)\bformat\b`), reason: "disk formatting"},
	{re: regexp.MustCompile(`(?i)\bmkfs(?:\.[\w-]+)?\b`), reason: "disk formatting"},
	{re: regexp.MustCompile(`(?i)\bdiskpart\b`), reason: "disk formatting"},
	{re: regexp.MustCompile(`(?i)\bdd\s+if=`), reason: "disk imaging"},
	{re: regexp.MustCompile(`(?i)/dev/(?:sd[a-z]\w*|hd[a-z]\w*|nvme\d+n\d+(?:p\d+)?|mmcblk\d+(?:p\d+)?|loop\d+)`), reason: "direct block device access"},
	{re: regexp.MustCompile(`(?i)\bshutdown\b`), reason: "system shutdown"},
	{re: regexp.MustCompile(`(?i)\breboot\b`), reason: "system shutdown"},
	{re: regexp.MustCompile(`(?i)\bpoweroff\b`), reason: "system shutdown"},
	{re: regexp.MustCompile(`:\(\)\s*\{\s*:\|:&\s*\};:`), reason: "fork bomb"},
}

func normalizeExecConfig(cfg ExecConfig) ExecConfig {
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = 5 * time.Minute
	}
	if cfg.MaxTimeout < cfg.DefaultTimeout {
		cfg.MaxTimeout = cfg.DefaultTimeout
	}
	cfg.ApprovalMode = strings.ToLower(strings.TrimSpace(cfg.ApprovalMode))
	if cfg.ApprovalMode == "" {
		cfg.ApprovalMode = "dangerous"
	}
	if cfg.ApprovalTTL <= 0 {
		cfg.ApprovalTTL = 15 * time.Minute
	}
	// Default to enabled and deny patterns active when not explicitly configured.
	if !cfg.EnableDenyPatterns {
		cfg.EnableDenyPatterns = true
	}
	if !cfg.Enabled {
		cfg.Enabled = true
	}
	return cfg
}

func (h *Handler) runExecTool(ctx context.Context, peerID string, p execParams) (map[string]any, error) {
	if !h.execConfig.Enabled {
		return nil, fmt.Errorf("exec tool is disabled")
	}
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("exec is not enabled")
	}
	command := strings.TrimSpace(p.Command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	timeout := h.execConfig.DefaultTimeout
	if p.Timeout > 0 {
		timeout = time.Duration(p.Timeout) * time.Second
	}
	if timeout > h.execConfig.MaxTimeout {
		return nil, fmt.Errorf("timeout exceeds max of %s", h.execConfig.MaxTimeout)
	}
	workdir := "."
	if strings.TrimSpace(p.Workdir) != "" {
		workdir = p.Workdir
	}
	absWorkdir, err := h.workspaceStore.Resolve(peerID, workdir)
	if err != nil {
		return nil, err
	}

	// When the session has elevated bash enabled, dangerous-pattern checks are
	// skipped for this session (the operator has explicitly opted in).
	elevated := h.sessionElevatedBash(ctx, peerID)
	if !elevated {
		if decision, ok := h.execNeedsApproval(command); ok {
			return h.requestApproval(peerID, pendingApproval{
				Kind:    "shell_execution",
				Action:  "exec",
				Summary: command,
				Reason:  decision.reason,
				Command: command,
				Workdir: absWorkdir,
				Timeout: int(timeout / time.Second),
				Metadata: map[string]any{
					"approval_mode":   h.execConfig.ApprovalMode,
					"policy":          decision.policy,
					"matched_pattern": decision.matchedPattern,
				},
			}, func(runCtx context.Context, approvedPeerID string, approval pendingApproval) (map[string]any, error) {
				return h.executeCommand(runCtx, approvedPeerID, approval.Command, approval.Workdir, time.Duration(approval.Timeout)*time.Second)
			}), nil
		}
	}
	return h.executeCommand(ctx, peerID, command, absWorkdir, timeout)
}

// sessionElevatedBash returns true when the session associated with ctx (or
// peerID as fallback) has ElevatedBash enabled.
func (h *Handler) sessionElevatedBash(ctx context.Context, peerID string) bool {
	toolCtx, _ := agent.ToolRunContextFromContext(ctx)
	key := toolCtx.SessionKey
	if key == "" {
		key = peerID
	}
	if key == "" {
		return false
	}
	return h.store.Policy(key).ElevatedBash
}

func (h *Handler) runSystemNotifyTool(ctx context.Context, title, message string) (map[string]any, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Koios"
	}
	name, args, err := systemNotifyCommand(title, message)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, fmt.Errorf("system.notify failed: %s", errMsg)
	}
	return map[string]any{
		"ok":      true,
		"title":   title,
		"message": message,
		"command": append([]string{name}, args...),
	}, nil
}

func systemNotifyCommand(title, message string) (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("osascript"); err != nil {
			return "", nil, fmt.Errorf("system notifications are unavailable: osascript not found")
		}
		script := fmt.Sprintf(`display notification %q with title %q`, message, title)
		return "osascript", []string{"-e", script}, nil
	case "windows":
		if _, err := exec.LookPath("powershell"); err != nil {
			if _, err := exec.LookPath("pwsh"); err != nil {
				return "", nil, fmt.Errorf("system notifications are unavailable: PowerShell not found")
			}
			return "pwsh", []string{"-NoProfile", "-Command", fmt.Sprintf(`[console]::beep(800,200); Write-Output %q`, title+": "+message)}, nil
		}
		return "powershell", []string{"-NoProfile", "-Command", fmt.Sprintf(`[console]::beep(800,200); Write-Output %q`, title+": "+message)}, nil
	default:
		if _, err := exec.LookPath("notify-send"); err != nil {
			return "", nil, fmt.Errorf("system notifications are unavailable: notify-send not found")
		}
		return "notify-send", []string{title, message}, nil
	}
}

// validNotificationKinds is the set of accepted kind values for notification.send.
var validNotificationKinds = map[string]bool{
	"reminder":     true,
	"run_complete": true,
	"cron":         true,
	"waiting_on":   true,
	"status":       true,
	"alert":        true,
	"info":         true,
}

// validNotificationUrgencies is the set of accepted urgency values for notification.send.
var validNotificationUrgencies = map[string]bool{
	"low":    true,
	"normal": true,
	"high":   true,
}

// runNotificationSendTool delivers a structured local notification to the
// owner's device.  It delegates to the same OS notification mechanism as
// system.notify but forwards kind and urgency metadata where the platform
// supports it (notify-send on Linux accepts --urgency).
func (h *Handler) runNotificationSendTool(ctx context.Context, title, message, kind, urgency string) (map[string]any, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, fmt.Errorf("message is required")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Koios"
	}
	kind = strings.TrimSpace(kind)
	if kind != "" && !validNotificationKinds[kind] {
		return nil, fmt.Errorf("invalid kind %q: must be one of reminder, run_complete, cron, waiting_on, status, alert, info", kind)
	}
	urgency = strings.TrimSpace(urgency)
	if urgency != "" && !validNotificationUrgencies[urgency] {
		return nil, fmt.Errorf("invalid urgency %q: must be one of low, normal, high", urgency)
	}
	name, args, err := notificationSendCommand(title, message, urgency)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return nil, fmt.Errorf("notification.send failed: %s", errMsg)
	}
	result := map[string]any{
		"ok":      true,
		"title":   title,
		"message": message,
	}
	if kind != "" {
		result["kind"] = kind
	}
	if urgency != "" {
		result["urgency"] = urgency
	}
	return result, nil
}

// notificationSendCommand builds the OS command for notification.send.  On
// Linux it passes --urgency to notify-send when an urgency value is provided,
// mapping "high" to the notify-send "critical" level.
func notificationSendCommand(title, message, urgency string) (string, []string, error) {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("osascript"); err != nil {
			return "", nil, fmt.Errorf("system notifications are unavailable: osascript not found")
		}
		script := fmt.Sprintf(`display notification %q with title %q`, message, title)
		return "osascript", []string{"-e", script}, nil
	case "windows":
		if _, err := exec.LookPath("powershell"); err != nil {
			if _, err := exec.LookPath("pwsh"); err != nil {
				return "", nil, fmt.Errorf("system notifications are unavailable: PowerShell not found")
			}
			return "pwsh", []string{"-NoProfile", "-Command", fmt.Sprintf(`[console]::beep(800,200); Write-Output %q`, title+": "+message)}, nil
		}
		return "powershell", []string{"-NoProfile", "-Command", fmt.Sprintf(`[console]::beep(800,200); Write-Output %q`, title+": "+message)}, nil
	default:
		if _, err := exec.LookPath("notify-send"); err != nil {
			return "", nil, fmt.Errorf("system notifications are unavailable: notify-send not found")
		}
		args := []string{}
		if urgency != "" {
			// notify-send uses "critical" where the tool API uses "high".
			level := urgency
			if level == "high" {
				level = "critical"
			}
			args = append(args, "--urgency", level)
		}
		args = append(args, title, message)
		return "notify-send", args, nil
	}
}

func (h *Handler) execNeedsApproval(command string) (execApprovalDecision, bool) {
	switch h.execConfig.ApprovalMode {
	case "never", "off":
		return execApprovalDecision{}, false
	case "always":
		return execApprovalDecision{reason: "command requires approval by policy", policy: "approval_mode_always"}, true
	default:
		return detectDangerousCommand(command, h.execConfig)
	}
}

func detectDangerousCommand(command string, cfg ExecConfig) (execApprovalDecision, bool) {
	// Custom allow patterns take priority — matching commands bypass all deny checks.
	for _, re := range cfg.CustomAllowPatterns {
		if re.MatchString(command) {
			return execApprovalDecision{}, false
		}
	}
	// Built-in dangerous command patterns (enabled by default).
	if cfg.EnableDenyPatterns {
		for _, pattern := range dangerousExecPatterns {
			if pattern.re.MatchString(command) {
				return execApprovalDecision{reason: pattern.reason, policy: "builtin_deny_pattern", matchedPattern: pattern.re.String()}, true
			}
		}
	}
	// User-supplied custom deny patterns.
	for _, re := range cfg.CustomDenyPatterns {
		if re.MatchString(command) {
			return execApprovalDecision{reason: "blocked by custom deny pattern", policy: "custom_deny_pattern", matchedPattern: re.String()}, true
		}
	}
	return execApprovalDecision{}, false
}
func (h *Handler) executeApprovedExec(ctx context.Context, peerID, approvalID string) (map[string]any, error) {
	return h.approvePendingAction(ctx, peerID, approvalID, shellApprovalFilter)
}

func (h *Handler) executeCommand(ctx context.Context, peerID, command, workdir string, timeout time.Duration) (map[string]any, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if h.execConfig.IsolationEnabled {
		var err error
		cmd, err = h.buildIsolatedCommand(runCtx, peerID, command, workdir)
		if err != nil {
			return nil, err
		}
	} else {
		shell, args := shellCommand(command)
		cmd = exec.CommandContext(runCtx, shell, args...)
		cmd.Dir = workdir
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	started := time.Now()
	err := cmd.Run()
	duration := time.Since(started)
	exitCode := 0
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)

	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if timedOut {
			exitCode = -1
		} else {
			return nil, err
		}
	}
	status := "completed"
	if timedOut {
		status = "timed_out"
	}
	return map[string]any{
		"status":      status,
		"command":     command,
		"workdir":     workdir,
		"stdout":      stdout.String(),
		"stderr":      stderr.String(),
		"exit_code":   exitCode,
		"timed_out":   timedOut,
		"duration_ms": duration.Milliseconds(),
	}, nil
}

// buildIsolatedCommand constructs a bwrap-wrapped command.
// Default mounts expose the bare minimum needed to run shell commands:
//   - /usr, /bin, /lib, /lib64 (read-only)
//   - /etc/resolv.conf (read-only)
//   - workdir (read-write) mapped to /workspace, with CWD set there
//   - Per-instance temp dir as /tmp
//
// Additional paths come from ExecIsolationPaths in the config.
func (h *Handler) buildIsolatedCommand(ctx context.Context, peerID, command, workdir string) (*exec.Cmd, error) {
	workspaceRoot, err := h.workspaceStore.EnsurePeer(peerID)
	if err != nil {
		return nil, err
	}
	relWorkdir, err := filepath.Rel(workspaceRoot, workdir)
	if err != nil {
		return nil, fmt.Errorf("resolve exec isolation workdir: %w", err)
	}
	if relWorkdir == "." {
		relWorkdir = ""
	}
	mounts := make([]sandbox.BindMount, 0, len(h.execConfig.IsolationPaths))
	for _, p := range h.execConfig.IsolationPaths {
		target := p.Target
		if strings.TrimSpace(target) == "" {
			target = p.Source
		}
		mounts = append(mounts, sandbox.BindMount{
			Source:   p.Source,
			Target:   target,
			ReadOnly: p.Mode != "rw",
		})
	}
	cmd, cleanup, err := defaultSandboxRunner.BuildBubblewrapCommand(ctx, sandbox.Request{
		Command:        command,
		WorkspaceRoot:  workspaceRoot,
		Workdir:        relWorkdir,
		NetworkEnabled: false,
		ExtraMounts:    mounts,
	})
	if err != nil {
		return nil, err
	}
	go func() {
		<-ctx.Done()
		cleanup()
	}()
	return cmd, nil
}

func shellCommand(command string) (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C", command}
	}
	return "sh", []string{"-lc", command}
}

func (h *Handler) rpcExec(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p execParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.runExecTool(ctx, wsc.peerID, p)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcExecPending(_ context.Context, wsc *wsConn, req *rpcRequest) {
	wsc.reply(req.ID, map[string]any{"pending": h.approvals.list(wsc.peerID, shellApprovalFilter)})
}

func (h *Handler) rpcExecApprove(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.ID) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	result, err := h.executeApprovedExec(ctx, wsc.peerID, p.ID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcExecReject(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.ID) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	if err := h.rejectPendingAction(wsc.peerID, p.ID, shellApprovalFilter); err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) execPromptHint() string {
	if h.execConfig.ApprovalMode == "never" || h.execConfig.ApprovalMode == "off" {
		return ""
	}
	return "Dangerous shell commands may return status=approval_required instead of running; wait for a human approval via the approval or exec RPC APIs instead of retrying."
}
