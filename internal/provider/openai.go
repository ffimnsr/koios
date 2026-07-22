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
	Model            string          `json:"model"`
	Messages         []types.Message `json:"messages"`
	Tools            []types.Tool    `json:"tools,omitempty"`
	ToolChoice       any             `json:"tool_choice,omitempty"`
	Stream           bool            `json:"stream"`
	MaxTokens        int             `json:"max_tokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	Stop             json.RawMessage `json:"stop,omitempty"`
	User             string          `json:"user,omitempty"`
	ReasoningEffort  string          `json:"reasoning_effort,omitempty"`
}

func marshalOpenAIWireRequest(providerName string, req *types.ChatRequest) ([]byte, error) {
	return json.Marshal(openAIWireRequest{
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
	})
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
				Reasoning        string `json:"reasoning,omitempty"`
				ReasoningContent string `json:"reasoning_content,omitempty"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil
	}
	if len(raw.Choices) == 0 {
		return nil
	}
	text := strings.TrimSpace(raw.Choices[0].Message.ReasoningContent)
	if text == "" {
		text = strings.TrimSpace(raw.Choices[0].Message.Reasoning)
	}
	if text == "" {
		return nil
	}
	return []types.ReasoningBlock{{Provider: providerName, Type: "summary", Text: text}}
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
	if req.ReasoningVisibility != "off" && len(chatResp.Reasoning) > 0 {
		emitOpenAIReasoningSummary(ctx, p.hooks.name, chatResp.Reasoning[0].Text)
	}
	return &chatResp, nil
}

func (p *openAIProvider) CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
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
					Content          string `json:"content,omitempty"`
					Reasoning        string `json:"reasoning,omitempty"`
					ReasoningContent string `json:"reasoning_content,omitempty"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err == nil {
			for _, ch := range chunk.Choices {
				sb.WriteString(ch.Delta.Content)
				reasoningText := ch.Delta.ReasoningContent
				if reasoningText == "" {
					reasoningText = ch.Delta.Reasoning
				}
				if strings.TrimSpace(reasoningText) != "" {
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

// setSSEHeaders sets the HTTP headers required for a server-sent events response.
func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
}
