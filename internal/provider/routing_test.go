package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ffimnsr/koios/internal/types"
)

type routingStubProvider struct {
	caps           types.ProviderCapabilities
	handoffKey     string
	complete       func(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error)
	completeStream func(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error)
}

func (p *routingStubProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	if p.complete != nil {
		return p.complete(ctx, req)
	}
	return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}, nil
}

func (p *routingStubProvider) CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	if p.completeStream != nil {
		return p.completeStream(ctx, req, w)
	}
	return "ok", nil
}

func (p *routingStubProvider) Capabilities(string) types.ProviderCapabilities {
	return p.caps
}

func (p *routingStubProvider) HandoffIdentity(model string) string {
	if p.handoffKey != "" {
		return p.handoffKey
	}
	return p.caps.Name + "|" + model
}

func TestRoutingProviderBuildChainPrefersCompatibleFallbacks(t *testing.T) {
	primary := &routingStubProvider{caps: types.ProviderCapabilities{Name: "openai", SupportsStreaming: true, SupportsNativeTools: true}, handoffKey: "openai|primary|gpt-5"}
	fallbackNoTools := &routingStubProvider{caps: types.ProviderCapabilities{Name: "anthropic", SupportsStreaming: true}, handoffKey: "anthropic|fallback|claude"}
	fallbackSameProvider := &routingStubProvider{caps: types.ProviderCapabilities{Name: "openai", SupportsStreaming: true, SupportsNativeTools: true}, handoffKey: "openai|primary|gpt-5-mini"}

	rp := NewRoutingProvider(RoutingConfig{
		Primary: modelEntry{model: "gpt-5", prov: primary},
		Fallbacks: []modelEntry{
			{model: "claude-3", prov: fallbackNoTools},
			{model: "gpt-5-mini", prov: fallbackSameProvider},
		},
	})

	chain := rp.buildChain(&types.ChatRequest{
		Stream: true,
		Tools:  []types.Tool{{Type: "function", Function: types.ToolFunction{Name: "calendar.lookup"}}},
	})
	if len(chain) != 3 {
		t.Fatalf("chain length = %d, want 3", len(chain))
	}
	if chain[1].model != "gpt-5-mini" {
		t.Fatalf("first fallback model = %q, want gpt-5-mini", chain[1].model)
	}
	if chain[2].model != "claude-3" {
		t.Fatalf("second fallback model = %q, want claude-3", chain[2].model)
	}
}

func TestRoutingProviderFallbacksOnContextDeadlineExceeded(t *testing.T) {
	primary := &routingStubProvider{
		caps:       types.ProviderCapabilities{Name: "gemini", SupportsStreaming: true, SupportsNativeTools: true},
		handoffKey: "gemini|primary|gemini-3.5-flash-lite",
		complete: func(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
			return nil, context.DeadlineExceeded
		},
	}
	var fallbackModel string
	fallback := &routingStubProvider{
		caps:       types.ProviderCapabilities{Name: "nvidia", SupportsStreaming: true, SupportsNativeTools: true},
		handoffKey: "nvidia|fallback|deepseek-v4-flash",
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			fallbackModel = req.Model
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "fallback ok"}}}}, nil
		},
	}

	rp := NewRoutingProvider(RoutingConfig{
		Primary:   modelEntry{model: "gemini-3.5-flash-lite", prov: primary},
		Fallbacks: []modelEntry{{model: "deepseek-v4-flash", prov: fallback}},
	})

	resp, err := rp.Complete(context.Background(), &types.ChatRequest{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if fallbackModel != "deepseek-v4-flash" {
		t.Fatalf("fallback model = %q, want deepseek-v4-flash", fallbackModel)
	}
	if resp.Choices[0].Message.Content != "fallback ok" {
		t.Fatalf("fallback response = %#v", resp)
	}
}

func TestRoutingProviderStreamFallbacksOnContextDeadlineExceeded(t *testing.T) {
	primary := &routingStubProvider{
		caps:       types.ProviderCapabilities{Name: "gemini", SupportsStreaming: true, SupportsNativeTools: true},
		handoffKey: "gemini|primary|gemini-3.5-flash-lite",
		completeStream: func(context.Context, *types.ChatRequest, http.ResponseWriter) (string, error) {
			return "", context.DeadlineExceeded
		},
	}
	fallback := &routingStubProvider{
		caps:       types.ProviderCapabilities{Name: "nvidia", SupportsStreaming: true, SupportsNativeTools: true},
		handoffKey: "nvidia|fallback|deepseek-v4-flash",
		complete: func(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "stream fallback ok"}}}}, nil
		},
	}

	rp := NewRoutingProvider(RoutingConfig{
		Primary:   modelEntry{model: "gemini-3.5-flash-lite", prov: primary},
		Fallbacks: []modelEntry{{model: "deepseek-v4-flash", prov: fallback}},
	})

	rec := httptest.NewRecorder()
	text, err := rp.CompleteStream(context.Background(), &types.ChatRequest{Stream: true}, rec)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "stream fallback ok" {
		t.Fatalf("text = %q, want stream fallback ok", text)
	}
	if rec.Body.String() != "data: [DONE]\n\n" {
		t.Fatalf("unexpected fallback SSE body: %q", rec.Body.String())
	}
}

