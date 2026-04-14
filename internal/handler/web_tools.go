package handler

import (
	"context"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

type webSearchParams struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type webFetchParams struct {
	URL string `json:"url"`
}

var (
	ddgResultBodyRE = regexp.MustCompile(`(?is)<div[^>]*class="result__body"[^>]*>(.*?)</div>`)
	ddgLinkRE       = regexp.MustCompile(`(?is)<a[^>]*class="result__a"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
	ddgSnippetRE    = regexp.MustCompile(`(?is)<(?:a|div)[^>]*class="result__snippet"[^>]*>(.*?)</(?:a|div)>`)
	titleRE         = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	scriptRE        = regexp.MustCompile(`(?is)<script[^>]*>.*?</script>`)
	styleRE         = regexp.MustCompile(`(?is)<style[^>]*>.*?</style>`)
	tagRE           = regexp.MustCompile(`(?s)<[^>]+>`)
	spaceRE         = regexp.MustCompile(`\s+`)
)

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
	searchURL := "https://html.duckduckgo.com/html/?q=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "koios/1.0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	results := parseDuckDuckGoResults(string(body), limit)
	return map[string]any{
		"engine":  "duckduckgo",
		"query":   query,
		"results": results,
		"count":   len(results),
	}, nil
}

func parseDuckDuckGoResults(body string, limit int) []map[string]string {
	blocks := ddgResultBodyRE.FindAllStringSubmatch(body, limit)
	results := make([]map[string]string, 0, len(blocks))
	for _, block := range blocks {
		link := ddgLinkRE.FindStringSubmatch(block[1])
		if len(link) < 3 {
			continue
		}
		snippet := ""
		if match := ddgSnippetRE.FindStringSubmatch(block[1]); len(match) >= 2 {
			snippet = cleanHTMLText(match[1])
		}
		results = append(results, map[string]string{
			"title":   cleanHTMLText(link[2]),
			"url":     decodeDuckDuckGoURL(link[1]),
			"snippet": snippet,
		})
	}
	return results
}

func decodeDuckDuckGoURL(raw string) string {
	u, err := url.Parse(html.UnescapeString(raw))
	if err != nil {
		return html.UnescapeString(raw)
	}
	if dest := u.Query().Get("uddg"); dest != "" {
		decoded, err := url.QueryUnescape(dest)
		if err == nil {
			return decoded
		}
		return dest
	}
	return u.String()
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
func blockPrivateURL(u *url.URL) error {
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("requests to private or reserved IP addresses are not allowed")
		}
		return nil
	}
	addrs, err := net.LookupHost(host)
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
			addrs, err := net.LookupHost(host)
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
	if err := blockPrivateURL(parsed); err != nil {
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
