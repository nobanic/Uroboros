package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/nobanic/UroborOS/selfupdate"
)

// The HTTP surface for one-click page updates. The real download/verify/swap logic all lives in the selfupdate package;
// here we only handle the auth boundary, concurrency mutex, progress broadcast, and telling main 'time to exit'.
//
// restart is not performed by this process: after staging the new version, the process exits with selfupdate.ExitRestart,
// and a guardian script (start.sh / start.bat, or the ENTRYPOINT under Docker) brings it back up.

// restartCh restartCh is closed once an upgrade is ready or a rollback completes; main receives it and exits with ExitRestart.
var (
	restartOnce sync.Once
	restartCh   = make(chan struct{})
)

// RestartRequested returns a channel that is closed when 'please exit and let the guardian restart me'.
func RestartRequested() <-chan struct{} { return restartCh }

func requestRestart() { restartOnce.Do(func() { close(restartCh) }) }

// bootState bootState is the conclusion of selfupdate.Bootstrap at this startup (upgrade succeeded / just rolled back / /
// staged file discarded), injected by main, so /api/update/check can truthfully tell the frontend how the last upgrade turned out.
var (
	bootStateMu sync.Mutex
	bootState   selfupdate.State
)

// SetBootUpdateState SetBootUpdateState is called once by main at startup.
func SetBootUpdateState(st selfupdate.State) {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	bootState = st
}

func bootUpdateState() selfupdate.State {
	bootStateMu.Lock()
	defer bootStateMu.Unlock()
	return bootState
}

// releaseCache releaseCache caches the result of querying GitHub for the latest version.
//
// the top-bar 'new version available' hint queries once on every full-page load, and the unauthenticated GitHub API is
// capped at 60 requests per IP per hour -- without caching, opening a few tabs or refreshing a few times exhausts the quota,
// so when you actually want to update you can't query. When a user explicitly clicks 'Check for updates', force can bypass the cache.
type releaseCache struct {
	mu  sync.Mutex
	rel *selfupdate.Release
	err error
	at  time.Time
	// fetch fetch is the data-fetching function, an injection point kept only for tests; when nil, it uses the real GitHub query.
	fetch func(context.Context, *http.Client) (*selfupdate.Release, error)
}

const (
	releaseTTL = 30 * time.Minute
	// failed results are cached briefly too, otherwise every page load would wait out a timeout while GitHub is unreachable;
	// but the TTL must be short, so it recovers on its own soon after the network comes back.
	releaseErrTTL = 2 * time.Minute
	// the timeout for queries. NewClient's 30-minute timeout is for downloading the full package; a version query must not wait that long.
	releaseTimeout = 20 * time.Second
)

var relCache = &releaseCache{}

// get get returns the latest Release, hitting the cache without touching the network.
//
// the lock is held throughout fetching: concurrent requests queue for the result of the same query instead of each hitting GitHub GitHub
// (when a page just loaded and several tabs query at once is exactly when rate-limiting is easiest to trigger).
func (c *releaseCache) get(ctx context.Context, client *http.Client, force bool) (*selfupdate.Release, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force {
		ttl := releaseTTL
		if c.err != nil {
			ttl = releaseErrTTL
		}
		if !c.at.IsZero() && time.Since(c.at) < ttl {
			return c.rel, c.err
		}
	}

	fetch := c.fetch
	if fetch == nil {
		fetch = selfupdate.FetchLatest
	}
	ctx, cancel := context.WithTimeout(ctx, releaseTimeout)
	defer cancel()
	rel, err := fetch(ctx, client)
	// a canceled request (the user closed the tab) does not mean GitHub has a problem, so do not write it into the cache,
	// otherwise the next visitor would get a baffling 'canceled' error.
	if err != nil && ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return c.rel, err
	}
	c.rel, c.err, c.at = rel, err, time.Now()
	return rel, err
}