func TestRoutingProviderStreamFallbacksWhenFirstProviderCannotStream(t *testing.T) {
	primary := &routingStubProvider{
		caps:       types.ProviderCapabilities{Name: "local", SupportsNativeTools: true},
		handoffKey: "local|primary|gemma4:e2b",
		complete: func(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
			return nil, errors.New("upstream request: context deadline exceeded")
		},
	}
	fallback := &routingStubProvider{
		caps:       types.ProviderCapabilities{Name: "nvidia", SupportsStreaming: true, SupportsNativeTools: true},
		handoffKey: "nvidia|fallback|deepseek-v4-flash",
		complete: func(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "nonstream fallback ok"}}}}, nil
		},
	}

	rp := NewRoutingProvider(RoutingConfig{
		Primary:   modelEntry{model: "gemma4:e2b", prov: primary},
		Fallbacks: []modelEntry{{model: "deepseek-v4-flash", prov: fallback}},
	})

	text, err := rp.CompleteStream(context.Background(), &types.ChatRequest{Stream: true}, httptest.NewRecorder())
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "nonstream fallback ok" {
		t.Fatalf("text = %q, want nonstream fallback ok", text)
	}
}

func TestRoutingProviderClearsProviderScopedStateAcrossFallbackHandoff(t *testing.T) {
	primary := &routingStubProvider{
		caps:       types.ProviderCapabilities{Name: "openai", SupportsStreaming: true, SupportsNativeTools: true},
		handoffKey: "openai|primary|gpt-5",
		complete: func(context.Context, *types.ChatRequest) (*types.ChatResponse, error) {
			return nil, errors.New("timeout")
		},
	}
	var captured *types.ChatRequest
	fallback := &routingStubProvider{
		caps:       types.ProviderCapabilities{Name: "anthropic", SupportsStreaming: true, SupportsNativeTools: true},
		handoffKey: "anthropic|fallback|claude-3",
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			cp := *req
			captured = &cp
			return &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}, nil
		},
	}

	rp := NewRoutingProvider(RoutingConfig{
		Primary:   modelEntry{model: "gpt-5", prov: primary},
		Fallbacks: []modelEntry{{model: "claude-3", prov: fallback}},
	})

	_, err := rp.Complete(context.Background(), &types.ChatRequest{
		Model:                  "gpt-5",
		Messages:               []types.Message{{Role: "user", Content: "hello"}},
		OpenAIServerCompaction: &types.OpenAIServerCompaction{CompactThreshold: 123},
		OpenAIConversationState: &types.OpenAIConversationState{
			PreviousResponseID:  "resp_1",
			CoveredMessages:     2,
			CoveredMessagesHash: "hash",
		},
		AnthropicServerCompaction: &types.AnthropicServerCompaction{TriggerTokens: 512},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if captured == nil {
		t.Fatal("expected fallback request capture")
	}
	if captured.OpenAIServerCompaction != nil {
		t.Fatalf("expected openai compaction cleared on handoff, got %#v", captured.OpenAIServerCompaction)
	}
	if captured.OpenAIConversationState != nil {
		t.Fatalf("expected openai conversation state cleared on handoff, got %#v", captured.OpenAIConversationState)
	}
	if captured.AnthropicServerCompaction != nil {
		t.Fatalf("expected provider-scoped compaction reset on handoff, got %#v", captured.AnthropicServerCompaction)
	}
}
