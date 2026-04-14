package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type gatewayClient struct {
	httpClient *http.Client
	baseHTTP   string
	baseWS     string
}

type rpcEnvelope struct {
	ID     string          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcErr         `json:"error,omitempty"`
}

type rpcErr struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type streamDeltaEnvelope struct {
	ReqID   string `json:"req_id"`
	Content string `json:"content"`
}

type streamEventEnvelope struct {
	ReqID string         `json:"req_id"`
	Event map[string]any `json:"event"`
}

type sessionMessageEnvelope struct {
	PeerID  string         `json:"peer_id"`
	Source  string         `json:"source"`
	Message map[string]any `json:"message"`
}

type agentRunResult struct {
	SessionKey    string           `json:"session_key"`
	Attempts      int              `json:"attempts"`
	AssistantText string           `json:"assistant_text"`
	Usage         map[string]any   `json:"usage,omitempty"`
	Steps         int              `json:"steps"`
	Events        []map[string]any `json:"events,omitempty"`
}

func newGatewayClient(state *repoState, timeout time.Duration) *gatewayClient {
	return &gatewayClient{
		httpClient: &http.Client{Timeout: timeout},
		baseHTTP:   state.baseHTTPURL(),
		baseWS:     state.baseWSURL(),
	}
}

func (c *gatewayClient) health(ctx context.Context) (map[string]any, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseHTTP+"/healthz", nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, resp.StatusCode, err
	}
	return payload, resp.StatusCode, nil
}

func (c *gatewayClient) version(ctx context.Context) (map[string]any, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseHTTP+"/", nil)
	if err != nil {
		return nil, 0, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return map[string]any{"raw": strings.TrimSpace(string(body))}, resp.StatusCode, nil
	}
	return payload, resp.StatusCode, nil
}

func (c *gatewayClient) rpc(ctx context.Context, peer, method string, params any, out any) error {
	conn, err := c.openConn(ctx, peer)
	if err != nil {
		return err
	}
	defer conn.Close()

	return c.rpcWithConn(conn, method, params, out)
}

func (c *gatewayClient) openConn(ctx context.Context, peer string) (*websocket.Conn, error) {
	u, err := url.Parse(c.baseWS + "/v1/ws")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("peer_id", peer)
	u.RawQuery = q.Encode()

	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("websocket dial failed: %s", resp.Status)
		}
		return nil, err
	}
	return conn, nil
}

func (c *gatewayClient) rpcWithConn(conn *websocket.Conn, method string, params any, out any) error {
	frame := map[string]any{
		"id":     "1",
		"method": method,
	}
	if params != nil {
		frame["params"] = params
	}
	if err := conn.WriteJSON(frame); err != nil {
		return err
	}

	for {
		var env rpcEnvelope
		if err := conn.ReadJSON(&env); err != nil {
			return err
		}
		if env.ID == "" {
			continue
		}
		if env.Error != nil {
			return fmt.Errorf("%s", env.Error.Message)
		}
		if out == nil {
			return nil
		}
		if len(env.Result) == 0 {
			return nil
		}
		return json.Unmarshal(env.Result, out)
	}
}

func (c *gatewayClient) agentRunStream(
	ctx context.Context,
	peer string,
	params map[string]any,
	onDelta func(string),
	onEvent func(map[string]any),
	onSessionMessage func(sessionMessageEnvelope),
) (*agentRunResult, error) {
	conn, err := c.openConn(ctx, peer)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	req := map[string]any{
		"id":     "1",
		"method": "agent.run",
		"params": params,
	}
	if err := conn.WriteJSON(req); err != nil {
		return nil, err
	}

	for {
		var env rpcEnvelope
		if err := conn.ReadJSON(&env); err != nil {
			return nil, err
		}
		if env.Method != "" {
			switch env.Method {
			case "stream.delta":
				if onDelta != nil {
					var delta streamDeltaEnvelope
					if err := json.Unmarshal(env.Params, &delta); err == nil && delta.ReqID == "1" {
						onDelta(delta.Content)
					}
				}
			case "stream.event":
				if onEvent != nil {
					var event streamEventEnvelope
					if err := json.Unmarshal(env.Params, &event); err == nil && event.ReqID == "1" {
						onEvent(event.Event)
					}
				}
			case "session.message":
				if onSessionMessage != nil {
					var sessionMsg sessionMessageEnvelope
					if err := json.Unmarshal(env.Params, &sessionMsg); err == nil {
						onSessionMessage(sessionMsg)
					}
				}
			}
			continue
		}
		if env.Error != nil {
			return nil, fmt.Errorf("%s", env.Error.Message)
		}
		var result agentRunResult
		if err := json.Unmarshal(env.Result, &result); err != nil {
			return nil, err
		}
		return &result, nil
	}
}
