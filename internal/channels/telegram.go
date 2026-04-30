package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/session"
)

const telegramAPIBaseURL = "https://api.telegram.org"

type telegramClient interface {
	GetMe(ctx context.Context, token string) (*telegramUser, error)
	GetUpdates(ctx context.Context, token string, offset int64, timeoutSeconds int) ([]telegramUpdate, error)
	SendMessage(ctx context.Context, token string, req telegramSendMessageRequest) (*telegramMessage, error)
	SetWebhook(ctx context.Context, token string, req telegramSetWebhookRequest) error
	DeleteWebhook(ctx context.Context, token string, dropPending bool) error
}

type TelegramOptions struct {
	BindingStorePath string
	PairingStorePath string
	SessionPolicy    func(sessionKey string) session.SessionPolicy
}

type telegramPolicy struct {
	activationMode   string
	replyActivation  bool
	threadMode       string
	senderSet        map[int64]struct{}
	commandSenderSet map[int64]struct{}
	topicSet         map[int64]struct{}
}

type Telegram struct {
	cfg         config.TelegramChannelConfig
	client      telegramClient
	dispatch    func(context.Context, InboundMessage) (*OutboundMessage, error)
	webhookPath string
	bindings    *BindingStore

	mu               sync.RWMutex
	self             *telegramUser
	started          bool
	cancel           context.CancelFunc
	wg               sync.WaitGroup
	allowSet         map[int64]struct{}
	senderSet        map[int64]struct{}
	commandSenderSet map[int64]struct{}
	policies         map[int64]telegramPolicy
	sessionPolicy    func(sessionKey string) session.SessionPolicy
}

func NewTelegramChannel(cfg config.TelegramChannelConfig, dispatcher *Dispatcher, client telegramClient, opts *TelegramOptions) (*Telegram, error) {
	if client == nil {
		client = &telegramHTTPClient{baseURL: telegramAPIBaseURL, httpClient: &http.Client{Timeout: 30 * time.Second}}
	}
	cfg.Mode = telegramMode(cfg.Mode)
	cfg.ActivationMode = telegramActivationMode(cfg.ActivationMode)
	cfg.DMPolicy = NormalizeDirectMessagePolicy(cfg.DMPolicy)
	if cfg.PairingCodeTTL <= 0 {
		cfg.PairingCodeTTL = 24 * time.Hour
	}
	if cfg.TextChunkLimit <= 0 {
		cfg.TextChunkLimit = 4096
	}
	cfg.TextChunkMode = NormalizeTextChunkMode(cfg.TextChunkMode)
	cfg.StreamQueueMode = NormalizeStreamQueueMode(cfg.StreamQueueMode)
	if cfg.StreamThrottle <= 0 {
		cfg.StreamThrottle = 750 * time.Millisecond
	}
	cfg.ThreadMode = telegramThreadMode(cfg.ThreadMode)
	var webhookPath string
	if strings.EqualFold(strings.TrimSpace(cfg.Mode), "webhook") {
		parsed, err := url.Parse(strings.TrimSpace(cfg.WebhookURL))
		if err != nil {
			return nil, fmt.Errorf("parse telegram webhook url: %w", err)
		}
		webhookPath = strings.TrimSpace(parsed.EscapedPath())
		if webhookPath == "" {
			webhookPath = strings.TrimSpace(parsed.Path)
		}
		if webhookPath == "" {
			return nil, fmt.Errorf("telegram webhook url must include a path")
		}
	}
	allowSet := make(map[int64]struct{}, len(cfg.AllowedChatIDs))
	for _, id := range cfg.AllowedChatIDs {
		allowSet[id] = struct{}{}
	}
	senderSet := make(map[int64]struct{}, len(cfg.AllowedSenderIDs))
	for _, id := range cfg.AllowedSenderIDs {
		senderSet[id] = struct{}{}
	}
	commandSenderSet := make(map[int64]struct{}, len(cfg.CommandSenderIDs))
	for _, id := range cfg.CommandSenderIDs {
		commandSenderSet[id] = struct{}{}
	}
	policies := make(map[int64]telegramPolicy, len(cfg.GroupPolicies))
	for _, group := range cfg.GroupPolicies {
		policy := telegramPolicy{
			activationMode:   strings.TrimSpace(group.ActivationMode),
			replyActivation:  cfg.ReplyActivation,
			threadMode:       strings.TrimSpace(group.ThreadMode),
			senderSet:        make(map[int64]struct{}, len(group.AllowedSenderIDs)),
			commandSenderSet: make(map[int64]struct{}, len(group.CommandSenderIDs)),
			topicSet:         make(map[int64]struct{}, len(group.AllowedTopicIDs)),
		}
		for _, id := range group.AllowedSenderIDs {
			policy.senderSet[id] = struct{}{}
		}
		for _, id := range group.CommandSenderIDs {
			policy.commandSenderSet[id] = struct{}{}
		}
		if group.ReplyActivation != nil {
			policy.replyActivation = *group.ReplyActivation
		}
		for _, id := range group.AllowedTopicIDs {
			policy.topicSet[id] = struct{}{}
		}
		policies[group.ChatID] = policy
	}
	var bindings *BindingStore
	if cfg.DMPolicy == "pairing" {
		bindingStorePath := ""
		if opts != nil {
			bindingStorePath = strings.TrimSpace(opts.BindingStorePath)
			if bindingStorePath == "" {
				bindingStorePath = strings.TrimSpace(opts.PairingStorePath)
			}
		}
		if bindingStorePath == "" {
			return nil, fmt.Errorf("telegram pairing requires a pairing store path")
		}
		bindings = NewBindingStore(bindingStorePath)
	}
	t := &Telegram{
		cfg:              cfg,
		client:           client,
		webhookPath:      webhookPath,
		allowSet:         allowSet,
		senderSet:        senderSet,
		commandSenderSet: commandSenderSet,
		policies:         policies,
		bindings:         bindings,
	}
	if opts != nil {
		t.sessionPolicy = opts.SessionPolicy
	}
	if dispatcher != nil {
		t.dispatch = dispatcher.Dispatch
	}
	return t, nil
}

