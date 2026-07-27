package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/peerllm"
)

// RemoteModel describes one provider-owned model entry discovered dynamically
// from an upstream provider catalog.
type RemoteModel struct {
	ID      string `json:"id"`
	Object  string `json:"object,omitempty"`
	OwnedBy string `json:"owned_by,omitempty"`
	Created int64  `json:"created,omitempty"`
}

// ProviderInspection describes the upstream surface Koios used for provider
// inspection and how that surface is normalized.
type ProviderInspection struct {
	Family                 string   `json:"family"`
	Interface              string   `json:"interface"`
	CatalogMode            string   `json:"catalog_mode,omitempty"`
	CatalogEndpoint        string   `json:"catalog_endpoint,omitempty"`
	UsageMode              string   `json:"usage_mode,omitempty"`
	UsageEndpoint          string   `json:"usage_endpoint,omitempty"`
	SupportsDynamicCatalog bool     `json:"supports_dynamic_catalog"`
	SupportsUsage          bool     `json:"supports_usage"`
	Notes                  []string `json:"notes,omitempty"`
}

// RemoteModelCatalog is a normalized provider-owned model catalog response.
type RemoteModelCatalog struct {
	Provider   string             `json:"provider"`
	BaseURL    string             `json:"base_url,omitempty"`
	Source     string             `json:"source"`
	Endpoint   string             `json:"endpoint,omitempty"`
	Inspection ProviderInspection `json:"inspection"`
	Models     []RemoteModel      `json:"models"`
	Count      int                `json:"count"`
}

// UsageQuotaStatus is a normalized provider-owned usage or quota response.
type UsageQuotaStatus struct {
	Provider   string             `json:"provider"`
	BaseURL    string             `json:"base_url,omitempty"`
	Source     string             `json:"source"`
	Status     string             `json:"status"`
	Endpoint   string             `json:"endpoint,omitempty"`
	Inspection ProviderInspection `json:"inspection"`
	Message    string             `json:"message,omitempty"`
	Label      string             `json:"label,omitempty"`
	Usage      float64            `json:"usage,omitempty"`
	Limit      float64            `json:"limit,omitempty"`
	Remaining  float64            `json:"remaining,omitempty"`
	Currency   string             `json:"currency,omitempty"`
	Requests   int                `json:"requests,omitempty"`
	Interval   string             `json:"interval,omitempty"`
	IsFreeTier *bool              `json:"is_free_tier,omitempty"`
}

// ModelCatalogProvider optionally exposes provider-owned dynamic model catalogs.
type ModelCatalogProvider interface {
	ListModels(ctx context.Context) (*RemoteModelCatalog, error)
}

// UsageStatusProvider optionally exposes provider-owned usage or quota status.
type UsageStatusProvider interface {
	UsageStatus(ctx context.Context) (*UsageQuotaStatus, error)
}

func BuildConfigFromPeerProfile(profile *peerllm.ProviderProfile, requestTimeout, idleTimeout time.Duration) *config.Config {
	if requestTimeout <= 0 {
		requestTimeout = 15 * time.Second
	}
	if idleTimeout <= 0 {
		idleTimeout = 5 * time.Second
	}
	model := strings.TrimSpace(profile.DefaultModel)
	if model == "" {
		model = "default"
	}
	return &config.Config{
		Provider:       strings.TrimSpace(profile.Provider),
		APIKey:         profile.APIKeyEnc,
		APIKeys:        append([]string(nil), profile.APIKeys...),
		BaseURL:        strings.TrimSpace(profile.BaseURL),
		Model:          model,
		RequestTimeout: requestTimeout,
		LLMIdleTimeout: idleTimeout,
	}
}

func normalizeRemoteModels(models []RemoteModel) []RemoteModel {
	filtered := make([]RemoteModel, 0, len(models))
	for _, model := range models {
		model.ID = strings.TrimSpace(model.ID)
		if model.ID == "" {
			continue
		}
		filtered = append(filtered, model)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].ID < filtered[j].ID
	})
	return filtered
}

