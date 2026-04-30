package redact

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"

	"github.com/ffimnsr/koios/internal/types"
)

const placeholder = "[REDACTED]"

var stringRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	{
		re:   regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer|token)\s+)[^\s"']+`),
		repl: `${1}[REDACTED]`,
	},
	{
		re:   regexp.MustCompile(`(?i)(x-api-key\s*[:=]\s*)[^\s"',}]+`),
		repl: `${1}[REDACTED]`,
	},
	{
		re:   regexp.MustCompile(`(?i)("(?:api[_-]?key|access[_-]?token|refresh[_-]?token|secret|password|passwd|token)"\s*:\s*")([^"]+)(")`),
		repl: `${1}[REDACTED]${3}`,
	},
	{
		re:   regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?token|refresh[_-]?token|secret|password|passwd|token)\s*[:=]\s*("?)[^\s"',}]+("?)`),
		repl: `${1}=${2}[REDACTED]${3}`,
	},
	{
		re:   regexp.MustCompile(`\b(?:sk|rk|pk)_[A-Za-z0-9]{16,}\b`),
		repl: placeholder,
	},
	{
		re:   regexp.MustCompile(`\b(?:ghp|github_pat)_[A-Za-z0-9_]{20,}\b`),
		repl: placeholder,
	},
	{
		re:   regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		repl: placeholder,
	},
}

// String removes obvious credentials and tokens from free-form text.
func String(s string) string {
	if s == "" {
		return ""
	}
	out := s
	for _, rule := range stringRules {
		out = rule.re.ReplaceAllString(out, rule.repl)
	}
	return out
}

// Error returns a scrubbed error string.
func Error(err error) string {
	if err == nil {
		return ""
	}
	return String(err.Error())
}

// Message redacts message content while preserving role and structure.
func Message(m types.Message) types.Message {
	m.Content = String(m.Content)
	if len(m.Parts) > 0 {
		parts := make([]types.ContentPart, len(m.Parts))
		for i, p := range m.Parts {
			parts[i] = p
			parts[i].Text = String(p.Text)
			if p.ImageURL != nil {
				img := *p.ImageURL
				img.URL = String(img.URL)
				parts[i].ImageURL = &img
			}
		}
		m.Parts = parts
	}
	if len(m.ToolCalls) > 0 {
		calls := make([]types.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			calls[i] = tc
			calls[i].Function.Arguments = String(tc.Function.Arguments)
		}
		m.ToolCalls = calls
	}
	m.ToolCallID = String(m.ToolCallID)
	return m
}

// Messages returns a redacted copy of msgs.
func Messages(msgs []types.Message) []types.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]types.Message, len(msgs))
	for i, m := range msgs {
		out[i] = Message(m)
	}
	return out
}

// Map returns a redacted deep copy of m where string-like leaf values are scrubbed.
func Map(m map[string]any) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = valueWithHint(k, v)
	}
	return out
}

// Value redacts common dynamic values used in hook payloads and persisted traces.
func Value(v any) any {
	return valueWithHint("", v)
}

func valueWithHint(key string, v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		if isSensitiveKey(key) {
			return placeholder
		}
		return String(t)
	case error:
		return Error(t)
	case []byte:
		if isSensitiveKey(key) {
			return []byte(placeholder)
		}
		return []byte(String(string(t)))
	case json.RawMessage:
		return scrubRawJSON(t)
	case map[string]any:
		return Map(t)
	case []any:
		out := make([]any, len(t))
		for i := range t {
			out[i] = valueWithHint(key, t[i])
		}
		return out
	case types.Message:
		return Message(t)
	case []types.Message:
		return Messages(t)
	case fmt.Stringer:
		if isSensitiveKey(key) {
			return placeholder
		}
		return String(t.String())
	}
	return reflectValue(reflect.ValueOf(v))
}

func isSensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	key = strings.Trim(key, `"'`)
	return key == "api_key" || key == "apikey" || key == "x-api-key" || key == "access_token" || key == "refresh_token" || key == "secret" || key == "password" || key == "passwd" || key == "token"
}

func scrubRawJSON(raw json.RawMessage) json.RawMessage {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return raw
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return json.RawMessage([]byte(String(trimmed)))
	}
	clean := Value(decoded)
	encoded, err := json.Marshal(clean)
	if err != nil {
		return json.RawMessage([]byte(String(trimmed)))
	}
	return json.RawMessage(encoded)
}

func reflectValue(v reflect.Value) any {
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return reflectValue(v.Elem())
	case reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return Value(v.Elem().Interface())
	case reflect.Slice, reflect.Array:
		out := make([]any, v.Len())
		for i := 0; i < v.Len(); i++ {
			out[i] = Value(v.Index(i).Interface())
		}
		return out
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return v.Interface()
		}
		out := make(map[string]any, v.Len())
		iter := v.MapRange()
		for iter.Next() {
			out[iter.Key().String()] = Value(iter.Value().Interface())
		}
		return out
	case reflect.Struct:
		return v.Interface()
	case reflect.String:
		return String(v.String())
	default:
		return v.Interface()
	}
}

// Handler is an slog.Handler wrapper that scrubs sensitive values from log
// records before forwarding them to the inner handler.
type Handler struct {
	inner slog.Handler
}

// NewHandler returns an slog.Handler that redacts sensitive attribute values
// before passing log records to inner.
func NewHandler(inner slog.Handler) *Handler {
	return &Handler{inner: inner}
}

func (h *Handler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle scrubs each attribute value and the message before forwarding.
func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	clean := slog.NewRecord(r.Time, r.Level, String(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(scrubAttr(a))
		return true
	})
	return h.inner.Handle(ctx, clean)
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	scrubbed := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		scrubbed[i] = scrubAttr(a)
	}
	return &Handler{inner: h.inner.WithAttrs(scrubbed)}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name)}
}

func scrubAttr(a slog.Attr) slog.Attr {
	switch a.Value.Kind() {
	case slog.KindString:
		if isSensitiveKey(a.Key) {
			return slog.String(a.Key, placeholder)
		}
		return slog.String(a.Key, String(a.Value.String()))
	case slog.KindGroup:
		grp := a.Value.Group()
		scrubbed := make([]slog.Attr, len(grp))
		for i, ga := range grp {
			scrubbed[i] = scrubAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(scrubbed...)}
	case slog.KindAny:
		return slog.Attr{Key: a.Key, Value: slog.AnyValue(Value(a.Value.Any()))}
	default:
		return a
	}
}
