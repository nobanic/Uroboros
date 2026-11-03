package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/binaryintake"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/traffic"
	"github.com/nobanic/norma/permission"
	actool "github.com/nobanic/norma/tool"
)

const (
	defaultHTTPRequestTimeout  = 30 * time.Second
	maxHTTPRequestTimeout      = 120 * time.Second
	maxHTTPRequestBody         = 2 << 20
	maxHTTPResponseRead        = 8 << 20
	defaultHTTPResponsePreview = 64 << 10
	maxHTTPResponsePreview     = 256 << 10
	maxHTTPRedirects           = 5
)

var httpMethodToken = regexp.MustCompile(`^[A-Z][A-Z0-9_-]{0,31}$`)

type httpRequestArgs struct {
	TaskID               string            `json:"task_id"`
	Method               string            `json:"method"`
	URL                  string            `json:"url"`
	Query                map[string]any    `json:"query"`
	Headers              map[string]string `json:"headers"`
	Body                 string            `json:"body"`
	BodyBase64           string            `json:"body_base64"`
	JSONBody             json.RawMessage   `json:"json_body"`
	Form                 map[string]string `json:"form"`
	TimeoutMS            int               `json:"timeout_ms"`
	FollowRedirects      bool              `json:"follow_redirects"`
	MaxRedirects         int               `json:"max_redirects"`
	ResponsePreviewBytes int               `json:"response_preview_bytes"`
	RepeatOverrideReason string            `json:"repeat_override_reason"`
	// Ephemeral is accepted only by the authenticated operator preflight route.
	// The model-facing tool forcibly clears it so agents cannot hide evidence.
	Ephemeral bool `json:"ephemeral,omitempty"`
}

type httpRepeatEndpoint struct {
	Updated          time.Time
	BlockedSignature string
	Counts           map[string]int
}

type httpRepeatInfo struct {
	Count     int
	Stop      bool
	Signature string
}

type authorizedDestination struct {
	Host        string
	ResolvedIPs []string
	Scope       string
}

// httpRequestTool is the shell-independent request/repeater primitive. It is a
// host tool so it works in the distroless runtime, but the DB binding still has
// the final say over which agents can see it.
func (s *Server) httpRequestTool() actool.CoreTool {
	schema := objSchema(map[string]any{
		"method": map[string]any{"type": "string", "description": "HTTP method (GET, HEAD, POST, PUT, PATCH, DELETE, OPTIONS)", "default": "GET"},
		"url":    map[string]any{"type": "string", "description": "absolute http/https URL; its destination must be inside the current task scope"},
		"query": map[string]any{
			"type": "object", "description": "optional query parameters; scalar values and arrays are accepted",
			"additionalProperties": true,
		},
		"headers": map[string]any{
			"type": "object", "description": "optional request headers (string values)",
			"additionalProperties": map[string]any{"type": "string"},
		},
		"body":        map[string]any{"type": "string", "description": "optional text request body"},
		"body_base64": map[string]any{"type": "string", "description": "optional base64-encoded binary request body"},
		"json_body":   map[string]any{"description": "optional JSON request body; mutually exclusive with body/body_base64/form"},
		"form": map[string]any{
			"type": "object", "description": "optional application/x-www-form-urlencoded body",
			"additionalProperties": map[string]any{"type": "string"},
		},
		"timeout_ms":             map[string]any{"type": "integer", "description": "request timeout in milliseconds (100..120000)", "default": 30000},
		"follow_redirects":       map[string]any{"type": "boolean", "description": "follow redirects only after re-validating every destination against task scope", "default": false},
		"max_redirects":          map[string]any{"type": "integer", "description": "maximum followed redirects (1..5)", "default": 3},
		"response_preview_bytes": map[string]any{"type": "integer", "description": "maximum response bytes returned inline (1024..262144, default 65536); the complete response remains captured for traffic_match/traffic_blob"},
		"repeat_override_reason": map[string]any{"type": "string", "description": "explicit reason a genuinely different evidence-derived mechanism must cross a stopped method+URL+request-body-class direction; retrying with more budget, different wording, or guessed field renames is not sufficient"},
	}, "url")
	return actool.Build(actool.Spec{
		Name:        "http_request",
		Description: "Send one bounded HTTP request to a destination in this task's declared scope through the recording proxy. Returns exact traffic_id evidence, response headers/body/hash, final URL, resolved IPs, timing, and explicit truncation. Inline response preview defaults to 64 KiB (max 256 KiB); the complete captured body stays available through traffic_match/traffic_blob, which should be used for large HTML/JavaScript instead of pulling bundles into model context. Redirects are off unless requested and every redirect is scope-checked. Repeated equivalent transport/HTTP errors and recognized HTTP-200 application errors are semantically fingerprinted after volatile IDs/timestamps are normalized. Non-idempotent failed directions can set a task-wide repeat_stop; safe GET/HEAD/OPTIONS requests only receive repeat_warning. A different encoding/content type remains testable, while crossing a stopped shape requires an evidence-derived mechanism in repeat_override_reason. Use this instead of Bash/curl in a shell-less runtime.",
		Schema:      schema,
		ReadOnly: func(in json.RawMessage) bool {
			var a httpRequestArgs
			_ = json.Unmarshal(in, &a)
			m := strings.ToUpper(strings.TrimSpace(a.Method))
			return m == "" || m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
		},
		Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, in json.RawMessage, tc *actool.ToolContext) (actool.Result, error) {
			var a httpRequestArgs
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf("invalid http_request input: " + err.Error()), nil
			}
			a.Ephemeral = false
			out, err := s.executeHTTPRequest(ctx, a, true)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			encoded, err := json.Marshal(out)
			if err != nil {
				return actool.Errorf("encode http_request result: " + err.Error()), nil
			}
			return actool.Text(actool.Capture(tc, string(encoded))), nil
		},
	})
}

