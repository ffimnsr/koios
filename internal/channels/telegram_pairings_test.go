package channels

import (
	"strings"
	"testing"
	"time"
)

func TestBindingStoreApproveAndReject(t *testing.T) {
	store := NewBindingStore(t.TempDir() + "/channel_bindings.json")
	pending, err := store.EnsurePending(BindingRequest{
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
	if pending == nil || pending.Code == "" {
		t.Fatalf("expected pending binding code, got %#v", pending)
	}
	approved, err := store.ApproveCode(pending.Code, "tester")
	if err != nil {
		t.Fatalf("ApproveCode: %v", err)
	}
	if approved.Channel != "telegram" || approved.SubjectID != "7" || approved.ApprovedBy != "tester" {
		t.Fatalf("unexpected approved binding: %#v", approved)
	}
	ok, err := store.IsApproved("telegram", "7")
	if err != nil {
		t.Fatalf("IsApproved: %v", err)
	}
	if !ok {
		t.Fatal("expected user to be approved")
	}
	pending, err = store.EnsurePending(BindingRequest{
		Channel:        "telegram",
		SubjectID:      "8",
		ConversationID: "456",
		Username:       "sam",
		DisplayName:    "Sam",
		TTL:            time.Hour,
	})
	if err != nil {
		t.Fatalf("EnsurePending second: %v", err)
	}
	rejected, err := store.RejectCode(pending.Code)
	if err != nil {
		t.Fatalf("RejectCode: %v", err)
	}
	if rejected.Channel != "telegram" || rejected.SubjectID != "8" {
		t.Fatalf("unexpected rejected binding: %#v", rejected)
	}
	items, err := store.ListPending("telegram")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no pending bindings, got %#v", items)
	}
}

func TestBindingStoreExpiresPendingCodes(t *testing.T) {
	store := NewBindingStore(t.TempDir() + "/channel_bindings.json")
	first, err := store.EnsurePending(BindingRequest{
		Channel:        "telegram",
		SubjectID:      "7",
		ConversationID: "123",
		Username:       "pat",
		DisplayName:    "Pat",
		TTL:            time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	time.Sleep(time.Millisecond)
	second, err := store.EnsurePending(BindingRequest{
		Channel:        "telegram",
		SubjectID:      "7",
		ConversationID: "123",
		Username:       "pat",
		DisplayName:    "Pat",
		TTL:            time.Hour,
	})
	if err != nil {
		t.Fatalf("EnsurePending second: %v", err)
	}
	if first.Code == second.Code {
		t.Fatalf("expected expired binding code to be replaced, got %q", second.Code)
	}
}

func TestTelegramPairingStoreWrapsGenericBindings(t *testing.T) {
	store := NewTelegramPairingStore(t.TempDir() + "/channel_bindings.json")
	pending, err := store.EnsurePending(7, 123, "pat", "Pat", time.Hour)
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	if pending.UserID != 7 || pending.ChatID != 123 {
		t.Fatalf("unexpected telegram pending binding: %#v", pending)
	}
}

func TestBindingStoreApproveCodeForChannel(t *testing.T) {
	store := NewBindingStore(t.TempDir() + "/channel_bindings.json")
	pending, err := store.EnsurePending(BindingRequest{
		Channel:   "telegram",
		SubjectID: "7",
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	approved, err := store.ApproveCodeForChannel("telegram", pending.Code, "tester")
	if err != nil {
		t.Fatalf("ApproveCodeForChannel: %v", err)
	}
	if approved == nil || approved.Channel != "telegram" || approved.SubjectID != "7" {
		t.Fatalf("unexpected approved binding: %#v", approved)
	}

	pending, err = store.EnsurePending(BindingRequest{
		Channel:   "telegram",
		SubjectID: "8",
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("EnsurePending second: %v", err)
	}
	if _, err := store.ApproveCodeForChannel("slack", pending.Code, "tester"); err == nil || !strings.Contains(err.Error(), `belongs to channel "telegram", not "slack"`) {
		t.Fatalf("expected wrong-channel error, got %v", err)
	}

	pending, err = store.EnsurePending(BindingRequest{
		Channel:   "telegram",
		SubjectID: "9",
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("EnsurePending third: %v", err)
	}
	rejected, err := store.RejectCodeForChannel("telegram", pending.Code)
	if err != nil {
		t.Fatalf("RejectCodeForChannel: %v", err)
	}
	if rejected == nil || rejected.Channel != "telegram" || rejected.SubjectID != "9" {
		t.Fatalf("unexpected rejected binding: %#v", rejected)
	}

	pending, err = store.EnsurePending(BindingRequest{
		Channel:   "telegram",
		SubjectID: "10",
		TTL:       time.Hour,
	})
	if err != nil {
		t.Fatalf("EnsurePending fourth: %v", err)
	}
	if _, err := store.RejectCodeForChannel("slack", pending.Code); err == nil || !strings.Contains(err.Error(), `belongs to channel "telegram", not "slack"`) {
		t.Fatalf("expected wrong-channel reject error, got %v", err)
	}
}
