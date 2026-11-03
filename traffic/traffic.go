// Package traffic implements the request-recording subsystem (docs §10): an
// embedded go-mitmproxy proxy whose addon writes every target HTTP exchange into
// a human-browsable file tree, with a sidecar SQLite index for paged queries.
// Full capture, plaintext (no redaction), target HTTP only.
package traffic

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"

	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
	"github.com/nobanic/UroborOS/internal/reversal"
	actool "github.com/nobanic/norma/tool"
	_ "modernc.org/sqlite"
)

const indexSchema = `
CREATE TABLE IF NOT EXISTS exchanges (
  id           TEXT PRIMARY KEY,
  ts           INTEGER,
  host         TEXT,
  method       TEXT,
  url_template TEXT,
  url          TEXT,
  status       INTEGER,
  content_type TEXT,
  req_len      INTEGER,
  resp_len     INTEGER,
  path         TEXT
);
CREATE INDEX IF NOT EXISTS idx_ex_host ON exchanges(host);
CREATE INDEX IF NOT EXISTS idx_ex_tmpl ON exchanges(host, url_template);
CREATE INDEX IF NOT EXISTS idx_ex_ts   ON exchanges(ts);
CREATE INDEX IF NOT EXISTS idx_ex_status ON exchanges(status);
CREATE INDEX IF NOT EXISTS idx_ex_resp   ON exchanges(resp_len);

CREATE TABLE IF NOT EXISTS exchange_bodies (
  id        TEXT PRIMARY KEY,
  req_head  TEXT NOT NULL DEFAULT '',
  req_body  BLOB,
  req_blob  TEXT,
  resp_head TEXT NOT NULL DEFAULT '',
  resp_body BLOB,
  resp_blob TEXT
);

CREATE TABLE IF NOT EXISTS blob_refs (
  hash        TEXT NOT NULL,
  exchange_id TEXT NOT NULL,
  PRIMARY KEY (hash, exchange_id)
);
CREATE INDEX IF NOT EXISTS idx_blob_refs_ex ON blob_refs(exchange_id);
`

// ftsSchema is applied separately from indexSchema: a driver build without FTS5
// must degrade to "no full-text search" rather than take the whole recorder down.
// trigram (not the default unicode61) is required for two reasons this subsystem
// depends on: it matches arbitrary substrings — "ssw0r" finds "P@ssw0rd" — and it
// handles CJK, which unicode61 does not tokenize. Contentless (content=”) keeps
// only the index, since the text itself lives in exchange_bodies; contentless_delete
// lets rows be deleted without replaying the original text back in.
const ftsSchema = `CREATE VIRTUAL TABLE IF NOT EXISTS ex_fts USING fts5(
  content, tokenize='trigram', content='', contentless_delete=1
);`

const (
	// CaptureRequestHeader is an internal opt-in marker used by first-party host
	// tools that need the exact exchange id produced by the recording proxy. The
	// proxy removes it before forwarding the request to the target.
	CaptureRequestHeader = "X-Uroboros-Capture-Request"
	// TrafficIDHeader is added only to the proxy response for opted-in callers.
	// It is never sent by the target and is not persisted in the captured response.
	TrafficIDHeader = "X-Uroboros-Traffic-ID"
	// NoRecordRequestHeader marks an operator setup check that must traverse the
	// real proxy/network path without becoming part of the later agent-visible
	// traffic corpus. The proxy strips it before forwarding to the target.
	NoRecordRequestHeader = "X-Uroboros-No-Record"

	// maxInlineBody is the cutoff between a body stored inline (SQLite column) and
	// one spilled to the content-addressed blob store.
	maxInlineBody = 256 * 1024
	// blobPreview is how much of a spilled body stays inline so readers can tell
	// what it is (JSON shape, SQL dump header, ZIP magic) without fetching it.
	blobPreview = 8 * 1024
	// maxIndexBody caps a single body's contribution to the full-text index. Text
	// is indexed in full below this; binary never reaches the index at all.
	maxIndexBody = 4 * 1024 * 1024
	// minTrigram is the shortest term the trigram tokenizer can match; shorter
	// queries fall back to metadata LIKE.
	minTrigram = 3
	// maxBlobRead caps one traffic_blob read so paging through a large body never
	// floods the agent's context.
	maxBlobRead = 8 * 1024
	// maxInspectBody caps one evidence-bound search/extraction pass. Native HTTP
	// responses are capped at the same 8 MiB, while larger proxy captures report
	// search_complete=false instead of silently pretending the tail was searched.
	maxInspectBody = 8 * 1024 * 1024
	// The remaining limits keep match context useful without turning a minified
	// JavaScript response into another multi-megabyte model result.
	maxInspectPattern  = 512
	maxInspectContext  = 1024
	maxInspectMatches  = 20
	maxRouteCandidates = 200
	// Contract-worklist limits bound a cross-document client-code pass. The tool
	// intentionally scans substantially more than traffic_search returns, but it
	// must still have a predictable CPU, I/O and model-context ceiling.
	defaultContractDocuments = 48
	maxContractDocuments     = 128
	defaultContractBytes     = 32 * 1024 * 1024
	maxContractBytes         = 64 * 1024 * 1024
	defaultContractMatches   = 40
	maxContractMatches       = 80
	maxContractPatterns      = 8
	maxContractReferences    = 128
	// autoVacuumIncremental is SQLite's numeric value for auto_vacuum=incremental.
	autoVacuumIncremental = 2
	// reclaimChunkPages bounds how much index space one locked step returns to the
	// filesystem (pages are 4KiB, so ~32MB). Reclamation holds the write lock, and
	// record() runs before go-mitmproxy replies to the client, so an unbounded
	// pass would stall the very requests being recorded.
	reclaimChunkPages = 8192
	// reclaimMergePages bounds the full-text merge done alongside each chunk.
	// Deleting from a contentless_delete index only writes tombstones; merging is
	// what discards them, and left undone the index grows on every deletion.
	reclaimMergePages = 256
	// reclaimMergeSteps bounds how many merges one reclamation performs. fts5
	// offers no way to ask whether an index has settled — its special INSERT
	// registers a row change of its own, so change counting cannot tell a real
	// merge from a no-op — so the budget is simply spent down, and whatever is
	// left over falls to the next deletion.
	reclaimMergeSteps = 16
	// reclaimMaxSteps backstops the loop. Both merging and incremental_vacuum are
	// documented to stop making progress eventually, but a reclamation that cannot
	// converge must give up rather than spin holding the write lock.
	reclaimMaxSteps = 512
	// reclaimBudget caps one background reclamation. Deleting a busy host can free
	// gigabytes, and the next deletion resumes wherever this one stopped.
	reclaimBudget = 5 * time.Minute
)

// TrafficSearchDescription is persisted into the tool catalog for new and
// upgraded installations. Keep it in the traffic package so the runtime tool
// and the catalog migration cannot drift apart.
const TrafficSearchDescription = "Query target traffic already captured by the recording proxy (host is required; accepts a bare host, host:port, or a full URL, and can be filtered further by a URL substring or a body keyword). When a port is given, only that service's traffic is returned, avoiding cross-contamination between different ports on the same IP. body_contains runs a full-text search over the captured request/response headers and bodies, supporting arbitrary substrings and CJK text (at least 3 characters). Returns only an extremely lightweight index (id/method/url/status/resp_len) with no response content; once the result is non-empty you must verify each request/response with traffic_get, then hand the IDs that truly support the current vulnerability to bind_finding_traffic. Returns only 3 entries by default, at most 10 per page; use page to page through when there are many results."

