package traffic

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
	"golang.org/x/net/idna"
)

const (
	defaultScopedProxyStartTimeout = 5 * time.Second
	scopedBlockHeader              = "X-Uroboros-Discovery-Block"
)

// NetworkScopeSnapshot is the immutable, network-enforceable part of a task's
// scope at the instant a discovery run starts. RootDomains include their
// subdomains. ExactHosts do not. CIDRs authorize IP-literal destinations; known
// names backed by an in-scope address must be included in ExactHosts by the task
// snapshot builder rather than resolved here with a second, raceable DNS lookup.
//
// The slices are normalized and cloned by StartScopedProxy. Mutating the value a
// caller supplied after the run starts cannot expand that run's authority.
type NetworkScopeSnapshot struct {
	RootDomains []string `json:"root_domains,omitempty"`
	ExactHosts  []string `json:"exact_hosts,omitempty"`
	CIDRs       []string `json:"cidrs,omitempty"`
}

// ScopedProxyPolicy describes one run-specific recording proxy. Empty
// AllowedMethods means all HTTP methods. MaxRequests=0 means unlimited; a
// positive value is an optional operator budget, not a product feature lock.
type ScopedProxyPolicy struct {
	RunID          string
	Scope          NetworkScopeSnapshot
	AllowedMethods []string
	MaxRequests    int64
	// ExactEndpoints optionally narrows the network scope to specific HTTP(S)
	// origins and escaped paths. Query strings may vary, but scheme, normalized
	// host, effective port and path must match. Values must not contain userinfo,
	// a query or a fragment. An empty slice retains the broader scope behavior
	// used by Katana and existing Arsenal scanners.
	ExactEndpoints []string
	// ListenHost and AdvertiseHost are used when the owned scanner runs in a
	// separate container. The default remains loopback-only. A non-loopback
	// listener is rejected unless ProxyToken supplies per-run authentication.
	ListenHost    string
	AdvertiseHost string
	ProxyToken    string
	// UpstreamProxyURL is an optional run-owned proxy. Discovery uses an
	// authenticated ScopedSOCKS5 lease here to pin HTTP traffic to the DNS
	// answers and ports frozen for this run. Empty retains the configured global
	// egress proxy behavior.
	UpstreamProxyURL string
}

// ScopedProxyStats is a lock-free snapshot of the proxy's enforcement counters.
// Requests counts decrypted/plain HTTP attempts (CONNECT is excluded). Allowed
// counts requests admitted upstream and therefore budget-consuming.
type ScopedProxyStats struct {
	Requests      int64 `json:"requests"`
	Allowed       int64 `json:"allowed"`
	ScopeBlocked  int64 `json:"scope_blocked"`
	MethodBlocked int64 `json:"method_blocked"`
	BudgetBlocked int64 `json:"budget_blocked"`
	AuthBlocked   int64 `json:"auth_blocked"`
	Captured      int64 `json:"captured"`
}

// ScopedExchange is the minimal, already-recorded evidence projection retained
// by a scoped discovery proxy. The complete request and response remain in the
// Traffic store under TrafficID; this value is intentionally small enough for a
// discovery runner to import assets without duplicating bodies or headers.
type ScopedExchange struct {
	TrafficID string `json:"traffic_id"`
	Method    string `json:"method"`
	URL       string `json:"url"`
	Status    int    `json:"status"`
}

type networkScopeMatcher struct {
	rootDomains map[string]struct{}
	exactHosts  map[string]struct{}
	cidrs       []netip.Prefix
}

type scopedExactEndpoint struct {
	scheme string
	host   string
	port   int
	path   string
}

type scopedExactEndpointMatcher struct {
	endpoints []scopedExactEndpoint
}

