package handler

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/channels"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

type inboxEntry struct {
	SessionKey          string   `json:"session_key"`
	Channel             string   `json:"channel,omitempty"`
	ConversationPeerID  string   `json:"conversation_peer_id,omitempty"`
	ConversationID      string   `json:"conversation_id,omitempty"`
	ThreadID            string   `json:"thread_id,omitempty"`
	SubjectID           string   `json:"subject_id,omitempty"`
	Username            string   `json:"username,omitempty"`
	DisplayName         string   `json:"display_name,omitempty"`
	MessageCount        int      `json:"message_count"`
	UserMessageCount    int      `json:"user_message_count"`
	AssistantCount      int      `json:"assistant_message_count"`
	UnreadCount         int      `json:"unread_count"`
	LastActivity        string   `json:"last_activity,omitempty"`
	LastMessagePreview  string   `json:"last_message_preview,omitempty"`
	RecentUserPreviews  []string `json:"recent_user_previews,omitempty"`
	RoutedPeerID        string   `json:"routed_peer_id,omitempty"`
	RoutedSessionKey    string   `json:"routed_session_key,omitempty"`
	ApprovedBindingCode string   `json:"approved_binding_code,omitempty"`
	ReadUserCount       int      `json:"read_user_count"`
}

func (h *Handler) runInboxListTool(peerID, channel string, limit int, unreadOnly bool) (map[string]any, error) {
	items, err := h.listInboxEntries(peerID, strings.TrimSpace(channel))
	if err != nil {
		return nil, err
	}
	filtered := make([]inboxEntry, 0, len(items))
	for _, item := range items {
		if unreadOnly && item.UnreadCount == 0 {
			continue
		}
		filtered = append(filtered, item)
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	unreadSessions := 0
	unreadMessages := 0
	for _, item := range filtered {
		if item.UnreadCount > 0 {
			unreadSessions++
		}
		unreadMessages += item.UnreadCount
	}
	return map[string]any{
		"peer_id":         peerID,
		"channel":         strings.TrimSpace(channel),
		"count":           len(filtered),
		"unread_sessions": unreadSessions,
		"unread_messages": unreadMessages,
		"conversations":   filtered,
	}, nil
}

func (h *Handler) runInboxReadTool(peerID, sessionKey string, limit int, unreadOnly bool) (map[string]any, error) {
	resolved, err := h.resolveInboxSessionKey(peerID, sessionKey)
	if err != nil {
		return nil, err
	}
	history := h.store.Get(resolved).History()
	readUserCount := h.store.Policy(resolved).InboxReadUserCount
	stats := inboxHistoryStats(history)
	unreadMessages := unreadInboxMessages(history, readUserCount)
	returned := history
	if unreadOnly {
		returned = unreadMessages
	}
	if limit > 0 && len(returned) > limit {
		returned = returned[len(returned)-limit:]
	}
	return map[string]any{
		"peer_id":            peerID,
		"session_key":        resolved,
		"message_count":      len(history),
		"user_message_count": stats.userCount,
		"assistant_count":    stats.assistantCount,
		"read_user_count":    readUserCount,
		"unread_count":       maxInt(0, stats.userCount-readUserCount),
		"messages":           returned,
		"unread_messages":    unreadMessages,
	}, nil
}

func (h *Handler) runInboxMarkReadTool(peerID, sessionKey string) (map[string]any, error) {
	resolved, err := h.resolveInboxSessionKey(peerID, sessionKey)
	if err != nil {
		return nil, err
	}
	stats := inboxHistoryStats(h.store.Get(resolved).History())
	if err := h.store.PatchPolicy(resolved, func(policy *session.SessionPolicy) {
		policy.InboxReadUserCount = stats.userCount
	}); err != nil {
		return nil, fmt.Errorf("persist inbox read marker: %w", err)
	}
	return map[string]any{
		"ok":               true,
		"peer_id":          peerID,
		"session_key":      resolved,
		"read_user_count":  stats.userCount,
		"remaining_unread": 0,
	}, nil
}

func (h *Handler) runInboxRouteTool(peerID, channel, subjectID, routePeerID, sessionKey string) (map[string]any, error) {
	if h.channelBindingStore == nil {
		return nil, fmt.Errorf("channel bindings are not enabled")
	}
	channel = strings.TrimSpace(channel)
	subjectID = strings.TrimSpace(subjectID)
	if channel == "" || subjectID == "" {
		return nil, fmt.Errorf("channel and subject_id are required")
	}
	routePeerID = strings.TrimSpace(routePeerID)
	sessionKey = strings.TrimSpace(sessionKey)
	if routePeerID == "" && sessionKey == "" {
		return nil, fmt.Errorf("peer_id or session_key is required")
	}
	if routePeerID != "" && routePeerID != peerID {
		return nil, fmt.Errorf("peer_id %q is not accessible to peer %q", routePeerID, peerID)
	}
	if sessionKey != "" {
		owner := channels.SessionKeyOwnerPeer(sessionKey)
		if owner == "" || owner != peerID {
			return nil, fmt.Errorf("session_key %q is not accessible to peer %q", sessionKey, peerID)
		}
		if routePeerID != "" && routePeerID != owner {
			return nil, fmt.Errorf("peer_id %q does not match session_key owner %q", routePeerID, owner)
		}
		if routePeerID == "" {
			routePeerID = owner
		}
	}
	updated, err := h.channelBindingStore.UpdateApprovedRoute(channel, subjectID, channels.BindingRoute{
		PeerID:     routePeerID,
		SessionKey: sessionKey,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":              true,
		"channel":         updated.Channel,
		"subject_id":      updated.SubjectID,
		"conversation_id": updated.ConversationID,
		"peer_id":         updated.PeerID,
		"session_key":     updated.SessionKey,
		"binding":         updated,
	}, nil
}

func (h *Handler) runInboxSummarizeTool(peerID, sessionKey string, limit int) (map[string]any, error) {
	resolved, err := h.resolveInboxSessionKey(peerID, sessionKey)
	if err != nil {
		return nil, err
	}
	history := h.store.Get(resolved).History()
	stats := inboxHistoryStats(history)
	entry, _ := h.inboxEntryForSession(peerID, resolved, nil)
	previews := collectRecentUserPreviews(history, limit)
	summaryText := buildInboxSummaryText(entry, previews)
	return map[string]any{
		"peer_id":              peerID,
		"session_key":          resolved,
		"message_count":        len(history),
		"user_message_count":   stats.userCount,
		"assistant_count":      stats.assistantCount,
		"read_user_count":      entry.ReadUserCount,
		"unread_count":         entry.UnreadCount,
		"summary":              summaryText,
		"recent_user_previews": previews,
	}, nil
}

func (h *Handler) listInboxEntries(peerID, channel string) ([]inboxEntry, error) {
	keys := h.store.SessionKeys(peerID)
	approvedBySession := map[string]channels.ApprovedBinding{}
	approvedByConversation := map[string]channels.ApprovedBinding{}
	if h.channelBindingStore != nil {
		approved, err := h.channelBindingStore.ListApproved(channel)
		if err != nil {
			return nil, err
		}
		for _, item := range approved {
			if bindingVisibleToPeer(peerID, item) {
				if key := strings.TrimSpace(item.SessionKey); key != "" {
					approvedBySession[key] = item
				}
				conversationKey := approvedConversationLookupKey(item.Channel, item.ConversationID)
				if conversationKey != "" {
					approvedByConversation[conversationKey] = item
				}
			}
		}
	}
	entries := make([]inboxEntry, 0, len(keys))
	for _, key := range keys {
		if !isInboxSessionKey(peerID, key) {
			continue
		}
		approved := (*channels.ApprovedBinding)(nil)
		if item, ok := approvedBySession[key]; ok {
			approved = &item
		} else {
			conversationPeerID := inboxConversationPeerID(peerID, key)
			parsedChannel, conversationID, _ := parseConversationPeerID(conversationPeerID)
			if item, ok := approvedByConversation[approvedConversationLookupKey(parsedChannel, conversationID)]; ok {
				approved = &item
			}
		}
		entry, ok := h.inboxEntryForSession(peerID, key, approved)
		if !ok {
			continue
		}
		if channel != "" && !strings.EqualFold(entry.Channel, channel) {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		left, right := entries[i].LastActivity, entries[j].LastActivity
		if left == right {
			return entries[i].SessionKey < entries[j].SessionKey
		}
		return left > right
	})
	return entries, nil
}

func (h *Handler) inboxEntryForSession(peerID, sessionKey string, approved *channels.ApprovedBinding) (inboxEntry, bool) {
	sess := h.store.Get(sessionKey)
	history := sess.History()
	stats := inboxHistoryStats(history)
	policy := h.store.Policy(sessionKey)
	conversationPeerID := inboxConversationPeerID(peerID, sessionKey)
	channel, conversationID, threadID := parseConversationPeerID(conversationPeerID)
	lastActivity := sess.LastActivity().UTC()
	entry := inboxEntry{
		SessionKey:         sessionKey,
		Channel:            channel,
		ConversationPeerID: conversationPeerID,
		ConversationID:     conversationID,
		ThreadID:           threadID,
		MessageCount:       len(history),
		UserMessageCount:   stats.userCount,
		AssistantCount:     stats.assistantCount,
		ReadUserCount:      policy.InboxReadUserCount,
		UnreadCount:        maxInt(0, stats.userCount-policy.InboxReadUserCount),
		LastMessagePreview: previewMessageContent(lastNonEmptyContent(history)),
		RecentUserPreviews: collectRecentUserPreviews(history, 3),
	}
	if !lastActivity.IsZero() {
		entry.LastActivity = lastActivity.Format(time.RFC3339)
	}
	if approved != nil {
		entry.SubjectID = approved.SubjectID
		entry.Username = approved.Username
		entry.DisplayName = approved.DisplayName
		entry.RoutedPeerID = approved.PeerID
		entry.RoutedSessionKey = approved.SessionKey
		entry.ApprovedBindingCode = approved.Code
	}
	return entry, true
}

func (h *Handler) resolveInboxSessionKey(peerID, sessionKey string) (string, error) {
	resolved := strings.TrimSpace(sessionKey)
	if resolved == "" {
		return "", fmt.Errorf("session_key is required")
	}
	if resolved != peerID && !strings.HasPrefix(resolved, peerID+"::") {
		return "", fmt.Errorf("session_key %q is not accessible to peer %q", resolved, peerID)
	}
	return resolved, nil
}

type inboxStats struct {
	userCount      int
	assistantCount int
}

func inboxHistoryStats(history []types.Message) inboxStats {
	stats := inboxStats{}
	for _, msg := range history {
		switch strings.ToLower(strings.TrimSpace(msg.Role)) {
		case "user":
			stats.userCount++
		case "assistant":
			stats.assistantCount++
		}
	}
	return stats
}

func unreadInboxMessages(history []types.Message, readUserCount int) []types.Message {
	if readUserCount < 0 {
		readUserCount = 0
	}
	unread := make([]types.Message, 0)
	seenUsers := 0
	for _, msg := range history {
		if strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			seenUsers++
			if seenUsers <= readUserCount {
				continue
			}
		}
		if seenUsers > readUserCount {
			unread = append(unread, msg)
		}
	}
	return unread
}

func isInboxSessionKey(peerID, sessionKey string) bool {
	trimmedPeer := strings.TrimSpace(peerID)
	trimmedKey := strings.TrimSpace(sessionKey)
	if strings.HasPrefix(trimmedKey, trimmedPeer+"::channel::") {
		return true
	}
	return trimmedKey != trimmedPeer && strings.HasPrefix(trimmedKey, trimmedPeer+"::")
}

func inboxConversationPeerID(peerID, sessionKey string) string {
	prefix := strings.TrimSpace(peerID) + "::channel::"
	trimmedKey := strings.TrimSpace(sessionKey)
	if strings.HasPrefix(trimmedKey, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(trimmedKey, prefix))
	}
	return ""
}

