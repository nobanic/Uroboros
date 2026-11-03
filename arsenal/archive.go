package arsenal

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nobanic/UroborOS/db"
	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"
)

const (
	ArchiveContractVersion = db.ArchiveProviderContractVersion
	ArchiveNoticeVersion   = db.ArchiveProviderNoticeVersion

	archiveProviderHost        = "web.archive.org"
	archiveProviderPath        = "/cdx/search/cdx"
	archiveProjectionVersion   = db.ArchiveProjectionVersion
	archivePageSize            = 500
	archiveMaxDecodedBody      = 4 << 20
	archiveMaxRecordBytes      = 16 << 10
	archiveMaxCursorBytes      = 16 << 10
	archiveRequestTimeout      = 45 * time.Second
	archiveCourtesyInterval    = 5 * time.Second
	archiveMaxTransientRetries = 2
)

var (
	archiveDigits         = regexp.MustCompile(`^[0-9]+$`)
	archiveSafeToken      = regexp.MustCompile(`^[a-z0-9._~-]{1,32}$`)
	archiveSafeExtension  = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)
	archiveSafeDigest     = regexp.MustCompile(`^(?:-|[A-Za-z0-9:+/_=-]{1,160})$`)
	archiveSafeMIME       = regexp.MustCompile(`^(?:-|[A-Za-z0-9.+-]+/[A-Za-z0-9.+-]+)$`)
	archiveSafePathTokens = map[string]bool{
		".well-known": true, "admin": true, "api": true, "assets": true,
		"css": true, "docs": true, "graphql": true, "health": true,
		"images": true, "img": true, "index.html": true, "js": true,
		"login": true, "logout": true, "openapi": true, "robots.txt": true,
		"sitemap.xml": true, "static": true, "swagger": true,
		"v1": true, "v2": true, "v3": true,
	}
	archiveSafeQueryNames = map[string]bool{
		"callback": true, "format": true, "id": true, "lang": true,
		"limit": true, "locale": true, "next": true, "offset": true,
		"order": true, "page": true, "q": true, "query": true,
		"redirect": true, "return": true, "sort": true, "url": true,
	}
	archiveRequiredFields = []string{
		"urlkey", "timestamp", "original", "mimetype", "statuscode", "digest", "length",
	}
	archiveDisallowedPrefixes = []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("224.0.0.0/4"), netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("::/128"), netip.MustParsePrefix("::1/128"),
		netip.MustParsePrefix("100::/64"), netip.MustParsePrefix("2001:db8::/32"),
		netip.MustParsePrefix("fc00::/7"), netip.MustParsePrefix("fe80::/10"),
		netip.MustParsePrefix("ff00::/8"),
	}
)

var (
	errArchiveRedirect  = errors.New("archive provider redirects are disabled")
	errArchiveBodyLimit = errors.New("archive provider response exceeds the decoded body limit")
)

type archiveProviderRequestError struct{ cause error }

func (e archiveProviderRequestError) Error() string { return "archive provider request failed" }
func (e archiveProviderRequestError) Unwrap() error { return e.cause }

type archiveAdmissionRecordError struct{ cause error }

func (e archiveAdmissionRecordError) Error() string {
	return "archive provider admission could not be recorded"
}
func (e archiveAdmissionRecordError) Unwrap() error { return e.cause }

func ArchiveDisclosureNotice(domain string) string {
	return fmt.Sprintf("The query sends the domain %s to Internet Archive (web.archive.org). The service also sees the connection IP address. Results are historical URL metadata; current availability and vulnerability are not verified.", domain)
}

type archiveFetchRequest struct {
	Domain      string
	CaptureFrom string
	CaptureTo   string
	Cursor      string
}

type archiveFetchResponse struct {
	Status      int
	ContentType string
	RetryAfter  string
	Body        []byte
	RouteSHA256 string
	StartedAt   time.Time
	FinishedAt  time.Time
}

type archiveFetchAdmission func(archiveFetchResponse) error

type archivePage struct {
	Observations []db.ArchiveObservationCreate
	Projection   map[string]any
	Cursor       string
	Rows         int
}

func archiveContractSHA256() string {
	return sha256Hex([]byte(strings.Join([]string{
		ArchiveContractVersion,
		"GET", "https://" + archiveProviderHost + ":443" + archiveProviderPath,
		strings.Join(archiveRequiredFields, ","), strconv.Itoa(archivePageSize),
		archiveProjectionVersion, "raw-retention=false",
	}, "\n")))
}

