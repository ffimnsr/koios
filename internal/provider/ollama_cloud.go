package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/types"
)

const ollamaCloudBaseURL = "https://ollama.com"

type ollamaCloudProvider struct {
	client      *http.Client
	selector    *credentialSelector
	apiKey      string
	baseURL     string
	model       string
	idleTimeout time.Duration
}

type ollamaChatRequest struct {
	Model    string          `json:"model"`
	Messages []ollamaMessage `json:"messages"`
	Tools    []types.Tool    `json:"tools,omitempty"`
	Stream   bool            `json:"stream"`
	Options  map[string]any  `json:"options,omitempty"`
	Think    any             `json:"think,omitempty"`
}

type ollamaMessage struct {
	Role      string           `json:"role"`
	Content   string           `json:"content,omitempty"`
	Images    []string         `json:"images,omitempty"`
	ToolCalls []ollamaToolCall `json:"tool_calls,omitempty"`
}

type ollamaToolCall struct {
	Function ollamaToolFunction `json:"function"`
}

type ollamaToolFunction struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type ollamaChatResponse struct {
	Model           string        `json:"model"`
	CreatedAt       string        `json:"created_at"`
	Message         ollamaMessage `json:"message"`
	Done            bool          `json:"done"`
	DoneReason      string        `json:"done_reason"`
	PromptEvalCount int           `json:"prompt_eval_count"`
	EvalCount       int           `json:"eval_count"`
}

func (p *ollamaCloudProvider) Capabilities(string) types.ProviderCapabilities {
	return types.ProviderCapabilities{
		Name:                "ollama-cloud",
		SupportsStreaming:   true,
		SupportsNativeTools: true,
	}
}

func (p *ollamaCloudProvider) HandoffIdentity(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(p.model)
	}
	return "ollama-cloud|" + strings.TrimRight(strings.TrimSpace(p.baseURL), "/") + "|" + model
}

func (p *ollamaCloudProvider) selectAPIKey(ctx context.Context, req *types.ChatRequest) (string, func(error)) {
	if p.selector != nil {
		return p.selector.Select(ctx, req)
	}
	return strings.TrimSpace(p.apiKey), func(error) {}
}

func (p *ollamaCloudProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("ollama cloud request is required")
	}
	prepared := *req
	if strings.TrimSpace(prepared.Model) == "" {
		prepared.Model = p.model
	}
	prepared.Stream = false
	body, err := marshalOllamaChatRequest(&prepared)
	if err != nil {
		return nil, fmt.Errorf("marshal ollama cloud request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build ollama cloud request: %w", err)
	}
	apiKey, report := p.selectAPIKey(ctx, &prepared)
	defer func() { report(err) }()
	setOllamaCloudHeaders(httpReq, apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama cloud upstream request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama cloud upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var raw ollamaChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode ollama cloud response: %w", err)
	}
	return ollamaChatResponseToChatResponse(raw), nil
}

