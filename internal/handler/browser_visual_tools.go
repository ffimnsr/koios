package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/agent"
	"github.com/ffimnsr/koios/internal/artifacts"
)

const (
	screenRecordMaxFrames        = 24
	screenRecordInlineMaxBytes   = 256 * 1024
	screenRecordArtifactMaxBytes = 2 * 1024 * 1024
)

type browserCanvasState struct {
	HTML     string
	CSS      string
	Title    string
	Mount    string
	State    any
	Layout   any
	LastEval any
	Version  int
	Mounted  bool
}

type browserCanvasLayoutSpec struct {
	Theme    string                     `json:"theme"`
	Title    string                     `json:"title"`
	Subtitle string                     `json:"subtitle"`
	Panels   []browserCanvasLayoutPanel `json:"panels"`
}

type browserCanvasLayoutPanel struct {
	ID     string   `json:"id"`
	Title  string   `json:"title"`
	Body   string   `json:"body"`
	Accent string   `json:"accent"`
	Footer string   `json:"footer"`
	Items  []string `json:"items"`
}

type browserRecordedFrame struct {
	Image image.Image
	Delay int
}

func browserCanvasStateKey(sessionKey, profile string, pageID int) string {
	return browserSnapshotStateKey(sessionKey, profile, pageID)
}

func (h *Handler) storeBrowserCanvasState(sessionKey, profile string, pageID int, state browserCanvasState) browserCanvasState {
	key := browserCanvasStateKey(sessionKey, profile, pageID)
	h.browserCanvasMu.Lock()
	defer h.browserCanvasMu.Unlock()
	copyState := state
	h.browserCanvas[key] = &copyState
	return copyState
}

func (h *Handler) loadBrowserCanvasState(sessionKey, profile string, pageID int) (browserCanvasState, bool) {
	key := browserCanvasStateKey(sessionKey, profile, pageID)
	h.browserCanvasMu.Lock()
	defer h.browserCanvasMu.Unlock()
	state := h.browserCanvas[key]
	if state == nil {
		return browserCanvasState{}, false
	}
	return *state, true
}

func (h *Handler) clearBrowserCanvasState(sessionKey, profile string, pageID int) {
	key := browserCanvasStateKey(sessionKey, profile, pageID)
	h.browserCanvasMu.Lock()
	defer h.browserCanvasMu.Unlock()
	delete(h.browserCanvas, key)
}

func decodeBrowserRecordingFrame(dataBase64, label string) (image.Image, error) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(dataBase64))
	if err != nil {
		return nil, fmt.Errorf("decode %s image: %w", label, err)
	}
	img, _, err := image.Decode(bytes.NewReader(decoded))
	if err != nil {
		return nil, fmt.Errorf("decode %s image payload: %w", label, err)
	}
	return img, nil
}

func encodeBrowserRecordingGIF(frames []browserRecordedFrame) (string, int, int, error) {
	if len(frames) == 0 {
		return "", 0, 0, fmt.Errorf("no recording frames captured")
	}
	gifFrames := make([]*image.Paletted, 0, len(frames))
	delays := make([]int, 0, len(frames))
	maxWidth := 0
	maxHeight := 0
	for _, frame := range frames {
		bounds := frame.Image.Bounds()
		if bounds.Dx() > maxWidth {
			maxWidth = bounds.Dx()
		}
		if bounds.Dy() > maxHeight {
			maxHeight = bounds.Dy()
		}
		paletted := image.NewPaletted(bounds, palette.Plan9)
		draw.FloydSteinberg.Draw(paletted, bounds, frame.Image, bounds.Min)
		gifFrames = append(gifFrames, paletted)
		delay := frame.Delay / 10
		if delay <= 0 {
			delay = 1
		}
		delays = append(delays, delay)
	}
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: gifFrames, Delay: delays, LoopCount: 0}); err != nil {
		return "", 0, 0, fmt.Errorf("encode recording gif: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), maxWidth, maxHeight, nil
}

func decodedBase64Size(encoded string) int {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return 0
	}
	return base64.StdEncoding.DecodedLen(len(encoded))
}

func normalizeBrowserCaptureFormat(format string) (string, error) {
	format = strings.ToLower(strings.TrimSpace(format))
	if format == "" {
		format = "png"
	}
	switch format {
	case "png", "jpeg", "webp":
		return format, nil
	default:
		return "", fmt.Errorf("unsupported capture format %q", format)
	}
}

func normalizeCanvasTheme(theme string) string {
	theme = strings.ToLower(strings.TrimSpace(theme))
	switch theme {
	case "", "light":
		return "light"
	case "dark", "warm", "cool":
		return theme
	default:
		return "light"
	}
}

