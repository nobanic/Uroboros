package katana

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var diagnosticURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"']+`)

const (
	maxKnowledgeForms       = 16
	maxKnowledgeFields      = 32
	maxKnowledgeSecrets     = 64
	maxKnowledgeParams      = 128
	maxKnowledgeStringRunes = 256
)

type outputRequest struct {
	Method   string `json:"method"`
	Endpoint string `json:"endpoint"`
	Source   string `json:"source"`
}

type outputForm struct {
	Method string `json:"method"`
	Action string `json:"action"`
}

type outputResponse struct {
	StatusCode    int             `json:"status_code"`
	Technologies  []string        `json:"technologies"`
	Forms         []outputForm    `json:"forms"`
	XHRRequests   []outputRequest `json:"xhr_requests"`
	KnowledgeBase json.RawMessage `json:"knowledgebase"`
}

type outputLine struct {
	Request  *outputRequest  `json:"request"`
	Response *outputResponse `json:"response"`
	Error    string          `json:"error"`
}

type candidate struct {
	Source        string
	Method        string
	URL           string
	StatusCode    *int
	Technologies  []string
	KnowledgeBase json.RawMessage
}

type rawKnowledgeBase struct {
	PageType string                `json:"PageType"`
	Forms    []rawKnowledgeForm    `json:"Forms"`
	Secrets  rawKnowledgeSecrets   `json:"secrets"`
	Endpoint *rawKnowledgeEndpoint `json:"endpoints"`
}

type rawKnowledgeForm struct {
	Type        string            `json:"type"`
	CaptchaType string            `json:"captcha_type"`
	Fields      map[string]string `json:"fields"`
}

type rawKnowledgeSecrets struct {
	Findings []rawKnowledgeSecret `json:"findings"`
}

type rawKnowledgeSecret struct {
	Rule       string               `json:"rule"`
	Kind       string               `json:"kind"`
	Score      *float64             `json:"score"`
	Severity   string               `json:"severity"`
	Validation *rawSecretValidation `json:"validation"`
}

type rawSecretValidation struct {
	Status     string   `json:"status"`
	Confidence *float64 `json:"confidence"`
}

type rawKnowledgeEndpoint struct {
	Class       string   `json:"class"`
	Method      string   `json:"method"`
	URL         string   `json:"url"`
	ContentType string   `json:"content_type"`
	Auth        string   `json:"auth"`
	Params      []string `json:"params"`
}

type durableKnowledgeBase struct {
	PageType string                    `json:"page_type,omitempty"`
	Forms    []durableKnowledgeForm    `json:"forms,omitempty"`
	Secrets  []durableKnowledgeSecret  `json:"secrets,omitempty"`
	Endpoint *durableKnowledgeEndpoint `json:"endpoint,omitempty"`
}

type durableKnowledgeForm struct {
	Type        string            `json:"type,omitempty"`
	CaptchaType string            `json:"captcha_type,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
}

type durableKnowledgeSecret struct {
	Rule       string                   `json:"rule,omitempty"`
	Kind       string                   `json:"kind,omitempty"`
	Score      *float64                 `json:"score,omitempty"`
	Severity   string                   `json:"severity,omitempty"`
	Validation *durableSecretValidation `json:"validation,omitempty"`
}

type durableSecretValidation struct {
	Status     string   `json:"status,omitempty"`
	Confidence *float64 `json:"confidence,omitempty"`
}

type durableKnowledgeEndpoint struct {
	Class       string   `json:"class,omitempty"`
	Method      string   `json:"method,omitempty"`
	URL         string   `json:"url,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	Auth        string   `json:"auth,omitempty"`
	Params      []string `json:"params,omitempty"`
}

// sanitizeExecutionURL canonicalizes a seed without removing authorized request
// semantics. Seeds travel over a private stdin pipe and are separately redacted
// before persistence; fragments are omitted because HTTP never sends them.
func sanitizeExecutionURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	host := strings.ToLower(parsed.Hostname())
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		parsed.Host = "[" + host + "]"
	} else {
		parsed.Host = host
	}
	parsed.Fragment = ""
	parsed.RawFragment = ""
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), true
}