// normalizeNetworkScope validates, deduplicates and sorts a snapshot so its
// fingerprint is stable regardless of input order.
func normalizeNetworkScope(in NetworkScopeSnapshot) (NetworkScopeSnapshot, *networkScopeMatcher, error) {
	roots := make(map[string]struct{}, len(in.RootDomains))
	exact := make(map[string]struct{}, len(in.ExactHosts))
	prefixes := make(map[string]netip.Prefix, len(in.CIDRs))

	for _, raw := range in.RootDomains {
		host, ip, err := normalizeScopeHost(raw)
		if err != nil {
			return NetworkScopeSnapshot{}, nil, fmt.Errorf("invalid root domain %q: %w", raw, err)
		}
		if ip.IsValid() {
			return NetworkScopeSnapshot{}, nil, fmt.Errorf("invalid root domain %q: IP literals belong in CIDRs", raw)
		}
		roots[host] = struct{}{}
	}
	for _, raw := range in.ExactHosts {
		host, _, err := normalizeScopeHost(raw)
		if err != nil {
			return NetworkScopeSnapshot{}, nil, fmt.Errorf("invalid exact host %q: %w", raw, err)
		}
		exact[host] = struct{}{}
	}
	for _, raw := range in.CIDRs {
		prefix, err := normalizeScopePrefix(raw)
		if err != nil {
			return NetworkScopeSnapshot{}, nil, fmt.Errorf("invalid CIDR %q: %w", raw, err)
		}
		prefixes[prefix.String()] = prefix
	}

	// Keep the persisted snapshot and its fingerprint semantic rather than
	// provenance-shaped. Assets discovered beneath a root domain or inside an
	// already-authorized CIDR do not widen the network boundary, so retaining
	// those redundant exact entries would make an unchanged scope look new and
	// needlessly restart automatic discovery.
	for root := range roots {
		for parent := range roots {
			if root != parent && strings.HasSuffix(root, "."+parent) {
				delete(roots, root)
				break
			}
		}
	}
	for host := range exact {
		_, ip, err := normalizeScopeHost(host)
		if err != nil {
			return NetworkScopeSnapshot{}, nil, err
		}
		if ip.IsValid() {
			for _, prefix := range prefixes {
				if prefix.Contains(ip) {
					delete(exact, host)
					break
				}
			}
			continue
		}
		for root := range roots {
			if host == root || strings.HasSuffix(host, "."+root) {
				delete(exact, host)
				break
			}
		}
	}
	for canonical, prefix := range prefixes {
		for otherCanonical, other := range prefixes {
			if canonical == otherCanonical || prefix.Addr().BitLen() != other.Addr().BitLen() {
				continue
			}
			if other.Bits() <= prefix.Bits() && other.Contains(prefix.Addr()) {
				delete(prefixes, canonical)
				break
			}
		}
	}

	out := NetworkScopeSnapshot{
		RootDomains: sortedKeys(roots),
		ExactHosts:  sortedKeys(exact),
		CIDRs:       make([]string, 0, len(prefixes)),
	}
	matcher := &networkScopeMatcher{
		rootDomains: roots,
		exactHosts:  exact,
		cidrs:       make([]netip.Prefix, 0, len(prefixes)),
	}
	for canonical, prefix := range prefixes {
		out.CIDRs = append(out.CIDRs, canonical)
		matcher.cidrs = append(matcher.cidrs, prefix)
	}
	sort.Strings(out.CIDRs)
	sort.Slice(matcher.cidrs, func(i, j int) bool {
		return matcher.cidrs[i].String() < matcher.cidrs[j].String()
	})
	if len(out.RootDomains) == 0 && len(out.ExactHosts) == 0 && len(out.CIDRs) == 0 {
		return NetworkScopeSnapshot{}, nil, errors.New("network scope is empty")
	}
	return out, matcher, nil
}

// NormalizeNetworkScope returns the canonical immutable representation and its
// stable fingerprint. Callers persist this exact value with a discovery run;
// enforcement uses the same normalization inside StartScopedProxy.
func NormalizeNetworkScope(in NetworkScopeSnapshot) (NetworkScopeSnapshot, string, error) {
	scope, _, err := normalizeNetworkScope(in)
	if err != nil {
		return NetworkScopeSnapshot{}, "", err
	}
	return scope, scopeFingerprint(scope), nil
}

// AllowsURL reports whether an HTTP(S) URL is authorized by this snapshot. An
// invalid or empty snapshot fails closed.
func (scope NetworkScopeSnapshot) AllowsURL(raw string) bool {
	_, matcher, err := normalizeNetworkScope(scope)
	if err != nil {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && matcher.allowsURL(u)
}

func sortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func normalizeScopePrefix(raw string) (netip.Prefix, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Prefix{}, errors.New("value is empty")
	}
	if addr, err := netip.ParseAddr(strings.Trim(raw, "[]")); err == nil {
		if addr.Zone() != "" {
			return netip.Prefix{}, errors.New("scoped IPv6 zones are not supported")
		}
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return netip.Prefix{}, err
	}
	if prefix.Addr().Zone() != "" {
		return netip.Prefix{}, errors.New("scoped IPv6 zones are not supported")
	}
	if prefix.Addr().Is4In6() && prefix.Bits() >= 96 {
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96)
	}
	return prefix.Masked(), nil
}

