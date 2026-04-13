package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/app"
	"github.com/ffimnsr/koios/internal/cli"
)

var (
	version   = "dev"
	gitHash   = "unknown"
	buildTime = "unknown"
)

// init resolves version metadata at runtime when the binary was not built with
// ldflags (e.g. during `go run .`). Values injected by -ldflags take precedence.
func init() {
	if version == "dev" {
		if b, err := os.ReadFile("VERSION"); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				version = v
			}
		}
	}
	if gitHash == "unknown" {
		if out, err := exec.Command("git", "rev-parse", "--short", "HEAD").Output(); err == nil {
			if h := strings.TrimSpace(string(out)); h != "" {
				gitHash = h
			}
		}
	}
	if buildTime == "unknown" {
		buildTime = time.Now().UTC().Format(time.RFC3339)
	}
}

func main() {
	build := app.BuildInfo{
		Version:   version,
		GitHash:   gitHash,
		BuildTime: buildTime,
	}
	root := cli.NewRootCommand(build, app.RunDaemon)
	if err := root.Execute(); err != nil && err.Error() != "" {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
