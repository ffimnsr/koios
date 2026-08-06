package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/types"
)

// openCodeZenProvider routes each OpenCode Zen model to its documented wire
// protocol. Zen exposes one catalog but mixes OpenAI Chat Completions,
// Responses, Anthropic Messages, and Google-native models.
type openCodeZenProvider struct {
	model     string
	chat      *openAIProvider
	responses *openAIProvider
	anthropic *anthropicProvider
}

type openCodeZenProtocol string

const (
	openCodeZenChat      openCodeZenProtocol = "chat_completions"
	openCodeZenResponses openCodeZenProtocol = "responses"
	openCodeZenMessages  openCodeZenProtocol = "messages"
	openCodeZenGoogle    openCodeZenProtocol = "google"
	openCodeZenUnknown   openCodeZenProtocol = "unknown"
)

func newOpenCodeZenProvider(client *http.Client, selector *credentialSelector, baseURL, model string, idleTimeout time.Duration) *openCodeZenProvider {
	return &openCodeZenProvider{
		model: model,
		chat: &openAIProvider{
			client:      client,
			selector:    selector,
			baseURL:     baseURL,
			model:       model,
			idleTimeout: idleTimeout,
			hooks:       openAICompatibleHooks("opencode-zen"),
		},
		responses: &openAIProvider{
			client:         client,
			selector:       selector,
			baseURL:        baseURL,
			model:          model,
			idleTimeout:    idleTimeout,
			hooks:          openAICompatibleHooks("opencode-zen"),
			forceResponses: true,
		},
		anthropic: &anthropicProvider{
			client:      client,
			selector:    selector,
			baseURL:     baseURL,
			model:       model,
			idleTimeout: idleTimeout,
			hooks:       anthropicCompatibleHooks("opencode-zen"),
		},
	}
}

func (p *openCodeZenProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	provider, prepared, err := p.forRequest(req)
	if err != nil {
		return nil, err
	}
	return provider.Complete(ctx, prepared)
}

func (p *openCodeZenProvider) CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	provider, prepared, err := p.forRequest(req)
	if err != nil {
		return "", err
	}
	return provider.CompleteStream(ctx, prepared, w)
}

func (p *openCodeZenProvider) Capabilities(model string) types.ProviderCapabilities {
	provider, _, err := p.forRequest(&types.ChatRequest{Model: model})
	if err != nil {
		return types.ProviderCapabilities{Name: "opencode-zen"}
	}
	return provider.(interface {
		Capabilities(string) types.ProviderCapabilities
	}).Capabilities(model)
}

func (p *openCodeZenProvider) HandoffIdentity(model string) string {
	provider, _, err := p.forRequest(&types.ChatRequest{Model: model})
	if err != nil {
		return "opencode-zen|unsupported|" + strings.TrimSpace(model)
	}
	return provider.(interface{ HandoffIdentity(string) string }).HandoffIdentity(model)
}

func (p *openCodeZenProvider) ListModels(ctx context.Context) (*RemoteModelCatalog, error) {
	catalog, err := p.chat.ListModels(ctx)
	if err != nil {
		return nil, err
	}
	models := catalog.Models[:0]
	for _, model := range catalog.Models {
		if openCodeZenModelProtocol(model.ID) != openCodeZenGoogle {
			models = append(models, model)
		}
	}
	catalog.Models = models
	catalog.Count = len(models)
	catalog.Inspection = openCodeZenInspection()
	return catalog, nil
}

func (p *openCodeZenProvider) UsageStatus(ctx context.Context) (*UsageQuotaStatus, error) {
	status, err := p.chat.UsageStatus(ctx)
	if err != nil {
		return nil, err
	}
	status.Inspection = openCodeZenInspection()
	return status, nil
}

func (p *openCodeZenProvider) forRequest(req *types.ChatRequest) (Provider, *types.ChatRequest, error) {
	if req == nil {
		return nil, nil, fmt.Errorf("opencode zen request is required")
	}
	prepared := *req
	if strings.TrimSpace(prepared.Model) == "" {
		prepared.Model = p.model
	}
	switch openCodeZenModelProtocol(prepared.Model) {
	case openCodeZenChat:
		return p.chat, &prepared, nil
	case openCodeZenResponses:
		return p.responses, &prepared, nil
	case openCodeZenMessages:
		return p.anthropic, &prepared, nil
	case openCodeZenGoogle:
		return nil, nil, fmt.Errorf("opencode zen model %q requires Google-native API support, which Koios does not implement", prepared.Model)
	default:
		return nil, nil, fmt.Errorf("opencode zen model %q has no known supported API protocol", prepared.Model)
	}
}

func openCodeZenModelProtocol(model string) openCodeZenProtocol {
	model = strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.HasPrefix(model, "gpt-"), strings.HasPrefix(model, "grok-"):
		return openCodeZenResponses
	case strings.HasPrefix(model, "claude-"), strings.HasPrefix(model, "qwen"):
		return openCodeZenMessages
	case strings.HasPrefix(model, "gemini-"):
		return openCodeZenGoogle
	case strings.HasPrefix(model, "deepseek-"),
		strings.HasPrefix(model, "minimax-"),
		strings.HasPrefix(model, "glm-"),
		strings.HasPrefix(model, "kimi-"),
		strings.HasPrefix(model, "big-pickle"),
		strings.HasPrefix(model, "mimo-"),
		strings.HasPrefix(model, "laguna-"),
		strings.HasPrefix(model, "ling-"),
		strings.HasPrefix(model, "longcat-"),
		strings.HasPrefix(model, "north-"),
		strings.HasPrefix(model, "nemotron-"):
		return openCodeZenChat
	default:
		return openCodeZenUnknown
	}
}

func openCodeZenInspection() ProviderInspection {
	return ProviderInspection{
		Family:                 "opencode_zen",
		Interface:              "multi_protocol",
		CatalogMode:            "protocol_aware_catalog",
		CatalogEndpoint:        "/v1/models",
		UsageMode:              "unsupported",
		SupportsDynamicCatalog: true,
		SupportsUsage:          false,
		Notes: []string{
			"Zen routes models by family: Chat Completions, Responses, or Anthropic Messages",
			"Gemini models are omitted because Koios does not implement Zen's Google-native transport",
		},
	}
}
