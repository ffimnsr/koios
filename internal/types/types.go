// Package types defines the OpenAI-compatible request and response structures
// used throughout the service. All providers translate to/from these types.
package types

import "encoding/json"

// ContentPart is a single element in a multipart message used for multimodal
// inputs (text and images).
type ContentPart struct {
	Type     string        `json:"type"`                // "text" or "image_url"
	Text     string        `json:"text,omitempty"`      // for type="text"
	ImageURL *ImageURLPart `json:"image_url,omitempty"` // for type="image_url"
}

// ImageURLPart carries an image as a URL or base64 data URI.
type ImageURLPart struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"` // "auto", "low", "high"
}

// Message is a single turn in a conversation.
// When Parts is non-empty the message carries multimodal content and Parts
// takes precedence over Content during JSON marshaling.
type Message struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	Parts      []ContentPart `json:"-"` // multimodal content; overrides Content when non-empty
	ToolCallID string        `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
}

// MarshalJSON serialises a Message. When Parts is non-empty the content field
// is emitted as a JSON array of content parts (multimodal format); otherwise
// it is emitted as a plain string (standard format).
func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Parts) > 0 {
		type multipart struct {
			Role       string        `json:"role"`
			Content    []ContentPart `json:"content"`
			ToolCallID string        `json:"tool_call_id,omitempty"`
			ToolCalls  []ToolCall    `json:"tool_calls,omitempty"`
		}
		return json.Marshal(multipart{
			Role:       m.Role,
			Content:    m.Parts,
			ToolCallID: m.ToolCallID,
			ToolCalls:  m.ToolCalls,
		})
	}
	type plain struct {
		Role       string     `json:"role"`
		Content    string     `json:"content"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	}
	return json.Marshal(plain{
		Role:       m.Role,
		Content:    m.Content,
		ToolCallID: m.ToolCallID,
		ToolCalls:  m.ToolCalls,
	})
}

// UnmarshalJSON deserialises a Message, handling both string and array content.
func (m *Message) UnmarshalJSON(data []byte) error {
	// Use a raw struct to capture the content field as a raw JSON value.
	var raw struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id,omitempty"`
		ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Role = raw.Role
	m.ToolCallID = raw.ToolCallID
	m.ToolCalls = raw.ToolCalls
	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		return nil
	}
	// Try string first.
	var s string
	if err := json.Unmarshal(raw.Content, &s); err == nil {
		m.Content = s
		return nil
	}
	// Fall back to array of content parts.
	var parts []ContentPart
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return err
	}
	m.Parts = parts
	// Populate Content with concatenated text parts for backward-compat code
	// that still reads m.Content.
	for _, p := range parts {
		if p.Type == "text" {
			m.Content += p.Text
		}
	}
	return nil
}

// ChatRequest is the OpenAI-compatible /v1/chat/completions request body.
type ChatRequest struct {
	Model            string          `json:"model"` // set by the handler; ignored if supplied by the caller
	Messages         []Message       `json:"messages"`
	Tools            []Tool          `json:"tools,omitempty"`
	ToolChoice       any             `json:"tool_choice,omitempty"`
	Stream           bool            `json:"stream"`
	MaxTokens        int             `json:"max_tokens,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	Stop             json.RawMessage `json:"stop,omitempty"`
	User             string          `json:"user,omitempty"`
}

// ChatChoice is one completion candidate.
type ChatChoice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Tool is an OpenAI-compatible tool definition.
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction describes a callable tool.
type ToolFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

// ToolCall is a tool invocation emitted by the model.
type ToolCall struct {
	ID       string              `json:"id,omitempty"`
	Type     string              `json:"type,omitempty"`
	Function ToolCallFunctionRef `json:"function"`
}

// ToolCallFunctionRef identifies the tool name and serialized arguments.
type ToolCallFunctionRef struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
}

// Usage reports token consumption for a request.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatResponse is the OpenAI-compatible /v1/chat/completions response body.
type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Choices []ChatChoice `json:"choices"`
	Usage   Usage        `json:"usage"`
}

// StreamDelta carries incremental content in a streaming chunk.
type StreamDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// StreamChoice is one candidate in a streaming response chunk.
type StreamChoice struct {
	Index        int         `json:"index"`
	Delta        StreamDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

// StreamChunk is one SSE data payload in a streaming response.
type StreamChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []StreamChoice `json:"choices"`
}
