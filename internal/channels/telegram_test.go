package channels

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/session"
)

type telegramClientStub struct {
	me               *telegramUser
	updates          []telegramUpdate
	getMeErr         error
	getUpdatesErr    error
	sendMessageErr   error
	sendResults      []*telegramMessage
	setWebhookErr    error
	deleteWebhookErr error
	sent             []telegramSendMessageRequest
	setWebhookReq    *telegramSetWebhookRequest
	deleteCalled     bool
	getUpdatesCalls  int
}

func (s *telegramClientStub) GetMe(context.Context, string) (*telegramUser, error) {
	if s.getMeErr != nil {
		return nil, s.getMeErr
	}
	if s.me == nil {
		return &telegramUser{ID: 99, Username: "koiosbot", IsBot: true}, nil
	}
	return s.me, nil
}

func (s *telegramClientStub) GetUpdates(context.Context, string, int64, int) ([]telegramUpdate, error) {
	s.getUpdatesCalls++
	if s.getUpdatesErr != nil {
		return nil, s.getUpdatesErr
	}
	updates := s.updates
	s.updates = nil
	return updates, nil
}

func (s *telegramClientStub) SendMessage(_ context.Context, _ string, req telegramSendMessageRequest) (*telegramMessage, error) {
	if s.sendMessageErr != nil {
		return nil, s.sendMessageErr
	}
	s.sent = append(s.sent, req)
	if len(s.sendResults) > 0 {
		result := s.sendResults[0]
		s.sendResults = s.sendResults[1:]
		return result, nil
	}
	return &telegramMessage{MessageID: int64(len(s.sent)), Chat: telegramChat{ID: req.ChatID, Type: "private"}, MessageThreadID: req.MessageThreadID}, nil
}

func (s *telegramClientStub) SetWebhook(_ context.Context, _ string, req telegramSetWebhookRequest) error {
	if s.setWebhookErr != nil {
		return s.setWebhookErr
	}
	s.setWebhookReq = &req
	return nil
}

func (s *telegramClientStub) DeleteWebhook(context.Context, string, bool) error {
	if s.deleteWebhookErr != nil {
		return s.deleteWebhookErr
	}
	s.deleteCalled = true
	return nil
}

func TestTelegramWebhookDispatchesAndReplies(t *testing.T) {
	client := &telegramClientStub{}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "webhook",
		WebhookURL:     "https://example.com/v1/channels/telegram/webhook",
		WebhookSecret:  "secret",
		PollTimeout:    25 * time.Second,
		TextChunkLimit: 4096,
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		if msg.PeerID != "telegram:123" {
			t.Fatalf("PeerID = %q", msg.PeerID)
		}
		if msg.Text != "hello" {
			t.Fatalf("Text = %q", msg.Text)
		}
		return &OutboundMessage{Text: "hi there"}, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	route := ch.Routes()[0]
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/telegram/webhook", strings.NewReader(`{"update_id":1,"message":{"message_id":42,"text":"hello","chat":{"id":123,"type":"private"},"from":{"id":7,"first_name":"Pat"}}}`))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "secret")
	route.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if client.setWebhookReq == nil || client.setWebhookReq.URL != "https://example.com/v1/channels/telegram/webhook" {
		t.Fatalf("unexpected setWebhook request: %#v", client.setWebhookReq)
	}
	if len(client.sent) != 1 {
		t.Fatalf("sent count = %d, want 1", len(client.sent))
	}
	if client.sent[0].ChatID != 123 || client.sent[0].ReplyToMessageID != 42 || client.sent[0].Text != "hi there" {
		t.Fatalf("unexpected sendMessage payload: %#v", client.sent[0])
	}
}

func TestTelegramWebhookSplitsRepliesByChunkLimit(t *testing.T) {
	client := &telegramClientStub{}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "webhook",
		WebhookURL:     "https://example.com/v1/channels/telegram/webhook",
		WebhookSecret:  "secret",
		PollTimeout:    25 * time.Second,
		TextChunkLimit: 10,
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		return &OutboundMessage{Text: "alpha beta gamma delta"}, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/telegram/webhook", strings.NewReader(`{"update_id":1,"message":{"message_id":42,"text":"hello","chat":{"id":123,"type":"private"},"from":{"id":7,"first_name":"Pat"}}}`))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "secret")
	ch.Routes()[0].Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(client.sent) != 3 {
		t.Fatalf("sent count = %d, want 3", len(client.sent))
	}
	if client.sent[0].Text != "alpha beta" || client.sent[1].Text != "gamma" || client.sent[2].Text != "delta" {
		t.Fatalf("unexpected chunked messages: %#v", client.sent)
	}
	if client.sent[0].ReplyToMessageID != 42 || client.sent[1].ReplyToMessageID != 0 || client.sent[2].ReplyToMessageID != 0 {
		t.Fatalf("unexpected reply ids for chunked messages: %#v", client.sent)
	}
}