// httpRequestPreflight exercises the exact native client, recording proxy and
// network namespace used by the model-facing tool before a benchmark spends a
// model budget. It is operator-authenticated. Normal preflights are deliberately
// read-only; benchmark fixture validation may opt into an ephemeral POST that is
// stripped from the evidence corpus. When task_id is supplied it also executes
// the exact task-scope authorization path; the field is intentionally absent
// from the model-facing tool schema.
func (s *Server) httpRequestPreflight(w http.ResponseWriter, r *http.Request) {
	var a httpRequestArgs
	if err := decode(r, &a); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	method := strings.ToUpper(strings.TrimSpace(a.Method))
	if method == "" {
		method = http.MethodGet
	}
	if a.Ephemeral {
		if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions && method != http.MethodPost {
			writeErr(w, 400, "ephemeral fixture preflight permits only GET, HEAD, OPTIONS, or POST")
			return
		}
		if strings.TrimSpace(a.TaskID) != "" {
			writeErr(w, 400, "ephemeral fixture preflight cannot be task-scoped")
			return
		}
	} else {
		if method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions {
			writeErr(w, 400, "preflight permits only GET, HEAD, or OPTIONS")
			return
		}
		if a.Body != "" || a.BodyBase64 != "" || len(a.JSONBody) > 0 || len(a.Form) > 0 {
			writeErr(w, 400, "preflight does not accept a request body")
			return
		}
	}
	a.Method = method
	ctx := r.Context()
	requireTaskScope := false
	if rawTaskID := strings.TrimSpace(a.TaskID); rawTaskID != "" {
		taskID, err := strconv.ParseInt(rawTaskID, 10, 64)
		if err != nil || taskID <= 0 {
			writeErr(w, 400, "task_id must be a positive integer")
			return
		}
		canonicalID := strconv.FormatInt(taskID, 10)
		if s.m == nil {
			writeErr(w, 503, "task registry is unavailable")
			return
		}
		if _, ok := s.m.Task(canonicalID); !ok {
			writeErr(w, 404, "task not found")
			return
		}
		ctx = agent.WithRunInfo(ctx, agent.RunInfo{TaskID: taskID})
		requireTaskScope = true
	}
	out, err := s.executeHTTPRequest(ctx, a, requireTaskScope)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	writeJSON(w, 200, out)
}

