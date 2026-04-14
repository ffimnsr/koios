package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// httpClient implements the MCP Client interface using the Streamable HTTP
// transport: every JSON-RPC request is a POST to the configured URL.
type httpClient struct {
	name    string
	url     string
	headers map[string]string
	timeout time.Duration
	http    *http.Client
	nextID  atomic.Int64
}

// NewHTTPClient creates a new MCP client using the HTTP transport.
// endpoint is the POST URL, e.g. "http://localhost:3000/mcp".
func NewHTTPClient(name, endpoint string, headers map[string]string, timeout time.Duration) Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &httpClient{
		name:    name,
		url:     endpoint,
		headers: headers,
		timeout: timeout,
		http:    &http.Client{Timeout: timeout},
	}
}

func (c *httpClient) Initialize(ctx context.Context) error {
	params := encodeParams(initializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    map[string]any{},
		ClientInfo:      map[string]string{"name": "koios", "version": "1.0"},
	})
	resp, err := c.call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("mcp http %s: initialize: %w", c.name, err)
	}
	var result initializeResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("mcp http %s: parse initialize result: %w", c.name, err)
	}
	slog.Debug("mcp http initialized", "server", c.name, "protocol", result.ProtocolVersion)

	// Send notifications/initialized (fire-and-forget; errors are non-fatal).
	_ = c.postNotify(ctx, "notifications/initialized", nil)
	return nil
}

func (c *httpClient) ListTools(ctx context.Context) ([]Tool, error) {
	resp, err := c.call(ctx, "tools/list", encodeParams(map[string]any{}))
	if err != nil {
		return nil, fmt.Errorf("mcp http %s: tools/list: %w", c.name, err)
	}
	var result toolsListResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp http %s: parse tools/list: %w", c.name, err)
	}
	return result.Tools, nil
}

func (c *httpClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	params := encodeParams(toolsCallParams{Name: name, Arguments: args})
	resp, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("mcp http %s: tools/call %s: %w", c.name, name, err)
	}
	var result ToolResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp http %s: parse tools/call result: %w", c.name, err)
	}
	return &result, nil
}

func (c *httpClient) Close() error { return nil }

func (c *httpClient) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST: %w", err)
	}
	defer httpResp.Body.Close()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return nil, fmt.Errorf("HTTP %d from %s", httpResp.StatusCode, c.url)
	}

	var rpc rpcResponse
	if err := json.NewDecoder(httpResp.Body).Decode(&rpc); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if rpc.Error != nil {
		return nil, rpc.Error
	}
	return rpc.Result, nil
}

func (c *httpClient) postNotify(ctx context.Context, method string, params json.RawMessage) error {
	req := rpcRequest{JSONRPC: "2.0", Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// ─── SSE transport ───────────────────────────────────────────────────────────

// sseClient implements the legacy MCP SSE transport:
//
//	GET {url}         — establishes the SSE stream; server sends an endpoint event
//	                    with the POST URL in the "data" field.
//	POST {postURL}    — sends JSON-RPC requests; responses arrive on the SSE stream.
//
// The newer Streamable HTTP transport (POST /mcp) is handled by httpClient.
type sseClient struct {
	name      string
	sseURL    string
	headers   map[string]string
	timeout   time.Duration
	http      *http.Client
	postURL   string
	nextID    atomic.Int64
	responses chan *rpcResponse
	sseCancel context.CancelFunc
}

// NewSSEClient creates a new MCP client using the SSE transport.
// sseURL is the GET endpoint, e.g. "http://localhost:3001/sse".
func NewSSEClient(name, sseURL string, headers map[string]string, timeout time.Duration) Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &sseClient{
		name:      name,
		sseURL:    sseURL,
		headers:   headers,
		timeout:   timeout,
		http:      &http.Client{Timeout: 0}, // no timeout on stream connection
		responses: make(chan *rpcResponse, 256),
	}
}

func (c *sseClient) Initialize(ctx context.Context) error {
	sseCtx, cancel := context.WithCancel(context.Background())
	c.sseCancel = cancel

	// Connect to SSE stream, wait for the endpoint event.
	endpointCh := make(chan string, 1)
	go c.connectSSE(sseCtx, endpointCh)

	select {
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	case ep := <-endpointCh:
		if ep == "" {
			cancel()
			return fmt.Errorf("mcp sse %s: no endpoint received", c.name)
		}
		c.postURL = ep
	case <-time.After(15 * time.Second):
		cancel()
		return fmt.Errorf("mcp sse %s: timeout waiting for endpoint event", c.name)
	}

	params := encodeParams(initializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    map[string]any{},
		ClientInfo:      map[string]string{"name": "koios", "version": "1.0"},
	})
	resp, err := c.send(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("mcp sse %s: initialize: %w", c.name, err)
	}
	var result initializeResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("mcp sse %s: parse initialize: %w", c.name, err)
	}
	slog.Debug("mcp sse initialized", "server", c.name, "protocol", result.ProtocolVersion)
	return c.postNotify(ctx, "notifications/initialized", nil)
}

