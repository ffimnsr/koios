package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const defaultPATH = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

type BindMount struct {
	Source   string
	Target   string
	ReadOnly bool
}

type Limits struct {
	Timeout          time.Duration
	MaxStdoutBytes   int
	MaxStderrBytes   int
	MaxArtifactBytes int64
	MaxOpenFiles     int
	MaxProcesses     int
	CPUSeconds       int
	MemoryBytes      int64
}

type Request struct {
	Command        string
	WorkspaceRoot  string
	Workdir        string
	NetworkEnabled bool
	ExtraMounts    []BindMount
	Env            map[string]string
	Limits         Limits
}

type Result struct {
	Status           string        `json:"status"`
	ExitCode         int           `json:"exit_code"`
	Stdout           string        `json:"stdout"`
	Stderr           string        `json:"stderr"`
	Duration         time.Duration `json:"duration"`
	TimedOut         bool          `json:"timed_out"`
	StdoutTruncated  bool          `json:"stdout_truncated"`
	StderrTruncated  bool          `json:"stderr_truncated"`
	ArtifactPaths    []string      `json:"artifacts"`
	ArtifactsTrimmed bool          `json:"artifacts_truncated"`
}

type Runner struct {
	ResolveBubblewrap func() (string, error)
}

func NewRunner() *Runner {
	return &Runner{ResolveBubblewrap: resolveBubblewrap}
}

func resolveBubblewrap() (string, error) {
	p, err := exec.LookPath("bwrap")
	if err != nil {
		return "", fmt.Errorf("bubblewrap (bwrap) is not installed; install it with your package manager (e.g. apt install bubblewrap) and restart koios")
	}
	return p, nil
}