func parseConversationPeerID(conversationPeerID string) (channel, conversationID, threadID string) {
	parts := strings.Split(strings.TrimSpace(conversationPeerID), ":")
	if len(parts) > 0 {
		channel = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		conversationID = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		threadID = strings.Join(parts[2:], ":")
	}
	return channel, conversationID, threadID
}

func approvedConversationLookupKey(channel, conversationID string) string {
	channel = strings.ToLower(strings.TrimSpace(channel))
	conversationID = strings.TrimSpace(conversationID)
	if channel == "" || conversationID == "" {
		return ""
	}
	return channel + "::" + conversationID
}

func bindingVisibleToPeer(peerID string, item channels.ApprovedBinding) bool {
	if strings.TrimSpace(item.PeerID) == strings.TrimSpace(peerID) {
		return true
	}
	if owner := channels.SessionKeyOwnerPeer(strings.TrimSpace(item.SessionKey)); owner != "" && owner == strings.TrimSpace(peerID) {
		return true
	}
	return false
}

func collectRecentUserPreviews(history []types.Message, limit int) []string {
	if limit <= 0 {
		limit = 3
	}
	previews := make([]string, 0, limit)
	for i := len(history) - 1; i >= 0 && len(previews) < limit; i-- {
		msg := history[i]
		if !strings.EqualFold(strings.TrimSpace(msg.Role), "user") {
			continue
		}
		preview := previewMessageContent(msg.Content)
		if preview == "" {
			continue
		}
		previews = append(previews, preview)
	}
	for i, j := 0, len(previews)-1; i < j; i, j = i+1, j-1 {
		previews[i], previews[j] = previews[j], previews[i]
	}
	return previews
}

func lastNonEmptyContent(history []types.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if content := strings.TrimSpace(history[i].Content); content != "" {
			return content
		}
	}
	return ""
}

func previewMessageContent(content string) string {
	content = strings.Join(strings.Fields(strings.TrimSpace(content)), " ")
	if len(content) <= 140 {
		return content
	}
	return strings.TrimSpace(content[:137]) + "..."
}

func buildInboxSummaryText(entry inboxEntry, previews []string) string {
	parts := []string{}
	if entry.Channel != "" {
		parts = append(parts, fmt.Sprintf("channel %s", entry.Channel))
	}
	if entry.DisplayName != "" {
		parts = append(parts, fmt.Sprintf("from %s", entry.DisplayName))
	}
	summary := fmt.Sprintf("Conversation has %d inbound messages and %d unread.", entry.UserMessageCount, entry.UnreadCount)
	if len(parts) > 0 {
		summary = strings.Join(parts, " ") + ": " + summary
	}
	if len(previews) > 0 {
		summary += " Recent inbound messages: " + strings.Join(previews, " | ")
	}
	return summary
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