// normalizeScopeHost accepts the forms stored in task assets (host, host:port,
// bracketed IPv6, or URL) but deliberately discards the port: Uroboros task
// scope is host/network based. It returns a canonical hostname and, for an IP
// literal, its parsed address.
func normalizeScopeHost(raw string) (string, netip.Addr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", netip.Addr{}, errors.New("value is empty")
	}
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return "", netip.Addr{}, errors.New("cannot parse URL host")
		}
		raw = u.Hostname()
	} else if addr, err := netip.ParseAddr(strings.Trim(raw, "[]")); err == nil {
		if addr.Zone() != "" {
			return "", netip.Addr{}, errors.New("IPv6 zones are not supported")
		}
		addr = addr.Unmap()
		return addr.String(), addr, nil
	} else if host, port, err := net.SplitHostPort(raw); err == nil {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", netip.Addr{}, errors.New("host has an invalid port")
		}
		raw = host
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		raw = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	}

	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if host == "" || strings.IndexFunc(host, unicode.IsSpace) >= 0 || strings.ContainsAny(host, "/\\?#@") {
		return "", netip.Addr{}, errors.New("host is malformed")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if addr.Zone() != "" {
			return "", netip.Addr{}, errors.New("IPv6 zones are not supported")
		}
		addr = addr.Unmap()
		return addr.String(), addr, nil
	}
	if strings.Contains(host, ":") {
		return "", netip.Addr{}, errors.New("host has an invalid port or IPv6 form")
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil || ascii == "" {
		return "", netip.Addr{}, errors.New("host is not a valid DNS name")
	}
	return strings.ToLower(strings.TrimSuffix(ascii, ".")), netip.Addr{}, nil
}

func (m *networkScopeMatcher) allowsHost(raw string) bool {
	host, ip, err := normalizeScopeHost(raw)
	if err != nil {
		return false
	}
	if _, ok := m.exactHosts[host]; ok {
		return true
	}
	if ip.IsValid() {
		for _, prefix := range m.cidrs {
			if prefix.Contains(ip) {
				return true
			}
		}
		return false
	}
	for root := range m.rootDomains {
		if host == root || strings.HasSuffix(host, "."+root) {
			return true
		}
	}
	return false
}

func (m *networkScopeMatcher) allowsURL(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	return m.allowsHost(u.Hostname())
}

func normalizeScopedExactEndpoints(raw []string, scope *networkScopeMatcher) ([]string, *scopedExactEndpointMatcher, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	if scope == nil {
		return nil, nil, errors.New("exact endpoint policy requires a network scope")
	}
	unique := make(map[string]scopedExactEndpoint, len(raw))
	for _, value := range raw {
		endpoint, canonical, err := normalizeScopedExactEndpoint(value)
		if err != nil {
			return nil, nil, err
		}
		if !scope.allowsHost(endpoint.host) {
			return nil, nil, fmt.Errorf("exact endpoint %q is outside the frozen network scope", value)
		}
		unique[canonical] = endpoint
	}
	canonical := make([]string, 0, len(unique))
	for value := range unique {
		canonical = append(canonical, value)
	}
	sort.Strings(canonical)
	matcher := &scopedExactEndpointMatcher{endpoints: make([]scopedExactEndpoint, 0, len(canonical))}
	for _, value := range canonical {
		matcher.endpoints = append(matcher.endpoints, unique[value])
	}
	return canonical, matcher, nil
}

