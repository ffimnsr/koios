package handler

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCleanHTMLText(t *testing.T) {
	cleaned := cleanHTMLText(`<html><head><title>x</title><style>.a{}</style></head><body><script>alert(1)</script><p>Hello&nbsp;world</p></body></html>`)
	if cleaned != "x Hello world" {
		t.Fatalf("cleaned=%q", cleaned)
	}
}

func TestRunWebSearchToolBrave(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Subscription-Token"); got != "brave-secret" {
			t.Fatalf("X-Subscription-Token=%q", got)
		}
		if got := r.URL.Query().Get("q"); got != "golang context tutorial" {
			t.Fatalf("q=%q", got)
		}
		if got := r.URL.Query().Get("count"); got != "2" {
			t.Fatalf("count=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
				"web": {
					"results": [
						{"title":"Result One","url":"https://example.com/one","description":"Snippet one"},
						{"title":"Result Two","url":"https://example.com/two","description":"Snippet two"},
						{"title":"Result Three","url":"https://example.com/three","description":"Snippet three"}
					]
				}
			}`))
	}))
	defer srv.Close()

	h := &Handler{webSearchConfig: normalizeWebSearchConfig(WebSearchConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{{
			Name:           "brave",
			APIKey:         "brave-secret",
			BaseURL:        srv.URL,
			DefaultTimeout: 5 * time.Second,
		}},
	})}

	result, err := h.runWebSearchTool(context.Background(), webSearchParams{Query: "golang context tutorial", Limit: 2})
	if err != nil {
		t.Fatalf("runWebSearchTool: %v", err)
	}
	if result["engine"] != "brave" {
		t.Fatalf("engine=%#v", result["engine"])
	}
	if result["count"] != 2 {
		t.Fatalf("count=%#v", result["count"])
	}
	results, ok := result["results"].([]map[string]string)
	if !ok {
		t.Fatalf("results type=%T", result["results"])
	}
	if len(results) != 2 {
		t.Fatalf("len(results)=%d", len(results))
	}
	if results[0]["url"] != "https://example.com/one" || results[1]["title"] != "Result Two" {
		t.Fatalf("unexpected results=%#v", results)
	}
}

func TestRunWebSearchToolExa(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if got := r.Header.Get("x-api-key"); got != "exa-secret" {
			t.Fatalf("x-api-key=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{"title":"Exa Result","url":"https://example.com/exa","summary":"Exa summary"}
			]
		}`))
	}))
	defer srv.Close()

	h := &Handler{webSearchConfig: normalizeWebSearchConfig(WebSearchConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{{
			Name:           "exa",
			APIKey:         "exa-secret",
			BaseURL:        srv.URL,
			DefaultTimeout: 5 * time.Second,
		}},
	})}

	result, err := h.runWebSearchTool(context.Background(), webSearchParams{Query: "golang context tutorial", Limit: 1})
	if err != nil {
		t.Fatalf("runWebSearchTool: %v", err)
	}
	if result["engine"] != "exa" {
		t.Fatalf("engine=%#v", result["engine"])
	}
	results := result["results"].([]map[string]string)
	if len(results) != 1 || results[0]["snippet"] != "Exa summary" {
		t.Fatalf("unexpected results=%#v", results)
	}
}

func TestRunWebSearchToolTavily(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tavily-secret" {
			t.Fatalf("authorization=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"results": [
				{"title":"Tavily Result","url":"https://example.com/tavily","content":"Tavily content"}
			]
		}`))
	}))
	defer srv.Close()

	h := &Handler{webSearchConfig: normalizeWebSearchConfig(WebSearchConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{{
			Name:           "tavily",
			APIKey:         "tavily-secret",
			BaseURL:        srv.URL,
			DefaultTimeout: 5 * time.Second,
		}},
	})}

	result, err := h.runWebSearchTool(context.Background(), webSearchParams{Query: "golang context tutorial", Limit: 1})
	if err != nil {
		t.Fatalf("runWebSearchTool: %v", err)
	}
	if result["engine"] != "tavily" {
		t.Fatalf("engine=%#v", result["engine"])
	}
	results := result["results"].([]map[string]string)
	if len(results) != 1 || results[0]["snippet"] != "Tavily content" {
		t.Fatalf("unexpected results=%#v", results)
	}
}

func TestRunWebSearchToolFallsBackToNextProvider(t *testing.T) {
	brave := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer brave.Close()
	exa := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Exa Result","url":"https://example.com/exa","summary":"fallback worked"}]}`))
	}))
	defer exa.Close()

	h := &Handler{webSearchConfig: normalizeWebSearchConfig(WebSearchConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{
			{Name: "brave", APIKey: "brave-secret", BaseURL: brave.URL, DefaultTimeout: 5 * time.Second},
			{Name: "exa", APIKey: "exa-secret", BaseURL: exa.URL, DefaultTimeout: 5 * time.Second},
		},
	})}

	result, err := h.runWebSearchTool(context.Background(), webSearchParams{Query: "golang"})
	if err != nil {
		t.Fatalf("runWebSearchTool: %v", err)
	}
	if result["engine"] != "exa" {
		t.Fatalf("expected exa fallback, got %#v", result["engine"])
	}
}

