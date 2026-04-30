package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/artifacts"
	"github.com/ffimnsr/koios/internal/config"
	"github.com/ffimnsr/koios/internal/mcp"
	"github.com/ffimnsr/koios/internal/session"
)

func TestBrowserProfileToolsListUseAndStatus(t *testing.T) {
	store := session.New(10)
	artifactStore, err := artifacts.New(t.TempDir() + "/artifacts.db")
	if err != nil {
		t.Fatalf("artifact store: %v", err)
	}
	defer artifactStore.Close()
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{{
		Name:           "browser_work",
		Enabled:        true,
		Transport:      "stdio",
		Command:        "ignored",
		ToolNamePrefix: mcp.ToolPrefix("browser_work"),
		HideTools:      true,
		Kind:           "browser",
		ProfileName:    "work",
	}, {
		Name:           "browser_review",
		Enabled:        true,
		Transport:      "stdio",
		Command:        "ignored",
		ToolNamePrefix: mcp.ToolPrefix("browser_review"),
		HideTools:      true,
		Kind:           "browser",
		ProfileName:    "review",
	}}, func(cfg config.MCPServerConfig) mcp.Client {
		return &fakeBrowserClient{profile: cfg.ProfileName}
	})
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:         "test-model",
		MCPManager:    mgr,
		ArtifactStore: artifactStore,
		BrowserConfig: config.BrowserConfig{
			DefaultProfile: "work",
			Profiles: []config.BrowserProfileConfig{
				{Name: "work", Enabled: true, Mode: "managed"},
				{Name: "review", Enabled: true, Mode: "existing_session", UserDataDir: "/tmp/koios-browser-review", HostAllowlist: []string{"example.com", "www.example.com", "iana.org", "www.iana.org"}},
			},
		},
	})

	defs := h.ToolDefinitionsForRun("alice", "alice::tab", "")
	counts := make(map[string]int)
	for _, def := range defs {
		if def.Type != "function" {
			continue
		}
		counts[def.Function.Name]++
	}
	for _, name := range []string{"browser.snapshot", "browser.pdf", "browser.click", "browser.click_coords", "browser.type", "browser.press", "browser.submit", "browser.drag", "browser.select", "browser.hover", "browser.scroll", "browser.scroll_into_view", "browser.fill", "browser.cookies.list", "browser.cookies.set", "browser.cookies.delete", "browser.storage.inspect", "browser.storage.set", "browser.storage.delete", "browser.storage.clear", "browser.emulate", "screen.snapshot", "screen.record", "canvas.push", "canvas.eval", "canvas.snapshot", "canvas.reset"} {
		if counts[name] != 1 {
			t.Fatalf("expected exactly one tool definition for %s, got %d", name, counts[name])
		}
	}
	if counts["browser.screenshot"] != 1 {
		t.Fatalf("expected exactly one tool definition for browser.screenshot, got %d", counts["browser.screenshot"])
	}

	listResult, err := h.executeSessionWorkspaceTool(context.Background(), "alice", agent.ToolCall{Name: "browser.profile.list", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("browser.profile.list: %v", err)
	}
	listMap := listResult.(map[string]any)
	if listMap["active_profile"] != "work" {
		t.Fatalf("expected default active profile work, got %#v", listMap["active_profile"])
	}
	profiles := listMap["profiles"].([]map[string]any)
	if len(profiles) != 2 {
		t.Fatalf("expected two browser profiles, got %#v", listMap["profiles"])
	}
	if profiles[1]["mode"] != "existing_session" {
		t.Fatalf("expected review profile mode existing_session, got %#v", profiles[1]["mode"])
	}
	if profiles[1]["host_allowlist_count"] != 4 {
		t.Fatalf("expected review profile host allowlist count, got %#v", profiles[1])
	}
	if attached, _ := profiles[1]["attached_existing_browser"].(bool); !attached {
		t.Fatalf("expected review profile to be marked as existing-session attach, got %#v", profiles[1])
	}

	useArgs := json.RawMessage(`{"session_key":"alice::tab","profile":"review"}`)
	useResult, err := h.executeSessionWorkspaceTool(context.Background(), "alice", agent.ToolCall{Name: "browser.profile.use", Arguments: useArgs})
	if err != nil {
		t.Fatalf("browser.profile.use: %v", err)
	}
	useMap := useResult.(map[string]any)
	if useMap["browser_profile"] != "review" {
		t.Fatalf("expected selected profile review, got %#v", useMap["browser_profile"])
	}
	if got := store.Policy("alice::tab").BrowserProfile; got != "review" {
		t.Fatalf("expected session policy browser profile review, got %q", got)
	}

	statusCtx := agent.WithToolRunContext(context.Background(), "alice::tab", "")
	statusResult, err := h.executeSessionWorkspaceTool(statusCtx, "alice", agent.ToolCall{Name: "browser.status", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("browser.status: %v", err)
	}
	statusMap := statusResult.(map[string]any)
	if statusMap["active_profile"] != "review" {
		t.Fatalf("expected status to use session override review, got %#v", statusMap["active_profile"])
	}
	statusProfile := statusMap["profile"].(map[string]any)
	if statusProfile["mode"] != "existing_session" {
		t.Fatalf("expected browser.status profile mode existing_session, got %#v", statusProfile)
	}
	server := statusMap["server"].(map[string]any)
	if connected, _ := server["connected"].(bool); connected {
		t.Fatalf("expected browser server to be disconnected before explicit start, got %#v", server)
	}

	startCtx := agent.WithToolRunContext(context.Background(), "alice::tab", "")
	started, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.start", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("browser.start: %v", err)
	}
	startedMap := started.(map[string]any)
	if startedMap["active_profile"] != "review" {
		t.Fatalf("expected browser.start to use session override review, got %#v", startedMap["active_profile"])
	}
	if startedMap["mode"] != "existing_session" {
		t.Fatalf("expected browser.start to report existing_session mode, got %#v", startedMap["mode"])
	}
	if pages, ok := startedMap["pages"].([]any); !ok || len(pages) != 2 {
		t.Fatalf("expected normalized pages payload from browser.start, got %#v", startedMap["pages"])
	}

	openResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.open", Arguments: json.RawMessage(`{"url":"https://example.com","background":true,"timeout_ms":5000}`)})
	if err != nil {
		t.Fatalf("browser.open: %v", err)
	}
	openMap := openResult.(map[string]any)
	if openMap["source_tool"] != "browser.new_page" {
		t.Fatalf("expected browser.open to call browser.new_page, got %#v", openMap)
	}

	focusResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.focus", Arguments: json.RawMessage(`{"page_id":2,"bring_to_front":false}`)})
	if err != nil {
		t.Fatalf("browser.focus: %v", err)
	}
	if focusResult.(map[string]any)["source_tool"] != "browser.select_page" {
		t.Fatalf("expected browser.focus to call browser.select_page, got %#v", focusResult)
	}

	closeResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.close", Arguments: json.RawMessage(`{"page_id":2}`)})
	if err != nil {
		t.Fatalf("browser.close: %v", err)
	}
	if closeResult.(map[string]any)["source_tool"] != "browser.close_page" {
		t.Fatalf("expected browser.close to call browser.close_page, got %#v", closeResult)
	}

	snapshotResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.snapshot", Arguments: json.RawMessage(`{"page_id":2}`)})
	if err != nil {
		t.Fatalf("browser.snapshot: %v", err)
	}
	snapshotMap := snapshotResult.(map[string]any)
	if snapshotMap["source_tool"] != "browser.take_snapshot" {
		t.Fatalf("expected browser.snapshot to call browser.take_snapshot, got %#v", snapshotResult)
	}
	if !strings.Contains(snapshotMap["snapshot"].(string), "@e1") {
		t.Fatalf("expected browser.snapshot to rewrite uids to stable refs, got %#v", snapshotMap["snapshot"])
	}
	if snapshotMap["format"] != "ai" {
		t.Fatalf("expected default browser.snapshot format ai, got %#v", snapshotMap["format"])
	}

	roleSnapshotResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.snapshot", Arguments: json.RawMessage(`{"page_id":2,"format":"role"}`)})
	if err != nil {
		t.Fatalf("browser.snapshot role: %v", err)
	}
	roleSnapshotMap := roleSnapshotResult.(map[string]any)
	if roleSnapshotMap["format"] != "role" {
		t.Fatalf("expected role snapshot format, got %#v", roleSnapshotMap["format"])
	}
	if strings.Contains(roleSnapshotMap["snapshot"].(string), `value="existing"`) {
		t.Fatalf("expected role snapshot to omit accessibility attributes, got %#v", roleSnapshotMap["snapshot"])
	}

	accessibilitySnapshotResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.snapshot", Arguments: json.RawMessage(`{"page_id":2,"format":"accessibility"}`)})
	if err != nil {
		t.Fatalf("browser.snapshot accessibility: %v", err)
	}
	accessibilitySnapshotMap := accessibilitySnapshotResult.(map[string]any)
	if accessibilitySnapshotMap["format"] != "accessibility" {
		t.Fatalf("expected accessibility snapshot format, got %#v", accessibilitySnapshotMap["format"])
	}
	accessibilityTree, ok := accessibilitySnapshotMap["tree"].([]map[string]any)
	if !ok || len(accessibilityTree) == 0 {
		t.Fatalf("expected accessibility tree output, got %#v", accessibilitySnapshotMap["tree"])
	}

	clickResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.click", Arguments: json.RawMessage(`{"page_id":2,"uid":"uid-click","double_click":true,"timeout_ms":2500}`)})
	if err != nil {
		t.Fatalf("browser.click: %v", err)
	}
	if clickResult.(map[string]any)["source_tool"] != "browser.click" {
		t.Fatalf("expected browser.click to call browser.click, got %#v", clickResult)
	}

	clickByRefResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.click", Arguments: json.RawMessage(`{"page_id":2,"ref":"@e2"}`)})
	if err != nil {
		t.Fatalf("browser.click by ref: %v", err)
	}
	clickByRefMap := clickByRefResult.(map[string]any)
	clickedPayload := clickByRefMap["result"].(map[string]any)
	if clickedPayload["uid"] != "uid-click" {
		t.Fatalf("expected browser.click ref @e2 to resolve uid-click, got %#v", clickedPayload)
	}

	clickCoordsResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.click_coords", Arguments: json.RawMessage(`{"page_id":2,"x":320,"y":240,"button":"right","double_click":true,"delay_ms":25}`)})
	if err != nil {
		t.Fatalf("browser.click_coords: %v", err)
	}
	if clickCoordsResult.(map[string]any)["source_tool"] != "browser.evaluate_script" {
		t.Fatalf("expected browser.click_coords to call browser.evaluate_script, got %#v", clickCoordsResult)
	}

	screenshotResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.screenshot", Arguments: json.RawMessage(`{"page_id":2,"ref":"@e2","format":"png"}`)})
	if err != nil {
		t.Fatalf("browser.screenshot: %v", err)
	}
	screenshotMap := screenshotResult.(map[string]any)
	if screenshotMap["source_tool"] != "browser.take_screenshot" {
		t.Fatalf("expected browser.screenshot to call browser.take_screenshot, got %#v", screenshotResult)
	}
	if screenshotMap["mime_type"] != "image/png" {
		t.Fatalf("expected browser.screenshot mime type image/png, got %#v", screenshotMap["mime_type"])
	}
	if screenshotMap["data_base64"] != fakeBrowserPNGBase64 {
		t.Fatalf("expected browser.screenshot image payload, got %#v", screenshotMap["data_base64"])
	}

	screenSnapshotResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "screen.snapshot", Arguments: json.RawMessage(`{"page_id":2,"full_page":true,"format":"png"}`)})
	if err != nil {
		t.Fatalf("screen.snapshot: %v", err)
	}
	screenSnapshotMap := screenSnapshotResult.(map[string]any)
	if screenSnapshotMap["tool"] != "screen.snapshot" {
		t.Fatalf("expected screen.snapshot tool marker, got %#v", screenSnapshotResult)
	}
	if screenSnapshotMap["mime_type"] != "image/png" {
		t.Fatalf("expected screen.snapshot mime type image/png, got %#v", screenSnapshotMap["mime_type"])
	}

	screenRecordResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "screen.record", Arguments: json.RawMessage(`{"page_id":2,"duration_ms":200,"interval_ms":100,"format":"gif","persist_artifact":true,"artifact_title":"Review capture","artifact_labels":["demo"]}`)})
	if err != nil {
		t.Fatalf("screen.record: %v", err)
	}
	screenRecordMap := screenRecordResult.(map[string]any)
	if screenRecordMap["tool"] != "screen.record" {
		t.Fatalf("expected screen.record tool marker, got %#v", screenRecordResult)
	}
	if screenRecordMap["mime_type"] != "image/gif" {
		t.Fatalf("expected screen.record mime type image/gif, got %#v", screenRecordMap["mime_type"])
	}
	if screenRecordMap["frame_count"] != 2 {
		t.Fatalf("expected two recording frames, got %#v", screenRecordMap["frame_count"])
	}
	artifactPayload, ok := screenRecordMap["artifact"].(*artifacts.Artifact)
	if !ok || artifactPayload == nil {
		t.Fatalf("expected screen.record artifact, got %#v", screenRecordMap["artifact"])
	}
	if artifactPayload.Kind != "screen_recording" {
		t.Fatalf("expected screen.record artifact kind screen_recording, got %#v", artifactPayload)
	}

	canvasPushResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "canvas.push", Arguments: json.RawMessage(`{"page_id":2,"layout":{"theme":"warm","title":"Preview","subtitle":"Morning brief","panels":[{"id":"today","title":"Today","body":"Three follow-ups","accent":"amber","items":["Reply to Mia","Draft spec"],"footer":"Updated now"}]},"state":{"count":1},"mount":"overlay"}`)})
	if err != nil {
		t.Fatalf("canvas.push: %v", err)
	}
	canvasPushMap := canvasPushResult.(map[string]any)
	if canvasPushMap["tool"] != "canvas.push" {
		t.Fatalf("expected canvas.push tool marker, got %#v", canvasPushResult)
	}
	canvasPushPayload := canvasPushMap["canvas"].(map[string]any)
	if mounted, _ := canvasPushPayload["mounted"].(bool); !mounted {
		t.Fatalf("expected canvas.push to mount canvas, got %#v", canvasPushPayload)
	}
	if _, ok := canvasPushPayload["layout"].(map[string]any); !ok {
		t.Fatalf("expected canvas.push to preserve layout metadata, got %#v", canvasPushPayload)
	}

	canvasEvalResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "canvas.eval", Arguments: json.RawMessage(`{"page_id":2,"script":"canvas.appendHTML(args.html); canvas.mergeState(args.patch); return canvas.state;","args":{"html":"<button>Next</button>","patch":{"count":2}}}`)})
	if err != nil {
		t.Fatalf("canvas.eval: %v", err)
	}
	canvasEvalMap := canvasEvalResult.(map[string]any)
	canvasEvalPayload := canvasEvalMap["canvas"].(map[string]any)
	canvasEvalState := canvasEvalPayload["state"].(map[string]any)
	if canvasEvalState["count"] != float64(2) {
		t.Fatalf("expected canvas.eval to update state count, got %#v", canvasEvalPayload)
	}

	canvasSnapshotResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "canvas.snapshot", Arguments: json.RawMessage(`{"page_id":2,"include_image":true,"format":"png"}`)})
	if err != nil {
		t.Fatalf("canvas.snapshot: %v", err)
	}
	canvasSnapshotMap := canvasSnapshotResult.(map[string]any)
	canvasSnapshotPayload := canvasSnapshotMap["canvas"].(map[string]any)
	if mounted, _ := canvasSnapshotPayload["mounted"].(bool); !mounted {
		t.Fatalf("expected canvas.snapshot to report mounted canvas, got %#v", canvasSnapshotPayload)
	}
	storedPayload := canvasSnapshotMap["stored"].(map[string]any)
	storedState := storedPayload["state"].(map[string]any)
	if storedState["count"] != float64(2) {
		t.Fatalf("expected canvas.snapshot stored state count 2, got %#v", storedPayload)
	}
	if layoutPayload, ok := storedPayload["layout"].(map[string]any); !ok || layoutPayload["title"] != "Preview" {
		t.Fatalf("expected canvas.snapshot stored layout, got %#v", storedPayload)
	}
	imagePayload := canvasSnapshotMap["image"].(map[string]any)
	if imagePayload["mime_type"] != "image/png" {
		t.Fatalf("expected canvas.snapshot image mime type image/png, got %#v", imagePayload)
	}

	canvasResetResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "canvas.reset", Arguments: json.RawMessage(`{"page_id":2}`)})
	if err != nil {
		t.Fatalf("canvas.reset: %v", err)
	}
	if canvasResetResult.(map[string]any)["tool"] != "canvas.reset" {
		t.Fatalf("expected canvas.reset tool marker, got %#v", canvasResetResult)
	}

	canvasSnapshotAfterReset, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "canvas.snapshot", Arguments: json.RawMessage(`{"page_id":2,"include_image":false}`)})
	if err != nil {
		t.Fatalf("canvas.snapshot after reset: %v", err)
	}
	if mounted, _ := canvasSnapshotAfterReset.(map[string]any)["canvas"].(map[string]any)["mounted"].(bool); mounted {
		t.Fatalf("expected canvas to be unmounted after reset, got %#v", canvasSnapshotAfterReset)
	}

	pdfResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.pdf", Arguments: json.RawMessage(`{"page_id":2,"print_background":true}`)})
	if err != nil {
		t.Fatalf("browser.pdf: %v", err)
	}
	pdfMap := pdfResult.(map[string]any)
	if pdfMap["source_tool"] != "browser.print_to_pdf" {
		t.Fatalf("expected browser.pdf to call browser.print_to_pdf, got %#v", pdfResult)
	}
	if pdfMap["mime_type"] != "application/pdf" {
		t.Fatalf("expected browser.pdf mime type application/pdf, got %#v", pdfMap["mime_type"])
	}
	if pdfMap["data_base64"] != "cGRmLWJ5dGVz" {
		t.Fatalf("expected browser.pdf payload, got %#v", pdfMap["data_base64"])
	}

	hoverResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.hover", Arguments: json.RawMessage(`{"page_id":2,"uid":"uid-hover"}`)})
	if err != nil {
		t.Fatalf("browser.hover: %v", err)
	}
	if hoverResult.(map[string]any)["source_tool"] != "browser.hover" {
		t.Fatalf("expected browser.hover to call browser.hover, got %#v", hoverResult)
	}

	dragResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.drag", Arguments: json.RawMessage(`{"page_id":2,"from_uid":"uid-a","to_uid":"uid-b"}`)})
	if err != nil {
		t.Fatalf("browser.drag: %v", err)
	}
	if dragResult.(map[string]any)["source_tool"] != "browser.drag" {
		t.Fatalf("expected browser.drag to call browser.drag, got %#v", dragResult)
	}

	fillResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.fill", Arguments: json.RawMessage(`{"page_id":2,"uid":"uid-fill","value":"value"}`)})
	if err != nil {
		t.Fatalf("browser.fill: %v", err)
	}
	if fillResult.(map[string]any)["source_tool"] != "browser.fill" {
		t.Fatalf("expected browser.fill to call browser.fill, got %#v", fillResult)
	}

	typeResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.type", Arguments: json.RawMessage(`{"page_id":2,"uid":"uid-type","text":"hello"}`)})
	if err != nil {
		t.Fatalf("browser.type: %v", err)
	}
	if typeResult.(map[string]any)["source_tool"] != "browser.fill" {
		t.Fatalf("expected browser.type to call browser.fill, got %#v", typeResult)
	}

	typeByRefResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.type", Arguments: json.RawMessage(`{"page_id":2,"ref":"@e3","text":"hello via ref"}`)})
	if err != nil {
		t.Fatalf("browser.type by ref: %v", err)
	}
	typeByRefMap := typeByRefResult.(map[string]any)
	typedPayload := typeByRefMap["result"].(map[string]any)
	if typedPayload["uid"] != "uid-type" {
		t.Fatalf("expected browser.type ref @e3 to resolve uid-type, got %#v", typedPayload)
	}

	pressResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.press", Arguments: json.RawMessage(`{"page_id":2,"key":"Escape"}`)})
	if err != nil {
		t.Fatalf("browser.press: %v", err)
	}
	if pressResult.(map[string]any)["source_tool"] != "browser.press_key" {
		t.Fatalf("expected browser.press to call browser.press_key, got %#v", pressResult)
	}

	submitResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.submit", Arguments: json.RawMessage(`{"page_id":2}`)})
	if err != nil {
		t.Fatalf("browser.submit: %v", err)
	}
	if submitResult.(map[string]any)["source_tool"] != "browser.press_key" {
		t.Fatalf("expected browser.submit to call browser.press_key, got %#v", submitResult)
	}

	selectResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.select", Arguments: json.RawMessage(`{"page_id":2,"uid":"uid-select","value":"option-a"}`)})
	if err != nil {
		t.Fatalf("browser.select: %v", err)
	}
	if selectResult.(map[string]any)["source_tool"] != "browser.fill" {
		t.Fatalf("expected browser.select to call browser.fill, got %#v", selectResult)
	}

	scrollResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.scroll", Arguments: json.RawMessage(`{"page_id":2,"dx":0,"dy":600,"behavior":"smooth"}`)})
	if err != nil {
		t.Fatalf("browser.scroll: %v", err)
	}
	if scrollResult.(map[string]any)["source_tool"] != "browser.evaluate_script" {
		t.Fatalf("expected browser.scroll to call browser.evaluate_script, got %#v", scrollResult)
	}

	scrollIntoViewResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.scroll_into_view", Arguments: json.RawMessage(`{"page_id":2,"selector":"button[type=submit]","behavior":"smooth","block":"center","inline":"nearest"}`)})
	if err != nil {
		t.Fatalf("browser.scroll_into_view: %v", err)
	}
	if scrollIntoViewResult.(map[string]any)["source_tool"] != "browser.evaluate_script" {
		t.Fatalf("expected browser.scroll_into_view to call browser.evaluate_script, got %#v", scrollIntoViewResult)
	}

	cookieListResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.cookies.list", Arguments: json.RawMessage(`{"page_id":2}`)})
	if err != nil {
		t.Fatalf("browser.cookies.list: %v", err)
	}
	cookieListMap := cookieListResult.(map[string]any)
	if cookieListMap["count"] != 1 {
		t.Fatalf("expected one cookie, got %#v", cookieListMap)
	}
	cookies := cookieListMap["cookies"].([]any)
	if len(cookies) != 1 {
		t.Fatalf("expected one cookie entry, got %#v", cookies)
	}

	cookieSetResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.cookies.set", Arguments: json.RawMessage(`{"page_id":2,"name":"session_hint","value":"enabled","path":"/","same_site":"Lax"}`)})
	if err != nil {
		t.Fatalf("browser.cookies.set: %v", err)
	}
	if cookieSetResult.(map[string]any)["source_tool"] != "browser.evaluate_script" {
		t.Fatalf("expected browser.cookies.set to call browser.evaluate_script, got %#v", cookieSetResult)
	}

	cookieDeleteResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.cookies.delete", Arguments: json.RawMessage(`{"page_id":2,"name":"session_hint","path":"/"}`)})
	if err != nil {
		t.Fatalf("browser.cookies.delete: %v", err)
	}
	if cookieDeleteResult.(map[string]any)["source_tool"] != "browser.evaluate_script" {
		t.Fatalf("expected browser.cookies.delete to call browser.evaluate_script, got %#v", cookieDeleteResult)
	}

	storageInspectResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.storage.inspect", Arguments: json.RawMessage(`{"page_id":2,"scopes":["local","session"]}`)})
	if err != nil {
		t.Fatalf("browser.storage.inspect: %v", err)
	}
	storageInspectMap := storageInspectResult.(map[string]any)
	storagePayload := storageInspectMap["storage"].(map[string]any)
	if _, ok := storagePayload["local"]; !ok {
		t.Fatalf("expected local storage in inspect result, got %#v", storageInspectResult)
	}

	storageSetResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.storage.set", Arguments: json.RawMessage(`{"page_id":2,"scope":"local","key":"theme","value":"dark"}`)})
	if err != nil {
		t.Fatalf("browser.storage.set: %v", err)
	}
	if storageSetResult.(map[string]any)["source_tool"] != "browser.evaluate_script" {
		t.Fatalf("expected browser.storage.set to call browser.evaluate_script, got %#v", storageSetResult)
	}

	storageDeleteResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.storage.delete", Arguments: json.RawMessage(`{"page_id":2,"scope":"local","key":"theme"}`)})
	if err != nil {
		t.Fatalf("browser.storage.delete: %v", err)
	}
	if storageDeleteResult.(map[string]any)["source_tool"] != "browser.evaluate_script" {
		t.Fatalf("expected browser.storage.delete to call browser.evaluate_script, got %#v", storageDeleteResult)
	}

	storageClearResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.storage.clear", Arguments: json.RawMessage(`{"page_id":2,"scopes":["session"]}`)})
	if err != nil {
		t.Fatalf("browser.storage.clear: %v", err)
	}
	if storageClearResult.(map[string]any)["source_tool"] != "browser.evaluate_script" {
		t.Fatalf("expected browser.storage.clear to call browser.evaluate_script, got %#v", storageClearResult)
	}

	emulateResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.emulate", Arguments: json.RawMessage(`{"page_id":2,"offline":true,"timezone":"America/New_York","network_profile":"offline","geolocation":{"latitude":40.7128,"longitude":-74.0060},"viewport":{"width":390,"height":844,"device_scale_factor":3,"mobile":true,"touch":true},"extra_headers":{"X-Debug":"1"}}`)})
	if err != nil {
		t.Fatalf("browser.emulate: %v", err)
	}
	emulateMap := emulateResult.(map[string]any)
	if emulateMap["native_supported"] != true {
		t.Fatalf("expected native emulate support, got %#v", emulateResult)
	}
	if emulateMap["js_source_tool"] != "browser.evaluate_script" {
		t.Fatalf("expected browser.emulate JS fallback to use browser.evaluate_script, got %#v", emulateResult)
	}

	tabsResult, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.tabs", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("browser.tabs: %v", err)
	}
	tabsMap := tabsResult.(map[string]any)
	server = tabsMap["server"].(map[string]any)
	if connected, _ := server["connected"].(bool); !connected {
		t.Fatalf("expected browser server to be connected after start, got %#v", server)
	}

	stopped, err := h.executeSessionWorkspaceTool(startCtx, "alice", agent.ToolCall{Name: "browser.stop", Arguments: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatalf("browser.stop: %v", err)
	}
	stoppedServer := stopped.(map[string]any)["server"].(map[string]any)
	if connected, _ := stoppedServer["connected"].(bool); connected {
		t.Fatalf("expected browser.stop to disconnect the server, got %#v", stoppedServer)
	}
}

