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

type openAIWireRequest struct {
	Model               string               `json:"model"`
	Messages            []types.Message      `json:"messages"`
	Tools               []types.Tool         `json:"tools,omitempty"`
	ToolChoice          any                  `json:"tool_choice,omitempty"`
	Stream              bool                 `json:"stream"`
	MaxTokens           int                  `json:"max_tokens,omitempty"`
	Temperature         *float64             `json:"temperature,omitempty"`
	TopP                *float64             `json:"top_p,omitempty"`
	FrequencyPenalty    *float64             `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64             `json:"presence_penalty,omitempty"`
	Stop                json.RawMessage      `json:"stop,omitempty"`
	User                string               `json:"user,omitempty"`
	ReasoningEffort     string               `json:"reasoning_effort,omitempty"`
	Effort              string               `json:"effort,omitempty"`
	Think               any                  `json:"think,omitempty"`
	ThinkingTokenBudget int                  `json:"thinking_token_budget,omitempty"`
	Reasoning           *openRouterReasoning `json:"reasoning,omitempty"`
	IncludeReasoning    *bool                `json:"include_reasoning,omitempty"`
	ChatTemplateKwargs  map[string]any       `json:"chat_template_kwargs,omitempty"`
	ExtraBody           map[string]any       `json:"extra_body,omitempty"`
}

type openRouterReasoning struct {
	Enabled   *bool  `json:"enabled,omitempty"`
	Effort    string `json:"effort,omitempty"`
	MaxTokens int    `json:"max_tokens,omitempty"`
	Exclude   *bool  `json:"exclude,omitempty"`
}

type geminiThinkingConfig struct {
	IncludeThoughts bool   `json:"include_thoughts,omitempty"`
	ThinkingLevel   string `json:"thinking_level,omitempty"`
}

type openAIResponsesRequest struct {
	Model           string                     `json:"model"`
	Input           []openAIResponsesInputItem `json:"input,omitempty"`
	Tools           []types.Tool               `json:"tools,omitempty"`
	ToolChoice      any                        `json:"tool_choice,omitempty"`
	Stream          bool                       `json:"stream,omitempty"`
	MaxOutputTokens int                        `json:"max_output_tokens,omitempty"`
	Temperature     *float64                   `json:"temperature,omitempty"`
	TopP            *float64                   `json:"top_p,omitempty"`
	User            string                     `json:"user,omitempty"`
	Reasoning       *openAIResponsesReasoning  `json:"reasoning,omitempty"`
}

type openAIResponsesReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type openAIResponsesInputItem struct {
	Type      string                       `json:"type,omitempty"`
	Role      string                       `json:"role,omitempty"`
	Content   []openAIResponsesContentPart `json:"content,omitempty"`
	CallID    string                       `json:"call_id,omitempty"`
	Name      string                       `json:"name,omitempty"`
	Arguments string                       `json:"arguments,omitempty"`
	Output    string                       `json:"output,omitempty"`
}

type openAIResponsesContentPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

type openAIResponsesResponse struct {
	ID        string                      `json:"id"`
	Object    string                      `json:"object"`
	Created   int64                       `json:"created,omitempty"`
	CreatedAt int64                       `json:"created_at,omitempty"`
	Output    []openAIResponsesOutputItem `json:"output"`
	Usage     openAIResponsesUsage        `json:"usage"`
}

type openAIResponsesOutputItem struct {
	Type      string          `json:"type,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Summary   json.RawMessage `json:"summary,omitempty"`
	Text      string          `json:"text,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
}

type openAIResponsesUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

func marshalOpenAIWireRequest(providerName string, req *types.ChatRequest) ([]byte, error) {
	wire := openAIWireRequest{
		Model:            req.Model,
		Messages:         sanitizeOpenAIWireMessages(providerName, req.Messages),
		Tools:            req.Tools,
		ToolChoice:       req.ToolChoice,
		Stream:           req.Stream,
		MaxTokens:        req.MaxTokens,
		Temperature:      req.Temperature,
		TopP:             req.TopP,
		FrequencyPenalty: req.FrequencyPenalty,
		PresencePenalty:  req.PresencePenalty,
		Stop:             req.Stop,
		User:             req.User,
		ReasoningEffort:  req.ReasoningEffort,
	}
	applyOpenAICompatibleReasoning(providerName, req, &wire)
	return json.Marshal(wire)
}

func marshalOpenAIResponsesRequest(req *types.ChatRequest) ([]byte, error) {
	wire := openAIResponsesRequest{
		Model:           req.Model,
		Input:           buildOpenAIResponsesInput(req.Messages),
		Tools:           req.Tools,
		ToolChoice:      req.ToolChoice,
		Stream:          req.Stream,
		MaxOutputTokens: req.MaxTokens,
		Temperature:     req.Temperature,
		TopP:            req.TopP,
		User:            req.User,
	}
	if reasoningEnabled(req) {
		wire.Reasoning = &openAIResponsesReasoning{Effort: req.ReasoningEffort}
		if req.ReasoningVisibility != "off" {
			wire.Reasoning.Summary = "auto"
		}
	}
	return json.Marshal(wire)
}

func buildOpenAIResponsesInput(messages []types.Message) []openAIResponsesInputItem {
	items := make([]openAIResponsesInputItem, 0, len(messages))
	for _, msg := range messages {
		switch msg.Role {
		case "tool":
			items = append(items, openAIResponsesInputItem{
				Type:   "function_call_output",
				CallID: strings.TrimSpace(msg.ToolCallID),
				Output: strings.TrimSpace(messagePlainText(msg)),
			})
			continue
		}

		if content := buildOpenAIResponsesMessageContent(msg); len(content) > 0 {
			items = append(items, openAIResponsesInputItem{
				Type:    "message",
				Role:    normalizeOpenAIResponsesRole(msg.Role),
				Content: content,
			})
		}
		for _, call := range msg.ToolCalls {
			items = append(items, openAIResponsesInputItem{
				Type:      "function_call",
				CallID:    strings.TrimSpace(call.ID),
				Name:      strings.TrimSpace(call.Function.Name),
				Arguments: strings.TrimSpace(call.Function.Arguments),
			})
		}
	}
	return items
}

func buildOpenAIResponsesMessageContent(msg types.Message) []openAIResponsesContentPart {
	parts := make([]openAIResponsesContentPart, 0, len(msg.Parts)+1)
	for _, part := range msg.Parts {
		switch part.Type {
		case "text":
			text := strings.TrimSpace(part.Text)
			if text != "" {
				parts = append(parts, openAIResponsesContentPart{Type: "input_text", Text: text})
			}
		case "image_url":
			if part.ImageURL != nil && strings.TrimSpace(part.ImageURL.URL) != "" {
				parts = append(parts, openAIResponsesContentPart{
					Type:     "input_image",
					ImageURL: strings.TrimSpace(part.ImageURL.URL),
					Detail:   strings.TrimSpace(part.ImageURL.Detail),
				})
			}
		}
	}
	if len(parts) > 0 {
		return parts
	}
	if text := strings.TrimSpace(messagePlainText(msg)); text != "" {
		return []openAIResponsesContentPart{{Type: "input_text", Text: text}}
	}
	return nil
}

func normalizeOpenAIResponsesRole(role string) string {
	switch strings.TrimSpace(role) {
	case "system", "developer", "user", "assistant":
		return strings.TrimSpace(role)
	default:
		return "user"
	}
}

func messagePlainText(msg types.Message) string {
	if text := strings.TrimSpace(msg.Content); text != "" {
		return text
	}
	if len(msg.Parts) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, part := range msg.Parts {
		if part.Type != "text" {
			continue
		}
		text := strings.TrimSpace(part.Text)
		if text == "" {
			continue
		}
		if sb.Len() > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(text)
	}
	return sb.String()
}

func applyOpenAICompatibleReasoning(providerName string, req *types.ChatRequest, wire *openAIWireRequest) {
	if wire == nil {
		return
	}
	if req.ReasoningVisibility == "off" && req.ReasoningEffort == "" && req.ReasoningBudget <= 0 {
		return
	}
	switch providerName {
	case "openrouter":
		enabled := true
		wire.IncludeReasoning = &enabled
		wire.Reasoning = &openRouterReasoning{Enabled: &enabled}
		if req.ReasoningEffort != "" {
			wire.Reasoning.Effort = req.ReasoningEffort
		}
		if req.ReasoningBudget > 0 {
			wire.Reasoning.MaxTokens = req.ReasoningBudget
		}
	case "gemini":
		if req.ReasoningVisibility == "off" {
			return
		}
		level := geminiThinkingLevel(req.ReasoningEffort, req.ReasoningBudget)
		if level == "" {
			return
		}
		wire.ReasoningEffort = ""
		wire.ExtraBody = map[string]any{
			"google": map[string]any{
				"thinking_config": geminiThinkingConfig{
					IncludeThoughts: true,
					ThinkingLevel:   level,
				},
			},
		}
	case "nvidia":
		enabled := reasoningEnabled(req)
		wire.IncludeReasoning = &enabled
		wire.ChatTemplateKwargs = chatTemplateThinkingKwargs(enabled)
	case "litellm":
		enabled := req.ReasoningVisibility != "off"
		wire.IncludeReasoning = &enabled
	case "ollama":
		if think := ollamaThinkValue(req.ReasoningEffort, req.ReasoningBudget); think != nil {
			wire.Think = think
		} else if req.ReasoningVisibility != "off" {
			enabled := true
			wire.Think = enabled
		}
		if effort := ollamaReasoningEffort(req.ReasoningEffort); effort != "" {
			wire.Effort = effort
		}
	case "vllm":
		enabled := reasoningEnabled(req)
		wire.IncludeReasoning = &enabled
		wire.ChatTemplateKwargs = chatTemplateThinkingKwargs(enabled)
		if req.ReasoningBudget > 0 {
			wire.ThinkingTokenBudget = req.ReasoningBudget
		}
	}
}

func reasoningEnabled(req *types.ChatRequest) bool {
	if req == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(req.ReasoningEffort), "none") {
		return false
	}
	visibility := strings.TrimSpace(req.ReasoningVisibility)
	return (visibility != "" && visibility != "off") || req.ReasoningBudget > 0 || strings.TrimSpace(req.ReasoningEffort) != ""
}

func shouldUseOpenAIResponses(req *types.ChatRequest, providerName string) bool {
	if providerName != "openai" || req == nil {
		return false
	}
	return reasoningEnabled(req) || len(req.Tools) > 0
}

func chatTemplateThinkingKwargs(enabled bool) map[string]any {
	return map[string]any{
		"enable_thinking": enabled,
		"thinking":        enabled,
	}
}

func ollamaThinkValue(effort string, budget int) any {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low":
		return "low"
	case "medium":
		return "medium"
	case "high", "xhigh", "max":
		return "high"
	case "none":
		return false
	}
	if budget > 0 {
		return true
	}
	return nil
}

func ollamaReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal":
		return "low"
	case "low", "medium", "high", "max", "none":
		return strings.ToLower(strings.TrimSpace(effort))
	case "xhigh":
		return "high"
	default:
		return ""
	}
}

func geminiThinkingLevel(effort string, budget int) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "minimal", "low", "medium", "high":
		return strings.ToLower(strings.TrimSpace(effort))
	case "xhigh", "max":
		return "high"
	case "none", "":
		// Fall back to budget-derived level when explicit effort is absent.
	default:
		return "medium"
	}
	switch {
	case budget >= 8192:
		return "high"
	case budget >= 4096:
		return "medium"
	case budget >= 2048:
		return "low"
	case budget > 0:
		return "minimal"
	default:
		return ""
	}
}

func sanitizeOpenAIWireMessages(providerName string, messages []types.Message) []types.Message {
	if providerName != "gemini" || len(messages) == 0 {
		return messages
	}
	return sanitizeGeminiReplayMessages(messages)
}

func sanitizeGeminiReplayMessages(messages []types.Message) []types.Message {
	out := make([]types.Message, 0, len(messages))
	for i := 0; i < len(messages); i++ {
		msg := messages[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 || geminiToolReplayHasThoughtSignature(msg.RawContent) {
			out = append(out, msg)
			continue
		}

		pending := make(map[string]string, len(msg.ToolCalls))
		var summary strings.Builder
		base := strings.TrimSpace(msg.Content)
		if base != "" {
			summary.WriteString(base)
		}
		for _, call := range msg.ToolCalls {
			name := strings.TrimSpace(call.Function.Name)
			if name == "" {
				name = "tool"
			}
			pending[strings.TrimSpace(call.ID)] = name
			if summary.Len() > 0 {
				summary.WriteString("\n\n")
			}
			summary.WriteString("[previous tool call replay omitted for Gemini compatibility] ")
			summary.WriteString(name)
			args := strings.TrimSpace(call.Function.Arguments)
			if args != "" {
				summary.WriteString(" args=")
				summary.WriteString(args)
			}
		}

		for i+1 < len(messages) {
			next := messages[i+1]
			if next.Role != "tool" {
				break
			}
			name, ok := pending[strings.TrimSpace(next.ToolCallID)]
			if !ok {
				break
			}
			if summary.Len() > 0 {
				summary.WriteString("\n")
			}
			summary.WriteString("[tool result ")
			summary.WriteString(name)
			summary.WriteString("] ")
			summary.WriteString(strings.TrimSpace(next.Content))
			delete(pending, strings.TrimSpace(next.ToolCallID))
			i++
		}

		out = append(out, types.Message{Role: "assistant", Content: strings.TrimSpace(summary.String())})
	}
	return out
}

func geminiToolReplayHasThoughtSignature(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) || bytes.Equal(trimmed, []byte(`""`)) {
		return false
	}
	var parts []map[string]any
	if err := json.Unmarshal(trimmed, &parts); err != nil {
		return false
	}
	for _, part := range parts {
		if strings.TrimSpace(stringFromAny(part["type"])) != "functionCall" {
			continue
		}
		if strings.TrimSpace(stringFromAny(part["thought_signature"])) != "" {
			return true
		}
	}
	return false
}

func stringFromAny(v any) string {
	s, _ := v.(string)
	return s
}

func emitOpenAIReasoningSummary(ctx context.Context, providerName, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	types.EmitReasoningEvent(ctx, types.ReasoningEvent{
		Kind:     types.ReasoningEventSummary,
		Provider: providerName,
		Text:     text,
	})
}

func extractOpenAIReasoning(body []byte, providerName string) []types.ReasoningBlock {
	var raw struct {
		Choices []struct {
			Message struct {
				Reasoning        string                  `json:"reasoning,omitempty"`
				ReasoningContent string                  `json:"reasoning_content,omitempty"`
				Thinking         string                  `json:"thinking,omitempty"`
				ReasoningDetails []openAIReasoningDetail `json:"reasoning_details,omitempty"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	if len(raw.Choices) == 0 {
		return nil
	}
	return collectOpenAIReasoningBlocks(providerName, raw.Choices[0].Message.ReasoningContent, raw.Choices[0].Message.Thinking, raw.Choices[0].Message.Reasoning, raw.Choices[0].Message.ReasoningDetails)
}