// Traffic runs the recording proxy and owns the file tree + index.
type Traffic struct {
	dir   string
	addr  string
	db    *sql.DB
	wmu   sync.Mutex // serializes record() vs DeleteHost (incl. blob GC)
	seq   atomic.Int64
	proxy *mproxy.Proxy
	// fts reports whether the full-text index is available. False on a driver
	// build without FTS5: recording and metadata search still work, body search
	// degrades to unsupported rather than erroring.
	fts bool
	// reaping tracks background reclamation of legacy trees and index space, so
	// shutdown and tests can wait for it instead of racing it.
	reaping sync.WaitGroup
	// incrementalVacuum reports whether the index can return freed pages to the
	// filesystem on its own. False on a database created before this was the
	// default: PRAGMA incremental_vacuum is a silent no-op there, so deletions
	// reclaim nothing until an explicit full compaction converts the file.
	incrementalVacuum bool
	// reclaiming keeps a single background reclamation in flight. Concurrent ones
	// would only contend for wmu, never finish sooner.
	reclaiming atomic.Bool
	// closed is shut by Close so a reclamation abandons its remaining budget
	// instead of holding shutdown open for minutes. Nil on the zero value, which
	// stopping() treats as "not closing".
	closed    chan struct{}
	closeOnce sync.Once
	// pass is the set of hosts whose MITM interception failed for a proxy/protocol
	// reason; connections to them are tunneled transparently (fail-open) so the
	// request still reaches the target — unrecorded — instead of being killed.
	pass sync.Map // hostname(string) -> struct{}
	// captureRequested tracks proxy flows whose first-party caller requested the
	// exact persisted exchange id. Keys are go-mitmproxy flow UUID strings.
	captureRequested sync.Map
	// noRecordRequested tracks setup-only flows that must not be persisted.
	// Keys are go-mitmproxy flow UUID strings.
	noRecordRequested sync.Map
	// upstream is the global egress proxy every captured request is forwarded
	// through (nil = dial targets directly). Both the intercepted and the
	// transparent-passthrough paths honor it (go-mitmproxy's getUpstreamConn),
	// so no host escapes it. Hot-swappable at runtime via SetUpstreamProxy.
	upstream atomic.Pointer[url.URL]
	// scoped tracks task/run-specific fail-closed proxy listeners (for example a
	// Katana discovery run). They share this Traffic store and CA, but not the
	// fail-open interception policy of the general recorder. Close stops these
	// listeners before closing the index they write to.
	scopedMu sync.Mutex
	scoped   map[*ScopedProxy]struct{}
	// scopedSOCKS contains short-lived, authenticated TCP leases used by
	// task-bound discovery. They are closed before the Traffic index so no
	// scanner can outlive the authority snapshot that created it.
	scopedSOCKS map[*ScopedSOCKS5]struct{}
}

// Open initializes the traffic tree, blob store and SQLite index under dir.
func Open(dir, addr string) (*Traffic, error) {
	return (trafficStorageBoot{directory: dir, address: addr}).open()
}

// initIndex applies the schema on a single pinned connection. Pinning is what
// makes auto_vacuum reliable: it can only be set while the database still holds
// no tables, and only the VACUUM that follows writes it into the file header —
// two steps a pooled *sql.DB is free to route to different connections, which
// would silently drop the setting.
//
// auto_vacuum=incremental is what lets a deletion hand freed pages back to the
// filesystem. Without it SQLite merely chains them onto its freelist, so the
// index file never shrinks no matter how much traffic is deleted — and since
// every body below maxInlineBody lives in that file, plus a trigram index
// roughly twice the size of the text it covers, a capture-heavy install ends up
// holding gigabytes for traffic it no longer has. On a database that already has
// tables the pragma is a documented no-op, so installs created before this keep
// auto_vacuum=0 until a full compaction converts the file; incrementalVacuum
// records that so reclaim can say so instead of pretending to reclaim.
func (t *Traffic) initIndex() error {
	return t.withIndexLease(func(lease indexLease) error { return lease.initialize(t) })
}

// hostOnly strips an optional :port, so passthrough keys match whether the host
// arrives as "example.com:443" (CONNECT) or "example.com" (request URL).
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// ProxyAddr returns the address workers should set as HTTP(S)_PROXY. A bare
// ":port" means "bind all interfaces" (legacy default), so map it to loopback.
func (t *Traffic) ProxyAddr() string {
	if strings.HasPrefix(t.addr, ":") {
		return "http://127.0.0.1" + t.addr
	}
	return "http://" + t.addr
}

// SetUpstreamProxy points every captured request at a global egress proxy
// (http/https/socks5, optional user:pass in the URL). An empty raw string clears
// it, restoring direct dialing. The change is atomic and takes effect on the next
// connection — no restart, no proxy rebuild. go-mitmproxy dials all three schemes
// itself, so socks5 works uniformly here regardless of the target tool.
func (t *Traffic) SetUpstreamProxy(raw string) error {
	if strings.TrimSpace(raw) == "" {
		t.upstream.Store(nil)
		return nil
	}
	u, err := ValidateProxyURL(raw)
	if err != nil {
		return err
	}
	t.upstream.Store(u)
	return nil
}

// ValidateProxyURL parses and checks a proxy URL (http/https/socks5, optional
// user:pass), returning the parsed URL. Exposed so callers can validate a global
// proxy before persisting it even when the traffic proxy itself is disabled.
func ValidateProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse proxy address %q: %w", raw, err)
	}
	switch u.Scheme {
	case "http", "https", "socks5":
	case "":
		return nil, fmt.Errorf("proxy %q is missing a scheme (use http://, https://, or socks5://)", raw)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use http, https, or socks5)", u.Scheme)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("proxy %q is missing a host address", raw)
	}
	return u, nil
}

// CACertPath returns the PEM CA cert clients must trust to verify HTTPS through
// the MITM proxy (go-mitmproxy writes it here on first start).
func (t *Traffic) CACertPath() string {
	return filepath.Join(t.dir, "_ca", "mitmproxy-ca-cert.pem")
}

// Start runs the proxy (blocking); run in a goroutine.
func (t *Traffic) Start() error { return t.proxy.Start() }

// Close waits for background tree reclamation to finish before closing the
// index, so shutdown never leaves a goroutine unlinking files out from under a
// removed data directory. Index-space reclamation is signalled to stop first:
// it holds a whole minutes-long budget, and finishing it is never worth delaying
// shutdown for — the next deletion resumes it.
func (t *Traffic) Close() error { return closeTrafficStorage(t) }

// stopping reports whether Close has been called. A nil channel (the zero value)
// is never ready, so this reads as "not closing" without a separate guard.
func (t *Traffic) stopping() bool {
	select {
	case <-t.closed:
		return true
	default:
		return false
	}
}
func (t *Traffic) DB() *sql.DB { return t.db }

