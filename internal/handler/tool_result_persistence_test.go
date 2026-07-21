package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/artifacts"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/toolresults"
)

func TestRecordToolResultStoresFullPayloadInArtifactWhenTruncated(t *testing.T) {
	store := session.New(10)
	artifactStore, err := artifacts.New(t.TempDir() + "/artifacts.db")
	if err != nil {
		t.Fatalf("artifacts.New: %v", err)
	}
	defer artifactStore.Close()
	toolResultStore, err := toolresults.New(t.TempDir() + "/tool_results.db")
	if err != nil {
		t.Fatalf("toolresults.New: %v", err)
	}
	defer toolResultStore.Close()
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:           "test-model",
		ArtifactStore:   artifactStore,
		ToolResultStore: toolResultStore,
	})

	result := map[string]any{"payload": strings.Repeat("A", 12000)}
	call := agent.ToolCall{ID: "tc-1", Name: "workspace.read", Arguments: json.RawMessage(`{"path":"README.md"}`)}
	h.recordToolResult(context.Background(), "alice", call, agent.ToolRunContext{SessionKey: "alice", ActiveProfile: "default"}, result, nil, 12)

	records, err := toolResultStore.List(context.Background(), "alice", toolresults.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("toolResultStore.List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one tool result record, got %d", len(records))
	}
	rec := records[0]
	if !rec.Provenance.ResultTruncated {
		t.Fatalf("expected truncated provenance, got %#v", rec.Provenance)
	}
	if rec.Provenance.FullResultArtifact == "" {
		t.Fatalf("expected full result artifact pointer, got %#v", rec.Provenance)
	}
	if len(rec.ResultJSON) >= len(strings.Repeat("A", 12000)) {
		t.Fatalf("expected inline result json to be truncated, got %d chars", len(rec.ResultJSON))
	}
	artifact, err := artifactStore.Get(context.Background(), "alice", rec.Provenance.FullResultArtifact)
	if err != nil {
		t.Fatalf("artifactStore.Get: %v", err)
	}
	if artifact.Kind != "tool_result" {
		t.Fatalf("unexpected artifact kind: %#v", artifact)
	}
	if !strings.Contains(artifact.Content, strings.Repeat("A", 4000)) {
		t.Fatalf("expected artifact content to include full payload, got %q", artifact.Content)
	}
}

func TestRecordToolResultKeepsFullPayloadInlineWithoutArtifactStore(t *testing.T) {
	store := session.New(10)
	toolResultStore, err := toolresults.New(t.TempDir() + "/tool_results.db")
	if err != nil {
		t.Fatalf("toolresults.New: %v", err)
	}
	defer toolResultStore.Close()
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:           "test-model",
		ToolResultStore: toolResultStore,
	})

	result := map[string]any{"payload": strings.Repeat("B", 12000)}
	call := agent.ToolCall{ID: "tc-2", Name: "workspace.read", Arguments: json.RawMessage(`{"path":"README.md"}`)}
	h.recordToolResult(context.Background(), "alice", call, agent.ToolRunContext{SessionKey: "alice"}, result, nil, 8)

	records, err := toolResultStore.List(context.Background(), "alice", toolresults.Filter{Limit: 10})
	if err != nil {
		t.Fatalf("toolResultStore.List: %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("expected one tool result record, got %d", len(records))
	}
	rec := records[0]
	if rec.Provenance.ResultTruncated {
		t.Fatalf("did not expect truncation without artifact store, got %#v", rec.Provenance)
	}
	if rec.Provenance.FullResultArtifact != "" {
		t.Fatalf("did not expect artifact pointer without artifact store, got %#v", rec.Provenance)
	}
	if !strings.Contains(rec.ResultJSON, strings.Repeat("B", 4000)) {
		t.Fatalf("expected full inline payload, got %q", rec.ResultJSON)
	}
}