func extractOpenAIResponsesChatResponse(body []byte, providerName string) (*types.ChatResponse, error) {
	var raw openAIResponsesResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}
	created := raw.Created
	if created == 0 {
		created = raw.CreatedAt
	}

	message := types.Message{Role: "assistant"}
	reasoning := make([]types.ReasoningBlock, 0, 1)
	for _, item := range raw.Output {
		switch item.Type {
		case "message":
			texts := extractTextFragments(item.Content)
			if len(texts) == 0 && strings.TrimSpace(item.Text) != "" {
				texts = []string{strings.TrimSpace(item.Text)}
			}
			if len(texts) > 0 {
				if message.Content != "" {
					message.Content += "\n"
				}
				message.Content += strings.Join(texts, "\n")
			}
		case "function_call":
			message.ToolCalls = append(message.ToolCalls, types.ToolCall{
				ID:   strings.TrimSpace(item.CallID),
				Type: "function",
				Function: types.ToolCallFunctionRef{
					Name:      strings.TrimSpace(item.Name),
					Arguments: strings.TrimSpace(item.Arguments),
				},
			})
		case "reasoning":
			texts := extractTextFragments(item.Summary)
			if len(texts) == 0 && strings.TrimSpace(item.Text) != "" {
				texts = []string{strings.TrimSpace(item.Text)}
			}
			for _, text := range texts {
				reasoning = append(reasoning, types.ReasoningBlock{Provider: providerName, Type: "summary", Text: text})
			}
		}
	}
	finishReason := "stop"
	if len(message.ToolCalls) > 0 {
		finishReason = "tool_calls"
	}
	return &types.ChatResponse{
		ID:      raw.ID,
		Object:  raw.Object,
		Created: created,
		Choices: []types.ChatChoice{{
			Index:        0,
			Message:      message,
			FinishReason: finishReason,
		}},
		Usage: types.Usage{
			PromptTokens:     raw.Usage.InputTokens,
			CompletionTokens: raw.Usage.OutputTokens,
			TotalTokens:      raw.Usage.TotalTokens,
		},
		Reasoning: reasoning,
	}, nil
}

