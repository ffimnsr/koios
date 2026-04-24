package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/sandbox"
	"github.com/google/uuid"
)

type CodeExecutionConfig struct {
	Enabled          bool
	NetworkEnabled   bool
	DefaultTimeout   time.Duration
	MaxTimeout       time.Duration
	MaxStdoutBytes   int
	MaxStderrBytes   int
	MaxArtifactBytes int64
	MaxOpenFiles     int
	MaxProcesses     int
	CPUSeconds       int
	MemoryBytes      int64
}

type codeExecutionParams struct {
	Command string `json:"command"`
	Workdir string `json:"workdir,omitempty"`
	Timeout int    `json:"timeout_seconds,omitempty"`
	Async   bool   `json:"async,omitempty"`
}

type codeExecutionRunParams struct {
	ID string `json:"id"`
}

type preparedCodeExecution struct {
	command        string
	workdir        string
	timeoutSeconds int
	request        sandbox.Request
}

var defaultSandboxRunner = sandbox.NewRunner()

func normalizeCodeExecutionConfig(cfg CodeExecutionConfig) CodeExecutionConfig {
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 10 * time.Second
	}
	if cfg.MaxTimeout <= 0 {
		cfg.MaxTimeout = 30 * time.Second
	}
	if cfg.MaxTimeout < cfg.DefaultTimeout {
		cfg.MaxTimeout = cfg.DefaultTimeout
	}
	if cfg.MaxStdoutBytes <= 0 {
		cfg.MaxStdoutBytes = 64 * 1024
	}
	if cfg.MaxStderrBytes <= 0 {
		cfg.MaxStderrBytes = 64 * 1024
	}
	if cfg.MaxArtifactBytes <= 0 {
		cfg.MaxArtifactBytes = 1 << 20
	}
	if cfg.MaxOpenFiles <= 0 {
		cfg.MaxOpenFiles = 64
	}
	if cfg.MaxProcesses <= 0 {
		cfg.MaxProcesses = 32
	}
	if cfg.CPUSeconds <= 0 {
		cfg.CPUSeconds = 10
	}
	if cfg.MemoryBytes <= 0 {
		cfg.MemoryBytes = 256 << 20
	}
	return cfg
}

