package cli

import (
	"fmt"
	"os/user"
	"strings"

	"github.com/spf13/cobra"

	channelstore "github.com/ffimnsr/koios/internal/channels"
)

func newChannelCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "channel",
		Aliases: []string{"channels"},
		Short:   "Inspect and manage built-in channel state",
	}
	cmd.AddCommand(newChannelBindingCommand(ctx))
	return cmd
}

func newChannelBindingCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "binding",
		Aliases: []string{"bindings"},
		Short:   "List and approve channel conversation bindings",
	}
	cmd.AddCommand(newChannelBindingListCommand(ctx))
	cmd.AddCommand(newChannelBindingApproveCommand(ctx))
	cmd.AddCommand(newChannelBindingRejectCommand(ctx))
	return cmd
}

func newChannelBindingListCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	var channel string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List pending and approved channel bindings",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadChannelBindingStore(ctx)
			if err != nil {
				return err
			}
			pending, err := store.ListPending(channel)
			if err != nil {
				return err
			}
			approved, err := store.ListApproved(channel)
			if err != nil {
				return err
			}
			emit(cmd, jsonOut, map[string]any{
				"path":           store.Path(),
				"channel":        strings.TrimSpace(channel),
				"pending_count":  len(pending),
				"pending":        pending,
				"approved_count": len(approved),
				"approved":       approved,
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().StringVar(&channel, "channel", "", "filter bindings by channel id")
	return cmd
}

func newChannelBindingApproveCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	var approvedBy string
	var peerID string
	var sessionKey string
	cmd := &cobra.Command{
		Use:   "approve <code>",
		Short: "Approve one pending channel binding code",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadChannelBindingStore(ctx)
			if err != nil {
				return err
			}
			entry, err := store.ApproveCodeWithRoute(args[0], pairingApprovedBy(approvedBy), channelstore.BindingRoute{
				PeerID:     strings.TrimSpace(peerID),
				SessionKey: strings.TrimSpace(sessionKey),
			})
			if err != nil {
				return err
			}
			emit(cmd, jsonOut, map[string]any{"approved": entry, "path": store.Path()})
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	cmd.Flags().StringVar(&approvedBy, "approved-by", "", "approval actor recorded with the binding")
	cmd.Flags().StringVar(&peerID, "peer", "", "peer that should own the routed inbox session")
	cmd.Flags().StringVar(&sessionKey, "session-key", "", "explicit routed session key override")
	return cmd
}

func newChannelBindingRejectCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "reject <code>",
		Short: "Reject one pending channel binding code",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadChannelBindingStore(ctx)
			if err != nil {
				return err
			}
			entry, err := store.RejectCode(args[0])
			if err != nil {
				return err
			}
			emit(cmd, jsonOut, map[string]any{"rejected": entry, "path": store.Path()})
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func loadChannelBindingStore(ctx *commandContext) (*channelstore.BindingStore, error) {
	state, err := ctx.state()
	if err != nil {
		return nil, err
	}
	if state.Config == nil || state.WorkspaceRoot == "" {
		return nil, fmt.Errorf("workspace.root is not configured")
	}
	return channelstore.NewBindingStore(state.Config.ChannelBindingsPath()), nil
}

func pairingApprovedBy(raw string) string {
	if trimmed := strings.TrimSpace(raw); trimmed != "" {
		return trimmed
	}
	current, err := user.Current()
	if err == nil && current != nil {
		if current.Username != "" {
			return current.Username
		}
		if current.Name != "" {
			return current.Name
		}
	}
	return "operator"
}
