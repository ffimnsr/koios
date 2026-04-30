package channels

import (
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type BindingStore struct {
	path string
	mu   sync.Mutex
}

type BindingRequest struct {
	Channel        string
	SubjectID      string
	ConversationID string
	Username       string
	DisplayName    string
	Metadata       map[string]string
	TTL            time.Duration
}

type PendingBinding struct {
	Code           string            `json:"code"`
	Channel        string            `json:"channel"`
	SubjectID      string            `json:"subject_id"`
	ConversationID string            `json:"conversation_id,omitempty"`
	Username       string            `json:"username,omitempty"`
	DisplayName    string            `json:"display_name,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	ExpiresAt      time.Time         `json:"expires_at"`
}

type ApprovedBinding struct {
	Channel        string            `json:"channel"`
	SubjectID      string            `json:"subject_id"`
	ConversationID string            `json:"conversation_id,omitempty"`
	Username       string            `json:"username,omitempty"`
	DisplayName    string            `json:"display_name,omitempty"`
	PeerID         string            `json:"peer_id,omitempty"`
	SessionKey     string            `json:"session_key,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	ApprovedAt     time.Time         `json:"approved_at"`
	ApprovedBy     string            `json:"approved_by,omitempty"`
	Code           string            `json:"code,omitempty"`
}

type BindingRoute struct {
	PeerID     string
	SessionKey string
}

type bindingFile struct {
	Pending  map[string]PendingBinding  `json:"pending"`
	Approved map[string]ApprovedBinding `json:"approved"`
}

func NewBindingStore(path string) *BindingStore {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return &BindingStore{path: path}
}

func (s *BindingStore) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *BindingStore) IsApproved(channel, subjectID string) (bool, error) {
	if s == nil {
		return false, nil
	}
	key, err := bindingKey(channel, subjectID)
	if err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return false, err
	}
	_, ok := state.Approved[key]
	return ok, nil
}

func (s *BindingStore) EnsurePending(req BindingRequest) (*PendingBinding, error) {
	if s == nil {
		return nil, nil
	}
	key, err := bindingKey(req.Channel, req.SubjectID)
	if err != nil {
		return nil, err
	}
	channel := normalizeBindingChannel(req.Channel)
	if req.TTL <= 0 {
		req.TTL = 24 * time.Hour
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	if approved, ok := state.Approved[key]; ok {
		pending := PendingBinding{
			Code:           approved.Code,
			Channel:        approved.Channel,
			SubjectID:      approved.SubjectID,
			ConversationID: approved.ConversationID,
			Username:       approved.Username,
			DisplayName:    approved.DisplayName,
			Metadata:       copyBindingMetadata(approved.Metadata),
		}
		return &pending, nil
	}
	now := time.Now().UTC()
	if pending, ok := state.Pending[key]; ok && pending.ExpiresAt.After(now) {
		pending.ConversationID = strings.TrimSpace(req.ConversationID)
		if trimmed := strings.TrimSpace(req.Username); trimmed != "" {
			pending.Username = trimmed
		}
		if trimmed := strings.TrimSpace(req.DisplayName); trimmed != "" {
			pending.DisplayName = trimmed
		}
		if len(req.Metadata) > 0 {
			pending.Metadata = copyBindingMetadata(req.Metadata)
		}
		state.Pending[key] = pending
		if err := s.saveLocked(state); err != nil {
			return nil, err
		}
		copy := pending
		return &copy, nil
	}
	code, err := bindingCode()
	if err != nil {
		return nil, err
	}
	pending := PendingBinding{
		Code:           code,
		Channel:        channel,
		SubjectID:      strings.TrimSpace(req.SubjectID),
		ConversationID: strings.TrimSpace(req.ConversationID),
		Username:       strings.TrimSpace(req.Username),
		DisplayName:    strings.TrimSpace(req.DisplayName),
		Metadata:       copyBindingMetadata(req.Metadata),
		CreatedAt:      now,
		ExpiresAt:      now.Add(req.TTL),
	}
	state.Pending[key] = pending
	if err := s.saveLocked(state); err != nil {
		return nil, err
	}
	return &pending, nil
}

func (s *BindingStore) ListPending(channel string) ([]PendingBinding, error) {
	if s == nil {
		return nil, nil
	}
	channel = normalizeBindingChannel(channel)
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	items := make([]PendingBinding, 0, len(state.Pending))
	for _, pending := range state.Pending {
		if channel != "" && pending.Channel != channel {
			continue
		}
		items = append(items, pending)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Channel == items[j].Channel {
			if items[i].CreatedAt.Equal(items[j].CreatedAt) {
				return items[i].SubjectID < items[j].SubjectID
			}
			return items[i].CreatedAt.Before(items[j].CreatedAt)
		}
		return items[i].Channel < items[j].Channel
	})
	return items, nil
}

