package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/google/uuid"
)

type BackgroundProcessConfig struct {
	Enabled             bool
	StopTimeout         time.Duration
	LogTailBytes        int
	MaxProcessesPerPeer int
}

type backgroundProcessStartParams struct {
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
}

type backgroundProcessRunParams struct {
	ID string `json:"id"`
}

type backgroundProcessLogsParams struct {
	ID       string `json:"id"`
	MaxBytes int    `json:"max_bytes,omitempty"`
}

type managedBackgroundProcess struct {
	id         string
	peerID     string
	command    string
	workdir    string
	stdoutPath string
	stderrPath string
	queuedAt   time.Time
	startedAt  time.Time
	cmd        *exec.Cmd

	mu            sync.Mutex
	stopRequested bool
}

func normalizeBackgroundProcessConfig(cfg BackgroundProcessConfig) BackgroundProcessConfig {
	if cfg.StopTimeout <= 0 {
		cfg.StopTimeout = 5 * time.Second
	}
	if cfg.LogTailBytes <= 0 {
		cfg.LogTailBytes = 64 * 1024
	}
	if cfg.MaxProcessesPerPeer <= 0 {
		cfg.MaxProcessesPerPeer = 8
	}
	return cfg
}

func (p *managedBackgroundProcess) pid() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *managedBackgroundProcess) markStopRequested() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.stopRequested = true
}

func (p *managedBackgroundProcess) wasStopRequested() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopRequested
}

func (h *Handler) startBackgroundProcess(_ context.Context, peerID string, p backgroundProcessStartParams) (map[string]any, error) {
	if !h.backgroundProcessConfig.Enabled {
		return nil, fmt.Errorf("process tool is disabled")
	}
	if h.workspaceStore == nil {
		return nil, fmt.Errorf("process tool is not enabled")
	}
	if h.runLedger == nil {
		return nil, fmt.Errorf("process tool requires the run ledger")
	}
	command := strings.TrimSpace(p.Command)
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	if reason, blocked := h.execNeedsApproval(command); blocked {
		return nil, fmt.Errorf("process start blocked by exec approval policy: %s", reason)
	}
	h.backgroundProcessesMu.Lock()
	active := 0
	for _, proc := range h.backgroundProcesses {
		if proc.peerID == peerID {
			active++
		}
	}
	h.backgroundProcessesMu.Unlock()
	if active >= h.backgroundProcessConfig.MaxProcessesPerPeer {
		return nil, fmt.Errorf("background process limit reached for peer (%d)", h.backgroundProcessConfig.MaxProcessesPerPeer)
	}
	workdir := "."
	if strings.TrimSpace(p.Workdir) != "" {
		workdir = p.Workdir
	}
	absWorkdir, err := h.workspaceStore.Resolve(peerID, workdir)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	stdoutRel, stderrRel := backgroundProcessLogPaths(id)
	stdoutAbs, err := h.workspaceStore.Resolve(peerID, stdoutRel)
	if err != nil {
		return nil, err
	}
	stderrAbs, err := h.workspaceStore.Resolve(peerID, stderrRel)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(stdoutAbs), 0o755); err != nil {
		return nil, fmt.Errorf("create process log dir: %w", err)
	}
	stdoutFile, err := os.Create(stdoutAbs)
	if err != nil {
		return nil, fmt.Errorf("create stdout log: %w", err)
	}
	stderrFile, err := os.Create(stderrAbs)
	if err != nil {
		_ = stdoutFile.Close()
		return nil, fmt.Errorf("create stderr log: %w", err)
	}
	requestPayload, err := json.Marshal(map[string]any{
		"command":     command,
		"workdir":     workdir,
		"stdout_path": stdoutRel,
		"stderr_path": stderrRel,
	})
	if err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return nil, fmt.Errorf("marshal process request: %w", err)
	}
	queuedAt := time.Now().UTC()
	if err := h.runLedger.Add(runledger.Record{
		ID:         id,
		Kind:       runledger.KindProcess,
		PeerID:     peerID,
		SessionKey: peerID,
		Status:     runledger.StatusQueued,
		Request:    requestPayload,
		QueuedAt:   queuedAt,
	}); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		return nil, err
	}
	shell, args := shellCommand(command)
	cmd := exec.Command(shell, args...)
	cmd.Dir = absWorkdir
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	configureBackgroundProcessCommand(cmd)
	if err := cmd.Start(); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		finishedAt := time.Now().UTC()
		resultPayload, _ := json.Marshal(map[string]any{
			"status":      "errored",
			"stdout_path": stdoutRel,
			"stderr_path": stderrRel,
		})
		_ = h.runLedger.Update(id, func(r *runledger.Record) {
			r.Status = runledger.StatusErrored
			r.Error = err.Error()
			r.Result = resultPayload
			r.FinishedAt = &finishedAt
		})
		return nil, fmt.Errorf("start background process: %w", err)
	}
	startedAt := time.Now().UTC()
	proc := &managedBackgroundProcess{
		id:         id,
		peerID:     peerID,
		command:    command,
		workdir:    workdir,
		stdoutPath: stdoutRel,
		stderrPath: stderrRel,
		queuedAt:   queuedAt,
		startedAt:  startedAt,
		cmd:        cmd,
	}
	h.backgroundProcessesMu.Lock()
	h.backgroundProcesses[id] = proc
	h.backgroundProcessesMu.Unlock()
	if err := h.runLedger.Update(id, func(r *runledger.Record) {
		r.Status = runledger.StatusRunning
		r.StartedAt = &startedAt
	}); err != nil {
		slog.Debug("process: failed to mark run started", "id", id, "error", err)
	}
	h.dispatchWG.Add(1)
	go h.waitBackgroundProcess(proc, stdoutFile, stderrFile)
	return map[string]any{
		"id":          id,
		"kind":        string(runledger.KindProcess),
		"status":      string(runledger.StatusRunning),
		"command":     command,
		"workdir":     workdir,
		"pid":         proc.pid(),
		"active":      true,
		"stdout_path": stdoutRel,
		"stderr_path": stderrRel,
		"queued_at":   queuedAt,
		"started_at":  startedAt,
	}, nil
}