func normalizeScopedExactEndpoint(raw string) (scopedExactEndpoint, string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return scopedExactEndpoint{}, "", fmt.Errorf("invalid exact endpoint %q: %w", raw, err)
	}
	if u == nil || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.RawFragment != "" {
		return scopedExactEndpoint{}, "", fmt.Errorf("exact endpoint %q must not contain opaque data, userinfo, query or fragment", raw)
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	if scheme != "http" && scheme != "https" {
		return scopedExactEndpoint{}, "", fmt.Errorf("exact endpoint %q must use http or https", raw)
	}
	host, _, err := normalizeScopeHost(u.Hostname())
	if err != nil || host == "" {
		return scopedExactEndpoint{}, "", fmt.Errorf("exact endpoint %q has an invalid host", raw)
	}
	port, err := scopedEndpointPort(scheme, u.Port())
	if err != nil {
		return scopedExactEndpoint{}, "", fmt.Errorf("exact endpoint %q: %w", raw, err)
	}
	path, err := normalizeScopedEndpointPath(u)
	if err != nil {
		return scopedExactEndpoint{}, "", fmt.Errorf("exact endpoint %q: %w", raw, err)
	}
	endpoint := scopedExactEndpoint{scheme: scheme, host: host, port: port, path: path}
	canonical := scheme + "\x00" + host + "\x00" + strconv.Itoa(port) + "\x00" + path
	return endpoint, canonical, nil
}

func scopedEndpointPort(scheme, raw string) (int, error) {
	if raw == "" {
		if scheme == "http" {
			return 80, nil
		}
		if scheme == "https" {
			return 443, nil
		}
		return 0, errors.New("unsupported endpoint scheme")
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid endpoint port %q", raw)
	}
	return port, nil
}

func normalizeScopedEndpointPath(u *url.URL) (string, error) {
	if u == nil {
		return "", errors.New("endpoint URL is missing")
	}
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if !strings.HasPrefix(path, "/") || strings.IndexFunc(path, unicode.IsControl) >= 0 {
		return "", errors.New("endpoint path is malformed")
	}
	lower := strings.ToLower(path)
	if strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") {
		return "", errors.New("endpoint path contains an encoded path separator")
	}
	decoded, err := url.PathUnescape(path)
	if err != nil || strings.Contains(decoded, "\\") {
		return "", errors.New("endpoint path has an invalid escape or separator")
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("endpoint path contains an ambiguous dot segment")
		}
	}
	return path, nil
}

func (m *scopedExactEndpointMatcher) allowsURL(u *url.URL) bool {
	if m == nil {
		return true
	}
	if u == nil || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.RawFragment != "" {
		return false
	}
	scheme := strings.ToLower(strings.TrimSpace(u.Scheme))
	host, _, err := normalizeScopeHost(u.Hostname())
	if err != nil {
		return false
	}
	port, err := scopedEndpointPort(scheme, u.Port())
	if err != nil {
		return false
	}
	path, err := normalizeScopedEndpointPath(u)
	if err != nil {
		return false
	}
	for _, endpoint := range m.endpoints {
		if endpoint.scheme == scheme && endpoint.host == host && endpoint.port == port && endpoint.path == path {
			return true
		}
	}
	return false
}