func parseNumericField(raw map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		switch v := value.(type) {
		case float64:
			return v, true
		case float32:
			return float64(v), true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		case json.Number:
			if f, err := v.Float64(); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func parseStringField(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if s, ok := value.(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func parseBoolField(raw map[string]any, keys ...string) (*bool, bool) {
	for _, key := range keys {
		value, ok := raw[key]
		if !ok {
			continue
		}
		if b, ok := value.(bool); ok {
			return &b, true
		}
	}
	return nil, false
}

func readProviderJSONResponse(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func openAIProviderInspection(name string) ProviderInspection {
	inspection := ProviderInspection{
		SupportsDynamicCatalog: true,
		SupportsUsage:          false,
		CatalogEndpoint:        "/v1/models",
	}
	switch name {
	case "openai":
		inspection.Family = "openai"
		inspection.Interface = "native_openai"
		inspection.CatalogMode = "native_models_api"
	case "openrouter":
		inspection.Family = "openrouter"
		inspection.Interface = "openai_compatible"
		inspection.CatalogMode = "normalized_openai_compatible_catalog"
		inspection.UsageMode = "provider_key_metadata"
		inspection.UsageEndpoint = "/v1/auth/key"
		inspection.SupportsUsage = true
	case "openai-compatible":
		inspection.Family = "openai_compatible_generic"
		inspection.Interface = "openai_compatible"
		inspection.CatalogMode = "openai_compatible_catalog"
		inspection.Notes = []string{"Generic OpenAI-compatible provider uses the legacy /v1/chat/completions and /v1/models surface and requires an explicit base_url"}
	case "opencode-go":
		inspection.Family = "opencode_go"
		inspection.Interface = "openai_compatible"
		inspection.CatalogMode = "openai_compatible_catalog"
		inspection.Notes = []string{"OpenCode Go exposes a documented /v1/chat/completions and /v1/models surface for its curated open-model subscription"}
	case "nvidia":
		inspection.Family = "nvidia_nim"
		inspection.Interface = "openai_compatible"
		inspection.CatalogMode = "openai_compatible_catalog"
	case "ollama":
		inspection.Family = "ollama"
		inspection.Interface = "openai_compatible"
		inspection.CatalogMode = "openai_compatible_catalog"
		inspection.Notes = []string{"owned_by defaults to library in the OpenAI-compatible models surface"}
	case "vllm":
		inspection.Family = "vllm"
		inspection.Interface = "openai_compatible"
		inspection.CatalogMode = "openai_compatible_catalog"
	case "litellm":
		inspection.Family = "litellm_proxy"
		inspection.Interface = "openai_compatible"
		inspection.CatalogMode = "openai_compatible_catalog"
		inspection.Notes = []string{"LiteLLM also exposes proxy-admin model management separately from the OpenAI-compatible inference surface"}
	case "gemini":
		inspection.Family = "gemini"
		inspection.Interface = "openai_compatible"
		inspection.CatalogMode = "openai_compatible_catalog"
		inspection.CatalogEndpoint = "/models"
		inspection.Notes = []string{"Gemini uses the v1beta/openai compatibility surface rather than a root /v1 prefix"}
	default:
		inspection.Family = strings.TrimSpace(name)
		inspection.Interface = "openai_compatible"
		inspection.CatalogMode = "openai_compatible_catalog"
	}
	return inspection
}

func anthropicInspection() ProviderInspection {
	return ProviderInspection{
		Family:                 "anthropic",
		Interface:              "native_anthropic",
		CatalogMode:            "native_models_api",
		CatalogEndpoint:        "/v1/models",
		UsageMode:              "unsupported",
		SupportsDynamicCatalog: true,
		SupportsUsage:          false,
	}
}

func (p *openAIProvider) modelCatalogPath() string {
	if p.hooks.name == "gemini" {
		return "/models"
	}
	return "/v1/models"
}

func (p *openAIProvider) ListModels(ctx context.Context) (*RemoteModelCatalog, error) {
	endpoint := p.modelCatalogPath()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	apiKey, report := p.selectAPIKey(ctx, nil)
	defer func() { report(err) }()
	p.hooks.applyHeaders(httpReq, apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	var payload struct {
		Data []RemoteModel `json:"data"`
	}
	if err := readProviderJSONResponse(resp, &payload); err != nil {
		return nil, err
	}
	payload.Data = normalizeRemoteModels(payload.Data)
	return &RemoteModelCatalog{
		Provider:   p.hooks.name,
		BaseURL:    p.baseURL,
		Source:     "provider_api",
		Endpoint:   endpoint,
		Inspection: openAIProviderInspection(p.hooks.name),
		Models:     payload.Data,
		Count:      len(payload.Data),
	}, nil
}

func (p *anthropicProvider) ListModels(ctx context.Context) (*RemoteModelCatalog, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	apiKey, report := p.selectAPIKey(ctx, nil)
	defer func() { report(err) }()
	p.setHeaders(httpReq, apiKey, nil)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	var payload struct {
		Data []RemoteModel `json:"data"`
	}
	if err := readProviderJSONResponse(resp, &payload); err != nil {
		return nil, err
	}
	payload.Data = normalizeRemoteModels(payload.Data)
	return &RemoteModelCatalog{
		Provider:   p.hooks.name,
		BaseURL:    p.baseURL,
		Source:     "provider_api",
		Endpoint:   "/v1/models",
		Inspection: anthropicInspection(),
		Models:     payload.Data,
		Count:      len(payload.Data),
	}, nil
}

func (p *openAIProvider) UsageStatus(ctx context.Context) (*UsageQuotaStatus, error) {
	if p.hooks.name != "openrouter" {
		inspection := openAIProviderInspection(p.hooks.name)
		inspection.UsageMode = "unsupported"
		return &UsageQuotaStatus{
			Provider:   p.hooks.name,
			BaseURL:    p.baseURL,
			Source:     "provider_api",
			Status:     "unsupported",
			Inspection: inspection,
			Message:    "provider does not expose a normalized usage/quota endpoint through this integration",
		}, nil
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/auth/key", nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	apiKey, report := p.selectAPIKey(ctx, nil)
	defer func() { report(err) }()
	p.hooks.applyHeaders(httpReq, apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	var payload struct {
		Data map[string]any `json:"data"`
	}
	if err := readProviderJSONResponse(resp, &payload); err != nil {
		return nil, err
	}
	inspection := openAIProviderInspection(p.hooks.name)
	status := &UsageQuotaStatus{
		Provider:   p.hooks.name,
		BaseURL:    p.baseURL,
		Source:     "provider_api",
		Status:     "ok",
		Endpoint:   "/v1/auth/key",
		Inspection: inspection,
		Label:      parseStringField(payload.Data, "label"),
		Currency:   parseStringField(payload.Data, "currency"),
	}
	if usage, ok := parseNumericField(payload.Data, "usage"); ok {
		status.Usage = usage
	}
	if limit, ok := parseNumericField(payload.Data, "limit"); ok {
		status.Limit = limit
	}
	if remaining, ok := parseNumericField(payload.Data, "remaining", "limit_remaining", "credits_remaining"); ok {
		status.Remaining = remaining
	} else if status.Limit > 0 || status.Usage > 0 {
		status.Remaining = status.Limit - status.Usage
	}
	if isFreeTier, ok := parseBoolField(payload.Data, "is_free_tier"); ok {
		status.IsFreeTier = isFreeTier
	}
	if rawRateLimit, ok := payload.Data["rate_limit"].(map[string]any); ok {
		if requests, ok := parseNumericField(rawRateLimit, "requests"); ok {
			status.Requests = int(requests)
		}
		status.Interval = parseStringField(rawRateLimit, "interval")
	}
	return status, nil
}

func (p *anthropicProvider) UsageStatus(context.Context) (*UsageQuotaStatus, error) {
	inspection := anthropicInspection()
	inspection.UsageMode = "unsupported"
	return &UsageQuotaStatus{
		Provider:   p.hooks.name,
		BaseURL:    p.baseURL,
		Source:     "provider_api",
		Status:     "unsupported",
		Inspection: inspection,
		Message:    "provider does not expose a normalized usage/quota endpoint through this integration",
	}, nil
}

func (rp *RoutingProvider) ListModels(ctx context.Context) (*RemoteModelCatalog, error) {
	catalogProvider, ok := rp.primary.prov.(ModelCatalogProvider)
	if !ok {
		return nil, fmt.Errorf("provider does not support dynamic model catalogs")
	}
	return catalogProvider.ListModels(ctx)
}

func (rp *RoutingProvider) UsageStatus(ctx context.Context) (*UsageQuotaStatus, error) {
	usageProvider, ok := rp.primary.prov.(UsageStatusProvider)
	if !ok {
		return nil, fmt.Errorf("provider does not support provider-owned usage status")
	}
	return usageProvider.UsageStatus(ctx)
}
