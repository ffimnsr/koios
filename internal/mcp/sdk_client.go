package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ffimnsr/koios/internal/config"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Compile-time assertion that the SDK-backed client satisfies Client.
var _ Client = (*sdkClient)(nil)

// sdkClient implements the Koios Client interface on top of the official
// Model Context Protocol Go SDK. Network or subprocess resources are created
// lazily during Initialize and torn down by Close.
//
// Koios-facing structs remain the ones defined in client.go; SDK types never
// cross the package boundary.
type sdkClient struct {
	// name is the runtime server name used in logs and error messages.
	name string
	// cfg is the original server configuration.
	cfg config.MCPServerConfig
	// transport is the normalized transport: "stdio" or "http".
	transport string
	// timeout is the parsed per-request timeout, at least 30s.
	timeout time.Duration

	mu sync.Mutex
	// client and session are the connected SDK state, populated by Initialize.
	client *sdkmcp.Client
	sess   *sdkmcp.ClientSession
	// notifications receives server-to-client notifications once connected. Close
	// clears it without closing the channel: SDK notification handlers may still
	// run on other goroutines, and sending on a closed channel would panic.
	notifications chan Notification
}

// NewSDKClient builds an SDK-backed MCP client from a server config. Startup
// of the subprocess or HTTP connection is deferred until Initialize.
func NewSDKClient(c config.MCPServerConfig) Client {
	timeout, _ := time.ParseDuration(c.Timeout)
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	transport := strings.ToLower(strings.TrimSpace(c.Transport))
	if transport != "stdio" {
		// Mirrors newClient: anything but stdio is treated as http.
		transport = "http"
	}
	return &sdkClient{
		name:      strings.TrimSpace(c.Name),
		cfg:       c,
		transport: transport,
		timeout:   timeout,
	}
}

// requestContext applies the configured per-request timeout unless the caller
// already provided an earlier deadline. It mirrors the legacy clients' habit
// of bounding every MCP request.
func (c *sdkClient) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= c.timeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, c.timeout)
}

// requireSession returns the connected SDK session, or an error when the
// client has not been initialized (or has been closed).
func (c *sdkClient) requireSession() (*sdkmcp.ClientSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil {
		return nil, fmt.Errorf("client is not initialized")
	}
	return c.sess, nil
}

// newSDKNotificationHandler adapts an SDK notification callback into a push
// into the given buffered Koios Notification channel. The SDK passes a typed
// request that is ignored; only the notification method matters. Handlers fire
// from SDK goroutines, so the channel must never be closed while they can still
// run.
func newSDKNotificationHandler[T any](ch chan Notification, method string, params func() json.RawMessage) func(context.Context, T) {
	return func(context.Context, T) {
		var raw json.RawMessage
		if params != nil {
			raw = params()
		}
		pushNotification(ch, Notification{Method: method, Params: raw})
	}
}

// pushNotification delivers a notification to the buffered channel, dropping
// it when the buffer is full so slow consumers cannot stall the SDK.
func pushNotification(ch chan Notification, n Notification) {
	select {
	case ch <- n:
	default:
		slog.Warn("mcp sdk: notification buffer full; dropping", "method", n.Method)
	}
}

