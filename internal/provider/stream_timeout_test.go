package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/types"
)

func TestOpenAICompleteStreamIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "gpt-4o",
		idleTimeout: 50 * time.Millisecond,
		hooks:       openAICompatibleHooks("openai"),
	}

	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(context.Background(), &types.ChatRequest{}, rec)
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
	if err == nil || !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("expected idle-timeout error, got %v", err)
	}
	if !strings.Contains(err.Error(), "50ms") {
		t.Fatalf("expected timeout duration in error, got %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("expected streamed content to be forwarded, got %q", rec.Body.String())
	}
}

func TestOpenAICompleteEmitsReasoningSummary(t *testing.T) {
	var requestPath string
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_1","object":"response","created_at":1,"output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"inspect markets before answer"}]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "gpt-5",
		idleTimeout: time.Second,
		hooks:       openAICompatibleHooks("openai"),
	}

	var events []types.ReasoningEvent
	ctx := types.WithReasoningSink(context.Background(), func(ev types.ReasoningEvent) {
		events = append(events, ev)
	})
	resp, err := p.Complete(ctx, &types.ChatRequest{
		ReasoningEffort:     "medium",
		ReasoningVisibility: "summary",
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if requestPath != "/v1/responses" {
		t.Fatalf("request path = %q, want /v1/responses", requestPath)
	}
	if len(resp.Reasoning) != 1 || resp.Reasoning[0].Text != "inspect markets before answer" {
		t.Fatalf("unexpected reasoning blocks: %#v", resp.Reasoning)
	}
	if resp.Choices[0].Message.Content != "done" {
		t.Fatalf("message content = %q, want done", resp.Choices[0].Message.Content)
	}
	reasoning, ok := requestBody["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning request body = %#v, want object", requestBody["reasoning"])
	}
	if reasoning["effort"] != "medium" {
		t.Fatalf("reasoning.effort = %#v, want medium", reasoning["effort"])
	}
	if reasoning["summary"] != "auto" {
		t.Fatalf("reasoning.summary = %#v, want auto", reasoning["summary"])
	}
	if len(events) != 1 || events[0].Kind != types.ReasoningEventSummary {
		t.Fatalf("unexpected reasoning events: %#v", events)
	}
}

