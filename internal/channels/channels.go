package channels

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

type Route struct {
	Pattern string
	Handler http.Handler
}

type Channel interface {
	ID() string
	Routes() []Route
	Start(context.Context) error
	Shutdown(context.Context) error
}

type Manager struct {
	mu       sync.RWMutex
	channels []Channel
}

func NewManager(channels ...Channel) *Manager {
	mgr := &Manager{}
	for _, ch := range channels {
		mgr.Register(ch)
	}
	return mgr
}

func (m *Manager) Register(ch Channel) {
	if m == nil || ch == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.channels = append(m.channels, ch)
	sort.SliceStable(m.channels, func(i, j int) bool {
		return m.channels[i].ID() < m.channels[j].ID()
	})
}

func (m *Manager) HasChannels() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.channels) > 0
}

func (m *Manager) HasChannel(channelID string) bool {
	if m == nil {
		return false
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ch := range m.channels {
		if ch != nil && strings.EqualFold(strings.TrimSpace(ch.ID()), channelID) {
			return true
		}
	}
	return false
}

func (m *Manager) HasOutboundSenders() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ch := range m.channels {
		if _, ok := ch.(OutboundSender); ok {
			return true
		}
	}
	return false
}

func (m *Manager) SendMessage(ctx context.Context, channelID string, target OutboundTarget, msg OutboundMessage) (*OutboundReceipt, error) {
	if m == nil {
		return nil, fmt.Errorf("channel manager is not configured")
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, fmt.Errorf("channel is required")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, ch := range m.channels {
		if ch == nil || !strings.EqualFold(strings.TrimSpace(ch.ID()), channelID) {
			continue
		}
		sender, ok := ch.(OutboundSender)
		if !ok {
			return nil, fmt.Errorf("channel %q does not support outbound messaging", channelID)
		}
		receipt, err := sender.SendMessage(ctx, target, msg)
		if err != nil {
			return nil, err
		}
		return normalizeOutboundReceipt(channelID, target, receipt), nil
	}
	return nil, fmt.Errorf("channel %q is not configured", channelID)
}

func (m *Manager) RegisterRoutes(mux *http.ServeMux) error {
	if m == nil || mux == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := make(map[string]string)
	for _, ch := range m.channels {
		for _, route := range ch.Routes() {
			pattern := strings.TrimSpace(route.Pattern)
			if pattern == "" {
				return fmt.Errorf("channel %q declared an empty route pattern", ch.ID())
			}
			if route.Handler == nil {
				return fmt.Errorf("channel %q route %q has no handler", ch.ID(), pattern)
			}
			if owner, exists := seen[pattern]; exists {
				return fmt.Errorf("channel route %q is declared by both %q and %q", pattern, owner, ch.ID())
			}
			seen[pattern] = ch.ID()
			mux.Handle(pattern, route.Handler)
		}
	}
	return nil
}

func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	channels := append([]Channel(nil), m.channels...)
	m.mu.RUnlock()
	started := make([]Channel, 0, len(channels))
	for _, ch := range channels {
		if err := ch.Start(ctx); err != nil {
			for i := len(started) - 1; i >= 0; i-- {
				_ = started[i].Shutdown(ctx)
			}
			return fmt.Errorf("start channel %q: %w", ch.ID(), err)
		}
		started = append(started, ch)
	}
	return nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	channels := append([]Channel(nil), m.channels...)
	m.mu.RUnlock()
	var errs []string
	for i := len(channels) - 1; i >= 0; i-- {
		if err := channels[i].Shutdown(ctx); err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", channels[i].ID(), err))
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("shutdown channels: %s", strings.Join(errs, "; "))
	}
	return nil
}

