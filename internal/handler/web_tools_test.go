package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseDuckDuckGoResults(t *testing.T) {
	results := parseDuckDuckGoResults(duckDuckGoResultsFixture, 10)
	if len(results) != 2 {
		t.Fatalf("len(results)=%d", len(results))
	}
	if results[0]["url"] != "https://example.com/one" {
		t.Fatalf("unexpected decoded url: %q", results[0]["url"])
	}
	if results[1]["title"] != "Result Two" {
		t.Fatalf("unexpected title: %q", results[1]["title"])
	}
}

func TestCleanHTMLText(t *testing.T) {
	cleaned := cleanHTMLText(`<html><head><title>x</title><style>.a{}</style></head><body><script>alert(1)</script><p>Hello&nbsp;world</p></body></html>`)
	if cleaned != "x Hello world" {
		t.Fatalf("cleaned=%q", cleaned)
	}
}

func TestRunWebFetchTool(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<html><head><title>Example</title></head><body><p>Hello web</p></body></html>`))
	}))
	defer srv.Close()

	h := &Handler{fetchClient: srv.Client()}
	result, err := h.runWebFetchTool(context.Background(), webFetchParams{URL: srv.URL})
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