// updateProgress updateProgress is one progress entry pushed to the frontend.
type updateProgress struct {
	Phase   selfupdate.Phase `json:"phase"`
	Percent int              `json:"percent"` // meaningful only during the download phase; otherwise -1 -1
	Message string           `json:"message"`
	Version string           `json:"version,omitempty"`
	Error   string           `json:"error,omitempty"`
}

// updateHub updateHub holds one upgrade's progress and broadcasts it to SSE subscribers.
//
// running running also acts as a mutex: a second POST /api/update/apply during an upgrade returns 409 directly,
// avoiding two goroutines writing the same uroboros.new at once.
type updateHub struct {
	mu      sync.Mutex
	running bool
	cur     updateProgress
	subs    map[chan updateProgress]struct{}
}

var updHub = &updateHub{
	cur:  updateProgress{Phase: selfupdate.PhaseIdle, Percent: -1},
	subs: map[chan updateProgress]struct{}{},
}

// begin begin claims the upgrade permission, returning false if one is already in progress.
func (h *updateHub) begin(version string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.running {
		return false
	}
	h.running = true
	h.cur = updateProgress{Phase: selfupdate.PhaseDownload, Percent: 0, Message: "Preparing...", Version: version}
	h.fanout(h.cur)
	return true
}

// finish finish ends one upgrade. err == nil means staging succeeded and we await restart.
func (h *updateHub) finish(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.running = false
	if err != nil {
		h.cur = updateProgress{Phase: selfupdate.PhaseFailed, Percent: -1, Message: "Update failed", Error: err.Error(), Version: h.cur.Version}
	} else {
		h.cur = updateProgress{Phase: selfupdate.PhaseStaged, Percent: 100, Message: "New version ready, restarting...", Version: h.cur.Version}
	}
	h.fanout(h.cur)
}

func (h *updateHub) publish(ph selfupdate.Phase, pct int, msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.cur = updateProgress{Phase: ph, Percent: pct, Message: msg, Version: h.cur.Version}
	h.fanout(h.cur)
}

// fanout fanout must be called while holding h.mu. Subscriber channels are buffered; when full, drop --——
// progress is disposable, transient info; a stuck SSE connection must never block the upgrade itself.
func (h *updateHub) fanout(p updateProgress) {
	for ch := range h.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

func (h *updateHub) snapshot() (updateProgress, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cur, h.running
}

func (h *updateHub) subscribe() (<-chan updateProgress, func()) {
	ch := make(chan updateProgress, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			delete(h.subs, ch)
			h.mu.Unlock()
			close(ch)
		})
	}
}

// updateCheck updateCheck queries GitHub for the latest stable release and compares it to the current version.
//
// the frontend also connects directly to api.github.com (GitHub's CORS is *), but **this endpoint is authoritative**:
// the download is done by the backend, and updating is only possible if the backend can reach GitHub. The browser reaching it but the server not
// is common (the server is on an intranet, or the proxy is configured only in the browser); clicking update would then surely fail,
// so it is better to report the error truthfully at this check step.
func (s *Server) updateCheck(w http.ResponseWriter, r *http.Request) {
	if managedUpdatesEnabled() {
		s.updateProxy(w, r, "check")
		return
	}
	current := BuildVersion
	mode := "binary"
	if selfupdate.InDocker() {
		mode = "docker"
	}
	boot := bootUpdateState()
	out := map[string]any{
		"current":     current,
		"mode":        mode,
		"os":          runtime.GOOS,
		"arch":        runtime.GOARCH,
		"has_backup":  selfupdate.HasBackup(),
		"repo":        selfupdate.Repo,
		"boot_notice": boot.Detail,
		"rolled_back": boot.RolledBack,
	}

	// the top-bar hint uses the cache (default); when a user clicks 'Check for updates', force=1 forces a refetch.
	force := r.URL.Query().Get("force") != ""
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, force)
	if err != nil {
		out["error"] = err.Error()
		writeJSON(w, 200, out)
		return
	}

	latest := rel.TagName
	out["latest"] = latest
	out["notes"] = rel.Body
	out["html_url"] = rel.HTMLURL
	if !rel.PublishedAt.IsZero() {
		out["published_at"] = rel.PublishedAt.Format(time.RFC3339)
	}

	asset := selfupdate.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	out["asset"] = asset
	if a, ok := rel.FindAsset(asset); ok {
		out["asset_available"] = true
		out["size"] = a.Size
	} else {
		out["asset_available"] = false
	}

	cmp, comparable := selfupdate.CompareVersions(current, latest)
	out["comparable"] = comparable
	out["has_update"] = comparable && cmp < 0
	if !comparable {
		// a dev build (dev / git describe with a suffix) has no comparable version number. Allowing it would only
		// overwrite the locally-debugged binary with a stable release, so updates are simply disallowed.
		out["reason"] = fmt.Sprintf("Current version %q is not a stable release; one-click update is disabled", current)
	}
	writeJSON(w, 200, out)
}

