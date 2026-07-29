package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// stdioClient implements the MCP client over a subprocess stdin/stdout stream.
type stdioClient struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner

	mu       sync.Mutex
	nextID   atomic.Int64
	pending  map[int64]chan *rpcResponse
	listenCh chan Notification
	timeout  time.Duration
}

// NewStdioClient creates a new stdio MCP client.
func NewStdioClient(name, command string, args []string, env map[string]string) Client {
	return NewStdioClientWithContext(context.Background(), name, command, args, env)
}

// NewStdioClientWithContext creates a new stdio MCP client bound to ctx for the
// lifetime of the spawned subprocess.
func NewStdioClientWithContext(ctx context.Context, name, command string, args []string, env map[string]string) Client {
	cmd := exec.CommandContext(ctx, command, args...)
	if len(env) > 0 {
		cmd.Env = append([]string(nil), os.Environ()...)
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	return &stdioClient{
		name:     name,
		cmd:      cmd,
		pending:  make(map[int64]chan *rpcResponse),
		listenCh: make(chan Notification, 64),
		timeout:  60 * time.Second,
	}
}

func (c *stdioClient) forwardStderr(r io.ReadCloser) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.Warn("mcp stdio: server stderr", "server", c.name, "output", scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		slog.Debug("mcp stdio: stderr read error", "server", c.name, "err", err)
	}
}

func (c *stdioClient) Discover(ctx context.Context) (*DiscoverResult, error) {
	resp, err := c.call(ctx, "server/discover", encodeParams(discoverParams{Meta: defaultRequestMeta()}))
	if err != nil {
		return nil, fmt.Errorf("mcp stdio %s: server/discover: %w", c.name, err)
	}
	var result DiscoverResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp stdio %s: parse server/discover: %w", c.name, err)
	}
	result.ResultType = normalizeResultType(result.ResultType)
	return &result, nil
}

func (c *stdioClient) Initialize(ctx context.Context) error {
	stdin, err := c.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("mcp stdio %s: stdin pipe: %w", c.name, err)
	}
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("mcp stdio %s: stdout pipe: %w", c.name, err)
	}
	stderr, err := c.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("mcp stdio %s: stderr pipe: %w", c.name, err)
	}
	c.stdin = stdin
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("mcp stdio %s: start command: %w", c.name, err)
	}
	go c.forwardStderr(stderr)
	go c.readLoop()

	if _, err := c.Discover(ctx); err == nil {
		return nil
	} else if !isMethodNotFoundError(err) {
		return err
	}

	resp, err := c.call(ctx, "initialize", encodeParams(defaultLegacyInitializeParams()))
	if err != nil {
		return fmt.Errorf("mcp stdio %s: initialize: %w", c.name, err)
	}
	var result initializeResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("mcp stdio %s: parse initialize result: %w", c.name, err)
	}
	return c.notify("notifications/initialized", encodeParams(discoverParams{Meta: defaultRequestMeta()}))
}

func (c *stdioClient) ListTools(ctx context.Context) ([]Tool, error) {
	var out []Tool
	var cursor *string
	for {
		resp, err := c.call(ctx, "tools/list", encodeParams(listParams{Cursor: cursor, Meta: defaultRequestMeta()}))
		if err != nil {
			return nil, fmt.Errorf("mcp stdio %s: tools/list: %w", c.name, err)
		}
		var result toolsListResult
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("mcp stdio %s: parse tools/list: %w", c.name, err)
		}
		out = append(out, filterValidTools(c.name, result.Tools)...)
		if result.NextCursor == nil {
			break
		}
		cursor = result.NextCursor
	}
	return out, nil
}

func (c *stdioClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	return c.CallToolWithInput(ctx, name, args, nil, nil)
}

func (c *stdioClient) CallToolWithInput(ctx context.Context, name string, args map[string]any, inputResponses, requestState json.RawMessage) (*ToolResult, error) {
	resp, err := c.call(ctx, "tools/call", encodeParams(toolsCallParams{Name: name, Arguments: args, InputResponses: inputResponses, RequestState: requestState, Meta: defaultRequestMeta()}))
	if err != nil {
		return nil, fmt.Errorf("mcp stdio %s: tools/call %s: %w", c.name, name, err)
	}
	var result ToolResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp stdio %s: parse tools/call result: %w", c.name, err)
	}
	result.ResultType = normalizeResultType(result.ResultType)
	return &result, nil
}

func (c *stdioClient) ListResources(ctx context.Context) ([]Resource, error) {
	var out []Resource
	var cursor *string
	for {
		resp, err := c.call(ctx, "resources/list", encodeParams(listParams{Cursor: cursor, Meta: defaultRequestMeta()}))
		if err != nil {
			return nil, fmt.Errorf("mcp stdio %s: resources/list: %w", c.name, err)
		}
		var result resourcesListResult
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("mcp stdio %s: parse resources/list: %w", c.name, err)
		}
		out = append(out, result.Resources...)
		if result.NextCursor == nil {
			break
		}
		cursor = result.NextCursor
	}
	return out, nil
}

