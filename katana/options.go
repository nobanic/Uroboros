package katana

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	ExpectedVersion               = "v1.8.0"
	ExpectedKnowledgeModelSHA256  = "eba238b3093ff7aa4772ce17536bc313cb955428a6aa87dae41695a2dede6e59"
	defaultKnowledgeModelFilename = "model.json"
)

const practicalUnlimitedInt = math.MaxInt

// Options is the typed Katana surface Uroboros owns. Zero request, duration,
// depth, page and rate budgets mean unlimited. There is deliberately no raw CLI
// escape hatch: scope/proxy/evidence flags remain runtime invariants.
type Options struct {
	Mode                        string   `json:"mode"`
	MaxDepth                    int      `json:"max_depth"`
	MaxRequests                 int64    `json:"max_requests"`
	MaxDurationSeconds          int      `json:"max_duration_seconds"`
	MaxDomainPages              int      `json:"max_domain_pages"`
	MaxResponseBytes            int      `json:"max_response_bytes"`
	AllowedMethods              []string `json:"allowed_methods,omitempty"`
	JSCrawl                     bool     `json:"js_crawl"`
	JSLuice                     bool     `json:"jsluice"`
	KnownFiles                  string   `json:"known_files"`
	AutomaticFormFill           bool     `json:"automatic_form_fill"`
	FormExtraction              bool     `json:"form_extraction"`
	TechDetect                  bool     `json:"tech_detect"`
	Strategy                    string   `json:"strategy"`
	IgnoreQueryParams           bool     `json:"ignore_query_params"`
	FilterSimilar               bool     `json:"filter_similar"`
	FilterSimilarThreshold      int      `json:"filter_similar_threshold"`
	TLSImpersonate              bool     `json:"tls_impersonate"`
	DisableRedirects            bool     `json:"disable_redirects"`
	PathClimb                   bool     `json:"path_climb"`
	KnowledgeBase               bool     `json:"knowledge_base"`
	ExtractSecrets              bool     `json:"extract_secrets"`
	ExtractEndpoints            bool     `json:"extract_endpoints"`
	XHRExtraction               bool     `json:"xhr_extraction"`
	Concurrency                 int      `json:"concurrency"`
	Parallelism                 int      `json:"parallelism"`
	DelaySeconds                int      `json:"delay_seconds"`
	RateLimitPerSecond          int      `json:"rate_limit_per_second"`
	RateLimitPerMinute          int      `json:"rate_limit_per_minute"`
	HostRateLimitPerSecond      int      `json:"host_rate_limit_per_second"`
	HostRateLimitPerMinute      int      `json:"host_rate_limit_per_minute"`
	TimeoutSeconds              int      `json:"timeout_seconds"`
	Retries                     int      `json:"retries"`
	TimeStableSeconds           int      `json:"time_stable_seconds"`
	MaxFailureCount             int      `json:"max_failure_count"`
	PageLoadStrategy            string   `json:"page_load_strategy"`
	DOMWaitSeconds              int      `json:"dom_wait_seconds"`
	HeadlessNoSandbox           bool     `json:"headless_no_sandbox"`
	PageContentSimilar          bool     `json:"page_content_similar"`
	PageContentSimilarMode      string   `json:"page_content_similar_mode"`
	PageContentSimilarDistance  int      `json:"page_content_similar_distance"`
	PageContentSimilarThreshold string   `json:"page_content_similar_threshold"`
	PageContentSimilarBudget    int      `json:"page_content_similar_budget"`
}

func DefaultOptions() Options {
	return Options{
		Mode:                        "standard",
		JSCrawl:                     true,
		JSLuice:                     true,
		KnownFiles:                  "all",
		FormExtraction:              true,
		TechDetect:                  true,
		Strategy:                    "depth-first",
		PathClimb:                   true,
		KnowledgeBase:               true,
		ExtractSecrets:              true,
		ExtractEndpoints:            true,
		XHRExtraction:               true,
		Concurrency:                 10,
		Parallelism:                 10,
		TimeoutSeconds:              10,
		Retries:                     1,
		TimeStableSeconds:           1,
		MaxFailureCount:             0,
		PageLoadStrategy:            "heuristic",
		DOMWaitSeconds:              5,
		HeadlessNoSandbox:           true,
		FilterSimilarThreshold:      10,
		PageContentSimilarMode:      "simhash",
		PageContentSimilarDistance:  3,
		PageContentSimilarThreshold: "0.85",
		PageContentSimilarBudget:    1,
	}
}

