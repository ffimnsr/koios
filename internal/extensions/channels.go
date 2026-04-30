package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/channels"
	"github.com/ffimnsr/koios/internal/mcp"
)

type ChannelToolCaller interface {
	CallTool(ctx context.Context, fullName string, rawArgs json.RawMessage) (any, error)
}

type ChannelMessageDispatcher interface {
	Dispatch(ctx context.Context, msg channels.InboundMessage) (*channels.OutboundMessage, error)
}

type DiscoveredChannelBinding struct {
	Manifest DiscoveredManifest
	Binding  ChannelBinding
}

type channelToolEnvelope struct {
	HTTPResponse *HTTPRouteResponse       `json:"http_response,omitempty"`
	Message      *channelInboundEnvelope  `json:"message,omitempty"`
	Messages     []channelInboundEnvelope `json:"messages,omitempty"`
	NoReply      bool                     `json:"no_reply,omitempty"`
	PollState    json.RawMessage          `json:"poll_state,omitempty"`
}

type channelInboundEnvelope struct {
	PeerID           string         `json:"peer_id,omitempty"`
	SessionKey       string         `json:"session_key,omitempty"`
	SenderID         string         `json:"sender_id,omitempty"`
	SubjectID        string         `json:"subject_id,omitempty"`
	ConversationID   string         `json:"conversation_id,omitempty"`
	Text             string         `json:"text,omitempty"`
	DisplayName      string         `json:"display_name,omitempty"`
	ReplyToMessageID string         `json:"reply_to_message_id,omitempty"`
	ThreadID         string         `json:"thread_id,omitempty"`
	NoReply          bool           `json:"no_reply,omitempty"`
	Metadata         map[string]any `json:"metadata,omitempty"`
}

func ChannelBindings(manifests []DiscoveredManifest) ([]DiscoveredChannelBinding, error) {
	bindings := make([]DiscoveredChannelBinding, 0)
	for _, manifest := range manifests {
		if !manifest.Enabled() || !manifest.HasCapability(CapabilityChannels) {
			continue
		}
		for _, binding := range manifest.Manifest.Channels {
			if binding.Enabled != nil && !*binding.Enabled {
				continue
			}
			if err := validateChannelBinding(binding, manifest.Path); err != nil {
				return nil, err
			}
			bindings = append(bindings, DiscoveredChannelBinding{Manifest: manifest, Binding: binding})
		}
	}
	return bindings, nil
}

func RegisterChannels(manager *channels.Manager, caller ChannelToolCaller, manifests []DiscoveredManifest, dispatcher *channels.Dispatcher) error {
	return registerChannels(manager, caller, manifests, dispatcher)
}

func registerChannels(manager *channels.Manager, caller ChannelToolCaller, manifests []DiscoveredManifest, dispatcher ChannelMessageDispatcher) error {
	channelBindings, err := ChannelBindings(manifests)
	if err != nil {
		return err
	}
	outboundBindings, err := OutboundMessageBindings(manifests)
	if err != nil {
		return err
	}
	if len(channelBindings) == 0 && len(outboundBindings) == 0 {
		return nil
	}
	if manager == nil {
		return fmt.Errorf("extension channels require a channel manager")
	}
	if caller == nil {
		return fmt.Errorf("extension channels require an MCP tool caller")
	}
	if len(channelBindings) > 0 && dispatcher == nil {
		return fmt.Errorf("extension channels require a dispatcher")
	}

	channelByID := make(map[string]DiscoveredChannelBinding, len(channelBindings))
	for _, discovered := range channelBindings {
		channelID := strings.TrimSpace(discovered.Binding.Channel)
		if owner, exists := channelByID[channelID]; exists {
			return fmt.Errorf("extension channel %q is declared by both %q and %q", channelID, owner.Manifest.Manifest.ID, discovered.Manifest.Manifest.ID)
		}
		if manager.HasChannel(channelID) {
			return fmt.Errorf("extension channel %q conflicts with an existing channel registration", channelID)
		}
		channelByID[channelID] = discovered
	}

	outboundByID := make(map[string]DiscoveredOutboundMessageBinding, len(outboundBindings))
	for _, discovered := range outboundBindings {
		channelID := strings.TrimSpace(discovered.Binding.Channel)
		if owner, exists := outboundByID[channelID]; exists {
			return fmt.Errorf("extension outbound channel %q is declared by both %q and %q", channelID, owner.Manifest.Manifest.ID, discovered.Manifest.Manifest.ID)
		}
		if _, claimedByChannel := channelByID[channelID]; !claimedByChannel && manager.HasChannel(channelID) {
			return fmt.Errorf("extension outbound channel %q conflicts with an existing channel registration", channelID)
		}
		outboundByID[channelID] = discovered
	}

	for channelID, discovered := range channelByID {
		outbound, hasOutbound := outboundByID[channelID]
		managed := &extensionChannel{
			binding:    discovered,
			caller:     caller,
			dispatcher: dispatcher,
		}
		if hasOutbound {
			managed.outbound = &outbound
			delete(outboundByID, channelID)
		}
		manager.Register(managed)
	}

	for _, discovered := range outboundByID {
		manager.Register(&extensionOutboundSender{binding: discovered, caller: caller})
	}
	return nil
}