func TestSessionPatchRejectsUnknownBrowserProfile(t *testing.T) {
	store := session.New(10)
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:      "test-model",
		MCPManager: &mcp.Manager{},
		BrowserConfig: config.BrowserConfig{
			Profiles: []config.BrowserProfileConfig{{Name: "work", Enabled: true, Mode: "managed"}},
		},
	})

	_, err := h.executeSessionWorkspaceTool(context.Background(), "alice", agent.ToolCall{
		Name:      "session.patch",
		Arguments: json.RawMessage(`{"session_key":"alice","browser_profile":"missing"}`),
	})
	if err == nil {
		t.Fatalf("expected unknown browser profile error")
	}
}

func TestBrowserSSRFGuardsPrivateTargets(t *testing.T) {
	store := session.New(10)
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{{
		Name:           "browser_work",
		Enabled:        true,
		Transport:      "stdio",
		Command:        "ignored",
		ToolNamePrefix: mcp.ToolPrefix("browser_work"),
		HideTools:      true,
		Kind:           "browser",
		ProfileName:    "work",
	}}, func(cfg config.MCPServerConfig) mcp.Client {
		return &fakeBrowserClient{
			profile: cfg.ProfileName,
			pages: map[int]string{
				1: "https://example.com",
				2: "http://127.0.0.1:8080/admin",
				3: "file:///tmp/private.txt",
			},
		}
	})
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:      "test-model",
		MCPManager: mgr,
		BrowserConfig: config.BrowserConfig{
			DefaultProfile: "work",
			Profiles:       []config.BrowserProfileConfig{{Name: "work", Enabled: true, Mode: "managed"}},
		},
	})

	ctx := agent.WithToolRunContext(context.Background(), "alice::tab", "")
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.open", Arguments: json.RawMessage(`{"url":"file:///tmp/private.txt"}`)}); err == nil || !strings.Contains(err.Error(), "http and https") {
		t.Fatalf("expected browser.open file target to be blocked, got %v", err)
	}
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.open", Arguments: json.RawMessage(`{"url":"data:text/html,owned"}`)}); err == nil || !strings.Contains(err.Error(), "http and https") {
		t.Fatalf("expected browser.open data target to be blocked, got %v", err)
	}
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.open", Arguments: json.RawMessage(`{"url":"http://127.0.0.1:3000/secret"}`)}); err == nil || !strings.Contains(err.Error(), "private or reserved") {
		t.Fatalf("expected browser.open private target to be blocked, got %v", err)
	}
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.snapshot", Arguments: json.RawMessage(`{"page_id":3}`)}); err == nil || !strings.Contains(err.Error(), "http and https") {
		t.Fatalf("expected browser.snapshot on file page to be blocked, got %v", err)
	}
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.snapshot", Arguments: json.RawMessage(`{"page_id":2}`)}); err == nil || !strings.Contains(err.Error(), "private or reserved") {
		t.Fatalf("expected browser.snapshot on private page to be blocked, got %v", err)
	}
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.snapshot", Arguments: json.RawMessage(`{"page_id":1}`)}); err != nil {
		t.Fatalf("expected public browser.snapshot to remain allowed, got %v", err)
	}
}

