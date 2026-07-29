package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// httpClient implements the MCP Streamable HTTP transport.
type httpClient struct {
	name    string
	url     string
	headers map[string]string
	timeout time.Duration
	http    *http.Client
	nextID  atomic.Int64

	mu    sync.RWMutex
	tools map[string]Tool
}

// NewHTTPClient creates a new MCP client using Streamable HTTP.
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
		tools:   make(map[string]Tool),
	}
}

func (c *httpClient) Discover(ctx context.Context) (*DiscoverResult, error) {
	resp, err := c.call(ctx, "server/discover", encodeParams(discoverParams{Meta: defaultRequestMeta()}), "", nil)
	if err != nil {
		return nil, fmt.Errorf("mcp http %s: server/discover: %w", c.name, err)
	}
	var result DiscoverResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp http %s: parse server/discover: %w", c.name, err)
	}
	result.ResultType = normalizeResultType(result.ResultType)
	return &result, nil
}

func (c *httpClient) Initialize(ctx context.Context) error {
	if _, err := c.Discover(ctx); err == nil {
		return nil
	} else if !isMethodNotFoundError(err) {
		return err
	}

	resp, err := c.call(ctx, "initialize", encodeParams(defaultLegacyInitializeParams()), "", nil)
	if err != nil {
		return fmt.Errorf("mcp http %s: initialize: %w", c.name, err)
	}
	var result initializeResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("mcp http %s: parse initialize result: %w", c.name, err)
	}
	return c.postNotify(ctx, "notifications/initialized", encodeParams(discoverParams{Meta: defaultRequestMeta()}), "")
}

func (c *httpClient) ListTools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	var cursor *string
	for {
		resp, err := c.call(ctx, "tools/list", encodeParams(listParams{Cursor: cursor, Meta: defaultRequestMeta()}), "", nil)
		if err != nil {
			return nil, fmt.Errorf("mcp http %s: tools/list: %w", c.name, err)
		}
		var result toolsListResult
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("mcp http %s: parse tools/list: %w", c.name, err)
		}
		out = append(out, filterValidTools(c.name, result.Tools)...)
		if result.NextCursor == nil {
			break
		}
		cursor = result.NextCursor
	}
	c.mu.Lock()
	c.tools = make(map[string]Tool, len(out))
	for _, tool := range out {
		c.tools[tool.Name] = tool
	}
	c.mu.Unlock()
	return out, nil
}

func (c *httpClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	return c.CallToolWithInput(ctx, name, args, nil, nil)
}

func (c *httpClient) CallToolWithInput(ctx context.Context, name string, args map[string]any, inputResponses, requestState json.RawMessage) (*ToolResult, error) {
	extraHeaders := c.toolArgumentHeaders(name, args)
	resp, err := c.call(ctx, "tools/call", encodeParams(toolsCallParams{Name: name, Arguments: args, InputResponses: inputResponses, RequestState: requestState, Meta: defaultRequestMeta()}), name, extraHeaders)
	if err != nil {
		return nil, fmt.Errorf("mcp http %s: tools/call %s: %w", c.name, name, err)
	}
	var result ToolResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp http %s: parse tools/call result: %w", c.name, err)
	}
	result.ResultType = normalizeResultType(result.ResultType)
	return &result, nil
}

func (c *httpClient) ListResources(ctx context.Context) ([]Resource, error) {
	var out []Resource
	var cursor *string
	for {
		resp, err := c.call(ctx, "resources/list", encodeParams(listParams{Cursor: cursor, Meta: defaultRequestMeta()}), "", nil)
		if err != nil {
			return nil, fmt.Errorf("mcp http %s: resources/list: %w", c.name, err)
		}
		var result resourcesListResult
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("mcp http %s: parse resources/list: %w", c.name, err)
		}
		out = append(out, result.Resources...)
		if result.NextCursor == nil {
			break
		}
		cursor = result.NextCursor
	}
	return out, nil
}

func (c *httpClient) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	var out []ResourceTemplate
	var cursor *string
	for {
		resp, err := c.call(ctx, "resources/templates/list", encodeParams(listParams{Cursor: cursor, Meta: defaultRequestMeta()}), "", nil)
		if err != nil {
			return nil, fmt.Errorf("mcp http %s: resources/templates/list: %w", c.name, err)
		}
		var result resourceTemplatesListResult
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("mcp http %s: parse resources/templates/list: %w", c.name, err)
		}
		out = append(out, result.ResourceTemplates...)
		if result.NextCursor == nil {
			break
		}
		cursor = result.NextCursor
	}
	return out, nil
}

func (c *httpClient) ReadResource(ctx context.Context, uri string) (*ResourceReadResult, error) {
	resp, err := c.call(ctx, "resources/read", encodeParams(resourceReadParams{URI: uri, Meta: defaultRequestMeta()}), uri, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp http %s: resources/read %s: %w", c.name, uri, err)
	}
	var result ResourceReadResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp http %s: parse resources/read result: %w", c.name, err)
	}
	result.ResultType = normalizeResultType(result.ResultType)
	return &result, nil
}

func (c *httpClient) ListPrompts(ctx context.Context) ([]Prompt, error) {
	var out []Prompt
	var cursor *string
	for {
		resp, err := c.call(ctx, "prompts/list", encodeParams(listParams{Cursor: cursor, Meta: defaultRequestMeta()}), "", nil)
		if err != nil {
			return nil, fmt.Errorf("mcp http %s: prompts/list: %w", c.name, err)
		}
		var result promptsListResult
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("mcp http %s: parse prompts/list: %w", c.name, err)
		}
		out = append(out, result.Prompts...)
		if result.NextCursor == nil {
			break
		}
		cursor = result.NextCursor
	}
	return out, nil
}