func renderCanvasLayoutHTML(layout browserCanvasLayoutSpec) string {
	if len(layout.Panels) == 0 && strings.TrimSpace(layout.Title) == "" && strings.TrimSpace(layout.Subtitle) == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString(`<section class="koios-layout">`)
	if strings.TrimSpace(layout.Title) != "" || strings.TrimSpace(layout.Subtitle) != "" {
		b.WriteString(`<header class="koios-layout__header">`)
		if strings.TrimSpace(layout.Title) != "" {
			b.WriteString(`<h1 class="koios-layout__title">`)
			b.WriteString(html.EscapeString(layout.Title))
			b.WriteString(`</h1>`)
		}
		if strings.TrimSpace(layout.Subtitle) != "" {
			b.WriteString(`<p class="koios-layout__subtitle">`)
			b.WriteString(html.EscapeString(layout.Subtitle))
			b.WriteString(`</p>`)
		}
		b.WriteString(`</header>`)
	}
	b.WriteString(`<div class="koios-layout__grid">`)
	for _, panel := range layout.Panels {
		b.WriteString(`<article class="koios-layout__panel"`)
		if accent := strings.TrimSpace(panel.Accent); accent != "" {
			b.WriteString(` data-accent="`)
			b.WriteString(html.EscapeString(accent))
			b.WriteString(`"`)
		}
		if id := strings.TrimSpace(panel.ID); id != "" {
			b.WriteString(` id="`)
			b.WriteString(html.EscapeString(id))
			b.WriteString(`"`)
		}
		b.WriteString(`>`)
		if strings.TrimSpace(panel.Title) != "" {
			b.WriteString(`<h2 class="koios-layout__panel-title">`)
			b.WriteString(html.EscapeString(panel.Title))
			b.WriteString(`</h2>`)
		}
		if strings.TrimSpace(panel.Body) != "" {
			b.WriteString(`<p class="koios-layout__panel-body">`)
			b.WriteString(html.EscapeString(panel.Body))
			b.WriteString(`</p>`)
		}
		if len(panel.Items) > 0 {
			b.WriteString(`<ul class="koios-layout__panel-items">`)
			for _, item := range panel.Items {
				if strings.TrimSpace(item) == "" {
					continue
				}
				b.WriteString(`<li>`)
				b.WriteString(html.EscapeString(item))
				b.WriteString(`</li>`)
			}
			b.WriteString(`</ul>`)
		}
		if strings.TrimSpace(panel.Footer) != "" {
			b.WriteString(`<footer class="koios-layout__panel-footer">`)
			b.WriteString(html.EscapeString(panel.Footer))
			b.WriteString(`</footer>`)
		}
		b.WriteString(`</article>`)
	}
	b.WriteString(`</div></section>`)
	return b.String()
}

func renderCanvasLayoutCSS(layout browserCanvasLayoutSpec) string {
	background := "#f6efe6"
	foreground := "#1f2521"
	panel := "rgba(255,255,255,0.9)"
	border := "rgba(31,37,33,0.12)"
	shadow := "0 18px 50px rgba(31,37,33,0.12)"
	muted := "#5d675f"
	switch normalizeCanvasTheme(layout.Theme) {
	case "dark":
		background = "#17191d"
		foreground = "#f2efe8"
		panel = "rgba(32,35,41,0.96)"
		border = "rgba(242,239,232,0.12)"
		shadow = "0 22px 60px rgba(0,0,0,0.32)"
		muted = "#c7c1b5"
	case "cool":
		background = "#e7f1f7"
		foreground = "#162836"
		panel = "rgba(255,255,255,0.92)"
		border = "rgba(22,40,54,0.12)"
		shadow = "0 20px 54px rgba(22,40,54,0.12)"
		muted = "#4e6473"
	case "warm":
		background = "#f7eadf"
		foreground = "#33211a"
		panel = "rgba(255,250,245,0.92)"
		border = "rgba(51,33,26,0.12)"
		shadow = "0 18px 50px rgba(95,56,33,0.16)"
		muted = "#7d6053"
	}
	return fmt.Sprintf(`
.koios-layout { font-family: "Iowan Old Style", "Palatino Linotype", serif; min-height: 100%%; color: %s; background: radial-gradient(circle at top left, rgba(255,255,255,0.45), transparent 40%%), linear-gradient(180deg, %s 0%%, %s 100%%); border-radius: 28px; padding: 24px; }
.koios-layout__header { margin-bottom: 18px; }
.koios-layout__title { margin: 0; font-size: 2rem; line-height: 1.1; }
.koios-layout__subtitle { margin: 8px 0 0; color: %s; font-size: 1rem; }
.koios-layout__grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 16px; }
.koios-layout__panel { background: %s; border: 1px solid %s; border-radius: 20px; padding: 18px; box-shadow: %s; }
.koios-layout__panel[data-accent="amber"] { border-top: 4px solid #d38b21; }
.koios-layout__panel[data-accent="teal"] { border-top: 4px solid #1d8d83; }
.koios-layout__panel[data-accent="rose"] { border-top: 4px solid #cb5f75; }
.koios-layout__panel-title { margin: 0 0 10px; font-size: 1.1rem; }
.koios-layout__panel-body { margin: 0 0 10px; color: %s; }
.koios-layout__panel-items { margin: 0; padding-left: 18px; }
.koios-layout__panel-items li + li { margin-top: 6px; }
.koios-layout__panel-footer { margin-top: 12px; color: %s; font-size: 0.9rem; }
`, foreground, background, background, muted, panel, border, shadow, muted, muted)
}