func (o Options) Normalize() (Options, error) {
	o.Mode = strings.ToLower(strings.TrimSpace(o.Mode))
	if o.Mode == "" {
		o.Mode = "standard"
	}
	switch o.Mode {
	case "standard", "headless", "hybrid":
	default:
		return Options{}, fmt.Errorf("mode must be standard, headless or hybrid")
	}
	if o.MaxDepth < 0 || o.MaxRequests < 0 || o.MaxDurationSeconds < 0 || o.MaxDomainPages < 0 || o.MaxResponseBytes < 0 {
		return Options{}, fmt.Errorf("Katana limits cannot be negative; use 0 for unlimited")
	}
	for name, value := range map[string]int{
		"concurrency": o.Concurrency, "parallelism": o.Parallelism, "delay_seconds": o.DelaySeconds,
		"rate_limit_per_second": o.RateLimitPerSecond, "rate_limit_per_minute": o.RateLimitPerMinute,
		"host_rate_limit_per_second": o.HostRateLimitPerSecond, "host_rate_limit_per_minute": o.HostRateLimitPerMinute,
		"timeout_seconds": o.TimeoutSeconds, "retries": o.Retries, "time_stable_seconds": o.TimeStableSeconds,
		"max_failure_count": o.MaxFailureCount, "dom_wait_seconds": o.DOMWaitSeconds,
		"filter_similar_threshold": o.FilterSimilarThreshold, "page_content_similar_distance": o.PageContentSimilarDistance,
		"page_content_similar_budget": o.PageContentSimilarBudget,
	} {
		if value < 0 {
			return Options{}, fmt.Errorf("%s cannot be negative", name)
		}
	}
	o.KnownFiles = strings.ToLower(strings.TrimSpace(o.KnownFiles))
	switch o.KnownFiles {
	case "", "all", "robotstxt", "sitemapxml":
	default:
		return Options{}, fmt.Errorf("known_files must be empty, all, robotstxt or sitemapxml")
	}
	o.Strategy = strings.ToLower(strings.TrimSpace(o.Strategy))
	if o.Strategy == "" {
		o.Strategy = "depth-first"
	}
	if o.Strategy != "depth-first" && o.Strategy != "breadth-first" {
		return Options{}, fmt.Errorf("strategy must be depth-first or breadth-first")
	}
	o.PageLoadStrategy = strings.ToLower(strings.TrimSpace(o.PageLoadStrategy))
	if o.PageLoadStrategy == "" {
		o.PageLoadStrategy = "heuristic"
	}
	switch o.PageLoadStrategy {
	case "heuristic", "load", "domcontentloaded", "networkidle", "none":
	default:
		return Options{}, fmt.Errorf("invalid page_load_strategy")
	}
	o.PageContentSimilarMode = strings.ToLower(strings.TrimSpace(o.PageContentSimilarMode))
	if o.PageContentSimilarMode == "" {
		o.PageContentSimilarMode = "simhash"
	}
	switch o.PageContentSimilarMode {
	case "simhash", "tfidf", "bm25":
	default:
		return Options{}, fmt.Errorf("invalid page_content_similar_mode")
	}
	if o.PageContentSimilarThreshold == "" {
		o.PageContentSimilarThreshold = "0.85"
	}
	threshold, err := strconv.ParseFloat(o.PageContentSimilarThreshold, 64)
	if err != nil || threshold < 0 || threshold > 1 {
		return Options{}, fmt.Errorf("page_content_similar_threshold must be between 0 and 1")
	}
	methods := make(map[string]struct{}, len(o.AllowedMethods))
	for _, method := range o.AllowedMethods {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" {
			return Options{}, fmt.Errorf("allowed method cannot be empty")
		}
		for _, r := range method {
			if r <= 32 || r >= 127 || strings.ContainsRune("()<>@,;:\\\"/[]?={}", r) {
				return Options{}, fmt.Errorf("invalid allowed HTTP method %q", method)
			}
		}
		methods[method] = struct{}{}
	}
	o.AllowedMethods = make([]string, 0, len(methods))
	for method := range methods {
		o.AllowedMethods = append(o.AllowedMethods, method)
	}
	sort.Strings(o.AllowedMethods)
	return o, nil
}

