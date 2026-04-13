package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

func newAgentCommand(ctx *commandContext) *cobra.Command {
	var (
		peer       string
		message    string
		scope      string
		senderID   string
		sessionKey string
		timeout    time.Duration
		jsonOut    bool
		tui        bool
	)
	cmd := &cobra.Command{
		Use:   "agent",
		Short: "Run an agent turn or launch an interactive agent TUI",
		RunE: func(cmd *cobra.Command, args []string) error {
			if peer == "" {
				return errors.New("--peer is required")
			}
			if message == "" {
				if !tui && !term.IsTerminal(int(os.Stdin.Fd())) {
					return errors.New("--message is required when stdin is not a terminal")
				}
				return runAgentTUI(cmd.Context(), ctx, agentOptions{
					Peer:       peer,
					Scope:      scope,
					SenderID:   senderID,
					SessionKey: sessionKey,
					Timeout:    timeout,
				})
			}
			result, err := runAgentOneShot(cmd.Context(), ctx, agentOptions{
				Peer:       peer,
				Message:    message,
				Scope:      scope,
				SenderID:   senderID,
				SessionKey: sessionKey,
				Timeout:    timeout,
			})
			if err != nil {
				return err
			}
			if jsonOut {
				emit(cmd, true, result)
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), strings.TrimSpace(result.AssistantText))
			return nil
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "", "message body")
	cmd.Flags().StringVar(&peer, "peer", "", "peer id")
	cmd.Flags().StringVar(&scope, "scope", "main", "session scope: main, direct, isolated, global")
	cmd.Flags().StringVar(&senderID, "sender-id", "", "sender id for direct scope")
	cmd.Flags().StringVar(&sessionKey, "session-key", "", "explicit session key override")
	cmd.Flags().DurationVar(&timeout, "timeout", 2*time.Minute, "agent timeout")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON for one-shot runs")
	cmd.Flags().BoolVar(&tui, "tui", false, "force TUI mode")
	return cmd
}

type agentOptions struct {
	Peer       string
	Message    string
	Scope      string
	SenderID   string
	SessionKey string
	Timeout    time.Duration
}

func runAgentOneShot(ctx context.Context, cmdCtx *commandContext, opts agentOptions) (*agentRunResult, error) {
	state, err := cmdCtx.state()
	if err != nil {
		return nil, err
	}
	client := newDaemonClient(state, opts.Timeout)
	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	params := map[string]any{
		"messages": []map[string]any{{"role": "user", "content": opts.Message}},
		"scope":    opts.Scope,
		"stream":   true,
		"timeout":  opts.Timeout.String(),
	}
	if opts.SenderID != "" {
		params["sender_id"] = opts.SenderID
	}
	if opts.SessionKey != "" {
		params["session_key"] = opts.SessionKey
	}
	return client.agentRunStream(reqCtx, opts.Peer, params, nil, nil, nil)
}

func agentRunParams(opts agentOptions, message string) map[string]any {
	params := map[string]any{
		"messages": []map[string]any{{"role": "user", "content": message}},
		"scope":    opts.Scope,
		"stream":   true,
		"timeout":  opts.Timeout.String(),
	}
	if opts.SenderID != "" {
		params["sender_id"] = opts.SenderID
	}
	if opts.SessionKey != "" {
		params["session_key"] = opts.SessionKey
	}
	return params
}

func runAgentTUI(ctx context.Context, cmdCtx *commandContext, opts agentOptions) error {
	state, err := cmdCtx.state()
	if err != nil {
		return err
	}
	client := newDaemonClient(state, opts.Timeout)
	model := newAgentTUIModel(client, opts)
	prog := tea.NewProgram(model, tea.WithAltScreen())
	model.program = prog
	model.submit = func(msg string) {
		go func() {
			runCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
			defer cancel()
			params := agentRunParams(opts, msg)
			res, err := client.agentRunStream(
				runCtx,
				opts.Peer,
				params,
				func(delta string) { prog.Send(agentDeltaMsg(delta)) },
				func(event map[string]any) { prog.Send(agentEventMsg(event)) },
				func(session sessionMessageEnvelope) { prog.Send(agentSessionPushMsg(session)) },
			)
			if err != nil {
				prog.Send(agentErrorMsg(err))
				return
			}
			prog.Send(agentDoneMsg(*res))
		}()
	}
	_, err = prog.Run()
	return err
}