func extractTextFragments(raw json.RawMessage) []string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var decoded any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return nil
	}
	var texts []string
	collectTextFragments(decoded, &texts)
	return texts
}

func collectTextFragments(v any, texts *[]string) {
	switch x := v.(type) {
	case string:
		text := strings.TrimSpace(x)
		if text != "" {
			*texts = append(*texts, text)
		}
	case []any:
		for _, item := range x {
			collectTextFragments(item, texts)
		}
	case map[string]any:
		if text, ok := x["text"].(string); ok {
			text = strings.TrimSpace(text)
			if text != "" {
				*texts = append(*texts, text)
			}
		}
		if summary, ok := x["summary"]; ok {
			collectTextFragments(summary, texts)
		}
		if content, ok := x["content"]; ok {
			collectTextFragments(content, texts)
		}
	}
}

type openAIReasoningDetail struct {
	Type      string `json:"type,omitempty"`
	Text      string `json:"text,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Signature string `json:"signature,omitempty"`
	Format    string `json:"format,omitempty"`
}

func collectOpenAIReasoningBlocks(providerName, reasoningContent, thinking, reasoning string, details []openAIReasoningDetail) []types.ReasoningBlock {
	blocks := make([]types.ReasoningBlock, 0, len(details)+1)
	if text := strings.TrimSpace(reasoningContent); text != "" {
		blocks = append(blocks, types.ReasoningBlock{Provider: providerName, Type: "summary", Text: text})
	} else if text := strings.TrimSpace(thinking); text != "" {
		blocks = append(blocks, types.ReasoningBlock{Provider: providerName, Type: "summary", Text: text})
	} else if text := strings.TrimSpace(reasoning); text != "" {
		blocks = append(blocks, types.ReasoningBlock{Provider: providerName, Type: "summary", Text: text})
	}
	for _, detail := range details {
		text := strings.TrimSpace(detail.Text)
		if text == "" {
			text = strings.TrimSpace(detail.Summary)
		}
		if text == "" {
			continue
		}
		kind := strings.TrimSpace(detail.Type)
		if kind == "" {
			kind = "summary"
		}
		blocks = append(blocks, types.ReasoningBlock{Provider: providerName, Type: kind, Text: text})
	}
	if len(blocks) == 0 {
		return nil
	}
	return blocks
}

func collectOpenAIReasoningTexts(reasoningContent, thinking, reasoning string, details []openAIReasoningDetail) []string {
	texts := make([]string, 0, len(details)+1)
	if text := strings.TrimSpace(reasoningContent); text != "" {
		texts = append(texts, text)
	} else if text := strings.TrimSpace(thinking); text != "" {
		texts = append(texts, text)
	} else if text := strings.TrimSpace(reasoning); text != "" {
		texts = append(texts, text)
	}
	for _, detail := range details {
		text := strings.TrimSpace(detail.Text)
		if text == "" {
			text = strings.TrimSpace(detail.Summary)
		}
		if text != "" {
			texts = append(texts, text)
		}
	}
	return texts
}

type openAIProvider struct {
	client      *http.Client
	apiKey      string
	baseURL     string
	model       string
	idleTimeout time.Duration
	hooks       transportHooks
}

func (p *openAIProvider) Capabilities(string) types.ProviderCapabilities {
	return p.hooks.capabilities
}

func (p *openAIProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	if req.Model == "" {
		req.Model = p.model
	}
	req.Stream = false
	if shouldUseOpenAIResponses(req, p.hooks.name) {
		return p.completeResponses(ctx, req)
	}
	return p.completeChatCompletions(ctx, req)
}

func (p *openAIProvider) completeChatCompletions(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	body, err := marshalOpenAIWireRequest(p.hooks.name, req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	p.hooks.applyHeaders(httpReq, p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	var chatResp types.ChatResponse
	if err := json.Unmarshal(responseBody, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	chatResp.Reasoning = extractOpenAIReasoning(responseBody, p.hooks.name)
	if req.ReasoningVisibility != "off" {
		for _, block := range chatResp.Reasoning {
			emitOpenAIReasoningSummary(ctx, p.hooks.name, block.Text)
		}
	}
	return &chatResp, nil
}

func (p *openAIProvider) completeResponses(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	body, err := marshalOpenAIResponsesRequest(req)
	if err != nil {
		return nil, fmt.Errorf("marshal responses request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build responses request: %w", err)
	}
	p.hooks.applyHeaders(httpReq, p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("responses upstream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read responses response: %w", err)
	}
	chatResp, err := extractOpenAIResponsesChatResponse(responseBody, p.hooks.name)
	if err != nil {
		return nil, fmt.Errorf("decode responses response: %w", err)
	}
	if req.ReasoningVisibility != "off" {
		for _, block := range chatResp.Reasoning {
			emitOpenAIReasoningSummary(ctx, p.hooks.name, block.Text)
		}
	}
	return chatResp, nil
}

func (p *openAIProvider) CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	if shouldUseOpenAIResponses(req, p.hooks.name) {
		return p.completeResponsesStream(ctx, req, w)
	}
	return p.completeChatCompletionsStream(ctx, req, w)
}

func (p *openAIProvider) completeChatCompletionsStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	streamCtx, cancel := newStreamContext(ctx)
	defer cancel(nil)
	if req.Model == "" {
		req.Model = p.model
	}
	req.Stream = true

	body, err := marshalOpenAIWireRequest(p.hooks.name, req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost,
		p.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	p.hooks.applyHeaders(httpReq, p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("upstream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	setSSEHeaders(w)
	flusher, _ := w.(http.Flusher)
	touch, stop := startStreamIdleWatchdog(streamCtx, p.idleTimeout, cancel)
	defer stop()

	var (
		sb              strings.Builder
		reasoningBuffer strings.Builder
	)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		touch()
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
			break
		}

		// Accumulate assistant text and optional reasoning text for session storage.
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content          string                  `json:"content,omitempty"`
					Reasoning        string                  `json:"reasoning,omitempty"`
					ReasoningContent string                  `json:"reasoning_content,omitempty"`
					Thinking         string                  `json:"thinking,omitempty"`
					ReasoningDetails []openAIReasoningDetail `json:"reasoning_details,omitempty"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err == nil {
			for _, ch := range chunk.Choices {
				sb.WriteString(ch.Delta.Content)
				for _, reasoningText := range collectOpenAIReasoningTexts(ch.Delta.ReasoningContent, ch.Delta.Thinking, ch.Delta.Reasoning, ch.Delta.ReasoningDetails) {
					reasoningBuffer.WriteString(reasoningText)
					if req.ReasoningVisibility == "full" {
						types.EmitReasoningEvent(streamCtx, types.ReasoningEvent{
							Kind:     types.ReasoningEventDelta,
							Provider: p.hooks.name,
							Text:     reasoningText,
						})
					}
				}
			}
		}

		fmt.Fprintf(w, "data: %s\n\n", payload)
		if flusher != nil {
			flusher.Flush()
		}
	}
	if err := scanner.Err(); err != nil {
		return sb.String(), wrapStreamReadError(streamCtx, err)
	}
	if req.ReasoningVisibility != "off" {
		emitOpenAIReasoningSummary(streamCtx, p.hooks.name, reasoningBuffer.String())
	}
	return sb.String(), nil
}