type extensionChannel struct {
	binding    DiscoveredChannelBinding
	outbound   *DiscoveredOutboundMessageBinding
	caller     ChannelToolCaller
	dispatcher ChannelMessageDispatcher

	mu        sync.Mutex
	pollState json.RawMessage
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

func (c *extensionChannel) ID() string { return c.binding.Binding.Channel }

func (c *extensionChannel) Routes() []channels.Route {
	if c == nil || strings.TrimSpace(c.binding.Binding.Tool) == "" {
		return nil
	}
	return []channels.Route{{
		Pattern: strings.TrimSpace(c.binding.Binding.Method) + " " + strings.TrimSpace(c.binding.Binding.Path),
		Handler: http.HandlerFunc(c.serveHTTP),
	}}
}

func (c *extensionChannel) Start(ctx context.Context) error {
	if c == nil {
		return nil
	}
	toolName := strings.TrimSpace(c.binding.Binding.StartTool)
	if toolName == "" {
		if strings.TrimSpace(c.binding.Binding.PollTool) == "" {
			return nil
		}
	} else {
		_, err := c.caller.CallTool(ctx, c.fullToolName(toolName), c.lifecyclePayload("start"))
		if err != nil {
			return fmt.Errorf("start extension channel %q: %w", c.ID(), err)
		}
	}
	pollTool := strings.TrimSpace(c.binding.Binding.PollTool)
	if pollTool != "" {
		pollCtx, cancel := context.WithCancel(context.Background())
		c.mu.Lock()
		c.cancel = cancel
		c.mu.Unlock()
		c.wg.Add(1)
		go c.pollLoop(pollCtx)
	}
	return nil
}

func (c *extensionChannel) Shutdown(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.wg.Wait()
	}()
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	toolName := strings.TrimSpace(c.binding.Binding.ShutdownTool)
	if toolName == "" {
		return nil
	}
	_, err := c.caller.CallTool(ctx, c.fullToolName(toolName), c.lifecyclePayload("shutdown"))
	if err != nil {
		return fmt.Errorf("shutdown extension channel %q: %w", c.ID(), err)
	}
	return nil
}

func (c *extensionChannel) SendMessage(ctx context.Context, target channels.OutboundTarget, msg channels.OutboundMessage) (*channels.OutboundReceipt, error) {
	if c == nil || c.outbound == nil {
		return nil, fmt.Errorf("channel %q does not support outbound messaging", c.ID())
	}
	result, err := c.caller.CallTool(ctx, c.outbound.fullToolName(), c.outbound.payload(target, msg))
	if err != nil {
		return nil, err
	}
	return decodeOutboundMessageToolResult(result, &channels.OutboundReceipt{
		Channel:        c.ID(),
		SubjectID:      strings.TrimSpace(target.SubjectID),
		ConversationID: strings.TrimSpace(target.ConversationID),
		ThreadID:       strings.TrimSpace(target.ThreadID),
		ChunkCount:     1,
	})
}