func (s *BindingStore) ListApproved(channel string) ([]ApprovedBinding, error) {
	if s == nil {
		return nil, nil
	}
	channel = normalizeBindingChannel(channel)
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	items := make([]ApprovedBinding, 0, len(state.Approved))
	for _, approved := range state.Approved {
		if channel != "" && approved.Channel != channel {
			continue
		}
		items = append(items, approved)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Channel == items[j].Channel {
			if items[i].ApprovedAt.Equal(items[j].ApprovedAt) {
				return items[i].SubjectID < items[j].SubjectID
			}
			return items[i].ApprovedAt.Before(items[j].ApprovedAt)
		}
		return items[i].Channel < items[j].Channel
	})
	return items, nil
}

func (s *BindingStore) ApproveCode(code, approvedBy string) (*ApprovedBinding, error) {
	return s.ApproveCodeWithRoute(code, approvedBy, BindingRoute{})
}

func (s *BindingStore) ApproveCodeForChannel(channel, code, approvedBy string) (*ApprovedBinding, error) {
	return s.ApproveCodeForChannelWithRoute(channel, code, approvedBy, BindingRoute{})
}

func (s *BindingStore) ApproveCodeForChannelWithRoute(channel, code, approvedBy string, route BindingRoute) (*ApprovedBinding, error) {
	if s == nil {
		return nil, errors.New("channel binding store is not configured")
	}
	channel = normalizeBindingChannel(channel)
	if channel == "" {
		return nil, errors.New("binding channel is required")
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil, errors.New("binding code is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	for key, pending := range state.Pending {
		if pending.Code != code {
			continue
		}
		if pending.Channel != channel {
			return nil, fmt.Errorf("binding code %q belongs to channel %q, not %q", code, pending.Channel, channel)
		}
		peerID := strings.TrimSpace(route.PeerID)
		sessionKey := strings.TrimSpace(route.SessionKey)
		approved := ApprovedBinding{
			Channel:        pending.Channel,
			SubjectID:      pending.SubjectID,
			ConversationID: pending.ConversationID,
			Username:       pending.Username,
			DisplayName:    pending.DisplayName,
			PeerID:         peerID,
			SessionKey:     sessionKey,
			Metadata:       copyBindingMetadata(pending.Metadata),
			ApprovedAt:     time.Now().UTC(),
			ApprovedBy:     strings.TrimSpace(approvedBy),
			Code:           pending.Code,
		}
		delete(state.Pending, key)
		state.Approved[key] = approved
		if err := s.saveLocked(state); err != nil {
			return nil, err
		}
		return &approved, nil
	}
	return nil, fmt.Errorf("binding code %q was not found", code)
}

func (s *BindingStore) ApproveCodeWithRoute(code, approvedBy string, route BindingRoute) (*ApprovedBinding, error) {
	if s == nil {
		return nil, errors.New("channel binding store is not configured")
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil, errors.New("binding code is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	for key, pending := range state.Pending {
		if pending.Code != code {
			continue
		}
		peerID := strings.TrimSpace(route.PeerID)
		sessionKey := strings.TrimSpace(route.SessionKey)
		approved := ApprovedBinding{
			Channel:        pending.Channel,
			SubjectID:      pending.SubjectID,
			ConversationID: pending.ConversationID,
			Username:       pending.Username,
			DisplayName:    pending.DisplayName,
			PeerID:         peerID,
			SessionKey:     sessionKey,
			Metadata:       copyBindingMetadata(pending.Metadata),
			ApprovedAt:     time.Now().UTC(),
			ApprovedBy:     strings.TrimSpace(approvedBy),
			Code:           pending.Code,
		}
		delete(state.Pending, key)
		state.Approved[key] = approved
		if err := s.saveLocked(state); err != nil {
			return nil, err
		}
		return &approved, nil
	}
	return nil, fmt.Errorf("binding code %q was not found", code)
}

func (s *BindingStore) ApprovedBinding(channel, subjectID string) (*ApprovedBinding, error) {
	if s == nil {
		return nil, nil
	}
	key, err := bindingKey(channel, subjectID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	approved, ok := state.Approved[key]
	if !ok {
		return nil, nil
	}
	copy := approved
	return &copy, nil
}

func (s *BindingStore) UpdateApprovedRoute(channel, subjectID string, route BindingRoute) (*ApprovedBinding, error) {
	if s == nil {
		return nil, errors.New("channel binding store is not configured")
	}
	key, err := bindingKey(channel, subjectID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	approved, ok := state.Approved[key]
	if !ok {
		return nil, fmt.Errorf("approved binding for %s/%s was not found", normalizeBindingChannel(channel), strings.TrimSpace(subjectID))
	}
	approved.PeerID = strings.TrimSpace(route.PeerID)
	approved.SessionKey = strings.TrimSpace(route.SessionKey)
	state.Approved[key] = approved
	if err := s.saveLocked(state); err != nil {
		return nil, err
	}
	copy := approved
	return &copy, nil
}

func (s *BindingStore) UpdateApprovedMetadata(channel, subjectID string, metadata map[string]string) (*ApprovedBinding, error) {
	if s == nil {
		return nil, errors.New("channel binding store is not configured")
	}
	key, err := bindingKey(channel, subjectID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	approved, ok := state.Approved[key]
	if !ok {
		return nil, fmt.Errorf("approved binding for %s/%s was not found", normalizeBindingChannel(channel), strings.TrimSpace(subjectID))
	}
	approved.Metadata = copyBindingMetadata(metadata)
	state.Approved[key] = approved
	if err := s.saveLocked(state); err != nil {
		return nil, err
	}
	copy := approved
	return &copy, nil
}

func (s *BindingStore) RejectCode(code string) (*PendingBinding, error) {
	if s == nil {
		return nil, errors.New("channel binding store is not configured")
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil, errors.New("binding code is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	for key, pending := range state.Pending {
		if pending.Code != code {
			continue
		}
		delete(state.Pending, key)
		if err := s.saveLocked(state); err != nil {
			return nil, err
		}
		return &pending, nil
	}
	return nil, fmt.Errorf("binding code %q was not found", code)
}

func (s *BindingStore) RejectCodeForChannel(channel, code string) (*PendingBinding, error) {
	if s == nil {
		return nil, errors.New("channel binding store is not configured")
	}
	channel = normalizeBindingChannel(channel)
	if channel == "" {
		return nil, errors.New("binding channel is required")
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return nil, errors.New("binding code is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	for key, pending := range state.Pending {
		if pending.Code != code {
			continue
		}
		if pending.Channel != channel {
			return nil, fmt.Errorf("binding code %q belongs to channel %q, not %q", code, pending.Channel, channel)
		}
		delete(state.Pending, key)
		if err := s.saveLocked(state); err != nil {
			return nil, err
		}
		return &pending, nil
	}
	return nil, fmt.Errorf("binding code %q was not found", code)
}

func (s *BindingStore) loadLocked() (*bindingFile, error) {
	state := &bindingFile{
		Pending:  map[string]PendingBinding{},
		Approved: map[string]ApprovedBinding{},
	}
	if s == nil || strings.TrimSpace(s.path) == "" {
		return state, nil
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return nil, fmt.Errorf("read channel bindings: %w", err)
	}
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("decode channel bindings: %w", err)
	}
	now := time.Now().UTC()
	for key, pending := range state.Pending {
		if !pending.ExpiresAt.IsZero() && !pending.ExpiresAt.After(now) {
			delete(state.Pending, key)
		}
	}
	if state.Pending == nil {
		state.Pending = map[string]PendingBinding{}
	}
	if state.Approved == nil {
		state.Approved = map[string]ApprovedBinding{}
	}
	return state, nil
}

func (s *BindingStore) saveLocked(state *bindingFile) error {
	if s == nil || strings.TrimSpace(s.path) == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("create channel binding dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode channel bindings: %w", err)
	}
	if err := os.WriteFile(s.path, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write channel bindings: %w", err)
	}
	return nil
}

func bindingKey(channel, subjectID string) (string, error) {
	channel = normalizeBindingChannel(channel)
	subjectID = strings.TrimSpace(subjectID)
	if channel == "" {
		return "", errors.New("binding channel is required")
	}
	if subjectID == "" {
		return "", errors.New("binding subject id is required")
	}
	return channel + ":" + subjectID, nil
}

func normalizeBindingChannel(channel string) string {
	return strings.ToLower(strings.TrimSpace(channel))
}

func copyBindingMetadata(metadata map[string]string) map[string]string {
	if len(metadata) == 0 {
		return nil
	}
	clone := make(map[string]string, len(metadata))
	for key, value := range metadata {
		trimmedKey := strings.TrimSpace(key)
		if trimmedKey == "" {
			continue
		}
		clone[trimmedKey] = value
	}
	if len(clone) == 0 {
		return nil
	}
	return clone
}

func bindingCode() (string, error) {
	buf := make([]byte, 5)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate binding code: %w", err)
	}
	code := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(buf)
	return strings.ToUpper(strings.TrimSpace(code)), nil
}
