package traffic

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xproxy "golang.org/x/net/proxy"
)

const (
	socksVersion       = 5
	socksUserPass      = 2
	socksNoAcceptable  = 0xff
	socksConnect       = 1
	socksAddressIPv4   = 1
	socksAddressDomain = 3
	socksAddressIPv6   = 4
)

// ScopedTCPRoute freezes one logical host/port to a bounded set of concrete
// addresses. Host may itself be an IP literal. The SOCKS lease never performs
// DNS and rejects every tuple absent from this list.
type ScopedTCPRoute struct {
	Host      string
	Port      int
	Addresses []string
}

type ScopedSOCKS5Policy struct {
	RunID          string
	Routes         []ScopedTCPRoute
	ListenHost     string
	AdvertiseHost  string
	Username       string
	Password       string
	ConnectTimeout time.Duration
}

type ScopedSOCKS5Stats struct {
	Attempts     int64 `json:"attempts"`
	Allowed      int64 `json:"allowed"`
	Connected    int64 `json:"connected"`
	AuthBlocked  int64 `json:"auth_blocked"`
	ScopeBlocked int64 `json:"scope_blocked"`
	DialFailed   int64 `json:"dial_failed"`
}

type scopedTCPRoute struct {
	addresses []netip.Addr
	next      atomic.Uint64
}

// ScopedSOCKS5 is a run-owned, authenticated CONNECT-only TCP gateway. It is
// deliberately not a general SOCKS server: BIND, UDP, runtime DNS and unknown
// destinations are rejected before any upstream dial.
type ScopedSOCKS5 struct {
	traffic   *Traffic
	runID     string
	listener  net.Listener
	proxyURL  string
	username  string
	password  string
	timeout   time.Duration
	routes    map[string]*scopedTCPRoute
	routeHash string

	ctx       context.Context
	cancel    context.CancelFunc
	done      chan struct{}
	closeOnce sync.Once
	closeErr  error
	connMu    sync.Mutex
	conns     map[net.Conn]struct{}
	wg        sync.WaitGroup

	attempts     atomic.Int64
	allowed      atomic.Int64
	connected    atomic.Int64
	authBlocked  atomic.Int64
	scopeBlocked atomic.Int64
	dialFailed   atomic.Int64
}