func (h *Handler) waitBackgroundProcess(proc *managedBackgroundProcess, stdoutFile, stderrFile *os.File) {
	defer h.dispatchWG.Done()
	defer func() {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		h.backgroundProcessesMu.Lock()
		delete(h.backgroundProcesses, proc.id)
		h.backgroundProcessesMu.Unlock()
	}()
	err := proc.cmd.Wait()
	finishedAt := time.Now().UTC()
	result, status, errMsg := backgroundProcessResult(proc, finishedAt, err)
	resultPayload, marshalErr := json.Marshal(result)
	if marshalErr != nil {
		slog.Debug("process: failed to marshal result", "id", proc.id, "error", marshalErr)
	}
	if err := h.runLedger.Update(proc.id, func(r *runledger.Record) {
		r.Status = status
		r.Error = errMsg
		r.Result = resultPayload
		r.FinishedAt = &finishedAt
	}); err != nil {
		slog.Debug("process: failed to mark run finished", "id", proc.id, "error", err)
	}
}

func backgroundProcessResult(proc *managedBackgroundProcess, finishedAt time.Time, waitErr error) (map[string]any, runledger.RunStatus, string) {
	result := map[string]any{
		"command":     proc.command,
		"workdir":     proc.workdir,
		"pid":         proc.pid(),
		"stdout_path": proc.stdoutPath,
		"stderr_path": proc.stderrPath,
		"duration_ms": finishedAt.Sub(proc.startedAt).Milliseconds(),
	}
	if waitErr == nil {
		if proc.wasStopRequested() {
			result["status"] = "canceled"
			result["exit_code"] = 0
			return result, runledger.StatusCanceled, "process stopped"
		}
		result["status"] = "completed"
		result["exit_code"] = 0
		return result, runledger.StatusCompleted, ""
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode := exitErr.ExitCode()
		result["exit_code"] = exitCode
		if proc.wasStopRequested() {
			result["status"] = "canceled"
			return result, runledger.StatusCanceled, "process stopped"
		}
		result["status"] = "errored"
		return result, runledger.StatusErrored, fmt.Sprintf("process exited with code %d", exitCode)
	}
	if proc.wasStopRequested() {
		result["status"] = "canceled"
		result["exit_code"] = -1
		return result, runledger.StatusCanceled, "process stopped"
	}
	result["status"] = "errored"
	result["exit_code"] = -1
	return result, runledger.StatusErrored, waitErr.Error()
}