func TestOpenAICompleteUsesResponsesToolReplay(t *testing.T) {
	var requestPath string
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"resp_tool","object":"response","created_at":1,"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Need fresh balance."}]},{"type":"function_call","call_id":"call_1","name":"monaco.get_balance","arguments":"{\"asset\":\"USDC\"}"}],"usage":{"input_tokens":7,"output_tokens":5,"total_tokens":12}}`)
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "gpt-5",
		idleTimeout: time.Second,
		hooks:       openAICompatibleHooks("openai"),
	}

	resp, err := p.Complete(context.Background(), &types.ChatRequest{
		Tools: []types.Tool{{
			Type:     "function",
			Function: types.ToolFunction{Name: "monaco.get_balance"},
		}},
		Messages: []types.Message{
			{
				Role:    "assistant",
				Content: "Checking prior balance.",
				ToolCalls: []types.ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: types.ToolCallFunctionRef{
						Name:      "monaco.get_balance",
						Arguments: `{"asset":"USDC"}`,
					},
				}},
			},
			{Role: "tool", ToolCallID: "call_1", Content: `{"balance":"12.34"}`},
			{Role: "user", Content: "refresh it"},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if requestPath != "/v1/responses" {
		t.Fatalf("request path = %q, want /v1/responses", requestPath)
	}
	if resp.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("finish reason = %q, want tool_calls", resp.Choices[0].FinishReason)
	}
	if len(resp.Choices[0].Message.ToolCalls) != 1 {
		t.Fatalf("tool call count = %d, want 1", len(resp.Choices[0].Message.ToolCalls))
	}
	if resp.Choices[0].Message.ToolCalls[0].Function.Name != "monaco.get_balance" {
		t.Fatalf("tool name = %q, want monaco.get_balance", resp.Choices[0].Message.ToolCalls[0].Function.Name)
	}
	input, ok := requestBody["input"].([]any)
	if !ok {
		t.Fatalf("input = %#v, want array", requestBody["input"])
	}
	var sawFunctionCall, sawFunctionOutput bool
	for _, item := range input {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		switch entry["type"] {
		case "function_call":
			sawFunctionCall = true
		case "function_call_output":
			sawFunctionOutput = true
		}
	}
	if !sawFunctionCall || !sawFunctionOutput {
		t.Fatalf("responses input missing tool replay items: %#v", input)
	}
}

func TestOpenAICompleteFallsBackToChatCompletionsWithoutReasoningOrTools(t *testing.T) {
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chat_1","object":"chat.completion","created":1,"choices":[{"index":0,"message":{"role":"assistant","content":"plain"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "gpt-4o",
		idleTimeout: time.Second,
		hooks:       openAICompatibleHooks("openai"),
	}

	resp, err := p.Complete(context.Background(), &types.ChatRequest{Messages: []types.Message{{Role: "user", Content: "hello"}}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if requestPath != "/v1/chat/completions" {
		t.Fatalf("request path = %q, want /v1/chat/completions", requestPath)
	}
	if resp.Choices[0].Message.Content != "plain" {
		t.Fatalf("message content = %q, want plain", resp.Choices[0].Message.Content)
	}
}

func TestOpenAICompleteStreamEmitsReasoningDeltaAndSummary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think 1\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "gpt-4o",
		idleTimeout: time.Second,
		hooks:       openAICompatibleHooks("openrouter"),
	}

	var events []types.ReasoningEvent
	ctx := types.WithReasoningSink(context.Background(), func(ev types.ReasoningEvent) {
		events = append(events, ev)
	})
	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(ctx, &types.ChatRequest{ReasoningVisibility: "full"}, rec)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "hello" {
		t.Fatalf("text = %q, want hello", text)
	}
	if len(events) != 2 || events[0].Kind != types.ReasoningEventDelta || events[1].Kind != types.ReasoningEventSummary {
		t.Fatalf("unexpected reasoning events: %#v", events)
	}
}

func TestOpenAICompleteStreamUsesResponsesSSEForReasoning(t *testing.T) {
	var requestPath string
	var requestBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: response.created\n")
		fmt.Fprint(w, "data: {\"response\":{\"id\":\"resp_stream_1\"}}\n\n")
		fmt.Fprint(w, "event: response.reasoning_summary_text.delta\n")
		fmt.Fprint(w, "data: {\"delta\":\"plan first\"}\n\n")
		fmt.Fprint(w, "event: response.output_text.delta\n")
		fmt.Fprint(w, "data: {\"delta\":\"hello\"}\n\n")
		fmt.Fprint(w, "event: response.completed\n")
		fmt.Fprint(w, "data: {\"response\":{\"id\":\"resp_stream_1\"}}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "gpt-5",
		idleTimeout: time.Second,
		hooks:       openAICompatibleHooks("openai"),
	}

	var events []types.ReasoningEvent
	ctx := types.WithReasoningSink(context.Background(), func(ev types.ReasoningEvent) {
		events = append(events, ev)
	})
	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(ctx, &types.ChatRequest{ReasoningEffort: "medium", ReasoningVisibility: "full"}, rec)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if requestPath != "/v1/responses" {
		t.Fatalf("request path = %q, want /v1/responses", requestPath)
	}
	if text != "hello" {
		t.Fatalf("text = %q, want hello", text)
	}
	if got, ok := requestBody["stream"].(bool); !ok || !got {
		t.Fatalf("stream = %#v, want true", requestBody["stream"])
	}
	if len(events) != 2 || events[0].Kind != types.ReasoningEventDelta || events[1].Kind != types.ReasoningEventSummary {
		t.Fatalf("unexpected reasoning events: %#v", events)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "chat.completion.chunk") {
		t.Fatalf("expected translated OpenAI stream chunk, got %q", body)
	}
	if !strings.Contains(body, "hello") || !strings.Contains(body, "[DONE]") {
		t.Fatalf("expected translated content and DONE marker, got %q", body)
	}
}

func TestOpenAICompleteStreamUsesResponsesSSEForToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: response.created\n")
		fmt.Fprint(w, "data: {\"response\":{\"id\":\"resp_stream_tool\"}}\n\n")
		fmt.Fprint(w, "event: response.output_item.added\n")
		fmt.Fprint(w, "data: {\"output_index\":0,\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"monaco.get_balance\",\"arguments\":\"\"}}\n\n")
		fmt.Fprint(w, "event: response.function_call_arguments.delta\n")
		fmt.Fprint(w, "data: {\"item_id\":\"fc_1\",\"call_id\":\"call_1\",\"output_index\":0,\"delta\":\"{\\\"asset\\\":\\\"USDC\\\"}\"}\n\n")
		fmt.Fprint(w, "event: response.completed\n")
		fmt.Fprint(w, "data: {\"response\":{\"id\":\"resp_stream_tool\"}}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := &openAIProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "gpt-5",
		idleTimeout: time.Second,
		hooks:       openAICompatibleHooks("openai"),
	}

	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(context.Background(), &types.ChatRequest{
		Tools: []types.Tool{{Type: "function", Function: types.ToolFunction{Name: "monaco.get_balance"}}},
	}, rec)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "" {
		t.Fatalf("text = %q, want empty", text)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "tool_calls") {
		t.Fatalf("expected tool_calls delta in translated stream, got %q", body)
	}
	if !strings.Contains(body, "monaco.get_balance") || !strings.Contains(body, `{\"asset\":\"USDC\"}`) {
		t.Fatalf("expected translated tool call name and args, got %q", body)
	}
	if !strings.Contains(body, `"finish_reason":"tool_calls"`) {
		t.Fatalf("expected tool_calls finish reason, got %q", body)
	}
}

