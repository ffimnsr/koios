package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type webSearchParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type webFetchParams struct {
	URL string `json:"url"`
}

type webBrowserRunParams struct {
	Action         string         `json:"action"`
	URL            string         `json:"url,omitempty"`
	HTML           string         `json:"html,omitempty"`
	Elements       []scrapeTarget `json:"elements,omitempty"`
	Prompt         string         `json:"prompt,omitempty"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	Options        map[string]any `json:"options,omitempty"`
}

type scrapeTarget struct {
	Selector string `json:"selector"`
}

type BrowserRunConfig struct {
	Enabled        bool
	AccountID      string
	APIToken       string
	BaseURL        string
	DefaultTimeout time.Duration
}

type WebSearchProviderConfig struct {
	Name           string
	APIKey         string
	BaseURL        string
	DefaultTimeout time.Duration
}

type WebSearchConfig struct {
	Enabled   bool
	Providers []WebSearchProviderConfig
}

type braveWebSearchResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}

type exaWebSearchRequest struct {
	Query      string `json:"query"`
	NumResults int    `json:"numResults"`
	Contents   struct {
		Highlights bool `json:"highlights"`
		Summary    bool `json:"summary"`
		Text       bool `json:"text"`
	} `json:"contents"`
}

type exaWebSearchResponse struct {
	Results []struct {
		Title      string   `json:"title"`
		URL        string   `json:"url"`
		Summary    string   `json:"summary"`
		Highlights []string `json:"highlights"`
		Text       string   `json:"text"`
	} `json:"results"`
}

type tavilyWebSearchRequest struct {
	Query             string `json:"query"`
	SearchDepth       string `json:"search_depth"`
	Topic             string `json:"topic"`
	MaxResults        int    `json:"max_results"`
	IncludeAnswer     bool   `json:"include_answer"`
	IncludeRawContent bool   `json:"include_raw_content"`
	IncludeImages     bool   `json:"include_images"`
	IncludeFavicon    bool   `json:"include_favicon"`
}

type tavilyWebSearchResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

type cloudflareAPIEnvelope struct {
	Success  bool              `json:"success"`
	Errors   []cloudflareError `json:"errors"`
	Messages []cloudflareError `json:"messages"`
	Result   json.RawMessage   `json:"result"`
}

type cloudflareError struct {
	Code    any    `json:"code"`
	Message string `json:"message"`
}

const (
	defaultWebSearchProvider = "brave"
	defaultBraveSearchURL    = "https://api.search.brave.com/res/v1/web/search"
	defaultExaSearchURL      = "https://api.exa.ai/search"
	defaultTavilySearchURL   = "https://api.tavily.com/search"
	defaultBrowserRunBaseURL = "https://api.cloudflare.com/client/v4"
)

var (
	titleRE  = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	scriptRE = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleRE  = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	tagRE    = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRE  = regexp.MustCompile(`\s+`)
)

func normalizeBrowserRunConfig(cfg BrowserRunConfig) BrowserRunConfig {
	cfg.AccountID = strings.TrimSpace(cfg.AccountID)
	cfg.APIToken = strings.TrimSpace(cfg.APIToken)
	cfg.BaseURL = strings.TrimSpace(cfg.BaseURL)
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBrowserRunBaseURL
	}
	if cfg.DefaultTimeout <= 0 {
		cfg.DefaultTimeout = 30 * time.Second
	}
	return cfg
}

func normalizeWebSearchConfig(cfg WebSearchConfig) WebSearchConfig {
	normalized := WebSearchConfig{Enabled: cfg.Enabled}
	providers := cfg.Providers
	if len(providers) == 0 {
		providers = []WebSearchProviderConfig{{Name: defaultWebSearchProvider}}
	}
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		provider.Name = strings.ToLower(strings.TrimSpace(provider.Name))
		if provider.Name == "" {
			provider.Name = defaultWebSearchProvider
		}
		if _, exists := seen[provider.Name]; exists {
			continue
		}
		seen[provider.Name] = struct{}{}
		provider.APIKey = strings.TrimSpace(provider.APIKey)
		provider.BaseURL = strings.TrimSpace(provider.BaseURL)
		if provider.BaseURL == "" {
			switch provider.Name {
			case "brave":
				provider.BaseURL = defaultBraveSearchURL
			case "exa":
				provider.BaseURL = defaultExaSearchURL
			case "tavily":
				provider.BaseURL = defaultTavilySearchURL
			}
		}
		if provider.DefaultTimeout <= 0 {
			provider.DefaultTimeout = 15 * time.Second
		}
		normalized.Providers = append(normalized.Providers, provider)
	}
	return normalized
}

func (h *Handler) runWebSearchTool(ctx context.Context, p webSearchParams) (map[string]any, error) {
	query := strings.TrimSpace(p.Query)
	if query == "" {
		return nil, fmt.Errorf("query is required")
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 10 {
		limit = 10
	}
	if !h.webSearchConfig.Enabled {
		return nil, fmt.Errorf("web_search tool is disabled")
	}
	if len(h.webSearchConfig.Providers) == 0 {
		return nil, fmt.Errorf("web_search has no configured providers")
	}
	var errs []string
	for _, provider := range h.webSearchConfig.Providers {
		var (
			result map[string]any
			err    error
		)
		switch provider.Name {
		case "brave":
			result, err = h.runBraveWebSearch(ctx, provider, query, limit)
		case "exa":
			result, err = h.runExaWebSearch(ctx, provider, query, limit)
		case "tavily":
			result, err = h.runTavilyWebSearch(ctx, provider, query, limit)
		default:
			err = fmt.Errorf("provider is not available")
		}
		if err == nil {
			return result, nil
		}
		errs = append(errs, provider.Name+": "+err.Error())
	}
	return nil, fmt.Errorf("web_search failed for all providers: %s", strings.Join(errs, "; "))
}

func (h *Handler) runBraveWebSearch(ctx context.Context, provider WebSearchProviderConfig, query string, limit int) (map[string]any, error) {
	if provider.APIKey == "" {
		return nil, fmt.Errorf("requires tools.web_search.%s.api_key", provider.Name)
	}
	searchURL, err := url.Parse(provider.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid web_search base url: %w", err)
	}
	params := searchURL.Query()
	params.Set("q", query)
	params.Set("count", fmt.Sprintf("%d", limit))
	searchURL.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "koios/1.0")
	req.Header.Set("X-Subscription-Token", provider.APIKey)

	client := h.webSearchClient
	if client == nil {
		client = &http.Client{Timeout: provider.DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read web_search response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(body))
		if len(message) > 300 {
			message = message[:300]
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("web_search provider %q returned %d: %s", provider.Name, resp.StatusCode, message)
	}

	var payload braveWebSearchResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode web_search response: %w", err)
	}

	results := make([]map[string]string, 0, len(payload.Web.Results))
	for _, item := range payload.Web.Results {
		resultURL := strings.TrimSpace(item.URL)
		if resultURL == "" {
			continue
		}
		results = append(results, map[string]string{
			"title":   strings.TrimSpace(item.Title),
			"url":     resultURL,
			"snippet": strings.TrimSpace(item.Description),
		})
		if len(results) >= limit {
			break
		}
	}

	return map[string]any{
		"engine":  provider.Name,
		"query":   query,
		"results": results,
		"count":   len(results),
	}, nil
}

func (h *Handler) runExaWebSearch(ctx context.Context, provider WebSearchProviderConfig, query string, limit int) (map[string]any, error) {
	if provider.APIKey == "" {
		return nil, fmt.Errorf("requires tools.web_search.%s.api_key", provider.Name)
	}
	payload := exaWebSearchRequest{Query: query, NumResults: limit}
	payload.Contents.Highlights = true
	payload.Contents.Summary = true
	payload.Contents.Text = true
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode web_search request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.BaseURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "koios/1.0")
	req.Header.Set("x-api-key", provider.APIKey)
	client := h.webSearchClient
	if client == nil {
		client = &http.Client{Timeout: provider.DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read web_search response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 300 {
			message = message[:300]
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("web_search provider %q returned %d: %s", provider.Name, resp.StatusCode, message)
	}
	var payloadResp exaWebSearchResponse
	if err := json.Unmarshal(responseBody, &payloadResp); err != nil {
		return nil, fmt.Errorf("decode web_search response: %w", err)
	}
	results := make([]map[string]string, 0, len(payloadResp.Results))
	for _, item := range payloadResp.Results {
		resultURL := strings.TrimSpace(item.URL)
		if resultURL == "" {
			continue
		}
		snippet := strings.TrimSpace(item.Summary)
		if snippet == "" && len(item.Highlights) > 0 {
			snippet = strings.TrimSpace(item.Highlights[0])
		}
		if snippet == "" {
			snippet = strings.TrimSpace(item.Text)
		}
		results = append(results, map[string]string{
			"title":   strings.TrimSpace(item.Title),
			"url":     resultURL,
			"snippet": truncateWebSearchSnippet(snippet),
		})
		if len(results) >= limit {
			break
		}
	}
	return map[string]any{
		"engine":  provider.Name,
		"query":   query,
		"results": results,
		"count":   len(results),
	}, nil
}

func (h *Handler) runTavilyWebSearch(ctx context.Context, provider WebSearchProviderConfig, query string, limit int) (map[string]any, error) {
	if provider.APIKey == "" {
		return nil, fmt.Errorf("requires tools.web_search.%s.api_key", provider.Name)
	}
	payload := tavilyWebSearchRequest{
		Query:             query,
		SearchDepth:       "basic",
		Topic:             "general",
		MaxResults:        limit,
		IncludeAnswer:     false,
		IncludeRawContent: false,
		IncludeImages:     false,
		IncludeFavicon:    false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode web_search request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.BaseURL, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "koios/1.0")
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	client := h.webSearchClient
	if client == nil {
		client = &http.Client{Timeout: provider.DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_search request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read web_search response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		message := strings.TrimSpace(string(responseBody))
		if len(message) > 300 {
			message = message[:300]
		}
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("web_search provider %q returned %d: %s", provider.Name, resp.StatusCode, message)
	}
	var payloadResp tavilyWebSearchResponse
	if err := json.Unmarshal(responseBody, &payloadResp); err != nil {
		return nil, fmt.Errorf("decode web_search response: %w", err)
	}
	results := make([]map[string]string, 0, len(payloadResp.Results))
	for _, item := range payloadResp.Results {
		resultURL := strings.TrimSpace(item.URL)
		if resultURL == "" {
			continue
		}
		results = append(results, map[string]string{
			"title":   strings.TrimSpace(item.Title),
			"url":     resultURL,
			"snippet": truncateWebSearchSnippet(strings.TrimSpace(item.Content)),
		})
		if len(results) >= limit {
			break
		}
	}
	return map[string]any{
		"engine":  provider.Name,
		"query":   query,
		"results": results,
		"count":   len(results),
	}, nil
}

func truncateWebSearchSnippet(snippet string) string {
	snippet = strings.TrimSpace(snippet)
	if len(snippet) <= 400 {
		return snippet
	}
	return strings.TrimSpace(snippet[:400])
}

func (h *Handler) rpcWebSearch(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p webSearchParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.runWebSearchTool(ctx, p)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWebBrowserRun(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p webBrowserRunParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.runWebBrowserRunTool(ctx, p)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWebFetch(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p webFetchParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.runWebFetchTool(ctx, p)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

// privateIPCIDRs lists IP ranges that must not be reached via web_fetch to
// prevent server-side request forgery (SSRF).
var privateIPCIDRs = func() []*net.IPNet {
	var cidrs []*net.IPNet
	for _, cidr := range []string{
		"127.0.0.0/8",    // loopback
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"169.254.0.0/16", // link-local / cloud metadata (AWS/GCP/Azure IMDS)
		"100.64.0.0/10",  // CGNAT shared address space (RFC 6598)
		"0.0.0.0/8",      // "this" network
		"::1/128",        // IPv6 loopback
		"fc00::/7",       // IPv6 unique-local
		"fe80::/10",      // IPv6 link-local
	} {
		_, n, _ := net.ParseCIDR(cidr)
		if n != nil {
			cidrs = append(cidrs, n)
		}
	}
	return cidrs
}()

func isPrivateIP(ip net.IP) bool {
	for _, cidr := range privateIPCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// blockPrivateURL returns an error when the URL's host is a private or
// reserved IP address, stopping trivial SSRF attempts at parse time.
func blockPrivateURL(ctx context.Context, u *url.URL) error {
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("requests to private or reserved IP addresses are not allowed")
		}
		return nil
	}
	var r net.Resolver
	addrs, err := r.LookupHost(ctx, host)
	if err != nil {
		return fmt.Errorf("could not resolve host %q: %w", host, err)
	}
	for _, addr := range addrs {
		if ip := net.ParseIP(addr); ip != nil && isPrivateIP(ip) {
			return fmt.Errorf("requests to private or reserved IP addresses are not allowed")
		}
	}
	return nil
}

// ssrfSafeTransport returns a cloned default transport whose dial hook
// re-validates the resolved IP on every new connection.  This prevents
// redirect-based SSRF bypass where a public URL redirects to a private one.
func ssrfSafeTransport() *http.Transport {
	base := http.DefaultTransport.(*http.Transport).Clone()
	dialer := &net.Dialer{}
	base.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(host); ip != nil {
			if isPrivateIP(ip) {
				return nil, fmt.Errorf("blocked: connection to private address %s", host)
			}
		} else {
			var r net.Resolver
			addrs, err := r.LookupHost(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, a := range addrs {
				if ip := net.ParseIP(a); ip != nil && isPrivateIP(ip) {
					return nil, fmt.Errorf("blocked: connection to private address %s", host)
				}
			}
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
	}
	return base
}

func (h *Handler) runWebBrowserRunTool(ctx context.Context, p webBrowserRunParams) (map[string]any, error) {
	if !h.browserRunConfig.Enabled {
		return nil, fmt.Errorf("web_browser_run tool is disabled")
	}
	action := strings.ToLower(strings.TrimSpace(p.Action))
	if action == "" {
		return nil, fmt.Errorf("action is required")
	}
	switch action {
	case "content", "markdown", "screenshot", "scrape", "json":
	default:
		return nil, fmt.Errorf("unsupported action %q", p.Action)
	}
	if strings.TrimSpace(p.URL) == "" && strings.TrimSpace(p.HTML) == "" {
		return nil, fmt.Errorf("either url or html is required")
	}
	if strings.TrimSpace(p.URL) != "" && strings.TrimSpace(p.HTML) != "" {
		return nil, fmt.Errorf("url and html are mutually exclusive")
	}
	if action == "scrape" && len(p.Elements) == 0 {
		return nil, fmt.Errorf("elements are required for scrape action")
	}
	if action == "json" && strings.TrimSpace(p.Prompt) == "" && len(p.ResponseFormat) == 0 {
		return nil, fmt.Errorf("prompt or response_format is required for json action")
	}
	payload := make(map[string]any, len(p.Options)+4)
	maps.Copy(payload, p.Options)
	if strings.TrimSpace(p.URL) != "" {
		payload["url"] = strings.TrimSpace(p.URL)
	}
	if strings.TrimSpace(p.HTML) != "" {
		payload["html"] = p.HTML
	}
	if action == "scrape" {
		payload["elements"] = p.Elements
	}
	if action == "json" {
		if strings.TrimSpace(p.Prompt) != "" {
			payload["prompt"] = strings.TrimSpace(p.Prompt)
		}
		if len(p.ResponseFormat) > 0 {
			payload["response_format"] = p.ResponseFormat
		}
	}
	endpoint, err := h.browserRunEndpoint(action)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode web_browser_run request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "koios/1.0")
	req.Header.Set("Authorization", "Bearer "+h.browserRunConfig.APIToken)
	client := h.browserRunClient
	if client == nil {
		client = &http.Client{Timeout: h.browserRunConfig.DefaultTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("web_browser_run request failed: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	if err != nil {
		return nil, fmt.Errorf("read web_browser_run response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("web_browser_run action %q returned %d: %s", action, resp.StatusCode, summarizeCloudflareErrorBody(responseBody))
	}
	switch action {
	case "content":
		return map[string]any{
			"engine":       "cloudflare-browser-run",
			"action":       action,
			"content_type": firstNonEmptyString(strings.TrimSpace(resp.Header.Get("Content-Type")), "text/html"),
			"content":      decodeCloudflareTextResult(responseBody),
		}, nil
	case "markdown":
		return map[string]any{
			"engine":       "cloudflare-browser-run",
			"action":       action,
			"content_type": "text/markdown",
			"content":      decodeCloudflareTextResult(responseBody),
		}, nil
	case "screenshot":
		return map[string]any{
			"engine":         "cloudflare-browser-run",
			"action":         action,
			"content_type":   firstNonEmptyString(strings.TrimSpace(resp.Header.Get("Content-Type")), "image/png"),
			"image_base64":   base64.StdEncoding.EncodeToString(responseBody),
			"bytes":          len(responseBody),
			"base64_encoded": true,
		}, nil
	case "scrape", "json":
		decoded, err := decodeCloudflareJSONResult(responseBody)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"engine": "cloudflare-browser-run",
			"action": action,
			"result": decoded,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported action %q", action)
	}
}

func (h *Handler) browserRunEndpoint(action string) (string, error) {
	baseURL := strings.TrimRight(strings.TrimSpace(h.browserRunConfig.BaseURL), "/")
	if baseURL == "" {
		return "", fmt.Errorf("browser run base url is not configured")
	}
	if strings.TrimSpace(h.browserRunConfig.AccountID) == "" {
		return "", fmt.Errorf("browser run account id is not configured")
	}
	if strings.TrimSpace(h.browserRunConfig.APIToken) == "" {
		return "", fmt.Errorf("browser run api token is not configured")
	}
	return baseURL + "/accounts/" + url.PathEscape(h.browserRunConfig.AccountID) + "/browser-rendering/" + action, nil
}

func decodeCloudflareTextResult(body []byte) string {
	var envelope cloudflareAPIEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Result) > 0 {
		var text string
		if err := json.Unmarshal(envelope.Result, &text); err == nil {
			return text
		}
	}
	return string(body)
}

func decodeCloudflareJSONResult(body []byte) (any, error) {
	var envelope cloudflareAPIEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && len(envelope.Result) > 0 {
		var decoded any
		if err := json.Unmarshal(envelope.Result, &decoded); err != nil {
			return nil, fmt.Errorf("decode web_browser_run response: %w", err)
		}
		return decoded, nil
	}
	var decoded any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode web_browser_run response: %w", err)
	}
	return decoded, nil
}

func summarizeCloudflareErrorBody(body []byte) string {
	var envelope cloudflareAPIEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil {
		parts := make([]string, 0, len(envelope.Errors)+len(envelope.Messages))
		for _, item := range envelope.Errors {
			if msg := strings.TrimSpace(item.Message); msg != "" {
				parts = append(parts, msg)
			}
		}
		for _, item := range envelope.Messages {
			if msg := strings.TrimSpace(item.Message); msg != "" {
				parts = append(parts, msg)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "; ")
		}
	}
	message := strings.TrimSpace(string(body))
	if len(message) > 300 {
		message = message[:300]
	}
	if message == "" {
		message = http.StatusText(http.StatusBadGateway)
	}
	return message
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (h *Handler) runWebFetchTool(ctx context.Context, p webFetchParams) (map[string]any, error) {
	target := strings.TrimSpace(p.URL)
	if target == "" {
		return nil, fmt.Errorf("url is required")
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("invalid url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("url must use http or https")
	}
	if err := blockPrivateURL(ctx, parsed); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "koios/1.0")
	client := h.fetchClient
	if client == nil {
		client = &http.Client{Transport: ssrfSafeTransport()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	contentType := resp.Header.Get("Content-Type")
	text := string(body)
	title := ""
	if strings.Contains(strings.ToLower(contentType), "text/html") {
		if match := titleRE.FindStringSubmatch(text); len(match) >= 2 {
			title = cleanHTMLText(match[1])
		}
		text = cleanHTMLText(text)
	}
	truncated := false
	if len(text) > 20000 {
		text = text[:20000]
		truncated = true
	}
	return map[string]any{
		"url":          target,
		"final_url":    resp.Request.URL.String(),
		"status_code":  resp.StatusCode,
		"content_type": contentType,
		"title":        title,
		"content":      text,
		"truncated":    truncated,
	}, nil
}

func cleanHTMLText(s string) string {
	s = scriptRE.ReplaceAllString(s, " ")
	s = styleRE.ReplaceAllString(s, " ")
	s = tagRE.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\u00a0", " ")
	s = spaceRE.ReplaceAllString(strings.TrimSpace(s), " ")
	return s
}