func (h *Handler) backgroundProcessStatus(peerID, id string) (map[string]any, error) {
	rec, err := h.backgroundProcessRecord(peerID, id)
	if err != nil {
		return nil, err
	}
	stdoutRel, stderrRel := backgroundProcessLogPaths(id)
	result := map[string]any{
		"id":          rec.ID,
		"kind":        string(rec.Kind),
		"status":      string(rec.Status),
		"error":       rec.Error,
		"queued_at":   rec.QueuedAt,
		"started_at":  rec.StartedAt,
		"finished_at": rec.FinishedAt,
		"active":      h.backgroundProcessActive(rec.ID),
		"stdout_path": stdoutRel,
		"stderr_path": stderrRel,
	}
	if decoded, ok := decodeRawJSON(rec.Request); ok {
		result["request"] = decoded
	}
	if decoded, ok := decodeRawJSON(rec.Result); ok {
		result["result"] = decoded
	}
	if pid := h.backgroundProcessPID(rec.ID); pid > 0 {
		result["pid"] = pid
	}
	return result, nil
}

func (h *Handler) stopBackgroundProcess(peerID, id string) (map[string]any, error) {
	rec, err := h.backgroundProcessRecord(peerID, id)
	if err != nil {
		return nil, err
	}
	if isTerminalRunStatus(rec.Status) {
		return map[string]any{
			"id":     rec.ID,
			"kind":   string(rec.Kind),
			"status": string(rec.Status),
			"error":  rec.Error,
		}, nil
	}
	h.backgroundProcessesMu.Lock()
	proc := h.backgroundProcesses[id]
	h.backgroundProcessesMu.Unlock()
	if proc == nil {
		return map[string]any{
			"id":     rec.ID,
			"kind":   string(rec.Kind),
			"status": string(rec.Status),
			"error":  rec.Error,
		}, nil
	}
	proc.markStopRequested()
	if err := requestBackgroundProcessStop(proc.cmd); err != nil {
		return nil, err
	}
	grace := h.backgroundProcessConfig.StopTimeout
	go func(id string, cmd *exec.Cmd, timeout time.Duration) {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		<-timer.C
		if h.backgroundProcessActive(id) {
			_ = killBackgroundProcess(cmd)
		}
	}(id, proc.cmd, grace)
	return map[string]any{
		"id":     rec.ID,
		"kind":   string(rec.Kind),
		"status": "stopping",
	}, nil
}

func (h *Handler) listBackgroundProcesses(peerID string, limit int) (map[string]any, error) {
	if h.runLedger == nil {
		return nil, fmt.Errorf("run ledger is not enabled")
	}
	if limit <= 0 {
		limit = 20
	}
	records := h.runLedger.List(runledger.Filter{PeerID: peerID, Kind: runledger.KindProcess, Limit: limit}, 30*24*time.Hour)
	processes := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		stdoutRel, stderrRel := backgroundProcessLogPaths(rec.ID)
		item := map[string]any{
			"id":          rec.ID,
			"kind":        string(rec.Kind),
			"status":      string(rec.Status),
			"error":       rec.Error,
			"queued_at":   rec.QueuedAt,
			"started_at":  rec.StartedAt,
			"finished_at": rec.FinishedAt,
			"active":      h.backgroundProcessActive(rec.ID),
			"stdout_path": stdoutRel,
			"stderr_path": stderrRel,
		}
		if decoded, ok := decodeRawJSON(rec.Request); ok {
			if reqMap, ok := decoded.(map[string]any); ok {
				if command, ok := reqMap["command"]; ok {
					item["command"] = command
				}
				if workdir, ok := reqMap["workdir"]; ok {
					item["workdir"] = workdir
				}
			}
		}
		if pid := h.backgroundProcessPID(rec.ID); pid > 0 {
			item["pid"] = pid
		}
		processes = append(processes, item)
	}
	return map[string]any{"processes": processes}, nil
}