func normalizeHTTPRequestArgs(a *httpRequestArgs) (time.Duration, error) {
	a.Method = strings.ToUpper(strings.TrimSpace(a.Method))
	a.RepeatOverrideReason = strings.TrimSpace(a.RepeatOverrideReason)
	if len(a.RepeatOverrideReason) > 400 {
		return 0, fmt.Errorf("repeat_override_reason must be at most 400 characters")
	}
	if a.Method == "" {
		a.Method = http.MethodGet
	}
	if !httpMethodToken.MatchString(a.Method) || a.Method == http.MethodConnect || a.Method == http.MethodTrace {
		return 0, fmt.Errorf("unsupported HTTP method %q", a.Method)
	}
	if strings.TrimSpace(a.URL) == "" {
		return 0, fmt.Errorf("url is required")
	}
	if a.TimeoutMS == 0 {
		a.TimeoutMS = int(defaultHTTPRequestTimeout / time.Millisecond)
	}
	if a.TimeoutMS < 100 || time.Duration(a.TimeoutMS)*time.Millisecond > maxHTTPRequestTimeout {
		return 0, fmt.Errorf("timeout_ms must be between 100 and %d", maxHTTPRequestTimeout/time.Millisecond)
	}
	if a.MaxRedirects == 0 {
		a.MaxRedirects = 3
	}
	if a.MaxRedirects < 1 || a.MaxRedirects > maxHTTPRedirects {
		return 0, fmt.Errorf("max_redirects must be between 1 and %d", maxHTTPRedirects)
	}
	if a.ResponsePreviewBytes == 0 {
		a.ResponsePreviewBytes = defaultHTTPResponsePreview
	}
	if a.ResponsePreviewBytes < 1024 || a.ResponsePreviewBytes > maxHTTPResponsePreview {
		return 0, fmt.Errorf("response_preview_bytes must be between 1024 and %d", maxHTTPResponsePreview)
	}
	return time.Duration(a.TimeoutMS) * time.Millisecond, nil
}

func buildHTTPRequestBody(a httpRequestArgs) ([]byte, string, error) {
	modes := 0
	if a.Body != "" {
		modes++
	}
	if strings.TrimSpace(a.BodyBase64) != "" {
		modes++
	}
	if len(a.JSONBody) > 0 && string(a.JSONBody) != "null" {
		modes++
	}
	if len(a.Form) > 0 {
		modes++
	}
	if modes > 1 {
		return nil, "", fmt.Errorf("body, body_base64, json_body, and form are mutually exclusive")
	}
	var body []byte
	var contentType string
	switch {
	case a.Body != "":
		body = []byte(a.Body)
	case strings.TrimSpace(a.BodyBase64) != "":
		decoded, err := base64.StdEncoding.DecodeString(a.BodyBase64)
		if err != nil {
			return nil, "", fmt.Errorf("body_base64 is invalid: %w", err)
		}
		body = decoded
	case len(a.JSONBody) > 0 && string(a.JSONBody) != "null":
		if !json.Valid(a.JSONBody) {
			return nil, "", fmt.Errorf("json_body is invalid JSON")
		}
		body = append([]byte(nil), a.JSONBody...)
		contentType = "application/json"
	case len(a.Form) > 0:
		values := make(url.Values, len(a.Form))
		for k, v := range a.Form {
			values.Set(k, v)
		}
		body = []byte(values.Encode())
		contentType = "application/x-www-form-urlencoded"
	}
	if len(body) > maxHTTPRequestBody {
		return nil, "", fmt.Errorf("request body exceeds %d bytes", maxHTTPRequestBody)
	}
	return body, contentType, nil
}

func addHTTPQuery(u *url.URL, query map[string]any) error {
	if len(query) == 0 {
		return nil
	}
	values := u.Query()
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("query parameter name cannot be empty")
		}
		switch value := query[key].(type) {
		case []any:
			for _, item := range value {
				values.Add(key, fmt.Sprint(item))
			}
		case []string:
			for _, item := range value {
				values.Add(key, item)
			}
		case nil:
			values.Add(key, "")
		default:
			values.Add(key, fmt.Sprint(value))
		}
	}
	u.RawQuery = values.Encode()
	return nil
}