func canonicalArchiveDate(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if !archiveDigits.MatchString(raw) {
		return "", errors.New("capture date must contain only digits")
	}
	layouts := map[int]string{4: "2006", 6: "200601", 8: "20060102", 14: "20060102150405"}
	layout, ok := layouts[len(raw)]
	if !ok {
		return "", errors.New("capture date must be YYYY, YYYYMM, YYYYMMDD or YYYYMMDDhhmmss")
	}
	if _, err := time.Parse(layout, raw); err != nil {
		return "", errors.New("capture date is not a valid calendar value")
	}
	return raw, nil
}

func archiveDateLow(value string) string {
	return value + strings.Repeat("0", 14-len(value))
}

func archiveDateHigh(value string) string {
	return value + strings.Repeat("9", 14-len(value))
}

func normalizeArchiveConfig(input map[string]any) (map[string]any, error) {
	if input == nil {
		input = map[string]any{}
	}
	for key := range input {
		if key != "from" && key != "to" {
			return nil, fmt.Errorf("unknown archive config field %q", key)
		}
	}
	out := map[string]any{"from": "", "to": ""}
	for _, key := range []string{"from", "to"} {
		if raw, ok := input[key]; ok {
			value, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("archive %s must be a string", key)
			}
			value, err := canonicalArchiveDate(value)
			if err != nil {
				return nil, fmt.Errorf("archive %s: %w", key, err)
			}
			out[key] = value
		}
	}
	from, _ := out["from"].(string)
	to, _ := out["to"].(string)
	if from != "" && to != "" && archiveDateLow(from) > archiveDateHigh(to) {
		return nil, errors.New("archive from must not be after to")
	}
	return out, nil
}

func compileArchiveProviderURL(request archiveFetchRequest) (*url.URL, string, error) {
	domain, err := canonicalDiscoveryHost(request.Domain)
	if err != nil || domain != request.Domain {
		return nil, "", errors.New("archive domain must be canonical")
	}
	root, err := publicsuffix.EffectiveTLDPlusOne(domain)
	if err != nil || root != domain {
		return nil, "", errors.New("archive domain must be a registrable root domain")
	}
	values := url.Values{
		"url":           {domain + "/*"},
		"matchType":     {"domain"},
		"output":        {"json"},
		"fl":            {strings.Join(archiveRequiredFields, ",")},
		"showResumeKey": {"true"},
		"limit":         {strconv.Itoa(archivePageSize)},
	}
	if request.CaptureFrom != "" {
		values.Set("from", request.CaptureFrom)
	}
	if request.CaptureTo != "" {
		values.Set("to", request.CaptureTo)
	}
	if request.Cursor != "" {
		if len(request.Cursor) > archiveMaxCursorBytes || strings.IndexFunc(request.Cursor, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return nil, "", errors.New("archive continuation key is invalid")
		}
		values.Set("resumeKey", request.Cursor)
	}
	compiled := &url.URL{Scheme: "https", Host: archiveProviderHost, Path: archiveProviderPath, RawQuery: values.Encode()}
	identity := sha256Hex([]byte(compiled.RequestURI()))
	return compiled, identity, nil
}

func publicArchiveRoutes(ips []net.IP) ([]netip.Addr, error) {
	set := map[netip.Addr]struct{}{}
	for _, raw := range ips {
		addr, ok := netip.AddrFromSlice(raw)
		if !ok {
			return nil, errors.New("archive provider resolver returned an invalid address")
		}
		addr = addr.Unmap()
		if !addr.IsGlobalUnicast() {
			return nil, errors.New("archive provider resolver returned a non-public address")
		}
		for _, prefix := range archiveDisallowedPrefixes {
			if prefix.Contains(addr) {
				return nil, errors.New("archive provider resolver returned a reserved address")
			}
		}
		set[addr] = struct{}{}
	}
	if len(set) == 0 {
		return nil, errors.New("archive provider has no public address")
	}
	out := make([]netip.Addr, 0, len(set))
	for addr := range set {
		out = append(out, addr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out, nil
}

func archiveRouteSHA256(routes []netip.Addr) string {
	values := make([]string, len(routes))
	for i, route := range routes {
		values[i] = route.String()
	}
	return sha256Hex([]byte(strings.Join(values, "\n")))
}

func archiveProviderHTTPClient(routes []netip.Addr) (*http.Client, *http.Transport) {
	dialer := &net.Dialer{Timeout: archiveRequestTimeout}
	return archiveProviderHTTPClientWithDial(routes, dialer.DialContext)
}

func archiveProviderHTTPClientWithDial(
	routes []netip.Addr,
	dialRoute func(context.Context, string, string) (net.Conn, error),
) (*http.Client, *http.Transport) {
	transport := &http.Transport{
		Proxy:                  nil,
		ForceAttemptHTTP2:      true,
		DisableKeepAlives:      true,
		TLSHandshakeTimeout:    archiveRequestTimeout,
		MaxResponseHeaderBytes: 64 << 10,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: archiveProviderHost,
		},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var last error
			for _, route := range routes {
				conn, err := dialRoute(ctx, "tcp", net.JoinHostPort(route.String(), "443"))
				if err == nil {
					return conn, nil
				}
				last = err
			}
			if last == nil {
				last = errors.New("no frozen archive provider route")
			}
			return nil, last
		},
	}
	client := &http.Client{
		Transport:     transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errArchiveRedirect },
	}
	return client, transport
}

