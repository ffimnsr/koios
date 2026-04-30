package handler_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/calendar"
	"github.com/ffimnsr/koios/internal/channels"
	"github.com/ffimnsr/koios/internal/handler"
	"github.com/ffimnsr/koios/internal/memory"
	"github.com/ffimnsr/koios/internal/runledger"
	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
	"github.com/ffimnsr/koios/internal/subagent"
	"github.com/ffimnsr/koios/internal/tasks"
	"github.com/ffimnsr/koios/internal/types"
	"github.com/ffimnsr/koios/internal/workspace"
)

type stubOutboundChannel struct {
	id          string
	lastTarget  channels.OutboundTarget
	lastMessage channels.OutboundMessage
	receipt     *channels.OutboundReceipt
	err         error
}

func (s *stubOutboundChannel) ID() string { return s.id }

func (s *stubOutboundChannel) Routes() []channels.Route { return nil }

func (s *stubOutboundChannel) Start(context.Context) error { return nil }

func (s *stubOutboundChannel) Shutdown(context.Context) error { return nil }

func (s *stubOutboundChannel) SendMessage(_ context.Context, target channels.OutboundTarget, msg channels.OutboundMessage) (*channels.OutboundReceipt, error) {
	s.lastTarget = target
	s.lastMessage = msg
	if s.err != nil {
		return nil, s.err
	}
	if s.receipt != nil {
		return s.receipt, nil
	}
	return &channels.OutboundReceipt{Channel: s.id, SubjectID: target.SubjectID, ConversationID: target.ConversationID, ThreadID: target.ThreadID, ChunkCount: 1, Deliveries: []channels.OutboundDelivery{{MessageID: "msg-1", ConversationID: target.ConversationID, ThreadID: target.ThreadID, ChunkIndex: 1}}}, nil
}

func TestToolDefinitionsHonorPolicy(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		MemStore:       memStore,
		ToolPolicy: handler.ToolPolicy{
			Allow: []string{"read", "memory.get", "time.now"},
			Deny:  []string{"memory.get"},
		},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "read") {
		t.Fatalf("expected read tool, got %#v", names)
	}
	if containsString(names, "memory.get") {
		t.Fatalf("expected memory.get to be denied, got %#v", names)
	}
	if containsString(names, "exec") {
		t.Fatalf("expected exec to be excluded by allow list, got %#v", names)
	}
}

func TestToolPromptRequiresExactFullToolNames(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:     "test-model",
		Timeout:   5 * time.Second,
		MemStore:  memStore,
		TaskStore: taskStore,
		ToolPolicy: handler.ToolPolicy{
			Profile: "coding",
		},
	})

	prompt := h.ToolPrompt("alice")
	for _, want := range []string{
		"ALWAYS use the EXACT FULL tool name",
		"task.create",
		"memory.insert",
		"domain.operation",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("expected prompt to contain %q, got %q", want, prompt)
		}
	}
}

func TestExecuteToolRejectsBareToolName(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:     "test-model",
		Timeout:   5 * time.Second,
		TaskStore: taskStore,
		ToolPolicy: handler.ToolPolicy{
			Profile: "coding",
		},
	})

	// Bare tool names (without domain prefix) are no longer inferred;
	// the dispatcher must reject them as unknown.
	_, err = h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "create",
		Arguments: json.RawMessage(`{"title":"Book venue","details":"Confirm with ops","owner":"alice"}`),
	})
	if err == nil {
		t.Fatal("expected error for bare tool name 'create', got nil")
	}
}

func TestExecuteToolInfersBareMemoryInsertAlias(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:    "test-model",
		Timeout:  5 * time.Second,
		MemStore: memStore,
		ToolPolicy: handler.ToolPolicy{
			Profile: "coding",
		},
	})

	// Unambiguous bare names (unique suffix match in NormalizeToolName) still resolve.
	insertedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "insert",
		Arguments: json.RawMessage(`{"content":"Remember margin trading ships today","category":"tasks","tags":["deadline"]}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(insert->memory.insert): %v", err)
	}
	insertedRaw, _ := json.Marshal(insertedAny)
	var inserted struct {
		OK    bool `json:"ok"`
		Chunk struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"chunk"`
	}
	if err := json.Unmarshal(insertedRaw, &inserted); err != nil {
		t.Fatalf("unmarshal insert alias result: %v", err)
	}
	if !inserted.OK || inserted.Chunk.ID == "" || inserted.Chunk.Content != "Remember margin trading ships today" {
		t.Fatalf("unexpected insert alias result: %#v", insertedAny)
	}
}

func TestToolDefinitionsHonorCodeExecutionPolicy(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		CodeExecutionConfig: handler.CodeExecutionConfig{
			Enabled: true,
		},
		ToolPolicy: handler.ToolPolicy{
			Allow: []string{"code_execution", "read"},
			Deny:  []string{"exec"},
		},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "code_execution") {
		t.Fatalf("expected code_execution to be exposed, got %#v", names)
	}
	if containsString(names, "exec") {
		t.Fatalf("expected exec to remain denied, got %#v", names)
	}
}

func TestToolDefinitionsRuntimeProfileIncludesCodeExecutionHelpers(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	ledger, err := runledger.New(t.TempDir())
	if err != nil {
		t.Fatalf("runledger.New: %v", err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		RunLedger:      ledger,
		CodeExecutionConfig: handler.CodeExecutionConfig{
			Enabled: true,
		},
		ToolPolicy: handler.ToolPolicy{Profile: "coding"},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "code_execution.status") || !containsString(names, "code_execution.cancel") {
		t.Fatalf("expected code_execution helper tools, got %#v", names)
	}
}

func TestToolDefinitionsIncludeApplyPatch(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "apply_patch") {
		t.Fatalf("expected apply_patch tool, got %#v", names)
	}
	if !containsString(names, "grep") || !containsString(names, "workspace.grep") {
		t.Fatalf("expected grep tools, got %#v", names)
	}
	if !containsString(names, "find") || !containsString(names, "workspace.find") {
		t.Fatalf("expected find tools, got %#v", names)
	}
	if !containsString(names, "head") || !containsString(names, "workspace.head") || !containsString(names, "tail") || !containsString(names, "workspace.tail") {
		t.Fatalf("expected head/tail tools, got %#v", names)
	}
	if !containsString(names, "sort") || !containsString(names, "workspace.sort") || !containsString(names, "uniq") || !containsString(names, "workspace.uniq") {
		t.Fatalf("expected sort/uniq tools, got %#v", names)
	}
	if !containsString(names, "diff") || !containsString(names, "workspace.diff") {
		t.Fatalf("expected diff tools, got %#v", names)
	}
}