func TestRunWebSearchToolFallsBackWhenProviderKeyMissing(t *testing.T) {
	tavily := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"Tavily Result","url":"https://example.com/tavily","content":"fallback content"}]}`))
	}))
	defer tavily.Close()

	h := &Handler{webSearchConfig: normalizeWebSearchConfig(WebSearchConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{
			{Name: "brave"},
			{Name: "tavily", APIKey: "tavily-secret", BaseURL: tavily.URL, DefaultTimeout: 5 * time.Second},
		},
	})}

	result, err := h.runWebSearchTool(context.Background(), webSearchParams{Query: "golang"})
	if err != nil {
		t.Fatalf("runWebSearchTool: %v", err)
	}
	if result["engine"] != "tavily" {
		t.Fatalf("expected tavily fallback, got %#v", result["engine"])
	}
}

func TestRunWebSearchToolAllProvidersFail(t *testing.T) {
	h := &Handler{webSearchConfig: normalizeWebSearchConfig(WebSearchConfig{
		Enabled: true,
		Providers: []WebSearchProviderConfig{
			{Name: "brave"},
			{Name: "exa"},
		},
	})}
	_, err := h.runWebSearchTool(context.Background(), webSearchParams{Query: "golang"})
	if err == nil || !strings.Contains(err.Error(), "failed for all providers") || !strings.Contains(err.Error(), "brave") || !strings.Contains(err.Error(), "exa") {
		t.Fatalf("expected combined provider failure, got %v", err)
	}
}

func TestRunWebSearchToolDisabled(t *testing.T) {
	h := &Handler{webSearchConfig: normalizeWebSearchConfig(WebSearchConfig{})}
	_, err := h.runWebSearchTool(context.Background(), webSearchParams{Query: "golang"})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
}

func TestRunWebBrowserRunToolMarkdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method=%s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer browser-secret" {
			t.Fatalf("authorization=%q", got)
		}
		if r.URL.Path != "/accounts/account-123/browser-rendering/markdown" {
			t.Fatalf("path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":"# Example"}`))
	}))
	defer srv.Close()

	h := &Handler{browserRunConfig: normalizeBrowserRunConfig(BrowserRunConfig{
		Enabled:        true,
		AccountID:      "account-123",
		APIToken:       "browser-secret",
		BaseURL:        srv.URL,
		DefaultTimeout: 5 * time.Second,
	})}

	result, err := h.runWebBrowserRunTool(context.Background(), webBrowserRunParams{Action: "markdown", URL: "https://example.com"})
	if err != nil {
		t.Fatalf("runWebBrowserRunTool: %v", err)
	}
	if result["action"] != "markdown" || result["content"] != "# Example" {
		t.Fatalf("unexpected result=%#v", result)
	}
}

func TestRunWebBrowserRunToolScreenshot(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-bytes"))
	}))
	defer srv.Close()

	h := &Handler{browserRunConfig: normalizeBrowserRunConfig(BrowserRunConfig{
		Enabled:        true,
		AccountID:      "account-123",
		APIToken:       "browser-secret",
		BaseURL:        srv.URL,
		DefaultTimeout: 5 * time.Second,
	})}

	result, err := h.runWebBrowserRunTool(context.Background(), webBrowserRunParams{Action: "screenshot", URL: "https://example.com"})
	if err != nil {
		t.Fatalf("runWebBrowserRunTool: %v", err)
	}
	if result["content_type"] != "image/png" || result["base64_encoded"] != true {
		t.Fatalf("unexpected result=%#v", result)
	}
	if result["image_base64"] != "cG5nLWJ5dGVz" {
		t.Fatalf("unexpected image base64=%#v", result["image_base64"])
	}
}

func TestRunWebBrowserRunToolScrape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":[{"selector":"h1","results":[{"text":"Example"}]}]}`))
	}))
	defer srv.Close()

	h := &Handler{browserRunConfig: normalizeBrowserRunConfig(BrowserRunConfig{
		Enabled:        true,
		AccountID:      "account-123",
		APIToken:       "browser-secret",
		BaseURL:        srv.URL,
		DefaultTimeout: 5 * time.Second,
	})}

	result, err := h.runWebBrowserRunTool(context.Background(), webBrowserRunParams{Action: "scrape", URL: "https://example.com", Elements: []scrapeTarget{{Selector: "h1"}}})
	if err != nil {
		t.Fatalf("runWebBrowserRunTool: %v", err)
	}
	if result["action"] != "scrape" {
		t.Fatalf("unexpected result=%#v", result)
	}
	decoded, ok := result["result"].([]any)
	if !ok || len(decoded) != 1 {
		t.Fatalf("unexpected decoded result=%#v", result["result"])
	}
}

func TestRunWebBrowserRunToolDisabled(t *testing.T) {
	h := &Handler{browserRunConfig: normalizeBrowserRunConfig(BrowserRunConfig{})}
	_, err := h.runWebBrowserRunTool(context.Background(), webBrowserRunParams{Action: "content", URL: "https://example.com"})
	if err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("expected disabled error, got %v", err)
	}
}

func TestRunWebFetchTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Example</title></head><body><p>Hello web</p></body></html>`))
	}))
	defer srv.Close()

	targetURL, err := url.Parse("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		fetchClient: &http.Client{
			Transport: &http.Transport{
				Proxy: nil,
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, srv.Listener.Addr().String())
				},
			},
		},
	}
	result, err := h.runWebFetchTool(context.Background(), webFetchParams{URL: targetURL.String()})
	if err != nil {
		t.Fatalf("runWebFetchTool: %v", err)
	}
	if result["title"] != "Example" {
		t.Fatalf("title=%#v", result["title"])
	}
	if result["content"] != "Example Hello web" {
		t.Fatalf("content=%#v", result["content"])
	}
}

func TestRunWebFetchTool_BlocksPrivateAddress(t *testing.T) {
	h := &Handler{}
	_, err := h.runWebFetchTool(context.Background(), webFetchParams{URL: "http://127.0.0.1:8080"})
	if err == nil {
		t.Fatal("expected error for private address")
	}
	if !strings.Contains(err.Error(), "private or reserved IP addresses") && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected error: %v", err)
	}
}