func (h *Handler) runCodeExecutionTool(ctx context.Context, peerID string, p codeExecutionParams) (map[string]any, error) {
	prepared, err := h.prepareCodeExecution(peerID, p)
	if err != nil {
		return nil, err
	}
	if p.Async {
		return h.startCodeExecutionJob(peerID, prepared)
	}
	result, err := h.executeCodeExecution(ctx, prepared)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (h *Handler) prepareCodeExecution(peerID string, p codeExecutionParams) (preparedCodeExecution, error) {
	if !h.codeExecutionConfig.Enabled {
		return preparedCodeExecution{}, fmt.Errorf("code_execution tool is disabled")
	}
	if h.workspaceStore == nil {
		return preparedCodeExecution{}, fmt.Errorf("code_execution is not enabled")
	}
	command := strings.TrimSpace(p.Command)
	if command == "" {
		return preparedCodeExecution{}, fmt.Errorf("command is required")
	}
	timeout := h.codeExecutionConfig.DefaultTimeout
	if p.Timeout > 0 {
		timeout = time.Duration(p.Timeout) * time.Second
	}
	if timeout > h.codeExecutionConfig.MaxTimeout {
		return preparedCodeExecution{}, fmt.Errorf("timeout exceeds max of %s", h.codeExecutionConfig.MaxTimeout)
	}
	workdir := "."
	if strings.TrimSpace(p.Workdir) != "" {
		workdir = p.Workdir
	}
	workspaceRoot, err := h.workspaceStore.EnsurePeer(peerID)
	if err != nil {
		return preparedCodeExecution{}, err
	}
	absWorkdir, err := h.workspaceStore.Resolve(peerID, workdir)
	if err != nil {
		return preparedCodeExecution{}, err
	}
	relWorkdir, err := filepath.Rel(workspaceRoot, absWorkdir)
	if err != nil {
		return preparedCodeExecution{}, fmt.Errorf("resolve code_execution workdir: %w", err)
	}
	if relWorkdir == "." {
		relWorkdir = ""
	}
	return preparedCodeExecution{
		command:        command,
		workdir:        workdir,
		timeoutSeconds: int(timeout / time.Second),
		request: sandbox.Request{
			Command:        command,
			WorkspaceRoot:  workspaceRoot,
			Workdir:        relWorkdir,
			NetworkEnabled: h.codeExecutionConfig.NetworkEnabled,
			Limits: sandbox.Limits{
				Timeout:          timeout,
				MaxStdoutBytes:   h.codeExecutionConfig.MaxStdoutBytes,
				MaxStderrBytes:   h.codeExecutionConfig.MaxStderrBytes,
				MaxArtifactBytes: h.codeExecutionConfig.MaxArtifactBytes,
				MaxOpenFiles:     h.codeExecutionConfig.MaxOpenFiles,
				MaxProcesses:     h.codeExecutionConfig.MaxProcesses,
				CPUSeconds:       h.codeExecutionConfig.CPUSeconds,
				MemoryBytes:      h.codeExecutionConfig.MemoryBytes,
			},
		},
	}, nil
}

func (h *Handler) executeCodeExecution(ctx context.Context, prepared preparedCodeExecution) (map[string]any, error) {
	result, err := defaultSandboxRunner.Run(ctx, prepared.request)
	if err != nil {
		return nil, err
	}
	return codeExecutionResultMap(prepared, result), nil
}

func (h *Handler) startCodeExecutionJob(peerID string, prepared preparedCodeExecution) (map[string]any, error) {
	if h.runLedger == nil {
		return nil, fmt.Errorf("async code_execution jobs require the run ledger")
	}
	id := uuid.NewString()
	queuedAt := time.Now().UTC()
	runCtx, runCancel := context.WithCancel(context.Background())
	requestPayload, err := json.Marshal(map[string]any{
		"command":         prepared.command,
		"workdir":         prepared.workdir,
		"timeout_seconds": prepared.timeoutSeconds,
		"async":           true,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal code_execution request: %w", err)
	}
	if err := h.runLedger.Add(runledger.Record{
		ID:         id,
		Kind:       runledger.KindCodeExecution,
		PeerID:     peerID,
		SessionKey: peerID,
		Status:     runledger.StatusQueued,
		Request:    requestPayload,
		QueuedAt:   queuedAt,
	}); err != nil {
		return nil, err
	}
	h.codeExecutionRunsMu.Lock()
	h.codeExecutionRuns[id] = runCancel
	h.codeExecutionRunsMu.Unlock()
	h.dispatchWG.Add(1)
	go func() {
		defer h.dispatchWG.Done()
		defer func() {
			h.codeExecutionRunsMu.Lock()
			delete(h.codeExecutionRuns, id)
			h.codeExecutionRunsMu.Unlock()
		}()
		defer func() {
			if recovered := recover(); recovered != nil {
				finishedAt := time.Now().UTC()
				msg := fmt.Sprintf("code_execution job panicked: %v", recovered)
				if err := h.runLedger.Update(id, func(r *runledger.Record) {
					r.Status = runledger.StatusErrored
					r.Error = msg
					r.FinishedAt = &finishedAt
				}); err != nil {
					slog.Debug("code_execution: failed to persist panic state", "id", id, "error", err)
				}
			}
		}()
		defer runCancel()
		startedAt := time.Now().UTC()
		if err := h.runLedger.Update(id, func(r *runledger.Record) {
			r.Status = runledger.StatusRunning
			r.StartedAt = &startedAt
		}); err != nil {
			slog.Debug("code_execution: failed to mark run started", "id", id, "error", err)
		}
		result, runErr := h.executeCodeExecution(runCtx, prepared)
		finishedAt := time.Now().UTC()
		status, errMsg := codeExecutionRunStatusWithContext(runCtx, result, runErr)
		if status == runledger.StatusCanceled && result != nil {
			result["status"] = "canceled"
		}
		var resultPayload json.RawMessage
		if result != nil {
			b, err := json.Marshal(result)
			if err != nil {
				slog.Debug("code_execution: failed to marshal async result", "id", id, "error", err)
			} else {
				resultPayload = b
			}
		}
		if err := h.runLedger.Update(id, func(r *runledger.Record) {
			r.Status = status
			r.Error = errMsg
			r.Result = resultPayload
			r.FinishedAt = &finishedAt
		}); err != nil {
			slog.Debug("code_execution: failed to mark run finished", "id", id, "error", err)
		}
	}()
	return map[string]any{
		"id":              id,
		"kind":            string(runledger.KindCodeExecution),
		"status":          string(runledger.StatusQueued),
		"command":         prepared.command,
		"workdir":         prepared.workdir,
		"timeout_seconds": prepared.timeoutSeconds,
		"async":           true,
	}, nil
}

func (h *Handler) codeExecutionStatus(peerID, id string) (map[string]any, error) {
	rec, err := h.codeExecutionRecord(peerID, id)
	if err != nil {
		return nil, err
	}
	result := map[string]any{
		"id":          rec.ID,
		"kind":        string(rec.Kind),
		"status":      string(rec.Status),
		"error":       rec.Error,
		"queued_at":   rec.QueuedAt,
		"started_at":  rec.StartedAt,
		"finished_at": rec.FinishedAt,
		"active":      h.codeExecutionRunActive(rec.ID),
	}
	if decoded, ok := decodeRawJSON(rec.Request); ok {
		result["request"] = decoded
	}
	if decoded, ok := decodeRawJSON(rec.Result); ok {
		result["result"] = decoded
	}
	return result, nil
}

func (h *Handler) codeExecutionCancel(peerID, id string) (map[string]any, error) {
	rec, err := h.codeExecutionRecord(peerID, id)
	if err != nil {
		return nil, err
	}
	if isTerminalCodeExecutionStatus(rec.Status) {
		return map[string]any{
			"id":     rec.ID,
			"kind":   string(rec.Kind),
			"status": string(rec.Status),
			"error":  rec.Error,
		}, nil
	}
	h.codeExecutionRunsMu.Lock()
	cancel, ok := h.codeExecutionRuns[id]
	h.codeExecutionRunsMu.Unlock()
	if ok {
		cancel()
		return map[string]any{
			"id":     rec.ID,
			"kind":   string(rec.Kind),
			"status": "canceling",
		}, nil
	}
	return map[string]any{
		"id":     rec.ID,
		"kind":   string(rec.Kind),
		"status": string(rec.Status),
		"error":  rec.Error,
	}, nil
}

func (h *Handler) codeExecutionRecord(peerID, id string) (runledger.Record, error) {
	if h.runLedger == nil {
		return runledger.Record{}, fmt.Errorf("run ledger is not enabled")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return runledger.Record{}, fmt.Errorf("id is required")
	}
	rec, ok := h.runLedger.Get(id)
	if !ok {
		return runledger.Record{}, fmt.Errorf("code_execution run not found")
	}
	if rec.Kind != runledger.KindCodeExecution {
		return runledger.Record{}, fmt.Errorf("run %q is not a code_execution run", id)
	}
	if rec.PeerID != "" && rec.PeerID != peerID {
		return runledger.Record{}, fmt.Errorf("code_execution run not found")
	}
	return rec, nil
}

func (h *Handler) codeExecutionRunActive(id string) bool {
	h.codeExecutionRunsMu.Lock()
	defer h.codeExecutionRunsMu.Unlock()
	_, ok := h.codeExecutionRuns[id]
	return ok
}

func decodeRawJSON(raw json.RawMessage) (any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return string(raw), true
	}
	return decoded, true
}

func isTerminalCodeExecutionStatus(status runledger.RunStatus) bool {
	switch status {
	case runledger.StatusCompleted, runledger.StatusErrored, runledger.StatusCanceled, runledger.StatusSkipped:
		return true
	default:
		return false
	}
}

func codeExecutionResultMap(prepared preparedCodeExecution, result *sandbox.Result) map[string]any {
	return map[string]any{
		"status":              result.Status,
		"command":             prepared.command,
		"workdir":             prepared.workdir,
		"timeout_seconds":     prepared.timeoutSeconds,
		"exit_code":           result.ExitCode,
		"stdout":              result.Stdout,
		"stderr":              result.Stderr,
		"duration_ms":         result.Duration.Milliseconds(),
		"timed_out":           result.TimedOut,
		"stdout_truncated":    result.StdoutTruncated,
		"stderr_truncated":    result.StderrTruncated,
		"artifacts":           result.ArtifactPaths,
		"artifacts_truncated": result.ArtifactsTrimmed,
	}
}

func codeExecutionRunStatusWithContext(ctx context.Context, result map[string]any, runErr error) (runledger.RunStatus, string) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return runledger.StatusCanceled, context.Canceled.Error()
	}
	return codeExecutionRunStatus(result, runErr)
}

func codeExecutionRunStatus(result map[string]any, runErr error) (runledger.RunStatus, string) {
	if runErr != nil {
		if errors.Is(runErr, context.Canceled) {
			return runledger.StatusCanceled, runErr.Error()
		}
		return runledger.StatusErrored, runErr.Error()
	}
	if result == nil {
		return runledger.StatusErrored, "code_execution did not produce a result"
	}
	if timedOut, _ := result["timed_out"].(bool); timedOut {
		return runledger.StatusErrored, "code_execution timed out"
	}
	if exitCode, ok := result["exit_code"].(int); ok && exitCode != 0 {
		return runledger.StatusErrored, fmt.Sprintf("code_execution exited with code %d", exitCode)
	}
	if exitCode, ok := result["exit_code"].(float64); ok && int(exitCode) != 0 {
		return runledger.StatusErrored, fmt.Sprintf("code_execution exited with code %d", int(exitCode))
	}
	return runledger.StatusCompleted, ""
}