func (t *Telegram) ID() string { return "telegram" }

func (t *Telegram) SendMessage(ctx context.Context, target OutboundTarget, msg OutboundMessage) (*OutboundReceipt, error) {
	if t == nil || !t.cfg.Enabled {
		return nil, fmt.Errorf("telegram channel is not enabled")
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return nil, fmt.Errorf("message text is required")
	}
	subjectID := strings.TrimSpace(target.SubjectID)
	conversationID := strings.TrimSpace(target.ConversationID)
	usedBinding := false
	if conversationID == "" && subjectID != "" && t.bindings != nil {
		approved, err := t.bindings.ApprovedBinding("telegram", subjectID)
		if err != nil {
			return nil, err
		}
		if approved != nil {
			conversationID = strings.TrimSpace(approved.ConversationID)
			if subjectID == "" {
				subjectID = strings.TrimSpace(approved.SubjectID)
			}
			usedBinding = true
		}
	}
	if conversationID == "" {
		return nil, fmt.Errorf("conversation_id is required when no approved binding matches subject_id")
	}
	chatID, err := strconv.ParseInt(conversationID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid telegram conversation_id %q", conversationID)
	}
	if !t.allowedChat(chatID) {
		return nil, fmt.Errorf("telegram chat %d is not allowed by configuration", chatID)
	}
	replyToID, err := parseTelegramOptionalInt64(strings.TrimSpace(msg.ReplyToMessageID), "reply_to_message_id")
	if err != nil {
		return nil, err
	}
	threadValue := strings.TrimSpace(msg.ThreadID)
	if threadValue == "" {
		threadValue = strings.TrimSpace(target.ThreadID)
	}
	threadID, err := parseTelegramOptionalInt64(threadValue, "thread_id")
	if err != nil {
		return nil, err
	}
	chunks := ChunkTextWithPolicy(text, TextChunkPolicy{
		Limit: t.cfg.TextChunkLimit,
		Mode:  t.cfg.TextChunkMode,
	})
	deliveries := make([]OutboundDelivery, 0, len(chunks))
	err = SendChunked(ctx, chunks, StreamQueuePolicy{Mode: t.cfg.StreamQueueMode, Throttle: t.cfg.StreamThrottle}, func(idx int, chunk string) error {
		req := telegramSendMessageRequest{
			ChatID: chatID,
			Text:   chunk,
		}
		if idx == 0 && replyToID != 0 {
			req.ReplyToMessageID = replyToID
		}
		if threadID != 0 {
			req.MessageThreadID = threadID
		}
		sent, err := t.client.SendMessage(ctx, t.cfg.BotToken, req)
		if err != nil {
			return err
		}
		delivery := OutboundDelivery{
			ConversationID: strconv.FormatInt(chatID, 10),
			ChunkIndex:     idx + 1,
		}
		if sent != nil {
			if sent.MessageID != 0 {
				delivery.MessageID = strconv.FormatInt(sent.MessageID, 10)
			}
			if sent.Chat.ID != 0 {
				delivery.ConversationID = strconv.FormatInt(sent.Chat.ID, 10)
			}
			if sent.MessageThreadID != 0 {
				delivery.ThreadID = strconv.FormatInt(sent.MessageThreadID, 10)
				if threadValue == "" {
					threadValue = delivery.ThreadID
				}
			}
		}
		deliveries = append(deliveries, delivery)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &OutboundReceipt{
		Channel:        t.ID(),
		SubjectID:      subjectID,
		ConversationID: strconv.FormatInt(chatID, 10),
		ThreadID:       threadValue,
		ChunkCount:     len(chunks),
		UsedBinding:    usedBinding,
		Deliveries:     deliveries,
		Metadata: map[string]any{
			"stream_queue_mode":  t.cfg.StreamQueueMode,
			"stream_throttle_ms": t.cfg.StreamThrottle.Milliseconds(),
		},
	}, nil
}

func (t *Telegram) Routes() []Route {
	if t == nil || !t.cfg.Enabled || !strings.EqualFold(strings.TrimSpace(t.cfg.Mode), "webhook") || t.webhookPath == "" {
		return nil
	}
	return []Route{{Pattern: "POST " + t.webhookPath, Handler: http.HandlerFunc(t.serveWebhook)}}
}

func (t *Telegram) Start(ctx context.Context) error {
	if t == nil || !t.cfg.Enabled {
		return nil
	}
	t.mu.Lock()
	if t.started {
		t.mu.Unlock()
		return nil
	}
	t.mu.Unlock()
	self, err := t.client.GetMe(ctx, t.cfg.BotToken)
	if err != nil {
		return fmt.Errorf("telegram getMe: %w", err)
	}
	t.mu.Lock()
	t.self = self
	t.started = true
	t.mu.Unlock()
	mode := strings.ToLower(strings.TrimSpace(t.cfg.Mode))
	if mode == "" {
		mode = "polling"
	}
	switch mode {
	case "webhook":
		if err := t.client.SetWebhook(ctx, t.cfg.BotToken, telegramSetWebhookRequest{URL: t.cfg.WebhookURL, SecretToken: t.cfg.WebhookSecret}); err != nil {
			return fmt.Errorf("telegram setWebhook: %w", err)
		}
		return nil
	case "polling":
		if err := t.client.DeleteWebhook(ctx, t.cfg.BotToken, false); err != nil {
			return fmt.Errorf("telegram deleteWebhook: %w", err)
		}
		pollCtx, cancel := context.WithCancel(context.Background())
		t.mu.Lock()
		t.cancel = cancel
		t.mu.Unlock()
		t.wg.Add(1)
		go t.pollLoop(pollCtx)
		return nil
	default:
		return fmt.Errorf("unsupported telegram mode %q", t.cfg.Mode)
	}
}

func (t *Telegram) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	cancel := t.cancel
	t.cancel = nil
	t.started = false
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		t.wg.Wait()
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Telegram) pollLoop(ctx context.Context) {
	defer t.wg.Done()
	offset := int64(0)
	timeoutSeconds := int(t.cfg.PollTimeout / time.Second)
	if timeoutSeconds < 1 {
		timeoutSeconds = 1
	}
	if timeoutSeconds > 50 {
		timeoutSeconds = 50
	}
	for {
		updates, err := t.client.GetUpdates(ctx, t.cfg.BotToken, offset, timeoutSeconds)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("telegram polling failed", "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		for _, update := range updates {
			if update.UpdateID >= offset {
				offset = update.UpdateID + 1
			}
			if err := t.handleUpdate(ctx, update); err != nil {
				slog.Warn("telegram update failed", "update_id", update.UpdateID, "err", err)
			}
		}
		if len(updates) == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
}

func (t *Telegram) serveWebhook(w http.ResponseWriter, r *http.Request) {
	if t == nil || !t.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "use POST", http.StatusMethodNotAllowed)
		return
	}
	if secret := strings.TrimSpace(t.cfg.WebhookSecret); secret != "" {
		if strings.TrimSpace(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")) != secret {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	var update telegramUpdate
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&update); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if err := t.handleUpdate(r.Context(), update); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (t *Telegram) handleUpdate(ctx context.Context, update telegramUpdate) error {
	msg := update.primaryMessage()
	if msg == nil {
		return nil
	}
	if !t.allowedChat(msg.Chat.ID) {
		return nil
	}
	policy := t.policyForChat(msg.Chat.ID)
	if !policy.allowsTopic(msg.MessageThreadID) {
		return nil
	}
	text := strings.TrimSpace(msg.Text)
	if text == "" {
		text = strings.TrimSpace(msg.Caption)
	}
	if text == "" {
		return nil
	}
	if msg.Chat.Type == "private" {
		allowed, reply, err := t.authorizeDirectMessage(ctx, msg)
		if err != nil {
			return err
		}
		if !allowed {
			if reply != nil {
				_, err := t.client.SendMessage(ctx, t.cfg.BotToken, *reply)
				return err
			}
			return nil
		}
	} else if !policy.allowsSender(telegramUserID(msg.From)) {
		return nil
	}
	if policy.blocksCommand(telegramUserID(msg.From), text) {
		return nil
	}
	dispatchSessionKey, dispatchPeerID := t.dispatchRoute(msg, policy)
	policy = t.applySessionActivationPolicy(msg, dispatchSessionKey, dispatchPeerID, policy)
	if !t.shouldProcessMessage(msg, text, policy) {
		return nil
	}
	text = t.stripBotMention(text)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if t.dispatch == nil {
		return fmt.Errorf("telegram channel has no dispatcher")
	}
	threadID := ""
	if policy.threadMode == "topic" && msg.MessageThreadID != 0 {
		threadID = strconv.FormatInt(msg.MessageThreadID, 10)
	}
	conversationPeerID := telegramPeerID(msg.Chat.ID, msg.MessageThreadID, policy.threadMode)
	reply, err := t.dispatch(ctx, InboundMessage{
		Channel:          "telegram",
		PeerID:           dispatchPeerID,
		SessionKey:       dispatchSessionKey,
		SenderID:         telegramSenderID(msg.From),
		Text:             text,
		DisplayName:      telegramDisplayName(msg.From),
		ReplyToMessageID: strconv.FormatInt(msg.MessageID, 10),
		ThreadID:         threadID,
		Metadata: map[string]any{
			"chat_id":              msg.Chat.ID,
			"chat_type":            msg.Chat.Type,
			"message_id":           msg.MessageID,
			"conversation_peer_id": conversationPeerID,
		},
	})
	if err != nil {
		return err
	}
	if reply == nil || strings.TrimSpace(reply.Text) == "" {
		return nil
	}
	chunks := ChunkTextWithPolicy(reply.Text, TextChunkPolicy{
		Limit: t.cfg.TextChunkLimit,
		Mode:  t.cfg.TextChunkMode,
	})
	for i, chunk := range chunks {
		sendReq := telegramSendMessageRequest{
			ChatID: msg.Chat.ID,
			Text:   chunk,
		}
		if i == 0 {
			sendReq.ReplyToMessageID = msg.MessageID
		}
		if msg.MessageThreadID != 0 {
			sendReq.MessageThreadID = msg.MessageThreadID
		}
		if _, err := t.client.SendMessage(ctx, t.cfg.BotToken, sendReq); err != nil {
			return err
		}
	}
	return nil
}

func (t *Telegram) dispatchRoute(msg *telegramMessage, policy telegramPolicy) (sessionKey, peerID string) {
	conversationPeerID := telegramPeerID(msg.Chat.ID, msg.MessageThreadID, policy.threadMode)
	peerID = conversationPeerID
	if routedPeerID := strings.TrimSpace(t.cfg.InboxPeerID); routedPeerID != "" {
		peerID = routedPeerID
		sessionKey = ChannelInboxSessionKey(routedPeerID, conversationPeerID)
	}
	if msg.Chat.Type == "private" && t.bindings != nil {
		approved, err := t.bindings.ApprovedBinding("telegram", telegramSenderID(msg.From))
		if err == nil && approved != nil {
			if routedPeerID := strings.TrimSpace(approved.PeerID); routedPeerID != "" {
				peerID = routedPeerID
				sessionKey = ChannelInboxSessionKey(routedPeerID, conversationPeerID)
			}
			if explicitSessionKey := strings.TrimSpace(approved.SessionKey); explicitSessionKey != "" {
				if routedPeerID := SessionKeyOwnerPeer(explicitSessionKey); routedPeerID != "" {
					peerID = routedPeerID
				}
				sessionKey = explicitSessionKey
			}
		}
	}
	return sessionKey, peerID
}

func (t *Telegram) applySessionActivationPolicy(msg *telegramMessage, sessionKey, peerID string, policy telegramPolicy) telegramPolicy {
	if t == nil || t.sessionPolicy == nil {
		return policy
	}
	if override := session.NormalizeActivationMode(t.sessionPolicy(sessionKey).ActivationMode); override != "" {
		policy.activationMode = override
		return policy
	}
	if strings.TrimSpace(peerID) == "" {
		return policy
	}
	peerPolicy := t.sessionPolicy(peerID)
	if msg != nil && msg.Chat.Type == "private" {
		if override := session.NormalizeActivationMode(peerPolicy.ChatActivationMode); override != "" {
			policy.activationMode = override
		}
		return policy
	}
	if override := session.NormalizeActivationMode(peerPolicy.GroupActivationMode); override != "" {
		policy.activationMode = override
	}
	return policy
}

func (t *Telegram) authorizeDirectMessage(ctx context.Context, msg *telegramMessage) (bool, *telegramSendMessageRequest, error) {
	userID := telegramUserID(msg.From)
	if userID == 0 {
		return false, nil, nil
	}
	subjectID := telegramSenderID(msg.From)
	_, allowedSender := t.senderSet[userID]
	decision, err := AuthorizeDirectMessage(DirectMessageAuthRequest{
		Channel:        "telegram",
		Policy:         t.cfg.DMPolicy,
		SubjectID:      subjectID,
		ConversationID: strconv.FormatInt(msg.Chat.ID, 10),
		Username:       strings.TrimSpace(msg.From.Username),
		DisplayName:    telegramDisplayName(msg.From),
		Metadata: map[string]string{
			"chat_id": strconv.FormatInt(msg.Chat.ID, 10),
		},
		PairingCodeTTL: t.cfg.PairingCodeTTL,
		Allowed:        allowedSender,
		Store:          t.bindings,
	})
	if err != nil {
		return false, nil, err
	}
	if decision.Allowed {
		return true, nil, nil
	}
	if strings.TrimSpace(decision.ReplyText) == "" {
		return false, nil, nil
	}
	return false, &telegramSendMessageRequest{
		ChatID: msg.Chat.ID,
		Text:   decision.ReplyText,
	}, nil
}

func (t *Telegram) allowedChat(chatID int64) bool {
	if len(t.allowSet) == 0 {
		return true
	}
	_, ok := t.allowSet[chatID]
	return ok
}

func (t *Telegram) shouldProcessMessage(msg *telegramMessage, text string, policy telegramPolicy) bool {
	if msg == nil {
		return false
	}
	if msg.Chat.Type == "private" {
		return true
	}
	if policy.activationMode != "mention" {
		return true
	}
	if policy.replyActivation && msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		self := t.selfUser()
		if self != nil && msg.ReplyToMessage.From.ID == self.ID {
			return true
		}
	}
	username := strings.ToLower(strings.TrimSpace(t.selfUsername()))
	if username == "" {
		return false
	}
	mention := "@" + username
	return strings.Contains(strings.ToLower(text), mention)
}

func (t *Telegram) stripBotMention(text string) string {
	username := strings.TrimSpace(t.selfUsername())
	if username == "" {
		return strings.TrimSpace(text)
	}
	mention := "@" + username
	replacer := strings.NewReplacer(mention, " ", strings.ToLower(mention), " ")
	cleaned := replacer.Replace(text)
	return strings.Join(strings.Fields(cleaned), " ")
}

func (t *Telegram) selfUsername() string {
	self := t.selfUser()
	if self == nil {
		return ""
	}
	return self.Username
}

func (t *Telegram) selfUser() *telegramUser {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.self
}

func (t *Telegram) policyForChat(chatID int64) telegramPolicy {
	policy := telegramPolicy{
		activationMode:   t.cfg.ActivationMode,
		replyActivation:  t.cfg.ReplyActivation,
		threadMode:       t.cfg.ThreadMode,
		commandSenderSet: t.commandSenderSet,
	}
	if override, ok := t.policies[chatID]; ok {
		if strings.TrimSpace(override.activationMode) != "" {
			policy.activationMode = telegramActivationMode(override.activationMode)
		}
		if strings.TrimSpace(override.threadMode) != "" {
			policy.threadMode = telegramThreadMode(override.threadMode)
		}
		if len(override.senderSet) > 0 {
			policy.senderSet = override.senderSet
		}
		if len(override.commandSenderSet) > 0 {
			policy.commandSenderSet = override.commandSenderSet
		}
		policy.replyActivation = override.replyActivation
		if len(override.topicSet) > 0 {
			policy.topicSet = override.topicSet
		}
	}
	if policy.activationMode == "" {
		policy.activationMode = telegramActivationMode("")
	}
	if policy.threadMode == "" {
		policy.threadMode = telegramThreadMode("")
	}
	return policy
}

func (p telegramPolicy) allowsSender(userID int64) bool {
	if len(p.senderSet) == 0 {
		return true
	}
	_, ok := p.senderSet[userID]
	return ok
}

func (p telegramPolicy) allowsTopic(threadID int64) bool {
	if len(p.topicSet) == 0 {
		return true
	}
	_, ok := p.topicSet[threadID]
	return ok
}

func (p telegramPolicy) blocksCommand(userID int64, text string) bool {
	if len(p.commandSenderSet) == 0 {
		return false
	}
	if !isTelegramCommand(text) {
		return false
	}
	_, ok := p.commandSenderSet[userID]
	return !ok
}

func isTelegramCommand(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), "/")
}