func (t *Traffic) StartScopedSOCKS5(ctx context.Context, policy ScopedSOCKS5Policy) (*ScopedSOCKS5, error) {
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
	listenHost, advertiseHost, err := normalizeSOCKSEndpoint(policy.ListenHost, policy.AdvertiseHost)
	if err != nil {
		return nil, err
	}
	username := strings.TrimSpace(policy.Username)
	if username == "" || len(username) > 255 || strings.ContainsAny(username, "\x00\r\n") {
		return nil, errors.New("scoped SOCKS username is missing or invalid")
	}
	if len(policy.Password) < 32 || len(policy.Password) > 255 || strings.ContainsAny(policy.Password, "\x00\r\n") {
		return nil, errors.New("scoped SOCKS password must contain 32..255 characters and no control lines")
	}
	routes, err := normalizeScopedTCPRoutes(policy.Routes)
	if err != nil {
		return nil, err
	}
	timeout := policy.ConnectTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if timeout > time.Minute {
		return nil, errors.New("scoped SOCKS connect timeout exceeds one minute")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort(listenHost, "0"))
	if err != nil {
		return nil, fmt.Errorf("listen for scoped SOCKS: %w", err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return nil, err
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	sp := &ScopedSOCKS5{
		traffic: t, runID: strings.TrimSpace(policy.RunID), listener: listener,
		proxyURL: (&url.URL{Scheme: "socks5", Host: net.JoinHostPort(advertiseHost, port),
			User: url.UserPassword(username, policy.Password)}).String(),
		username: username, password: policy.Password, timeout: timeout, routes: routes,
		routeHash: scopedTCPRoutesFingerprint(routes),
		ctx:       leaseCtx, cancel: cancel, done: make(chan struct{}), conns: make(map[net.Conn]struct{}),
	}
	t.scopedMu.Lock()
	if t.stopping() {
		t.scopedMu.Unlock()
		cancel()
		_ = listener.Close()
		return nil, errors.New("traffic store is closing")
	}
	t.scopedSOCKS[sp] = struct{}{}
	t.scopedMu.Unlock()
	go sp.serve()
	go func() {
		<-leaseCtx.Done()
		_ = sp.Close()
	}()
	return sp, nil
}

func normalizeSOCKSEndpoint(listenHost, advertiseHost string) (string, string, error) {
	listenHost = strings.TrimSpace(listenHost)
	if listenHost == "" {
		listenHost = "127.0.0.1"
	}
	ip := net.ParseIP(strings.Trim(listenHost, "[]"))
	if ip == nil || ip.To4() == nil || (!ip.IsLoopback() && !ip.IsUnspecified()) {
		return "", "", errors.New("scoped SOCKS listen host must be IPv4 loopback or 0.0.0.0")
	}
	advertiseHost = strings.TrimSpace(advertiseHost)
	if advertiseHost == "" {
		if ip.IsUnspecified() {
			return "", "", errors.New("shared scoped SOCKS requires a non-wildcard advertise host")
		}
		advertiseHost = listenHost
	}
	if strings.ContainsAny(advertiseHost, "/\\?#@ \t\r\n") || strings.Contains(advertiseHost, ":") {
		return "", "", errors.New("scoped SOCKS advertise host is invalid")
	}
	return listenHost, advertiseHost, nil
}

func normalizeScopedTCPRoutes(input []ScopedTCPRoute) (map[string]*scopedTCPRoute, error) {
	out := make(map[string]*scopedTCPRoute)
	for _, item := range input {
		host, _, err := normalizeScopeHost(item.Host)
		if err != nil {
			return nil, fmt.Errorf("invalid scoped TCP route host %q: %w", item.Host, err)
		}
		if item.Port < 1 || item.Port > 65535 {
			return nil, fmt.Errorf("invalid scoped TCP route port %d", item.Port)
		}
		key := scopedTCPRouteKey(host, item.Port)
		route := out[key]
		if route == nil {
			route = &scopedTCPRoute{}
			out[key] = route
		}
		seen := make(map[netip.Addr]struct{}, len(route.addresses))
		for _, address := range route.addresses {
			seen[address] = struct{}{}
		}
		for _, raw := range item.Addresses {
			address, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(raw), "[]"))
			if err != nil || address.Zone() != "" {
				return nil, fmt.Errorf("invalid scoped TCP route address %q", raw)
			}
			address = address.Unmap()
			if _, ok := seen[address]; !ok {
				route.addresses = append(route.addresses, address)
				seen[address] = struct{}{}
			}
		}
	}
	for key, route := range out {
		if len(route.addresses) == 0 {
			return nil, fmt.Errorf("scoped TCP route %q has no addresses", key)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("scoped TCP routes are empty")
	}
	return out, nil
}

func scopedTCPRoutesFingerprint(routes map[string]*scopedTCPRoute) string {
	type routeDocument struct {
		Host      string   `json:"host"`
		Port      int      `json:"port"`
		Addresses []string `json:"addresses"`
	}
	document := make([]routeDocument, 0, len(routes))
	for key, route := range routes {
		parts := strings.SplitN(key, "\x00", 2)
		port, _ := strconv.Atoi(parts[1])
		addresses := make([]string, 0, len(route.addresses))
		for _, address := range route.addresses {
			addresses = append(addresses, address.String())
		}
		sort.Strings(addresses)
		document = append(document, routeDocument{Host: parts[0], Port: port, Addresses: addresses})
	}
	sort.Slice(document, func(i, j int) bool {
		if document[i].Host == document[j].Host {
			return document[i].Port < document[j].Port
		}
		return document[i].Host < document[j].Host
	})
	raw, _ := json.Marshal(document)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// ScopedTCPRoutesFingerprint validates and fingerprints the exact logical
// host/port-to-address routes enforced by a scoped SOCKS lease.
func ScopedTCPRoutesFingerprint(input []ScopedTCPRoute) (string, error) {
	routes, err := normalizeScopedTCPRoutes(input)
	if err != nil {
		return "", err
	}
	return scopedTCPRoutesFingerprint(routes), nil
}

func scopedTCPRouteKey(host string, port int) string {
	return host + "\x00" + strconv.Itoa(port)
}

func (sp *ScopedSOCKS5) serve() {
	defer close(sp.done)
	for {
		conn, err := sp.listener.Accept()
		if err != nil {
			if sp.ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			sp.closeErr = err
			return
		}
		sp.connMu.Lock()
		sp.conns[conn] = struct{}{}
		sp.connMu.Unlock()
		sp.wg.Add(1)
		go func() {
			defer sp.wg.Done()
			defer func() {
				sp.connMu.Lock()
				delete(sp.conns, conn)
				sp.connMu.Unlock()
				_ = conn.Close()
			}()
			sp.handle(conn)
		}()
	}
}

func (sp *ScopedSOCKS5) handle(client net.Conn) {
	sp.attempts.Add(1)
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))
	reader := bufio.NewReader(client)
	if !sp.authenticate(reader, client) {
		return
	}
	host, port, ok := readSOCKSRequest(reader, client)
	if !ok {
		return
	}
	route := sp.routes[scopedTCPRouteKey(host, port)]
	if route == nil {
		sp.scopeBlocked.Add(1)
		writeSOCKSReply(client, 2, netip.AddrPort{})
		return
	}
	sp.allowed.Add(1)
	index := route.next.Add(1) - 1
	address := route.addresses[index%uint64(len(route.addresses))]
	dialCtx, cancel := context.WithTimeout(sp.ctx, sp.timeout)
	upstream, err := sp.traffic.dialScopedTarget(dialCtx, net.JoinHostPort(address.String(), strconv.Itoa(port)))
	cancel()
	if err != nil {
		sp.dialFailed.Add(1)
		writeSOCKSReply(client, 5, netip.AddrPort{})
		return
	}
	defer upstream.Close()
	sp.connected.Add(1)
	if tcp, ok := upstream.LocalAddr().(*net.TCPAddr); ok {
		if local, ok := netip.AddrFromSlice(tcp.IP); ok {
			writeSOCKSReply(client, 0, netip.AddrPortFrom(local.Unmap(), uint16(tcp.Port)))
		} else {
			writeSOCKSReply(client, 0, netip.AddrPort{})
		}
	} else {
		writeSOCKSReply(client, 0, netip.AddrPort{})
	}
	_ = client.SetDeadline(time.Time{})
	copyDone := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(upstream, reader)
		if tcp, ok := upstream.(*net.TCPConn); ok {
			_ = tcp.CloseWrite()
		}
		copyDone <- struct{}{}
	}()
	_, _ = io.Copy(client, upstream)
	<-copyDone
}

