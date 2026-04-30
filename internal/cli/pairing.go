package cli

import (
	"strings"

	"github.com/spf13/cobra"

	channelstore "github.com/ffimnsr/koios/internal/channels"
)

func newPairingCommand(ctx *commandContext) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pairing",
		Short: "List and approve pending DM pairing codes",
	}
	cmd.AddCommand(newPairingListCommand(ctx))
	cmd.AddCommand(newPairingApproveCommand(ctx))
	cmd.AddCommand(newPairingRejectCommand(ctx))
	return cmd
}

func newPairingListCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list [channel]",
		Short: "List pending and approved pairing codes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadChannelBindingStore(ctx)
			if err != nil {
				return err
			}
			channel := ""
			if len(args) == 1 {
				channel = strings.TrimSpace(args[0])
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
				"channel":        channel,
				"pending_count":  len(pending),
				"pending":        pending,
				"approved_count": len(approved),
				"approved":       approved,
			})
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newPairingApproveCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	var approvedBy string
	var peerID string
	var sessionKey string
	cmd := &cobra.Command{
		Use:   "approve <channel> <code>",
		Short: "Approve one pending pairing code for a channel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadChannelBindingStore(ctx)
			if err != nil {
				return err
			}
			entry, err := store.ApproveCodeForChannelWithRoute(args[0], args[1], pairingApprovedBy(approvedBy), channelstore.BindingRoute{
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
	cmd.Flags().StringVar(&approvedBy, "approved-by", "", "approval actor recorded with the pairing")
	cmd.Flags().StringVar(&peerID, "peer", "", "peer that should own the routed inbox session")
	cmd.Flags().StringVar(&sessionKey, "session-key", "", "explicit routed session key override")
	return cmd
}

func newPairingRejectCommand(ctx *commandContext) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "reject <channel> <code>",
		Short: "Reject one pending pairing code for a channel",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := loadChannelBindingStore(ctx)
			if err != nil {
				return err
			}
			entry, err := store.RejectCodeForChannel(args[0], args[1])
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
