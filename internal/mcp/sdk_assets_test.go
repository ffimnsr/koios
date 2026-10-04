package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ffimnsr/koios/internal/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// jsonValid reports whether raw is nil or valid JSON.
func jsonValid(raw json.RawMessage) bool {
	return len(raw) == 0 || json.Valid(raw)
}

// configForMinimalServer returns the client config for the minimal test server.
func configForMinimalServer(server *minimalMCPServer) config.MCPServerConfig {
	return config.MCPServerConfig{
		Name:      "sdk-minimal",
		Transport: "http",
		URL:       server.url,
		Timeout:   "10s",
		Enabled:   true,
	}
}

// ─── conversion helpers ───────────────────────────────────────────────────────

func TestFromSDKResourcePreservesMetadata(t *testing.T) {
	resource := fromSDKResource(&sdkmcp.Resource{
		URI:         "mach1://strategy-spec/schema.json",
		Name:        "strategy-spec",
		Title:       "Strategy Spec",
		Description: "the strategy specification",
		MIMEType:    "application/json",
		Size:        12345,
		Annotations: &sdkmcp.Annotations{Audience: []sdkmcp.Role{"user"}},
	})
	if resource.URI != "mach1://strategy-spec/schema.json" || resource.Name != "strategy-spec" {
		t.Fatalf("uri/name not preserved: %#v", resource)
	}
	if resource.Title != "Strategy Spec" || resource.Description != "the strategy specification" {
		t.Fatalf("title/description not preserved: %#v", resource)
	}
	if resource.MimeType != "application/json" || resource.Size != 12345 {
		t.Fatalf("mimeType/size not preserved: %#v", resource)
	}
	if !jsonValid(resource.Annotations) {
		t.Fatalf("annotations not preserved as JSON: %s", resource.Annotations)
	}
}

func TestFromSDKResourceTemplatePreservesMetadata(t *testing.T) {
	tpl := fromSDKResourceTemplate(&sdkmcp.ResourceTemplate{
		URITemplate: "mach1://specs/{id}/schema.json",
		Name:        "spec-template",
		Title:       "Spec Template",
		Description: "one spec per id",
		MIMEType:    "application/json",
		Annotations: &sdkmcp.Annotations{Audience: []sdkmcp.Role{"assistant"}},
	})
	if tpl.URITemplate != "mach1://specs/{id}/schema.json" || tpl.Name != "spec-template" {
		t.Fatalf("uriTemplate/name not preserved: %#v", tpl)
	}
	if tpl.Title != "Spec Template" || tpl.Description != "one spec per id" {
		t.Fatalf("title/description not preserved: %#v", tpl)
	}
	if tpl.MimeType != "application/json" || !jsonValid(tpl.Annotations) {
		t.Fatalf("mimeType/annotations not preserved: %#v", tpl)
	}
}

func TestFromSDKResourceContentsTextAndBlob(t *testing.T) {
	text := fromSDKResourceContents(&sdkmcp.ResourceContents{
		URI:      "mach1://strategy-spec/schema.json",
		MIMEType: "application/json",
		Text:     "{}",
	})
	if text.URI != "mach1://strategy-spec/schema.json" || text.MimeType != "application/json" || text.Text != "{}" || text.Blob != "" {
		t.Fatalf("text contents not preserved: %#v", text)
	}

	blob := fromSDKResourceContents(&sdkmcp.ResourceContents{
		URI:      "mach1://strategy-spec/logo.png",
		MIMEType: "image/png",
		Blob:     []byte("logo"),
	})
	if blob.Text != "" || blob.Blob != "bG9nbw==" {
		t.Fatalf("blob contents not preserved as base64: %#v", blob)
	}
}

// TestFromSDKReadResourceResultPreservesTTL verifies that the cache metadata
// driving manager-level ReadResource caching reaches the Koios result.
func TestFromSDKReadResourceResultPreservesTTL(t *testing.T) {
	result := fromSDKReadResourceResult(&sdkmcp.ReadResourceResult{
		Contents: []*sdkmcp.ResourceContents{
			{URI: "mach1://a", MIMEType: "application/json", Text: `{"a":1}`},
			{URI: "mach1://b", MIMEType: "image/png", Blob: []byte{0x01, 0x02}},
		},
		Cacheable: sdkmcp.Cacheable{TTLMs: 5000, CacheScope: "public"},
	})
	if result == nil {
		t.Fatal("nil result")
	}
	if result.TTLMs != 5000 || result.CacheScope != "public" {
		t.Fatalf("TTL metadata not preserved: %#v", result)
	}
	if result.ResultType != "complete" {
		t.Fatalf("unexpected result type: %q", result.ResultType)
	}
	if len(result.Contents) != 2 {
		t.Fatalf("unexpected contents: %#v", result.Contents)
	}
	if result.Contents[0].Text != `{"a":1}` || result.Contents[1].Blob != "AQI=" {
		t.Fatalf("contents not preserved: %#v", result.Contents)
	}
}