func TestTelegramWebhookUsesConfiguredChunkMode(t *testing.T) {
	client := &telegramClientStub{}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "webhook",
		WebhookURL:     "https://example.com/v1/channels/telegram/webhook",
		WebhookSecret:  "secret",
		PollTimeout:    25 * time.Second,
		TextChunkLimit: 14,
		TextChunkMode:  "word",
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		return &OutboundMessage{Text: "one two\n\nthree four"}, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/telegram/webhook", strings.NewReader(`{"update_id":1,"message":{"message_id":42,"text":"hello","chat":{"id":123,"type":"private"},"from":{"id":7,"first_name":"Pat"}}}`))
	req.Header.Set("X-Telegram-Bot-Api-Secret-Token", "secret")
	ch.Routes()[0].Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(client.sent) != 2 {
		t.Fatalf("sent count = %d, want 2", len(client.sent))
	}
	if client.sent[0].Text != "one two\n\nthree" || client.sent[1].Text != "four" {
		t.Fatalf("unexpected mode-aware chunked messages: %#v", client.sent)
	}
}

func TestTelegramWebhookRejectsBadSecret(t *testing.T) {
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:       true,
		BotToken:      "token",
		Mode:          "webhook",
		WebhookURL:    "https://example.com/v1/channels/telegram/webhook",
		WebhookSecret: "secret",
		PollTimeout:   25 * time.Second,
	}, nil, &telegramClientStub{}, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/channels/telegram/webhook", strings.NewReader(`{}`))
	ch.Routes()[0].Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
}

func TestTelegramMentionActivationSkipsUnmentionedGroupMessages(t *testing.T) {
	client := &telegramClientStub{me: &telegramUser{ID: 99, Username: "koiosbot", IsBot: true}}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "polling",
		PollTimeout:    25 * time.Second,
		ActivationMode: "mention",
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	called := false
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		called = true
		if msg.Text != "hello" {
			t.Fatalf("Text = %q", msg.Text)
		}
		return nil, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !client.deleteCalled {
		t.Fatal("expected polling start to delete existing webhook")
	}
	if err := ch.handleUpdate(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 1,
		Text:      "plain group message",
		Chat:      telegramChat{ID: 123, Type: "group"},
		From:      &telegramUser{ID: 7, FirstName: "Pat"},
	}}); err != nil {
		t.Fatalf("handleUpdate: %v", err)
	}
	if called {
		t.Fatal("expected unmentioned group message to be skipped")
	}
	if err := ch.handleUpdate(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 2,
		Text:      "@koiosbot hello",
		Chat:      telegramChat{ID: 123, Type: "group"},
		From:      &telegramUser{ID: 7, FirstName: "Pat"},
	}}); err != nil {
		t.Fatalf("handleUpdate: %v", err)
	}
	if !called {
		t.Fatal("expected mentioned group message to be dispatched")
	}
	if err := ch.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestTelegramMentionActivationCanDisableReplyTags(t *testing.T) {
	client := &telegramClientStub{me: &telegramUser{ID: 99, Username: "koiosbot", IsBot: true}}
	falseValue := false
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:         true,
		BotToken:        "token",
		Mode:            "polling",
		PollTimeout:     25 * time.Second,
		ActivationMode:  "mention",
		ReplyActivation: true,
		GroupPolicies: []config.TelegramGroupPolicy{{
			ChatID:          123,
			ReplyActivation: &falseValue,
		}},
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	called := false
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		called = true
		return nil, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	replyOnly := telegramUpdate{Message: &telegramMessage{
		MessageID: 1,
		Text:      "hello",
		Chat:      telegramChat{ID: 123, Type: "group"},
		From:      &telegramUser{ID: 7, FirstName: "Pat"},
		ReplyToMessage: &telegramMessage{
			MessageID: 10,
			From:      &telegramUser{ID: 99, Username: "koiosbot", IsBot: true},
		},
	}}
	if err := ch.handleUpdate(context.Background(), replyOnly); err != nil {
		t.Fatalf("handleUpdate replyOnly: %v", err)
	}
	if called {
		t.Fatal("expected reply-only activation to be skipped when reply tags are disabled")
	}
	mentionedReply := replyOnly
	mentionedReply.Message = &telegramMessage{
		MessageID: 2,
		Text:      "@koiosbot hello",
		Chat:      telegramChat{ID: 123, Type: "group"},
		From:      &telegramUser{ID: 7, FirstName: "Pat"},
		ReplyToMessage: &telegramMessage{
			MessageID: 10,
			From:      &telegramUser{ID: 99, Username: "koiosbot", IsBot: true},
		},
	}
	if err := ch.handleUpdate(context.Background(), mentionedReply); err != nil {
		t.Fatalf("handleUpdate mentionedReply: %v", err)
	}
	if !called {
		t.Fatal("expected explicit mention to dispatch even when reply tags are disabled")
	}
}