func (c *sdkClient) Initialize(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess != nil {
		// Repeated initialization is idempotent for a connected session.
		return nil
	}
	if c.transport == "stdio" && strings.TrimSpace(c.cfg.Command) == "" {
		return fmt.Errorf("mcp sdk %s: initialize: stdio command is required", c.name)
	}
	if c.transport == "http" && strings.TrimSpace(c.cfg.URL) == "" {
		return fmt.Errorf("mcp sdk %s: initialize: http URL is required", c.name)
	}

	notifications := make(chan Notification, 64)
	// No client capabilities are declared beyond an empty set: Koios fulfills
	// MRTR input-required results itself through CallToolWithInput (the agent
	// supplies the responses), so declaring the elicitation capability would
	// overstate the client — a server honoring it would send
	// elicitation/request, which this client has no handler for and would fail
	// with method-not-found. Servers fall back to the input_required result
	// type when the client declares no elicitation support, which is exactly
	// the flow Koios implements. (The empty value also keeps the SDK from
	// advertising roots support, which Koios does not provide either.)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: clientName, Version: clientVersion},
		&sdkmcp.ClientOptions{
			Capabilities: &sdkmcp.ClientCapabilities{},
			// The SDK's automatic input-request middleware must not swallow
			// input_required results or fail because no elicitation/sampling
			// handlers are registered: Koios drives the MRTR retry loop itself
			// through CallToolWithInput.
			MultiRoundTrip:             &sdkmcp.MultiRoundTripOptions{Disabled: true},
			ToolListChangedHandler:     newSDKNotificationHandler[*sdkmcp.ToolListChangedRequest](notifications, "notifications/tools/list_changed", nil),
			PromptListChangedHandler:   newSDKNotificationHandler[*sdkmcp.PromptListChangedRequest](notifications, "notifications/prompts/list_changed", nil),
			ResourceListChangedHandler: newSDKNotificationHandler[*sdkmcp.ResourceListChangedRequest](notifications, "notifications/resources/list_changed", nil),
			ResourceUpdatedHandler: func(_ context.Context, req *sdkmcp.ResourceUpdatedNotificationRequest) {
				var params json.RawMessage
				if req != nil {
					params = marshalRaw(req.Params)
				}
				pushNotification(notifications, Notification{Method: "notifications/resources/updated", Params: params})
			},
		})

	var transport sdkmcp.Transport
	switch c.transport {
	case "stdio":
		transport = newSDKCommandTransport(ctx, c.cfg)
	default:
		transport = newSDKHTTPTransport(c.cfg)
	}

	// Bound the handshake with the configured per-request timeout; the SDK
	// detaches its own context for the connection lifetime, so this does not
	// affect the background SSE stream or later requests.
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	sess, err := client.Connect(ctx, transport, &sdkmcp.ClientSessionOptions{ProtocolVersion: ProtocolVersion2026})
	if err != nil {
		return fmt.Errorf("mcp sdk %s: initialize: %w", c.name, err)
	}
	c.client = client
	c.sess = sess
	c.notifications = notifications
	return nil
}

// newSDKCommandTransport builds the SDK stdio transport from the server config.
// The subprocess is spawned on first use (Client.Connect). The command context
// is detached from the Initialize handshake because the subprocess outlives it:
// teardown is driven by Close through the SDK transport, which closes stdin and
// escalates to SIGTERM/SIGKILL.
func newSDKCommandTransport(ctx context.Context, c config.MCPServerConfig) sdkmcp.Transport {
	// Command and Args come from the operator-owned koios.config.toml; spawning
	// the configured MCP server process is the intended behavior.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), c.Command, c.Args...) // #nosec G204 -- operator-configured MCP stdio server
	if len(c.Env) > 0 {
		cmd.Env = append([]string(nil), os.Environ()...)
		for k, v := range c.Env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	// The SDK transport reads MCP JSON-RPC only from stdout. stderr is streamed
	// to the operator's stderr instead of being parsed or dropped; unlike the
	// legacy client, which forwarded each stderr line through slog with server
	// attribution, the raw stream keeps diagnostics visible even when the log
	// level filters out warnings.
	cmd.Stderr = os.Stderr
	return &sdkmcp.CommandTransport{Command: cmd}
}

// newSDKHTTPTransport builds the SDK Streamable HTTP transport from the server
// config. The SDK transport has no per-request header option, so configured
// headers are injected at the round-trip layer, matching the legacy client's
// behavior of applying them to every request the SDK makes (handshake, RPC,
// SSE stream, and session teardown).
//
// A client-level http.Client.Timeout is deliberately not set: it would also
// bound the long-lived standalone SSE stream and kill it whenever the server
// is idle for longer than the timeout. Per-request timing is instead applied
// through the context deadlines that sdkClient.requestContext attaches to
// every RPC.
func newSDKHTTPTransport(c config.MCPServerConfig) sdkmcp.Transport {
	httpClient := &http.Client{}
	if len(c.Headers) > 0 {
		httpClient.Transport = headerInjectingTransport{headers: c.Headers}
	}
	return &sdkmcp.StreamableClientTransport{Endpoint: c.URL, HTTPClient: httpClient}
}

// headerInjectingTransport adds configured headers to every outgoing request
// made by the SDK HTTP transport.
type headerInjectingTransport struct {
	headers map[string]string
}

func (t headerInjectingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	for k, v := range t.headers {
		// The SDK transport derives Mcp-Param-* headers from the called tool's
		// x-mcp-header annotations, and those are applied before this wrapper
		// runs. A static configuration header must not override them: the legacy
		// client applied dynamic headers after static ones, so the dynamic value
		// won on collision.
		if canonical := http.CanonicalHeaderKey(k); strings.HasPrefix(canonical, "Mcp-Param-") && req.Header.Get(canonical) != "" {
			continue
		}
		req.Header.Set(k, v)
	}
	return http.DefaultTransport.RoundTrip(req)
}

func (c *sdkClient) Discover(ctx context.Context) (*DiscoverResult, error) {
	sess, err := c.requireSession()
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: server/discover: %w", c.name, err)
	}
	initRes := sess.InitializeResult()
	if initRes == nil {
		return nil, fmt.Errorf("mcp sdk %s: server/discover: no initialization result", c.name)
	}
	return &DiscoverResult{
		ProtocolVersion: initRes.ProtocolVersion,
		Capabilities:    marshalRaw(initRes.Capabilities),
		ServerInfo:      Implementation{Name: sdkServerName(initRes), Version: sdkServerVersion(initRes)},
		Instructions:    initRes.Instructions,
		ResultType:      normalizeResultType(""),
	}, nil
}