func (c *sseClient) ListTools(ctx context.Context) ([]Tool, error) {
	resp, err := c.send(ctx, "tools/list", encodeParams(map[string]any{}))
	if err != nil {
		return nil, fmt.Errorf("mcp sse %s: tools/list: %w", c.name, err)
	}
	var result toolsListResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp sse %s: parse tools/list: %w", c.name, err)
	}
	return result.Tools, nil
}

func (c *sseClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	params := encodeParams(toolsCallParams{Name: name, Arguments: args})
	resp, err := c.send(ctx, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("mcp sse %s: tools/call %s: %w", c.name, name, err)
	}
	var result ToolResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp sse %s: parse tools/call result: %w", c.name, err)
	}
	return &result, nil
}

func (c *sseClient) Close() error {
	if c.sseCancel != nil {
		c.sseCancel()
	}
	return nil
}

// connectSSE opens the SSE stream and:
//   - sends the first "endpoint" event's data on endpointCh
//   - routes subsequent "message" events into c.responses
func (c *sseClient) connectSSE(ctx context.Context, endpointCh chan<- string) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.sseURL, nil)
	if err != nil {
		slog.Error("mcp sse: create request", "server", c.name, "err", err)
		endpointCh <- ""
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		slog.Error("mcp sse: connect", "server", c.name, "err", err)
		endpointCh <- ""
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	var eventType, data string
	sentEndpoint := false

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		case line == "":
			// End of event block.
			if !sentEndpoint && eventType == "endpoint" {
				endpointCh <- data
				sentEndpoint = true
			} else if eventType == "message" && data != "" {
				var rpc rpcResponse
				if err := json.Unmarshal([]byte(data), &rpc); err == nil {
					select {
					case c.responses <- &rpc:
					default:
					}
				}
			}
			eventType, data = "", ""
		}
	}

	// Signal end-of-stream.
	if !sentEndpoint {
		endpointCh <- ""
	}
	close(c.responses)
}

// send posts a JSON-RPC request and waits for the matching response on the
// SSE stream.
func (c *sseClient) send(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	postURL := c.postURL
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, postURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	postClient := &http.Client{Timeout: c.timeout}
	httpResp, err := postClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("POST: %w", err)
	}
	// 202 Accepted is the expected response from the SSE message endpoint.
	if httpResp.StatusCode >= 300 && httpResp.StatusCode != http.StatusAccepted {
		_ = httpResp.Body.Close()
		return nil, fmt.Errorf("HTTP %d from %s", httpResp.StatusCode, postURL)
	}
	_, _ = io.Copy(io.Discard, httpResp.Body)
	_ = httpResp.Body.Close()

	// Wait for the response on the SSE stream.
	deadline := time.After(c.timeout)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("timeout waiting for SSE response to %s", method)
		case resp, ok := <-c.responses:
			if !ok {
				return nil, fmt.Errorf("SSE stream closed")
			}
			// Check if this response matches our request ID.
			var respID int64
			switch v := resp.ID.(type) {
			case float64:
				respID = int64(v)
			case json.Number:
				respID, _ = v.Int64()
			}
			if respID != id {
				// Not ours — put it back if possible, skip otherwise.
				select {
				case c.responses <- resp:
				default:
				}
				continue
			}
			if resp.Error != nil {
				return nil, resp.Error
			}
			return resp.Result, nil
		}
	}
}

func (c *sseClient) postNotify(ctx context.Context, method string, params json.RawMessage) error {
	req := rpcRequest{JSONRPC: "2.0", Method: method, Params: params}
	body, _ := json.Marshal(req)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.postURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range c.headers {
		httpReq.Header.Set(k, v)
	}
	r, err := (&http.Client{Timeout: c.timeout}).Do(httpReq)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	return nil
}