func TestTelegramStartFailsOnGetMeError(t *testing.T) {
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:     true,
		BotToken:    "token",
		Mode:        "polling",
		PollTimeout: 25 * time.Second,
	}, nil, &telegramClientStub{getMeErr: errors.New("boom")}, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	if err := ch.Start(context.Background()); err == nil {
		t.Fatal("expected start error")
	}
}

func TestTelegramPairingRequiresApprovalBeforeDispatch(t *testing.T) {
	client := &telegramClientStub{}
	storePath := t.TempDir() + "/channel_bindings.json"
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "polling",
		PollTimeout:    25 * time.Second,
		DMPolicy:       "pairing",
		PairingCodeTTL: time.Hour,
	}, nil, client, &TelegramOptions{BindingStorePath: storePath})
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	called := false
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		called = true
		return &OutboundMessage{Text: "approved"}, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	update := telegramUpdate{Message: &telegramMessage{
		MessageID: 1,
		Text:      "hello",
		Chat:      telegramChat{ID: 123, Type: "private"},
		From:      &telegramUser{ID: 7, Username: "pat", FirstName: "Pat"},
	}}
	if err := ch.handleUpdate(context.Background(), update); err != nil {
		t.Fatalf("handleUpdate first: %v", err)
	}
	if called {
		t.Fatal("expected dispatch to be blocked pending pairing approval")
	}
	if len(client.sent) != 1 || !strings.Contains(client.sent[0].Text, "binding code") {
		t.Fatalf("expected binding instructions, got %#v", client.sent)
	}
	store := NewBindingStore(storePath)
	pending, err := store.ListPending("telegram")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	if _, err := store.ApproveCode(pending[0].Code, "test"); err != nil {
		t.Fatalf("ApproveCode: %v", err)
	}
	if err := ch.handleUpdate(context.Background(), update); err != nil {
		t.Fatalf("handleUpdate second: %v", err)
	}
	if !called {
		t.Fatal("expected dispatch after pairing approval")
	}
	if len(client.sent) != 2 || client.sent[1].Text != "approved" {
		t.Fatalf("unexpected post-approval send payloads: %#v", client.sent)
	}
}

func TestTelegramGroupPolicyControlsActivationTopicAndRouting(t *testing.T) {
	client := &telegramClientStub{me: &telegramUser{ID: 99, Username: "koiosbot", IsBot: true}}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "polling",
		PollTimeout:    25 * time.Second,
		ActivationMode: "mention",
		ThreadMode:     "topic",
		GroupPolicies: []config.TelegramGroupPolicy{{
			ChatID:           123,
			ActivationMode:   "always",
			AllowedSenderIDs: []int64{7},
			AllowedTopicIDs:  []int64{9},
			ThreadMode:       "chat",
		}},
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	peerIDs := []string{}
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		peerIDs = append(peerIDs, msg.PeerID)
		return nil, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	blocked := telegramUpdate{Message: &telegramMessage{
		MessageID:       1,
		MessageThreadID: 8,
		Text:            "plain group message",
		Chat:            telegramChat{ID: 123, Type: "group"},
		From:            &telegramUser{ID: 7, FirstName: "Pat"},
	}}
	if err := ch.handleUpdate(context.Background(), blocked); err != nil {
		t.Fatalf("handleUpdate blocked: %v", err)
	}
	if len(peerIDs) != 0 {
		t.Fatalf("expected blocked topic to skip dispatch, got %#v", peerIDs)
	}
	allowed := telegramUpdate{Message: &telegramMessage{
		MessageID:       2,
		MessageThreadID: 9,
		Text:            "plain group message",
		Chat:            telegramChat{ID: 123, Type: "group"},
		From:            &telegramUser{ID: 7, FirstName: "Pat"},
	}}
	if err := ch.handleUpdate(context.Background(), allowed); err != nil {
		t.Fatalf("handleUpdate allowed: %v", err)
	}
	if len(peerIDs) != 1 || peerIDs[0] != "telegram:123" {
		t.Fatalf("unexpected peer IDs: %#v", peerIDs)
	}
}