func (h *Handler) backgroundProcessLogs(peerID, id string, maxBytes int) (map[string]any, error) {
	rec, err := h.backgroundProcessRecord(peerID, id)
	if err != nil {
		return nil, err
	}
	limit := h.backgroundProcessConfig.LogTailBytes
	if maxBytes > 0 && maxBytes < limit {
		limit = maxBytes
	}
	stdoutRel, stderrRel := backgroundProcessLogPaths(id)
	stdoutAbs, err := h.workspaceStore.Resolve(peerID, stdoutRel)
	if err != nil {
		return nil, err
	}
	stderrAbs, err := h.workspaceStore.Resolve(peerID, stderrRel)
	if err != nil {
		return nil, err
	}
	stdoutText, stdoutTruncated, err := readTail(stdoutAbs, limit)
	if err != nil {
		return nil, err
	}
	stderrText, stderrTruncated, err := readTail(stderrAbs, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"id":               rec.ID,
		"kind":             string(rec.Kind),
		"status":           string(rec.Status),
		"stdout":           stdoutText,
		"stderr":           stderrText,
		"stdout_truncated": stdoutTruncated,
		"stderr_truncated": stderrTruncated,
		"stdout_path":      stdoutRel,
		"stderr_path":      stderrRel,
	}, nil
}

func (h *Handler) backgroundProcessRecord(peerID, id string) (runledger.Record, error) {
	if h.runLedger == nil {
		return runledger.Record{}, fmt.Errorf("run ledger is not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return runledger.Record{}, fmt.Errorf("id is required")
	}
	rec, ok := h.runLedger.Get(id)
	if !ok {
		return runledger.Record{}, fmt.Errorf("background process run not found")
	}
	if rec.Kind != runledger.KindProcess {
		return runledger.Record{}, fmt.Errorf("run %q is not a background_process run", id)
	}
	if rec.PeerID != "" && rec.PeerID != peerID {
		return runledger.Record{}, fmt.Errorf("background process run not found")
	}
	return rec, nil
}

func (h *Handler) backgroundProcessActive(id string) bool {
	h.backgroundProcessesMu.Lock()
	defer h.backgroundProcessesMu.Unlock()
	_, ok := h.backgroundProcesses[id]
	return ok
}

func (h *Handler) backgroundProcessPID(id string) int {
	h.backgroundProcessesMu.Lock()
	defer h.backgroundProcessesMu.Unlock()
	if proc := h.backgroundProcesses[id]; proc != nil {
		return proc.pid()
	}
	return 0
}

func (h *Handler) stopAllBackgroundProcesses() {
	h.backgroundProcessesMu.Lock()
	active := make([]*managedBackgroundProcess, 0, len(h.backgroundProcesses))
	for _, proc := range h.backgroundProcesses {
		active = append(active, proc)
	}
	h.backgroundProcessesMu.Unlock()
	for _, proc := range active {
		proc.markStopRequested()
		if err := requestBackgroundProcessStop(proc.cmd); err != nil {
			slog.Debug("process: shutdown stop failed", "id", proc.id, "error", err)
			continue
		}
		go func(id string, cmd *exec.Cmd, timeout time.Duration) {
			timer := time.NewTimer(timeout)
			defer timer.Stop()
			<-timer.C
			if h.backgroundProcessActive(id) {
				_ = killBackgroundProcess(cmd)
			}
		}(proc.id, proc.cmd, h.backgroundProcessConfig.StopTimeout)
	}
}

func backgroundProcessLogPaths(id string) (stdoutPath, stderrPath string) {
	base := filepath.ToSlash(filepath.Join(".koios", "processes", id))
	return filepath.ToSlash(filepath.Join(base, "stdout.log")), filepath.ToSlash(filepath.Join(base, "stderr.log"))
}

func readTail(path string, maxBytes int) (string, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", false, err
	}
	if maxBytes <= 0 || info.Size() <= int64(maxBytes) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", false, err
		}
		return string(b), false, nil
	}
	offset := info.Size() - int64(maxBytes)
	if _, err := f.Seek(offset, 0); err != nil {
		return "", false, err
	}
	b := make([]byte, maxBytes)
	n, err := f.Read(b)
	if err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, os.ErrNotExist) {
		if !errors.Is(err, io.EOF) {
			return "", false, err
		}
	}
	return string(b[:n]), true, nil
}

func isTerminalRunStatus(status runledger.RunStatus) bool {
	switch status {
	case runledger.StatusCompleted, runledger.StatusErrored, runledger.StatusCanceled, runledger.StatusSkipped:
		return true
	default:
		return false
	}
}
