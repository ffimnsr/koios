package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/types"
)

// newStreamContractHandler builds a handler wired to the given provider with a
// fresh store and coordinator, ready for WS dials.
func newStreamContractHandler(t *testing.T, prov *stubProvider) *httptest.Server {
	t.Helper()
	store := session.New(10)
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// TestWS_ChatBufferedTurnEmitsFinalDeltaAndStreamedFalse pins the buffered-run
// output contract: a turn that streams nothing emits one final stream.delta
// (final: true) and the reply carries streamed: false, so clients can render
// reply.assistant_text (or the final delta) exactly once.
func TestWS_ChatBufferedTurnEmitsFinalDeltaAndStreamedFalse(t *testing.T) {
	var calls int
	prov := &stubProvider{
		complete: func(_ context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			calls++
			if calls == 1 {
				return &types.ChatResponse{Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: `<tool_call>{"name":"time.now","arguments":{}}</tool_call>`},
				}}}, nil
			}
			return &types.ChatResponse{Choices: []types.ChatChoice{{
				Message: types.Message{Role: "assistant", Content: "Buffered."},
			}}}, nil
		},
	}
	srv := newStreamContractHandler(t, prov)

	conn := dialWS(t, srv, "contract-buffered")
	sendRPC(t, conn, "c1", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "hi"}},
		"stream":   true,
	})
	frames := readFramesUntilID(t, conn, "c1")

	wantID, _ := json.Marshal("c1")
	var (
		sawFinalDelta bool
		reply         struct {
			AssistantText string `json:"assistant_text"`
			Done          bool   `json:"done"`
			Streamed      *bool  `json:"streamed"`
		}
		replied bool
	)
	for _, frame := range frames {
		if frame.Method == "stream.delta" {
			var payload struct {
				Content string `json:"content"`
				Final   *bool  `json:"final"`
			}
			if err := json.Unmarshal(frame.Params, &payload); err != nil {
				t.Fatalf("unmarshal delta: %v", err)
			}
			if payload.Content == "Buffered." && payload.Final != nil && *payload.Final {
				sawFinalDelta = true
			}
			continue
		}
		if string(frame.ID) != string(wantID) {
			continue
		}
		if err := json.Unmarshal(frame.Result, &reply); err != nil {
			t.Fatalf("unmarshal reply: %v", err)
		}
		replied = true
	}
	if !sawFinalDelta {
		t.Fatal("expected a final stream.delta for the buffered turn")
	}
	if !replied || reply.AssistantText != "Buffered." || !reply.Done {
		t.Fatalf("unexpected buffered reply: %#v replied=%v", reply, replied)
	}
	if reply.Streamed == nil || *reply.Streamed {
		t.Fatalf("expected streamed=false on the buffered reply, got %#v", reply)
	}
}

// TestWS_ChatLiveTurnMarksStreamedTrue pins the live-run output contract: when
// deltas streamed, the reply carries streamed: true and the buffered final
// delta is not emitted.
func TestWS_ChatLiveTurnMarksStreamedTrue(t *testing.T) {
	prov := &stubProvider{
		caps: types.ProviderCapabilities{SupportsNativeTools: true},
		stream: func(_ context.Context, _ *types.ChatRequest, w http.ResponseWriter) (string, error) {
			_, _ = w.Write([]byte(sseChunk("Hello ")))
			_, _ = w.Write([]byte(sseChunk("world")))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return "Hello world", nil
		},
	}
	srv := newStreamContractHandler(t, prov)

	conn := dialWS(t, srv, "contract-live")
	sendRPC(t, conn, "c2", "chat", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "say hi"}},
		"stream":   true,
	})
	frames := readFramesUntilID(t, conn, "c2")

	wantID, _ := json.Marshal("c2")
	var (
		sawFinalDelta bool
		reply         struct {
			AssistantText string `json:"assistant_text"`
			Streamed      *bool  `json:"streamed"`
		}
	)
	for _, frame := range frames {
		if frame.Method == "stream.delta" {
			var payload struct {
				Content string `json:"content"`
				Final   *bool  `json:"final"`
			}
			_ = json.Unmarshal(frame.Params, &payload)
			if payload.Final != nil && *payload.Final {
				sawFinalDelta = true
			}
			continue
		}
		if string(frame.ID) == string(wantID) {
			_ = json.Unmarshal(frame.Result, &reply)
		}
	}
	if sawFinalDelta {
		t.Fatal("expected no final delta when deltas streamed live")
	}
	if reply.Streamed == nil || !*reply.Streamed {
		t.Fatalf("expected streamed=true on the live reply, got %#v", reply)
	}
	if reply.AssistantText != "Hello world" {
		t.Fatalf("unexpected live reply text %q", reply.AssistantText)
	}
}

// TestWS_AgentRunStreamReplyCarriesRunIDAndStreamed pins the agent.run stream
// reply shape: it mirrors the non-stream shape (run_id + result) and reports
// streamed so callers can correlate progress deltas with the final result.
func TestWS_AgentRunStreamReplyCarriesRunIDAndStreamed(t *testing.T) {
	prov := &stubProvider{
		caps: types.ProviderCapabilities{SupportsNativeTools: true},
		stream: func(_ context.Context, _ *types.ChatRequest, w http.ResponseWriter) (string, error) {
			_, _ = w.Write([]byte(sseChunk("streamed run")))
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
			return "streamed run", nil
		},
	}
	srv := newStreamContractHandler(t, prov)

	conn := dialWS(t, srv, "contract-agentrun")
	sendRPC(t, conn, "c3", "agent.run", map[string]any{
		"messages": []types.Message{{Role: "user", Content: "run something"}},
		"stream":   true,
	})
	frames := readFramesUntilID(t, conn, "c3")

	wantID, _ := json.Marshal("c3")
	var reply struct {
		RunID    string          `json:"run_id"`
		Streamed *bool           `json:"streamed"`
		Result   json.RawMessage `json:"result"`
	}
	for _, frame := range frames {
		if string(frame.ID) == string(wantID) {
			if err := json.Unmarshal(frame.Result, &reply); err != nil {
				t.Fatalf("unmarshal agent.run reply: %v", err)
			}
		}
	}
	if reply.RunID == "" {
		t.Fatal("expected run_id on the agent.run stream reply")
	}
	if reply.Streamed == nil || !*reply.Streamed {
		t.Fatalf("expected streamed=true on the agent.run stream reply, got %#v", reply)
	}
	if len(reply.Result) == 0 {
		t.Fatal("expected the agent result in the agent.run stream reply")
	}
}
