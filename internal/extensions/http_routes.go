package extensions

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/ffimnsr/koios/internal/mcp"
)

const HTTPRouteNamespacePrefix = "/v1/extensions/"

type HTTPRouteCaller interface {
	CallTool(ctx context.Context, fullName string, rawArgs json.RawMessage) (any, error)
}

type DiscoveredHTTPRouteBinding struct {
	Manifest DiscoveredManifest
	Binding  HTTPRouteBinding
}

type HTTPRouteResponse struct {
	Status      int                 `json:"status,omitempty"`
	Headers     map[string][]string `json:"headers,omitempty"`
	ContentType string              `json:"content_type,omitempty"`
	Body        string              `json:"body,omitempty"`
	JSON        any                 `json:"json,omitempty"`
}

type HTTPRouteHandler struct {
	caller HTTPRouteCaller
	routes map[string][]DiscoveredHTTPRouteBinding
}

func HTTPRouteBindings(manifests []DiscoveredManifest) ([]DiscoveredHTTPRouteBinding, error) {
	bindings := make([]DiscoveredHTTPRouteBinding, 0)
	for _, manifest := range manifests {
		if !manifest.Enabled() || !manifest.HasCapability(CapabilityHTTPRoutes) {
			continue
		}
		for _, binding := range manifest.Manifest.Routes {
			if binding.Enabled != nil && !*binding.Enabled {
				continue
			}
			if err := validateHTTPRouteBinding(binding, manifest.Path); err != nil {
				return nil, err
			}
			bindings = append(bindings, DiscoveredHTTPRouteBinding{Manifest: manifest, Binding: binding})
		}
	}
	sort.Slice(bindings, func(i, j int) bool {
		if bindings[i].Manifest.Manifest.ID == bindings[j].Manifest.Manifest.ID {
			if bindings[i].Binding.Path == bindings[j].Binding.Path {
				return bindings[i].Binding.Method < bindings[j].Binding.Method
			}
			return bindings[i].Binding.Path < bindings[j].Binding.Path
		}
		return bindings[i].Manifest.Manifest.ID < bindings[j].Manifest.Manifest.ID
	})
	return bindings, nil
}

func NewHTTPRouteHandler(caller HTTPRouteCaller, manifests []DiscoveredManifest) (*HTTPRouteHandler, error) {
	bindings, err := HTTPRouteBindings(manifests)
	if err != nil {
		return nil, err
	}
	routes := make(map[string][]DiscoveredHTTPRouteBinding)
	for _, binding := range bindings {
		routes[binding.Manifest.Manifest.ID] = append(routes[binding.Manifest.Manifest.ID], binding)
	}
	return &HTTPRouteHandler{caller: caller, routes: routes}, nil
}

func (h *HTTPRouteHandler) HasRoutes() bool {
	return h != nil && len(h.routes) > 0
}

func (h *HTTPRouteHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h == nil || len(h.routes) == 0 || h.caller == nil {
		http.NotFound(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, HTTPRouteNamespacePrefix) {
		http.NotFound(w, r)
		return
	}
	relative := strings.TrimPrefix(r.URL.Path, HTTPRouteNamespacePrefix)
	extID, routePath, ok := strings.Cut(relative, "/")
	if !ok {
		extID = relative
		routePath = ""
	}
	extID = strings.TrimSpace(extID)
	if extID == "" {
		http.NotFound(w, r)
		return
	}
	routePath = normalizeHTTPRoutePath(routePath)
	bindings := h.routes[extID]
	for _, binding := range bindings {
		if binding.Binding.Method == r.Method && binding.Binding.Path == routePath {
			h.handleRoute(w, r, binding)
			return
		}
	}
	allowed := allowedMethods(bindings, routePath)
	if len(allowed) > 0 {
		w.Header().Set("Allow", strings.Join(allowed, ", "))
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	http.NotFound(w, r)
}

func (h *HTTPRouteHandler) handleRoute(w http.ResponseWriter, r *http.Request, binding DiscoveredHTTPRouteBinding) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}
	args, err := json.Marshal(map[string]any{
		"http": map[string]any{
			"extension_id":   binding.Manifest.Manifest.ID,
			"extension_name": binding.Manifest.Manifest.Name,
			"route": map[string]any{
				"name":      binding.Binding.Name,
				"method":    binding.Binding.Method,
				"path":      binding.Binding.Path,
				"tool":      binding.Binding.Tool,
				"namespace": HTTPRouteNamespacePrefix + binding.Manifest.Manifest.ID,
			},
			"request": map[string]any{
				"method":       r.Method,
				"path":         binding.Binding.Path,
				"raw_path":     r.URL.Path,
				"query":        r.URL.Query(),
				"headers":      r.Header,
				"body":         string(body),
				"content_type": r.Header.Get("Content-Type"),
				"remote_addr":  r.RemoteAddr,
			},
		},
	})
	if err != nil {
		http.Error(w, "failed to encode request", http.StatusInternalServerError)
		return
	}
	result, err := h.caller.CallTool(r.Context(), mcp.PluginToolPrefix(binding.Manifest.Manifest.ID)+binding.Binding.Tool, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	response, ok, err := decodeHTTPRouteResponse(result)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	if !ok {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, fmt.Sprint(result))
		return
	}
	for key, values := range response.Headers {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if response.ContentType != "" && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", response.ContentType)
	}
	status := response.Status
	if status == 0 {
		status = http.StatusOK
	}
	if response.JSON != nil {
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(response.JSON)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, response.Body)
	}
}

func allowedMethods(bindings []DiscoveredHTTPRouteBinding, routePath string) []string {
	methods := make([]string, 0)
	seen := make(map[string]struct{})
	for _, binding := range bindings {
		if binding.Binding.Path != routePath {
			continue
		}
		if _, exists := seen[binding.Binding.Method]; exists {
			continue
		}
		seen[binding.Binding.Method] = struct{}{}
		methods = append(methods, binding.Binding.Method)
	}
	sort.Strings(methods)
	return methods
}

func decodeHTTPRouteResponse(result any) (HTTPRouteResponse, bool, error) {
	text, ok := result.(string)
	if !ok {
		return HTTPRouteResponse{}, false, nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return HTTPRouteResponse{Status: http.StatusNoContent}, true, nil
	}
	if !strings.HasPrefix(trimmed, "{") {
		return HTTPRouteResponse{}, false, nil
	}
	var response HTTPRouteResponse
	if err := json.Unmarshal([]byte(trimmed), &response); err != nil {
		return HTTPRouteResponse{}, false, nil
	}
	if response.Status < 0 || response.Status > 999 {
		return HTTPRouteResponse{}, false, fmt.Errorf("invalid extension route status %d", response.Status)
	}
	return response, true, nil
}