func TestToolDefinitionsMessagingProfileIncludesWaitingTools(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	channelMgr := channels.NewManager(&stubOutboundChannel{id: "telegram"})
	bindingStore := channels.NewBindingStore(filepath.Join(t.TempDir(), "bindings.json"))
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:               "test-model",
		Timeout:             5 * time.Second,
		MemStore:            memStore,
		TaskStore:           taskStore,
		ChannelManager:      channelMgr,
		ChannelBindingStore: bindingStore,
		ToolPolicy: handler.ToolPolicy{
			Profile: "messaging",
		},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if !containsString(names, "waiting.create") || !containsString(names, "waiting.list") || !containsString(names, "waiting.resolve") {
		t.Fatalf("expected waiting tools in messaging profile, got %#v", names)
	}
	if !containsString(names, "message") {
		t.Fatalf("expected message tool in messaging profile, got %#v", names)
	}
	for _, want := range []string{"message.send", "inbox.list", "inbox.read", "inbox.mark_read", "inbox.route", "inbox.summarize"} {
		if !containsString(names, want) {
			t.Fatalf("expected %s in messaging profile, got %#v", want, names)
		}
	}
	for _, want := range []string{"contact.list", "contact.resolve", "contact.alias", "contact.link_channel_identity"} {
		if !containsString(names, want) {
			t.Fatalf("expected %s in messaging profile, got %#v", want, names)
		}
	}
	if containsString(names, "exec") {
		t.Fatalf("expected exec to remain excluded from messaging profile, got %#v", names)
	}
}

func TestExecuteTool_Message(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	sender := &stubOutboundChannel{
		id: "telegram",
		receipt: &channels.OutboundReceipt{
			Channel:        "telegram",
			SubjectID:      "7",
			ConversationID: "123",
			ChunkCount:     1,
			UsedBinding:    true,
			Deliveries:     []channels.OutboundDelivery{{MessageID: "tg-501", ConversationID: "123", ChunkIndex: 1}},
		},
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		ChannelManager: channels.NewManager(sender),
	})

	resultAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "message",
		Arguments: json.RawMessage(`{"channel":"telegram","subject_id":"7","text":"Build finished successfully"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(message): %v", err)
	}
	if sender.lastTarget.SubjectID != "7" || sender.lastMessage.Text != "Build finished successfully" {
		t.Fatalf("unexpected outbound send payload: target=%#v message=%#v", sender.lastTarget, sender.lastMessage)
	}
	if sender.lastMessage.Metadata["source_peer_id"] != "alice" {
		t.Fatalf("expected source_peer_id metadata, got %#v", sender.lastMessage.Metadata)
	}
	raw, _ := json.Marshal(resultAny)
	var result struct {
		OK             bool     `json:"ok"`
		Channel        string   `json:"channel"`
		SubjectID      string   `json:"subject_id"`
		ConversationID string   `json:"conversation_id"`
		ChunkCount     int      `json:"chunk_count"`
		UsedBinding    bool     `json:"used_binding"`
		DeliveryCount  int      `json:"delivery_count"`
		MessageIDs     []string `json:"message_ids"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal message result: %v", err)
	}
	if !result.OK || result.Channel != "telegram" || result.SubjectID != "7" || result.ConversationID != "123" || result.ChunkCount != 1 || !result.UsedBinding || result.DeliveryCount != 1 || len(result.MessageIDs) != 1 || result.MessageIDs[0] != "tg-501" {
		t.Fatalf("unexpected message result: %#v", result)
	}
}

func TestExecuteTool_MessageSendAlias(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	sender := &stubOutboundChannel{id: "telegram"}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		ChannelManager: channels.NewManager(sender),
	})

	resultAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "message.send",
		Arguments: json.RawMessage(`{"channel":"telegram","subject_id":"7","text":"hello"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(message.send): %v", err)
	}
	if sender.lastMessage.Text != "hello" {
		t.Fatalf("unexpected outbound payload: %#v", sender.lastMessage)
	}
	raw, _ := json.Marshal(resultAny)
	if !strings.Contains(string(raw), `"ok":true`) {
		t.Fatalf("expected ok result, got %s", raw)
	}
}

func TestExecuteTool_InboxTools(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{response: &types.ChatResponse{}}
	bindingStore := channels.NewBindingStore(filepath.Join(t.TempDir(), "bindings.json"))
	pending, err := bindingStore.EnsurePending(channels.BindingRequest{
		Channel:        "telegram",
		SubjectID:      "7",
		ConversationID: "123",
		DisplayName:    "Alice Sender",
	})
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	if _, err := bindingStore.ApproveCodeWithRoute(pending.Code, "operator", channels.BindingRoute{PeerID: "alice"}); err != nil {
		t.Fatalf("ApproveCodeWithRoute: %v", err)
	}
	sessionKey := "alice::channel::telegram:123"
	store.Append(sessionKey,
		types.Message{Role: "user", Content: "Need an ETA on the build."},
		types.Message{Role: "assistant", Content: "I am checking now."},
		types.Message{Role: "user", Content: "Please route this to ops."},
	)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:               "test-model",
		Timeout:             5 * time.Second,
		ChannelBindingStore: bindingStore,
		ToolPolicy:          handler.ToolPolicy{Profile: "messaging"},
	})

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "inbox.list",
		Arguments: json.RawMessage(`{"channel":"telegram"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(inbox.list): %v", err)
	}
	listRaw, _ := json.Marshal(listAny)
	if !strings.Contains(string(listRaw), sessionKey) || !strings.Contains(string(listRaw), `"unread_count":2`) {
		t.Fatalf("unexpected inbox.list result: %s", listRaw)
	}

	readAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "inbox.read",
		Arguments: json.RawMessage(`{"session_key":"` + sessionKey + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(inbox.read): %v", err)
	}
	readRaw, _ := json.Marshal(readAny)
	if !strings.Contains(string(readRaw), `"unread_count":2`) {
		t.Fatalf("unexpected inbox.read result: %s", readRaw)
	}

	markAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "inbox.mark_read",
		Arguments: json.RawMessage(`{"session_key":"` + sessionKey + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(inbox.mark_read): %v", err)
	}
	markRaw, _ := json.Marshal(markAny)
	if !strings.Contains(string(markRaw), `"remaining_unread":0`) {
		t.Fatalf("unexpected inbox.mark_read result: %s", markRaw)
	}

	summaryAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "inbox.summarize",
		Arguments: json.RawMessage(`{"session_key":"` + sessionKey + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(inbox.summarize): %v", err)
	}
	summaryRaw, _ := json.Marshal(summaryAny)
	if !strings.Contains(string(summaryRaw), `"summary":`) || !strings.Contains(string(summaryRaw), `"unread_count":0`) {
		t.Fatalf("unexpected inbox.summarize result: %s", summaryRaw)
	}

	if _, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "inbox.route",
		Arguments: json.RawMessage(`{"channel":"telegram","subject_id":"7","session_key":"alice::sender::ops"}`),
	}); err != nil {
		t.Fatalf("ExecuteTool(inbox.route): %v", err)
	}
	approved, err := bindingStore.ApprovedBinding("telegram", "7")
	if err != nil {
		t.Fatalf("ApprovedBinding: %v", err)
	}
	if approved == nil || approved.SessionKey != "alice::sender::ops" || approved.PeerID != "alice" {
		t.Fatalf("unexpected routed binding: %#v", approved)
	}
}

