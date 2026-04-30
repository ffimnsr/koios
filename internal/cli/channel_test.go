package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	channelstore "github.com/ffimnsr/koios/internal/channels"
)

func TestPairingApproveCommand(t *testing.T) {
	dir := t.TempDir()
	store := channelstore.NewBindingStore(filepath.Join(dir, "workspace", "db", "channel_bindings.json"))
	pending, err := store.EnsurePending(channelstore.BindingRequest{
		Channel:        "telegram",
		SubjectID:      "7",
		ConversationID: "123",
		Username:       "pat",
		DisplayName:    "Pat",
		TTL:            time.Hour,
	})
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	cmd := newPairingCommand(&commandContext{cwd: dir})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"approve", "telegram", pending.Code, "--json", "--peer", "default", "--session-key", "default::channel::telegram:123"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v\n%s", err, out.String())
	}
	text := out.String()
	if !strings.Contains(text, `"channel": "telegram"`) || !strings.Contains(text, `"subject_id": "7"`) || !strings.Contains(text, `"approved_by":`) || !strings.Contains(text, `"peer_id": "default"`) || !strings.Contains(text, `"session_key": "default::channel::telegram:123"`) {
		t.Fatalf("unexpected command output:\n%s", text)
	}
}

func TestPairingApproveCommandRejectsWrongChannel(t *testing.T) {
	dir := t.TempDir()
	store := channelstore.NewBindingStore(filepath.Join(dir, "workspace", "db", "channel_bindings.json"))
	pending, err := store.EnsurePending(channelstore.BindingRequest{
		Channel:        "telegram",
		SubjectID:      "7",
		ConversationID: "123",
		TTL:            time.Hour,
	})
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	cmd := newPairingCommand(&commandContext{cwd: dir})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"approve", "slack", pending.Code})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), `belongs to channel "telegram", not "slack"`) {
		t.Fatalf("expected wrong-channel error, got %v\n%s", err, out.String())
	}
}