func TestMarshalOpenAIWireRequestGeminiSanitizesToolTranscriptWithoutRawContent(t *testing.T) {
	messages := []types.Message{
		{
			Role:       "assistant",
			Content:    "Checking balance.",
			RawContent: json.RawMessage(`""`),
			ToolCalls: []types.ToolCall{{
				ID:   "call_123",
				Type: "function",
				Function: types.ToolCallFunctionRef{
					Name:      "monaco.get_balance",
					Arguments: `{"asset":"USDC"}`,
				},
			}},
		},
		{
			Role:       "tool",
			ToolCallID: "call_123",
			Content:    `{"ok":true,"balance":"12.34"}`,
		},
		{
			Role:    "assistant",
			Content: "Fresh Gemini tool call",
			RawContent: json.RawMessage(`[
				{"type":"functionCall","id":"call_live","name":"monaco.get_balance","args":{"asset":"SOL"},"thought_signature":"sig_live"}
			]`),
			ToolCalls: []types.ToolCall{{
				ID:   "call_live",
				Type: "function",
				Function: types.ToolCallFunctionRef{
					Name:      "monaco.get_balance",
					Arguments: `{"asset":"SOL"}`,
				},
			}},
		},
	}

	body, err := marshalOpenAIWireRequest("gemini", &types.ChatRequest{Model: "gemini-2.5-flash", Messages: messages})
	if err != nil {
		t.Fatalf("marshalOpenAIWireRequest: %v", err)
	}

	var wire struct {
		Messages []struct {
			Role      string           `json:"role"`
			Content   any              `json:"content"`
			ToolCalls []types.ToolCall `json:"tool_calls,omitempty"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if len(wire.Messages) != 2 {
		t.Fatalf("wire message count = %d, want 2", len(wire.Messages))
	}
	if wire.Messages[0].Role != "assistant" {
		t.Fatalf("first role = %q, want assistant", wire.Messages[0].Role)
	}
	if _, ok := wire.Messages[0].Content.(string); !ok {
		t.Fatalf("expected sanitized Gemini replay content to become plain text, got %#v", wire.Messages[0].Content)
	}
	if len(wire.Messages[0].ToolCalls) != 0 {
		t.Fatalf("expected sanitized Gemini replay to drop tool_calls, got %#v", wire.Messages[0].ToolCalls)
	}
	if !strings.Contains(wire.Messages[0].Content.(string), "previous tool call replay omitted") {
		t.Fatalf("expected compatibility note in sanitized content, got %q", wire.Messages[0].Content.(string))
	}
	if !strings.Contains(wire.Messages[0].Content.(string), "tool result monaco.get_balance") {
		t.Fatalf("expected tool result summary in sanitized content, got %q", wire.Messages[0].Content.(string))
	}
	contentParts, ok := wire.Messages[1].Content.([]any)
	if !ok {
		t.Fatalf("expected live Gemini tool call to keep raw multipart content, got %#v", wire.Messages[1].Content)
	}
	if len(contentParts) != 1 {
		t.Fatalf("live Gemini content parts = %d, want 1", len(contentParts))
	}
	part, ok := contentParts[0].(map[string]any)
	if !ok {
		t.Fatalf("expected Gemini content part object, got %#v", contentParts[0])
	}
	if part["thought_signature"] != "sig_live" {
		t.Fatalf("expected live Gemini thought_signature to survive, got %#v", part["thought_signature"])
	}
}

func TestMarshalOpenAIWireRequestOpenRouterIncludesReasoning(t *testing.T) {
	body, err := marshalOpenAIWireRequest("openrouter", &types.ChatRequest{
		Model:               "openai/gpt-5",
		Messages:            []types.Message{{Role: "user", Content: "hello"}},
		ReasoningEffort:     "medium",
		ReasoningBudget:     2048,
		ReasoningVisibility: "summary",
	})
	if err != nil {
		t.Fatalf("marshalOpenAIWireRequest: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if got, ok := wire["include_reasoning"].(bool); !ok || !got {
		t.Fatalf("include_reasoning = %#v, want true", wire["include_reasoning"])
	}
	reasoning, ok := wire["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("reasoning = %#v, want object", wire["reasoning"])
	}
	if got := reasoning["effort"]; got != "medium" {
		t.Fatalf("reasoning.effort = %#v, want medium", got)
	}
	if got := reasoning["max_tokens"]; got != float64(2048) {
		t.Fatalf("reasoning.max_tokens = %#v, want 2048", got)
	}
	if got, ok := reasoning["enabled"].(bool); !ok || !got {
		t.Fatalf("reasoning.enabled = %#v, want true", reasoning["enabled"])
	}
}

func TestExtractOpenAIReasoningFromReasoningDetails(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"done","reasoning_details":[{"type":"reasoning.summary","summary":"summary thought"},{"type":"reasoning.text","text":"full thought"}]}}]}`)
	blocks := extractOpenAIReasoning(body, "openrouter")
	if len(blocks) != 2 {
		t.Fatalf("reasoning block count = %d, want 2", len(blocks))
	}
	if blocks[0].Text != "summary thought" || blocks[1].Text != "full thought" {
		t.Fatalf("unexpected reasoning blocks: %#v", blocks)
	}
}

func TestExtractOpenAIReasoningFromThinkingField(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"role":"assistant","content":"done","thinking":"trace thought"}}]}`)
	blocks := extractOpenAIReasoning(body, "ollama")
	if len(blocks) != 1 {
		t.Fatalf("reasoning block count = %d, want 1", len(blocks))
	}
	if blocks[0].Text != "trace thought" {
		t.Fatalf("unexpected reasoning block: %#v", blocks[0])
	}
}

func TestMarshalOpenAIWireRequestGeminiIncludesThinkingConfig(t *testing.T) {
	body, err := marshalOpenAIWireRequest("gemini", &types.ChatRequest{
		Model:               "gemini-3.6-flash",
		Messages:            []types.Message{{Role: "user", Content: "hello"}},
		ReasoningEffort:     "medium",
		ReasoningVisibility: "full",
	})
	if err != nil {
		t.Fatalf("marshalOpenAIWireRequest: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if got := wire["reasoning_effort"]; got != nil {
		t.Fatalf("reasoning_effort = %#v, want omitted when Gemini extra_body thinking_config is used", got)
	}
	extraBody, ok := wire["extra_body"].(map[string]any)
	if !ok {
		t.Fatalf("extra_body = %#v, want object", wire["extra_body"])
	}
	google, ok := extraBody["google"].(map[string]any)
	if !ok {
		t.Fatalf("extra_body.google = %#v, want object", extraBody["google"])
	}
	thinkingConfig, ok := google["thinking_config"].(map[string]any)
	if !ok {
		t.Fatalf("extra_body.google.thinking_config = %#v, want object", google["thinking_config"])
	}
	if got, ok := thinkingConfig["include_thoughts"].(bool); !ok || !got {
		t.Fatalf("include_thoughts = %#v, want true", thinkingConfig["include_thoughts"])
	}
	if got := thinkingConfig["thinking_level"]; got != "medium" {
		t.Fatalf("thinking_level = %#v, want medium", got)
	}
}

func TestMarshalOpenAIWireRequestNVIDIAIncludesChatTemplateKwargs(t *testing.T) {
	body, err := marshalOpenAIWireRequest("nvidia", &types.ChatRequest{
		Model:               "deepseek-v3",
		Messages:            []types.Message{{Role: "user", Content: "hello"}},
		ReasoningEffort:     "medium",
		ReasoningVisibility: "full",
	})
	if err != nil {
		t.Fatalf("marshalOpenAIWireRequest: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if got := wire["include_reasoning"]; got != nil {
		t.Fatalf("include_reasoning = %#v, want omitted for NVIDIA NIM", got)
	}
	kwargs, ok := wire["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatalf("chat_template_kwargs = %#v, want object", wire["chat_template_kwargs"])
	}
	if got, ok := kwargs["enable_thinking"].(bool); !ok || !got {
		t.Fatalf("enable_thinking = %#v, want true", kwargs["enable_thinking"])
	}
	if got, ok := kwargs["thinking"].(bool); !ok || !got {
		t.Fatalf("thinking = %#v, want true", kwargs["thinking"])
	}
}

func TestMarshalOpenAIWireRequestLiteLLMIncludesReasoningVisibility(t *testing.T) {
	body, err := marshalOpenAIWireRequest("litellm", &types.ChatRequest{
		Model:               "openai/gpt-5.4",
		Messages:            []types.Message{{Role: "user", Content: "hello"}},
		ReasoningEffort:     "medium",
		ReasoningVisibility: "full",
	})
	if err != nil {
		t.Fatalf("marshalOpenAIWireRequest: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if got, ok := wire["include_reasoning"].(bool); !ok || !got {
		t.Fatalf("include_reasoning = %#v, want true", wire["include_reasoning"])
	}
	if got := wire["reasoning_effort"]; got != "medium" {
		t.Fatalf("reasoning_effort = %#v, want medium", got)
	}
}

func TestMarshalOpenAIWireRequestOllamaIncludesThinkAndEffort(t *testing.T) {
	body, err := marshalOpenAIWireRequest("ollama", &types.ChatRequest{
		Model:               "qwen3:8b",
		Messages:            []types.Message{{Role: "user", Content: "hello"}},
		ReasoningEffort:     "medium",
		ReasoningVisibility: "full",
	})
	if err != nil {
		t.Fatalf("marshalOpenAIWireRequest: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if got := wire["effort"]; got != "medium" {
		t.Fatalf("effort = %#v, want medium", got)
	}
	if got := wire["think"]; got != "medium" {
		t.Fatalf("think = %#v, want medium", got)
	}
}

func TestMarshalOpenAIWireRequestVLLMIncludesReasoningBudget(t *testing.T) {
	body, err := marshalOpenAIWireRequest("vllm", &types.ChatRequest{
		Model:               "Qwen/Qwen3-8B",
		Messages:            []types.Message{{Role: "user", Content: "hello"}},
		ReasoningEffort:     "medium",
		ReasoningBudget:     2048,
		ReasoningVisibility: "full",
	})
	if err != nil {
		t.Fatalf("marshalOpenAIWireRequest: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode wire request: %v", err)
	}
	if got, ok := wire["include_reasoning"].(bool); !ok || !got {
		t.Fatalf("include_reasoning = %#v, want true", wire["include_reasoning"])
	}
	if got := wire["thinking_token_budget"]; got != float64(2048) {
		t.Fatalf("thinking_token_budget = %#v, want 2048", got)
	}
	kwargs, ok := wire["chat_template_kwargs"].(map[string]any)
	if !ok {
		t.Fatalf("chat_template_kwargs = %#v, want object", wire["chat_template_kwargs"])
	}
	if got, ok := kwargs["enable_thinking"].(bool); !ok || !got {
		t.Fatalf("enable_thinking = %#v, want true", kwargs["enable_thinking"])
	}
	if got, ok := kwargs["thinking"].(bool); !ok || !got {
		t.Fatalf("thinking = %#v, want true", kwargs["thinking"])
	}
}

func TestGeminiToolReplayHasThoughtSignature(t *testing.T) {
	if geminiToolReplayHasThoughtSignature(json.RawMessage(`""`)) {
		t.Fatal("empty string raw content should not count as Gemini functionCall replay")
	}
	if geminiToolReplayHasThoughtSignature(json.RawMessage(`[{"type":"functionCall","id":"call_1","name":"peer.llm_provider.list","args":{},"thought_signature":""}]`)) {
		t.Fatal("blank thought_signature should not count as valid Gemini replay")
	}
	if !geminiToolReplayHasThoughtSignature(json.RawMessage(`[{"type":"functionCall","id":"call_1","name":"peer.llm_provider.list","args":{},"thought_signature":"sig_1"}]`)) {
		t.Fatal("expected Gemini functionCall replay with thought_signature to be preserved")
	}
}

func TestAnthropicCompleteStreamIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, "data: {\"message\":{\"id\":\"msg_1\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, "data: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	p := &anthropicProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "claude-3-7-sonnet",
		idleTimeout: 50 * time.Millisecond,
		hooks:       anthropicHooks(),
	}

	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(context.Background(), &types.ChatRequest{}, rec)
	if text != "hello" {
		t.Fatalf("text = %q", text)
	}
	if err == nil || !errors.Is(err, errStreamIdleTimeout) {
		t.Fatalf("expected idle-timeout error, got %v", err)
	}
	if !strings.Contains(rec.Body.String(), "hello") {
		t.Fatalf("expected streamed content to be forwarded, got %q", rec.Body.String())
	}
}

func TestAnthropicCompleteStreamEmitsReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, "data: {\"message\":{\"id\":\"msg_1\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, "data: {\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"plan step\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, "data: {\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\n")
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, "data: {}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	p := &anthropicProvider{
		client:      server.Client(),
		apiKey:      "test-key",
		baseURL:     server.URL,
		model:       "claude-3-7-sonnet",
		idleTimeout: time.Second,
		hooks:       anthropicHooks(),
	}

	var events []types.ReasoningEvent
	ctx := types.WithReasoningSink(context.Background(), func(ev types.ReasoningEvent) {
		events = append(events, ev)
	})
	rec := httptest.NewRecorder()
	text, err := p.CompleteStream(ctx, &types.ChatRequest{ReasoningBudget: 2048, ReasoningVisibility: "full"}, rec)
	if err != nil {
		t.Fatalf("CompleteStream: %v", err)
	}
	if text != "hello" {
		t.Fatalf("text = %q, want hello", text)
	}
	if len(events) != 2 || events[0].Kind != types.ReasoningEventDelta || events[1].Kind != types.ReasoningEventSummary {
		t.Fatalf("unexpected reasoning events: %#v", events)
	}
}
