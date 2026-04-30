package channels

import "testing"

func TestBindingStoreUpdateApprovedMetadata(t *testing.T) {
	store := NewBindingStore(t.TempDir() + "/bindings.json")
	pending, err := store.EnsurePending(BindingRequest{Channel: "telegram", SubjectID: "7", ConversationID: "123", DisplayName: "Alice Sender"})
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	if _, err := store.ApproveCodeWithRoute(pending.Code, "operator", BindingRoute{PeerID: "alice"}); err != nil {
		t.Fatalf("ApproveCodeWithRoute: %v", err)
	}
	updated, err := store.UpdateApprovedMetadata("telegram", "7", map[string]string{"contact_id": "contact-1", "contact_name": "Alice Example"})
	if err != nil {
		t.Fatalf("UpdateApprovedMetadata: %v", err)
	}
	if updated.Metadata["contact_id"] != "contact-1" {
		t.Fatalf("unexpected updated metadata: %#v", updated.Metadata)
	}
	approved, err := store.ApprovedBinding("telegram", "7")
	if err != nil {
		t.Fatalf("ApprovedBinding: %v", err)
	}
	if approved == nil || approved.Metadata["contact_name"] != "Alice Example" {
		t.Fatalf("unexpected stored metadata: %#v", approved)
	}
}