func (m *Manager) performArchiveProviderRequest(
	ctx context.Context,
	client *http.Client,
	compiled *url.URL,
	base archiveFetchResponse,
) (response archiveFetchResponse, resultErr error) {
	response = base
	requestCtx, cancel := context.WithTimeout(ctx, archiveRequestTimeout)
	defer cancel()
	defer func() { response.FinishedAt = m.archiveNow() }()
	httpRequest, err := http.NewRequestWithContext(requestCtx, http.MethodGet, compiled.String(), nil)
	if err != nil {
		return response, errors.New("compile archive provider request failed")
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "Uroboros/"+ArchiveContractVersion)
	res, err := client.Do(httpRequest)
	if err != nil {
		if res != nil {
			response.Status = res.StatusCode
			response.ContentType = res.Header.Get("Content-Type")
			response.RetryAfter = res.Header.Get("Retry-After")
			_ = res.Body.Close()
		}
		if errors.Is(err, errArchiveRedirect) {
			return response, errArchiveRedirect
		}
		return response, archiveProviderRequestError{cause: err}
	}
	defer res.Body.Close()
	response.Status = res.StatusCode
	response.ContentType = res.Header.Get("Content-Type")
	response.RetryAfter = res.Header.Get("Retry-After")
	body, err := io.ReadAll(io.LimitReader(res.Body, archiveMaxDecodedBody+1))
	if err != nil {
		return response, errors.New("read archive provider response failed")
	}
	if len(body) > archiveMaxDecodedBody {
		return response, errArchiveBodyLimit
	}
	response.Body = body
	return response, nil
}

func (m *Manager) fetchArchiveProvider(
	ctx context.Context,
	request archiveFetchRequest,
	admit archiveFetchAdmission,
) (archiveFetchResponse, error) {
	now := m.archiveNow()
	response := archiveFetchResponse{StartedAt: now, FinishedAt: now, RouteSHA256: sha256Hex([]byte("unresolved"))}
	compiled, _, err := compileArchiveProviderURL(request)
	if err != nil {
		return response, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, archiveRequestTimeout)
	defer cancel()
	resolve := m.cfg.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]net.IP, error) {
			return net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
	}
	ips, err := resolve(requestCtx, archiveProviderHost)
	if err != nil {
		return response, errors.New("archive provider resolution failed")
	}
	routes, err := publicArchiveRoutes(ips)
	if err != nil {
		return response, err
	}
	response.RouteSHA256 = archiveRouteSHA256(routes)
	response.StartedAt = m.archiveNow()
	response.FinishedAt = response.StartedAt
	if admit == nil {
		return response, errors.New("archive provider admission recorder is unavailable")
	}
	if err := admit(response); err != nil {
		return response, archiveAdmissionRecordError{cause: err}
	}
	var client *http.Client
	var transport *http.Transport
	if m.archiveDial != nil {
		client, transport = archiveProviderHTTPClientWithDial(routes, m.archiveDial)
	} else {
		client, transport = archiveProviderHTTPClient(routes)
	}
	defer transport.CloseIdleConnections()
	return m.performArchiveProviderRequest(requestCtx, client, compiled, response)
}

func canonicalArchiveHost(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if raw == "" || net.ParseIP(strings.Trim(raw, "[]")) != nil {
		return "", errors.New("archive URL host is invalid")
	}
	ascii, err := idna.Lookup.ToASCII(raw)
	if err != nil || ascii == "" || len(ascii) > 253 {
		return "", errors.New("archive URL host is invalid")
	}
	for _, label := range strings.Split(ascii, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", errors.New("archive URL host is invalid")
		}
	}
	return strings.ToLower(ascii), nil
}