func sdkServerName(initRes *sdkmcp.InitializeResult) string {
	if initRes == nil || initRes.ServerInfo == nil {
		return ""
	}
	return initRes.ServerInfo.Name
}

func sdkServerVersion(initRes *sdkmcp.InitializeResult) string {
	if initRes == nil || initRes.ServerInfo == nil {
		return ""
	}
	return initRes.ServerInfo.Version
}

func (c *sdkClient) ListTools(ctx context.Context) ([]Tool, error) {
	sess, err := c.requireSession()
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: tools/list: %w", c.name, err)
	}
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	var out []Tool
	var cursor string
	for {
		res, err := sess.ListTools(ctx, &sdkmcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp sdk %s: tools/list: %w", c.name, err)
		}
		for _, tool := range res.Tools {
			out = append(out, fromSDKTool(tool))
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return filterValidTools(c.name, out), nil
}

func (c *sdkClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	return c.CallToolWithInput(ctx, name, args, nil, nil)
}

func (c *sdkClient) CallToolWithInput(ctx context.Context, name string, args map[string]any, inputResponses, requestState json.RawMessage) (*ToolResult, error) {
	sess, err := c.requireSession()
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: tools/call %s: %w", c.name, name, err)
	}
	params := &sdkmcp.CallToolParams{Name: name, Arguments: args}
	// Validate and decode MRTR fields before the request is sent so malformed
	// inputResponses or requestState fail contextually without touching the
	// server. InputResponses must decode into the SDK response map (an object
	// whose values carry action/role/roots), and requestState must be the
	// opaque JSON string the input-required result carried.
	if len(inputResponses) > 0 {
		var responses sdkmcp.InputResponseMap
		if err := json.Unmarshal(inputResponses, &responses); err != nil {
			return nil, fmt.Errorf("mcp sdk %s: tools/call %s: decode inputResponses: %w", c.name, name, err)
		}
		params.InputResponses = responses
	}
	if len(requestState) > 0 {
		var state string
		if err := json.Unmarshal(requestState, &state); err != nil {
			return nil, fmt.Errorf("mcp sdk %s: tools/call %s: decode requestState: %w", c.name, name, err)
		}
		params.RequestState = state
	}
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	res, err := sess.CallTool(ctx, params)
	if err != nil {
		// Transport, protocol, and handshake errors surface as Go errors.
		// Tool-level failures are represented by the SDK result itself.
		return nil, fmt.Errorf("mcp sdk %s: tools/call %s: %w", c.name, name, err)
	}
	return fromSDKToolResult(res), nil
}

