package handler

import (
	"context"
	"net/http"

	"github.com/ffimnsr/koios/internal/requestctx"
	"github.com/ffimnsr/koios/internal/types"
)

// llmProvider is the interface the handler requires from an LLM backend.
// It is satisfied by *provider.openAIProvider and *provider.anthropicProvider
// without any adapter code, thanks to Go's structural typing.
type llmProvider interface {
	Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
	CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error)
}

// heartbeatEnsurer is the subset of the heartbeat.Runner interface used by
// the WebSocket handler. Using an interface keeps the handler package from
// importing the heartbeat package directly.
type heartbeatEnsurer interface {
	EnsureRunning(peerID string)
}

var (
	validateMessages = requestctx.ValidateMessages
	splitByRole      = requestctx.SplitMessages
)

func requestBuilder(ctx context.Context, h *Handler, peerID string, messages, history []types.Message, stream bool) (*types.ChatRequest, error) {
	var extraSystem []types.Message
	if h.standingManager != nil {
		msg, err := h.standingManager.SystemMessage(peerID)
		if err != nil {
			return nil, err
		}
		if msg != nil {
			extraSystem = append(extraSystem, *msg)
		}
	}
	built, err := requestctx.Build(ctx, requestctx.BuildOptions{
		Model:        h.model,
		Messages:     messages,
		History:      history,
		Stream:       stream,
		ExtraSystem:  extraSystem,
		MemoryStore:  h.memStore,
		MemoryTopK:   h.memTopK,
		MemoryInject: h.memInject,
		MemoryPeerID: peerID,
	})
	if err != nil {
		return nil, err
	}
	return built.Request, nil
}

// IsValidPeerID returns true if id is a non-empty string containing only
// safe characters. Acts as a guard against header and path injection.
func IsValidPeerID(id string) bool {
	if len(id) == 0 || len(id) > 256 {
		return false
	}
	for _, c := range id {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '@' || c == ':' {
			continue
		}
		return false
	}
	return true
}