func TestFromSDKPromptPreservesArguments(t *testing.T) {
	prompt := fromSDKPrompt(&sdkmcp.Prompt{
		Name:        "greet",
		Title:       "Greet",
		Description: "greets someone",
		Arguments: []*sdkmcp.PromptArgument{
			{Name: "name", Title: "Name", Description: "who to greet", Required: true},
			{Name: "tone", Title: "Tone"},
		},
	})
	if prompt.Name != "greet" || prompt.Title != "Greet" || prompt.Description != "greets someone" {
		t.Fatalf("prompt metadata not preserved: %#v", prompt)
	}
	if len(prompt.Arguments) != 2 {
		t.Fatalf("unexpected arguments: %#v", prompt.Arguments)
	}
	first := prompt.Arguments[0]
	if first.Name != "name" || first.Title != "Name" || first.Description != "who to greet" || !first.Required {
		t.Fatalf("first argument not preserved: %#v", first)
	}
	if prompt.Arguments[1].Required {
		t.Fatalf("required flag not preserved: %#v", prompt.Arguments[1])
	}
}

func TestSDKPromptArgumentsConversion(t *testing.T) {
	got := sdkPromptArguments(map[string]any{
		"name":    "acme",
		"count":   3,
		"enabled": true,
		"nested":  map[string]any{"x": 1},
	})
	if got["name"] != "acme" {
		t.Fatalf("string argument not preserved: %#v", got)
	}
	if got["count"] != "3" || got["enabled"] != "true" {
		t.Fatalf("primitive arguments not compact-encoded: %#v", got)
	}
	if got["nested"] != `{"x":1}` {
		t.Fatalf("nested argument not compact-encoded: %#v", got)
	}
}

func TestFromSDKGetPromptResultConversion(t *testing.T) {
	result := fromSDKGetPromptResult(&sdkmcp.GetPromptResult{
		Description: "greets the caller",
		Messages: []*sdkmcp.PromptMessage{
			{Role: sdkmcp.Role("user"), Content: &sdkmcp.TextContent{Text: "hello"}},
			{Role: sdkmcp.Role("assistant"), Content: &sdkmcp.TextContent{Text: "hi there"}},
		},
	})
	if result == nil {
		t.Fatal("nil result")
	}
	if result.Description != "greets the caller" || result.ResultType != "complete" {
		t.Fatalf("metadata not preserved: %#v", result)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("unexpected messages: %#v", result.Messages)
	}
	if result.Messages[0].Role != "user" || result.Messages[0].Content.Text != "hello" {
		t.Fatalf("first message not preserved: %#v", result.Messages[0])
	}
	if result.Messages[1].Role != "assistant" || result.Messages[1].Content.Text != "hi there" {
		t.Fatalf("second message not preserved: %#v", result.Messages[1])
	}
}

// ─── SDK-backed asset requests over HTTP ──────────────────────────────────────

// minimalAssetClient connects a client to the minimal asset-serving server.
func minimalAssetClient(t *testing.T) (Client, *minimalMCPServer) {
	t.Helper()
	server := newMinimalMCPServerWithAssets(t)
	client := NewSDKClient(configForMinimalServer(server))
	t.Cleanup(func() { _ = client.Close() })
	initializeFixture(t, client)
	return client, server
}

func TestSDKClientListResourcesOverHTTP(t *testing.T) {
	client, _ := minimalAssetClient(t)

	resources, err := client.ListResources(context.Background())
	if err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	if len(resources) != 1 {
		t.Fatalf("unexpected resources: %#v", resources)
	}
	r := resources[0]
	if r.URI != "mach1://strategy-spec/schema.json" || r.Name != "strategy-spec" || r.Title != "Strategy Spec" {
		t.Fatalf("resource not preserved: %#v", r)
	}
	if r.Description != "the strategy specification" || r.MimeType != "application/json" || r.Size != 12345 {
		t.Fatalf("resource metadata not preserved: %#v", r)
	}
	if !jsonValid(r.Annotations) {
		t.Fatalf("annotations not preserved: %s", r.Annotations)
	}
}