func TestBrowserHostPolicyAllowsAndBlocksConfiguredHosts(t *testing.T) {
	store := session.New(10)
	mgr := mcp.NewManagerWithFactory([]config.MCPServerConfig{{
		Name:           "browser_work",
		Enabled:        true,
		Transport:      "stdio",
		Command:        "ignored",
		ToolNamePrefix: mcp.ToolPrefix("browser_work"),
		HideTools:      true,
		Kind:           "browser",
		ProfileName:    "work",
	}}, func(cfg config.MCPServerConfig) mcp.Client {
		return &fakeBrowserClient{
			profile: cfg.ProfileName,
			pages: map[int]string{
				1: "https://example.com",
				2: "https://www.iana.org",
			},
		}
	})
	h := NewHandler(store, noopProvider{}, HandlerOptions{
		Model:      "test-model",
		MCPManager: mgr,
		BrowserConfig: config.BrowserConfig{
			DefaultProfile: "work",
			Profiles: []config.BrowserProfileConfig{{
				Name:          "work",
				Enabled:       true,
				Mode:          "managed",
				HostAllowlist: []string{"example.com"},
				HostDenylist:  []string{"www.iana.org"},
			}},
		},
	})

	ctx := agent.WithToolRunContext(context.Background(), "alice::tab", "")
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.open", Arguments: json.RawMessage(`{"url":"https://example.com"}`)}); err != nil {
		t.Fatalf("expected allowlisted browser.open to succeed, got %v", err)
	}
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.open", Arguments: json.RawMessage(`{"url":"https://www.iana.org"}`)}); err == nil || !strings.Contains(err.Error(), "denylist") {
		t.Fatalf("expected denylisted browser.open to be blocked, got %v", err)
	}
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.open", Arguments: json.RawMessage(`{"url":"https://www.example.com"}`)}); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("expected non-allowlisted browser.open to be blocked, got %v", err)
	}
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.snapshot", Arguments: json.RawMessage(`{"page_id":1}`)}); err != nil {
		t.Fatalf("expected allowlisted browser.snapshot to succeed, got %v", err)
	}
	if _, err := h.executeSessionWorkspaceTool(ctx, "alice", agent.ToolCall{Name: "browser.snapshot", Arguments: json.RawMessage(`{"page_id":2}`)}); err == nil || !strings.Contains(err.Error(), "denylist") {
		t.Fatalf("expected denylisted browser.snapshot to be blocked, got %v", err)
	}
}

