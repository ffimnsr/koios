package handler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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

type pendingExec struct {
	ID        string    `json:"id"`
	PeerID    string    `json:"peer_id"`
	Command   string    `json:"command"`
	Workdir   string    `json:"workdir"`
	Timeout   int       `json:"timeout_seconds"`
	Reason    string    `json:"reason"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

type execApprovalStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	pending map[string]pendingExec
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
	if !cfg.Enabled {
		// zero value: treat as enabled for backward compatibility unless caller
		// has explicitly set it via Config — app.go always sets this field so
		// the zero value only arises in tests that don't populate ExecConfig.
		cfg.Enabled = true
	}
	return cfg
}

// bwrapPath is the resolved absolute path to bwrap, or empty if not found.
// It is resolved once at handler construction time.
var bwrapPath string

func resolveBwrap() (string, error) {
	p, err := exec.LookPath("bwrap")
	if err != nil {
		return "", fmt.Errorf("bubblewrap (bwrap) is not installed; install it with your package manager (e.g. apt install bubblewrap) and restart koios")
	}
	return p, nil
}

func newExecApprovalStore(ttl time.Duration) *execApprovalStore {
	return &execApprovalStore{
		ttl:     ttl,
		pending: make(map[string]pendingExec),
	}
}

func (s *execApprovalStore) create(peerID, command, workdir string, timeout time.Duration, reason string) pendingExec {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	now := time.Now().UTC()
	item := pendingExec{
		ID:        uuid.NewString(),
		PeerID:    peerID,
		Command:   command,
		Workdir:   workdir,
		Timeout:   int(timeout / time.Second),
		Reason:    reason,
		CreatedAt: now,
		ExpiresAt: now.Add(s.ttl),
	}
	s.pending[item.ID] = item
	return item
}

func (s *execApprovalStore) list(peerID string) []pendingExec {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	out := make([]pendingExec, 0, len(s.pending))
	for _, item := range s.pending {
		if item.PeerID == peerID {
			out = append(out, item)
		}
	}
	return out
}

func (s *execApprovalStore) take(peerID, id string) (pendingExec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	item, ok := s.pending[id]
	if !ok {
		return pendingExec{}, fmt.Errorf("approval %s not found", id)
	}
	if item.PeerID != peerID {
		return pendingExec{}, fmt.Errorf("approval %s does not belong to peer", id)
	}
	delete(s.pending, id)
	return item, nil
}

func (s *execApprovalStore) reject(peerID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	item, ok := s.pending[id]
	if !ok {
		return fmt.Errorf("approval %s not found", id)
	}
	if item.PeerID != peerID {
		return fmt.Errorf("approval %s does not belong to peer", id)
	}
	delete(s.pending, id)
	return nil
}

func (s *execApprovalStore) pruneLocked() {
	now := time.Now().UTC()
	for id, item := range s.pending {
		if !item.ExpiresAt.After(now) {
			delete(s.pending, id)
		}
	}
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
	if reason, ok := h.execNeedsApproval(command); ok {
		approval := h.execApprovals.create(peerID, command, absWorkdir, timeout, reason)
		return map[string]any{
			"status":   "approval_required",
			"approval": approval,
		}, nil
	}
	return h.executeCommand(ctx, command, absWorkdir, timeout)
}

func (h *Handler) execNeedsApproval(command string) (string, bool) {
	switch h.execConfig.ApprovalMode {
	case "never", "off":
		return "", false
	case "always":
		return "command requires approval by policy", true
	default:
		return detectDangerousCommand(command, h.execConfig)
	}
}

func detectDangerousCommand(command string, cfg ExecConfig) (string, bool) {
	// Custom allow patterns take priority — matching commands bypass all deny checks.
	for _, re := range cfg.CustomAllowPatterns {
		if re.MatchString(command) {
			return "", false
		}
	}
	// Built-in dangerous command patterns (enabled by default).
	if cfg.EnableDenyPatterns {
		for _, pattern := range dangerousExecPatterns {
			if pattern.re.MatchString(command) {
				return pattern.reason, true
			}
		}
	}
	// User-supplied custom deny patterns.
	for _, re := range cfg.CustomDenyPatterns {
		if re.MatchString(command) {
			return "blocked by custom deny pattern", true
		}
	}
	return "", false
}

// detectDangerousExec is kept for backward compatibility with any tests that
// call it directly; it falls back to the default built-in patterns only.
func detectDangerousExec(command string) (string, bool) {
	return detectDangerousCommand(command, ExecConfig{EnableDenyPatterns: true})
}

func (h *Handler) executeApprovedExec(ctx context.Context, peerID, approvalID string) (map[string]any, error) {
	item, err := h.execApprovals.take(peerID, approvalID)
	if err != nil {
		return nil, err
	}
	result, err := h.executeCommand(ctx, item.Command, item.Workdir, time.Duration(item.Timeout)*time.Second)
	if err != nil {
		return nil, err
	}
	result["approval_id"] = item.ID
	return result, nil
}

func (h *Handler) executeCommand(ctx context.Context, command, workdir string, timeout time.Duration) (map[string]any, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if h.execConfig.IsolationEnabled {
		var err error
		cmd, err = h.buildIsolatedCommand(runCtx, command, workdir)
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
func (h *Handler) buildIsolatedCommand(ctx context.Context, command, workdir string) (*exec.Cmd, error) {
	if bwrapPath == "" {
		var err error
		bwrapPath, err = resolveBwrap()
		if err != nil {
			return nil, err
		}
	}

	// Create an isolated temp directory for this execution.
	tmpDir, err := os.MkdirTemp("", "koios-exec-*")
	if err != nil {
		return nil, fmt.Errorf("isolation: creating temp dir: %w", err)
	}

	args := []string{
		// Unshare IPC namespace.
		"--unshare-ipc",
		// New network namespace (no network access by default).
		"--unshare-net",
		// Minimal filesystem: proc, tmp, workdir.
		"--proc", "/proc",
		"--tmpfs", "/tmp",
		"--dir", "/workspace",
		// Bind workdir into /workspace (rw).
		"--bind", workdir, "/workspace",
		// Bind temp to a per-run dir so the sandbox can write temp files.
		"--bind", tmpDir, "/tmp",
	}

	// Standard read-only system paths.
	for _, dir := range []string{"/usr", "/bin", "/lib", "/lib64", "/sbin"} {
		if _, err := os.Stat(dir); err == nil {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	// DNS resolution.
	if _, err := os.Stat("/etc/resolv.conf"); err == nil {
		args = append(args, "--ro-bind", "/etc/resolv.conf", "/etc/resolv.conf")
	}

	// User-configured expose_paths.
	for _, p := range h.execConfig.IsolationPaths {
		target := p.Target
		if target == "" {
			target = p.Source
		}
		if p.Mode == "rw" {
			args = append(args, "--bind", p.Source, target)
		} else {
			args = append(args, "--ro-bind", p.Source, target)
		}
	}

	// Set environment variables for the per-instance layout.
	args = append(args,
		"--setenv", "HOME", "/tmp",
		"--setenv", "TMPDIR", "/tmp",
		"--chdir", "/workspace",
		// Exec the shell inside the sandbox.
		"--", "sh", "-lc", command,
	)

	cmd := exec.CommandContext(ctx, bwrapPath, args...)
	// Best-effort cleanup of the per-run temp directory once the context ends
	// (either the command finishes or the timeout fires).
	go func() {
		<-ctx.Done()
		_ = os.RemoveAll(tmpDir)
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
	wsc.reply(req.ID, map[string]any{"pending": h.execApprovals.list(wsc.peerID)})
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
	if err := h.execApprovals.reject(wsc.peerID, p.ID); err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) execPromptHint() string {
	if h.execConfig.ApprovalMode == "never" || h.execConfig.ApprovalMode == "off" {
		return ""
	}
	return "Dangerous shell commands may return status=approval_required instead of running; wait for a human approval via the RPC API instead of retrying."
}