func (c *sdkClient) ListResources(ctx context.Context) ([]Resource, error) {
	sess, err := c.requireSession()
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: resources/list: %w", c.name, err)
	}
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	var out []Resource
	var cursor string
	for {
		res, err := sess.ListResources(ctx, &sdkmcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp sdk %s: resources/list: %w", c.name, err)
		}
		for _, r := range res.Resources {
			out = append(out, fromSDKResource(r))
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

func (c *sdkClient) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	sess, err := c.requireSession()
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: resources/templates/list: %w", c.name, err)
	}
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	var out []ResourceTemplate
	var cursor string
	for {
		res, err := sess.ListResourceTemplates(ctx, &sdkmcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp sdk %s: resources/templates/list: %w", c.name, err)
		}
		for _, tpl := range res.ResourceTemplates {
			out = append(out, fromSDKResourceTemplate(tpl))
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

func (c *sdkClient) ReadResource(ctx context.Context, uri string) (*ResourceReadResult, error) {
	sess, err := c.requireSession()
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: resources/read %s: %w", c.name, uri, err)
	}
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	res, err := sess.ReadResource(ctx, &sdkmcp.ReadResourceParams{URI: uri})
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: resources/read %s: %w", c.name, uri, err)
	}
	return fromSDKReadResourceResult(res), nil
}

func (c *sdkClient) ListPrompts(ctx context.Context) ([]Prompt, error) {
	sess, err := c.requireSession()
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: prompts/list: %w", c.name, err)
	}
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	var out []Prompt
	var cursor string
	for {
		res, err := sess.ListPrompts(ctx, &sdkmcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, fmt.Errorf("mcp sdk %s: prompts/list: %w", c.name, err)
		}
		for _, p := range res.Prompts {
			out = append(out, fromSDKPrompt(p))
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	return out, nil
}

func (c *sdkClient) GetPrompt(ctx context.Context, name string, args map[string]any) (*PromptGetResult, error) {
	sess, err := c.requireSession()
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: prompts/get %s: %w", c.name, name, err)
	}
	ctx, cancel := c.requestContext(ctx)
	defer cancel()
	res, err := sess.GetPrompt(ctx, &sdkmcp.GetPromptParams{Name: name, Arguments: sdkPromptArguments(args)})
	if err != nil {
		return nil, fmt.Errorf("mcp sdk %s: prompts/get %s: %w", c.name, name, err)
	}
	return fromSDKGetPromptResult(res), nil
}

func (c *sdkClient) Listen(ctx context.Context) (<-chan Notification, error) {
	c.mu.Lock()
	notifications := c.notifications
	sess := c.sess
	c.mu.Unlock()
	if notifications == nil || sess == nil {
		return nil, fmt.Errorf("mcp sdk %s: listen: client is not initialized", c.name)
	}
	out := make(chan Notification, 64)
	// The relay exits when the listener context is canceled, when the SDK
	// session itself ends, or when the notification source is closed.
	sessClosed := make(chan struct{})
	go func() {
		_ = sess.Wait()
		close(sessClosed)
	}()
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case <-sessClosed:
				return
			case n, ok := <-notifications:
				if !ok {
					return
				}
				select {
				case out <- n:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}

// Cancel reports that the SDK client cannot send notifications/cancelled: the
// official Go SDK owns JSON-RPC request IDs internally and exposes no API to
// emit arbitrary server-bound notifications (its JSON-RPC layer and transports
// are unexported). Request cancellation is handled through context
// cancellation instead. Nothing in the Manager invokes Cancel.
func (c *sdkClient) Cancel(_ context.Context, requestID any, reason string) error {
	if _, err := c.requireSession(); err != nil {
		return fmt.Errorf("mcp sdk %s: notifications/cancelled: %w", c.name, err)
	}
	return fmt.Errorf("mcp sdk %s: notifications/cancelled: request %v (reason %q): not supported by the SDK client: the SDK owns request IDs and cancels requests via context", c.name, requestID, reason)
}

// Close terminates the SDK session (killing a stdio subprocess or closing the
// HTTP session) and clears the stored SDK state.
func (c *sdkClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sess == nil {
		return nil
	}
	err := c.sess.Close()
	c.client = nil
	c.sess = nil
	// The notification channel is deliberately not closed: SDK notification
	// handlers may still be running on other goroutines, and sending on a
	// closed channel would panic. Listen relays exit via context cancellation.
	c.notifications = nil
	return err
}

// ─── SDK-to-Koios conversion helpers ─────────────────────────────────────────

// fromSDKTool converts SDK tool metadata into a Koios Tool.
func fromSDKTool(tool *sdkmcp.Tool) Tool {
	converted := Tool{
		Name:        tool.Name,
		Title:       tool.Title,
		Description: tool.Description,
	}
	if tool.InputSchema != nil {
		converted.InputSchema = marshalRaw(tool.InputSchema)
	}
	if tool.OutputSchema != nil {
		converted.OutputSchema = marshalRaw(tool.OutputSchema)
	}
	if tool.Annotations != nil {
		converted.Annotations = marshalRaw(tool.Annotations)
	}
	return converted
}

// fromSDKToolResult converts an SDK tool call result into a Koios ToolResult.
//
// The SDK v1.8 retains resultType, content, structuredContent, isError,
// inputRequests (SEP-2322), and requestState from a tools/call response, and
// drops the rest during decode: a top-level "requestId", "ttlMs", or
// "cacheScope" field is not kept by CallToolResult (it embeds no Cacheable
// and has no requestId field), and a raw "elicitation" extension is replaced
// by the typed InputRequests carrier. Those fields therefore cannot be
// populated for tool results.
func fromSDKToolResult(result *sdkmcp.CallToolResult) *ToolResult {
	if result == nil {
		return nil
	}
	out := &ToolResult{
		ResultType: sdkResultType(result.NeedsInput()),
		IsError:    result.IsError,
	}
	if result.StructuredContent != nil {
		out.StructuredContent = marshalRaw(result.StructuredContent)
	}
	if result.RequestState != "" {
		out.RequestState = marshalRaw(result.RequestState)
	}
	if len(result.InputRequests) > 0 {
		// The Koios ToolResult carries input requests under Elicitation, the
		// legacy wire field this data replaces (SEP-2322).
		out.Elicitation = marshalRaw(result.InputRequests)
	}
	for _, block := range result.Content {
		if converted := fromSDKContent(block); converted != nil {
			out.Content = append(out.Content, *converted)
		}
	}
	return out
}

// fromSDKContent converts one SDK content block into a Koios Content value.
// Unsupported block kinds are skipped with a debug log.
func fromSDKContent(block sdkmcp.Content) *Content {
	if block == nil {
		return nil
	}
	var out Content
	switch v := block.(type) {
	case *sdkmcp.TextContent:
		out = Content{Type: "text", Text: v.Text, Annotations: marshalRaw(v.Annotations)}
	case *sdkmcp.ImageContent:
		out = Content{Type: "image", Data: base64.StdEncoding.EncodeToString(v.Data), MimeType: v.MIMEType, Annotations: marshalRaw(v.Annotations)}
	case *sdkmcp.AudioContent:
		out = Content{Type: "audio", Data: base64.StdEncoding.EncodeToString(v.Data), MimeType: v.MIMEType, Annotations: marshalRaw(v.Annotations)}
	case *sdkmcp.ResourceLink:
		out = Content{Type: "resource_link", URI: v.URI, MimeType: v.MIMEType, Annotations: marshalRaw(v.Annotations)}
	case *sdkmcp.EmbeddedResource:
		out = Content{Type: "resource", Resource: marshalRaw(v.Resource), Annotations: marshalRaw(v.Annotations)}
	default:
		slog.Debug("mcp sdk: skipping unsupported content block", "type", fmt.Sprintf("%T", block))
		return nil
	}
	return &out
}

func fromSDKResource(r *sdkmcp.Resource) Resource {
	return Resource{
		Name:        r.Name,
		Title:       r.Title,
		Description: r.Description,
		URI:         r.URI,
		MimeType:    r.MIMEType,
		Size:        r.Size,
		Annotations: marshalRaw(r.Annotations),
	}
}

// sdkPromptArguments converts Koios prompt arguments (any JSON values) into
// the string-valued map accepted by the SDK. String values pass through;
// other JSON values are compact-encoded.
func sdkPromptArguments(args map[string]any) map[string]string {
	if len(args) == 0 {
		return nil
	}
	out := make(map[string]string, len(args))
	for k, v := range args {
		switch t := v.(type) {
		case string:
			out[k] = t
		default:
			b, err := json.Marshal(t)
			if err == nil {
				out[k] = string(b)
			}
		}
	}
	return out
}

func fromSDKResourceTemplate(tpl *sdkmcp.ResourceTemplate) ResourceTemplate {
	return ResourceTemplate{
		Name:        tpl.Name,
		Title:       tpl.Title,
		Description: tpl.Description,
		URITemplate: tpl.URITemplate,
		MimeType:    tpl.MIMEType,
		Annotations: marshalRaw(tpl.Annotations),
	}
}

func fromSDKResourceContents(contents *sdkmcp.ResourceContents) ResourceContent {
	if contents == nil {
		return ResourceContent{}
	}
	// The SDK's ResourceContents exposes only uri, mimeType, text, and blob;
	// the Koios name/annotations fields stay empty for read contents.
	return ResourceContent{
		URI:      contents.URI,
		MimeType: contents.MIMEType,
		Text:     contents.Text,
		Blob:     base64.StdEncoding.EncodeToString(contents.Blob),
	}
}

// fromSDKReadResourceResult converts an SDK resource read result into a Koios
// ResourceReadResult, preserving the TTL and cache-scope metadata the manager
// uses for result caching.
func fromSDKReadResourceResult(res *sdkmcp.ReadResourceResult) *ResourceReadResult {
	if res == nil {
		return nil
	}
	out := &ResourceReadResult{
		ResultType: sdkResultType(res.NeedsInput()),
		TTLMs:      int64(res.TTLMs),
		CacheScope: res.CacheScope,
	}
	for _, contents := range res.Contents {
		out.Contents = append(out.Contents, fromSDKResourceContents(contents))
	}
	return out
}

func fromSDKPrompt(p *sdkmcp.Prompt) Prompt {
	converted := Prompt{
		Name:        p.Name,
		Title:       p.Title,
		Description: p.Description,
	}
	for _, arg := range p.Arguments {
		if arg == nil {
			continue
		}
		converted.Arguments = append(converted.Arguments, PromptArgument{
			Name:        arg.Name,
			Title:       arg.Title,
			Description: arg.Description,
			Required:    arg.Required,
		})
	}
	return converted
}

// fromSDKGetPromptResult converts an SDK prompt get result into a Koios
// PromptGetResult. The SDK v1.8 result type does not embed Cacheable, so
// ttlMs/cacheScope from a responses are not retained by the SDK and cannot be
// preserved here.
func fromSDKGetPromptResult(res *sdkmcp.GetPromptResult) *PromptGetResult {
	if res == nil {
		return nil
	}
	out := &PromptGetResult{
		ResultType:  sdkResultType(res.NeedsInput()),
		Description: res.Description,
	}
	for _, msg := range res.Messages {
		content := fromSDKContent(msg.Content)
		if content == nil {
			continue
		}
		out.Messages = append(out.Messages, PromptMessage{Role: string(msg.Role), Content: *content})
	}
	return out
}

// sdkResultType maps an SDK result into the Koios resultType string.
func sdkResultType(needsInput bool) string {
	if needsInput {
		return "input_required"
	}
	return normalizeResultType("")
}

// marshalRaw marshals a JSON-derived SDK value back into raw JSON. Marshallers
// for values received from the SDK wire should never fail; on error a nil
// result and a debug log are produced rather than propagating a failure.
func marshalRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		slog.Debug("mcp sdk: cannot marshal value", "err", err)
		return nil
	}
	return b
}