func requestHeaderValue(headers map[string]string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// httpRequestShape deliberately captures the request contract class, not the
// body value. Two random JSON objects should converge on one stopped branch, but
// those failures must not block a later text/plain or raw-text contract recovered
// from client code.
func httpRequestShape(a httpRequestArgs, inferredContentType string) string {
	mode := "none"
	switch {
	case a.Body != "":
		mode = "text"
	case strings.TrimSpace(a.BodyBase64) != "":
		mode = "binary"
	case len(a.JSONBody) > 0 && string(a.JSONBody) != "null":
		mode = "json"
	case len(a.Form) > 0:
		mode = "form"
	}
	contentType := requestHeaderValue(a.Headers, "Content-Type")
	if contentType == "" {
		contentType = inferredContentType
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = mediaType
	}
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	if contentType == "" {
		contentType = "unspecified"
	}
	return mode + ";" + contentType
}

func httpRepeatKey(ctx context.Context, method string, u *url.URL, requestShape string) (string, bool) {
	ri := agent.RunInfoFrom(ctx)
	if ri.TaskID <= 0 || ri.IntentID <= 0 {
		return "", false // operator/preflight calls are not autonomous worker loops
	}
	canonical := *u
	canonical.Scheme = strings.ToLower(canonical.Scheme)
	canonical.Host = strings.ToLower(canonical.Host)
	canonical.Fragment = ""
	return fmt.Sprintf("%d\x00%s\x00%s\x00%s", ri.TaskID, method, canonical.String(), requestShape), true
}

func (s *Server) checkHTTPRepeat(ctx context.Context, method string, u *url.URL, requestShape, overrideReason string) error {
	key, active := httpRepeatKey(ctx, method, u, requestShape)
	if !active {
		return nil
	}
	s.httpRepeatMu.Lock()
	defer s.httpRepeatMu.Unlock()
	entry := s.httpRepeats[key]
	if entry == nil || entry.BlockedSignature == "" || overrideReason != "" {
		return nil
	}
	return fmt.Errorf("repeat_stop=true: this task already received the same failure twice for %s %s with request_shape=%s (signature %s); pivot to a different mechanism or provide repeat_override_reason explaining the genuinely new mechanism", method, u.String(), requestShape, entry.BlockedSignature)
}

func (s *Server) recordHTTPRepeat(ctx context.Context, method string, u *url.URL, requestShape, signature string, success bool) httpRepeatInfo {
	return s.recordHTTPRepeatPolicy(ctx, method, u, requestShape, signature, success, 2, true)
}

func (s *Server) recordHTTPRepeatPolicy(ctx context.Context, method string, u *url.URL, requestShape, signature string, success bool, threshold int, hardStop bool) httpRepeatInfo {
	key, active := httpRepeatKey(ctx, method, u, requestShape)
	if !active {
		return httpRepeatInfo{}
	}
	s.httpRepeatMu.Lock()
	defer s.httpRepeatMu.Unlock()
	if s.httpRepeats == nil {
		s.httpRepeats = make(map[string]*httpRepeatEndpoint)
	}
	if success {
		delete(s.httpRepeats, key)
		return httpRepeatInfo{}
	}
	now := time.Now()
	if len(s.httpRepeats) > 2048 {
		for existingKey, entry := range s.httpRepeats {
			if now.Sub(entry.Updated) > 2*time.Hour {
				delete(s.httpRepeats, existingKey)
			}
		}
	}
	entry := s.httpRepeats[key]
	if entry == nil {
		entry = &httpRepeatEndpoint{Counts: make(map[string]int)}
		s.httpRepeats[key] = entry
	}
	entry.Updated = now
	entry.Counts[signature]++
	count := entry.Counts[signature]
	if threshold < 2 {
		threshold = 2
	}
	if hardStop && count >= threshold {
		entry.BlockedSignature = signature
	}
	return httpRepeatInfo{Count: count, Stop: hardStop && count >= threshold, Signature: signature}
}

func transportFailureSignature(err error) string {
	if err == nil {
		return ""
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return "transport:timeout"
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(err.Error()))))
	return "transport:" + hex.EncodeToString(sum[:8])
}