func (p *openAIProvider) completeResponsesStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	streamCtx, cancel := newStreamContext(ctx)
	defer cancel(nil)
	if req.Model == "" {
		req.Model = p.model
	}
	req.Stream = true

	body, err := marshalOpenAIResponsesRequest(req)
	if err != nil {
		return "", fmt.Errorf("marshal responses request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(streamCtx, http.MethodPost,
		p.baseURL+"/v1/responses", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build responses request: %w", err)
	}
	p.hooks.applyHeaders(httpReq, p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("responses upstream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	setSSEHeaders(w)
	flusher, _ := w.(http.Flusher)
	touch, stop := startStreamIdleWatchdog(streamCtx, p.idleTimeout, cancel)
	defer stop()

	var (
		msgID           string
		created         = time.Now().Unix()
		eventType       string
		sentRole        bool
		sawToolCall     bool
		sb              strings.Builder
		reasoningBuffer strings.Builder
		toolCalls       = map[string]*openAIResponsesStreamToolCall{}
	)
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		touch()
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			break
		}
		if msgID == "" {
			msgID = openAIResponsesStreamID([]byte(payload))
		}
		switch eventType {
		case "response.created", "response.in_progress":
			if !sentRole {
				if err := writeOpenAIChunk(w, flusher, msgID, req.Model, created, "", true, nil); err != nil {
					return sb.String(), err
				}
				sentRole = true
			}
		case "response.output_item.added":
			var ev struct {
				OutputIndex int `json:"output_index"`
				Item        struct {
					ID        string `json:"id"`
					Type      string `json:"type"`
					CallID    string `json:"call_id"`
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"item"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			if ev.Item.Type != "function_call" {
				if !sentRole {
					if err := writeOpenAIChunk(w, flusher, msgID, req.Model, created, "", true, nil); err != nil {
						return sb.String(), err
					}
					sentRole = true
				}
				continue
			}
			key := openAIResponsesToolCallKey(ev.Item.ID, ev.Item.CallID, ev.OutputIndex)
			call := &openAIResponsesStreamToolCall{
				Index:     ev.OutputIndex,
				ID:        strings.TrimSpace(ev.Item.CallID),
				ItemID:    strings.TrimSpace(ev.Item.ID),
				Name:      strings.TrimSpace(ev.Item.Name),
				Arguments: strings.TrimSpace(ev.Item.Arguments),
			}
			toolCalls[key] = call
			sawToolCall = true
			if err := writeOpenAIToolCallChunk(w, flusher, msgID, req.Model, created, call, call.Arguments); err != nil {
				return sb.String(), err
			}
		case "response.function_call_arguments.delta":
			var ev struct {
				ItemID      string `json:"item_id"`
				CallID      string `json:"call_id"`
				OutputIndex int    `json:"output_index"`
				Delta       string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			call := openAIResponsesFindToolCall(toolCalls, ev.ItemID, ev.CallID, ev.OutputIndex)
			if call == nil {
				call = &openAIResponsesStreamToolCall{Index: ev.OutputIndex, ID: strings.TrimSpace(ev.CallID), ItemID: strings.TrimSpace(ev.ItemID)}
				toolCalls[openAIResponsesToolCallKey(call.ItemID, call.ID, call.Index)] = call
			}
			if strings.TrimSpace(ev.Delta) == "" {
				continue
			}
			call.Arguments += ev.Delta
			sawToolCall = true
			if err := writeOpenAIToolCallChunk(w, flusher, msgID, req.Model, created, call, ev.Delta); err != nil {
				return sb.String(), err
			}
		case "response.function_call_arguments.done":
			var ev struct {
				ItemID      string `json:"item_id"`
				CallID      string `json:"call_id"`
				OutputIndex int    `json:"output_index"`
				Arguments   string `json:"arguments"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			call := openAIResponsesFindToolCall(toolCalls, ev.ItemID, ev.CallID, ev.OutputIndex)
			if call != nil && strings.TrimSpace(ev.Arguments) != "" {
				call.Arguments = strings.TrimSpace(ev.Arguments)
			}
		case "response.output_text.delta":
			var ev struct {
				Delta string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			if ev.Delta == "" {
				continue
			}
			if !sentRole {
				if err := writeOpenAIChunk(w, flusher, msgID, req.Model, created, "", true, nil); err != nil {
					return sb.String(), err
				}
				sentRole = true
			}
			sb.WriteString(ev.Delta)
			if err := writeOpenAIChunk(w, flusher, msgID, req.Model, created, ev.Delta, false, nil); err != nil {
				return sb.String(), err
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			var ev struct {
				Delta string `json:"delta"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err != nil {
				continue
			}
			if strings.TrimSpace(ev.Delta) == "" {
				continue
			}
			reasoningBuffer.WriteString(ev.Delta)
			if req.ReasoningVisibility == "full" {
				types.EmitReasoningEvent(streamCtx, types.ReasoningEvent{Kind: types.ReasoningEventDelta, Provider: p.hooks.name, Text: ev.Delta})
			}
		case "response.reasoning_summary_text.done", "response.reasoning_text.done":
			var ev struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal([]byte(payload), &ev); err == nil && strings.TrimSpace(ev.Text) != "" {
				reasoningBuffer.WriteString(ev.Text)
			}
		case "response.completed":
			stopReason := "stop"
			if sawToolCall {
				stopReason = "tool_calls"
			}
			if !sentRole {
				if err := writeOpenAIChunk(w, flusher, msgID, req.Model, created, "", true, nil); err != nil {
					return sb.String(), err
				}
				sentRole = true
			}
			if err := writeOpenAIChunk(w, flusher, msgID, req.Model, created, "", false, &stopReason); err != nil {
				return sb.String(), err
			}
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return sb.String(), wrapStreamReadError(streamCtx, err)
	}
	if req.ReasoningVisibility != "off" {
		emitOpenAIReasoningSummary(streamCtx, p.hooks.name, reasoningBuffer.String())
	}
	return sb.String(), nil
}

type openAIResponsesStreamToolCall struct {
	Index     int
	ID        string
	ItemID    string
	Name      string
	Arguments string
}

func openAIResponsesToolCallKey(itemID, callID string, outputIndex int) string {
	return fmt.Sprintf("%d|%s|%s", outputIndex, strings.TrimSpace(itemID), strings.TrimSpace(callID))
}

func openAIResponsesFindToolCall(calls map[string]*openAIResponsesStreamToolCall, itemID, callID string, outputIndex int) *openAIResponsesStreamToolCall {
	if call := calls[openAIResponsesToolCallKey(itemID, callID, outputIndex)]; call != nil {
		return call
	}
	for _, call := range calls {
		if strings.TrimSpace(itemID) != "" && call.ItemID == strings.TrimSpace(itemID) {
			return call
		}
		if strings.TrimSpace(callID) != "" && call.ID == strings.TrimSpace(callID) {
			return call
		}
	}
	return nil
}

func writeOpenAIToolCallChunk(w http.ResponseWriter, flusher http.Flusher, id, model string, created int64, call *openAIResponsesStreamToolCall, argumentDelta string) error {
	if call == nil {
		return nil
	}
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []map[string]any{{
			"index": 0,
			"delta": map[string]any{
				"tool_calls": []map[string]any{{
					"index": call.Index,
					"id":    call.ID,
					"type":  "function",
					"function": map[string]any{
						"name":      call.Name,
						"arguments": argumentDelta,
					},
				}},
			},
			"finish_reason": nil,
		}},
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("marshal tool-call chunk: %w", err)
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}

func openAIResponsesStreamID(payload []byte) string {
	var generic struct {
		ID       string `json:"id"`
		Response struct {
			ID string `json:"id"`
		} `json:"response"`
	}
	if err := json.Unmarshal(payload, &generic); err != nil {
		return ""
	}
	if strings.TrimSpace(generic.ID) != "" {
		return strings.TrimSpace(generic.ID)
	}
	return strings.TrimSpace(generic.Response.ID)
}

// setSSEHeaders sets the HTTP headers required for a server-sent events response.
func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}