func TestExecuteTool_ContactTools(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{response: &types.ChatResponse{}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	t.Cleanup(func() { _ = memStore.Close() })
	bindingStore := channels.NewBindingStore(filepath.Join(t.TempDir(), "bindings.json"))
	pending, err := bindingStore.EnsurePending(channels.BindingRequest{
		Channel:        "telegram",
		SubjectID:      "7",
		ConversationID: "123",
		DisplayName:    "Alice Sender",
	})
	if err != nil {
		t.Fatalf("EnsurePending: %v", err)
	}
	if _, err := bindingStore.ApproveCodeWithRoute(pending.Code, "operator", channels.BindingRoute{PeerID: "alice"}); err != nil {
		t.Fatalf("ApproveCodeWithRoute: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:               "test-model",
		Timeout:             5 * time.Second,
		MemStore:            memStore,
		ChannelBindingStore: bindingStore,
		ToolPolicy:          handler.ToolPolicy{Profile: "messaging"},
	})

	createAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "memory.entity.create",
		Arguments: json.RawMessage(`{"kind":"person","name":"Alice Example"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(memory.entity.create): %v", err)
	}
	createRaw, _ := json.Marshal(createAny)
	var createRes struct {
		Entity struct {
			ID string `json:"id"`
		} `json:"entity"`
	}
	if err := json.Unmarshal(createRaw, &createRes); err != nil {
		t.Fatalf("unmarshal create result: %v", err)
	}

	aliasAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "contact.alias",
		Arguments: json.RawMessage(`{"id":"` + createRes.Entity.ID + `","aliases":["Ali","Alice from Ops"]}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(contact.alias): %v", err)
	}
	aliasRaw, _ := json.Marshal(aliasAny)
	if !strings.Contains(string(aliasRaw), `"Alice from Ops"`) {
		t.Fatalf("unexpected contact.alias result: %s", aliasRaw)
	}

	linkAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "contact.link_channel_identity",
		Arguments: json.RawMessage(`{"id":"` + createRes.Entity.ID + `","channel":"telegram","subject_id":"7"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(contact.link_channel_identity): %v", err)
	}
	linkRaw, _ := json.Marshal(linkAny)
	if !strings.Contains(string(linkRaw), `"contact_id":"`+createRes.Entity.ID+`"`) {
		t.Fatalf("unexpected contact.link_channel_identity result: %s", linkRaw)
	}

	resolveAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "contact.resolve",
		Arguments: json.RawMessage(`{"channel":"telegram","subject_id":"7"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(contact.resolve): %v", err)
	}
	resolveRaw, _ := json.Marshal(resolveAny)
	if !strings.Contains(string(resolveRaw), `"resolved":true`) || !strings.Contains(string(resolveRaw), `"subject_id":"7"`) {
		t.Fatalf("unexpected contact.resolve result: %s", resolveRaw)
	}

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "contact.list",
		Arguments: json.RawMessage(`{"q":"Alice"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(contact.list): %v", err)
	}
	listRaw, _ := json.Marshal(listAny)
	if !strings.Contains(string(listRaw), `"count":1`) || !strings.Contains(string(listRaw), `"Alice Example"`) {
		t.Fatalf("unexpected contact.list result: %s", listRaw)
	}

	approved, err := bindingStore.ApprovedBinding("telegram", "7")
	if err != nil {
		t.Fatalf("ApprovedBinding: %v", err)
	}
	if approved == nil || approved.Metadata["contact_id"] != createRes.Entity.ID {
		t.Fatalf("unexpected approved binding metadata: %#v", approved)
	}
}

func TestToolDefinitionsHonorActiveStandingProfile(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	standingStore, err := standing.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("standing.NewStore: %v", err)
	}
	if _, err := standingStore.SaveDocument(&standing.Document{
		PeerID: "alice",
		Profiles: map[string]standing.Profile{
			"focus": {ToolProfile: "minimal", ToolsAllow: []string{"time.now"}},
		},
	}); err != nil {
		t.Fatalf("SaveDocument: %v", err)
	}
	if err := store.SetPolicy("alice", session.SessionPolicy{ActiveProfile: "focus"}); err != nil {
		t.Fatalf("SetPolicy: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:           "test-model",
		Timeout:         5 * time.Second,
		StandingManager: standing.NewManager(standingStore, ""),
		ToolPolicy:      handler.ToolPolicy{Profile: "coding"},
	})
	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	if len(names) != 1 || !containsString(names, "time.now") {
		t.Fatalf("expected standing profile to resolve to minimal tools, got %#v", names)
	}
	if containsString(names, "exec") {
		t.Fatalf("expected exec to be removed by active profile, got %#v", names)
	}
}

func TestWSStandingProfileLifecycle(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	standingStore, err := standing.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("standing.NewStore: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:           "test-model",
		Timeout:         5 * time.Second,
		StandingManager: standing.NewManager(standingStore, ""),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	conn := dialWS(t, srv, "alice")

	sendRPC(t, conn, "p1", "standing.profile.set", map[string]any{
		"name":           "focus",
		"content":        "Focus mode instructions",
		"tool_profile":   "minimal",
		"response_style": "Be terse.",
		"make_default":   true,
	})
	if msg := readUntilID(t, conn, "p1"); msg.Error != nil {
		t.Fatalf("standing.profile.set error: %#v", msg.Error)
	}

	sendRPC(t, conn, "p2", "standing.profile.activate", map[string]any{"name": "focus"})
	if msg := readUntilID(t, conn, "p2"); msg.Error != nil {
		t.Fatalf("standing.profile.activate error: %#v", msg.Error)
	}
	if policy := store.Policy("alice"); policy.ActiveProfile != "focus" {
		t.Fatalf("expected active profile focus, got %#v", policy)
	}

	sendRPC(t, conn, "p3", "standing.get", map[string]any{})
	msg := readUntilID(t, conn, "p3")
	if msg.Error != nil {
		t.Fatalf("standing.get error: %#v", msg.Error)
	}
	var result struct {
		ActiveProfile  string                      `json:"active_profile"`
		DefaultProfile string                      `json:"default_profile"`
		Effective      string                      `json:"effective_content"`
		Profiles       map[string]standing.Profile `json:"profiles"`
	}
	if err := json.Unmarshal(msg.Result, &result); err != nil {
		t.Fatalf("unmarshal standing.get: %v", err)
	}
	if result.ActiveProfile != "focus" || result.DefaultProfile != "focus" {
		t.Fatalf("unexpected standing profile result: %#v", result)
	}
	if !strings.Contains(result.Effective, "Focus mode instructions") {
		t.Fatalf("expected effective standing content to include profile, got %q", result.Effective)
	}
	if _, ok := result.Profiles["focus"]; !ok {
		t.Fatalf("expected focus profile in standing.get, got %#v", result.Profiles)
	}

	sendRPC(t, conn, "p4", "standing.profile.delete", map[string]any{"name": "focus"})
	if msg := readUntilID(t, conn, "p4"); msg.Error != nil {
		t.Fatalf("standing.profile.delete error: %#v", msg.Error)
	}
}

func TestExecuteToolWaitingLifecycle(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	t.Cleanup(func() { _ = taskStore.Close() })
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:     "test-model",
		Timeout:   5 * time.Second,
		TaskStore: taskStore,
		ToolPolicy: handler.ToolPolicy{
			Allow: []string{"waiting.create", "waiting.list", "waiting.resolve"},
		},
	})

	createdAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "waiting.create",
		Arguments: json.RawMessage(`{"title":"Need vendor answer","waiting_for":"vendor","follow_up_at":1735689600}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(waiting.create): %v", err)
	}
	raw, _ := json.Marshal(createdAny)
	var created struct {
		OK      bool            `json:"ok"`
		Waiting tasks.WaitingOn `json:"waiting"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("unmarshal waiting.create result: %v", err)
	}
	if !created.OK || created.Waiting.ID == "" {
		t.Fatalf("unexpected waiting.create result: %#v", created)
	}

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "waiting.list",
		Arguments: json.RawMessage(`{"status":"open","limit":10}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(waiting.list): %v", err)
	}
	raw, _ = json.Marshal(listAny)
	var listed struct {
		Count   int               `json:"count"`
		Waiting []tasks.WaitingOn `json:"waiting"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("unmarshal waiting.list result: %v", err)
	}
	if listed.Count != 1 || len(listed.Waiting) != 1 {
		t.Fatalf("unexpected waiting.list result: %#v", listed)
	}

	resolvedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "waiting.resolve",
		Arguments: json.RawMessage(fmt.Sprintf(`{"id":%q}`, created.Waiting.ID)),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(waiting.resolve): %v", err)
	}
	raw, _ = json.Marshal(resolvedAny)
	var resolved struct {
		OK      bool            `json:"ok"`
		Waiting tasks.WaitingOn `json:"waiting"`
	}
	if err := json.Unmarshal(raw, &resolved); err != nil {
		t.Fatalf("unmarshal waiting.resolve result: %v", err)
	}
	if !resolved.OK || resolved.Waiting.Status != tasks.WaitingStatusResolved {
		t.Fatalf("unexpected waiting.resolve result: %#v", resolved)
	}
}

func TestExecuteToolCalendarAgenda(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	calendarStore, err := calendar.New(filepath.Join(t.TempDir(), "calendar.db"))
	if err != nil {
		t.Fatalf("calendar.New: %v", err)
	}
	t.Cleanup(func() { _ = calendarStore.Close() })
	icsPath := filepath.Join(t.TempDir(), "calendar.ics")
	ics := "BEGIN:VCALENDAR\nVERSION:2.0\nBEGIN:VEVENT\nUID:evt-1\nSUMMARY:Focus block\nDTSTART:20260424T130000Z\nDTEND:20260424T140000Z\nEND:VEVENT\nEND:VCALENDAR\n"
	if err := os.WriteFile(icsPath, []byte(ics), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := calendarStore.CreateSource(context.Background(), "alice", calendar.SourceInput{Name: "Work", Path: icsPath}); err != nil {
		t.Fatalf("CreateSource: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:         "test-model",
		Timeout:       5 * time.Second,
		CalendarStore: calendarStore,
		ToolPolicy: handler.ToolPolicy{
			Allow: []string{"calendar.agenda"},
		},
	})

	resultAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "calendar.agenda",
		Arguments: json.RawMessage(`{"scope":"today","timezone":"UTC","now":1777017600}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(calendar.agenda): %v", err)
	}
	raw, _ := json.Marshal(resultAny)
	var result struct {
		Agenda struct {
			Count  int `json:"count"`
			Events []struct {
				Summary string `json:"summary"`
			} `json:"events"`
		} `json:"agenda"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal calendar.agenda result: %v", err)
	}
	if result.Agenda.Count != 1 || len(result.Agenda.Events) != 1 || result.Agenda.Events[0].Summary != "Focus block" {
		t.Fatalf("unexpected calendar.agenda result: %#v", result)
	}
}

func TestExecuteTool_TextOpsAliases(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	if _, err := wsStore.Write("alice", "notes/list.txt", "delta\nbeta\nbeta\nAlpha\n", false); err != nil {
		t.Fatalf("workspace write setup: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})

	headAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "head",
		Arguments: json.RawMessage(`{"path":"notes/list.txt","lines":2}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(head): %v", err)
	}
	sortAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "sort",
		Arguments: json.RawMessage(`{"path":"notes/list.txt","case_sensitive":false}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(sort): %v", err)
	}
	uniqAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "uniq",
		Arguments: json.RawMessage(`{"path":"notes/list.txt","count":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(uniq): %v", err)
	}
	tailAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "tail",
		Arguments: json.RawMessage(`{"path":"notes/list.txt","lines":2}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(tail): %v", err)
	}

	var headGot struct {
		Content string `json:"content"`
	}
	var sortGot struct {
		Content string `json:"content"`
	}
	var uniqGot struct {
		Content string `json:"content"`
	}
	var tailGot struct {
		Content string `json:"content"`
	}
	headRaw, _ := json.Marshal(headAny)
	sortRaw, _ := json.Marshal(sortAny)
	uniqRaw, _ := json.Marshal(uniqAny)
	tailRaw, _ := json.Marshal(tailAny)
	if err := json.Unmarshal(headRaw, &headGot); err != nil {
		t.Fatalf("unmarshal head result: %v", err)
	}
	if err := json.Unmarshal(sortRaw, &sortGot); err != nil {
		t.Fatalf("unmarshal sort result: %v", err)
	}
	if err := json.Unmarshal(uniqRaw, &uniqGot); err != nil {
		t.Fatalf("unmarshal uniq result: %v", err)
	}
	if err := json.Unmarshal(tailRaw, &tailGot); err != nil {
		t.Fatalf("unmarshal tail result: %v", err)
	}
	if headGot.Content != "delta\nbeta\n" {
		t.Fatalf("unexpected head result: %#v", headGot)
	}
	if sortGot.Content != "Alpha\nbeta\nbeta\ndelta\n" {
		t.Fatalf("unexpected sort result: %#v", sortGot)
	}
	if uniqGot.Content != "1 delta\n2 beta\n1 Alpha\n" {
		t.Fatalf("unexpected uniq result: %#v", uniqGot)
	}
	if tailGot.Content != "beta\nAlpha\n" {
		t.Fatalf("unexpected tail result: %#v", tailGot)
	}
}

func TestExecuteTool_GrepAlias(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	if _, err := wsStore.Write("alice", "notes/todo.md", "TODO one\nskip\nTODO two\n", false); err != nil {
		t.Fatalf("workspace write setup: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "grep",
		Arguments: json.RawMessage(`{"path":"notes","pattern":"TODO","recursive":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(grep): %v", err)
	}
	raw, _ := json.Marshal(result)
	var got struct {
		Count   int `json:"count"`
		Matches []struct {
			Path string `json:"path"`
			Line int    `json:"line"`
		} `json:"matches"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal grep result: %v", err)
	}
	if got.Count != 2 || len(got.Matches) != 2 {
		t.Fatalf("unexpected grep result: %#v", got)
	}
}

func TestExecuteTool_DiffAlias(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	if _, err := wsStore.Write("alice", "notes/todo.md", "one\ntwo\n", false); err != nil {
		t.Fatalf("workspace write setup: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
	})

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "diff",
		Arguments: json.RawMessage(`{"path":"notes/todo.md","content":"one\nTWO\n"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(diff): %v", err)
	}
	raw, _ := json.Marshal(result)
	var got struct {
		HasDiff bool   `json:"has_diff"`
		Diff    string `json:"diff"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal diff result: %v", err)
	}
	if !got.HasDiff || !strings.Contains(got.Diff, "-two") || !strings.Contains(got.Diff, "+TWO") {
		t.Fatalf("unexpected diff result: %#v", got)
	}
}

func TestExecuteTool_SystemRunUsesApprovalPolicy(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	wsStore, err := workspace.New(t.TempDir(), true, 1024)
	if err != nil {
		t.Fatalf("workspace.New: %v", err)
	}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:          "test-model",
		Timeout:        5 * time.Second,
		WorkspaceStore: wsStore,
		ExecConfig: handler.ExecConfig{
			Enabled:            true,
			EnableDenyPatterns: true,
			ApprovalMode:       "always",
		},
	})
	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "system.run",
		Arguments: json.RawMessage(`{"command":"echo hi"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(system.run): %v", err)
	}
	var got struct {
		Status string `json:"status"`
	}
	raw, _ := json.Marshal(result)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal system.run result: %v", err)
	}
	if got.Status != "approval_required" {
		t.Fatalf("expected approval_required, got %#v", result)
	}
}

func TestExecuteTool_TaskTools(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	defer taskStore.Close()
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:     "test-model",
		Timeout:   5 * time.Second,
		TaskStore: taskStore,
		ToolPolicy: handler.ToolPolicy{
			Profile: "coding",
		},
	})

	names := []string{}
	for _, tool := range h.ToolDefinitions("alice") {
		names = append(names, tool.Function.Name)
	}
	for _, want := range []string{"task.create", "task.extract", "task.list", "task.update", "task.complete", "approval.request"} {
		if !contains(names, want) {
			t.Fatalf("expected tool definition %q, got %#v", want, names)
		}
	}

	createdAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "task.create",
		Arguments: json.RawMessage(`{"title":"Book venue","details":"Confirm with ops","owner":"alice"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(task.create): %v", err)
	}
	createdRaw, _ := json.Marshal(createdAny)
	var created struct {
		Task struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Owner  string `json:"owner"`
		} `json:"task"`
	}
	if err := json.Unmarshal(createdRaw, &created); err != nil {
		t.Fatalf("unmarshal task.create result: %v", err)
	}
	if created.Task.ID == "" || created.Task.Status != string(tasks.TaskStatusOpen) || created.Task.Owner != "alice" {
		t.Fatalf("unexpected task.create result: %#v", createdAny)
	}

	updatedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "task.update",
		Arguments: json.RawMessage(`{"id":"` + created.Task.ID + `","owner":"finance"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(task.update): %v", err)
	}
	updatedRaw, _ := json.Marshal(updatedAny)
	var updated struct {
		Task struct {
			Owner string `json:"owner"`
		} `json:"task"`
	}
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatalf("unmarshal task.update result: %v", err)
	}
	if updated.Task.Owner != "finance" {
		t.Fatalf("expected updated owner finance, got %#v", updatedAny)
	}

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "task.list",
		Arguments: json.RawMessage(`{"status":"open"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(task.list): %v", err)
	}
	listRaw, _ := json.Marshal(listAny)
	var listed struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(listRaw, &listed); err != nil {
		t.Fatalf("unmarshal task.list result: %v", err)
	}
	if listed.Count != 1 {
		t.Fatalf("expected one open task, got %#v", listAny)
	}

	completedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "task.complete",
		Arguments: json.RawMessage(`{"id":"` + created.Task.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(task.complete): %v", err)
	}
	completedRaw, _ := json.Marshal(completedAny)
	var completed struct {
		Task struct {
			Status string `json:"status"`
		} `json:"task"`
	}
	if err := json.Unmarshal(completedRaw, &completed); err != nil {
		t.Fatalf("unmarshal task.complete result: %v", err)
	}
	if completed.Task.Status != string(tasks.TaskStatusCompleted) {
		t.Fatalf("expected completed task status, got %#v", completedAny)
	}

	extractedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "task.extract",
		Arguments: json.RawMessage(`{"text":"Remember to send the deck and book travel."}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(task.extract): %v", err)
	}
	extractedRaw, _ := json.Marshal(extractedAny)
	var extracted struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(extractedRaw, &extracted); err != nil {
		t.Fatalf("unmarshal task.extract result: %v", err)
	}
	if extracted.Count != 1 {
		t.Fatalf("expected one extracted task candidate, got %#v", extractedAny)
	}
}

func TestWS_ApprovalRequestFlow(t *testing.T) {
	store := session.New(10)
	prov := &stubProvider{response: &types.ChatResponse{}}
	h := handler.NewHandler(store, prov, handler.HandlerOptions{Model: "test-model", Timeout: 5 * time.Second})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	requestedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "approval.request",
		Arguments: json.RawMessage(`{"kind":"file_delete","action":"Delete build cache","summary":"Remove generated cache"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(approval.request): %v", err)
	}
	requestedRaw, _ := json.Marshal(requestedAny)
	var requested struct {
		Status   string `json:"status"`
		Approval struct {
			ID string `json:"id"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(requestedRaw, &requested); err != nil {
		t.Fatalf("unmarshal approval.request result: %v", err)
	}
	if requested.Status != "approval_required" || requested.Approval.ID == "" {
		t.Fatalf("expected approval_required result, got %#v", requestedAny)
	}

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "a1", "approval.pending", map[string]any{})
	msg := readUntilID(t, conn, "a1")
	if msg.Error != nil {
		t.Fatalf("approval.pending rpc error: %#v", msg.Error)
	}
	if !strings.Contains(string(msg.Result), requested.Approval.ID) {
		t.Fatalf("expected approval id in pending result, got %s", string(msg.Result))
	}

	sendRPC(t, conn, "a2", "approval.approve", map[string]any{"id": requested.Approval.ID})
	msg = readUntilID(t, conn, "a2")
	if msg.Error != nil {
		t.Fatalf("approval.approve rpc error: %#v", msg.Error)
	}
	var approved struct {
		Status   string `json:"status"`
		Approval struct {
			ID string `json:"id"`
		} `json:"approval"`
	}
	if err := json.Unmarshal(msg.Result, &approved); err != nil {
		t.Fatalf("unmarshal approval.approve result: %v", err)
	}
	if approved.Status != "approved" || approved.Approval.ID != requested.Approval.ID {
		t.Fatalf("unexpected approval.approve result: %#v", approved)
	}
}

func TestExecuteTool_SessionsTools(t *testing.T) {
	store := session.New(20)
	gate := make(chan struct{})
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "child finished"},
				}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	reg, err := subagent.NewRegistry("")
	if err != nil {
		t.Fatalf("subagent.NewRegistry: %v", err)
	}
	subrt := subagent.NewRuntime(rt, store, reg, nil, 4)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		SubRuntime:   subrt,
	})

	spawnedAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.spawn",
		Arguments: json.RawMessage(`{"task":"review parser errors","reply_back":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.spawn): %v", err)
	}
	rec, ok := spawnedAny.(*subagent.RunRecord)
	if !ok {
		t.Fatalf("expected *subagent.RunRecord, got %T", spawnedAny)
	}
	if !rec.ReplyBack {
		t.Fatalf("expected reply_back to be persisted on run record: %#v", rec)
	}
	if rec.SubTurn.ParentSessionKey != "alice" || rec.SubTurn.SourceSessionKey != "alice" {
		t.Fatalf("expected session.spawn to seed SubTurn parent/source session keys, got %#v", rec.SubTurn)
	}

	sentAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.send",
		Arguments: json.RawMessage(`{"run_id":"` + rec.ID + `","message":"focus on the lexer first","reply_back":false}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.send): %v", err)
	}
	updated, ok := sentAny.(*subagent.RunRecord)
	if !ok {
		t.Fatalf("expected *subagent.RunRecord from session.send, got %T", sentAny)
	}
	if len(updated.Steering) != 1 || updated.Steering[0] != "focus on the lexer first" {
		t.Fatalf("unexpected steering messages: %#v", updated.Steering)
	}
	if updated.ReplyBack {
		t.Fatalf("expected reply_back to be updated to false: %#v", updated)
	}

	statusAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "subagent.status",
		Arguments: json.RawMessage(`{"id":"` + rec.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(subagent.status): %v", err)
	}
	statusRec, ok := statusAny.(*subagent.RunRecord)
	if !ok {
		t.Fatalf("expected *subagent.RunRecord from subagent.status, got %T", statusAny)
	}
	if statusRec.ID != rec.ID {
		t.Fatalf("expected subagent.status to return run %q, got %#v", rec.ID, statusRec)
	}

	listAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.list",
		Arguments: json.RawMessage(`{}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.list): %v", err)
	}
	var listRes struct {
		Count    int `json:"count"`
		Sessions []struct {
			Kind      string `json:"kind"`
			RunID     string `json:"run_id"`
			ReplyBack bool   `json:"reply_back"`
		} `json:"sessions"`
	}
	raw, _ := json.Marshal(listAny)
	if err := json.Unmarshal(raw, &listRes); err != nil {
		t.Fatalf("unmarshal session.list result: %v", err)
	}
	if listRes.Count < 2 {
		t.Fatalf("expected at least main + sub-session, got %#v", listRes)
	}
	foundSub := false
	for _, sess := range listRes.Sessions {
		if sess.RunID == rec.ID {
			foundSub = true
			if sess.ReplyBack {
				t.Fatalf("expected session.list to show updated reply_back=false: %#v", sess)
			}
		}
	}
	if !foundSub {
		t.Fatalf("expected spawned sub-session in list: %#v", listRes)
	}

	close(gate)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		current, ok := subrt.Get(rec.ID)
		if ok && current.Status == subagent.StatusCompleted {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	historyAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.history",
		Arguments: json.RawMessage(`{"run_id":"` + rec.ID + `"}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.history): %v", err)
	}
	var historyRes struct {
		RunID    string          `json:"run_id"`
		Count    int             `json:"count"`
		Messages []types.Message `json:"messages"`
	}
	raw, _ = json.Marshal(historyAny)
	if err := json.Unmarshal(raw, &historyRes); err != nil {
		t.Fatalf("unmarshal session.history result: %v", err)
	}
	if historyRes.RunID != rec.ID || historyRes.Count == 0 {
		t.Fatalf("unexpected session.history result: %#v", historyRes)
	}
}

func TestExecuteTool_SessionSendCrossSessionReplyBack(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{
		complete: func(_ context.Context, req *types.ChatRequest) (*types.ChatResponse, error) {
			last := req.Messages[len(req.Messages)-1].Content
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "reply to: " + last},
				}},
				Usage: types.Usage{PromptTokens: 4, CompletionTokens: 5, TotalTokens: 9},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   coord,
	})

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.send",
		Arguments: json.RawMessage(`{"session_key":"alice::sender::bob","message":"hello from main","reply_back":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.send): %v", err)
	}
	var sendRes struct {
		SessionKey string `json:"session_key"`
		ReplyBack  bool   `json:"reply_back"`
		Result     struct {
			AssistantText string      `json:"assistant_text"`
			Usage         types.Usage `json:"usage"`
		} `json:"result"`
	}
	raw, _ := json.Marshal(result)
	if err := json.Unmarshal(raw, &sendRes); err != nil {
		t.Fatalf("unmarshal session.send result: %v", err)
	}
	if sendRes.SessionKey != "alice::sender::bob" || !sendRes.ReplyBack {
		t.Fatalf("unexpected session.send result: %#v", sendRes)
	}
	if sendRes.Result.AssistantText != "reply to: hello from main" || sendRes.Result.Usage.TotalTokens != 9 {
		t.Fatalf("unexpected nested result: %#v", sendRes.Result)
	}
	targetHistory := store.Get("alice::sender::bob").History()
	if len(targetHistory) != 2 {
		t.Fatalf("expected sent session history to contain user+assistant, got %#v", targetHistory)
	}
	sourceHistory := store.Get("alice").History()
	if len(sourceHistory) != 1 || !strings.Contains(sourceHistory[0].Content, "[reply:alice::sender::bob]") {
		t.Fatalf("expected mirrored reply in source history, got %#v", sourceHistory)
	}
	if !store.Policy("alice::sender::bob").ReplyBack {
		t.Fatal("expected reply_back policy to persist on target session")
	}
}

func TestExecuteTool_BriefGenerate(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{response: &types.ChatResponse{Choices: []types.ChatChoice{{Message: types.Message{Role: "assistant", Content: "ok"}}}}}
	memStore, err := memory.New(filepath.Join(t.TempDir(), "memory.db"), nil)
	if err != nil {
		t.Fatalf("memory.New: %v", err)
	}
	defer memStore.Close()
	taskStore, err := tasks.New(filepath.Join(t.TempDir(), "tasks.db"))
	if err != nil {
		t.Fatalf("tasks.New: %v", err)
	}
	defer taskStore.Close()
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	coord := agent.NewCoordinator(rt)
	defer coord.Stop()
	h := handler.NewHandler(store, prov, handler.HandlerOptions{Model: "test-model", Timeout: 5 * time.Second, AgentRuntime: rt, AgentCoord: coord, MemStore: memStore, TaskStore: taskStore})
	store.Append("alice", types.Message{Role: "user", Content: "We need to close the loop with finance."})
	if _, err := memStore.QueueCandidate(context.Background(), "alice", "Remember to keep summaries concise", memory.ChunkOptions{}); err != nil {
		t.Fatalf("QueueCandidate: %v", err)
	}

	resultAny, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{Name: "brief.generate", Arguments: json.RawMessage(`{"kind":"daily","timezone":"UTC"}`)})
	if err != nil {
		t.Fatalf("ExecuteTool(brief.generate): %v", err)
	}
	raw, _ := json.Marshal(resultAny)
	var result struct {
		Text   string `json:"text"`
		Report struct {
			Kind string `json:"kind"`
		} `json:"report"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("unmarshal brief.generate result: %v", err)
	}
	if result.Report.Kind != "daily" {
		t.Fatalf("unexpected brief kind: %#v", result.Report.Kind)
	}
	if !strings.Contains(result.Text, "Daily brief") {
		t.Fatalf("expected rendered brief text, got: %s", result.Text)
	}
}

func TestExecuteTool_SessionPatchUpdatesPolicy(t *testing.T) {
	store := session.New(20)
	prov := &stubProvider{response: &types.ChatResponse{}}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	reg, err := subagent.NewRegistry("")
	if err != nil {
		t.Fatalf("subagent.NewRegistry: %v", err)
	}
	subrt := subagent.NewRuntime(rt, store, reg, nil, 4)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		AgentCoord:   agent.NewCoordinator(rt),
		SubRuntime:   subrt,
	})

	result, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.patch",
		Arguments: json.RawMessage(`{"session_key":"alice::sender::bob","reply_back":true}`),
	})
	if err != nil {
		t.Fatalf("ExecuteTool(session.patch session_key): %v", err)
	}
	var patchRes struct {
		OK         bool   `json:"ok"`
		SessionKey string `json:"session_key"`
		ReplyBack  bool   `json:"reply_back"`
	}
	raw, _ := json.Marshal(result)
	if err := json.Unmarshal(raw, &patchRes); err != nil {
		t.Fatalf("unmarshal session.patch result: %v", err)
	}
	if !patchRes.OK || patchRes.SessionKey != "alice::sender::bob" || !patchRes.ReplyBack {
		t.Fatalf("unexpected session.patch result: %#v", patchRes)
	}
	if !store.Policy("alice::sender::bob").ReplyBack {
		t.Fatal("expected patched reply_back policy on target session")
	}
}