type InboundMessage struct {
	Channel          string         `json:"channel,omitempty"`
	PeerID           string         `json:"peer_id,omitempty"`
	SessionKey       string         `json:"session_key,omitempty"`
	SenderID         string         `json:"sender_id,omitempty"`
	Text             string         `json:"text,omitempty"`
	DisplayName      string         `json:"display_name,omitempty"`
	ReplyToMessageID string         `json:"reply_to_message_id,omitempty"`
	ThreadID         string         `json:"thread_id,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type OutboundMessage struct {
	Text             string         `json:"text,omitempty"`
	ReplyToMessageID string         `json:"reply_to_message_id,omitempty"`
	ThreadID         string         `json:"thread_id,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

type OutboundTarget struct {
	SubjectID      string            `json:"subject_id,omitempty"`
	ConversationID string            `json:"conversation_id,omitempty"`
	ThreadID       string            `json:"thread_id,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type OutboundDelivery struct {
	MessageID      string         `json:"message_id,omitempty"`
	ConversationID string         `json:"conversation_id,omitempty"`
	ThreadID       string         `json:"thread_id,omitempty"`
	ChunkIndex     int            `json:"chunk_index,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
}

type OutboundReceipt struct {
	Channel        string             `json:"channel"`
	SubjectID      string             `json:"subject_id,omitempty"`
	ConversationID string             `json:"conversation_id,omitempty"`
	ThreadID       string             `json:"thread_id,omitempty"`
	ChunkCount     int                `json:"chunk_count"`
	UsedBinding    bool               `json:"used_binding,omitempty"`
	Deliveries     []OutboundDelivery `json:"deliveries,omitempty"`
	Metadata       map[string]any     `json:"metadata,omitempty"`
}

type OutboundSender interface {
	Channel
	SendMessage(context.Context, OutboundTarget, OutboundMessage) (*OutboundReceipt, error)
}

type Dispatcher struct {
	AgentCoordinator *agent.Coordinator
	ToolExecutor     agent.ToolExecutor
	DefaultTimeout   func() context.Context
	RunTimeout       func() context.Context
	DefaultProfile   string
	DefaultModel     string
	SessionPolicy    func(sessionKey string) session.SessionPolicy
	PatchPolicy      func(sessionKey string, patch func(*session.SessionPolicy)) error
}

func (d *Dispatcher) Dispatch(ctx context.Context, msg InboundMessage) (*OutboundMessage, error) {
	if d == nil || d.AgentCoordinator == nil {
		return nil, fmt.Errorf("channel dispatcher is not configured")
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return nil, fmt.Errorf("message text is required")
	}
	peerID := strings.TrimSpace(msg.PeerID)
	if peerID == "" {
		return nil, fmt.Errorf("peer id is required")
	}
	if reply, handled, err := d.handleSessionCommand(msg); handled || err != nil {
		return reply, err
	}
	req := agent.RunRequest{
		PeerID:        peerID,
		SenderID:      strings.TrimSpace(msg.SenderID),
		SessionKey:    strings.TrimSpace(msg.SessionKey),
		Scope:         agent.ScopeMain,
		Messages:      []types.Message{{Role: "user", Content: text}},
		ToolExecutor:  d.ToolExecutor,
		Model:         strings.TrimSpace(d.DefaultModel),
		ActiveProfile: strings.TrimSpace(d.DefaultProfile),
	}
	result, err := d.AgentCoordinator.Run(ctx, req)
	if err != nil {
		return nil, err
	}
	if result == nil || strings.TrimSpace(result.AssistantText) == "" || result.SuppressedReply {
		return nil, nil
	}
	return &OutboundMessage{
		Text:             result.AssistantText,
		ReplyToMessageID: msg.ReplyToMessageID,
		ThreadID:         msg.ThreadID,
	}, nil
}

func (d *Dispatcher) handleSessionCommand(msg InboundMessage) (*OutboundMessage, bool, error) {
	name, arg, ok := parseDispatcherSlashCommand(msg.Text)
	if !ok {
		return nil, false, nil
	}
	switch name {
	case "activation":
		reply, err := d.handleActivationCommand(msg, arg)
		return reply, true, err
	default:
		return nil, false, nil
	}
}

func parseDispatcherSlashCommand(text string) (name, arg string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	text = text[1:]
	parts := strings.SplitN(text, " ", 2)
	name = strings.ToLower(strings.TrimSpace(parts[0]))
	if len(parts) == 2 {
		arg = strings.TrimSpace(parts[1])
	}
	return name, arg, name != ""
}

func (d *Dispatcher) handleActivationCommand(msg InboundMessage, arg string) (*OutboundMessage, error) {
	if d == nil || d.SessionPolicy == nil || d.PatchPolicy == nil {
		return &OutboundMessage{Text: "Activation policy is not available on this gateway."}, nil
	}
	fields := strings.Fields(strings.TrimSpace(arg))
	sessionKey := channelMessageSessionKey(msg)
	peerID := strings.TrimSpace(msg.PeerID)
	if len(fields) == 0 {
		return &OutboundMessage{Text: buildActivationStatusText(sessionKey, d.SessionPolicy(sessionKey), peerID, d.SessionPolicy(peerID))}, nil
	}
	if len(fields) == 1 {
		mode := session.NormalizeActivationMode(fields[0])
		if mode == "" {
			return &OutboundMessage{Text: activationUsageText()}, nil
		}
		if sessionKey == "" {
			return &OutboundMessage{Text: "Activation mode cannot be set for this conversation."}, nil
		}
		if err := d.PatchPolicy(sessionKey, func(policy *session.SessionPolicy) {
			policy.ActivationMode = mode
		}); err != nil {
			return nil, fmt.Errorf("persist activation policy: %w", err)
		}
		return &OutboundMessage{Text: fmt.Sprintf("Session activation: %s", mode)}, nil
	}
	if len(fields) == 2 {
		surface := strings.ToLower(strings.TrimSpace(fields[0]))
		mode := session.NormalizeActivationMode(fields[1])
		if mode == "" {
			return &OutboundMessage{Text: activationUsageText()}, nil
		}
		if peerID == "" {
			return &OutboundMessage{Text: "Activation defaults cannot be set for this conversation."}, nil
		}
		switch surface {
		case "group":
			if err := d.PatchPolicy(peerID, func(policy *session.SessionPolicy) {
				policy.GroupActivationMode = mode
			}); err != nil {
				return nil, fmt.Errorf("persist group activation policy: %w", err)
			}
			return &OutboundMessage{Text: fmt.Sprintf("Group activation default: %s", mode)}, nil
		case "chat":
			if err := d.PatchPolicy(peerID, func(policy *session.SessionPolicy) {
				policy.ChatActivationMode = mode
			}); err != nil {
				return nil, fmt.Errorf("persist chat activation policy: %w", err)
			}
			return &OutboundMessage{Text: fmt.Sprintf("Chat activation default: %s", mode)}, nil
		}
	}
	return &OutboundMessage{Text: activationUsageText()}, nil
}

func channelMessageSessionKey(msg InboundMessage) string {
	if sessionKey := strings.TrimSpace(msg.SessionKey); sessionKey != "" {
		return sessionKey
	}
	if conversationPeerID, _ := msg.Metadata["conversation_peer_id"].(string); strings.TrimSpace(conversationPeerID) != "" {
		return strings.TrimSpace(conversationPeerID)
	}
	return strings.TrimSpace(msg.PeerID)
}

func activationUsageText() string {
	return "Usage: /activation [mention|always|group <mention|always>|chat <mention|always>]"
}

func activationModeLabel(mode string) string {
	if normalized := session.NormalizeActivationMode(mode); normalized != "" {
		return normalized
	}
	return "inherit"
}

func buildActivationStatusText(sessionKey string, sessionPolicy session.SessionPolicy, peerID string, peerPolicy session.SessionPolicy) string {
	lines := []string{}
	if strings.TrimSpace(sessionKey) != "" {
		lines = append(lines, fmt.Sprintf("Session activation: %s", activationModeLabel(sessionPolicy.ActivationMode)))
	}
	if strings.TrimSpace(peerID) != "" {
		lines = append(lines, fmt.Sprintf("Group activation default: %s", activationModeLabel(peerPolicy.GroupActivationMode)))
		lines = append(lines, fmt.Sprintf("Chat activation default: %s", activationModeLabel(peerPolicy.ChatActivationMode)))
	}
	if len(lines) == 0 {
		return "Activation policy is not available for this conversation."
	}
	return strings.Join(lines, "\n")
}

func ChannelInboxSessionKey(peerID, conversationPeerID string) string {
	peerID = strings.TrimSpace(peerID)
	conversationPeerID = strings.TrimSpace(conversationPeerID)
	if peerID == "" || conversationPeerID == "" {
		return ""
	}
	return peerID + "::channel::" + conversationPeerID
}

func SessionKeyOwnerPeer(sessionKey string) string {
	sessionKey = strings.TrimSpace(sessionKey)
	if sessionKey == "" {
		return ""
	}
	owner, _, ok := strings.Cut(sessionKey, "::")
	if !ok {
		return ""
	}
	return strings.TrimSpace(owner)
}

func normalizeOutboundReceipt(channelID string, target OutboundTarget, receipt *OutboundReceipt) *OutboundReceipt {
	if receipt == nil {
		receipt = &OutboundReceipt{}
	}
	if strings.TrimSpace(receipt.Channel) == "" {
		receipt.Channel = strings.TrimSpace(channelID)
	}
	if strings.TrimSpace(receipt.SubjectID) == "" {
		receipt.SubjectID = strings.TrimSpace(target.SubjectID)
	}
	if strings.TrimSpace(receipt.ConversationID) == "" {
		receipt.ConversationID = strings.TrimSpace(target.ConversationID)
	}
	if strings.TrimSpace(receipt.ThreadID) == "" {
		receipt.ThreadID = strings.TrimSpace(target.ThreadID)
	}
	if receipt.ChunkCount == 0 && len(receipt.Deliveries) > 0 {
		receipt.ChunkCount = len(receipt.Deliveries)
	}
	if strings.TrimSpace(receipt.ConversationID) == "" || strings.TrimSpace(receipt.ThreadID) == "" {
		for _, delivery := range receipt.Deliveries {
			if strings.TrimSpace(receipt.ConversationID) == "" && strings.TrimSpace(delivery.ConversationID) != "" {
				receipt.ConversationID = strings.TrimSpace(delivery.ConversationID)
			}
			if strings.TrimSpace(receipt.ThreadID) == "" && strings.TrimSpace(delivery.ThreadID) != "" {
				receipt.ThreadID = strings.TrimSpace(delivery.ThreadID)
			}
		}
	}
	return receipt
}