// HasExchange verifies that a correlation id was durably committed by this
// recorder. Callers should not trust a response header alone: an untrusted target
// can emit arbitrary headers too.
func (t *Traffic) HasExchange(id string) (bool, error) {
	var one int
	err := t.db.QueryRow(`SELECT 1 FROM exchanges WHERE id=? LIMIT 1`, strings.TrimSpace(id)).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ResponseHead returns only the durable response-head projection for one
// exchange.  It intentionally never reads response body columns, blob files or
// legacy response.http files, so passive consumers cannot accidentally turn a
// header-only operation into body access.
func (t *Traffic) ResponseHead(id string) (ExchangeMeta, string, error) {
	var meta ExchangeMeta
	var head string
	id = strings.TrimSpace(id)
	if t == nil || t.db == nil || id == "" {
		return meta, "", errors.New("traffic response head requires a durable exchange id")
	}
	err := t.db.QueryRow(`SELECT e.id,e.ts,e.host,e.method,e.url_template,e.url,e.status,
e.content_type,e.resp_len,e.path,b.resp_head
FROM exchanges e JOIN exchange_bodies b ON b.id=e.id WHERE e.id=?`, id).Scan(
		&meta.ID, &meta.TS, &meta.Host, &meta.Method, &meta.URLTemplate, &meta.URL,
		&meta.Status, &meta.ContentType, &meta.RespLen, &meta.Path, &head,
	)
	if errors.Is(err, sql.ErrNoRows) {
		var exists int
		if lookupErr := t.db.QueryRow(`SELECT 1 FROM exchanges WHERE id=?`, id).Scan(&exists); lookupErr == nil {
			return ExchangeMeta{}, "", errors.New("legacy exchange response head is unavailable without body-file access")
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return ExchangeMeta{}, "", lookupErr
		}
		return ExchangeMeta{}, "", fmt.Errorf("traffic exchange %s was not found", id)
	}
	if err != nil {
		return ExchangeMeta{}, "", err
	}
	if strings.TrimSpace(head) == "" {
		return ExchangeMeta{}, "", errors.New("traffic exchange has no durable response head")
	}
	return meta, head, nil
}

// sink is the go-mitmproxy addon that records completed exchanges.
type sink struct {
	mproxy.BaseAddon
	t          *Traffic
	failClosed bool
	onRecord   func(id string, f *mproxy.Flow)
}

func (s *sink) Requestheaders(f *mproxy.Flow) {
	if f == nil || f.Request == nil || f.Request.Header == nil {
		return
	}
	if strings.TrimSpace(f.Request.Header.Get(NoRecordRequestHeader)) != "" {
		// CONNECT is proxy-only. The inner request carries a separate marker and
		// flow id, so tracking CONNECT would leak a stale entry.
		f.Request.Header.Del(NoRecordRequestHeader)
		// Scoped discovery is evidence-mandatory. A target-controlled browser
		// request must not be able to spoof the general proxy's operator-only
		// setup marker and suppress its own evidence.
		if !s.failClosed {
			if strings.EqualFold(f.Request.Method, http.MethodConnect) {
				return
			}
			s.t.noRecordRequested.Store(f.Id.String(), struct{}{})
			return
		}
		// Otherwise continue to the capture marker installed by scopedProxyGuard.
	}
	if strings.TrimSpace(f.Request.Header.Get(CaptureRequestHeader)) == "" {
		return
	}
	// Never leak the recorder's coordination marker to the target.
	f.Request.Header.Del(CaptureRequestHeader)
	// HTTPS clients also place the marker on the proxy-only CONNECT request so
	// SetShouldInterceptRule can fail closed before TLS interception begins. The
	// CONNECT flow itself is not target evidence; the inner HTTP flow gets its own
	// marker and UUID.
	if strings.EqualFold(f.Request.Method, http.MethodConnect) {
		return
	}
	s.t.captureRequested.Store(f.Id.String(), struct{}{})
}

func (s *sink) Response(f *mproxy.Flow) {
	if f.Request == nil || f.Response == nil {
		return
	}
	if _, skip := s.t.noRecordRequested.LoadAndDelete(f.Id.String()); skip {
		return
	}
	_, captureRequired := s.t.captureRequested.LoadAndDelete(f.Id.String())
	// A scoped proxy synthesizes local block responses for rejected requests.
	// Those are policy events, not target evidence, and must never be recorded or
	// imported as verified discoveries. Every admitted scoped request carries the
	// capture marker installed by scopedProxyGuard.
	if s.failClosed && !captureRequired {
		return
	}
	id := s.t.record(f)
	if captureRequired && id != "" {
		if f.Response.Header == nil {
			f.Response.Header = make(http.Header)
		}
		// record ran first, so this local coordination header is not part of the
		// target response stored as evidence.
		f.Response.Header.Set(TrafficIDHeader, id)
	}
	if id != "" && s.onRecord != nil {
		s.onRecord(id, f)
	}
}

// RequestError fires when a request through an established MITM tunnel fails. If
// the failure looks proxy/protocol-caused (h2 quirks, HEAD-with-body, protocol
// errors) — not a plain target-unreachable error — we flag the host for
// transparent passthrough so future requests to it succeed instead of dying.
func (s *sink) RequestError(f *mproxy.Flow, err error) {
	if f != nil {
		if _, skipped := s.t.noRecordRequested.LoadAndDelete(f.Id.String()); skipped {
			return
		}
		if _, required := s.t.captureRequested.LoadAndDelete(f.Id.String()); required {
			// Evidence-required callers fail closed: do not teach the proxy to let a
			// later retry reach this host through an unrecorded tunnel.
			return
		}
	}
	if s.failClosed {
		return
	}
	s.t.maybePassthrough(f, err)
}

// maybePassthrough marks a host to be tunneled transparently on the next
// connection, but only for errors the proxy itself caused — a target that is
// simply down/filtered would fail without us too, and must stay MITM'd+recorded.
func (t *Traffic) maybePassthrough(f *mproxy.Flow, err error) {
	if err == nil || f == nil || f.Request == nil || f.Request.URL == nil || !proxyCausedErr(err) {
		return
	}
	host := f.Request.URL.Hostname()
	if host == "" {
		return
	}
	if _, loaded := t.pass.LoadOrStore(host, struct{}{}); !loaded {
		log.Printf("[traffic] MITM with %s failed; switching to transparent passthrough (this host now dials the target directly and is no longer recorded, but requests still go through): %v", host, err)
	}
}

// proxyCausedErr reports whether err indicates the interception layer (not the
// target) is at fault — HTTP/2 handling, HEAD-with-body, or a protocol violation.
func proxyCausedErr(err error) bool {
	s := strings.ToLower(err.Error())
	for _, p := range []string{"head request", "http2", "http/2", "protocol error", "protocol_error", "malformed"} {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// record persists one exchange entirely in SQLite: metadata, bodies and the
// full-text index. Nothing is written to a per-request directory — only bodies
// above maxInlineBody spill to the content-addressed blob store.
func (t *Traffic) record(f *mproxy.Flow) string {
	t.wmu.Lock()
	defer t.wmu.Unlock()
	write := captureExchangeWrite{store: t}
	return write.persist(f)
}

// storedBody is one body after the inline/spill decision: inline is what goes in
// the SQLite column (the whole body, or a preview when spilled), hash names the
// blob when it spilled, and index is the text handed to FTS (empty for binary).
type storedBody struct {
	inline []byte
	hash   string
	index  string
}

// spill decides where one body lives. Small bodies stay inline. Large ones are
// written to the content-addressed store and keep a readable preview inline so a
// reader can identify them without fetching the blob. Either way, text bodies
// are handed to the full-text index in full (up to maxIndexBody) — indexing is
// independent of where the bytes end up.
func (t *Traffic) spill(body []byte, contentType string) storedBody {
	return (captureBodyWrite{directory: t.dir, contentType: contentType, bytes: body}).materialize()
}

// binaryTypes are content-type prefixes whose bodies are never worth indexing or
// previewing as text. Anything not listed is treated as text (with a NUL-byte
// check as backstop), so unusual-but-searchable types like application/sql or a
// bare text/* are not silently dropped from the index.
var binaryTypes = []string{
	"image/", "audio/", "video/", "font/",
	"application/octet-stream", "application/zip", "application/gzip",
	"application/x-gzip", "application/x-tar", "application/x-7z-compressed",
	"application/x-rar", "application/pdf", "application/x-msdownload",
	"application/vnd.android.package-archive", "application/java-archive",
	"application/wasm", "application/x-shockwave-flash",
}

func isBinaryBody(contentType string, body []byte) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	for _, p := range binaryTypes {
		if strings.HasPrefix(ct, p) {
			return true
		}
	}
	// Backstop for mislabeled or absent content types: real text does not carry
	// NUL bytes, so a NUL in the head of the body means binary regardless.
	return bytes.IndexByte(clipBytes(body, 512), 0) >= 0
}

// binaryTag describes a spilled binary body in one line, including the leading
// magic bytes so a reader can recognize the format without downloading it.
func binaryTag(contentType string, body []byte) string {
	ct := strings.TrimSpace(contentType)
	if ct == "" {
		ct = "application/octet-stream"
	}
	magic := hex.EncodeToString(clipBytes(body, 4))
	return fmt.Sprintf("[binary %s, %d bytes, magic=%s]", ct, len(body), magic)
}

func clipBytes(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

// truncateUTF8 cuts b to at most n bytes without splitting a multi-byte rune,
// so a preview never ends in half a CJK character.
func truncateUTF8(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	b = b[:n]
	// A rune is at most 4 bytes, so backing off 3 bytes always finds the boundary
	// (unless the input was already invalid UTF-8, in which case we keep the cut).
	for i := 0; i < utf8.UTFMax-1 && len(b) > 0; i++ {
		if r, size := utf8.DecodeLastRune(b); r != utf8.RuneError || size != 1 {
			break
		}
		b = b[:len(b)-1]
	}
	return string(b)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func headerLines(h map[string][]string) string {
	var b strings.Builder
	for k, vs := range h {
		for _, v := range vs {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// requestHeaderLines restores the HTTP Host header, which net/http stores on
// Request.Host rather than in Header. Keeping it in request.http makes the raw
// capture complete and directly replayable.
func requestHeaderLines(req *mproxy.Request) string {
	headers := req.Header.Clone()
	headers.Del("Host")
	host := req.URL.Host
	if raw := req.Raw(); raw != nil && strings.TrimSpace(raw.Host) != "" {
		host = raw.Host
	}
	var b strings.Builder
	if host = strings.TrimSpace(host); host != "" {
		b.WriteString("Host: ")
		b.WriteString(host)
		b.WriteByte('\n')
	}
	b.WriteString(headerLines(headers))
	return b.String()
}

func sanitize(s string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "?", "_", "*", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	out := r.Replace(s)
	if out == "" || out == "_" {
		return "root"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

var blobHashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// blobPath locates a stored blob. Current writes use one bucket level; blobs
// written before that change used two, so both layouts are probed rather than
// migrated.
func (t *Traffic) blobPath(hash string) (string, error) {
	return (captureReader{store: t}).blobPath(hash)
}

// Blob opens a spilled body for streaming; the caller must close the file.
// Streaming matters here: the driver exposes no incremental BLOB API, so keeping
// large bodies on disk is what lets them be served without loading them whole.
func (t *Traffic) Blob(hash string) (*os.File, int64, error) {
	return (captureReader{store: t}).openBlob(hash)
}

// BlobRange reads at most length bytes of a blob from offset, and reports the
// blob's total size. Used by the agent tool to page through a large body without
// pulling all of it into the model's context.
func (t *Traffic) BlobRange(hash string, offset, length int64) (data []byte, total int64, err error) {
	return (captureReader{store: t}).blobRange(hash, offset, length)
}

// inspectBody is the evidence boundary for response analysis tools. It resolves
// one captured exchange id to the complete inline body or the content-addressed
// blob it references. Very large bodies are read only to maxInspectBody and are
// explicitly marked incomplete; the full-body SHA-256 is still known for blobs
// because their immutable content address is the hash.
type inspectBody struct {
	Data        []byte
	Total       int64
	SHA256      string
	BlobHash    string
	ContentType string
	Source      string
	Complete    bool
}

// BodyRange is a bounded byte range from one immutable captured request or
// response body. Data is intentionally excluded from JSON so callers must
// decide explicitly how it is rendered or transformed.
type BodyRange struct {
	Data        []byte `json:"-"`
	Offset      int64  `json:"offset"`
	Length      int64  `json:"length"`
	Total       int64  `json:"total"`
	SHA256      string `json:"sha256"`
	BlobHash    string `json:"blob_hash,omitempty"`
	ContentType string `json:"content_type,omitempty"`
	Source      string `json:"source"`
}

// ReadBodyRange resolves an exchange/body reference and returns only the
// requested bounded bytes. Blob-backed captures can be addressed at arbitrary
// offsets without loading the full response into memory.
func (t *Traffic) ReadBodyRange(id, side string, offset, length int64) (BodyRange, error) {
	return (captureReader{store: t}).bodyRange(id, side, offset, length)
}

func (t *Traffic) loadInspectBody(id, side string) (inspectBody, error) {
	return (captureReader{store: t}).inspect(id, side)
}

func headerValue(head, name string) string {
	for _, line := range strings.Split(strings.ReplaceAll(head, "\r\n", "\n"), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(key), name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitRawHTTP(raw []byte) (head, body []byte) {
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		return raw[:i], raw[i+4:]
	}
	if i := bytes.Index(raw, []byte("\n\n")); i >= 0 {
		return raw[:i], raw[i+2:]
	}
	return raw, nil
}

// BodyMatch is one bounded match within immutable captured evidence. Offsets
// are byte offsets into the original body, which makes the result reproducible
// even for minified single-line JavaScript.
type BodyMatch struct {
	Side           string `json:"side"`
	ByteStart      int    `json:"byte_start"`
	ByteEnd        int    `json:"byte_end"`
	Line           int    `json:"line"`
	ByteColumn     int    `json:"byte_column"`
	Before         string `json:"before"`
	Match          string `json:"match"`
	MatchTruncated bool   `json:"match_truncated"`
	After          string `json:"after"`
}

type BodyEvidence struct {
	Side       string `json:"side"`
	BodySHA256 string `json:"body_sha256"`
	BlobSHA256 string `json:"blob_sha256,omitempty"`
	BodyBytes  int64  `json:"body_bytes"`
	Source     string `json:"source"`
	Complete   bool   `json:"complete"`
}

type BodyMatchResult struct {
	ExchangeID       string         `json:"exchange_id"`
	Pattern          string         `json:"pattern"`
	Regex            bool           `json:"regex"`
	CaseSensitive    bool           `json:"case_sensitive"`
	Matches          []BodyMatch    `json:"matches"`
	Evidence         []BodyEvidence `json:"evidence"`
	MatchesTruncated bool           `json:"matches_truncated"`
	SearchComplete   bool           `json:"search_complete"`
}

// SearchBodyMatches searches request and/or response bodies from one captured
// exchange and returns only bounded surrounding context. Go's RE2 engine keeps
// user-supplied regular expressions linear-time and free of catastrophic
// backtracking.
func (t *Traffic) SearchBodyMatches(id, side, pattern string, regexMode, caseSensitive bool, contextBytes, maxMatches int) (BodyMatchResult, error) {
	return (trafficInspection{store: t}).matches(id, side, pattern, regexMode, caseSensitive, contextBytes, maxMatches)
}

type RouteCandidate struct {
	Route       string `json:"route"`
	Occurrences int    `json:"occurrences"`
	ByteOffset  int    `json:"byte_offset"`
	Context     string `json:"context"`
}

type RouteExtractionResult struct {
	ExchangeID          string           `json:"exchange_id"`
	BodySHA256          string           `json:"body_sha256"`
	BlobSHA256          string           `json:"blob_sha256,omitempty"`
	BodyBytes           int64            `json:"body_bytes"`
	Source              string           `json:"source"`
	Prefixes            []string         `json:"prefixes"`
	Candidates          []RouteCandidate `json:"candidates"`
	CandidatesTruncated bool             `json:"candidates_truncated"`
	SearchComplete      bool             `json:"search_complete"`
}

// ContractWorklistDocument records exactly which immutable response body was
// inspected by BuildContractWorklist. BytesScanned can be smaller than
// BodyBytes when the explicit cross-document budget is exhausted.
type ContractWorklistDocument struct {
	ExchangeID     string `json:"exchange_id"`
	URL            string `json:"url"`
	ContentType    string `json:"content_type,omitempty"`
	BodySHA256     string `json:"body_sha256"`
	BlobSHA256     string `json:"blob_sha256,omitempty"`
	BodyBytes      int64  `json:"body_bytes"`
	BytesScanned   int    `json:"bytes_scanned"`
	Source         string `json:"source"`
	SearchComplete bool   `json:"search_complete"`
}

// ContractWorklistMatch is a bounded caller/route match tied to one captured
// response. Match is normalized for common JavaScript slash escapes; RawMatch
// and the surrounding context remain byte-for-byte evidence from the capture.
type ContractWorklistMatch struct {
	ExchangeID string `json:"exchange_id"`
	URL        string `json:"url"`
	Pattern    string `json:"pattern"`
	ByteStart  int    `json:"byte_start"`
	ByteEnd    int    `json:"byte_end"`
	Line       int    `json:"line"`
	ByteColumn int    `json:"byte_column"`
	Before     string `json:"before"`
	Match      string `json:"match"`
	RawMatch   string `json:"raw_match"`
	After      string `json:"after"`
	BodySHA256 string `json:"body_sha256"`
}

// ContractWorklistReference is one JavaScript resource referenced by captured
// client code. Captured references point back to their newest traffic row;
// pending references are the bounded, deterministic fetch worklist.
type ContractWorklistReference struct {
	URL                    string `json:"url"`
	DiscoveredInExchangeID string `json:"discovered_in_exchange_id"`
	ByteOffset             int    `json:"byte_offset"`
	CapturedExchangeID     string `json:"captured_exchange_id,omitempty"`
}

type ContractWorklistSkip struct {
	ExchangeID string `json:"exchange_id"`
	URL        string `json:"url"`
	Reason     string `json:"reason"`
}

type ContractWorklistResult struct {
	Host                string                      `json:"host"`
	Patterns            []string                    `json:"patterns"`
	Regex               bool                        `json:"regex"`
	CaseSensitive       bool                        `json:"case_sensitive"`
	Documents           []ContractWorklistDocument  `json:"documents"`
	DocumentsScanned    int                         `json:"documents_scanned"`
	DocumentsTruncated  bool                        `json:"documents_truncated"`
	BytesScanned        int64                       `json:"bytes_scanned"`
	ByteBudget          int64                       `json:"byte_budget"`
	Matches             []ContractWorklistMatch     `json:"matches"`
	MatchesTruncated    bool                        `json:"matches_truncated"`
	CapturedReferences  []ContractWorklistReference `json:"captured_references"`
	PendingFetches      []ContractWorklistReference `json:"pending_fetches"`
	ReferencesTruncated bool                        `json:"references_truncated"`
	Skipped             []ContractWorklistSkip      `json:"skipped,omitempty"`
	SearchComplete      bool                        `json:"search_complete"`
}

type contractPattern struct {
	text string
	re   *regexp.Regexp
}

var clientScriptReferenceRE = regexp.MustCompile(`(?i)(?:https?:)?//[^\s"'<>\\]{1,500}\.(?:m?js)(?:[?#][^\s"'<>\\]*)?|(?:\.\.?/|/)?[A-Za-z0-9_@%+~.-][A-Za-z0-9_@%+~./-]{0,480}\.(?:m?js)(?:[?#][A-Za-z0-9_@%+~.,=&/-]*)?`)

// BuildContractWorklist performs one bounded, deterministic pass over captured
// HTML/JavaScript for a target authority. It finds route/symbol callers across
// bodies and turns referenced-but-not-yet-captured JavaScript into an explicit
// fetch queue. It is read-only: the caller decides which pending URLs to fetch
// through the normal evidence-recording HTTP tool, then reruns this pass.
func (t *Traffic) BuildContractWorklist(host string, patterns []string, regexMode, caseSensitive bool, contextBytes, maxDocuments, maxTotalBytes, maxMatches int) (ContractWorklistResult, error) {
	return (trafficInspection{store: t}).worklist(host, patterns, regexMode, caseSensitive, contextBytes, maxDocuments, maxTotalBytes, maxMatches)
}

func compileContractPatterns(patterns []string, regexMode, caseSensitive bool) ([]contractPattern, []string, error) {
	if len(patterns) > maxContractPatterns {
		return nil, nil, fmt.Errorf("at most %d patterns are allowed", maxContractPatterns)
	}
	compiled := make([]contractPattern, 0, len(patterns))
	clean := make([]string, 0, len(patterns))
	seen := make(map[string]struct{})
	for _, pattern := range patterns {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			return nil, nil, errors.New("patterns must not contain an empty string")
		}
		if len(pattern) > maxInspectPattern {
			return nil, nil, fmt.Errorf("pattern exceeds %d bytes", maxInspectPattern)
		}
		if _, exists := seen[pattern]; exists {
			continue
		}
		seen[pattern] = struct{}{}
		expr := pattern
		if !regexMode {
			expr = regexp.QuoteMeta(pattern)
		}
		if !caseSensitive {
			expr = "(?i)" + expr
		}
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid RE2 pattern %q: %w", pattern, err)
		}
		if re.MatchString("") {
			return nil, nil, fmt.Errorf("pattern %q must not match an empty string", pattern)
		}
		compiled = append(compiled, contractPattern{text: pattern, re: re})
		clean = append(clean, pattern)
	}
	return compiled, clean, nil
}

// clientArtifactRows returns newest captures first, with HTML prioritized so a
// root document's script graph remains visible even after many API calls.
func (t *Traffic) clientArtifactRows(host, port string, limit int) ([]ExchangeMeta, bool, error) {
	q := `SELECT id,ts,host,method,url_template,url,status,content_type,resp_len,path FROM (
SELECT id,ts,host,method,url_template,url,status,content_type,resp_len,path,
ROW_NUMBER() OVER (PARTITION BY url ORDER BY ts DESC, id DESC) AS newest
FROM exchanges WHERE host=?`
	args := []any{host}
	if port != "" {
		authority := net.JoinHostPort(host, port)
		q += ` AND (url LIKE ? OR url LIKE ? OR url LIKE ?)`
		args = append(args, "%://"+authority+"/%", "%://"+authority+"?%", "%://"+authority)
	}
	// Only successful responses can be client artifacts. Without the status gate,
	// every captured 302/404 HTML error page for an API-looking URL crowds real
	// bundles out of the bounded corpus (especially behind a global login hook).
	q += ` AND status>=200 AND status<300
AND (lower(content_type) LIKE '%javascript%' OR lower(content_type) LIKE '%ecmascript%'
OR lower(content_type) LIKE '%text/html%' OR lower(content_type) LIKE '%application/xhtml%'
OR lower(url) LIKE '%.js%' OR lower(url) LIKE '%.mjs%')
) WHERE newest=1
ORDER BY CASE WHEN lower(content_type) LIKE '%html%' THEN 0 ELSE 1 END, ts DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := t.db.Query(q, args...)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	out := make([]ExchangeMeta, 0, limit)
	for rows.Next() {
		var m ExchangeMeta
		if err := rows.Scan(&m.ID, &m.TS, &m.Host, &m.Method, &m.URLTemplate, &m.URL, &m.Status, &m.ContentType, &m.RespLen, &m.Path); err != nil {
			return nil, false, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(out) >= limit
	if truncated {
		out = out[:limit-1]
	}
	return out, truncated, nil
}

func looksLikeClientScriptURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	path := strings.ToLower(u.Path)
	return strings.HasSuffix(path, ".js") || strings.HasSuffix(path, ".mjs")
}

func canonicalHTTPURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Fragment = ""
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	return u.String()
}

func resolveClientScriptReference(baseRaw, ref string) (string, bool) {
	base, err := url.Parse(baseRaw)
	if err != nil || base.Host == "" {
		return "", false
	}
	ref = strings.ReplaceAll(strings.TrimSpace(ref), "&amp;", "&")
	relative, err := url.Parse(ref)
	if err != nil {
		return "", false
	}
	resolved := base.ResolveReference(relative)
	resolved.Fragment = ""
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", false
	}
	return resolved.String(), true
}

func contractAuthorityMatches(rawURL, host, port string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.EqualFold(u.Hostname(), host) {
		return false
	}
	if port == "" {
		return true
	}
	actual := u.Port()
	if actual == "" {
		if u.Scheme == "http" {
			actual = "80"
		} else if u.Scheme == "https" {
			actual = "443"
		}
	}
	return actual == port
}

func (t *Traffic) capturedExchangeForURL(rawURL string) (string, error) {
	var id string
	err := t.db.QueryRow(`SELECT id FROM exchanges WHERE url=? ORDER BY ts DESC, id DESC LIMIT 1`, rawURL).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// ExtractRouteCandidates finds endpoint-looking strings in one captured
// response. Common JavaScript slash escapes (\/, \x2f, \u002f) are normalized
// only for matching; byte offsets and context still refer to the original body.
func (t *Traffic) ExtractRouteCandidates(id string, prefixes []string, contextBytes, maxCandidates int) (RouteExtractionResult, error) {
	return (trafficInspection{store: t}).routes(id, prefixes, contextBytes, maxCandidates)
}

func normalizeSlashEscapes(raw []byte) ([]byte, []int) {
	out := make([]byte, 0, len(raw))
	boundaries := make([]int, 1, len(raw)+1)
	for i := 0; i < len(raw); {
		width := 1
		value := raw[i]
		if raw[i] == '\\' && i+1 < len(raw) && raw[i+1] == '/' {
			value, width = '/', 2
		} else if raw[i] == '\\' && i+3 < len(raw) && (raw[i+1] == 'x' || raw[i+1] == 'X') && raw[i+2] == '2' && (raw[i+3] == 'f' || raw[i+3] == 'F') {
			value, width = '/', 4
		} else if raw[i] == '\\' && i+5 < len(raw) && (raw[i+1] == 'u' || raw[i+1] == 'U') && raw[i+2] == '0' && raw[i+3] == '0' && raw[i+4] == '2' && (raw[i+5] == 'f' || raw[i+5] == 'F') {
			value, width = '/', 6
		}
		out = append(out, value)
		i += width
		boundaries = append(boundaries, i)
	}
	return out, boundaries
}

func textSlice(data []byte, start, end int) string {
	start = maxInt(0, minInt(start, len(data)))
	end = maxInt(start, minInt(end, len(data)))
	for start < end && !utf8.RuneStart(data[start]) {
		start++
	}
	for end > start && end < len(data) && !utf8.RuneStart(data[end]) {
		end--
	}
	return string(data[start:end])
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// ExchangeMeta is one row of the index (returned by Search).
type ExchangeMeta struct {
	ID          string `json:"id"`
	TS          int64  `json:"ts"`
	Host        string `json:"host"`
	Method      string `json:"method"`
	URLTemplate string `json:"url_template"`
	URL         string `json:"url"`
	Status      int    `json:"status"`
	ContentType string `json:"content_type"`
	RespLen     int    `json:"resp_len"`
	Path        string `json:"path"`
}

// ftsFilter builds the SQL condition restricting rows to those whose indexed
// text matches term. ok is false when full-text search cannot serve the term —
// no FTS index on this build, or fewer than three characters, which the trigram
// tokenizer cannot match — leaving callers to fall back to metadata matching.
func (t *Traffic) ftsFilter(term string) (cond string, arg any, ok bool) {
	term = strings.TrimSpace(term)
	if !t.fts || utf8.RuneCountInString(term) < minTrigram {
		return "", nil, false
	}
	return `rowid IN (SELECT rowid FROM ex_fts WHERE ex_fts MATCH ?)`, ftsQuote(term), true
}

// ftsQuote wraps a term as an FTS5 string literal so that punctuation and query
// operators inside it (quotes, AND/OR/NEAR, *, ^) are matched literally instead
// of being parsed as query syntax.
func ftsQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// PageQuery bundles the optional filters and sort order for Page. Every filter
// is combined with AND; an empty string skips that filter, and RespMin/RespMax
// of -1 skip the response-size bounds. An empty Sort/Order defaults to
// newest-first by timestamp.
type PageQuery struct {
	Host    string // host substring
	Method  string // exact method (case-insensitive)
	Query   string // broad search box: metadata columns OR captured text
	Body    string // content box: captured request/response text only (full-text)
	Path    string // url_template substring — the vulnerability's path
	Status  string // exact code ("404") or class bucket ("4xx")
	RespMin int64  // minimum resp_len, or -1 when unset
	RespMax int64  // maximum resp_len, or -1 when unset
	Sort    string // ts | status | resp_len (default ts)
	Order   string // asc | desc (default desc)
}

// sortColumns whitelists the columns Page may order by, so the caller-supplied
// Sort can never reach the SQL as anything but one of these fixed names.
var sortColumns = map[string]string{"ts": "ts", "status": "status", "resp_len": "resp_len"}

// statusFilter turns a status token into a SQL condition: an exact code ("404")
// matches that status, an "Nxx" class ("4xx") matches the whole hundreds band.
// ok is false for an empty or unrecognized token.
func statusFilter(s string) (cond string, args []any, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return "", nil, false
	}
	if len(s) == 3 && s[0] >= '1' && s[0] <= '5' && s[1] == 'x' && s[2] == 'x' {
		base := int(s[0]-'0') * 100
		return "status>=? AND status<?", []any{base, base + 100}, true
	}
	if n, err := strconv.Atoi(s); err == nil {
		return "status=?", []any{n}, true
	}
	return "", nil, false
}

// assembleRaw rebuilds one side's raw HTTP text: head, blank line, body. A body
// that spilled to the blob store shows its inline preview followed by the blob
// pointer, so the reader can see what it is and fetch the rest by hash.
func assembleRaw(head string, body []byte, blob sql.NullString, total int) string {
	var b strings.Builder
	b.WriteString(head)
	b.WriteByte('\n')
	b.Write(body)
	if blob.Valid && blob.String != "" {
		fmt.Fprintf(&b, "\n…[truncated] @blob sha256:%s (len=%d)", blob.String, total)
	}
	return b.String()
}

// HostCount is one distinct recorded host plus its exchange count.
type HostCount struct {
	Host  string `json:"host"`
	Count int    `json:"count"`
}

// DeleteHost removes every recorded exchange whose host contains the given
// substring — mirroring the UI's host filter, so what you filtered is what gets
// deleted. Everything lives in SQLite, so the deletion is one transaction across
// the index, bodies, full-text index and blob references. Content-addressed
// blobs are shared across hosts and so are garbage-collected afterwards rather
// than deleted by host. Returns rows deleted.
func (t *Traffic) DeleteHost(host string) (int64, error) {
	return (trafficPurge{traffic: t}).host(host)
}

// DeleteAll removes every recorded exchange — the page's clear-everything
// action. It differs from the host-scoped deletions in two ways. It sweeps host
// directories without consulting the index, because "clear everything" should
// leave nothing behind and a directory whose rows are already gone would
// otherwise survive. And it ends with a full compaction: VACUUM costs what it
// keeps, so an emptied index is the one moment it is free, and it is also the
// only way to switch on auto_vacuum for a database created without it.
//
// Evidence already bound to a finding is untouched. Those bodies were copied
// into the evidence store when they were bound, precisely so that disposable
// traffic could be cleared without taking proof with it.
//
// Returns the number of exchanges deleted and how many bytes of index the
// compaction handed back.
func (t *Traffic) DeleteAll() (deleted int64, reclaimed int64, err error) {
	return (trafficPurge{traffic: t}).all()
}

// allHostTrees lists every host directory left by the pre-SQLite layout. Unlike
// hostTrees it does not go through the index, so it also finds directories whose
// rows are already gone. Underscore-prefixed entries are the store's own
// (_index, _blobs, _ca, _delete_staging) and are never hosts.
func (t *Traffic) allHostTrees() ([]string, error) {
	entries, err := os.ReadDir(t.dir)
	if err != nil {
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), "_") {
			dirs = append(dirs, filepath.Join(t.dir, e.Name()))
		}
	}
	return dirs, nil
}

// compactIndex rewrites the index into a fresh file, which is what actually
// returns its pages to the filesystem. VACUUM's runtime and temporary space
// scale with the content it keeps, so this is only reached right after DeleteAll
// has emptied the index — never as a routine step. It doubles as the conversion
// path for a database created before auto_vacuum=incremental: that pragma only
// takes effect through the VACUUM that follows it, and both must run on the same
// connection. Callers must hold wmu.
func (t *Traffic) compactIndex() error {
	return t.withIndexLease(func(lease indexLease) error { return lease.compact(t) })
}

// deleteWhere removes every trace of the exchanges matching the condition:
// full-text index rows, bodies, blob references, and finally the index rows.
// Order matters — each sub-select reads exchanges, so that table is emptied last.
func (t *Traffic) deleteWhere(tx *sql.Tx, where string, args ...any) (int64, error) {
	return (trafficPurge{traffic: t}).deleteRows(tx, where, args...)
}

// hostTrees returns the on-disk directory of every host matching the condition.
// Exchanges recorded since bodies moved into SQLite have no directory at all, so
// most of these paths simply will not exist — staging skips those. The lookup is
// deliberately not restricted to rows with a path: a host whose index rows are
// already gone can still have an orphaned directory, and deleting the host should
// take that with it.
func (t *Traffic) hostTrees(where string, args ...any) ([]string, error) {
	rows, err := t.db.Query(`SELECT DISTINCT host FROM exchanges WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var dirs []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		dirs = append(dirs, filepath.Join(t.dir, sanitize(h)))
	}
	return dirs, rows.Err()
}

type stagedTrafficPath struct {
	source string
	staged string
}

type hostDeleteStageJournal struct {
	Version   int                   `json:"version"`
	ArchiveID int64                 `json:"archive_id,omitempty"`
	TaskID    int64                 `json:"task_id,omitempty"`
	Hosts     []string              `json:"hosts,omitempty"`
	Moves     []hostDeleteStageMove `json:"moves"`
}

type hostDeleteStageMove struct {
	Source string `json:"source"`
	Staged string `json:"staged"`
}

const hostDeleteStageJournalName = "journal.json"

// stageTrees moves host directories aside onto the same filesystem. The rename is
// atomic and instant, which buys two things the unlink cannot: the deletion stays
// reversible until the transaction commits, and the tree stops being visible to
// blob reference collection right away (legacyBlobRefs skips underscore-prefixed
// directories, so anything under _delete_staging is already out of the live set).
// An empty stageDir is returned when there was nothing to stage.
func (t *Traffic) stageTrees(dirs []string) (stageDir string, moves []stagedTrafficPath, err error) {
	return t.stageTreesForArchive(dirs, nil, 0, 0)
}

func (t *Traffic) stageTreesForArchive(dirs, hosts []string, archiveID, taskID int64) (stageDir string, moves []stagedTrafficPath, err error) {
	return (hostTreeStaging{traffic: t, dirs: dirs, hosts: hosts, archiveID: archiveID, taskID: taskID}).run()
}

func writeHostDeleteStageJournal(path string, journal hostDeleteStageJournal) error {
	return (hostDeleteJournal{path: path, journal: journal}).write()
}

// restoreTrees puts staged directories back where they came from, newest move
// first, and drops the staging directory once everything is home.
func restoreTrees(stageDir string, moves []stagedTrafficPath) error {
	return errors.Join(reversal.Paths(stageDir, moves, func(move stagedTrafficPath) (string, string) { return move.source, move.staged }, 0, "traffic")...)
}

// reapStage unlinks a committed staging directory in the background. This is the
// step that used to stall the recorder: a legacy tree mirrors the URL path per
// request and can hold hundreds of thousands of small files, and the whole unlink
// ran while the write lock was held. The rows are already gone by now, so losing
// this to a shutdown leaves garbage under _delete_staging, never inconsistent
// state.
func (t *Traffic) reapStage(stageDir string) { (hostStageReaper{traffic: t, stageDir: stageDir}).run() }

// DeleteHostsExact removes recorded exchanges for a set of EXACT hosts (the
// batch path for the page's multi-select delete): index rows + each host's file
// tree, then one blob-GC pass. Exact match — unlike DeleteHost's substring —
// so picking "api.example.com" never sweeps "api.example.com.cn". Returns rows
// deleted. Duplicate hosts are harmless (idempotent deletes, single tree pass).
func (t *Traffic) DeleteHostsExact(hosts []string) (int64, error) {
	return (hostDeleteRequest{traffic: t, hosts: hosts}).remove()
}

// HostDeleteStage keeps the SQLite delete transaction open so a task deletion
// can be coordinated with PostgreSQL: the traffic side is staged here and only
// committed once the caller's own transaction has succeeded. The Traffic write
// lock is held until Commit or Rollback, so no recorder can add rows or a blob
// reference for a host that is mid-deletion. Rolling back is just a transaction
// rollback now that nothing is moved on disk.
type HostDeleteStage struct {
	traffic  *Traffic
	tx       *sql.Tx
	stageDir string
	moves    []stagedTrafficPath
	deleted  int64
	done     bool
}

// Deleted reports the number of exchange rows selected by this stage.
func (s *HostDeleteStage) Deleted() int64 {
	if s == nil {
		return 0
	}
	return s.deleted
}

// StageDeleteHostsExact prepares a reversible exact-host deletion. Callers must
// finish every successful stage with Commit or Rollback.
func (t *Traffic) StageDeleteHostsExact(hosts []string) (*HostDeleteStage, error) {
	return t.stageDeleteHostsExact(hosts, 0, 0)
}

// StageDeleteHostsExactForArchive ties the reversible traffic deletion to a
// persistent task archive. Startup recovery uses archiveCommitted to decide
// whether an interrupted stage must be completed or rolled back.
func (t *Traffic) StageDeleteHostsExactForArchive(hosts []string, archiveID, taskID int64) (*HostDeleteStage, error) {
	if archiveID <= 0 || taskID <= 0 {
		return nil, errors.New("archive and task ids must be positive")
	}
	return t.stageDeleteHostsExact(hosts, archiveID, taskID)
}

func (t *Traffic) stageDeleteHostsExact(hosts []string, archiveID, taskID int64) (*HostDeleteStage, error) {
	return (hostDeleteRequest{traffic: t, hosts: hosts, archiveID: archiveID, taskID: taskID}).prepare()
}

// RecoverHostDeleteStages resolves filesystem moves left by a process exit.
// SQLite rolls open transactions back on restart, while the supplied callback
// identifies task archives whose PostgreSQL compaction already committed.
func (t *Traffic) RecoverHostDeleteStages(archiveCommitted func(int64, int64) (bool, error)) error {
	return (hostStageRecovery{traffic: t, resolve: archiveCommitted}).run()
}

func (t *Traffic) hostsHaveNoExchanges(hosts []string) (bool, error) {
	for _, host := range hosts {
		var count int
		if err := t.db.QueryRow(`SELECT count(*) FROM exchanges WHERE host=?`, host).Scan(&count); err != nil {
			return false, err
		}
		if count > 0 {
			return false, nil
		}
	}
	return true, nil
}

func (t *Traffic) deleteArchivedHosts(hosts []string) error {
	tx, err := t.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	for _, host := range hosts {
		if _, err := t.deleteWhere(tx, `host=?`, host); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func pathWithin(root, candidate string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	candidateAbs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(rootAbs, candidateAbs)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Rollback discards the staged deletion, leaving the traffic store untouched.
func (s *HostDeleteStage) Rollback() error {
	if s == nil || s.done {
		return nil
	}
	return s.rollbackLocked()
}

func (s *HostDeleteStage) rollbackLocked() error { return (hostStageCompletion{stage: s}).rollback() }

// Commit makes the staged deletion permanent. SQLite is committed only after
// the caller has committed its PostgreSQL task deletion.
func (s *HostDeleteStage) Commit() error { return (hostStageCompletion{stage: s}).commit() }

// indexBytes is the index's real footprint on disk: the database plus its
// write-ahead log and shared-memory file, since those are what an operator sees
// the data directory holding.
func (t *Traffic) indexBytes() int64 { return indexFootprint(t.dir) }

// reclaim hands space freed by a deletion back to the filesystem. Deleting rows
// only makes them invisible: SQLite chains their pages onto a freelist, and
// deleting from the contentless_delete full-text index writes tombstones rather
// than removing the postings they hide. Neither shrinks a single byte on disk,
// and since bodies below maxInlineBody live in that same file, an install that
// captures heavily ends up holding far more than the traffic it still has.
//
// The work is done in the background, in bounded steps that drop the write lock
// between them, because it is proportional to what was deleted — freeing several
// gigabytes under one lock would stall record(), which go-mitmproxy calls before
// it replies to the client, and so would stall the requests being recorded.
// Safe to call with wmu held: the background pass simply waits for it.
//
// Best-effort throughout. Failing to reclaim costs disk space, never
// correctness, so errors are logged and the next deletion resumes the work.
func (t *Traffic) reclaim() { beginIndexReclamation(t) }

// reclaimChunk does one bounded step and reports whether it made progress, which
// is what the caller loops on. merges carries the remaining full-text merge
// budget and is spent down here. Callers must hold wmu.
func (t *Traffic) reclaimChunk(ctx context.Context, conn *sql.Conn, merges *int) (bool, error) {
	return (indexLease{context: ctx, connection: conn}).reclaimChunk(t, merges)
}

// gcBlobs removes blobs that no remaining exchange references. Live references
// come from blob_refs, so collection is one query plus a sweep of the blob
// directory — no exchange body is ever read. Emptied bucket directories are
// removed as well; the previous file-scanning collector deleted only files and
// left the buckets behind permanently. Best-effort: walk errors only skip the
// affected entries. Callers must hold wmu so this never races a concurrent
// record() writing a fresh blob + reference.
func (t *Traffic) gcBlobs() error { return collectTrafficBlobs(t) }

// legacyBlobRefs adds hashes referenced by pre-SQLite exchanges, whose bodies are
// still .http files on disk holding "@blob sha256:<hex>" pointers. Without this
// the first collection after the upgrade would delete blobs that history still
// points at. Skipped entirely once no legacy rows remain — the steady state — so
// the tree walk is transitional rather than a permanent cost.
func (t *Traffic) legacyBlobRefs(refs map[string]struct{}) error {
	return blobReferences(refs).addLegacy(t)
}

// query returns one page of exchange metadata filtered by host and/or a url
// substring. Default page size is intentionally small (3) to keep tool results
// lightweight and capped at 10; page is 0-based (page*limit offset).
func (t *Traffic) query(host, contains, bodyContains string, page, limit int) ([]ExchangeMeta, error) {
	return (captureSearch{store: t}).page(host, contains, bodyContains, page, limit)
}

// normalizeSearchHost accepts the forms agents commonly have at hand while
// keeping the stored host (URL.Hostname()) as the canonical host key. A port,
// when supplied, is applied to the URL authority so captures from different
// services on the same IP cannot be mixed.
func normalizeSearchHost(raw string) (host, port string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("host is a required parameter")
	}
	if strings.Contains(raw, "://") {
		u, parseErr := url.Parse(raw)
		if parseErr != nil || u.Host == "" {
			return "", "", fmt.Errorf("cannot parse host: %q", raw)
		}
		host, port = u.Hostname(), u.Port()
	} else if h, p, splitErr := net.SplitHostPort(raw); splitErr == nil {
		host, port = h, p
	} else if strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]") {
		host = strings.TrimSuffix(strings.TrimPrefix(raw, "["), "]")
	} else {
		host = raw
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" {
		return "", "", fmt.Errorf("cannot parse host: %q", raw)
	}
	if port != "" {
		p, parseErr := strconv.Atoi(port)
		if parseErr != nil || p < 1 || p > 65535 {
			return "", "", fmt.Errorf("invalid port: %q", port)
		}
		port = strconv.Itoa(p)
	}
	return strings.ToLower(host), port, nil
}

// Tools exposes traffic lookup to work agents so they query already-captured
// traffic instead of re-curling the same resource (token + dedup win).
func (t *Traffic) Tools() []actool.CoreTool { return trafficCommandCatalog(t) }

// SeedToolMetas returns the traffic tools built on a ZERO receiver, for seeding the
// tools catalog (metadata only — Name/Description/InputSchema). The handlers close
// over the nil receiver but are never invoked on this instance, so it is safe.
func SeedToolMetas() []actool.CoreTool { return (&Traffic{}).Tools() }

func clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [truncated, %d bytes total; full content in the traffic file tree] ...", len(s))
}