type fakeBrowserClient struct {
	profile       string
	selectedPage  int
	pages         map[int]string
	canvasHTML    string
	canvasCSS     string
	canvasTitle   string
	canvasMount   string
	canvasState   map[string]any
	canvasLayout  map[string]any
	canvasVersion int
	canvasMounted bool
}

func (c *fakeBrowserClient) Initialize(context.Context) error { return nil }

func (c *fakeBrowserClient) ListTools(context.Context) ([]mcp.Tool, error) {
	return []mcp.Tool{
		{Name: "list_pages", Description: "List pages"},
		{Name: "new_page", Description: "New page"},
		{Name: "select_page", Description: "Select page"},
		{Name: "close_page", Description: "Close page"},
		{Name: "click", Description: "Click element"},
		{Name: "fill", Description: "Fill element"},
		{Name: "hover", Description: "Hover element"},
		{Name: "drag", Description: "Drag element"},
		{Name: "press_key", Description: "Press key"},
		{Name: "take_snapshot", Description: "Take snapshot"},
		{Name: "take_screenshot", Description: "Take screenshot"},
		{Name: "print_to_pdf", Description: "Print to PDF"},
		{Name: "emulate", Description: "Apply emulation"},
		{Name: "evaluate_script", Description: "Evaluate script"},
	}, nil
}