func (r *Runner) BuildBubblewrapCommand(ctx context.Context, req Request) (*exec.Cmd, func(), error) {
	if r == nil {
		r = NewRunner()
	}
	if r.ResolveBubblewrap == nil {
		r.ResolveBubblewrap = resolveBubblewrap
	}
	bwrapPath, err := r.ResolveBubblewrap()
	if err != nil {
		return nil, nil, err
	}
	workspaceRoot, err := filepath.Abs(req.WorkspaceRoot)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	relWorkdir := strings.TrimSpace(req.Workdir)
	if relWorkdir == "" {
		relWorkdir = "."
	}
	relWorkdir = filepath.Clean(relWorkdir)
	if relWorkdir == "." {
		relWorkdir = ""
	}
	insideSandbox := "/workspace"
	if relWorkdir != "" {
		insideSandbox = filepath.ToSlash(filepath.Join(insideSandbox, relWorkdir))
	}
	tmpDir, err := os.MkdirTemp("", "koios-codeexec-*")
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox temp dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }
	args := []string{
		"--die-with-parent",
		"--new-session",
		"--unshare-all",
		"--clearenv",
	}
	if req.NetworkEnabled {
		args = append(args, "--share-net")
	}
	args = append(args,
		"--proc", "/proc",
		"--dev", "/dev",
		"--dir", "/workspace",
		"--bind", workspaceRoot, "/workspace",
		"--bind", tmpDir, "/tmp",
	)
	for _, dir := range []string{"/usr", "/bin", "/lib", "/lib64", "/sbin", "/etc", "/opt"} {
		if _, err := os.Stat(dir); err == nil {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	for _, mount := range req.ExtraMounts {
		target := strings.TrimSpace(mount.Target)
		if target == "" {
			target = mount.Source
		}
		if mount.ReadOnly {
			args = append(args, "--ro-bind", mount.Source, target)
		} else {
			args = append(args, "--bind", mount.Source, target)
		}
	}
	env := map[string]string{
		"PATH":   defaultPATH,
		"HOME":   "/tmp",
		"TMPDIR": "/tmp",
	}
	for k, v := range req.Env {
		env[k] = v
	}
	args = append(args,
		"--setenv", "PATH", env["PATH"],
		"--setenv", "HOME", env["HOME"],
		"--setenv", "TMPDIR", env["TMPDIR"],
		"--setenv", "KOIOS_SANDBOX_COMMAND", req.Command,
		"--chdir", insideSandbox,
		"--", "sh", "-c", buildPrelude(req.Limits),
	)
	cmd := exec.CommandContext(ctx, bwrapPath, args...)
	return cmd, cleanup, nil
}

func buildPrelude(limits Limits) string {
	var parts []string
	if limits.CPUSeconds > 0 {
		parts = append(parts, fmt.Sprintf("ulimit -t %d", limits.CPUSeconds))
	}
	if limits.MemoryBytes > 0 {
		parts = append(parts, fmt.Sprintf("ulimit -v %d", limits.MemoryBytes/1024))
	}
	if limits.MaxOpenFiles > 0 {
		parts = append(parts, fmt.Sprintf("ulimit -n %d", limits.MaxOpenFiles))
	}
	if limits.MaxProcesses > 0 {
		parts = append(parts, fmt.Sprintf("ulimit -u %d", limits.MaxProcesses))
	}
	if limits.MaxArtifactBytes > 0 {
		blocks := limits.MaxArtifactBytes / 512
		if limits.MaxArtifactBytes%512 != 0 {
			blocks++
		}
		if blocks < 1 {
			blocks = 1
		}
		parts = append(parts, fmt.Sprintf("ulimit -f %d", blocks))
	}
	parts = append(parts, `exec sh -c "$KOIOS_SANDBOX_COMMAND"`)
	return strings.Join(parts, "; ")
}

func (r *Runner) Run(ctx context.Context, req Request) (*Result, error) {
	if strings.TrimSpace(req.Command) == "" {
		return nil, fmt.Errorf("command is required")
	}
	if strings.TrimSpace(req.WorkspaceRoot) == "" {
		return nil, fmt.Errorf("workspace root is required")
	}
	runCtx := ctx
	var cancel context.CancelFunc
	if req.Limits.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, req.Limits.Timeout)
		defer cancel()
	}
	before, err := snapshotWorkspace(req.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	cmd, cleanup, err := r.BuildBubblewrapCommand(runCtx, req)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	stdoutCapture := newLimitedBuffer(req.Limits.MaxStdoutBytes)
	stderrCapture := newLimitedBuffer(req.Limits.MaxStderrBytes)
	cmd.Stdout = stdoutCapture
	cmd.Stderr = stderrCapture
	started := time.Now()
	err = cmd.Run()
	duration := time.Since(started)
	timedOut := errors.Is(runCtx.Err(), context.DeadlineExceeded)
	result := &Result{
		Status:          "completed",
		ExitCode:        0,
		Stdout:          stdoutCapture.String(),
		Stderr:          stderrCapture.String(),
		Duration:        duration,
		TimedOut:        timedOut,
		StdoutTruncated: stdoutCapture.Truncated(),
		StderrTruncated: stderrCapture.Truncated(),
	}
	if err != nil {
		var exitErr *exec.ExitError
		switch {
		case errors.As(err, &exitErr):
			result.ExitCode = exitErr.ExitCode()
		case timedOut:
			result.ExitCode = -1
		default:
			return nil, err
		}
	}
	if timedOut {
		result.Status = "timed_out"
	}
	after, err := snapshotWorkspace(req.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	result.ArtifactPaths, result.ArtifactsTrimmed = diffArtifacts(before, after, req.Limits.MaxArtifactBytes)
	return result, nil
}

type fileSnapshot struct {
	Size    int64
	ModTime time.Time
	IsDir   bool
}

func snapshotWorkspace(root string) (map[string]fileSnapshot, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace snapshot root: %w", err)
	}
	entries := make(map[string]fileSnapshot)
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		entries[filepath.ToSlash(rel)] = fileSnapshot{Size: info.Size(), ModTime: info.ModTime(), IsDir: d.IsDir()}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot workspace: %w", err)
	}
	return entries, nil
}

func diffArtifacts(before, after map[string]fileSnapshot, maxArtifactBytes int64) ([]string, bool) {
	var paths []string
	trimmed := false
	for path, now := range after {
		if now.IsDir {
			continue
		}
		prev, ok := before[path]
		if ok && prev.Size == now.Size && prev.ModTime.Equal(now.ModTime) {
			continue
		}
		if maxArtifactBytes > 0 && now.Size > maxArtifactBytes {
			trimmed = true
			continue
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, trimmed
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.limit <= 0 {
		return b.buf.Write(p)
	}
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			_, _ = b.buf.Write(p[:remaining])
			b.truncated = true
			return len(p), nil
		}
		_, _ = b.buf.Write(p)
		return len(p), nil
	}
	b.truncated = true
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

func (b *limitedBuffer) Truncated() bool {
	return b.truncated
}

var _ io.Writer = (*limitedBuffer)(nil)