func (s *Server) executeHTTPRequest(ctx context.Context, a httpRequestArgs, requireTaskScope bool) (map[string]any, error) {
	timeout, err := normalizeHTTPRequestArgs(&a)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(a.URL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if err := addHTTPQuery(u, a.Query); err != nil {
		return nil, err
	}
	body, inferredContentType, err := buildHTTPRequestBody(a)
	if err != nil {
		return nil, err
	}
	requestShape := httpRequestShape(a, inferredContentType)
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	firstDestination, err := s.authorizeHTTPDestination(runCtx, u, requireTaskScope)
	if err != nil {
		return nil, err
	}
	if err := s.checkHTTPRepeat(ctx, a.Method, u, requestShape, a.RepeatOverrideReason); err != nil {
		return nil, err
	}
	if s.m == nil || s.m.Traffic() == nil || !s.m.TrafficEnabled() {
		return nil, fmt.Errorf("http_request requires traffic capture to be enabled; request was not sent")
	}
	proxyURL, err := traffic.ValidateProxyURL(s.m.ProxyAddr())
	if err != nil {
		return nil, fmt.Errorf("recording proxy is unavailable: %w", err)
	}
	caPEM, err := os.ReadFile(s.m.ProxyCACert())
	if err != nil {
		return nil, fmt.Errorf("read recording proxy CA: %w", err)
	}
	rootCAs := x509.NewCertPool()
	if !rootCAs.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("recording proxy CA contains no certificates")
	}
	coordinationHeader := traffic.CaptureRequestHeader
	if a.Ephemeral {
		coordinationHeader = traffic.NoRecordRequestHeader
	}
	baseTransport := &http.Transport{
		Proxy: http.ProxyURL(proxyURL),
		// HTTPS reaches an HTTP proxy as CONNECT before the inner request headers
		// exist. Carry the evidence marker on CONNECT too, otherwise a host that a
		// prior best-effort request placed in transparent passthrough could bypass
		// MITM capture before the marker on the actual request is visible.
		ProxyConnectHeader: http.Header{coordinationHeader: []string{"1"}},
		TLSClientConfig:    &tls.Config{RootCAs: rootCAs, MinVersion: tls.VersionTLS12},
		DisableCompression: true,
	}
	defer baseTransport.CloseIdleConnections()
	capture := &captureIDTransport{base: baseTransport}

	resolvedSet := map[string]struct{}{}
	for _, ip := range firstDestination.ResolvedIPs {
		resolvedSet[ip] = struct{}{}
	}
	scopes := []string{firstDestination.Scope}
	client := &http.Client{Transport: capture, Timeout: timeout}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if !a.FollowRedirects {
			return http.ErrUseLastResponse
		}
		if len(via) > a.MaxRedirects {
			return fmt.Errorf("redirect limit exceeded (%d)", a.MaxRedirects)
		}
		destination, err := s.authorizeHTTPDestination(runCtx, req.URL, requireTaskScope)
		if err != nil {
			return fmt.Errorf("redirect destination rejected: %w", err)
		}
		for _, ip := range destination.ResolvedIPs {
			resolvedSet[ip] = struct{}{}
		}
		scopes = append(scopes, destination.Scope)
		req.Header.Set(coordinationHeader, "1")
		return nil
	}

	req, err := http.NewRequestWithContext(runCtx, a.Method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if len(a.Headers) > 64 {
		return nil, fmt.Errorf("at most 64 request headers are allowed")
	}
	for key, value := range a.Headers {
		key = http.CanonicalHeaderKey(strings.TrimSpace(key))
		if key == "" || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return nil, fmt.Errorf("invalid request header")
		}
		if strings.EqualFold(key, traffic.CaptureRequestHeader) || strings.EqualFold(key, traffic.NoRecordRequestHeader) || strings.EqualFold(key, traffic.TrafficIDHeader) || strings.EqualFold(key, "Proxy-Authorization") {
			return nil, fmt.Errorf("request header %q is reserved", key)
		}
		if strings.EqualFold(key, "Host") {
			return nil, fmt.Errorf("Host override is not supported; put the scoped destination in url")
		}
		req.Header.Set(key, value)
	}
	if inferredContentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", inferredContentType)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Uroboros-http_request/1")
	}
	req.Header.Set(coordinationHeader, "1")

	started := time.Now()
	resp, err := client.Do(req)
	elapsed := time.Since(started)
	if err != nil {
		repeat := s.recordHTTPRepeat(ctx, a.Method, u, requestShape, transportFailureSignature(err), false)
		if repeat.Stop {
			return nil, fmt.Errorf("http request failed after %d ms: %w; repeat_stop=true after %d identical failures (signature %s): pivot to a different mechanism", elapsed.Milliseconds(), err, repeat.Count, repeat.Signature)
		}
		return nil, fmt.Errorf("http request failed after %d ms: %w", elapsed.Milliseconds(), err)
	}
	defer resp.Body.Close()
	read, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseRead+1))
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	hashComplete := len(read) <= maxHTTPResponseRead
	if !hashComplete {
		read = read[:maxHTTPResponseRead]
	}
	preview := read
	previewTruncated := !hashComplete
	if len(preview) > a.ResponsePreviewBytes {
		preview = preview[:a.ResponsePreviewBytes]
		previewTruncated = true
	}
	sum := sha256.Sum256(read)
	responseHash := hex.EncodeToString(sum[:])
	requestSum := sha256.Sum256(body)
	ids := capture.IDs()
	if !a.Ephemeral && len(ids) == 0 {
		return nil, fmt.Errorf("request completed with HTTP %d but the recording proxy returned no traffic id; response is non-evidentiary", resp.StatusCode)
	}
	for _, id := range ids {
		exists, err := s.m.Traffic().HasExchange(id)
		if err != nil {
			return nil, fmt.Errorf("verify captured traffic id %q: %w", id, err)
		}
		if !exists {
			return nil, fmt.Errorf("recording proxy returned uncommitted traffic id %q; response is non-evidentiary", id)
		}
	}
	if !a.Ephemeral {
		runInfo := agent.RunInfoFrom(ctx)
		if runInfo.TaskID > 0 {
			for _, id := range ids {
				s.enqueueBinaryTraffic(binaryintake.TrafficOrigin{
					TaskID: runInfo.TaskID, TrafficID: id, ProducerKind: "http_request",
				})
			}
		}
	}

	resolved := make([]string, 0, len(resolvedSet))
	for ip := range resolvedSet {
		resolved = append(resolved, ip)
	}
	sort.Strings(resolved)
	result := map[string]any{
		"complete":               true,
		"captured":               !a.Ephemeral,
		"method":                 a.Method,
		"requested_url":          u.String(),
		"final_url":              resp.Request.URL.String(),
		"status":                 resp.StatusCode,
		"status_text":            resp.Status,
		"headers":                resp.Header,
		"elapsed_ms":             elapsed.Milliseconds(),
		"resolved_ips":           resolved,
		"scope_matches":          scopes,
		"request_bytes":          len(body),
		"request_sha256":         hex.EncodeToString(requestSum[:]),
		"request_shape":          requestShape,
		"response_bytes_read":    len(read),
		"response_preview_bytes": len(preview),
		"response_sha256":        responseHash,
		"response_hash_complete": hashComplete,
		"body_truncated":         previewTruncated,
	}
	if !a.Ephemeral {
		result["traffic_id"] = ids[len(ids)-1]
		result["traffic_ids"] = ids
	} else {
		result["ephemeral"] = true
	}
	policy := responseRepeatPolicy(a.Method, resp.StatusCode, resp.Header.Get("Content-Type"), read)
	repeat := s.recordHTTPRepeatPolicy(ctx, a.Method, u, requestShape, policy.Signature, !policy.Failure, policy.Threshold, policy.HardStop)
	if repeat.Count > 0 {
		repeatDiagnostic := map[string]any{"class": policy.Class, "count": repeat.Count, "stop": repeat.Stop}
		result["repeat_count"] = repeat.Count
		result["repeat_signature"] = repeat.Signature
		result["repeat_stop"] = repeat.Stop
		result["repeat_class"] = policy.Class
		if policy.Failure && !policy.HardStop && repeat.Count >= policy.Threshold {
			warning := "equivalent result repeated; safe/idempotent method was not blocked, but the planner should pivot unless new evidence changes the request contract"
			result["repeat_warning"] = warning
			repeatDiagnostic["warning"] = warning
		}
		// Alphabetically precedes body in JSON so bounded activity summaries retain
		// the circuit-breaker decision without persisting the full response detail.
		result["auto_repeat"] = repeatDiagnostic
	}
	if a.RepeatOverrideReason != "" {
		result["repeat_override_used"] = true
		result["repeat_override_reason"] = a.RepeatOverrideReason
	}
	if isTextualHTTPBody(resp.Header.Get("Content-Type"), preview) {
		result["body"] = string(preview)
		result["body_encoding"] = "utf-8"
	} else {
		result["body_base64"] = base64.StdEncoding.EncodeToString(preview)
		result["body_encoding"] = "base64"
	}
	if !a.Ephemeral {
		if bootstrap, ok := s.bootstrapClientContract(ctx, resp.Request.URL, resp.Header.Get("Content-Type")); ok {
			worklist, _ := bootstrap["worklist"].(traffic.ContractWorklistResult)
			// This compact prefix survives activity-summary truncation; the complete
			// evidence-bound worklist remains in client_contract_bootstrap.
			result["auto_client_contract"] = map[string]any{
				"status": bootstrap["status"], "documents": worklist.DocumentsScanned,
				"matches": len(worklist.Matches), "pending_fetches": len(worklist.PendingFetches),
				"search_complete": worklist.SearchComplete,
			}
			result["client_contract_bootstrap"] = bootstrap
		}
	}
	return result, nil
}