func (p *ollamaCloudProvider) CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	if req == nil {
		return "", fmt.Errorf("ollama cloud request is required")
	}
	streamCtx, cancel := newStreamContext(ctx)
	defer cancel(nil)
	prepared := *req
	if strings.TrimSpace(prepared.Model) == "" {
		prepared.Model = p.model
	}
	prepared.Stream = true
	body, err := marshalOllamaChatRequest(&prepared)
	if err != nil {
		return "", fmt.Errorf("marshal ollama cloud request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost, p.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build ollama cloud request: %w", err)
	}
	apiKey, report := p.selectAPIKey(streamCtx, &prepared)
	defer func() { report(err) }()
	setOllamaCloudHeaders(httpReq, apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("ollama cloud upstream request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("ollama cloud upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	setSSEHeaders(w)
	flusher, _ := w.(http.Flusher)
	touch, stop := startStreamIdleWatchdog(streamCtx, p.idleTimeout, cancel)
	defer stop()

	var text strings.Builder
	sentRole := false
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		touch()
		var chunk ollamaChatResponse
		if err := json.Unmarshal(scanner.Bytes(), &chunk); err != nil {
			return text.String(), fmt.Errorf("decode ollama cloud stream: %w", err)
		}
		if !sentRole {
			if err := writeOllamaCloudSSE(w, flusher, prepared.Model, "", "assistant", nil, nil); err != nil {
				return text.String(), err
			}
			sentRole = true
		}
		if chunk.Message.Content != "" {
			text.WriteString(chunk.Message.Content)
			if err := writeOllamaCloudSSE(w, flusher, prepared.Model, chunk.Message.Content, "", nil, nil); err != nil {
				return text.String(), err
			}
		}
		if len(chunk.Message.ToolCalls) > 0 {
			if err := writeOllamaCloudSSE(w, flusher, prepared.Model, "", "", chunk.Message.ToolCalls, nil); err != nil {
				return text.String(), err
			}
		}
		if chunk.Done {
			reason := "stop"
			if len(chunk.Message.ToolCalls) > 0 {
				reason = "tool_calls"
			}
			if err := writeOllamaCloudSSE(w, flusher, prepared.Model, "", "", nil, &reason); err != nil {
				return text.String(), err
			}
			fmt.Fprint(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return text.String(), wrapStreamReadError(streamCtx, err)
	}
	return text.String(), nil
}

func (p *ollamaCloudProvider) ListModels(ctx context.Context) (*RemoteModelCatalog, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, fmt.Errorf("build ollama cloud catalog request: %w", err)
	}
	apiKey, report := p.selectAPIKey(ctx, nil)
	defer func() { report(err) }()
	setOllamaCloudHeaders(httpReq, apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama cloud catalog request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("ollama cloud upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}
	var payload struct {
		Models []struct {
			Name       string `json:"name"`
			ModifiedAt string `json:"modified_at"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode ollama cloud catalog: %w", err)
	}
	models := make([]RemoteModel, 0, len(payload.Models))
	for _, model := range payload.Models {
		models = append(models, RemoteModel{ID: model.Name, Object: "model", OwnedBy: "ollama"})
	}
	models = normalizeRemoteModels(models)
	return &RemoteModelCatalog{
		Provider:   "ollama-cloud",
		BaseURL:    p.baseURL,
		Source:     "provider_api",
		Endpoint:   "/api/tags",
		Inspection: ollamaCloudInspection(),
		Models:     models,
		Count:      len(models),
	}, nil
}

func (p *ollamaCloudProvider) UsageStatus(context.Context) (*UsageQuotaStatus, error) {
	return &UsageQuotaStatus{
		Provider:   "ollama-cloud",
		BaseURL:    p.baseURL,
		Source:     "provider_api",
		Status:     "unsupported",
		Inspection: ollamaCloudInspection(),
		Message:    "Ollama Cloud does not expose a normalized usage/quota endpoint through this integration",
	}, nil
}

func marshalOllamaChatRequest(req *types.ChatRequest) ([]byte, error) {
	messages, err := toOllamaMessages(req.Messages)
	if err != nil {
		return nil, err
	}
	wire := ollamaChatRequest{
		Model:    req.Model,
		Messages: messages,
		Tools:    encodeProviderToolDefinitions(req.Tools),
		Stream:   req.Stream,
	}
	if think := ollamaThinkValue(req.ReasoningEffort, req.ReasoningBudget); think != nil {
		wire.Think = think
	} else if req.ReasoningVisibility != "off" {
		wire.Think = true
	}
	options := make(map[string]any)
	if req.Temperature != nil {
		options["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		options["top_p"] = *req.TopP
	}
	if req.MaxTokens > 0 {
		options["num_predict"] = req.MaxTokens
	}
	if len(options) > 0 {
		wire.Options = options
	}
	return json.Marshal(wire)
}

func toOllamaMessages(messages []types.Message) ([]ollamaMessage, error) {
	result := make([]ollamaMessage, 0, len(messages))
	for _, message := range messages {
		out := ollamaMessage{Role: message.Role, Content: message.Content}
		for _, part := range message.Parts {
			switch part.Type {
			case "text":
				out.Content += part.Text
			case "image_url":
				if part.ImageURL != nil && isDataURI(part.ImageURL.URL) {
					_, image := parseDataURI(part.ImageURL.URL)
					out.Images = append(out.Images, image)
				}
			}
		}
		for _, call := range message.ToolCalls {
			arguments := json.RawMessage(call.Function.Arguments)
			if !json.Valid(arguments) {
				return nil, fmt.Errorf("tool call %q has invalid JSON arguments", call.Function.Name)
			}
			out.ToolCalls = append(out.ToolCalls, ollamaToolCall{Function: ollamaToolFunction{
				Name:      types.EncodeProviderToolName(call.Function.Name),
				Arguments: arguments,
			}})
		}
		result = append(result, out)
	}
	return result, nil
}

func ollamaChatResponseToChatResponse(raw ollamaChatResponse) *types.ChatResponse {
	message := types.Message{Role: raw.Message.Role, Content: raw.Message.Content}
	for index, call := range raw.Message.ToolCalls {
		message.ToolCalls = append(message.ToolCalls, types.ToolCall{
			ID:   fmt.Sprintf("ollama_call_%d", index),
			Type: "function",
			Function: types.ToolCallFunctionRef{
				Name:      types.DecodeProviderToolName(call.Function.Name),
				Arguments: string(call.Function.Arguments),
			},
		})
	}
	finishReason := "stop"
	if len(message.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	return &types.ChatResponse{
		Object: "chat.completion",
		Choices: []types.ChatChoice{{
			Index:        0,
			Message:      message,
			FinishReason: finishReason,
		}},
		Usage: types.Usage{
			PromptTokens:     raw.PromptEvalCount,
			CompletionTokens: raw.EvalCount,
			TotalTokens:      raw.PromptEvalCount + raw.EvalCount,
		},
	}
}

func setOllamaCloudHeaders(request *http.Request, apiKey string) {
	request.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(apiKey) != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func writeOllamaCloudSSE(w http.ResponseWriter, flusher http.Flusher, model, content, role string, calls []ollamaToolCall, finishReason *string) error {
	delta := map[string]any{}
	if role != "" {
		delta["role"] = role
	}
	if content != "" {
		delta["content"] = content
	}
	if len(calls) > 0 {
		toolCalls := make([]map[string]any, 0, len(calls))
		for index, call := range calls {
			toolCalls = append(toolCalls, map[string]any{
				"index": index,
				"id":    fmt.Sprintf("ollama_call_%d", index),
				"type":  "function",
				"function": map[string]any{
					"name":      types.DecodeProviderToolName(call.Function.Name),
					"arguments": string(call.Function.Arguments),
				},
			})
		}
		delta["tool_calls"] = toolCalls
	}
	payload := map[string]any{
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []map[string]any{{
			"index":         0,
			"delta":         delta,
			"finish_reason": finishReason,
		}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode ollama cloud stream chunk: %w", err)
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", encoded); err != nil {
		return fmt.Errorf("write ollama cloud stream chunk: %w", err)
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func ollamaCloudInspection() ProviderInspection {
	return ProviderInspection{
		Family:                 "ollama_cloud",
		Interface:              "native_ollama",
		CatalogMode:            "native_tags_api",
		CatalogEndpoint:        "/api/tags",
		UsageMode:              "unsupported",
		SupportsDynamicCatalog: true,
		SupportsUsage:          false,
		Notes:                  []string{"Direct Ollama Cloud access uses the native /api/chat API with a Bearer API key"},
	}
}