// updateApply updateApply downloads and stages the new version, then lets the process exit for the guardian script to restart.
//
// returns 202 immediately; the real work runs in a background goroutine: a full-package download may take minutes,
// and hanging on the request would be cut off by the reverse-proxy timeout. Progress goes via /api/update/stream.
func (s *Server) updateApply(w http.ResponseWriter, r *http.Request) {
	if managedUpdatesEnabled() {
		if reason := s.updateBusyReason(); reason != "" {
			writeErr(w, 409, reason)
			return
		}
		s.updateProxy(w, r, "apply")
		return
	}
	current := BuildVersion

	// use the cache: ensure what gets installed is exactly the version the user saw and confirmed in the UI.
	client := selfupdate.NewClient(s.m.GlobalProxy())
	rel, err := relCache.get(r.Context(), client, false)
	if err != nil {
		writeErr(w, 502, err.Error())
		return
	}
	cmp, comparable := selfupdate.CompareVersions(current, rel.TagName)
	if !comparable {
		writeErr(w, 400, fmt.Sprintf("Current version %q is not a stable release; one-click update is disabled", current))
		return
	}
	if cmp >= 0 {
		writeErr(w, 400, fmt.Sprintf("Already on the latest version %s", current))
		return
	}
	if !updHub.begin(rel.TagName) {
		writeErr(w, 409, "An update is already in progress")
		return
	}

	go func() {
		// deliberately use s.ctx rather than the request's ctx: once the HTTP response returns the request ends,
		// and a download hanging on it would be canceled immediately.
		err := selfupdate.Stage(s.ctx, client, rel, current, func(ph selfupdate.Phase, pct int, msg string) {
			updHub.publish(ph, pct, msg)
		})
		updHub.finish(err)
		if err != nil {
			log.Printf("[update] update failed: %v", err)
			return
		}
		log.Printf("[update] %s → %s staged; about to exit to complete the swap", current, rel.TagName)
		// leave a moment to push the last progress entry to the frontend before triggering exit.
		time.Sleep(1500 * time.Millisecond)
		requestRestart()
	}()

	writeJSON(w, 202, map[string]any{"ok": true, "target": rel.TagName})
}

// updateRollback updateRollback actively reverts to the previous version (uroboros.old, backed up before the swap).
func (s *Server) updateRollback(w http.ResponseWriter, r *http.Request) {
	if managedUpdatesEnabled() {
		managedRollbackUnavailable(w)
		return
	}
	if _, running := updHub.snapshot(); running {
		writeErr(w, 409, "An update is in progress; cannot roll back")
		return
	}
	if err := selfupdate.Rollback(); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	log.Printf("[update] manually rolled back to the previous version; about to exit to complete the switch")
	writeJSON(w, 202, map[string]any{"ok": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		requestRestart()
	}()
}

// updateStream updateStream pushes update progress via SSE.
func (s *Server) updateStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, 500, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	ch, unsub := updHub.subscribe()
	defer unsub()

	send := func(p updateProgress) {
		b, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	// first send the current state so a page refresh immediately shows an in-progress upgrade.
	cur, _ := updHub.snapshot()
	send(cur)

	ctx := r.Context()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case p, ok := <-ch:
			if !ok {
				return
			}
			send(p)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}