// seedOriginFingerprint identifies the executable starting surface without
// retaining credentials, paths or query values. Automatic runs therefore
// repeat when a scanner discovers a genuinely new scheme/host/port, but not for
// every endpoint Katana (or another tool) adds beneath an already-crawled origin.
func seedOriginFingerprint(values []string) (string, error) {
	origins := make(map[string]struct{}, len(values))
	for _, raw := range values {
		clean, ok := sanitizeExecutionURL(raw)
		if !ok {
			continue
		}
		parsed, err := url.Parse(clean)
		if err != nil {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		port := parsed.Port()
		if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
			port = ""
		}
		authority := host
		if port != "" {
			authority = net.JoinHostPort(host, port)
		} else if strings.Contains(host, ":") {
			authority = "[" + host + "]"
		}
		origins[parsed.Scheme+"://"+authority] = struct{}{}
	}
	if len(origins) == 0 {
		return "", fmt.Errorf("no HTTP(S) seed origins")
	}
	ordered := make([]string, 0, len(origins))
	for origin := range origins {
		ordered = append(ordered, origin)
	}
	sort.Strings(ordered)
	hash := sha256.New()
	for _, origin := range ordered {
		_, _ = fmt.Fprintf(hash, "origin\x00%s\x00", origin)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func parseOutputCandidates(raw []byte) ([]candidate, error) {
	var line outputLine
	if err := json.Unmarshal(raw, &line); err != nil {
		return nil, err
	}
	if line.Request == nil {
		return nil, nil
	}
	method := strings.ToUpper(strings.TrimSpace(line.Request.Method))
	if method == "" {
		method = http.MethodGet
	}
	primaryURL, ok := sanitizeDiscoveryURL(line.Request.Endpoint)
	if !ok {
		return nil, nil
	}
	primary := candidate{Source: "katana", Method: method, URL: primaryURL}
	if source := strings.TrimSpace(line.Request.Source); source != "" {
		primary.Source = source
	}
	if line.Response != nil {
		if line.Response.StatusCode > 0 {
			status := line.Response.StatusCode
			primary.StatusCode = &status
		}
		primary.Technologies = normalizedStrings(line.Response.Technologies)
		knowledgeBase, err := sanitizeKnowledgeBase(line.Response.KnowledgeBase)
		if err != nil {
			return nil, fmt.Errorf("sanitize knowledge base: %w", err)
		}
		primary.KnowledgeBase = knowledgeBase
	}
	out := []candidate{primary}
	if line.Response == nil {
		return out, nil
	}
	for _, xhr := range line.Response.XHRRequests {
		if resolved, ok := resolveDiscoveryURL(primaryURL, xhr.Endpoint); ok {
			method := strings.ToUpper(strings.TrimSpace(xhr.Method))
			if method == "" {
				method = http.MethodGet
			}
			out = append(out, candidate{Source: "xhr", Method: method, URL: resolved})
		}
	}
	for _, form := range line.Response.Forms {
		if resolved, ok := resolveDiscoveryURL(primaryURL, form.Action); ok {
			method := strings.ToUpper(strings.TrimSpace(form.Method))
			if method == "" {
				method = http.MethodGet
			}
			out = append(out, candidate{Source: "form", Method: method, URL: resolved})
		}
	}
	return out, nil
}

// sanitizeKnowledgeBase keeps only bounded classifier metadata. Raw HTML,
// secret matches and query values never enter durable discovery records.
func sanitizeKnowledgeBase(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var input rawKnowledgeBase
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, err
	}
	output := durableKnowledgeBase{PageType: boundedKnowledgeString(input.PageType)}
	for _, form := range input.Forms {
		if len(output.Forms) >= maxKnowledgeForms {
			break
		}
		item := durableKnowledgeForm{
			Type:        boundedKnowledgeString(form.Type),
			CaptchaType: boundedKnowledgeString(form.CaptchaType),
		}
		if len(form.Fields) > 0 {
			keys := make([]string, 0, len(form.Fields))
			for key := range form.Fields {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			item.Fields = make(map[string]string, min(len(keys), maxKnowledgeFields))
			for _, key := range keys {
				if len(item.Fields) >= maxKnowledgeFields {
					break
				}
				name := boundedKnowledgeString(key)
				classification := boundedKnowledgeString(form.Fields[key])
				if name != "" && classification != "" {
					item.Fields[name] = classification
				}
			}
			if len(item.Fields) == 0 {
				item.Fields = nil
			}
		}
		if item.Type != "" || item.CaptchaType != "" || len(item.Fields) > 0 {
			output.Forms = append(output.Forms, item)
		}
	}
	for _, secret := range input.Secrets.Findings {
		if len(output.Secrets) >= maxKnowledgeSecrets {
			break
		}
		item := durableKnowledgeSecret{
			Rule: boundedKnowledgeString(secret.Rule), Kind: boundedKnowledgeString(secret.Kind),
			Score: secret.Score, Severity: boundedKnowledgeString(secret.Severity),
		}
		if secret.Validation != nil {
			item.Validation = &durableSecretValidation{
				Status: boundedKnowledgeString(secret.Validation.Status), Confidence: secret.Validation.Confidence,
			}
		}
		if item.Rule != "" || item.Kind != "" || item.Score != nil || item.Severity != "" || item.Validation != nil {
			output.Secrets = append(output.Secrets, item)
		}
	}
	if endpoint := input.Endpoint; endpoint != nil {
		item := &durableKnowledgeEndpoint{
			Class:       boundedKnowledgeString(endpoint.Class),
			Method:      strings.ToUpper(boundedKnowledgeString(endpoint.Method)),
			ContentType: boundedKnowledgeString(endpoint.ContentType),
			Auth:        strings.ToLower(boundedKnowledgeString(endpoint.Auth)),
			Params:      boundedKnowledgeStrings(endpoint.Params, maxKnowledgeParams),
		}
		if clean, ok := sanitizeDiscoveryURL(endpoint.URL); ok {
			item.URL = clean
		}
		if item.Class != "" || item.Method != "" || item.URL != "" || item.ContentType != "" || item.Auth != "" || len(item.Params) > 0 {
			output.Endpoint = item
		}
	}
	if output.PageType == "" && len(output.Forms) == 0 && len(output.Secrets) == 0 && output.Endpoint == nil {
		return nil, nil
	}
	return json.Marshal(output)
}

func boundedKnowledgeString(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) > maxKnowledgeStringRunes {
		runes = runes[:maxKnowledgeStringRunes]
	}
	return string(runes)
}

func boundedKnowledgeStrings(values []string, limit int) []string {
	if limit <= 0 || len(values) == 0 {
		return nil
	}
	set := make(map[string]struct{}, min(len(values), limit))
	for _, value := range values {
		value = boundedKnowledgeString(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, min(len(set), limit))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// sanitizeDiscoveryURL strips credentials, fragments and query values while
// retaining query parameter names. Full target traffic remains in the Traffic
// evidence store; job metadata does not need a second copy of tokens or PII.
func sanitizeDiscoveryURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	parsed.User = nil
	parsed.Fragment = ""
	parsed.RawFragment = ""
	query := parsed.Query()
	for key := range query {
		query[key] = []string{""}
	}
	parsed.RawQuery = query.Encode()
	parsed.ForceQuery = false
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	return parsed.String(), true
}

// redactDiagnostic retains enough child-process stderr for troubleshooting but
// never creates a second copy of URL credentials or query values in job metadata.
// Complete authorized exchanges remain available in the Traffic evidence store.
func redactDiagnostic(value string) string {
	return diagnosticURLPattern.ReplaceAllStringFunc(value, func(raw string) string {
		if clean, ok := sanitizeDiscoveryURL(raw); ok {
			return clean
		}
		return "[redacted URL]"
	})
}

func resolveDiscoveryURL(baseURL, raw string) (string, bool) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", false
	}
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	return sanitizeDiscoveryURL(base.ResolveReference(reference).String())
}

func normalizedStrings(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for value := range set {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func endpointParams(raw string) []map[string]any {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(parsed.Query()))
	for key := range parsed.Query() {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]any{"location": "query", "name": key})
	}
	return out
}

func serviceURL(raw string) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false
	}
	parsed.User = nil
	parsed.Path = "/"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), true
}

func isOriginRootURL(raw string) bool {
	parsed, err := url.Parse(raw)
	return err == nil && (parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == ""
}