func TestTelegramSessionActivationOverrideAllowsUnmentionedGroupMessage(t *testing.T) {
	client := &telegramClientStub{me: &telegramUser{ID: 99, Username: "koiosbot", IsBot: true}}
	store := session.NewWithOptions(session.Options{MaxMessages: 10})
	if err := store.SetPolicy("default::channel::telegram:123", session.SessionPolicy{ActivationMode: "always"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "polling",
		PollTimeout:    25 * time.Second,
		ActivationMode: "mention",
		InboxPeerID:    "default",
	}, nil, client, &TelegramOptions{SessionPolicy: store.Policy})
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	called := false
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		called = true
		if msg.SessionKey != "default::channel::telegram:123" {
			t.Fatalf("session key = %q", msg.SessionKey)
		}
		return nil, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := ch.handleUpdate(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 1,
		Text:      "plain group message",
		Chat:      telegramChat{ID: 123, Type: "group"},
		From:      &telegramUser{ID: 7, FirstName: "Pat"},
	}}); err != nil {
		t.Fatalf("handleUpdate: %v", err)
	}
	if !called {
		t.Fatal("expected session activation override to allow dispatch")
	}
}

func TestTelegramPeerGroupActivationDefaultAllowsUnmentionedGroupMessage(t *testing.T) {
	client := &telegramClientStub{me: &telegramUser{ID: 99, Username: "koiosbot", IsBot: true}}
	store := session.NewWithOptions(session.Options{MaxMessages: 10})
	if err := store.SetPolicy("default", session.SessionPolicy{GroupActivationMode: "always"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "polling",
		PollTimeout:    25 * time.Second,
		ActivationMode: "mention",
		InboxPeerID:    "default",
	}, nil, client, &TelegramOptions{SessionPolicy: store.Policy})
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	called := false
	ch.dispatch = func(_ context.Context, _ InboundMessage) (*OutboundMessage, error) {
		called = true
		return nil, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := ch.handleUpdate(context.Background(), telegramUpdate{Message: &telegramMessage{
		MessageID: 1,
		Text:      "plain group message",
		Chat:      telegramChat{ID: 123, Type: "group"},
		From:      &telegramUser{ID: 7, FirstName: "Pat"},
	}}); err != nil {
		t.Fatalf("handleUpdate: %v", err)
	}
	if !called {
		t.Fatal("expected peer group activation default to allow dispatch")
	}
}

func TestTelegramGroupPolicyRestrictsCommandsWithoutBlockingNormalMessages(t *testing.T) {
	client := &telegramClientStub{me: &telegramUser{ID: 99, Username: "koiosbot", IsBot: true}}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "polling",
		PollTimeout:    25 * time.Second,
		ActivationMode: "always",
		GroupPolicies: []config.TelegramGroupPolicy{{
			ChatID:           123,
			CommandSenderIDs: []int64{7},
		}},
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	texts := []string{}
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		texts = append(texts, msg.Text)
		return nil, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	blockedCommand := telegramUpdate{Message: &telegramMessage{
		MessageID: 1,
		Text:      "/status",
		Chat:      telegramChat{ID: 123, Type: "group"},
		From:      &telegramUser{ID: 8, FirstName: "Sam"},
	}}
	if err := ch.handleUpdate(context.Background(), blockedCommand); err != nil {
		t.Fatalf("handleUpdate blockedCommand: %v", err)
	}
	if len(texts) != 0 {
		t.Fatalf("expected command from non-operator to be skipped, got %#v", texts)
	}
	normalMessage := telegramUpdate{Message: &telegramMessage{
		MessageID: 2,
		Text:      "hello there",
		Chat:      telegramChat{ID: 123, Type: "group"},
		From:      &telegramUser{ID: 8, FirstName: "Sam"},
	}}
	if err := ch.handleUpdate(context.Background(), normalMessage); err != nil {
		t.Fatalf("handleUpdate normalMessage: %v", err)
	}
	allowedCommand := telegramUpdate{Message: &telegramMessage{
		MessageID: 3,
		Text:      "/status",
		Chat:      telegramChat{ID: 123, Type: "group"},
		From:      &telegramUser{ID: 7, FirstName: "Pat"},
	}}
	if err := ch.handleUpdate(context.Background(), allowedCommand); err != nil {
		t.Fatalf("handleUpdate allowedCommand: %v", err)
	}
	if len(texts) != 2 || texts[0] != "hello there" || texts[1] != "/status" {
		t.Fatalf("unexpected dispatched texts: %#v", texts)
	}
}

func TestTelegramInboxPeerRoutingUsesOwnerScopedSession(t *testing.T) {
	client := &telegramClientStub{me: &telegramUser{ID: 99, Username: "koiosbot", IsBot: true}}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:     true,
		BotToken:    "token",
		Mode:        "polling",
		PollTimeout: 25 * time.Second,
		InboxPeerID: "default",
		ThreadMode:  "topic",
		DMPolicy:    "open",
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	var received InboundMessage
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		received = msg
		return nil, nil
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	update := telegramUpdate{Message: &telegramMessage{
		MessageID:       1,
		MessageThreadID: 9,
		Text:            "hello",
		Chat:            telegramChat{ID: 123, Type: "group"},
		From:            &telegramUser{ID: 7, FirstName: "Pat"},
	}}
	if err := ch.handleUpdate(context.Background(), update); err != nil {
		t.Fatalf("handleUpdate: %v", err)
	}
	if received.PeerID != "default" {
		t.Fatalf("peer id = %q, want default", received.PeerID)
	}
	if received.SessionKey != "default::channel::telegram:123:9" {
		t.Fatalf("session key = %q", received.SessionKey)
	}
	if got := received.Metadata["conversation_peer_id"]; got != "telegram:123:9" {
		t.Fatalf("conversation_peer_id = %#v", got)
	}
	if err := ch.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

func TestTelegramApprovedBindingRouteOverridesInboxPeer(t *testing.T) {
	client := &telegramClientStub{}
	storePath := t.TempDir() + "/channel_bindings.json"
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "polling",
		PollTimeout:    25 * time.Second,
		DMPolicy:       "pairing",
		PairingCodeTTL: time.Hour,
		InboxPeerID:    "default",
	}, nil, client, &TelegramOptions{BindingStorePath: storePath})
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	update := telegramUpdate{Message: &telegramMessage{
		MessageID: 1,
		Text:      "hello",
		Chat:      telegramChat{ID: 123, Type: "private"},
		From:      &telegramUser{ID: 7, Username: "pat", FirstName: "Pat"},
	}}
	if err := ch.handleUpdate(context.Background(), update); err != nil {
		t.Fatalf("handleUpdate first: %v", err)
	}
	store := NewBindingStore(storePath)
	pending, err := store.ListPending("telegram")
	if err != nil {
		t.Fatalf("ListPending: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending count = %d, want 1", len(pending))
	}
	if _, err := store.ApproveCodeWithRoute(pending[0].Code, "test", BindingRoute{PeerID: "owner"}); err != nil {
		t.Fatalf("ApproveCodeWithRoute: %v", err)
	}
	var received InboundMessage
	ch.dispatch = func(_ context.Context, msg InboundMessage) (*OutboundMessage, error) {
		received = msg
		return nil, nil
	}
	if err := ch.handleUpdate(context.Background(), update); err != nil {
		t.Fatalf("handleUpdate second: %v", err)
	}
	if received.PeerID != "owner" {
		t.Fatalf("peer id = %q, want owner", received.PeerID)
	}
	if received.SessionKey != "owner::channel::telegram:123" {
		t.Fatalf("session key = %q", received.SessionKey)
	}
}

