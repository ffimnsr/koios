package redact

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/types"
)

func TestString_RedactsCommonSecrets(t *testing.T) {
	in := `Authorization: Bearer sk_secret1234567890 password="supersecret" x-api-key=abcdef123456`
	out := String(in)
	if strings.Contains(out, "sk_secret1234567890") || strings.Contains(out, "supersecret") || strings.Contains(out, "abcdef123456") {
		t.Fatalf("expected secrets to be redacted, got %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %q", out)
	}
}

func TestMessage_RedactsContentAndToolArguments(t *testing.T) {
	msg := Message(types.Message{
		Role:    "assistant",
		Content: `token=ghp_abcdefghijklmnopqrstuvwxyz`,
		ToolCalls: []types.ToolCall{{
			Function: types.ToolCallFunctionRef{Name: "web_fetch", Arguments: `{"password":"open-sesame"}`},
		}},
	})
	if strings.Contains(msg.Content, "ghp_") {
		t.Fatalf("expected message content to be redacted, got %q", msg.Content)
	}
	if strings.Contains(msg.ToolCalls[0].Function.Arguments, "open-sesame") {
		t.Fatalf("expected tool arguments to be redacted, got %q", msg.ToolCalls[0].Function.Arguments)
	}
	if !strings.Contains(msg.ToolCalls[0].Function.Arguments, "[REDACTED]") {
		t.Fatalf("expected redaction marker in tool arguments, got %q", msg.ToolCalls[0].Function.Arguments)
	}
	part := Message(types.Message{Role: "user", Parts: []types.ContentPart{{Type: "text", Text: `api_key=AKIAABCDEFGHIJKLMNOP`}}})
	if strings.Contains(part.Parts[0].Text, "AKIAABCDEFGHIJKLMNOP") {
		t.Fatalf("expected part text to be redacted, got %#v", part.Parts)
	}
}

func TestValue_RedactsRawJSON(t *testing.T) {
	raw := json.RawMessage(`{"token":"sk_secret1234567890","nested":{"password":"hidden"}}`)
	out := Value(raw).(json.RawMessage)
	text := string(out)
	if strings.Contains(text, "sk_secret1234567890") || strings.Contains(text, "hidden") {
		t.Fatalf("expected raw JSON to be redacted, got %s", text)
	}
	if !strings.Contains(text, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %s", text)
	}
}