func TestWS_SubagentStatusAlias(t *testing.T) {
	store := session.New(20)
	gate := make(chan struct{})
	prov := &stubProvider{
		complete: func(ctx context.Context, _ *types.ChatRequest) (*types.ChatResponse, error) {
			select {
			case <-gate:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return &types.ChatResponse{
				Choices: []types.ChatChoice{{
					Message: types.Message{Role: "assistant", Content: "child finished"},
				}},
			}, nil
		},
	}
	rt := agent.NewRuntime(store, prov, "test-model", 5*time.Second, agent.RetryPolicy{MaxAttempts: 1})
	subrt := subagent.NewRuntime(rt, store, mustSubagentRegistry(t), nil, 4)
	h := handler.NewHandler(store, prov, handler.HandlerOptions{
		Model:        "test-model",
		Timeout:      5 * time.Second,
		AgentRuntime: rt,
		SubRuntime:   subrt,
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	conn := dialWS(t, srv, "alice")
	sendRPC(t, conn, "sub-caps", "server.capabilities", nil)
	capsMsg := readUntilID(t, conn, "sub-caps")
	if capsMsg.Error != nil {
		t.Fatalf("unexpected capabilities error: %v", capsMsg.Error)
	}
	var caps struct {
		Methods   []string `json:"methods"`
		ChatTools []string `json:"chat_tools"`
	}
	if err := json.Unmarshal(capsMsg.Result, &caps); err != nil {
		t.Fatalf("unmarshal server.capabilities result: %v", err)
	}
	if !containsString(caps.Methods, "subagent.status") {
		t.Fatalf("expected methods to include subagent.status, got %#v", caps.Methods)
	}
	if !containsString(caps.ChatTools, "subagent.status") {
		t.Fatalf("expected chat tools to include subagent.status, got %#v", caps.ChatTools)
	}

	sendRPC(t, conn, "sub-spawn", "subagent.spawn", map[string]any{"task": "review parser errors"})
	spawnMsg := readUntilID(t, conn, "sub-spawn")
	if spawnMsg.Error != nil {
		t.Fatalf("unexpected spawn error: %v", spawnMsg.Error)
	}
	var rec subagent.RunRecord
	if err := json.Unmarshal(spawnMsg.Result, &rec); err != nil {
		t.Fatalf("unmarshal subagent.spawn result: %v", err)
	}

	sendRPC(t, conn, "sub-status", "subagent.status", map[string]any{"id": rec.ID})
	statusMsg := readUntilID(t, conn, "sub-status")
	if statusMsg.Error != nil {
		t.Fatalf("unexpected subagent.status error: %v", statusMsg.Error)
	}
	var statusRec subagent.RunRecord
	if err := json.Unmarshal(statusMsg.Result, &statusRec); err != nil {
		t.Fatalf("unmarshal subagent.status result: %v", err)
	}
	if statusRec.ID != rec.ID {
		t.Fatalf("expected subagent.status to return run %q, got %#v", rec.ID, statusRec)
	}

	sendRPC(t, conn, "spawn-status", "spawn.status", map[string]any{"id": rec.ID})
	aliasMsg := readUntilID(t, conn, "spawn-status")
	if aliasMsg.Error != nil {
		t.Fatalf("unexpected spawn.status error: %v", aliasMsg.Error)
	}
	var aliasRec subagent.RunRecord
	if err := json.Unmarshal(aliasMsg.Result, &aliasRec); err != nil {
		t.Fatalf("unmarshal spawn.status result: %v", err)
	}
	if aliasRec.ID != rec.ID {
		t.Fatalf("expected spawn.status alias to return run %q, got %#v", rec.ID, aliasRec)
	}

	close(gate)
}

func TestExecuteTool_SystemNotifyErrorsWhenNotifierUnavailable(t *testing.T) {
	t.Setenv("PATH", "")
	h := handler.NewHandler(session.New(10), &stubProvider{response: &types.ChatResponse{}}, handler.HandlerOptions{
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})
	_, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "system.notify",
		Arguments: json.RawMessage(`{"message":"hi"}`),
	})
	if err == nil {
		t.Fatal("expected system.notify to fail without a notifier command")
	}
}

func TestExecuteTool_NotificationSendErrorsWhenNotifierUnavailable(t *testing.T) {
	t.Setenv("PATH", "")
	h := handler.NewHandler(session.New(10), &stubProvider{response: &types.ChatResponse{}}, handler.HandlerOptions{
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})
	_, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "notification.send",
		Arguments: json.RawMessage(`{"message":"Task complete","kind":"run_complete"}`),
	})
	if err == nil {
		t.Fatal("expected notification.send to fail without a notifier command")
	}
}