var defaultClientContractPatterns = []string{
	"/api/", "/webapi/", "fetch(", "axios", "XMLHttpRequest", "graphql", "manifestUrl", "WebSocket",
}

func shouldBootstrapClientContract(target *url.URL, contentType string) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	mediaType = strings.ToLower(mediaType)
	if strings.Contains(mediaType, "html") || strings.Contains(mediaType, "javascript") || strings.Contains(mediaType, "ecmascript") {
		return true
	}
	if target == nil {
		return false
	}
	path := strings.ToLower(target.Path)
	return strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".mjs")
}

func (s *Server) bootstrapClientContract(ctx context.Context, target *url.URL, contentType string) (map[string]any, bool) {
	if !shouldBootstrapClientContract(target, contentType) || s.m == nil || s.m.Traffic() == nil {
		return nil, false
	}
	ri := agent.RunInfoFrom(ctx)
	if ri.TaskID <= 0 || ri.IntentID <= 0 || target == nil {
		return nil, false
	}
	authority := strings.ToLower(target.Host)
	key := fmt.Sprintf("%d\x00%d\x00%s", ri.TaskID, ri.IntentID, authority)
	if _, loaded := s.clientBootstraps.LoadOrStore(key, time.Now()); loaded {
		return nil, false
	}
	worklist, err := s.m.Traffic().BuildContractWorklist(
		authority, defaultClientContractPatterns, false, false, 240, 48, 32<<20, 40,
	)
	if err != nil {
		s.clientBootstraps.Delete(key)
		return map[string]any{
			"status": "error", "error": err.Error(),
			"instruction": "Run traffic_contract_worklist manually after more client artifacts are captured.",
		}, true
	}
	return map[string]any{
		"status":      "complete",
		"trigger":     "first_captured_client_artifact_for_worker_direction",
		"instruction": "Inspect the matches now. Fetch useful pending_fetches with http_request, rerun traffic_contract_worklist, and derive requests from recovered client contracts before closing route-dependent directions.",
		"worklist":    worklist,
	}, true
}