func (c *fakeBrowserClient) CallTool(_ context.Context, name string, args map[string]any) (*mcp.ToolResult, error) {
	if isFakeBrowserPageScopedTool(name) {
		if _, ok := args["pageId"]; ok {
			return nil, fmt.Errorf("tool %s received unexpected pageId argument", name)
		}
		if c.selectedPage == 0 {
			return nil, fmt.Errorf("tool %s requires a selected page", name)
		}
	}
	text := ""
	switch name {
	case "list_pages":
		text = c.listPagesJSON()
	case "new_page":
		text = fmt.Sprintf(`{"opened":true,"url":%q}`, args["url"])
	case "select_page":
		c.selectedPage = fakeBrowserArgInt(args["pageId"])
		text = fmt.Sprintf(`{"selected":true,"pageId":%v}`, args["pageId"])
	case "close_page":
		text = fmt.Sprintf(`{"closed":true,"pageId":%v}`, args["pageId"])
	case "click":
		doubleClick, _ := args["dblClick"].(bool)
		text = fmt.Sprintf(`{"clicked":true,"uid":%q,"double":%v}`, fmt.Sprint(args["uid"]), doubleClick)
	case "fill":
		text = fmt.Sprintf(`{"filled":true,"uid":%q,"value":%q}`, fmt.Sprint(args["uid"]), fmt.Sprint(args["value"]))
	case "hover":
		text = fmt.Sprintf(`{"hovered":true,"uid":%q}`, fmt.Sprint(args["uid"]))
	case "drag":
		text = fmt.Sprintf(`{"dragged":true,"from":%q,"to":%q}`, fmt.Sprint(args["from_uid"]), fmt.Sprint(args["to_uid"]))
	case "press_key":
		text = fmt.Sprintf(`{"pressed":true,"key":%q}`, fmt.Sprint(args["key"]))
	case "take_snapshot":
		text = strings.Join([]string{
			`uid=uid-root root "root"`,
			`  uid=uid-click button "Submit"`,
			`  uid=uid-type textbox "Search" value="existing"`,
		}, "\n")
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: text}}}, nil
	case "take_screenshot":
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "image", Data: fakeBrowserPNGBase64, MimeType: "image/png"}}}, nil
	case "print_to_pdf":
		return &mcp.ToolResult{Content: []mcp.Content{{Type: "resource", Data: "cGRmLWJ5dGVz", MimeType: "application/pdf"}}}, nil
	case "emulate":
		text = fmt.Sprintf(`{"emulated":true,"networkConditions":%q,"viewport":%q}`, fmt.Sprint(args["networkConditions"]), fmt.Sprint(args["viewport"]))
	case "evaluate_script":
		fn := fmt.Sprint(args["function"])
		switch {
		case strings.Contains(fn, "koios:canvas:push"):
			payload := fakeBrowserFirstArgMap(args)
			mode, _ := payload["mode"].(string)
			if html, ok := payload["html"].(string); ok {
				if mode == "append" {
					c.canvasHTML += html
				} else {
					c.canvasHTML = html
				}
			}
			if css, ok := payload["css"].(string); ok {
				c.canvasCSS = css
			}
			if title, ok := payload["title"].(string); ok {
				c.canvasTitle = title
			}
			if mount, ok := payload["mount"].(string); ok {
				c.canvasMount = mount
			}
			if state := fakeBrowserMap(payload["state"]); state != nil {
				if mode == "merge" {
					c.canvasState = fakeBrowserMergeMap(c.canvasState, state)
				} else {
					c.canvasState = state
				}
			}
			if layout := fakeBrowserMap(payload["layout"]); layout != nil {
				c.canvasLayout = layout
			}
			if c.canvasState == nil {
				c.canvasState = map[string]any{}
			}
			c.canvasMounted = true
			c.canvasVersion++
			text = fakeBrowserJSON(map[string]any{"marker": "koios:canvas:push", "ok": true, "mounted": true, "title": c.canvasTitle, "mount": c.canvasMount, "version": c.canvasVersion, "html": c.canvasHTML, "css": c.canvasCSS, "state": c.canvasState, "layout": c.canvasLayout, "child_count": fakeBrowserChildCount(c.canvasHTML)})
		case strings.Contains(fn, "koios:canvas:eval"):
			if !c.canvasMounted {
				text = `{"marker":"koios:canvas:eval","ok":false,"mounted":false,"error":"canvas has not been pushed"}`
				break
			}
			payload := fakeBrowserFirstArgMap(args)
			script, _ := payload["script"].(string)
			evalArgs := fakeBrowserMap(payload["args"])
			var value any
			if strings.Contains(script, "appendHTML(args.html)") {
				if html, ok := evalArgs["html"].(string); ok {
					c.canvasHTML += html
				}
			}
			if strings.Contains(script, "replaceHTML(args.html)") {
				if html, ok := evalArgs["html"].(string); ok {
					c.canvasHTML = html
				}
			}
			if strings.Contains(script, "mergeState(args.patch)") {
				if patch := fakeBrowserMap(evalArgs["patch"]); patch != nil {
					c.canvasState = fakeBrowserMergeMap(c.canvasState, patch)
				}
				value = c.canvasState
			}
			if strings.Contains(script, "setLayout(args.layout)") {
				if layout := fakeBrowserMap(evalArgs["layout"]); layout != nil {
					c.canvasLayout = layout
				}
				value = c.canvasLayout
			}
			if c.canvasState == nil {
				c.canvasState = map[string]any{}
			}
			c.canvasVersion++
			text = fakeBrowserJSON(map[string]any{"marker": "koios:canvas:eval", "ok": true, "mounted": true, "value": value, "state": c.canvasState, "layout": c.canvasLayout, "version": c.canvasVersion, "html": c.canvasHTML, "text_content": strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(c.canvasHTML, "<", " "), ">", " ")), "mount": c.canvasMount})
		case strings.Contains(fn, "koios:canvas:snapshot"):
			text = fakeBrowserJSON(map[string]any{"marker": "koios:canvas:snapshot", "ok": true, "mounted": c.canvasMounted, "version": c.canvasVersion, "state": c.canvasState, "layout": c.canvasLayout, "html": c.canvasHTML, "css": c.canvasCSS, "title": c.canvasTitle, "mount": c.canvasMount, "child_count": fakeBrowserChildCount(c.canvasHTML)})
		case strings.Contains(fn, "koios:canvas:reset"):
			c.canvasHTML = ""
			c.canvasCSS = ""
			c.canvasTitle = ""
			c.canvasMount = ""
			c.canvasState = nil
			c.canvasLayout = nil
			c.canvasVersion = 0
			c.canvasMounted = false
			text = `{"marker":"koios:canvas:reset","ok":true,"mounted":false}`
		case strings.Contains(fn, "koios:cookies:list"):
			text = `{"marker":"koios:cookies:list","cookies":[{"name":"session_hint","value":"enabled"}]}`
		case strings.Contains(fn, "koios:cookies:set"):
			text = `{"marker":"koios:cookies:set","ok":true}`
		case strings.Contains(fn, "koios:cookies:delete"):
			text = `{"marker":"koios:cookies:delete","ok":true}`
		case strings.Contains(fn, "koios:storage:inspect"):
			text = `{"marker":"koios:storage:inspect","local":[{"key":"theme","value":"dark"}],"session":[{"key":"draft","value":"1"}]}`
		case strings.Contains(fn, "koios:storage:set"):
			text = `{"marker":"koios:storage:set","ok":true}`
		case strings.Contains(fn, "koios:storage:delete"):
			text = `{"marker":"koios:storage:delete","ok":true}`
		case strings.Contains(fn, "koios:storage:clear"):
			text = `{"marker":"koios:storage:clear","ok":true}`
		case strings.Contains(fn, "koios:emulation:apply"):
			text = `{"marker":"koios:emulation:apply","offline":true,"timezone":"America/New_York"}`
		default:
			text = fmt.Sprintf(`{"evaluated":true,"scroll":%v,"scroll_into_view":%v,"coords":%v}`, strings.Contains(fn, "scrollBy"), strings.Contains(fn, "scrollIntoView"), strings.Contains(fn, "elementFromPoint"))
		}
	default:
		text = fmt.Sprintf(`{"tool":%q}`, name)
	}
	return &mcp.ToolResult{Content: []mcp.Content{{Type: "text", Text: text}}}, nil
}

