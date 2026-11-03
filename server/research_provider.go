package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
	xproxy "golang.org/x/net/proxy"
)

const researchMaxResponseBytes = 1 << 20

type researchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

type researchProviderResponse struct {
	Status    int
	RequestID string
	RawBody   []byte
	Results   []researchResult
}

type researchProviderError struct {
	Code      string
	Retryable bool
	Status    int
}

func (e *researchProviderError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("research provider %s (HTTP %d)", e.Code, e.Status)
	}
	return "research provider " + e.Code
}

type researchSearchProvider interface {
	Search(context.Context, string, int) (researchProviderResponse, error)
}

type httpResearchProvider struct {
	name     string
	key      string
	endpoint string
	client   *http.Client
}

func newHTTPResearchProvider(name, key, proxyURL, endpoint string) (researchSearchProvider, error) {
	if name != "brave-free" && name != "tavily" && name != "ddgs" {
		return nil, errors.New("unsupported research provider")
	}
	if name != "ddgs" && strings.TrimSpace(key) == "" {
		return nil, errors.New("research provider key is not configured")
	}
	if endpoint == "" {
		switch name {
		case "brave-free":
			endpoint = "https://api.search.brave.com/res/v1/web/search"
		case "tavily":
			endpoint = "https://api.tavily.com/search"
		case "ddgs":
			endpoint = "https://html.duckduckgo.com/html/"
		}
	}
	client, err := researchHTTPClient(proxyURL)
	if err != nil {
		return nil, err
	}
	return &httpResearchProvider{name: name, key: key, endpoint: endpoint, client: client}, nil
}

func researchHTTPClient(proxyRaw string) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	if strings.TrimSpace(proxyRaw) != "" {
		proxyURL, err := url.Parse(strings.TrimSpace(proxyRaw))
		if err != nil || proxyURL.Hostname() == "" {
			return nil, errors.New("invalid research proxy URL")
		}
		switch proxyURL.Scheme {
		case "http", "https":
			transport.Proxy = http.ProxyURL(proxyURL)
		case "socks5", "socks5h":
			var auth *xproxy.Auth
			if proxyURL.User != nil {
				password, _ := proxyURL.User.Password()
				auth = &xproxy.Auth{User: proxyURL.User.Username(), Password: password}
			}
			dialer, err := xproxy.SOCKS5("tcp", proxyURL.Host, auth, &net.Dialer{Timeout: 10 * time.Second})
			if err != nil {
				return nil, errors.New("invalid research SOCKS proxy")
			}
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				if contextDialer, ok := dialer.(xproxy.ContextDialer); ok {
					return contextDialer.DialContext(ctx, network, address)
				}
				return dialer.Dial(network, address)
			}
		default:
			return nil, errors.New("research proxy scheme must be http, https, socks5 or socks5h")
		}
	}
	return &http.Client{
		Transport: transport,
		Timeout:   25 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			// Provider APIs are fixed endpoints and are not expected to redirect.
			// Refusing every redirect also closes private-IP and scheme-changing SSRF.
			return http.ErrUseLastResponse
		},
	}, nil
}

func readResearchBody(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, researchMaxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > researchMaxResponseBytes {
		// The persisted digest must only ever describe a complete provider body.
		// Discard the bounded prefix instead of presenting its digest as the raw
		// response digest.
		return nil, &researchProviderError{Code: "response_too_large"}
	}
	return raw, nil
}

func researchStatusError(status int) error {
	switch {
	case status == http.StatusAccepted:
		return &researchProviderError{Code: "provider_pending", Retryable: true, Status: status}
	case status == http.StatusTooManyRequests:
		return &researchProviderError{Code: "rate_limited", Retryable: true, Status: status}
	case status >= 500:
		return &researchProviderError{Code: "provider_unavailable", Retryable: true, Status: status}
	case status >= 300 && status < 400:
		return &researchProviderError{Code: "redirect_blocked", Status: status}
	case status < 200 || status >= 300:
		return &researchProviderError{Code: "http_error", Status: status}
	default:
		return nil
	}
}

func validResearchContentType(provider, raw string) bool {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	switch provider {
	case "brave-free", "tavily":
		return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
	case "ddgs":
		return mediaType == "text/html" || mediaType == "application/xhtml+xml"
	default:
		return false
	}
}