func (o Options) Hash() (string, error) {
	normalized, err := o.Normalize()
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

// BuildArgs builds arguments only; the executable is selected separately and no
// shell is involved. The run-specific proxy is mandatory because it is the hard
// authorization/evidence boundary.
func BuildArgs(options Options, seeds []string, proxyURL, chromePath string) ([]string, error) {
	o, err := options.Normalize()
	if err != nil {
		return nil, err
	}
	if len(seeds) == 0 {
		return nil, fmt.Errorf("at least one HTTP seed is required")
	}
	if strings.TrimSpace(proxyURL) == "" {
		return nil, fmt.Errorf("scoped proxy URL is required")
	}
	args := []string{
		"-jsonl", "-silent", "-no-color", "-omit-raw", "-omit-body", "-disable-update-check",
		// Katana's own seed-relative scope would reject an authorized sibling
		// subdomain. Uroboros' scoped proxy remains the authoritative hard boundary.
		"-no-scope", "-proxy", proxyURL,
		"-depth", strconv.Itoa(unlimitedInt(o.MaxDepth)),
		"-max-domain-pages", strconv.Itoa(o.MaxDomainPages),
		"-max-response-size", strconv.Itoa(unlimitedInt(o.MaxResponseBytes)),
		"-rate-limit", strconv.Itoa(o.RateLimitPerSecond),
		"-rate-limit-minute", strconv.Itoa(o.RateLimitPerMinute),
		"-host-rate-limit", strconv.Itoa(o.HostRateLimitPerSecond),
		"-host-rate-limit-minute", strconv.Itoa(o.HostRateLimitPerMinute),
		"-strategy", o.Strategy,
	}
	// Seeds are written to the child process's stdin by the runner. Keeping target
	// URLs (which can carry authorized credentials/query values) out of argv avoids
	// exposing them through process listings.
	if o.MaxDurationSeconds > 0 {
		args = append(args, "-crawl-duration", strconv.Itoa(o.MaxDurationSeconds)+"s")
	}
	if o.Concurrency > 0 {
		args = append(args, "-concurrency", strconv.Itoa(o.Concurrency))
	}
	if o.Parallelism > 0 {
		args = append(args, "-parallelism", strconv.Itoa(o.Parallelism))
	}
	if o.DelaySeconds > 0 {
		args = append(args, "-delay", strconv.Itoa(o.DelaySeconds))
	}
	if o.TimeoutSeconds > 0 {
		args = append(args, "-timeout", strconv.Itoa(o.TimeoutSeconds))
	}
	// Katana defaults to one retry when the flag is absent, so pass zero
	// explicitly when the caller asks to disable retries.
	args = append(args, "-retry", strconv.Itoa(o.Retries))
	// Like retry, these upstream flags have nonzero defaults. Pass zero
	// explicitly so the typed API can genuinely request no stability/DOM wait.
	args = append(args, "-time-stable", strconv.Itoa(o.TimeStableSeconds))
	if o.KnownFiles != "" {
		args = append(args, "-known-files", o.KnownFiles)
	}
	args = appendBool(args, o.JSCrawl, "-js-crawl")
	args = appendBool(args, o.JSLuice, "-jsluice")
	args = appendBool(args, o.AutomaticFormFill, "-automatic-form-fill")
	args = appendBool(args, o.FormExtraction, "-form-extraction")
	args = appendBool(args, o.TechDetect, "-tech-detect")
	args = appendBool(args, o.IgnoreQueryParams, "-ignore-query-params")
	args = appendBool(args, o.FilterSimilar, "-filter-similar")
	if o.FilterSimilar {
		args = append(args, "-filter-similar-threshold", strconv.Itoa(o.FilterSimilarThreshold))
	}
	args = appendBool(args, o.TLSImpersonate, "-tls-impersonate")
	args = appendBool(args, o.DisableRedirects, "-disable-redirects")
	args = appendBool(args, o.PathClimb, "-path-climb")
	args = appendBool(args, o.KnowledgeBase, "-knowledge-base")
	args = appendBool(args, o.KnowledgeBase && o.ExtractSecrets, "-kb-secrets")
	args = appendBool(args, o.KnowledgeBase && o.ExtractEndpoints, "-kb-endpoints")
	args = appendBool(args, o.PageContentSimilar, "-page-content-similar")
	if o.PageContentSimilar {
		args = append(args,
			"-page-content-similar-mode", o.PageContentSimilarMode,
			"-page-content-similar-distance", strconv.Itoa(o.PageContentSimilarDistance),
			"-page-content-similar-threshold", o.PageContentSimilarThreshold,
			"-page-content-similar-budget", strconv.Itoa(o.PageContentSimilarBudget))
	}
	if o.Mode != "standard" {
		if strings.TrimSpace(chromePath) == "" {
			return nil, fmt.Errorf("%s mode requires a locally installed Chromium binary", o.Mode)
		}
		if o.Mode == "headless" {
			args = append(args, "-headless")
		} else {
			args = append(args, "-hybrid")
		}
		args = appendBool(args, o.HeadlessNoSandbox, "-no-sandbox")
		args = appendBool(args, o.XHRExtraction, "-xhr-extraction")
		// Chrome otherwise has non-HTTP escape paths that do not traverse the
		// scoped evidence proxy. These flags preserve browser discovery while
		// forcing WebRTC onto proxied transports and disabling QUIC/UDP egress.
		args = append(args, "-headless-options",
			"disable-quic=true,force-webrtc-ip-handling-policy=disable_non_proxied_udp")
		// Upstream treats zero as no consecutive-failure stop, but its CLI
		// default is 10. Pass the value explicitly to preserve Uroboros' 0 =
		// unlimited contract.
		args = append(args, "-max-failure-count", strconv.Itoa(o.MaxFailureCount))
		args = append(args, "-page-load-strategy", o.PageLoadStrategy)
		args = append(args, "-dom-wait-time", strconv.Itoa(o.DOMWaitSeconds))
		chromePath = strings.TrimSpace(chromePath)
		args = append(args, "-system-chrome", "-system-chrome-path", chromePath)
	}
	return args, nil
}

func unlimitedInt(value int) int {
	if value == 0 {
		return practicalUnlimitedInt
	}
	return value
}

func appendBool(args []string, enabled bool, flag string) []string {
	if enabled {
		return append(args, flag)
	}
	return args
}