func (c *fakeBrowserClient) Close() error { return nil }

func (c *fakeBrowserClient) listPagesJSON() string {
	if len(c.pages) == 0 {
		return `[{"pageId":1,"url":"https://example.com"},{"pageId":2,"url":"https://www.example.com"}]`
	}
	parts := make([]string, 0, len(c.pages))
	for pageID := 1; pageID <= len(c.pages)+1; pageID++ {
		pageURL, ok := c.pages[pageID]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf(`{"pageId":%d,"url":%q}`, pageID, pageURL))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func isFakeBrowserPageScopedTool(name string) bool {
	switch name {
	case "click", "fill", "hover", "drag", "press_key", "take_snapshot", "take_screenshot", "print_to_pdf", "evaluate_script", "emulate":
		return true
	default:
		return false
	}
}

func fakeBrowserArgInt(value any) int {
	switch typed := value.(type) {
	case int:
		return typed
	case int32:
		return int(typed)
	case int64:
		return int(typed)
	case float64:
		return int(typed)
	default:
		return 0
	}
}

const fakeBrowserPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAAEElEQVR4nGL6z8AACAAA//8DCQECWLbVUAAAAABJRU5ErkJggg=="

func fakeBrowserFirstArgMap(args map[string]any) map[string]any {
	raw, ok := args["args"].([]any)
	if !ok || len(raw) == 0 {
		return map[string]any{}
	}
	return fakeBrowserMap(raw[0])
}

func fakeBrowserMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return nil
}

func fakeBrowserMergeMap(base map[string]any, patch map[string]any) map[string]any {
	if base == nil {
		base = map[string]any{}
	}
	for key, value := range patch {
		base[key] = value
	}
	return base
}

func fakeBrowserChildCount(html string) int {
	if strings.TrimSpace(html) == "" {
		return 0
	}
	return 1
}

func fakeBrowserJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