func (m *scopedExactEndpointMatcher) allowsAuthority(raw string) bool {
	if m == nil {
		return true
	}
	hostRaw, portRaw, err := net.SplitHostPort(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	host, _, err := normalizeScopeHost(hostRaw)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	for _, endpoint := range m.endpoints {
		if endpoint.host == host && endpoint.port == port {
			return true
		}
	}
	return false
}

func exactEndpointFingerprint(canonical []string) string {
	if len(canonical) == 0 {
		return ""
	}
	h := sha256.New()
	for _, value := range canonical {
		_, _ = fmt.Fprintf(h, "endpoint\x00%s\x00", value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ExactEndpointPolicyFingerprint validates and fingerprints the same exact
// origin/path restriction that StartScopedProxy will enforce. Callers use it
// to persist the policy in a job before the proxy listener is started.
func ExactEndpointPolicyFingerprint(scope NetworkScopeSnapshot, endpoints []string) (string, error) {
	_, matcher, err := normalizeNetworkScope(scope)
	if err != nil {
		return "", err
	}
	canonical, _, err := normalizeScopedExactEndpoints(endpoints, matcher)
	if err != nil {
		return "", err
	}
	return exactEndpointFingerprint(canonical), nil
}

func scopeFingerprint(scope NetworkScopeSnapshot) string {
	h := sha256.New()
	for _, value := range scope.RootDomains {
		_, _ = fmt.Fprintf(h, "root\x00%s\x00", value)
	}
	for _, value := range scope.ExactHosts {
		_, _ = fmt.Fprintf(h, "host\x00%s\x00", value)
	}
	for _, value := range scope.CIDRs {
		_, _ = fmt.Fprintf(h, "cidr\x00%s\x00", value)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ScopedProxy is a run-specific MITM listener. It is loopback-only by default;
// an explicitly shared listener requires a strong per-run credential. Unlike
// Traffic's general proxy it never learns a transparent-passthrough exception:
// every CONNECT and every decrypted request is checked against the frozen scope.
type ScopedProxy struct {
	traffic     *Traffic
	proxy       *mproxy.Proxy
	runID       string
	listenAddr  string
	dialAddr    string
	proxyURL    string
	scope       NetworkScopeSnapshot
	scopeHash   string
	matcher     *networkScopeMatcher
	exactHash   string
	exact       *scopedExactEndpointMatcher
	done        chan struct{}
	closeOnce   sync.Once
	closeErr    error
	errMu       sync.RWMutex
	runErr      error
	requests    atomic.Int64
	allowed     atomic.Int64
	scopeBlock  atomic.Int64
	methodBlock atomic.Int64
	budgetBlock atomic.Int64
	authBlock   atomic.Int64
	captured    atomic.Int64
	capturedMu  sync.Mutex
	exchanges   []ScopedExchange
}

type scopedProxyGuard struct {
	mproxy.BaseAddon
	owner   *ScopedProxy
	matcher *networkScopeMatcher
	exact   *scopedExactEndpointMatcher
	methods map[string]struct{}
	max     int64
	token   string
}

// StartScopedProxy starts a fail-closed recording proxy. ctx owns the listener
// lifetime; cancellation is equivalent to Close. Readiness is confirmed before
// the method returns. Shared listeners require an authenticated lease.
func (t *Traffic) StartScopedProxy(ctx context.Context, policy ScopedProxyPolicy) (*ScopedProxy, error) {
	if t == nil || t.db == nil {
		return nil, errors.New("traffic store is not open")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.stopping() {
		return nil, errors.New("traffic store is closing")
	}
	if policy.MaxRequests < 0 {
		return nil, errors.New("max requests cannot be negative (use 0 for unlimited)")
	}

	scope, matcher, err := normalizeNetworkScope(policy.Scope)
	if err != nil {
		return nil, err
	}
	methods, err := normalizeAllowedMethods(policy.AllowedMethods)
	if err != nil {
		return nil, err
	}
	exactURLs, exactMatcher, err := normalizeScopedExactEndpoints(policy.ExactEndpoints, matcher)
	if err != nil {
		return nil, err
	}
	listenHost, advertiseHost, token, err := normalizeScopedProxyEndpoint(policy)
	if err != nil {
		return nil, err
	}
	listenAddr, dialAddr, advertisedAddr, err := reserveScopedAddress(listenHost, advertiseHost)
	if err != nil {
		return nil, fmt.Errorf("reserve scoped proxy address: %w", err)
	}
	var runUpstream *url.URL
	if strings.TrimSpace(policy.UpstreamProxyURL) != "" {
		runUpstream, err = ValidateProxyURL(policy.UpstreamProxyURL)
		if err != nil {
			return nil, fmt.Errorf("invalid scoped upstream proxy: %w", err)
		}
	}
	p, err := mproxy.NewProxy(&mproxy.Options{
		Addr:        listenAddr,
		SslInsecure: true,
		CaRootPath:  filepath.Join(t.dir, "_ca"),
	})
	if err != nil {
		return nil, fmt.Errorf("create scoped proxy: %w", err)
	}

	sp := &ScopedProxy{
		traffic:    t,
		proxy:      p,
		runID:      strings.TrimSpace(policy.RunID),
		listenAddr: listenAddr,
		dialAddr:   dialAddr,
		proxyURL:   scopedProxyURL(advertisedAddr, token),
		scope:      scope,
		scopeHash:  scopeFingerprint(scope),
		matcher:    matcher,
		exactHash:  exactEndpointFingerprint(exactURLs),
		exact:      exactMatcher,
		done:       make(chan struct{}),
		exchanges:  make([]ScopedExchange, 0),
	}
	guard := &scopedProxyGuard{
		owner: sp, matcher: matcher, exact: exactMatcher, methods: methods,
		max: policy.MaxRequests, token: token,
	}

	// Never inherit environment proxy variables. If Uroboros has an explicit
	// global egress proxy, read its current value atomically for each connection.
	p.SetUpstreamProxy(func(*http.Request) (*url.URL, error) {
		if runUpstream != nil {
			clone := *runUpstream
			return &clone, nil
		}
		return t.upstream.Load(), nil
	})
	// A scoped proxy must never fall back to a transparent, uninspectable tunnel.
	p.SetShouldInterceptRule(func(*http.Request) bool { return true })
	// This check runs before go-mitmproxy dials a CONNECT/plain-HTTP target. The
	// addon below repeats it against the decrypted inner URL.
	p.SetAuthProxy(guard.authorizeOuter)
	p.AddAddon(guard)
	p.AddAddon(&sink{
		t:          t,
		failClosed: true,
		onRecord:   sp.recordCaptured,
	})

	t.scopedMu.Lock()
	if t.stopping() {
		t.scopedMu.Unlock()
		return nil, errors.New("traffic store is closing")
	}
	t.scoped[sp] = struct{}{}
	t.scopedMu.Unlock()

	go func() {
		runErr := p.Start()
		if errors.Is(runErr, http.ErrServerClosed) {
			runErr = nil
		}
		sp.errMu.Lock()
		sp.runErr = runErr
		sp.errMu.Unlock()
		close(sp.done)
	}()

	startCtx, cancel := context.WithTimeout(ctx, defaultScopedProxyStartTimeout)
	defer cancel()
	if err := sp.waitReady(startCtx); err != nil {
		_ = sp.Close()
		return nil, fmt.Errorf("start scoped proxy: %w", err)
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = sp.Close()
		case <-sp.done:
		}
	}()
	return sp, nil
}

func normalizeScopedProxyEndpoint(policy ScopedProxyPolicy) (listenHost, advertiseHost, token string, err error) {
	listenHost = strings.TrimSpace(policy.ListenHost)
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}
	parsed := net.ParseIP(strings.Trim(listenHost, "[]"))
	if parsed == nil || parsed.To4() == nil || (!parsed.IsLoopback() && !parsed.IsUnspecified()) {
		return "", "", "", errors.New("scoped proxy listen host must be IPv4 loopback or 0.0.0.0")
	}
	advertiseHost = strings.TrimSpace(policy.AdvertiseHost)
	if advertiseHost == "" {
		if parsed.IsUnspecified() {
			return "", "", "", errors.New("shared scoped proxy requires a non-wildcard advertise host")
		}
		advertiseHost = listenHost
	}
	if strings.ContainsAny(advertiseHost, "/\\?#@ \t\r\n") || strings.Contains(advertiseHost, ":") {
		return "", "", "", errors.New("scoped proxy advertise host is invalid")
	}
	token = policy.ProxyToken
	if token != "" && (len(token) < 32 || strings.ContainsAny(token, "\r\n")) {
		return "", "", "", errors.New("scoped proxy token must contain at least 32 characters and no line breaks")
	}
	if !parsed.IsLoopback() && token == "" {
		return "", "", "", errors.New("shared scoped proxy requires a per-run token")
	}
	return listenHost, advertiseHost, token, nil
}

func reserveScopedAddress(listenHost, advertiseHost string) (listenAddr, dialAddr, advertisedAddr string, err error) {
	listener, err := net.Listen("tcp4", net.JoinHostPort(listenHost, "0"))
	if err != nil {
		return "", "", "", err
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return "", "", "", err
	}
	if err := listener.Close(); err != nil {
		return "", "", "", err
	}
	listenAddr = net.JoinHostPort(listenHost, port)
	dialHost := listenHost
	if ip := net.ParseIP(listenHost); ip != nil && ip.IsUnspecified() {
		dialHost = "127.0.0.1"
	}
	return listenAddr, net.JoinHostPort(dialHost, port), net.JoinHostPort(advertiseHost, port), nil
}

func scopedProxyURL(address, token string) string {
	value := &url.URL{Scheme: "http", Host: address}
	if token != "" {
		value.User = url.UserPassword("uroboros", token)
	}
	return value.String()
}

func normalizeAllowedMethods(raw []string) (map[string]struct{}, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make(map[string]struct{}, len(raw))
	for _, method := range raw {
		method = strings.ToUpper(strings.TrimSpace(method))
		if method == "" || !validHTTPToken(method) {
			return nil, fmt.Errorf("invalid allowed HTTP method %q", method)
		}
		out[method] = struct{}{}
	}
	return out, nil
}

func validHTTPToken(value string) bool {
	const separators = "()<>@,;:\\\"/[]?={} \t"
	for _, r := range value {
		if r <= 31 || r >= 127 || strings.ContainsRune(separators, r) {
			return false
		}
	}
	return value != ""
}

func (g *scopedProxyGuard) authorizeOuter(_ http.ResponseWriter, req *http.Request) (bool, error) {
	if req == nil {
		g.owner.scopeBlock.Add(1)
		return false, errors.New("missing proxy request")
	}
	if g.token != "" {
		expected := "Basic " + base64.StdEncoding.EncodeToString([]byte("uroboros:"+g.token))
		actual := req.Header.Get("Proxy-Authorization")
		if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
			g.owner.authBlock.Add(1)
			return false, errors.New("scoped proxy credentials are missing or invalid")
		}
		// Proxy credentials belong only to this listener and must never enter the
		// captured target request or be forwarded upstream.
		req.Header.Del("Proxy-Authorization")
	}
	host := req.Host
	if req.URL != nil && req.URL.Hostname() != "" {
		host = req.URL.Hostname()
	}
	if !g.matcher.allowsHost(host) {
		g.owner.scopeBlock.Add(1)
		return false, fmt.Errorf("destination %q is outside the frozen discovery scope", host)
	}
	if g.exact != nil {
		allowed := false
		if strings.EqualFold(req.Method, http.MethodConnect) {
			authority := req.Host
			if req.URL != nil && req.URL.Host != "" {
				authority = req.URL.Host
			}
			allowed = g.exact.allowsAuthority(authority)
		} else {
			allowed = g.exact.allowsURL(req.URL)
		}
		if !allowed {
			g.owner.scopeBlock.Add(1)
			return false, errors.New("destination is outside the frozen exact endpoint policy")
		}
	}
	if g.max > 0 && g.owner.allowed.Load() >= g.max && strings.EqualFold(req.Method, http.MethodConnect) {
		g.owner.budgetBlock.Add(1)
		return false, errors.New("discovery request budget is exhausted")
	}
	return true, nil
}

func (g *scopedProxyGuard) Requestheaders(f *mproxy.Flow) {
	if f == nil || f.Request == nil {
		return
	}
	// CONNECT has already been checked before any upstream dial. Its URL does not
	// represent target evidence and must not consume the per-request budget.
	if strings.EqualFold(f.Request.Method, http.MethodConnect) {
		return
	}
	g.owner.requests.Add(1)
	if !g.matcher.allowsURL(f.Request.URL) || !g.exact.allowsURL(f.Request.URL) {
		g.owner.scopeBlock.Add(1)
		blockScopedFlow(f, http.StatusForbidden, "scope")
		return
	}
	method := strings.ToUpper(strings.TrimSpace(f.Request.Method))
	if len(g.methods) > 0 {
		if _, ok := g.methods[method]; !ok {
			g.owner.methodBlock.Add(1)
			blockScopedFlow(f, http.StatusMethodNotAllowed, "method")
			return
		}
	}
	if !reserveRequestBudget(&g.owner.allowed, g.max) {
		g.owner.budgetBlock.Add(1)
		blockScopedFlow(f, http.StatusTooManyRequests, "budget")
		return
	}
	if f.Request.Header == nil {
		f.Request.Header = make(http.Header)
	}
	// The recording sink removes this marker before forwarding it and adds the
	// durable exchange id only to the local proxy response.
	f.Request.Header.Set(CaptureRequestHeader, "1")
}

func reserveRequestBudget(allowed *atomic.Int64, max int64) bool {
	if max == 0 {
		allowed.Add(1)
		return true
	}
	for {
		current := allowed.Load()
		if current >= max {
			return false
		}
		if allowed.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func blockScopedFlow(f *mproxy.Flow, status int, reason string) {
	f.Response = &mproxy.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type":    []string{"text/plain; charset=utf-8"},
			scopedBlockHeader: []string{reason},
		},
		Body: []byte("request blocked by Uroboros discovery policy\n"),
	}
}

func (sp *ScopedProxy) waitReady(ctx context.Context) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-sp.done:
			if err := sp.Err(); err != nil {
				return err
			}
			return errors.New("proxy stopped before becoming ready")
		case <-ticker.C:
			conn, err := net.DialTimeout("tcp4", sp.dialAddr, 100*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				return nil
			}
		}
	}
}

func (sp *ScopedProxy) recordCaptured(id string, flow *mproxy.Flow) {
	exchange := ScopedExchange{TrafficID: id}
	if flow != nil && flow.Request != nil {
		exchange.Method = strings.ToUpper(strings.TrimSpace(flow.Request.Method))
		if flow.Request.URL != nil {
			exchange.URL = flow.Request.URL.String()
		}
	}
	if flow != nil && flow.Response != nil {
		exchange.Status = flow.Response.StatusCode
	}
	sp.capturedMu.Lock()
	sp.exchanges = append(sp.exchanges, exchange)
	sp.capturedMu.Unlock()
	sp.captured.Add(1)
}

// ProxyURL is the authenticated HTTP(S)_PROXY value for the owned discovery
// process. The credential is never persisted by the proxy or traffic sink.
func (sp *ScopedProxy) ProxyURL() string { return sp.proxyURL }

func (sp *ScopedProxy) RunID() string { return sp.runID }

func (sp *ScopedProxy) ScopeFingerprint() string { return sp.scopeHash }

// AllowsURL uses the exact matcher frozen when this proxy started.
func (sp *ScopedProxy) AllowsURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && sp.matcher != nil && sp.matcher.allowsURL(u) && sp.exact.allowsURL(u)
}

