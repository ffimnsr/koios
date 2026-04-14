package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// stdioClient implements the MCP Client interface over a subprocess's
// stdin/stdout using newline-delimited JSON-RPC 2.0.
type stdioClient struct {
	name    string
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner

	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan *rpcResponse
}

// NewStdioClient creates a new MCP client that communicates with the given
// command over stdin/stdout. The command is not started until Initialize is
// called.
func NewStdioClient(name, command string, args []string, env map[string]string) Client {
	cmd := exec.Command(command, args...)
	if len(env) > 0 {
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
	}
	return &stdioClient{
		name:    name,
		cmd:     cmd,
		pending: make(map[int64]chan *rpcResponse),
	}
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
	c.stdin = stdin
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 4*1024*1024), 4*1024*1024)

	if err := c.cmd.Start(); err != nil {
		return fmt.Errorf("mcp stdio %s: start command: %w", c.name, err)
	}

	// Start dispatcher goroutine that reads responses and routes them.
	go c.readLoop()

	params := encodeParams(initializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities:    map[string]any{},
		ClientInfo:      map[string]string{"name": "koios", "version": "1.0"},
	})
	resp, err := c.call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("mcp stdio %s: initialize: %w", c.name, err)
	}
	var result initializeResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return fmt.Errorf("mcp stdio %s: parse initialize result: %w", c.name, err)
	}
	slog.Debug("mcp stdio initialized", "server", c.name, "protocol", result.ProtocolVersion)

	// Send notifications/initialized (no response expected).
	return c.notify("notifications/initialized", nil)
}

func (c *stdioClient) ListTools(ctx context.Context) ([]Tool, error) {
	resp, err := c.call(ctx, "tools/list", encodeParams(map[string]any{}))
	if err != nil {
		return nil, fmt.Errorf("mcp stdio %s: tools/list: %w", c.name, err)
	}
	var result toolsListResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp stdio %s: parse tools/list: %w", c.name, err)
	}
	return result.Tools, nil
}

func (c *stdioClient) CallTool(ctx context.Context, name string, args map[string]any) (*ToolResult, error) {
	params := encodeParams(toolsCallParams{Name: name, Arguments: args})
	resp, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return nil, fmt.Errorf("mcp stdio %s: tools/call %s: %w", c.name, name, err)
	}
	var result ToolResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("mcp stdio %s: parse tools/call result: %w", c.name, err)
	}
	return &result, nil
}

func (c *stdioClient) Close() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	return nil
}

// call sends a JSON-RPC request and waits for the response.
func (c *stdioClient) call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := rpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
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
		return nil, ctx.Err()
	case resp := <-ch:
		if resp.Error != nil {
			return nil, resp.Error
		}
		return resp.Result, nil
	case <-time.After(60 * time.Second):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, fmt.Errorf("timeout waiting for response to %s", method)
	}
}

// notify sends a JSON-RPC notification (no ID, no response expected).
func (c *stdioClient) notify(method string, params json.RawMessage) error {
	req := rpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
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

// readLoop reads newline-delimited JSON responses and dispatches them to
// waiting callers.
func (c *stdioClient) readLoop() {
	for c.scanner.Scan() {
		line := c.scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var resp rpcResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			slog.Debug("mcp stdio: parse response", "server", c.name, "err", err)
			continue
		}
		// Notifications (no id) are not awaited.
		if resp.ID == nil {
			continue
		}
		// The JSON decoder returns numeric IDs as float64.
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
	// Mark all pending callers as failed when the process exits.
	c.mu.Lock()
	for id, ch := range c.pending {
		ch <- &rpcResponse{Error: &rpcError{Code: -1, Message: "server process exited"}}
		delete(c.pending, id)
	}
	c.mu.Unlock()
}