func TestTelegramSendMessageUsesApprovedBinding(t *testing.T) {
	client := &telegramClientStub{sendResults: []*telegramMessage{{MessageID: 501, Chat: telegramChat{ID: 123, Type: "private"}}}}
	storePath := filepath.Join(t.TempDir(), "channel_bindings.json")
	store := NewBindingStore(storePath)
	pending, err := store.EnsurePending(BindingRequest{
		Channel:        "telegram",
		SubjectID:      "7",
		ConversationID: "123",
		TTL:            time.Hour,
	})
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	if _, err := store.ApproveCode(pending.Code, "test"); err != nil {
		t.Fatalf("ApproveCode: %v", err)
	}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "polling",
		PollTimeout:    25 * time.Second,
		TextChunkLimit: 4096,
		DMPolicy:       "pairing",
		PairingCodeTTL: time.Hour,
	}, nil, client, &TelegramOptions{BindingStorePath: storePath})
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	receipt, err := ch.SendMessage(context.Background(), OutboundTarget{SubjectID: "7"}, OutboundMessage{Text: "hello there"})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if receipt == nil || !receipt.UsedBinding || receipt.ConversationID != "123" || receipt.SubjectID != "7" {
		t.Fatalf("unexpected receipt: %#v", receipt)
	}
	if len(receipt.Deliveries) != 1 || receipt.Deliveries[0].MessageID != "501" || receipt.Deliveries[0].ConversationID != "123" {
		t.Fatalf("unexpected delivery receipts: %#v", receipt.Deliveries)
	}
	if len(client.sent) != 1 {
		t.Fatalf("sent count = %d, want 1", len(client.sent))
	}
	if client.sent[0].ChatID != 123 || client.sent[0].Text != "hello there" {
		t.Fatalf("unexpected send request: %#v", client.sent[0])
	}
}

