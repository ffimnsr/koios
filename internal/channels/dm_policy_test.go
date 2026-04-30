package channels

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeDirectMessagePolicy(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "default open", input: "", want: "open"},
		{name: "pairing", input: "pairing", want: "pairing"},
		{name: "closed", input: "closed", want: "closed"},
		{name: "invalid falls back", input: "weird", want: "open"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := NormalizeDirectMessagePolicy(tc.input); got != tc.want {
				t.Fatalf("NormalizeDirectMessagePolicy(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestAuthorizeDirectMessageAllowsAllowlistedSubject(t *testing.T) {
	decision, err := AuthorizeDirectMessage(DirectMessageAuthRequest{
		Channel:   "telegram",
		Policy:    "closed",
		SubjectID: "7",
		Allowed:   true,
	})
	if err != nil {
		t.Fatalf("AuthorizeDirectMessage: %v", err)
	}
	if !decision.Allowed {
		t.Fatal("expected allowlisted subject to bypass dm policy")
	}
	if decision.ReplyText != "" {
		t.Fatalf("unexpected reply text: %q", decision.ReplyText)
	}
}

func TestAuthorizeDirectMessageClosedPolicy(t *testing.T) {
	decision, err := AuthorizeDirectMessage(DirectMessageAuthRequest{
		Channel:   "telegram",
		Policy:    "closed",
		SubjectID: "7",
	})
	if err != nil {
		t.Fatalf("AuthorizeDirectMessage: %v", err)
	}
	if decision.Allowed {
		t.Fatal("expected closed policy to block direct messages")
	}
	if decision.ReplyText != "Direct messages are closed for this bot." {
		t.Fatalf("unexpected closed-policy reply: %q", decision.ReplyText)
	}
}

func TestAuthorizeDirectMessagePairingPolicy(t *testing.T) {
	store := NewBindingStore(t.TempDir() + "/bindings.json")
	decision, err := AuthorizeDirectMessage(DirectMessageAuthRequest{
		Channel:        "telegram",
		Policy:         "pairing",
		SubjectID:      "7",
		ConversationID: "123",
		Username:       "pat",
		DisplayName:    "Pat Example",
		Metadata:       map[string]string{"chat_id": "123"},
		PairingCodeTTL: time.Hour,
		Store:          store,
	})
	if err != nil {
		t.Fatalf("AuthorizeDirectMessage: %v", err)
	}
	if decision.Allowed {
		t.Fatal("expected pairing policy to block until approved")
	}
	if decision.Pending == nil {
		t.Fatal("expected pending binding to be created")
	}
	if !strings.Contains(decision.ReplyText, decision.Pending.Code) {
		t.Fatalf("expected reply to include pairing code, got %q", decision.ReplyText)
	}
	if !strings.Contains(decision.ReplyText, "koios pairing approve telegram "+decision.Pending.Code) {
		t.Fatalf("expected reply to include pairing command, got %q", decision.ReplyText)
	}

	pending, err := store.ListPending("telegram")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	if pending[0].SubjectID != "7" {
		t.Fatalf("pending subject id = %q, want 7", pending[0].SubjectID)
	}
	if _, err := store.ApproveCode(decision.Pending.Code, "tester"); err != nil {
		t.Fatalf("ApproveCode: %v", err)
	}

	approved, err := AuthorizeDirectMessage(DirectMessageAuthRequest{
		Channel:   "telegram",
		Policy:    "pairing",
		SubjectID: "7",
		Store:     store,
	})
	if err != nil {
		t.Fatalf("AuthorizeDirectMessage approved: %v", err)
	}
	if !approved.Allowed {
		t.Fatal("expected approved subject to be allowed")
	}
}

func TestAuthorizeDirectMessagePairingRequiresStore(t *testing.T) {
	_, err := AuthorizeDirectMessage(DirectMessageAuthRequest{
		Channel:   "telegram",
		Policy:    "pairing",
		SubjectID: "7",
	})
	if err == nil || !strings.Contains(err.Error(), "binding store") {
		t.Fatalf("expected missing binding store error, got %v", err)
	}
}