func (sp *ScopedSOCKS5) authenticate(reader *bufio.Reader, client net.Conn) bool {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != socksVersion || header[1] == 0 {
		return false
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return false
	}
	found := false
	for _, method := range methods {
		if method == socksUserPass {
			found = true
		}
	}
	if !found {
		sp.authBlocked.Add(1)
		_, _ = client.Write([]byte{socksVersion, socksNoAcceptable})
		return false
	}
	if _, err := client.Write([]byte{socksVersion, socksUserPass}); err != nil {
		return false
	}
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 1 || header[1] == 0 {
		return false
	}
	username := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, username); err != nil {
		return false
	}
	one := make([]byte, 1)
	if _, err := io.ReadFull(reader, one); err != nil || one[0] == 0 {
		return false
	}
	password := make([]byte, int(one[0]))
	if _, err := io.ReadFull(reader, password); err != nil {
		return false
	}
	valid := subtle.ConstantTimeCompare(username, []byte(sp.username)) == 1 &&
		subtle.ConstantTimeCompare(password, []byte(sp.password)) == 1
	if !valid {
		sp.authBlocked.Add(1)
		_, _ = client.Write([]byte{1, 1})
		return false
	}
	_, err := client.Write([]byte{1, 0})
	return err == nil
}

func readSOCKSRequest(reader *bufio.Reader, client net.Conn) (string, int, bool) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != socksVersion || header[1] != socksConnect || header[2] != 0 {
		writeSOCKSReply(client, 7, netip.AddrPort{})
		return "", 0, false
	}
	var raw string
	switch header[3] {
	case socksAddressIPv4:
		value := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", 0, false
		}
		raw = net.IP(value).String()
	case socksAddressIPv6:
		value := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", 0, false
		}
		raw = net.IP(value).String()
	case socksAddressDomain:
		length, err := reader.ReadByte()
		if err != nil || length == 0 {
			return "", 0, false
		}
		value := make([]byte, int(length))
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", 0, false
		}
		raw = string(value)
	default:
		writeSOCKSReply(client, 8, netip.AddrPort{})
		return "", 0, false
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", 0, false
	}
	port := int(portBytes[0])<<8 | int(portBytes[1])
	host, _, err := normalizeScopeHost(raw)
	if err != nil || port < 1 {
		writeSOCKSReply(client, 8, netip.AddrPort{})
		return "", 0, false
	}
	return host, port, true
}