func TestSDKClientListResourceTemplatesOverHTTP(t *testing.T) {
	client, _ := minimalAssetClient(t)

	templates, err := client.ListResourceTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListResourceTemplates: %v", err)
	}
	if len(templates) != 1 {
		t.Fatalf("unexpected templates: %#v", templates)
	}
	tpl := templates[0]
	if tpl.URITemplate != "mach1://specs/{id}/schema.json" || tpl.Name != "spec-template" {
		t.Fatalf("template not preserved: %#v", tpl)
	}
	if tpl.Title != "Spec Template" || tpl.Description != "one spec per id" || tpl.MimeType != "application/json" {
		t.Fatalf("template metadata not preserved: %#v", tpl)
	}
}

func TestSDKClientReadResourceOverHTTP(t *testing.T) {
	client, _ := minimalAssetClient(t)

	result, err := client.ReadResource(context.Background(), "mach1://strategy-spec/schema.json")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if result.ResultType != "complete" {
		t.Fatalf("unexpected result type: %q", result.ResultType)
	}
	// The fixture returns one text and one blob content block.
	if len(result.Contents) != 2 {
		t.Fatalf("unexpected contents: %#v", result.Contents)
	}
	if result.Contents[0].Text != "{}" || result.Contents[0].URI != "mach1://strategy-spec/schema.json" {
		t.Fatalf("text content not preserved: %#v", result.Contents[0])
	}
	if result.Contents[1].Blob != "bG9nbw==" || result.Contents[1].MimeType != "image/png" {
		t.Fatalf("blob content not preserved: %#v", result.Contents[1])
	}
}

// TestSDKClientReadResourceTTLMetadataOverHTTP verifies the TTL/cache-scope
// metadata the manager relies on reaches the Koios read result over the wire.
func TestSDKClientReadResourceTTLMetadataOverHTTP(t *testing.T) {
	client, _ := minimalAssetClient(t)

	result, err := client.ReadResource(context.Background(), "mach1://strategy-spec/schema.json")
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if result.TTLMs != 5000 || result.CacheScope != "public" {
		t.Fatalf("TTL metadata not preserved: %#v", result)
	}
}

func TestSDKClientListPromptsOverHTTP(t *testing.T) {
	client, _ := minimalAssetClient(t)

	prompts, err := client.ListPrompts(context.Background())
	if err != nil {
		t.Fatalf("ListPrompts: %v", err)
	}
	if len(prompts) != 1 {
		t.Fatalf("unexpected prompts: %#v", prompts)
	}
	p := prompts[0]
	if p.Name != "greet" || p.Title != "Greet" || p.Description != "greets someone" {
		t.Fatalf("prompt not preserved: %#v", p)
	}
	if len(p.Arguments) != 2 {
		t.Fatalf("unexpected arguments: %#v", p.Arguments)
	}
	if !p.Arguments[0].Required || p.Arguments[0].Name != "name" {
		t.Fatalf("required argument not preserved: %#v", p.Arguments[0])
	}
	if p.Arguments[1].Title != "Tone" || p.Arguments[1].Required {
		t.Fatalf("optional argument not preserved: %#v", p.Arguments[1])
	}
}

func TestSDKClientGetPromptOverHTTP(t *testing.T) {
	client, _ := minimalAssetClient(t)

	result, err := client.GetPrompt(context.Background(), "greet", map[string]any{"name": "acme"})
	if err != nil {
		t.Fatalf("GetPrompt: %v", err)
	}
	if result.Description != "greets the caller" || result.ResultType != "complete" {
		t.Fatalf("result metadata not preserved: %#v", result)
	}
	if len(result.Messages) != 1 {
		t.Fatalf("unexpected messages: %#v", result.Messages)
	}
	msg := result.Messages[0]
	if msg.Role != "user" || msg.Content.Type != "text" || msg.Content.Text != "hello acme" {
		t.Fatalf("message content not preserved: %#v", msg)
	}
}

// TestSDKClientOptionalAssetsErrorBeforeInitialize verifies the asset methods
// reject calls before a session exists.
func TestSDKClientOptionalAssetsErrorBeforeInitialize(t *testing.T) {
	client := NewSDKClient(config.MCPServerConfig{Name: "sdk-uninit", Transport: "http", URL: "http://127.0.0.1:1"})
	defer client.Close()

	for _, op := range []struct {
		name string
		call func() error
	}{
		{"ListResources", func() error { _, err := client.ListResources(context.Background()); return err }},
		{"ListResourceTemplates", func() error { _, err := client.ListResourceTemplates(context.Background()); return err }},
		{"ReadResource", func() error { _, err := client.ReadResource(context.Background(), "mach1://x"); return err }},
		{"ListPrompts", func() error { _, err := client.ListPrompts(context.Background()); return err }},
		{"GetPrompt", func() error { _, err := client.GetPrompt(context.Background(), "greet", nil); return err }},
	} {
		if err := op.call(); err == nil {
			t.Fatalf("%s before Initialize should fail", op.name)
		}
	}
}