func (c *stdioClient) ListResourceTemplates(ctx context.Context) ([]ResourceTemplate, error) {
	var out []ResourceTemplate
	var cursor *string
	for {
		resp, err := c.call(ctx, "resources/templates/list", encodeParams(listParams{Cursor: cursor, Meta: defaultRequestMeta()}))
		if err != nil {
			return nil, fmt.Errorf("mcp stdio %s: resources/templates/list: %w", c.name, err)
		}
		var result resourceTemplatesListResult
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("mcp stdio %s: parse resources/templates/list: %w", c.name, err)
		}
		out = append(out, result.ResourceTemplates...)
		if result.NextCursor == nil {
			break
		}
		cursor = result.NextCursor
	}
	return out, nil
}

func (c *stdioClient) ReadResource(ctx context.Context, uri string) (*ResourceReadResult, error) {
	resp, err := c.call(ctx, "resources/read", encodeParams(resourceReadParams{URI: uri, Meta: defaultRequestMeta()}))
	if err != nil {
		return nil, fmt.Errorf("mcp stdio %s: resources/read %s: %w", c.name, uri, err)
	}
	var result ResourceReadResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp stdio %s: parse resources/read result: %w", c.name, err)
	}
	result.ResultType = normalizeResultType(result.ResultType)
	return &result, nil
}

func (c *stdioClient) ListPrompts(ctx context.Context) ([]Prompt, error) {
	var out []Prompt
	var cursor *string
	for {
		resp, err := c.call(ctx, "prompts/list", encodeParams(listParams{Cursor: cursor, Meta: defaultRequestMeta()}))
		if err != nil {
			return nil, fmt.Errorf("mcp stdio %s: prompts/list: %w", c.name, err)
		}
		var result promptsListResult
		if err := json.Unmarshal(resp, &result); err != nil {
			return nil, fmt.Errorf("mcp stdio %s: parse prompts/list: %w", c.name, err)
		}
		out = append(out, result.Prompts...)
		if result.NextCursor == nil {
			break
		}
		cursor = result.NextCursor
	}
	return out, nil
}

func (c *stdioClient) GetPrompt(ctx context.Context, name string, args map[string]any) (*PromptGetResult, error) {
	resp, err := c.call(ctx, "prompts/get", encodeParams(promptGetParams{Name: name, Arguments: args, Meta: defaultRequestMeta()}))
	if err != nil {
		return nil, fmt.Errorf("mcp stdio %s: prompts/get %s: %w", c.name, name, err)
	}
	var result PromptGetResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp stdio %s: parse prompts/get result: %w", c.name, err)
	}
	result.ResultType = normalizeResultType(result.ResultType)
	return &result, nil
}

func (c *stdioClient) Listen(ctx context.Context) (<-chan Notification, error) {
	out := make(chan Notification, 64)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case n, ok := <-c.listenCh:
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

func (c *stdioClient) Cancel(_ context.Context, requestID any, reason string) error {
	return c.notify("notifications/cancelled", encodeParams(cancelledNotificationParams{RequestID: requestID, Reason: reason, Meta: defaultRequestMeta()}))
}

func (c *stdioClient) Close() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = c.cmd.Process.Kill()
			<-done
		}
	}
	return nil
}

func (c *stdioClient) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := rpcRequest{JSONRPC: JSONRPCVersion, ID: id, Method: method, Params: params}
	ch := make(chan *rpcResponse, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	b, err := json.Marshal(req)
	if err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	b = append(b, '\n')

	c.mu.Lock()
	_, writeErr := c.stdin.Write(b)
	c.mu.Unlock()
	if writeErr != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("write request: %w", writeErr)
	}

	select {
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		_ = c.Cancel(context.WithoutCancel(ctx), id, ctx.Err().Error())
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-time.After(c.timeout):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		_ = c.Cancel(context.WithoutCancel(ctx), id, "timeout")
		return nil, fmt.Errorf("timeout waiting for response to %s", method)
	}
}

func (c *stdioClient) notify(method string, params json.RawMessage) error {
	if c.stdin == nil {
		return fmt.Errorf("mcp stdio %s: stdin is not available", c.name)
	}
	req := rpcRequest{JSONRPC: JSONRPCVersion, Method: method, Params: params}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.stdin.Write(b)
	return err
}

func (c *stdioClient) readLoop() {
	defer close(c.listenCh)
	for c.scanner.Scan() {
		line := append([]byte(nil), c.scanner.Bytes()...)
		if len(line) == 0 {
			continue
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(line, &raw); err != nil {
			slog.Debug("mcp stdio: parse message", "server", c.name, "err", err)
			continue
		}
		if _, hasID := raw["id"]; !hasID {
			var n Notification
			if err := json.Unmarshal(line, &n); err != nil || n.Method == "" {
				slog.Debug("mcp stdio: parse notification", "server", c.name, "err", err)
				continue
			}
			select {
			case c.listenCh <- n:
			default:
				slog.Warn("mcp stdio: notification buffer full; dropping", "server", c.name, "method", n.Method)
			}
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			slog.Debug("mcp stdio: parse response", "server", c.name, "err", err)
			continue
		}
		var id int64
		switch v := resp.ID.(type) {
		case float64:
			id = int64(v)
		case json.Number:
			id, _ = v.Int64()
		}
		c.mu.Lock()
		ch, ok := c.pending[id]
		if ok {
			delete(c.pending, id)
		}
		c.mu.Unlock()
		if ok {
			ch <- &resp
		}
	}
	c.mu.Lock()
	for id, ch := range c.pending {
		ch <- &rpcResponse{Error: &rpcError{Code: -1, Message: "server process exited"}}
		delete(c.pending, id)
	}
	c.mu.Unlock()
}
