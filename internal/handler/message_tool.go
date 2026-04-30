package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/channels"
)

type messageToolParams struct {
	Channel          string         `json:"channel"`
	SubjectID        string         `json:"subject_id"`
	ConversationID   string         `json:"conversation_id"`
	ThreadID         string         `json:"thread_id"`
	Text             string         `json:"text"`
	ReplyToMessageID string         `json:"reply_to_message_id"`
	Metadata         map[string]any `json:"metadata"`
}

func (h *Handler) runMessageTool(ctx context.Context, peerID string, args messageToolParams) (map[string]any, error) {
	if h.channelManager == nil || !h.channelManager.HasOutboundSenders() {
		return nil, fmt.Errorf("outbound channels are not enabled")
	}
	channelID := strings.TrimSpace(args.Channel)
	if channelID == "" {
		return nil, fmt.Errorf("channel is required")
	}
	text := strings.TrimSpace(args.Text)
	if text == "" {
		return nil, fmt.Errorf("text is required")
	}
	subjectID := strings.TrimSpace(args.SubjectID)
	conversationID := strings.TrimSpace(args.ConversationID)
	if subjectID == "" && conversationID == "" {
		return nil, fmt.Errorf("subject_id or conversation_id is required")
	}
	messageMetadata := make(map[string]any, len(args.Metadata)+1)
	for key, value := range args.Metadata {
		messageMetadata[key] = value
	}
	messageMetadata["source_peer_id"] = strings.TrimSpace(peerID)
	receipt, err := h.channelManager.SendMessage(ctx, channelID, channels.OutboundTarget{
		SubjectID:      subjectID,
		ConversationID: conversationID,
		ThreadID:       strings.TrimSpace(args.ThreadID),
	}, channels.OutboundMessage{
		Text:             text,
		ReplyToMessageID: strings.TrimSpace(args.ReplyToMessageID),
		ThreadID:         strings.TrimSpace(args.ThreadID),
		Metadata:         messageMetadata,
	})
	if err != nil {
		return nil, err
	}
	messageIDs := make([]string, 0, len(receipt.Deliveries))
	for _, delivery := range receipt.Deliveries {
		if messageID := strings.TrimSpace(delivery.MessageID); messageID != "" {
			messageIDs = append(messageIDs, messageID)
		}
	}
	return map[string]any{
		"ok":               true,
		"channel":          receipt.Channel,
		"subject_id":       receipt.SubjectID,
		"conversation_id":  receipt.ConversationID,
		"thread_id":        receipt.ThreadID,
		"chunk_count":      receipt.ChunkCount,
		"used_binding":     receipt.UsedBinding,
		"delivery_count":   len(receipt.Deliveries),
		"deliveries":       receipt.Deliveries,
		"message_ids":      messageIDs,
		"metadata":         args.Metadata,
		"receipt_metadata": receipt.Metadata,
	}, nil
}