func telegramPeerID(chatID, threadID int64, threadMode string) string {
	if threadMode == "topic" && threadID != 0 {
		return fmt.Sprintf("telegram:%d:%d", chatID, threadID)
	}
	return fmt.Sprintf("telegram:%d", chatID)
}

func telegramMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "webhook") {
		return "webhook"
	}
	return "polling"
}

func telegramActivationMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "mention") {
		return "mention"
	}
	if strings.EqualFold(strings.TrimSpace(mode), "always") {
		return "always"
	}
	return "always"
}

func telegramThreadMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "chat") {
		return "chat"
	}
	return "topic"
}

func telegramUserID(user *telegramUser) int64 {
	if user == nil {
		return 0
	}
	return user.ID
}

func telegramSenderID(user *telegramUser) string {
	if user == nil {
		return ""
	}
	return strconv.FormatInt(user.ID, 10)
}

func telegramDisplayName(user *telegramUser) string {
	if user == nil {
		return ""
	}
	name := strings.TrimSpace(strings.TrimSpace(user.FirstName + " " + user.LastName))
	if name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

func parseTelegramOptionalInt64(raw, field string) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid telegram %s %q", field, raw)
	}
	return value, nil
}

type telegramHTTPClient struct {
	baseURL    string
	httpClient *http.Client
}

