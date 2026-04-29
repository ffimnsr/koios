package cli

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/user"
	"strings"

	"github.com/spf13/cobra"
)

var (
	cliCurrentUser = func() (*user.User, error) { return user.Current() }
	cliHostname    = os.Hostname
)

func defaultCLIPeerID() (string, error) {
	username := "user"
	if u, err := cliCurrentUser(); err == nil && u != nil {
		if trimmed := strings.TrimSpace(u.Username); trimmed != "" {
			username = trimmed
		} else if trimmed := strings.TrimSpace(u.Name); trimmed != "" {
			username = trimmed
		} else if trimmed := strings.TrimSpace(u.Uid); trimmed != "" {
			username = trimmed
		}
	}
	hostname, err := cliHostname()
	if err != nil {
		return "", fmt.Errorf("derive peer id: hostname: %w", err)
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "", errors.New("derive peer id: hostname is empty")
	}
	label := sanitizePeerComponent(username)
	fingerprint := sha256.Sum256([]byte(strings.ToLower(username) + "@" + strings.ToLower(hostname)))
	return fmt.Sprintf("%s-%x", label, fingerprint[:6]), nil
}

func sanitizePeerComponent(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	prevDash := false
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	result := strings.Trim(b.String(), "-")
	if result == "" {
		return "peer"
	}
	return result
}

func enableDerivedPeerDefault(cmd *cobra.Command) {
	existing := cmd.PersistentPreRunE
	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if err := maybeSetDerivedPeer(cmd); err != nil {
			return err
		}
		if existing != nil {
			return existing(cmd, args)
		}
		return nil
	}
}

func maybeSetDerivedPeer(cmd *cobra.Command) error {
	flag := cmd.Flags().Lookup("peer")
	if flag == nil {
		return nil
	}
	if cmd.Flags().Changed("peer") || strings.TrimSpace(flag.Value.String()) != "" {
		return nil
	}
	peer, err := defaultCLIPeerID()
	if err != nil {
		return err
	}
	return cmd.Flags().Set("peer", peer)
}
