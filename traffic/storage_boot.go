package traffic

import (
	"database/sql"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	mproxy "github.com/lqqyt2423/go-mitmproxy/proxy"
)

// Storage acquisition is ordered: directories, index, schema, proxy. A failed
// schema/proxy acquisition closes only the index, as in the existing lifecycle.
type trafficStorageBoot struct{ directory, address string }

func (boot trafficStorageBoot) open() (*Traffic, error) {
	for _, directory := range []string{boot.directory, filepath.Join(boot.directory, "_index"), filepath.Join(boot.directory, "_blobs"), filepath.Join(boot.directory, "_ca")} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			return nil, err
		}
	}
	index := filepath.Join(boot.directory, "_index", "index.sqlite")
	dsn := index
	// The driver's first '?' separates the filename from parameters. Retain the
	// bare-path fallback; initialization still sets WAL/busy_timeout on its lease.
	if !strings.ContainsRune(index, '?') {
		dsn = "file:" + index + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	store := &Traffic{dir: boot.directory, addr: boot.address, db: database,
		closed: make(chan struct{}), scoped: make(map[*ScopedProxy]struct{}), scopedSOCKS: make(map[*ScopedSOCKS5]struct{})}
	if err = store.initIndex(); err == nil {
		err = boot.attachProxy(store)
	}
	if err != nil {
		database.Close()
		return nil, err
	}
	return store, nil
}

func (boot trafficStorageBoot) attachProxy(store *Traffic) error {
	proxy, err := mproxy.NewProxy(&mproxy.Options{Addr: boot.address, SslInsecure: true, CaRootPath: filepath.Join(boot.directory, "_ca")})
	if err != nil {
		return err
	}
	// Returning nil explicitly selects direct dialing rather than environment
	// proxies. Both intercepted and transparent paths retain this atomic setting.
	proxy.SetUpstreamProxy(func(*http.Request) (*url.URL, error) { return store.upstream.Load(), nil })
	proxy.SetShouldInterceptRule(func(request *http.Request) bool {
		if strings.TrimSpace(request.Header.Get(CaptureRequestHeader)) != "" || strings.TrimSpace(request.Header.Get(NoRecordRequestHeader)) != "" {
			return true
		}
		_, passthrough := store.pass.Load(hostOnly(request.Host))
		return !passthrough
	})
	proxy.AddAddon(&sink{t: store})
	store.proxy = proxy
	return nil
}

// Scoped listeners stop outside their registry lock, then reapers join before
// the index closes. The primary MITM listener's existing Close boundary stays.
func closeTrafficStorage(store *Traffic) error {
	if store.closed != nil {
		store.closeOnce.Do(func() { close(store.closed) })
	}
	store.scopedMu.Lock()
	proxies := registeredTrafficListeners(store.scoped)
	socks := registeredTrafficListeners(store.scopedSOCKS)
	store.scopedMu.Unlock()
	closeTrafficListeners(proxies)
	closeTrafficListeners(socks)
	store.reaping.Wait()
	return store.db.Close()
}

func registeredTrafficListeners[T comparable](registry map[T]struct{}) []T {
	listeners := make([]T, 0, len(registry))
	for listener := range registry {
		listeners = append(listeners, listener)
	}
	return listeners
}

func closeTrafficListeners[T interface{ Close() error }](listeners []T) {
	for _, listener := range listeners {
		_ = listener.Close()
	}
}