func (c *httpClient) GetPrompt(ctx context.Context, name string, args map[string]any) (*PromptGetResult, error) {
	resp, err := c.call(ctx, "prompts/get", encodeParams(promptGetParams{Name: name, Arguments: args, Meta: defaultRequestMeta()}), name, nil)
	if err != nil {
		return nil, fmt.Errorf("mcp http %s: prompts/get %s: %w", c.name, name, err)
	}
	var result PromptGetResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp http %s: parse prompts/get result: %w", c.name, err)
	}
	result.ResultType = normalizeResultType(result.ResultType)
	return &result, nil
}

func (c *httpClient) Listen(ctx context.Context) (<-chan Notification, error) {
	ch := make(chan Notification)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch, nil
}

func (c *httpClient) Cancel(ctx context.Context, requestID any, reason string) error {
	return c.postNotify(ctx, "notifications/cancelled", encodeParams(cancelledNotificationParams{RequestID: requestID, Reason: reason, Meta: defaultRequestMeta()}), "")
}

func (c *httpClient) Close() error { return nil }

func (c *httpClient) call(ctx context.Context, method string, params json.RawMessage, nameHeader string, extraHeaders map[string]string) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := rpcRequest{JSONRPC: JSONRPCVersion, ID: id, Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	c.applyRequestHeaders(httpReq, method, nameHeader)
	for key, value := range extraHeaders {
		httpReq.Header.Set(key, value)
	}

	httpResp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("HTTP POST: %w", err)
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		bodyText, _ := io.ReadAll(io.LimitReader(httpResp.Body, 8192))
		if len(bodyText) == 0 {
			return nil, fmt.Errorf("HTTP %d from %s", httpResp.StatusCode, c.url)
		}
		return nil, fmt.Errorf("HTTP %d from %s: %s", httpResp.StatusCode, c.url, strings.TrimSpace(string(bodyText)))
	}

	rpc, err := decodeRPCResponse(httpResp.Header.Get("Content-Type"), httpResp.Body)
	if err != nil {
		return nil, err
	}
	if rpc.Error != nil {
		return nil, rpc.Error
	}
	return rpc.Result, nil
}

func (c *httpClient) postNotify(ctx context.Context, method string, params json.RawMessage, nameHeader string) error {
	req := rpcRequest{JSONRPC: JSONRPCVersion, Method: method, Params: params}
	body, err := json.Marshal(req)
	if err != nil {
		return err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	c.applyRequestHeaders(httpReq, method, nameHeader)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, c.url)
	}
	return nil
}

func (c *httpClient) applyRequestHeaders(req *http.Request, method, nameHeader string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", ProtocolVersion2026)
	req.Header.Set("Mcp-Method", method)
	if strings.TrimSpace(nameHeader) != "" {
		req.Header.Set("Mcp-Name", encodeMCPHeaderValue(nameHeader))
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
}

func encodeMCPHeaderValue(value string) string {
	if value == "" {
		return ""
	}
	if httpHeaderNameRE.MatchString(value) {
		return value
	}
	return "base64:" + base64.StdEncoding.EncodeToString([]byte(value))
}

func (c *httpClient) toolArgumentHeaders(name string, args map[string]any) map[string]string {
	c.mu.RLock()
	tool, ok := c.tools[name]
	c.mu.RUnlock()
	if !ok || len(args) == 0 {
		return nil
	}
	headers := map[string]string{}
	for _, mapping := range toolHeaderMappings(tool) {
		value, ok := args[mapping.arg]
		if !ok || !isPrimitiveHeaderValue(value) {
			continue
		}
		headers["Mcp-Param-"+mapping.header] = encodeMCPHeaderValue(fmt.Sprint(value))
	}
	return headers
}

func isPrimitiveHeaderValue(value any) bool {
	switch value.(type) {
	case string, bool, float64, float32, int, int64, int32, uint, uint64, uint32:
		return true
	default:
		return false
	}
}

func decodeRPCResponse(contentType string, body io.Reader) (*rpcResponse, error) {
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		return decodeRPCResponseFromSSE(body)
	}
	var rpc rpcResponse
	if err := json.NewDecoder(body).Decode(&rpc); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &rpc, nil
}

func decodeRPCResponseFromSSE(body io.Reader) (*rpcResponse, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var dataLines []string
	flush := func() (*rpcResponse, bool, error) {
		if len(dataLines) == 0 {
			return nil, false, nil
		}
		payload := strings.Join(dataLines, "\n")
		dataLines = nil
		payload = strings.TrimSpace(payload)
		if payload == "" {
			return nil, false, nil
		}
		var rpc rpcResponse
		if err := json.Unmarshal([]byte(payload), &rpc); err != nil {
			return nil, false, fmt.Errorf("decode SSE response: %w", err)
		}
		if rpc.ID == nil && rpc.Method != "" {
			return nil, false, nil
		}
		return &rpc, true, nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "":
			if rpc, ok, err := flush(); err != nil {
				return nil, err
			} else if ok {
				return rpc, nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read SSE response: %w", err)
	}
	if rpc, ok, err := flush(); err != nil {
		return nil, err
	} else if ok {
		return rpc, nil
	}
	return nil, fmt.Errorf("empty SSE response")
}