// ExactEndpointFingerprint identifies the optional origin/path restriction.
// Empty means this proxy uses only its broader frozen network scope.
func (sp *ScopedProxy) ExactEndpointFingerprint() string { return sp.exactHash }

// Scope returns a copy of the exact normalized snapshot enforced by this run.
func (sp *ScopedProxy) Scope() NetworkScopeSnapshot {
	return NetworkScopeSnapshot{
		RootDomains: append([]string(nil), sp.scope.RootDomains...),
		ExactHosts:  append([]string(nil), sp.scope.ExactHosts...),
		CIDRs:       append([]string(nil), sp.scope.CIDRs...),
	}
}

func (sp *ScopedProxy) Stats() ScopedProxyStats {
	return ScopedProxyStats{
		Requests:      sp.requests.Load(),
		Allowed:       sp.allowed.Load(),
		ScopeBlocked:  sp.scopeBlock.Load(),
		MethodBlocked: sp.methodBlock.Load(),
		BudgetBlocked: sp.budgetBlock.Load(),
		AuthBlocked:   sp.authBlock.Load(),
		Captured:      sp.captured.Load(),
	}
}

func (sp *ScopedProxy) CapturedExchangeIDs() []string {
	sp.capturedMu.Lock()
	defer sp.capturedMu.Unlock()
	ids := make([]string, 0, len(sp.exchanges))
	for _, exchange := range sp.exchanges {
		ids = append(ids, exchange.TrafficID)
	}
	return ids
}

// CapturedExchanges returns a copy in capture order. Every TrafficID has already
// been committed to the Traffic index before it appears here.
func (sp *ScopedProxy) CapturedExchanges() []ScopedExchange {
	sp.capturedMu.Lock()
	defer sp.capturedMu.Unlock()
	return append([]ScopedExchange(nil), sp.exchanges...)
}

func (sp *ScopedProxy) Done() <-chan struct{} { return sp.done }

// Err is non-nil only when the underlying listener stopped unexpectedly.
func (sp *ScopedProxy) Err() error {
	sp.errMu.RLock()
	defer sp.errMu.RUnlock()
	return sp.runErr
}

func (sp *ScopedProxy) Close() error {
	sp.closeOnce.Do(func() {
		if err := sp.proxy.Close(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			sp.closeErr = err
		}
		<-sp.done
		sp.traffic.scopedMu.Lock()
		delete(sp.traffic.scoped, sp)
		sp.traffic.scopedMu.Unlock()
	})
	return sp.closeErr
}