func decodeCanvasLayoutSpec(value any) (browserCanvasLayoutSpec, bool) {
	if value == nil {
		return browserCanvasLayoutSpec{}, false
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return browserCanvasLayoutSpec{}, false
	}
	var layout browserCanvasLayoutSpec
	if err := json.Unmarshal(raw, &layout); err != nil {
		return browserCanvasLayoutSpec{}, false
	}
	return layout, true
}

func encodeScreenRecordingArtifactContent(payload map[string]any) (string, error) {
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (h *Handler) persistScreenRecordingArtifact(ctx context.Context, peerID, title string, labels []string, payload map[string]any) (*artifacts.Artifact, error) {
	if h.artifactStore == nil {
		return nil, fmt.Errorf("persist_artifact requested but artifacts are not enabled")
	}
	content, err := encodeScreenRecordingArtifactContent(payload)
	if err != nil {
		return nil, fmt.Errorf("encode screen recording artifact: %w", err)
	}
	if strings.TrimSpace(title) == "" {
		title = fmt.Sprintf("Screen recording %s", time.Now().UTC().Format(time.RFC3339))
	}
	return h.artifactStore.Create(ctx, peerID, artifacts.Input{
		Kind:    "screen_recording",
		Title:   title,
		Content: content,
		Labels:  labels,
	})
}

func browserCaptureTarget(targetUID string, fullPage bool) string {
	if fullPage {
		return "page"
	}
	if strings.TrimSpace(targetUID) != "" {
		return "element"
	}
	return "viewport"
}

func (h *Handler) captureBrowserScreenshot(ctx context.Context, peerID, sessionKey string, pageID int, uid, ref string, fullPage bool, format string, quality int) (string, string, string, error) {
	profile, _, err := h.ensureBrowserServer(ctx, sessionKey)
	if err != nil {
		return "", "", "", err
	}
	if fullPage && (strings.TrimSpace(uid) != "" || strings.TrimSpace(ref) != "") {
		return "", "", "", fmt.Errorf("screen capture cannot combine full_page with uid or ref targeting")
	}
	captureArgs := map[string]any{"format": format}
	resolvedUID := ""
	if quality > 0 {
		captureArgs["quality"] = quality
	}
	if fullPage {
		captureArgs["fullPage"] = true
	} else if strings.TrimSpace(uid) != "" || strings.TrimSpace(ref) != "" {
		resolvedUID, err = h.resolveBrowserActionUID(sessionKey, strings.TrimSpace(profile.Name), pageID, uid, ref)
		if err != nil {
			return "", "", "", err
		}
		captureArgs["uid"] = resolvedUID
	}
	result, err := h.invokeBrowserPageToolResult(ctx, peerID, sessionKey, pageID, "browser.take_screenshot", captureArgs)
	if err != nil {
		return "", "", "", err
	}
	imageData, mimeType, err := extractBrowserImageContent(result)
	if err != nil {
		return "", "", "", err
	}
	return imageData, mimeType, resolvedUID, nil
}

func (h *Handler) executeScreenSnapshotTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	var args struct {
		PageID   int    `json:"page_id"`
		UID      string `json:"uid"`
		Ref      string `json:"ref"`
		FullPage bool   `json:"full_page"`
		Format   string `json:"format"`
		Quality  int    `json:"quality"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	format, err := normalizeBrowserCaptureFormat(args.Format)
	if err != nil {
		return nil, err
	}
	sessionKey := h.browserSessionKey(ctx, peerID)
	profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	imageData, mimeType, resolvedUID, err := h.captureBrowserScreenshot(ctx, peerID, sessionKey, args.PageID, args.UID, args.Ref, args.FullPage, format, args.Quality)
	if err == errUnhandledTool {
		return nil, fmt.Errorf("active browser profile %q does not expose browser.take_screenshot", strings.TrimSpace(profile.Name))
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"ok":             true,
		"active_profile": strings.TrimSpace(profile.Name),
		"page_id":        args.PageID,
		"target":         browserCaptureTarget(resolvedUID, args.FullPage),
		"uid":            resolvedUID,
		"format":         format,
		"mime_type":      mimeType,
		"data_base64":    imageData,
		"server":         browserStatusPayload(profile.Name, server),
		"source_tool":    "browser.take_screenshot",
		"backend":        "browser",
		"tool":           "screen.snapshot",
		"full_page":      args.FullPage,
	}, nil
}

func (h *Handler) executeScreenRecordTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	var args struct {
		PageID          int      `json:"page_id"`
		UID             string   `json:"uid"`
		Ref             string   `json:"ref"`
		FullPage        bool     `json:"full_page"`
		DurationMS      int      `json:"duration_ms"`
		IntervalMS      int      `json:"interval_ms"`
		Format          string   `json:"format"`
		PersistArtifact bool     `json:"persist_artifact"`
		ArtifactTitle   string   `json:"artifact_title"`
		ArtifactLabels  []string `json:"artifact_labels"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	format := strings.ToLower(strings.TrimSpace(args.Format))
	if format == "" {
		format = "gif"
	}
	if format != "gif" {
		return nil, fmt.Errorf("unsupported screen recording format %q", args.Format)
	}
	if args.DurationMS <= 0 {
		args.DurationMS = 1500
	}
	if args.IntervalMS <= 0 {
		args.IntervalMS = 500
	}
	if args.DurationMS < 100 || args.DurationMS > 30000 {
		return nil, fmt.Errorf("duration_ms must be between 100 and 30000")
	}
	if args.IntervalMS < 100 || args.IntervalMS > 5000 {
		return nil, fmt.Errorf("interval_ms must be between 100 and 5000")
	}
	if args.FullPage && (strings.TrimSpace(args.UID) != "" || strings.TrimSpace(args.Ref) != "") {
		return nil, fmt.Errorf("screen.record cannot combine full_page with uid or ref targeting")
	}
	sessionKey := h.browserSessionKey(ctx, peerID)
	profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	frameCount := args.DurationMS / args.IntervalMS
	if args.DurationMS%args.IntervalMS != 0 {
		frameCount++
	}
	if frameCount <= 0 {
		frameCount = 1
	}
	if frameCount > screenRecordMaxFrames {
		return nil, fmt.Errorf("screen recordings are capped at %d frames; shorten duration_ms or increase interval_ms", screenRecordMaxFrames)
	}
	frames := make([]browserRecordedFrame, 0, frameCount)
	resolvedUID := ""
	for i := 0; i < frameCount; i++ {
		if i > 0 {
			timer := time.NewTimer(time.Duration(args.IntervalMS) * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		imageData, _, captureUID, err := h.captureBrowserScreenshot(ctx, peerID, sessionKey, args.PageID, args.UID, args.Ref, args.FullPage, "png", 0)
		if err == errUnhandledTool {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.take_screenshot", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		if resolvedUID == "" {
			resolvedUID = captureUID
		}
		img, err := decodeBrowserRecordingFrame(imageData, "screen recording")
		if err != nil {
			return nil, err
		}
		frames = append(frames, browserRecordedFrame{Image: img, Delay: args.IntervalMS})
	}
	gifData, width, height, err := encodeBrowserRecordingGIF(frames)
	if err != nil {
		return nil, err
	}
	rawBytes := decodedBase64Size(gifData)
	if rawBytes > screenRecordArtifactMaxBytes {
		return nil, fmt.Errorf("screen recording exceeds the %d byte artifact budget; shorten the capture", screenRecordArtifactMaxBytes)
	}
	response := map[string]any{
		"ok":                    true,
		"active_profile":        strings.TrimSpace(profile.Name),
		"page_id":               args.PageID,
		"target":                browserCaptureTarget(resolvedUID, args.FullPage),
		"uid":                   resolvedUID,
		"format":                format,
		"mime_type":             "image/gif",
		"data_base64":           gifData,
		"frame_count":           len(frames),
		"duration_ms":           args.DurationMS,
		"interval_ms":           args.IntervalMS,
		"inline_data_max_bytes": screenRecordInlineMaxBytes,
		"artifact_max_bytes":    screenRecordArtifactMaxBytes,
		"raw_bytes":             rawBytes,
		"width":                 width,
		"height":                height,
		"server":                browserStatusPayload(profile.Name, server),
		"source_tool":           "browser.take_screenshot",
		"backend":               "browser",
		"tool":                  "screen.record",
		"full_page":             args.FullPage,
	}
	artifactPayload := map[string]any{
		"tool":           "screen.record",
		"mime_type":      "image/gif",
		"data_base64":    gifData,
		"page_id":        args.PageID,
		"active_profile": strings.TrimSpace(profile.Name),
		"target":         browserCaptureTarget(resolvedUID, args.FullPage),
		"uid":            resolvedUID,
		"frame_count":    len(frames),
		"duration_ms":    args.DurationMS,
		"interval_ms":    args.IntervalMS,
		"raw_bytes":      rawBytes,
		"width":          width,
		"height":         height,
	}
	if args.PersistArtifact {
		labels := append([]string{"screen", "recording"}, args.ArtifactLabels...)
		artifact, err := h.persistScreenRecordingArtifact(ctx, peerID, args.ArtifactTitle, labels, artifactPayload)
		if err != nil {
			return nil, err
		}
		response["artifact"] = artifact
	}
	if rawBytes > screenRecordInlineMaxBytes {
		if !args.PersistArtifact {
			return nil, fmt.Errorf("screen recording exceeds the %d byte inline budget; retry with persist_artifact=true or shorten the capture", screenRecordInlineMaxBytes)
		}
		response["data_base64"] = ""
		response["data_in_artifact_only"] = true
	} else {
		response["data_in_artifact_only"] = false
	}
	return response, nil
}

func buildBrowserCanvasPushFunction() string {
	return `async (payload) => {
		const marker = "koios:canvas:push";
		const rootId = "__koios_canvas_root";
		const styleId = "__koios_canvas_style";
		const store = window.__koiosCanvasStore = window.__koiosCanvasStore || { state: {}, version: 0, html: "", css: "", title: "", mount: "overlay", layout: null };
		let root = document.getElementById(rootId);
		if (!root) {
			root = document.createElement("div");
			root.id = rootId;
			root.setAttribute("data-koios-canvas-root", "true");
			document.body.appendChild(root);
		}
		let styleEl = document.getElementById(styleId);
		if (!styleEl) {
			styleEl = document.createElement("style");
			styleEl.id = styleId;
			document.head.appendChild(styleEl);
		}
		const mount = (payload && payload.mount) || store.mount || "overlay";
		const mode = (payload && payload.mode) || "replace";
		root.dataset.koiosCanvasMount = mount;
		root.style.position = mount === "overlay" ? "fixed" : "relative";
		root.style.inset = mount === "overlay" ? "0" : "auto";
		root.style.zIndex = "2147483000";
		root.style.overflow = "auto";
		root.style.pointerEvents = "auto";
		root.style.background = mount === "overlay" ? "rgba(255,255,255,0.94)" : "transparent";
		root.style.boxSizing = "border-box";
		root.style.padding = "16px";
		if (payload && Object.prototype.hasOwnProperty.call(payload, "html")) {
			if (mode === "append") {
				root.insertAdjacentHTML("beforeend", String(payload.html || ""));
			} else {
				root.innerHTML = String(payload.html || "");
			}
		}
		const baseCSS = "[data-koios-canvas-root=\\"true\\"] *{box-sizing:border-box;}";
		if (payload && Object.prototype.hasOwnProperty.call(payload, "css")) {
			styleEl.textContent = baseCSS + "\n" + String(payload.css || "");
			store.css = String(payload.css || "");
		} else if (!styleEl.textContent) {
			styleEl.textContent = baseCSS;
		}
		if (payload && payload.state && typeof payload.state === "object") {
			if (mode === "merge" && store.state && typeof store.state === "object" && !Array.isArray(store.state)) {
				store.state = Object.assign({}, store.state, payload.state);
			} else {
				store.state = payload.state;
			}
		}
		if (payload && typeof payload.title === "string") {
			store.title = payload.title;
		}
		if (payload && Object.prototype.hasOwnProperty.call(payload, "layout")) {
			store.layout = payload.layout;
		}
		store.mount = mount;
		store.html = root.innerHTML;
		store.version = (store.version || 0) + 1;
		if (payload && typeof payload.script === "string" && payload.script.trim() !== "") {
			const fn = new Function("canvas", "state", "payload", payload.script);
			await fn({ root, document, window }, store.state, payload);
			store.html = root.innerHTML;
		}
		return { marker, ok: true, mounted: true, root_id: rootId, title: store.title || "", mount: store.mount, version: store.version, html: store.html, css: store.css || "", state: store.state || {}, layout: store.layout || null, child_count: root.childElementCount };
	}`
}

func buildBrowserCanvasEvalFunction() string {
	return `async (payload) => {
		const marker = "koios:canvas:eval";
		const rootId = "__koios_canvas_root";
		const styleId = "__koios_canvas_style";
		const root = document.getElementById(rootId);
		if (!root) {
			return { marker, ok: false, mounted: false, error: "canvas has not been pushed" };
		}
		let styleEl = document.getElementById(styleId);
		if (!styleEl) {
			styleEl = document.createElement("style");
			styleEl.id = styleId;
			document.head.appendChild(styleEl);
		}
		const store = window.__koiosCanvasStore = window.__koiosCanvasStore || { state: {}, version: 0, html: root.innerHTML, css: styleEl.textContent || "", mount: root.dataset.koiosCanvasMount || "overlay", layout: null };
		const canvas = {
			root,
			document,
			window,
			state: store.state || {},
			layout: store.layout || null,
			setState(next) {
				store.state = next;
				canvas.state = store.state;
				return canvas.state;
			},
			setLayout(next) {
				store.layout = next;
				canvas.layout = store.layout;
				return canvas.layout;
			},
			mergeState(patch) {
				if (patch && typeof patch === "object" && !Array.isArray(patch)) {
					store.state = Object.assign({}, store.state || {}, patch);
					canvas.state = store.state;
				}
				return canvas.state;
			},
			replaceHTML(html) {
				root.innerHTML = String(html ?? "");
				store.html = root.innerHTML;
				return store.html;
			},
			appendHTML(html) {
				root.insertAdjacentHTML("beforeend", String(html ?? ""));
				store.html = root.innerHTML;
				return store.html;
			},
			setCSS(css) {
				styleEl.textContent = String(css ?? "");
				store.css = styleEl.textContent;
				return store.css;
			},
			clear() {
				root.innerHTML = "";
				store.html = "";
				return true;
			},
			snapshot() {
				const rect = root.getBoundingClientRect();
				return { mounted: true, html: root.innerHTML, text_content: root.textContent || "", state: store.state || {}, layout: store.layout || null, version: store.version || 0, mount: store.mount || "overlay", bounds: { x: rect.x, y: rect.y, width: rect.width, height: rect.height } };
			}
		};
		const fn = new Function("canvas", "args", payload && payload.script ? payload.script : "return null;");
		const value = await fn(canvas, payload ? payload.args : null);
		store.state = canvas.state;
		store.layout = canvas.layout;
		store.html = root.innerHTML;
		store.version = (store.version || 0) + 1;
		return { marker, ok: true, mounted: true, value, state: store.state || {}, layout: store.layout || null, version: store.version, html: store.html, text_content: root.textContent || "", mount: store.mount || "overlay" };
	}`
}

func buildBrowserCanvasSnapshotFunction() string {
	return `() => {
		const marker = "koios:canvas:snapshot";
		const root = document.getElementById("__koios_canvas_root");
		const store = window.__koiosCanvasStore || { state: {}, version: 0, html: "", css: "", mount: "overlay", title: "", layout: null };
		if (!root) {
			return { marker, ok: true, mounted: false, version: store.version || 0, state: store.state || {}, layout: store.layout || null, html: store.html || "", css: store.css || "", title: store.title || "", mount: store.mount || "overlay" };
		}
		const rect = root.getBoundingClientRect();
		store.html = root.innerHTML;
		return { marker, ok: true, mounted: true, root_id: root.id, version: store.version || 0, state: store.state || {}, layout: store.layout || null, html: root.innerHTML, css: store.css || "", title: store.title || "", text_content: root.textContent || "", mount: store.mount || root.dataset.koiosCanvasMount || "overlay", child_count: root.childElementCount, bounds: { x: rect.x, y: rect.y, width: rect.width, height: rect.height } };
	}`
}

func buildBrowserCanvasResetFunction() string {
	return `() => {
		const marker = "koios:canvas:reset";
		const root = document.getElementById("__koios_canvas_root");
		if (root) {
			root.remove();
		}
		const styleEl = document.getElementById("__koios_canvas_style");
		if (styleEl) {
			styleEl.remove();
		}
		window.__koiosCanvasStore = { state: {}, version: 0, html: "", css: "", title: "", mount: "overlay", layout: null };
		return { marker, ok: true, mounted: false };
	}`
}

func canvasStateFromPayload(payload map[string]any, existing browserCanvasState) browserCanvasState {
	next := existing
	if html, ok := payload["html"].(string); ok {
		next.HTML = html
	}
	if css, ok := payload["css"].(string); ok {
		next.CSS = css
	}
	if title, ok := payload["title"].(string); ok {
		next.Title = title
	}
	if mount, ok := payload["mount"].(string); ok && strings.TrimSpace(mount) != "" {
		next.Mount = mount
	}
	if state, ok := payload["state"]; ok {
		next.State = state
	}
	if layout, ok := payload["layout"]; ok {
		next.Layout = layout
	}
	if value, ok := payload["value"]; ok {
		next.LastEval = value
	}
	if version := browserMapInt(payload["version"]); version > 0 {
		next.Version = version
	}
	if mounted, ok := payload["mounted"].(bool); ok {
		next.Mounted = mounted
	}
	return next
}

func browserMapInt(value any) int {
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

func (h *Handler) executeCanvasPushTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	var args struct {
		PageID int    `json:"page_id"`
		HTML   string `json:"html"`
		CSS    string `json:"css"`
		Script string `json:"script"`
		State  any    `json:"state"`
		Layout any    `json:"layout"`
		Title  string `json:"title"`
		Mode   string `json:"mode"`
		Mount  string `json:"mount"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	mode := strings.ToLower(strings.TrimSpace(args.Mode))
	if mode == "" {
		mode = "replace"
	}
	if mode != "replace" && mode != "append" && mode != "merge" {
		return nil, fmt.Errorf("unsupported canvas mode %q", args.Mode)
	}
	mount := strings.ToLower(strings.TrimSpace(args.Mount))
	if mount == "" {
		mount = "overlay"
	}
	if mount != "overlay" && mount != "inline" {
		return nil, fmt.Errorf("unsupported canvas mount %q", args.Mount)
	}
	if layout, ok := decodeCanvasLayoutSpec(args.Layout); ok {
		if strings.TrimSpace(args.HTML) == "" {
			args.HTML = renderCanvasLayoutHTML(layout)
		}
		layoutCSS := renderCanvasLayoutCSS(layout)
		if strings.TrimSpace(args.CSS) == "" {
			args.CSS = layoutCSS
		} else {
			args.CSS = layoutCSS + "\n" + args.CSS
		}
		if strings.TrimSpace(args.Title) == "" {
			args.Title = layout.Title
		}
	}
	sessionKey := h.browserSessionKey(ctx, peerID)
	profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
		"function": buildBrowserCanvasPushFunction(),
		"args": []any{map[string]any{
			"html":   args.HTML,
			"css":    args.CSS,
			"script": args.Script,
			"state":  args.State,
			"layout": args.Layout,
			"title":  args.Title,
			"mode":   mode,
			"mount":  mount,
		}},
	})
	if err == errUnhandledTool {
		return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
	}
	if err != nil {
		return nil, err
	}
	normalized := normalizeBrowserToolResult(result)
	payload, _ := normalized.(map[string]any)
	existing, _ := h.loadBrowserCanvasState(sessionKey, strings.TrimSpace(profile.Name), args.PageID)
	stored := canvasStateFromPayload(payload, existing)
	if stored.Title == "" {
		stored.Title = args.Title
	}
	if stored.Mount == "" {
		stored.Mount = mount
	}
	if stored.Layout == nil {
		stored.Layout = args.Layout
	}
	stored.Mounted = true
	h.storeBrowserCanvasState(sessionKey, strings.TrimSpace(profile.Name), args.PageID, stored)
	return map[string]any{
		"ok":             true,
		"active_profile": strings.TrimSpace(profile.Name),
		"page_id":        args.PageID,
		"mode":           mode,
		"mount":          mount,
		"server":         browserStatusPayload(profile.Name, server),
		"canvas":         normalized,
		"source_tool":    "browser.evaluate_script",
		"tool":           "canvas.push",
	}, nil
}

func (h *Handler) executeCanvasEvalTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	var args struct {
		PageID int    `json:"page_id"`
		Script string `json:"script"`
		Args   any    `json:"args"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	sessionKey := h.browserSessionKey(ctx, peerID)
	profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
		"function": buildBrowserCanvasEvalFunction(),
		"args":     []any{map[string]any{"script": args.Script, "args": args.Args}},
	})
	if err == errUnhandledTool {
		return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
	}
	if err != nil {
		return nil, err
	}
	normalized := normalizeBrowserToolResult(result)
	payload, _ := normalized.(map[string]any)
	if mounted, _ := payload["mounted"].(bool); !mounted {
		return nil, fmt.Errorf("canvas has not been pushed for page %d", args.PageID)
	}
	existing, _ := h.loadBrowserCanvasState(sessionKey, strings.TrimSpace(profile.Name), args.PageID)
	stored := canvasStateFromPayload(payload, existing)
	stored.Mounted = true
	h.storeBrowserCanvasState(sessionKey, strings.TrimSpace(profile.Name), args.PageID, stored)
	return map[string]any{
		"ok":             true,
		"active_profile": strings.TrimSpace(profile.Name),
		"page_id":        args.PageID,
		"server":         browserStatusPayload(profile.Name, server),
		"canvas":         normalized,
		"source_tool":    "browser.evaluate_script",
		"tool":           "canvas.eval",
	}, nil
}

func (h *Handler) executeCanvasSnapshotTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	var args struct {
		PageID       int    `json:"page_id"`
		IncludeImage *bool  `json:"include_image"`
		FullPage     bool   `json:"full_page"`
		Format       string `json:"format"`
		Quality      int    `json:"quality"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	includeImage := true
	if args.IncludeImage != nil {
		includeImage = *args.IncludeImage
	}
	format, err := normalizeBrowserCaptureFormat(args.Format)
	if err != nil {
		return nil, err
	}
	sessionKey := h.browserSessionKey(ctx, peerID)
	profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
		"function": buildBrowserCanvasSnapshotFunction(),
	})
	if err == errUnhandledTool {
		return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
	}
	if err != nil {
		return nil, err
	}
	normalized := normalizeBrowserToolResult(result)
	payload, _ := normalized.(map[string]any)
	existing, _ := h.loadBrowserCanvasState(sessionKey, strings.TrimSpace(profile.Name), args.PageID)
	stored := canvasStateFromPayload(payload, existing)
	if mounted, ok := payload["mounted"].(bool); ok {
		stored.Mounted = mounted
	}
	h.storeBrowserCanvasState(sessionKey, strings.TrimSpace(profile.Name), args.PageID, stored)
	response := map[string]any{
		"ok":             true,
		"active_profile": strings.TrimSpace(profile.Name),
		"page_id":        args.PageID,
		"server":         browserStatusPayload(profile.Name, server),
		"canvas":         normalized,
		"stored": map[string]any{
			"html":      stored.HTML,
			"css":       stored.CSS,
			"title":     stored.Title,
			"mount":     stored.Mount,
			"state":     stored.State,
			"layout":    stored.Layout,
			"version":   stored.Version,
			"mounted":   stored.Mounted,
			"last_eval": stored.LastEval,
		},
		"source_tool": "browser.evaluate_script",
		"tool":        "canvas.snapshot",
	}
	if includeImage {
		imageData, mimeType, _, err := h.captureBrowserScreenshot(ctx, peerID, sessionKey, args.PageID, "", "", args.FullPage, format, args.Quality)
		if err == errUnhandledTool {
			return nil, fmt.Errorf("active browser profile %q does not expose browser.take_screenshot", strings.TrimSpace(profile.Name))
		}
		if err != nil {
			return nil, err
		}
		response["image"] = map[string]any{
			"format":      format,
			"mime_type":   mimeType,
			"data_base64": imageData,
			"full_page":   args.FullPage,
			"source_tool": "browser.take_screenshot",
		}
	}
	return response, nil
}

func (h *Handler) executeCanvasResetTool(ctx context.Context, peerID string, call agent.ToolCall) (any, error) {
	var args struct {
		PageID int `json:"page_id"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	sessionKey := h.browserSessionKey(ctx, peerID)
	profile, server, err := h.ensureBrowserServer(ctx, sessionKey)
	if err != nil {
		return nil, err
	}
	result, err := h.invokeBrowserPageTool(ctx, peerID, sessionKey, args.PageID, "browser.evaluate_script", map[string]any{
		"function": buildBrowserCanvasResetFunction(),
	})
	if err == errUnhandledTool {
		return nil, fmt.Errorf("active browser profile %q does not expose browser.evaluate_script", strings.TrimSpace(profile.Name))
	}
	if err != nil {
		return nil, err
	}
	h.clearBrowserCanvasState(sessionKey, strings.TrimSpace(profile.Name), args.PageID)
	return map[string]any{
		"ok":             true,
		"active_profile": strings.TrimSpace(profile.Name),
		"page_id":        args.PageID,
		"server":         browserStatusPayload(profile.Name, server),
		"canvas":         normalizeBrowserToolResult(result),
		"source_tool":    "browser.evaluate_script",
		"tool":           "canvas.reset",
	}, nil
}