type captureIDTransport struct {
	base http.RoundTripper
	mu   sync.Mutex
	ids  []string
}

func (t *captureIDTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}
	if id := strings.TrimSpace(resp.Header.Get(traffic.TrafficIDHeader)); id != "" {
		t.mu.Lock()
		t.ids = append(t.ids, id)
		t.mu.Unlock()
		resp.Header.Del(traffic.TrafficIDHeader)
	}
	return resp, nil
}

func (t *captureIDTransport) IDs() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.ids...)
}

func isTextualHTTPBody(contentType string, body []byte) bool {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	mediaType = strings.ToLower(mediaType)
	if strings.HasPrefix(mediaType, "text/") || strings.Contains(mediaType, "json") || strings.Contains(mediaType, "xml") || strings.Contains(mediaType, "javascript") || strings.Contains(mediaType, "x-www-form-urlencoded") {
		return utf8.Valid(body)
	}
	return mediaType == "" && utf8.Valid(body) && !bytes.Contains(body, []byte{0})
}

func (s *Server) authorizeHTTPDestination(ctx context.Context, u *url.URL, requireTaskScope bool) (authorizedDestination, error) {
	if u == nil {
		return authorizedDestination{}, fmt.Errorf("destination URL is missing")
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return authorizedDestination{}, fmt.Errorf("only http and https destinations are supported")
	}
	if u.User != nil {
		return authorizedDestination{}, fmt.Errorf("URL userinfo is not allowed; use an Authorization header")
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(u.Hostname()), "."))
	if host == "" {
		return authorizedDestination{}, fmt.Errorf("destination URL has no hostname")
	}
	if port := u.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return authorizedDestination{}, fmt.Errorf("destination URL has an invalid port")
		}
	}
	resolved, err := resolveHTTPHost(ctx, host)
	if err != nil {
		return authorizedDestination{}, err
	}
	result := authorizedDestination{Host: host, ResolvedIPs: resolved, Scope: "operator preflight"}
	if !requireTaskScope {
		return result, nil
	}
	if s.m == nil || s.m.Assets() == nil {
		return authorizedDestination{}, fmt.Errorf("task scope store is unavailable")
	}
	ri := agent.RunInfoFrom(ctx)
	if ri.TaskID <= 0 {
		return authorizedDestination{}, fmt.Errorf("http_request requires an active task context")
	}

	globalBlock, err := s.m.Assets().ListAssetInterceptRules()
	if err != nil {
		return authorizedDestination{}, fmt.Errorf("load global destination rules: %w", err)
	}
	taskBlock, taskAllow, err := s.m.Assets().TaskInterceptRulesSplit(ri.TaskID)
	if err != nil {
		return authorizedDestination{}, fmt.Errorf("load task destination rules: %w", err)
	}
	decision := db.EvaluateAssetGate(append(globalBlock, taskBlock...), taskAllow, []string{host}, resolved, []string{u.String()})
	if !decision.Allowed {
		return authorizedDestination{}, fmt.Errorf("destination rejected by task policy: %s", decision.Reason)
	}

	scopes, err := s.m.Assets().ListTaskScopeWithSources(ri.TaskID)
	if err != nil {
		return authorizedDestination{}, fmt.Errorf("load task scope: %w", err)
	}
	match, err := s.matchHTTPTaskScope(host, resolved, scopes)
	if err != nil {
		return authorizedDestination{}, err
	}
	if match == "" {
		return authorizedDestination{}, fmt.Errorf("destination %s is outside the current task scope; register the exact host or network before testing", host)
	}
	result.Scope = match
	return result, nil
}