func (p *httpResearchProvider) Search(ctx context.Context, query string, limit int) (researchProviderResponse, error) {
	var request *http.Request
	var err error
	switch p.name {
	case "brave-free":
		endpoint, parseErr := url.Parse(p.endpoint)
		if parseErr != nil {
			return researchProviderResponse{}, &researchProviderError{Code: "invalid_endpoint"}
		}
		values := endpoint.Query()
		values.Set("q", query)
		values.Set("count", fmt.Sprint(limit))
		endpoint.RawQuery = values.Encode()
		request, err = http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
		if err == nil {
			request.Header.Set("Accept", "application/json")
			request.Header.Set("X-Subscription-Token", p.key)
		}
	case "tavily":
		payload, _ := json.Marshal(map[string]any{
			"api_key": p.key, "query": query, "max_results": limit,
			"search_depth": "basic", "include_answer": false,
			"include_raw_content": false, "include_images": false,
		})
		request, err = http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(payload))
		if err == nil {
			request.Header.Set("Accept", "application/json")
			request.Header.Set("Content-Type", "application/json")
		}
	case "ddgs":
		form := url.Values{"q": {query}}
		request, err = http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, strings.NewReader(form.Encode()))
		if err == nil {
			request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			request.Header.Set("Accept", "text/html")
		}
	}
	if err != nil {
		return researchProviderResponse{}, &researchProviderError{Code: "request_build"}
	}
	request.Header.Set("User-Agent", "Uroboros-Research/1.0")
	response, err := p.client.Do(request)
	if err != nil {
		return researchProviderResponse{}, &researchProviderError{Code: "network_error", Retryable: true}
	}
	defer response.Body.Close()
	raw, readErr := readResearchBody(response.Body)
	out := researchProviderResponse{
		Status: response.StatusCode, RawBody: raw,
		RequestID: boundedResearchText(response.Header.Get("X-Request-Id"), 240),
	}
	if readErr != nil {
		return out, readErr
	}
	if statusErr := researchStatusError(response.StatusCode); statusErr != nil {
		return out, statusErr
	}
	if !validResearchContentType(p.name, response.Header.Get("Content-Type")) {
		return out, &researchProviderError{Code: "invalid_content_type"}
	}
	switch p.name {
	case "brave-free":
		var document struct {
			Web struct {
				Results []struct {
					Title       string `json:"title"`
					URL         string `json:"url"`
					Description string `json:"description"`
				} `json:"results"`
			} `json:"web"`
		}
		if json.Unmarshal(raw, &document) != nil {
			return out, &researchProviderError{Code: "invalid_response"}
		}
		for _, result := range document.Web.Results {
			out.Results = append(out.Results, researchResult{Title: result.Title, URL: result.URL, Snippet: result.Description})
		}
	case "tavily":
		var document struct {
			RequestID string `json:"request_id"`
			Results   []struct {
				Title   string `json:"title"`
				URL     string `json:"url"`
				Content string `json:"content"`
			} `json:"results"`
		}
		if json.Unmarshal(raw, &document) != nil {
			return out, &researchProviderError{Code: "invalid_response"}
		}
		if out.RequestID == "" {
			out.RequestID = boundedResearchText(document.RequestID, 240)
		}
		for _, result := range document.Results {
			out.Results = append(out.Results, researchResult{Title: result.Title, URL: result.URL, Snippet: result.Content})
		}
	case "ddgs":
		results, parseErr := parseDDGSResults(raw)
		if parseErr != nil {
			return out, &researchProviderError{Code: "invalid_response"}
		}
		out.Results = results
	}
	if len(out.Results) > limit {
		out.Results = out.Results[:limit]
	}
	return out, nil
}

func htmlClass(node *xhtml.Node, class string) bool {
	for _, attr := range node.Attr {
		if attr.Key == "class" {
			for _, item := range strings.Fields(attr.Val) {
				if item == class {
					return true
				}
			}
		}
	}
	return false
}

func htmlAttr(node *xhtml.Node, key string) string {
	for _, attr := range node.Attr {
		if attr.Key == key {
			return attr.Val
		}
	}
	return ""
}

func htmlText(node *xhtml.Node) string {
	var builder strings.Builder
	var walk func(*xhtml.Node)
	walk = func(current *xhtml.Node) {
		if current.Type == xhtml.TextNode {
			builder.WriteString(current.Data)
			builder.WriteByte(' ')
		}
		for child := current.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(node)
	return strings.Join(strings.Fields(stdhtml.UnescapeString(builder.String())), " ")
}

func ddgsResultURL(raw string) string {
	parsed, err := url.Parse(stdhtml.UnescapeString(raw))
	if err != nil {
		return raw
	}
	if value := parsed.Query().Get("uddg"); value != "" {
		return value
	}
	return parsed.String()
}

func parseDDGSResults(raw []byte) ([]researchResult, error) {
	document, err := xhtml.Parse(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	var results []researchResult
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode && node.Data == "div" && htmlClass(node, "result") {
			var result researchResult
			var inspect func(*xhtml.Node)
			inspect = func(part *xhtml.Node) {
				if part.Type == xhtml.ElementNode && part.Data == "a" && htmlClass(part, "result__a") {
					result.Title = htmlText(part)
					result.URL = ddgsResultURL(htmlAttr(part, "href"))
				}
				if part.Type == xhtml.ElementNode && htmlClass(part, "result__snippet") {
					result.Snippet = htmlText(part)
				}
				for child := part.FirstChild; child != nil; child = child.NextSibling {
					inspect(child)
				}
			}
			inspect(node)
			if result.URL != "" {
				results = append(results, result)
			}
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(document)
	return results, nil
}
