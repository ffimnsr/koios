package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildBubblewrapCommandIncludesHardenedFlags(t *testing.T) {
	runner := &Runner{ResolveBubblewrap: func() (string, error) { return "/usr/bin/bwrap", nil }}
	workspaceRoot := t.TempDir()
	cmd, cleanup, err := runner.BuildBubblewrapCommand(context.Background(), Request{
		Command:       "echo hi",
		WorkspaceRoot: workspaceRoot,
		Workdir:       "nested",
		Limits: Limits{
			CPUSeconds:       7,
			MemoryBytes:      64 << 20,
			MaxOpenFiles:     32,
			MaxProcesses:     16,
			MaxArtifactBytes: 4096,
		},
	})
	if err != nil {
		t.Fatalf("BuildBubblewrapCommand: %v", err)
	}
	t.Cleanup(cleanup)
	args := strings.Join(cmd.Args, " ")
	for _, want := range []string{"--die-with-parent", "--new-session", "--unshare-all", "--clearenv", "--setenv HOME /tmp", "--setenv TMPDIR /tmp", "--chdir /workspace/nested"} {
		if !strings.Contains(args, want) {
			t.Fatalf("expected args to contain %q, got %s", want, args)
		}
	}
	if strings.Contains(args, "--share-net") {
		t.Fatalf("expected network to stay disabled by default, got %s", args)
	}
	if !strings.Contains(args, `ulimit -t 7`) || !strings.Contains(args, `ulimit -n 32`) {
		t.Fatalf("expected ulimit prelude in args, got %s", args)
	}
}

func TestRunnerMissingBubblewrap(t *testing.T) {
	runner := &Runner{ResolveBubblewrap: func() (string, error) { return "", errors.New("missing") }}
	_, err := runner.Run(context.Background(), Request{
		Command:       "echo hi",
		WorkspaceRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing bubblewrap error, got %v", err)
	}
}

func TestRunnerTruncatesOutputAndCollectsArtifactsWithFakeBubblewrap(t *testing.T) {
	fake := writeFakeBubblewrap(t)
	workspaceRoot := t.TempDir()
	runner := &Runner{ResolveBubblewrap: func() (string, error) { return fake, nil }}
	result, err := runner.Run(context.Background(), Request{
		Command:       `printf '1234567890'; printf 'abcdefghij' 1>&2; printf 'artifact' > out.txt`,
		WorkspaceRoot: workspaceRoot,
		Limits: Limits{
			MaxStdoutBytes:   5,
			MaxStderrBytes:   4,
			MaxArtifactBytes: 1024,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Stdout != "12345" || !result.StdoutTruncated {
		t.Fatalf("unexpected stdout result: %#v", result)
	}
	if result.Stderr != "abcd" || !result.StderrTruncated {
		t.Fatalf("unexpected stderr result: %#v", result)
	}
	if len(result.ArtifactPaths) != 1 || result.ArtifactPaths[0] != "out.txt" {
		t.Fatalf("unexpected artifacts: %#v", result.ArtifactPaths)
	}
}

func TestRunnerTimeoutWithFakeBubblewrap(t *testing.T) {
	fake := writeFakeBubblewrap(t)
	runner := &Runner{ResolveBubblewrap: func() (string, error) { return fake, nil }}
	result, err := runner.Run(context.Background(), Request{
		Command:       `sleep 2`,
		WorkspaceRoot: t.TempDir(),
		Limits: Limits{
			Timeout: 200 * time.Millisecond,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.TimedOut || result.Status != "timed_out" {
		t.Fatalf("expected timeout result, got %#v", result)
	}
}

func TestRunnerRealBubblewrapConfinesWorkspaceAndAllowsWrites(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap not installed")
	}
	workspaceRoot := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "secret.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	runner := &Runner{ResolveBubblewrap: func() (string, error) { return bwrap, nil }}
	writeResult, err := runner.Run(context.Background(), Request{
		Command:       `mkdir -p nested && printf 'ok' > nested/out.txt && cat nested/out.txt`,
		WorkspaceRoot: workspaceRoot,
		Limits: Limits{
			Timeout:          5 * time.Second,
			MaxStdoutBytes:   1024,
			MaxStderrBytes:   1024,
			MaxArtifactBytes: 1024,
		},
	})
	if err != nil {
		t.Fatalf("write Run: %v", err)
	}
	if writeResult.ExitCode != 0 || strings.TrimSpace(writeResult.Stdout) != "ok" {
		t.Fatalf("unexpected writable workspace result: %#v", writeResult)
	}
	if len(writeResult.ArtifactPaths) != 1 || writeResult.ArtifactPaths[0] != "nested/out.txt" {
		t.Fatalf("unexpected writable workspace artifacts: %#v", writeResult.ArtifactPaths)
	}
	blockedResult, err := runner.Run(context.Background(), Request{
		Command:       `cat ` + shellSingleQuote(outsideFile) + ` > /workspace/leak.txt`,
		WorkspaceRoot: workspaceRoot,
		Limits: Limits{
			Timeout:          5 * time.Second,
			MaxStdoutBytes:   1024,
			MaxStderrBytes:   1024,
			MaxArtifactBytes: 1024,
		},
	})
	if err != nil {
		t.Fatalf("blocked Run: %v", err)
	}
	if blockedResult.ExitCode == 0 {
		t.Fatalf("expected outside read to fail, got %#v", blockedResult)
	}
	leaked, err := os.ReadFile(filepath.Join(workspaceRoot, "leak.txt"))
	if err != nil {
		t.Fatalf("read leak.txt: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("expected blocked outside read to leave leak.txt empty, got %q", string(leaked))
	}
}

func TestRunnerRealBubblewrapDisablesNetworkByDefault(t *testing.T) {
	bwrap, err := exec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap not installed")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	runner := &Runner{ResolveBubblewrap: func() (string, error) { return bwrap, nil }}
	result, err := runner.Run(context.Background(), Request{
		Command:       shellSingleQuote(python) + ` -c "import socket; socket.create_connection(('1.1.1.1', 53), 1)"`,
		WorkspaceRoot: t.TempDir(),
		Limits: Limits{
			Timeout:        5 * time.Second,
			MaxStdoutBytes: 1024,
			MaxStderrBytes: 1024,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode == 0 {
		t.Fatalf("expected network-disabled execution to fail, got %#v", result)
	}
}

func writeFakeBubblewrap(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-bwrap.sh")
	content := `#!/bin/sh
workspace_src=""
tmp_src=""
chdir_path=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--bind)
		src="$2"
		dst="$3"
		if [ "$dst" = "/workspace" ]; then workspace_src="$src"; fi
		if [ "$dst" = "/tmp" ]; then tmp_src="$src"; fi
		shift 3
		;;
	--ro-bind)
		shift 3
		;;
	--setenv)
		export "$2=$3"
		shift 3
		;;
	--chdir)
		chdir_path="$2"
		shift 2
		;;
	--)
		shift
		if [ -n "$chdir_path" ]; then
			case "$chdir_path" in
			/workspace)
				cd "$workspace_src" || exit 91
				;;
			/workspace/*)
				rel="${chdir_path#/workspace/}"
				cd "$workspace_src/$rel" || exit 92
				;;
			/tmp)
				cd "$tmp_src" || exit 93
				;;
			esac
		fi
		exec "$@"
		;;
	*)
		shift
		;;
	esac
done
echo "missing -- terminator" >&2
exit 99
`
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake bubblewrap: %v", err)
	}
	return path
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `"'"'`) + "'"
}
