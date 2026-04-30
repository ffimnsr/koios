package channels

import (
	"strconv"
	"time"
)

type TelegramPairingStore struct {
	store *BindingStore
}

type TelegramPendingPairing struct {
	Code        string    `json:"code"`
	UserID      int64     `json:"user_id"`
	ChatID      int64     `json:"chat_id"`
	Username    string    `json:"username,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
}

type TelegramApprovedPairing struct {
	UserID      int64     `json:"user_id"`
	ChatID      int64     `json:"chat_id"`
	Username    string    `json:"username,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	ApprovedAt  time.Time `json:"approved_at"`
	ApprovedBy  string    `json:"approved_by,omitempty"`
	Code        string    `json:"code,omitempty"`
}

func NewTelegramPairingStore(path string) *TelegramPairingStore {
	store := NewBindingStore(path)
	if store == nil {
		return nil
	}
	return &TelegramPairingStore{store: store}
}

func (s *TelegramPairingStore) Path() string {
	if s == nil || s.store == nil {
		return ""
	}
	return s.store.Path()
}

func (s *TelegramPairingStore) IsApproved(userID int64) (bool, error) {
	if s == nil || s.store == nil {
		return false, nil
	}
	return s.store.IsApproved("telegram", strconv.FormatInt(userID, 10))
}

func (s *TelegramPairingStore) EnsurePending(userID, chatID int64, username, displayName string, ttl time.Duration) (*TelegramPendingPairing, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	pending, err := s.store.EnsurePending(BindingRequest{
		Channel:        "telegram",
		SubjectID:      strconv.FormatInt(userID, 10),
		ConversationID: strconv.FormatInt(chatID, 10),
		Username:       username,
		DisplayName:    displayName,
		TTL:            ttl,
	})
	if err != nil {
		return nil, err
	}
	return telegramPendingFromBinding(pending), nil
}

func (s *TelegramPairingStore) ListPending() ([]TelegramPendingPairing, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	items, err := s.store.ListPending("telegram")
	if err != nil {
		return nil, err
	}
	out := make([]TelegramPendingPairing, 0, len(items))
	for _, item := range items {
		copy := item
		out = append(out, *telegramPendingFromBinding(&copy))
	}
	return out, nil
}

func (s *TelegramPairingStore) ListApproved() ([]TelegramApprovedPairing, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	items, err := s.store.ListApproved("telegram")
	if err != nil {
		return nil, err
	}
	out := make([]TelegramApprovedPairing, 0, len(items))
	for _, item := range items {
		copy := item
		out = append(out, *telegramApprovedFromBinding(&copy))
	}
	return out, nil
}

func (s *TelegramPairingStore) ApproveCode(code, approvedBy string) (*TelegramApprovedPairing, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	approved, err := s.store.ApproveCode(code, approvedBy)
	if err != nil {
		return nil, err
	}
	return telegramApprovedFromBinding(approved), nil
}

func (s *TelegramPairingStore) RejectCode(code string) (*TelegramPendingPairing, error) {
	if s == nil || s.store == nil {
		return nil, nil
	}
	pending, err := s.store.RejectCode(code)
	if err != nil {
		return nil, err
	}
	return telegramPendingFromBinding(pending), nil
}

func telegramPendingFromBinding(binding *PendingBinding) *TelegramPendingPairing {
	if binding == nil {
		return nil
	}
	return &TelegramPendingPairing{
		Code:        binding.Code,
		UserID:      parseBindingID(binding.SubjectID),
		ChatID:      parseBindingID(binding.ConversationID),
		Username:    binding.Username,
		DisplayName: binding.DisplayName,
		CreatedAt:   binding.CreatedAt,
		ExpiresAt:   binding.ExpiresAt,
	}
}

func telegramApprovedFromBinding(binding *ApprovedBinding) *TelegramApprovedPairing {
	if binding == nil {
		return nil
	}
	return &TelegramApprovedPairing{
		UserID:      parseBindingID(binding.SubjectID),
		ChatID:      parseBindingID(binding.ConversationID),
		Username:    binding.Username,
		DisplayName: binding.DisplayName,
		ApprovedAt:  binding.ApprovedAt,
		ApprovedBy:  binding.ApprovedBy,
		Code:        binding.Code,
	}
}

func parseBindingID(raw string) int64 {
	value, _ := strconv.ParseInt(raw, 10, 64)
	return value
}