func archiveOriginalProjection(raw, domain string) (string, bool, error) {
	if len(raw) == 0 || len(raw) > archiveMaxRecordBytes || strings.IndexFunc(raw, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return "", false, errors.New("archive original URL is malformed")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", false, errors.New("archive original URL must be HTTP(S)")
	}
	host, err := canonicalArchiveHost(parsed.Hostname())
	if err != nil {
		return "", false, err
	}
	if host != domain && !strings.HasSuffix(host, "."+domain) {
		return "", false, errors.New("archive original URL is outside the frozen root domain")
	}
	port := parsed.Port()
	if port != "" {
		value, err := strconv.Atoi(port)
		if err != nil || value < 1 || value > 65535 {
			return "", false, errors.New("archive original URL has an invalid port")
		}
	}
	hostPort := host
	if port != "" && !((parsed.Scheme == "http" && port == "80") || (parsed.Scheme == "https" && port == "443")) {
		hostPort = net.JoinHostPort(host, port)
	}
	reconstruct := parsed.User != nil || parsed.Fragment != ""
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	}
	segments := strings.Split(escapedPath, "/")
	for i, segment := range segments {
		if segment == "" {
			continue
		}
		lower := strings.ToLower(segment)
		if archiveSafeToken.MatchString(lower) && archiveSafePathTokens[lower] {
			if segment != lower {
				reconstruct = true
			}
			segments[i] = lower
			continue
		}
		ext := strings.ToLower(pathExtension(segment))
		if !archiveSafeExtension.MatchString(ext) {
			ext = ""
		}
		segments[i] = "~" + ext
		reconstruct = true
	}
	projection := &url.URL{Scheme: strings.ToLower(parsed.Scheme), Host: hostPort, Path: strings.Join(segments, "/")}
	if parsed.RawQuery != "" {
		reconstruct = true
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil {
			return "", false, errors.New("archive original URL has an invalid query")
		}
		safe := url.Values{}
		for key, values := range query {
			lower := strings.ToLower(key)
			if !archiveSafeToken.MatchString(lower) || !archiveSafeQueryNames[lower] {
				continue
			}
			if len(values) == 0 {
				values = []string{""}
			}
			for range values {
				safe.Add(lower, "REDACTED")
			}
		}
		projection.RawQuery = safe.Encode()
	}
	return projection.String(), reconstruct, nil
}

func pathExtension(segment string) string {
	index := strings.LastIndexByte(segment, '.')
	if index < 0 {
		return ""
	}
	return segment[index:]
}

func archiveCandidateID(key []byte, raw string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(raw))
	return hex.EncodeToString(mac.Sum(nil))
}

func parseArchiveStatus(raw string) (any, error) {
	if raw == "" || raw == "-" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 100 || value > 599 {
		return nil, errors.New("archive capture has an invalid status code")
	}
	return value, nil
}

func parseArchiveLength(raw string) (any, error) {
	if raw == "" || raw == "-" {
		return nil, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return nil, errors.New("archive capture has an invalid length")
	}
	return value, nil
}

