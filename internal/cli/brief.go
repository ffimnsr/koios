package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newBriefCommand(ctx *commandContext) *cobra.Command {
	var peer string
	var kind string
	var timezone string
	var now int64
	var historyLimit int
	var eventLimit int
	var waitingLimit int
	var taskLimit int
	var projectLimit int
	var candidateLimit int
	var jsonOut bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "brief",
		Short: "Generate a daily brief or weekly review synthesis",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return fmt.Errorf("--peer is required")
			}
			state, err := ctx.state()
			if err != nil {
				return err
			}
			client := newGatewayClient(state, timeout)
			params := map[string]any{"kind": kind}
			if timezone != "" {
				params["timezone"] = timezone
			}
			if now > 0 {
				params["now"] = now
			}
			if historyLimit > 0 {
				params["history_limit"] = historyLimit
			}
			if eventLimit > 0 {
				params["event_limit"] = eventLimit
			}
			if waitingLimit > 0 {
				params["waiting_limit"] = waitingLimit
			}
			if taskLimit > 0 {
				params["task_limit"] = taskLimit
			}
			if projectLimit > 0 {
				params["project_limit"] = projectLimit
			}
			if candidateLimit > 0 {
				params["candidate_limit"] = candidateLimit
			}
			var result map[string]any
			if err := client.rpc(cmd.Context(), peer, "brief.generate", params, &result); err != nil {
				return err
			}
			if jsonOut {
				emit(cmd, true, result)
				return nil
			}
			text, _ := result["text"].(string)
			if strings.TrimSpace(text) == "" {
				emit(cmd, false, result)
				return nil
			}
			emit(cmd, false, text)
			return nil
		},
	}
	enableDerivedPeerDefault(cmd)
	cmd.Flags().StringVar(&peer, "peer", "", "peer ID")
	cmd.Flags().StringVar(&kind, "kind", "daily", "brief kind: daily or weekly")
	cmd.Flags().StringVar(&timezone, "timezone", "", "display timezone")
	cmd.Flags().Int64Var(&now, "now", 0, "override current time as Unix seconds")
	cmd.Flags().IntVar(&historyLimit, "history-limit", 0, "max recent session messages to inspect")
	cmd.Flags().IntVar(&eventLimit, "event-limit", 0, "max agenda events to include")
	cmd.Flags().IntVar(&waitingLimit, "waiting-limit", 0, "max stale waiting-ons to include")
	cmd.Flags().IntVar(&taskLimit, "task-limit", 0, "max due tasks to include")
	cmd.Flags().IntVar(&projectLimit, "project-limit", 0, "max active projects to include")
	cmd.Flags().IntVar(&candidateLimit, "candidate-limit", 0, "max pending memory candidates to include")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON output")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Second, "request timeout")
	return cmd
}