func writeSOCKSReply(writer io.Writer, code byte, bound netip.AddrPort) {
	address := bound.Addr()
	if !address.IsValid() || address.Is4() {
		raw := []byte{0, 0, 0, 0}
		if address.IsValid() {
			value := address.As4()
			raw = value[:]
		}
		_, _ = writer.Write(append([]byte{socksVersion, code, 0, socksAddressIPv4}, append(raw, byte(bound.Port()>>8), byte(bound.Port()))...))
		return
	}
	value := address.As16()
	_, _ = writer.Write(append([]byte{socksVersion, code, 0, socksAddressIPv6}, append(value[:], byte(bound.Port()>>8), byte(bound.Port()))...))
}

func (t *Traffic) dialScopedTarget(ctx context.Context, address string) (net.Conn, error) {
	upstream := t.upstream.Load()
	if upstream == nil {
		return (&net.Dialer{}).DialContext(ctx, "tcp", address)
	}
	if upstream.Scheme == "socks5" {
		var auth *xproxy.Auth
		if upstream.User != nil {
			password, _ := upstream.User.Password()
			auth = &xproxy.Auth{User: upstream.User.Username(), Password: password}
		}
		dialer, err := xproxy.SOCKS5("tcp", upstream.Host, auth, xproxy.Direct)
		if err != nil {
			return nil, err
		}
		if contextual, ok := dialer.(xproxy.ContextDialer); ok {
			return contextual.DialContext(ctx, "tcp", address)
		}
		return dialer.Dial("tcp", address)
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", upstream.Host)
	if err != nil {
		return nil, err
	}
	if upstream.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: upstream.Hostname(), InsecureSkipVerify: true}) //nolint:gosec -- preserves existing configurable upstream behavior
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = tlsConn
	}
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: address}, Host: address, Header: make(http.Header)}
	if upstream.User != nil {
		password, _ := upstream.User.Password()
		credentials := upstream.User.Username() + ":" + password
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	if err := request.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("upstream proxy CONNECT returned %s", response.Status)
	}
	if reader.Buffered() > 0 {
		return &bufferedScopedConn{Conn: conn, reader: reader}, nil
	}
	return conn, nil
}

type bufferedScopedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedScopedConn) Read(value []byte) (int, error) { return c.reader.Read(value) }

func (sp *ScopedSOCKS5) ProxyURL() string { return sp.proxyURL }

func (sp *ScopedSOCKS5) RunID() string { return sp.runID }

func (sp *ScopedSOCKS5) RoutesFingerprint() string { return sp.routeHash }

func (sp *ScopedSOCKS5) Stats() ScopedSOCKS5Stats {
	return ScopedSOCKS5Stats{
		Attempts: sp.attempts.Load(), Allowed: sp.allowed.Load(), Connected: sp.connected.Load(),
		AuthBlocked: sp.authBlocked.Load(), ScopeBlocked: sp.scopeBlocked.Load(), DialFailed: sp.dialFailed.Load(),
	}
}

func (sp *ScopedSOCKS5) Close() error {
	sp.closeOnce.Do(func() {
		sp.cancel()
		if err := sp.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			sp.closeErr = err
		}
		sp.connMu.Lock()
		for conn := range sp.conns {
			_ = conn.Close()
		}
		sp.connMu.Unlock()
		<-sp.done
		sp.wg.Wait()
		sp.traffic.scopedMu.Lock()
		delete(sp.traffic.scopedSOCKS, sp)
		sp.traffic.scopedMu.Unlock()
	})
	return sp.closeErr
}