func parseArchivePage(
	body []byte,
	domain string,
	identityKey []byte,
	attemptID, rawBodySHA256, scopeHash, configHash string,
	observedAt time.Time,
) (archivePage, error) {
	var rawRows []json.RawMessage
	if len(body) == 0 || json.Unmarshal(body, &rawRows) != nil || len(rawRows) == 0 {
		return archivePage{}, errors.New("archive provider returned an invalid JSON table")
	}
	var header []string
	if len(rawRows[0]) > archiveMaxRecordBytes || json.Unmarshal(rawRows[0], &header) != nil {
		return archivePage{}, errors.New("archive provider returned an invalid header row")
	}
	indexes := map[string]int{}
	for i, field := range header {
		if _, exists := indexes[field]; exists {
			return archivePage{}, errors.New("archive provider returned a duplicate header field")
		}
		indexes[field] = i
	}
	if len(header) != len(archiveRequiredFields) {
		return archivePage{}, errors.New("archive provider schema differs from the pinned contract")
	}
	for _, field := range archiveRequiredFields {
		if _, ok := indexes[field]; !ok {
			return archivePage{}, errors.New("archive provider schema differs from the pinned contract")
		}
	}
	page := archivePage{Observations: []db.ArchiveObservationCreate{}}
	projectionRows := []map[string]any{}
	for rowIndex := 1; rowIndex < len(rawRows); rowIndex++ {
		rawRow := rawRows[rowIndex]
		if len(rawRow) > archiveMaxRecordBytes {
			return archivePage{}, errors.New("archive provider row exceeds the record limit")
		}
		var values []string
		if err := json.Unmarshal(rawRow, &values); err != nil {
			var continuation map[string]json.RawMessage
			var resumeKey string
			if rowIndex != len(rawRows)-1 || json.Unmarshal(rawRow, &continuation) != nil || len(continuation) != 1 ||
				json.Unmarshal(continuation["resumeKey"], &resumeKey) != nil || resumeKey == "" {
				return archivePage{}, errors.New("archive provider returned an unexpected non-record row")
			}
			page.Cursor = resumeKey
			break
		}
		if len(values) == 0 {
			if rowIndex+1 != len(rawRows)-1 {
				return archivePage{}, errors.New("archive provider returned a misplaced continuation separator")
			}
			var cursor []string
			if json.Unmarshal(rawRows[rowIndex+1], &cursor) != nil || len(cursor) != 1 || cursor[0] == "" {
				return archivePage{}, errors.New("archive provider returned an invalid continuation key")
			}
			page.Cursor = cursor[0]
			break
		}
		if len(values) != len(header) {
			return archivePage{}, errors.New("archive provider row does not match the pinned schema")
		}
		if page.Rows >= archivePageSize {
			return archivePage{}, errors.New("archive provider page exceeds the requested record limit")
		}
		get := func(field string) string { return values[indexes[field]] }
		timestamp := get("timestamp")
		if len(timestamp) != 14 || !archiveDigits.MatchString(timestamp) {
			return archivePage{}, errors.New("archive capture has an invalid timestamp")
		}
		if _, err := time.Parse("20060102150405", timestamp); err != nil {
			return archivePage{}, errors.New("archive capture has an invalid timestamp")
		}
		original := get("original")
		projectedURL, reconstruct, err := archiveOriginalProjection(original, domain)
		if err != nil {
			return archivePage{}, err
		}
		mimeType := get("mimetype")
		if !archiveSafeMIME.MatchString(mimeType) {
			return archivePage{}, errors.New("archive capture has an invalid MIME type")
		}
		statusCode, err := parseArchiveStatus(get("statuscode"))
		if err != nil {
			return archivePage{}, err
		}
		digest := get("digest")
		if !archiveSafeDigest.MatchString(digest) {
			return archivePage{}, errors.New("archive capture has an invalid provider digest")
		}
		length, err := parseArchiveLength(get("length"))
		if err != nil {
			return archivePage{}, err
		}
		candidateID := archiveCandidateID(identityKey, original)
		shapeKey := sha256Hex([]byte(projectedURL))
		record := map[string]any{
			"provider": db.ArchiveProviderInternetArchive, "contract_version": ArchiveContractVersion,
			"projection_version": archiveProjectionVersion, "authority": "candidate",
			"target_proxy_verified": false, "candidate_id": candidateID, "shape_key": shapeKey,
			"url": projectedURL, "requires_reconstruction": reconstruct,
			"capture_timestamp": timestamp, "mimetype": mimeType, "status_code": statusCode,
			"provider_digest": digest, "length": length, "observed_at": observedAt.UTC(),
			"provider_exchange_id": attemptID, "raw_page_sha256": rawBodySHA256,
			"row_ordinal": rowIndex, "scope_hash": scopeHash, "config_hash": configHash,
		}
		keyRaw, _ := canonicalJSON(map[string]any{
			"candidate_id": candidateID, "timestamp": timestamp, "digest": digest,
		})
		page.Observations = append(page.Observations, db.ArchiveObservationCreate{
			ObservationKey: sha256Hex(keyRaw), Record: record,
		})
		projectionRows = append(projectionRows, record)
		page.Rows++
	}
	if page.Cursor != "" {
		if len(page.Cursor) > archiveMaxCursorBytes || strings.IndexFunc(page.Cursor, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
			return archivePage{}, errors.New("archive provider continuation key is invalid")
		}
	}
	page.Projection = map[string]any{
		"provider": db.ArchiveProviderInternetArchive, "contract_version": ArchiveContractVersion,
		"projection_version": archiveProjectionVersion, "raw_retained": false,
		"rows": projectionRows, "continuation_sha256": optionalSHA256(page.Cursor),
	}
	return page, nil
}

func optionalSHA256(value string) string {
	if value == "" {
		return ""
	}
	return sha256Hex([]byte(value))
}

func archiveResponseContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && (mediaType == "application/json" || mediaType == "application/x-json")
}

func retryAfterDuration(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if archiveDigits.MatchString(value) {
		seconds, err := strconv.ParseUint(value, 10, 64)
		maxSeconds := uint64((1<<63 - 1) / int64(time.Second))
		if err != nil || seconds > maxSeconds {
			return time.Duration(1<<63 - 1), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delay := when.Sub(now)
		if delay < 0 {
			delay = 0
		}
		return delay, true
	}
	return 0, false
}

func sleepContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Cause(ctx)
	case <-timer.C:
		return nil
	}
}
