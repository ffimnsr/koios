package extensions

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ffimnsr/koios/internal/mcp"
)

type routeCallerStub struct {
	lastName string
	lastArgs map[string]any
	result   any
	err      error
}

func (s *routeCallerStub) CallTool(_ context.Context, fullName string, rawArgs json.RawMessage) (any, error) {
	s.lastName = fullName
	if len(rawArgs) > 0 {
		_ = json.Unmarshal(rawArgs, &s.lastArgs)
	}
	return s.result, s.err
}

func TestHTTPRouteHandlerDispatchesScopedRoute(t *testing.T) {
	caller := &routeCallerStub{result: `{"status":201,"content_type":"application/json","json":{"ok":true}}`}
	h, err := NewHTTPRouteHandler(caller, []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.echo",
			Name:         "echo",
			Capabilities: []string{CapabilityHTTPRoutes},
			Routes: []HTTPRouteBinding{{
				Method: "POST",
				Path:   "/echo",
				Tool:   "http_echo",
			}},
		},
		Path: "demo",
	}})
	if err != nil {
		t.Fatalf("NewHTTPRouteHandler: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, HTTPRouteNamespacePrefix+"demo.echo/echo?x=1", strings.NewReader("hello"))
	req.Header.Set("Content-Type", "text/plain")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)

	if res.Code != http.StatusCreated {
		t.Fatalf("status = %d, body=%s", res.Code, res.Body.String())
	}
	if caller.lastName != mcp.PluginToolPrefix("demo.echo")+"http_echo" {
		t.Fatalf("unexpected tool name: %q", caller.lastName)
	}
	httpArgs, _ := caller.lastArgs["http"].(map[string]any)
	requestArgs, _ := httpArgs["request"].(map[string]any)
	if requestArgs["body"] != "hello" {
		t.Fatalf("expected body to be forwarded, got %#v", caller.lastArgs)
	}
	if got := res.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("unexpected content type: %q", got)
	}
	if !strings.Contains(res.Body.String(), `"ok":true`) {
		t.Fatalf("unexpected response body: %s", res.Body.String())
	}
}

func TestHTTPRouteHandlerRejectsWrongMethod(t *testing.T) {
	h, err := NewHTTPRouteHandler(&routeCallerStub{}, []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.echo",
			Capabilities: []string{CapabilityHTTPRoutes},
			Routes:       []HTTPRouteBinding{{Method: "GET", Path: "/echo", Tool: "http_echo"}},
		},
	}})
	if err != nil {
		t.Fatalf("NewHTTPRouteHandler: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, HTTPRouteNamespacePrefix+"demo.echo/echo", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", res.Code)
	}
	if allow := res.Header().Get("Allow"); allow != "GET" {
		t.Fatalf("unexpected Allow header: %q", allow)
	}
}

func TestHTTPRouteHandlerFallsBackToPlainText(t *testing.T) {
	caller := &routeCallerStub{result: "plain text response"}
	h, err := NewHTTPRouteHandler(caller, []DiscoveredManifest{{
		Manifest: Manifest{
			ID:           "demo.text",
			Capabilities: []string{CapabilityHTTPRoutes},
			Routes:       []HTTPRouteBinding{{Method: "GET", Path: "/status", Tool: "http_status"}},
		},
	}})
	if err != nil {
		t.Fatalf("NewHTTPRouteHandler: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, HTTPRouteNamespacePrefix+"demo.text/status", nil)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK || strings.TrimSpace(res.Body.String()) != "plain text response" {
		t.Fatalf("unexpected plain-text response: status=%d body=%q", res.Code, res.Body.String())
	}
}