func TestExecuteTool_NotificationSendRejectsEmptyMessage(t *testing.T) {
	h := handler.NewHandler(session.New(10), &stubProvider{response: &types.ChatResponse{}}, handler.HandlerOptions{
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})
	_, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "notification.send",
		Arguments: json.RawMessage(`{"message":""}`),
	})
	if err == nil {
		t.Fatal("expected notification.send to fail with empty message")
	}
}

func TestExecuteTool_NotificationSendRejectsInvalidKind(t *testing.T) {
	h := handler.NewHandler(session.New(10), &stubProvider{response: &types.ChatResponse{}}, handler.HandlerOptions{
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})
	_, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "notification.send",
		Arguments: json.RawMessage(`{"message":"hi","kind":"bogus"}`),
	})
	if err == nil {
		t.Fatal("expected notification.send to fail with invalid kind")
	}
}

func TestExecuteTool_NotificationSendRejectsInvalidUrgency(t *testing.T) {
	h := handler.NewHandler(session.New(10), &stubProvider{response: &types.ChatResponse{}}, handler.HandlerOptions{
		Model:   "test-model",
		Timeout: 5 * time.Second,
	})
	_, err := h.ExecuteTool(context.Background(), "alice", agent.ToolCall{
		Name:      "notification.send",
		Arguments: json.RawMessage(`{"message":"hi","urgency":"extreme"}`),
	})
	if err == nil {
		t.Fatal("expected notification.send to fail with invalid urgency")
	}
}
