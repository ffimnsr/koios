package redact

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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

func TestHandler_ScrubsStringAttributes(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewHandler(inner)
	logger := slog.New(h)

	logger.Info("test event",
		slog.String("password", "super-secret"),
		slog.String("token", "sk_live_abc1234567890xyz"),
		slog.String("user", "alice"),
		slog.String("msg_body", "Authorization: Bearer ghp_abcdefghijklmnopqrstu"),
	)

	out := buf.String()
	if strings.Contains(out, "super-secret") {
		t.Errorf("password value should be redacted, got: %s", out)
	}
	if strings.Contains(out, "sk_live_abc1234567890xyz") {
		t.Errorf("token value should be redacted, got: %s", out)
	}
	if strings.Contains(out, "ghp_") {
		t.Errorf("bearer token in msg_body should be redacted, got: %s", out)
	}
	if !strings.Contains(out, "alice") {
		t.Errorf("non-sensitive field should be preserved, got: %s", out)
	}
}

func TestHandler_ScrubsMessage(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewHandler(inner)
	logger := slog.New(h)

	logger.Info("connecting with token=sk_live_abcdefghijklmno")

	out := buf.String()
	if strings.Contains(out, "sk_live_abcdefghijklmno") {
		t.Errorf("secret in message should be redacted, got: %s", out)
	}
}

func TestHandler_ScrubsGroupAttributes(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewHandler(inner)
	logger := slog.New(h)

	logger.Info("grouped",
		slog.Group("creds",
			slog.String("api_key", "AKIAIOSFODNN7EXAMPLE"),
			slog.String("region", "us-east-1"),
		),
	)

	out := buf.String()
	if strings.Contains(out, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("api_key inside group should be redacted, got: %s", out)
	}
	if !strings.Contains(out, "us-east-1") {
		t.Errorf("non-sensitive group field should be preserved, got: %s", out)
	}
}

func TestHandler_WithAttrs_Scrubs(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	h := NewHandler(inner)
	logger := slog.New(h).With(slog.String("secret", "my-top-secret"))

	logger.Info("event")

	out := buf.String()
	if strings.Contains(out, "my-top-secret") {
		t.Errorf("WithAttrs secret should be redacted, got: %s", out)
	}
}

func TestHandler_Enabled_Delegates(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := NewHandler(inner)

	if h.Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug should not be enabled when inner level is warn")
	}
	if !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Error("warn should be enabled when inner level is warn")
	}
}