func (c *extensionChannel) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.caller == nil {
		http.NotFound(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	result, err := c.caller.CallTool(r.Context(), c.fullToolName(c.binding.Binding.Tool), c.requestPayload(r, body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	envelope, response, plainText, err := decodeChannelToolEnvelope(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if err := c.dispatchMessages(r.Context(), envelope); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if response != nil {
		writeHTTPRouteResponse(w, r, *response)
		return
	}
	if plainText != "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = io.WriteString(w, plainText)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (c *extensionChannel) dispatchMessages(ctx context.Context, envelope channelToolEnvelope) error {
	if c == nil || c.dispatcher == nil {
		return fmt.Errorf("extension channel %q has no dispatcher", c.ID())
	}
	items := make([]channelInboundEnvelope, 0, len(envelope.Messages)+1)
	if envelope.Message != nil {
		items = append(items, *envelope.Message)
	}
	items = append(items, envelope.Messages...)
	for _, item := range items {
		message := c.normalizeInbound(item)
		if strings.TrimSpace(message.Text) == "" {
			continue
		}
		suppressReply := envelope.NoReply || item.NoReply
		reply, err := c.dispatcher.Dispatch(ctx, message)
		if err != nil {
			return err
		}
		if suppressReply {
			continue
		}
		if reply == nil || strings.TrimSpace(reply.Text) == "" {
			continue
		}
		if c.outbound == nil {
			return fmt.Errorf("extension channel %q produced a reply but has no outbound sender", c.ID())
		}
		_, err = c.SendMessage(ctx, channels.OutboundTarget{
			SubjectID:      strings.TrimSpace(item.SubjectID),
			ConversationID: strings.TrimSpace(item.ConversationID),
			ThreadID:       strings.TrimSpace(item.ThreadID),
		}, channels.OutboundMessage{
			Text:             reply.Text,
			ReplyToMessageID: firstNonEmpty(reply.ReplyToMessageID, strings.TrimSpace(item.ReplyToMessageID)),
			ThreadID:         firstNonEmpty(reply.ThreadID, strings.TrimSpace(item.ThreadID)),
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *extensionChannel) pollLoop(ctx context.Context) {
	defer c.wg.Done()
	interval := c.pollInterval()
	for {
		if err := c.pollOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("extension channel poll failed", "channel", c.ID(), "extension", c.binding.Manifest.Manifest.ID, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func (c *extensionChannel) pollOnce(ctx context.Context) error {
	result, err := c.caller.CallTool(ctx, c.fullToolName(c.binding.Binding.PollTool), c.pollPayload())
	if err != nil {
		return err
	}
	envelope, _, _, err := decodeChannelToolEnvelope(result)
	if err != nil {
		return err
	}
	c.setPollState(envelope.PollState)
	return c.dispatchMessages(ctx, envelope)
}

func (c *extensionChannel) pollPayload() json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"channel": map[string]any{
			"binding": map[string]any{
				"name":          c.binding.Binding.Name,
				"channel":       c.binding.Binding.Channel,
				"method":        c.binding.Binding.Method,
				"path":          c.binding.Binding.Path,
				"tool":          c.binding.Binding.Tool,
				"poll_tool":     c.binding.Binding.PollTool,
				"poll_interval": c.binding.Binding.PollInterval,
				"start_tool":    c.binding.Binding.StartTool,
				"shutdown_tool": c.binding.Binding.ShutdownTool,
			},
			"extension_id":   c.binding.Manifest.Manifest.ID,
			"extension_name": c.binding.Manifest.Manifest.Name,
			"poll": map[string]any{
				"state": c.pollStateValue(),
			},
		},
	})
	return payload
}

func (c *extensionChannel) normalizeInbound(item channelInboundEnvelope) channels.InboundMessage {
	conversationPeerID := extensionConversationPeerID(c.ID(), strings.TrimSpace(item.ConversationID), strings.TrimSpace(item.ThreadID), strings.TrimSpace(item.SubjectID))
	peerID := strings.TrimSpace(item.PeerID)
	if peerID == "" {
		if owner := channels.SessionKeyOwnerPeer(strings.TrimSpace(item.SessionKey)); owner != "" {
			peerID = owner
		} else if conversationPeerID != "" {
			peerID = conversationPeerID
		}
	}
	sessionKey := strings.TrimSpace(item.SessionKey)
	if sessionKey == "" && peerID != "" && conversationPeerID != "" && peerID != conversationPeerID {
		sessionKey = channels.ChannelInboxSessionKey(peerID, conversationPeerID)
	}
	metadata := cloneAnyMap(item.Metadata)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	if conversationPeerID != "" {
		metadata["conversation_peer_id"] = conversationPeerID
	}
	if strings.TrimSpace(item.ConversationID) != "" {
		metadata["conversation_id"] = strings.TrimSpace(item.ConversationID)
	}
	if strings.TrimSpace(item.SubjectID) != "" {
		metadata["subject_id"] = strings.TrimSpace(item.SubjectID)
	}
	return channels.InboundMessage{
		Channel:          c.ID(),
		PeerID:           peerID,
		SessionKey:       sessionKey,
		SenderID:         strings.TrimSpace(item.SenderID),
		Text:             strings.TrimSpace(item.Text),
		DisplayName:      strings.TrimSpace(item.DisplayName),
		ReplyToMessageID: strings.TrimSpace(item.ReplyToMessageID),
		ThreadID:         strings.TrimSpace(item.ThreadID),
		Metadata:         metadata,
	}
}

func (c *extensionChannel) fullToolName(tool string) string {
	return mcp.PluginToolPrefix(c.binding.Manifest.Manifest.ID) + strings.TrimSpace(tool)
}

func (c *extensionChannel) requestPayload(r *http.Request, body []byte) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"channel": map[string]any{
			"binding": map[string]any{
				"name":          c.binding.Binding.Name,
				"channel":       c.binding.Binding.Channel,
				"method":        c.binding.Binding.Method,
				"path":          c.binding.Binding.Path,
				"tool":          c.binding.Binding.Tool,
				"poll_tool":     c.binding.Binding.PollTool,
				"poll_interval": c.binding.Binding.PollInterval,
				"start_tool":    c.binding.Binding.StartTool,
				"shutdown_tool": c.binding.Binding.ShutdownTool,
			},
			"extension_id":   c.binding.Manifest.Manifest.ID,
			"extension_name": c.binding.Manifest.Manifest.Name,
			"request": map[string]any{
				"method":       r.Method,
				"path":         c.binding.Binding.Path,
				"raw_path":     r.URL.Path,
				"query":        r.URL.Query(),
				"headers":      r.Header,
				"body":         string(body),
				"content_type": r.Header.Get("Content-Type"),
				"remote_addr":  r.RemoteAddr,
			},
		},
	})
	return payload
}

func (c *extensionChannel) pollInterval() time.Duration {
	if raw := strings.TrimSpace(c.binding.Binding.PollInterval); raw != "" {
		if interval, err := time.ParseDuration(raw); err == nil && interval > 0 {
			return interval
		}
	}
	return 30 * time.Second
}

func (c *extensionChannel) getPollState() json.RawMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pollState) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), c.pollState...)
}

