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

// anthropicVersion is the minimum Anthropic API version we target.
const anthropicVersion = "2023-06-01"

// defaultMaxTokens is used when the caller does not specify max_tokens.
// Anthropic requires this field; OpenAI makes it optional.
const defaultMaxTokens = 4096

type anthropicProvider struct {
	client  *http.Client
	apiKey  string
	baseURL string
	model   string
}

// — Anthropic request/response types ——————————————————————————————————————————

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	Messages  []anthropicMessage `json:"messages"`
	System    string             `json:"system,omitempty"`
	Tools     []anthropicTool    `json:"tools,omitempty"`
	Stream    bool               `json:"stream,omitempty"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type anthropicResponse struct {
	ID      string                  `json:"id"`
	Type    string                  `json:"type"`
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
	Model   string                  `json:"model"`
	Usage   anthropicUsage          `json:"usage"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
}

type anthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// — Format conversion —————————————————————————————————————————————————————————

// toAnthropicRequest converts an OpenAI-format ChatRequest to an Anthropic
// messages request. System messages are extracted into the top-level system
// field; the remaining messages are passed in the messages array.
func toAnthropicRequest(req *types.ChatRequest, model string) anthropicRequest {
	var systemParts []string
	var msgs []anthropicMessage
	var tools []anthropicTool

	for _, m := range req.Messages {
		if m.Role == "system" {
			systemParts = append(systemParts, m.Content)
		} else if m.Role == "tool" {
			msgs = append(msgs, anthropicMessage{
				Role: "user",
				Content: []anthropicContentBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolCallID,
					Text:      m.Content,
				}},
			})
		} else if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			blocks := make([]anthropicContentBlock, 0, len(m.ToolCalls)+1)
			if m.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: json.RawMessage(tc.Function.Arguments),
				})
			}
			msgs = append(msgs, anthropicMessage{Role: m.Role, Content: blocks})
		} else {
			msgs = append(msgs, anthropicMessage{Role: m.Role, Content: m.Content})
		}
	}
	for _, tool := range req.Tools {
		tools = append(tools, anthropicTool{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			InputSchema: tool.Function.Parameters,
		})
	}

	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultMaxTokens
	}

	return anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  msgs,
		System:    strings.Join(systemParts, "\n\n"),
		Tools:     tools,
		Stream:    req.Stream,
	}
}

// toOpenAIResponse converts an Anthropic response to OpenAI format.
func toOpenAIResponse(ar *anthropicResponse) *types.ChatResponse {
	var content strings.Builder
	var toolCalls []types.ToolCall
	for _, block := range ar.Content {
		if block.Type == "text" {
			content.WriteString(block.Text)
		}
		if block.Type == "tool_use" {
			toolCalls = append(toolCalls, types.ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: types.ToolCallFunctionRef{
					Name:      block.Name,
					Arguments: string(block.Input),
				},
			})
		}
	}

	return &types.ChatResponse{
		ID:      ar.ID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Choices: []types.ChatChoice{
			{
				Index:        0,
				Message:      types.Message{Role: "assistant", Content: content.String(), ToolCalls: toolCalls},
				FinishReason: "stop",
			},
		},
		Usage: types.Usage{
			PromptTokens:     ar.Usage.InputTokens,
			CompletionTokens: ar.Usage.OutputTokens,
			TotalTokens:      ar.Usage.InputTokens + ar.Usage.OutputTokens,
		},
	}
}

// — Provider methods ——————————————————————————————————————————————————————————

func (p *anthropicProvider) Complete(ctx context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
	ar := toAnthropicRequest(req, p.model)
	ar.Stream = false

	body, err := json.Marshal(ar)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	p.setHeaders(httpReq)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("upstream request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("upstream %d: %s", resp.StatusCode, strings.TrimSpace(string(snippet)))
	}

	var aResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&aResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return toOpenAIResponse(&aResp), nil
}

func (p *anthropicProvider) CompleteStream(ctx context.Context, req *types.ChatRequest, w http.ResponseWriter) (string, error) {
	ar := toAnthropicRequest(req, p.model)
	ar.Stream = true

	body, err := json.Marshal(ar)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	p.setHeaders(httpReq)

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

	// Convert Anthropic SSE events to OpenAI SSE format in real-time.
	var (
		msgID     string
		created   = time.Now().Unix()
		eventType string
		sb        strings.Builder
	)

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()

		// SSE event lines: "event: <type>" or "data: <json>"
		if strings.HasPrefix(line, "event: ") {
			eventType = strings.TrimPrefix(line, "event: ")
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")

		switch eventType {
		case "message_start":
			// Extract the message ID emitted by Anthropic.
			var ms struct {
				Message struct {
					ID string `json:"id"`
				} `json:"message"`
			}
			if err := json.Unmarshal([]byte(data), &ms); err == nil {
				msgID = ms.Message.ID
			}
			// Send the initial role delta (matches OpenAI behaviour).
			if err := writeOpenAIChunk(w, flusher, msgID, p.model, created, "", true, nil); err != nil {
				return sb.String(), err
			}

		case "content_block_delta":
			var cbd struct {
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			}
			if err := json.Unmarshal([]byte(data), &cbd); err != nil {
				continue
			}
			if cbd.Delta.Type != "text_delta" {
				continue
			}
			sb.WriteString(cbd.Delta.Text)
			if err := writeOpenAIChunk(w, flusher, msgID, p.model, created, cbd.Delta.Text, false, nil); err != nil {
				return sb.String(), err
			}

		case "message_delta":
			stopReason := "stop"
			if err := writeOpenAIChunk(w, flusher, msgID, p.model, created, "", false, &stopReason); err != nil {
				return sb.String(), err
			}

		case "message_stop":
			fmt.Fprintf(w, "data: [DONE]\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return sb.String(), fmt.Errorf("reading stream: %w", err)
	}
	return sb.String(), nil
}

// setHeaders applies the Anthropic-specific authentication and version headers.
func (p *anthropicProvider) setHeaders(r *http.Request) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("x-api-key", p.apiKey)
	r.Header.Set("anthropic-version", anthropicVersion)
}

// writeOpenAIChunk serialises a StreamChunk in the OpenAI SSE wire format and
// flushes it to the client. sendRole=true emits the initial role:"assistant"
// delta; finishReason non-nil emits the closing chunk.
func writeOpenAIChunk(w http.ResponseWriter, flusher http.Flusher, id, model string, created int64, text string, sendRole bool, finishReason *string) error {
	delta := types.StreamDelta{Content: text}
	if sendRole {
		delta.Role = "assistant"
		delta.Content = ""
	}

	chunk := types.StreamChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []types.StreamChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
	}

	b, err := json.Marshal(chunk)
	if err != nil {
		return fmt.Errorf("marshal chunk: %w", err)
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}