func resolveHTTPHost(ctx context.Context, host string) ([]string, error) {
	if parsed := net.ParseIP(host); parsed != nil {
		return []string{parsed.String()}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupIPAddr(lookupCtx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve destination %s: %w", host, err)
	}
	set := map[string]struct{}{}
	for _, address := range addresses {
		if address.IP == nil || address.IP.IsUnspecified() || address.IP.IsMulticast() {
			return nil, fmt.Errorf("destination %s resolved to an invalid address", host)
		}
		set[address.IP.String()] = struct{}{}
	}
	if len(set) == 0 {
		return nil, fmt.Errorf("destination %s resolved to no addresses", host)
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out, nil
}

func domainWithin(host, scope string) bool {
	host = db.DomainKey(host)
	scope = db.DomainKey(scope)
	return host != "" && scope != "" && (host == scope || strings.HasSuffix(host, "."+scope))
}

func ipsWithin(resolved []string, network string) bool {
	_, ipnet, err := net.ParseCIDR(strings.TrimSpace(network))
	if err != nil {
		return false
	}
	for _, value := range resolved {
		if ip := net.ParseIP(value); ip != nil && ipnet.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) matchHTTPTaskScope(host string, resolved []string, scopes []db.TaskScope) (string, error) {
	for _, scope := range scopes {
		switch scope.Kind {
		case "subdomain":
			if db.DomainKey(host) == db.DomainKey(scope.Domain) {
				return "subdomain:" + scope.Domain, nil
			}
		case "root_domain":
			if domainWithin(host, scope.Domain) {
				return "root_domain:" + scope.Domain, nil
			}
		case "ip", "cidr":
			// A hostname authorized only through a prior DNS result can be resolved
			// differently by the recording proxy (DNS rebinding / TOCTOU). Network
			// scopes therefore authorize only literal-IP URLs; hostnames need an
			// explicit domain scope.
			if net.ParseIP(host) != nil && ipsWithin(resolved, scope.Net) {
				return scope.Kind + ":" + scope.Net, nil
			}
		case "company":
			if scope.CompanyID == nil || *scope.CompanyID <= 0 || s.m == nil || s.m.PG() == nil {
				continue
			}
			rules, err := s.m.PG().Companies().GetScope(*scope.CompanyID)
			if err != nil {
				return "", fmt.Errorf("load company scope %d: %w", *scope.CompanyID, err)
			}
			for _, rule := range rules {
				switch rule.Kind {
				case "domain":
					if domainWithin(host, rule.Domain) {
						return fmt.Sprintf("company:%d/domain:%s", *scope.CompanyID, rule.Domain), nil
					}
				case "ip", "cidr":
					if net.ParseIP(host) != nil && ipsWithin(resolved, rule.Net) {
						return fmt.Sprintf("company:%d/%s:%s", *scope.CompanyID, rule.Kind, rule.Net), nil
					}
				}
			}
		}
	}
	return "", nil
}