func (c *extensionChannel) pollStateValue() any {
	state := c.getPollState()
	if len(state) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(state, &value); err != nil {
		return nil
	}
	return value
}

func (c *extensionChannel) setPollState(state json.RawMessage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(state) == 0 {
		c.pollState = nil
		return
	}
	c.pollState = append(json.RawMessage(nil), state...)
}

func (c *extensionChannel) lifecyclePayload(phase string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"channel": map[string]any{
			"binding": map[string]any{
				"name":    c.binding.Binding.Name,
				"channel": c.binding.Binding.Channel,
				"method":  c.binding.Binding.Method,
				"path":    c.binding.Binding.Path,
			},
			"extension_id":   c.binding.Manifest.Manifest.ID,
			"extension_name": c.binding.Manifest.Manifest.Name,
			"lifecycle": map[string]any{
				"phase": phase,
			},
		},
	})
	return payload
}

func decodeChannelToolEnvelope(result any) (channelToolEnvelope, *HTTPRouteResponse, string, error) {
	data, err := decodeHookToolResult(result)
	if err != nil {
		if result == nil {
			return channelToolEnvelope{}, nil, "", nil
		}
		if value, ok := result.(map[string]any); ok {
			encoded, marshalErr := json.Marshal(value)
			if marshalErr != nil {
				return channelToolEnvelope{}, nil, "", marshalErr
			}
			data = encoded
		} else {
			return channelToolEnvelope{}, nil, "", err
		}
	}
	if len(data) == 0 {
		return channelToolEnvelope{}, nil, "", nil
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return channelToolEnvelope{}, nil, "", nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return channelToolEnvelope{}, nil, trimmed, nil
	}
	var envelope channelToolEnvelope
	if err := json.Unmarshal(data, &envelope); err == nil {
		if envelope.HTTPResponse != nil || envelope.Message != nil || len(envelope.Messages) > 0 || envelope.NoReply || len(envelope.PollState) > 0 {
			return envelope, envelope.HTTPResponse, "", nil
		}
	}
	response, ok, err := decodeHTTPRouteResponseText(trimmed)
	if err != nil {
		return channelToolEnvelope{}, nil, "", err
	}
	if ok {
		return channelToolEnvelope{}, &response, "", nil
	}
	return channelToolEnvelope{}, nil, trimmed, nil
}

func decodeHTTPRouteResponseText(raw string) (HTTPRouteResponse, bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return HTTPRouteResponse{Status: http.StatusNoContent}, true, nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return HTTPRouteResponse{}, false, nil
	}
	var response HTTPRouteResponse
	if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
		return HTTPRouteResponse{}, false, nil
	}
	if response.Status < 0 || response.Status > 999 {
		return HTTPRouteResponse{}, false, fmt.Errorf("invalid extension route status %d", response.Status)
	}
	return response, true, nil
}

func writeHTTPRouteResponse(w http.ResponseWriter, r *http.Request, response HTTPRouteResponse) {
	for key, values := range response.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if response.ContentType != "" && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", response.ContentType)
	}
	status := response.Status
	if status == 0 {
		status = http.StatusOK
	}
	if response.JSON != nil {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response.JSON)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, response.Body)
	}
}

func extensionConversationPeerID(channelID, conversationID, threadID, subjectID string) string {
	base := strings.TrimSpace(conversationID)
	if base == "" {
		base = strings.TrimSpace(subjectID)
	}
	if base == "" {
		return ""
	}
	if trimmedThread := strings.TrimSpace(threadID); trimmedThread != "" {
		return channelID + ":" + base + ":" + trimmedThread
	}
	return channelID + ":" + base
}

func cloneAnyMap(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