func TestTelegramSendMessageReturnsPerChunkDeliveries(t *testing.T) {
	client := &telegramClientStub{sendResults: []*telegramMessage{
		{MessageID: 601, Chat: telegramChat{ID: 123, Type: "private"}},
		{MessageID: 602, Chat: telegramChat{ID: 123, Type: "private"}},
	}}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:        true,
		BotToken:       "token",
		Mode:           "polling",
		PollTimeout:    25 * time.Second,
		TextChunkLimit: 5,
		TextChunkMode:  "hard",
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	receipt, err := ch.SendMessage(context.Background(), OutboundTarget{ConversationID: "123"}, OutboundMessage{Text: "abcdefgh"})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if receipt.ChunkCount != 2 {
		t.Fatalf("chunk count = %d, want 2", receipt.ChunkCount)
	}
	if len(receipt.Deliveries) != 2 {
		t.Fatalf("delivery count = %d, want 2", len(receipt.Deliveries))
	}
	if receipt.Deliveries[0].MessageID != "601" || receipt.Deliveries[0].ChunkIndex != 1 {
		t.Fatalf("unexpected first delivery: %#v", receipt.Deliveries[0])
	}
	if receipt.Deliveries[1].MessageID != "602" || receipt.Deliveries[1].ChunkIndex != 2 {
		t.Fatalf("unexpected second delivery: %#v", receipt.Deliveries[1])
	}
	if receipt.Metadata["stream_queue_mode"] != "burst" {
		t.Fatalf("unexpected receipt metadata: %#v", receipt.Metadata)
	}
}

func TestTelegramSendMessageThrottlesChunkQueue(t *testing.T) {
	client := &telegramClientStub{}
	ch, err := NewTelegramChannel(config.TelegramChannelConfig{
		Enabled:         true,
		BotToken:        "token",
		Mode:            "polling",
		PollTimeout:     25 * time.Second,
		TextChunkLimit:  5,
		TextChunkMode:   "hard",
		StreamQueueMode: "throttle",
		StreamThrottle:  20 * time.Millisecond,
	}, nil, client, nil)
	if err != nil {
		t.Fatalf("NewTelegramChannel: %v", err)
	}
	started := time.Now()
	if _, err := ch.SendMessage(context.Background(), OutboundTarget{ConversationID: "123"}, OutboundMessage{Text: "abcdefgh"}); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if len(client.sent) != 2 {
		t.Fatalf("sent count = %d, want 2", len(client.sent))
	}
	if elapsed := time.Since(started); elapsed < 15*time.Millisecond {
		t.Fatalf("expected throttled chunk queue, got elapsed %s", elapsed)
	}
}
