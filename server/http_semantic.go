package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/net/html"
)

type httpRepeatPolicy struct {
	Signature string
	Class     string
	Failure   bool
	HardStop  bool
	Threshold int
}

var (
	semanticUUID     = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\b`)
	semanticISOTime  = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}[T ][0-9:.+-]+Z?\b`)
	semanticUnixTime = regexp.MustCompile(`\b(?:1[5-9]|2[0-9])\d{8}(?:\d{3})?\b`)
	semanticSpace    = regexp.MustCompile(`\s+`)
)

var volatileJSONKeys = map[string]struct{}{
	"timestamp": {}, "time": {}, "datetime": {}, "date": {},
	"request_id": {}, "requestid": {}, "trace_id": {}, "traceid": {},
	"correlation_id": {}, "correlationid": {}, "nonce": {},
	"csrf": {}, "csrf_token": {}, "csrftoken": {},
}

func normalizeSemanticText(value string) string {
	value = strings.ToLower(value)
	value = semanticUUID.ReplaceAllString(value, "<uuid>")
	value = semanticISOTime.ReplaceAllString(value, "<time>")
	value = semanticUnixTime.ReplaceAllString(value, "<time>")
	return strings.TrimSpace(semanticSpace.ReplaceAllString(value, " "))
}

func visibleHTMLText(body []byte) string {
	tokenizer := html.NewTokenizer(bytes.NewReader(body))
	var out strings.Builder
	skipDepth := 0
	for {
		tokenType := tokenizer.Next()
		switch tokenType {
		case html.ErrorToken:
			return out.String()
		case html.StartTagToken:
			token := tokenizer.Token()
			if token.Data == "script" || token.Data == "style" || token.Data == "template" {
				skipDepth++
			}
		case html.EndTagToken:
			token := tokenizer.Token()
			if skipDepth > 0 && (token.Data == "script" || token.Data == "style" || token.Data == "template") {
				skipDepth--
			}
		case html.TextToken:
			if skipDepth == 0 {
				out.WriteByte(' ')
				out.Write(tokenizer.Text())
			}
		}
	}
}

func normalizeJSONValue(value any) any {
	switch current := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		out := make(map[string]any, len(current))
		for _, key := range keys {
			lower := strings.ToLower(strings.TrimSpace(key))
			if _, volatile := volatileJSONKeys[lower]; volatile {
				out[key] = "<volatile>"
				continue
			}
			out[key] = normalizeJSONValue(current[key])
		}
		return out
	case []any:
		out := make([]any, len(current))
		for index := range current {
			out[index] = normalizeJSONValue(current[index])
		}
		return out
	case string:
		return normalizeSemanticText(current)
	default:
		return value
	}
}

func canonicalResponseMeaning(contentType string, body []byte) (string, any) {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	mediaType = strings.ToLower(mediaType)
	if strings.Contains(mediaType, "json") || (mediaType == "" && json.Valid(body)) {
		var value any
		if json.Unmarshal(body, &value) == nil {
			normalized := normalizeJSONValue(value)
			encoded, _ := json.Marshal(normalized)
			return "json", normalizedWithRaw{Canonical: string(encoded), Value: normalized}
		}
	}
	if strings.Contains(mediaType, "html") {
		return "html", normalizeSemanticText(visibleHTMLText(body))
	}
	if isTextualHTTPBody(contentType, body) {
		return "text", normalizeSemanticText(string(body))
	}
	sum := sha256.Sum256(body)
	return "binary", hex.EncodeToString(sum[:])
}

type normalizedWithRaw struct {
	Canonical string
	Value     any
}

func semanticCanonical(value any) string {
	if wrapped, ok := value.(normalizedWithRaw); ok {
		return wrapped.Canonical
	}
	return fmt.Sprint(value)
}

func jsonSignalsApplicationFailure(value any) bool {
	switch current := value.(type) {
	case normalizedWithRaw:
		return jsonSignalsApplicationFailure(current.Value)
	case map[string]any:
		for key, item := range current {
			lower := strings.ToLower(strings.TrimSpace(key))
			switch lower {
			case "error", "errors", "exception":
				if meaningfulErrorValue(item) {
					return true
				}
			case "success", "ok":
				if boolean, ok := item.(bool); ok && !boolean {
					return true
				}
			case "status", "result":
				text := normalizeSemanticText(fmt.Sprint(item))
				if text == "error" || text == "failed" || text == "failure" || text == "denied" {
					return true
				}
			case "message", "detail", "reason":
				if textSignalsApplicationFailure(normalizeSemanticText(fmt.Sprint(item))) {
					return true
				}
			}
			if jsonSignalsApplicationFailure(item) {
				return true
			}
		}
	case []any:
		for _, item := range current {
			if jsonSignalsApplicationFailure(item) {
				return true
			}
		}
	}
	return false
}

func meaningfulErrorValue(value any) bool {
	switch current := value.(type) {
	case nil:
		return false
	case bool:
		return current
	case string:
		text := strings.TrimSpace(strings.ToLower(current))
		return text != "" && text != "false" && text != "none" && text != "null"
	case []any:
		return len(current) > 0
	case map[string]any:
		return len(current) > 0
	default:
		return true
	}
}

func textSignalsApplicationFailure(text string) bool {
	markers := []string{
		"invalid credential", "incorrect password", "login failed", "authentication failed",
		"access denied", "permission denied", "not authorized", "unauthorized", "forbidden",
		"request failed", "operation failed", "an error occurred", "internal server error",
		"invalid token", "token expired", "session expired",
	}
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func nonIdempotentMethod(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
		return false
	default:
		return true
	}
}

func responseRepeatPolicy(method string, status int, contentType string, body []byte) httpRepeatPolicy {
	kind, meaning := canonicalResponseMeaning(contentType, body)
	canonical := semanticCanonical(meaning)
	sum := sha256.Sum256([]byte(canonical))
	policy := httpRepeatPolicy{
		Signature: fmt.Sprintf("http:%d:%s:%s", status, kind, hex.EncodeToString(sum[:8])),
		Class:     "success",
		Threshold: 2,
	}
	if status >= 400 {
		policy.Failure = true
		policy.Class = "http_error"
		policy.HardStop = nonIdempotentMethod(method)
		return policy
	}
	applicationFailure := jsonSignalsApplicationFailure(meaning)
	if kind == "text" || kind == "html" {
		applicationFailure = applicationFailure || textSignalsApplicationFailure(canonical)
	}
	if applicationFailure {
		policy.Failure = true
		policy.Class = "application_error"
		policy.Threshold = 3
		policy.HardStop = nonIdempotentMethod(method)
	}
	return policy
}