func (c *telegramHTTPClient) GetMe(ctx context.Context, token string) (*telegramUser, error) {
	var resp struct {
		OK     bool          `json:"ok"`
		Result *telegramUser `json:"result"`
	}
	if err := c.call(ctx, token, "getMe", nil, &resp); err != nil {
		return nil, err
	}
	if !resp.OK || resp.Result == nil {
		return nil, fmt.Errorf("telegram getMe returned no user")
	}
	return resp.Result, nil
}

func (c *telegramHTTPClient) GetUpdates(ctx context.Context, token string, offset int64, timeoutSeconds int) ([]telegramUpdate, error) {
	var resp struct {
		OK     bool             `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}
	body := map[string]any{"offset": offset, "timeout": timeoutSeconds}
	if err := c.call(ctx, token, "getUpdates", body, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("telegram getUpdates returned ok=false")
	}
	return resp.Result, nil
}

func (c *telegramHTTPClient) SendMessage(ctx context.Context, token string, req telegramSendMessageRequest) (*telegramMessage, error) {
	var resp struct {
		OK     bool             `json:"ok"`
		Result *telegramMessage `json:"result"`
	}
	if err := c.call(ctx, token, "sendMessage", req, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("telegram sendMessage returned ok=false")
	}
	return resp.Result, nil
}

func (c *telegramHTTPClient) SetWebhook(ctx context.Context, token string, req telegramSetWebhookRequest) error {
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.call(ctx, token, "setWebhook", req, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram setWebhook returned ok=false")
	}
	return nil
}

func (c *telegramHTTPClient) DeleteWebhook(ctx context.Context, token string, dropPending bool) error {
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := c.call(ctx, token, "deleteWebhook", map[string]any{"drop_pending_updates": dropPending}, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("telegram deleteWebhook returned ok=false")
	}
	return nil
}

func (c *telegramHTTPClient) call(ctx context.Context, token, method string, body any, out any) error {
	base := strings.TrimRight(strings.TrimSpace(c.baseURL), "/")
	if base == "" {
		base = telegramAPIBaseURL
	}
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+token+"/"+method, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	httpClient := c.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("telegram %s HTTP %d: %s", method, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

type telegramUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *telegramMessage `json:"message,omitempty"`
	EditedMessage *telegramMessage `json:"edited_message,omitempty"`
}

func (u telegramUpdate) primaryMessage() *telegramMessage {
	if u.Message != nil {
		return u.Message
	}
	return u.EditedMessage
}

type telegramMessage struct {
	MessageID       int64            `json:"message_id"`
	MessageThreadID int64            `json:"message_thread_id,omitempty"`
	Text            string           `json:"text,omitempty"`
	Caption         string           `json:"caption,omitempty"`
	Chat            telegramChat     `json:"chat"`
	From            *telegramUser    `json:"from,omitempty"`
	ReplyToMessage  *telegramMessage `json:"reply_to_message,omitempty"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type telegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

type telegramSendMessageRequest struct {
	ChatID           int64  `json:"chat_id"`
	Text             string `json:"text"`
	ReplyToMessageID int64  `json:"reply_to_message_id,omitempty"`
	MessageThreadID  int64  `json:"message_thread_id,omitempty"`
	DisablePreview   bool   `json:"disable_web_page_preview,omitempty"`
	ParseMode        string `json:"parse_mode,omitempty"`
}

type telegramSetWebhookRequest struct {
	URL         string `json:"url"`
	SecretToken string `json:"secret_token,omitempty"`
}
