package katana

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/traffic"
)

const (
	maxOutputLineBytes = 4 * 1024 * 1024
	maxStderrBytes     = 64 * 1024
)

var (
	semanticVersionPattern      = regexp.MustCompile(`(?i)\bv?[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9a-z.-]+)?\b`)
	ErrNoExecutableNetworkScope = errors.New("task has no executable network scope")
	ErrNoConcreteHTTPSeed       = errors.New("task scope has no concrete HTTP(S) seed")
)

type ManagerConfig struct {
	BinaryPath           string
	ChromePath           string
	KnowledgeModelPath   string
	KnowledgeModelSHA256 string
}

type activeRun struct {
	jobID  int64
	cancel context.CancelCauseFunc
	done   chan struct{}
}

type preparedRun struct {
	options    Options
	configHash string
	scope      traffic.NetworkScopeSnapshot
	scopeHash  string
	seeds      []string
	seedHash   string
}

// Manager owns local Katana child processes. PostgreSQL is authoritative for
// run history; the in-memory map only supplies cancellation and joining.
type Manager struct {
	ctx                  context.Context
	pg                   *db.DB
	assets               *db.AssetStore
	traffic              *traffic.Traffic
	binary               string
	chrome               string
	knowledgeModel       string
	knowledgeModelSHA256 string

	versionOnce sync.Once
	version     string
	versionErr  error

	startMu sync.Mutex
	mu      sync.Mutex
	active  map[int64]*activeRun
	closed  bool
	// autoPending coalesces any number of executable scope/origin changes that
	// arrive while a task already has a child process. The completion path then
	// evaluates the newest snapshot exactly once.
	autoPending map[int64]struct{}
	// autoBlocked prevents a completion callback from recreating a child after
	// pause/cancel/delete won the lifecycle race. A later explicit EnsureAuto
	// call (for example resume or a new graph edge) removes the block.
	autoBlocked map[int64]struct{}
	onComplete  func(taskID, jobID int64)
}

func NewManager(ctx context.Context, pg *db.DB, assets *db.AssetStore, recorder *traffic.Traffic, cfg ManagerConfig) *Manager {
	if ctx == nil {
		ctx = context.Background()
	}
	m := &Manager{
		ctx: ctx, pg: pg, assets: assets, traffic: recorder,
		active: make(map[int64]*activeRun), autoPending: make(map[int64]struct{}),
		autoBlocked: make(map[int64]struct{}),
	}
	m.binary = resolveBinary(cfg.BinaryPath)
	m.chrome = resolveChrome(cfg.ChromePath)
	m.knowledgeModel = resolveKnowledgeModel(cfg.KnowledgeModelPath)
	m.knowledgeModelSHA256 = strings.ToLower(strings.TrimSpace(cfg.KnowledgeModelSHA256))
	if m.knowledgeModelSHA256 == "" {
		m.knowledgeModelSHA256 = ExpectedKnowledgeModelSHA256
	}
	if pg != nil {
		if count, err := pg.ReconcileKatanaJobs(context.Background()); err != nil {
			log.Printf("[katana] reconcile interrupted jobs failed: %v", err)
		} else if count > 0 {
			log.Printf("[katana] marked %d stale job(s) interrupted", count)
		}
	}
	return m
}

// SetCompletionHook installs the application lifecycle wake used after a job is
// durably finalized. The hook runs outside Manager locks.
func (m *Manager) SetCompletionHook(hook func(taskID, jobID int64)) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.onComplete = hook
	m.mu.Unlock()
}

func resolveBinary(configured string) string {
	for _, candidate := range []string{configured, os.Getenv("UROBOROS_KATANA_BIN")} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return absoluteExecutablePath(candidate)
		}
	}
	path, _ := exec.LookPath("katana")
	return absoluteExecutablePath(path)
}

func resolveChrome(configured string) string {
	for _, candidate := range []string{configured, os.Getenv("UROBOROS_CHROME_BIN"), os.Getenv("PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH")} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			if path := installedExecutablePath(candidate); path != "" {
				return path
			}
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "chrome", "chrome.exe"} {
		if path, err := exec.LookPath(name); err == nil {
			return absoluteExecutablePath(path)
		}
	}
	patterns := []string{
		"/root/.cache/ms-playwright/chromium-*/chrome-linux/chrome",
		"/root/.cache/ms-playwright/chromium-*/chrome-linux64/chrome",
		"/ms-playwright/chromium-*/chrome-linux/chrome",
		"/ms-playwright/chromium-*/chrome-linux64/chrome",
	}
	for _, pattern := range patterns {
		if matches, _ := filepath.Glob(pattern); len(matches) > 0 {
			sort.Strings(matches)
			if path := installedExecutablePath(matches[len(matches)-1]); path != "" {
				return path
			}
		}
	}
	return ""
}

func resolveKnowledgeModel(configured string) string {
	for _, candidate := range []string{configured, os.Getenv("UROBOROS_KATANA_MODEL")} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			if path, err := filepath.Abs(candidate); err == nil {
				return filepath.Clean(path)
			}
			return candidate
		}
	}
	return ""
}

func absoluteExecutablePath(candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return ""
	}
	if !strings.ContainsAny(candidate, `/\\`) {
		if path, err := exec.LookPath(candidate); err == nil {
			candidate = path
		}
	}
	if path, err := filepath.Abs(candidate); err == nil {
		return filepath.Clean(path)
	}
	return candidate
}

func installedExecutablePath(candidate string) string {
	path := absoluteExecutablePath(candidate)
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return ""
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return ""
	}
	return path
}

func (m *Manager) Available() (bool, string) {
	if m == nil || m.pg == nil || m.assets == nil {
		return false, "Katana manager has no PostgreSQL asset store"
	}
	if m.traffic == nil {
		return false, "the task-scoped recording proxy is unavailable"
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return false, "Katana manager is closed"
	}
	if strings.TrimSpace(m.binary) == "" {
		return false, "Katana executable is not installed or not on PATH"
	}
	if _, err := m.BinaryVersion(); err != nil {
		return false, err.Error()
	}
	return true, ""
}

func (m *Manager) BinaryVersion() (string, error) {
	if m == nil || strings.TrimSpace(m.binary) == "" {
		return "", errors.New("Katana executable is unavailable")
	}
	m.versionOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		output, err := exec.CommandContext(ctx, m.binary, "-version").CombinedOutput()
		if err != nil {
			m.versionErr = fmt.Errorf("read Katana version: %w", err)
			return
		}
		reported := strings.TrimSpace(string(output))
		if !hasExpectedVersion(reported) {
			m.versionErr = fmt.Errorf("unsupported Katana version %q; Uroboros requires %s", reported, ExpectedVersion)
			return
		}
		m.version = ExpectedVersion
	})
	return m.version, m.versionErr
}

func hasExpectedVersion(output string) bool {
	expected := strings.ToLower(ExpectedVersion)
	for _, match := range semanticVersionPattern.FindAllString(output, -1) {
		if normalized := strings.ToLower(match); normalized == expected || "v"+normalized == expected {
			return true
		}
	}
	return false
}

func (m *Manager) Start(taskID int64, source string, options Options) (*db.KatanaJob, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return nil, fmt.Errorf("Katana manager is stopping: %w", err)
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil, errors.New("Katana manager is closed")
	}
	if taskID <= 0 {
		return nil, fmt.Errorf("task id must be positive")
	}
	if source != "auto" && source != "tool" {
		return nil, fmt.Errorf("Katana source must be auto or tool")
	}
	if source == "auto" {
		skipped, err := m.pg.InitialKatanaSkipped(m.ctx, taskID)
		if err != nil {
			return nil, fmt.Errorf("read automatic Katana startup choice: %w", err)
		}
		if skipped {
			return nil, errors.New("automatic Katana skipped by operator; explicit tool runs remain available")
		}
	}
	if ok, reason := m.Available(); !ok {
		return nil, errors.New(reason)
	}
	prepared, err := m.prepareRun(taskID, options)
	if err != nil {
		return nil, err
	}
	return m.startPrepared(taskID, source, prepared)
}

func (m *Manager) prepareRun(taskID int64, options Options) (preparedRun, error) {
	prepared, err := m.prepareRunSnapshot(taskID, options)
	if err != nil {
		return preparedRun{}, err
	}
	if prepared.options.KnowledgeBase {
		if err := m.validateKnowledgeModel(); err != nil {
			return preparedRun{}, err
		}
	}
	return prepared, nil
}

// CurrentAutoFingerprint computes the exact automatic scope/config/origin
// snapshot without starting a process or requiring the Katana runtime to be
// installed. Server-side typed-tool admission uses it to invalidate stale
// coverage decisions even when the normal engine wake has not run yet.
func (m *Manager) CurrentAutoFingerprint(taskID int64) (db.KatanaAutoFingerprint, error) {
	if m == nil || m.assets == nil {
		return db.KatanaAutoFingerprint{}, errors.New("Katana manager has no PostgreSQL asset store")
	}
	prepared, err := m.prepareRunSnapshot(taskID, DefaultOptions())
	if err != nil {
		return db.KatanaAutoFingerprint{}, err
	}
	return db.KatanaAutoFingerprint{
		ScopeHash: prepared.scopeHash, ConfigHash: prepared.configHash, SeedHash: prepared.seedHash,
	}, nil
}

func (m *Manager) prepareRunSnapshot(taskID int64, options Options) (preparedRun, error) {
	options, err := options.Normalize()
	if err != nil {
		return preparedRun{}, err
	}
	configHash, err := options.Hash()
	if err != nil {
		return preparedRun{}, err
	}
	snapshot, err := m.assets.TaskNetworkScope(taskID)
	if err != nil {
		return preparedRun{}, fmt.Errorf("build task network scope: %w", err)
	}
	scope, scopeHash, err := traffic.NormalizeNetworkScope(traffic.NetworkScopeSnapshot{
		RootDomains: snapshot.RootDomains,
		ExactHosts:  snapshot.ExactHosts,
		CIDRs:       snapshot.CIDRs,
	})
	if err != nil {
		return preparedRun{}, fmt.Errorf("%w: %v", ErrNoExecutableNetworkScope, err)
	}
	seeds := normalizeSeeds(snapshot.Seeds, scope)
	if len(seeds) == 0 {
		return preparedRun{}, fmt.Errorf("%w; discover a service first", ErrNoConcreteHTTPSeed)
	}
	seedHash, err := seedOriginFingerprint(seeds)
	if err != nil {
		return preparedRun{}, err
	}
	return preparedRun{
		options: options, configHash: configHash, scope: scope, scopeHash: scopeHash,
		seeds: seeds, seedHash: seedHash,
	}, nil
}

func (m *Manager) validateKnowledgeModel() error {
	if m == nil || strings.TrimSpace(m.knowledgeModel) == "" {
		return errors.New("Katana knowledge-base model is not installed; set UROBOROS_KATANA_MODEL to the pinned model.json")
	}
	file, err := os.Open(m.knowledgeModel)
	if err != nil {
		return fmt.Errorf("open Katana knowledge-base model: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat Katana knowledge-base model: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("Katana knowledge-base model %q is not a regular file", m.knowledgeModel)
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("hash Katana knowledge-base model: %w", err)
	}
	actual := fmt.Sprintf("%x", hash.Sum(nil))
	if actual != m.knowledgeModelSHA256 {
		return fmt.Errorf("Katana knowledge-base model checksum %s does not match required %s", actual, m.knowledgeModelSHA256)
	}
	return nil
}

func (m *Manager) stageKnowledgeModel(runtimeDir string) error {
	if err := m.validateKnowledgeModel(); err != nil {
		return err
	}
	destination := filepath.Join(runtimeDir, defaultKnowledgeModelFilename)
	if err := os.Link(m.knowledgeModel, destination); err == nil {
		return nil
	}
	if err := os.Symlink(m.knowledgeModel, destination); err == nil {
		return nil
	}
	source, err := os.Open(m.knowledgeModel)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return fmt.Errorf("stage Katana knowledge-base model: %w", err)
	}
	_, copyErr := io.Copy(target, source)
	closeErr := target.Close()
	if copyErr != nil {
		return fmt.Errorf("copy Katana knowledge-base model: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close staged Katana knowledge-base model: %w", closeErr)
	}
	return nil
}

func (m *Manager) startPrepared(taskID int64, source string, prepared preparedRun) (*db.KatanaJob, error) {
	version, err := m.BinaryVersion()
	if err != nil {
		return nil, err
	}
	job, err := m.pg.CreateKatanaJob(context.Background(), db.KatanaJobCreate{
		TaskID: taskID, Source: source, Mode: prepared.options.Mode, Config: prepared.options, ConfigHash: prepared.configHash,
		Scope: prepared.scope, ScopeHash: prepared.scopeHash, Seeds: redactedSeeds(prepared.seeds),
		SeedHash: prepared.seedHash, BinaryVersion: version,
	})
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancelCause(m.ctx)
	run := &activeRun{jobID: job.ID, cancel: cancel, done: make(chan struct{})}
	m.mu.Lock()
	if existing := m.active[taskID]; existing != nil {
		m.mu.Unlock()
		cancel(errors.New("duplicate in-memory Katana run"))
		_ = m.pg.FinishKatanaJob(context.Background(), job.ID, db.KatanaJobFinish{Status: "canceled", Error: "duplicate active run"})
		return nil, db.ErrKatanaJobActive
	}
	m.active[taskID] = run
	m.mu.Unlock()
	go func() {
		m.execute(runCtx, job, prepared.scope, prepared.seeds, prepared.options)
		m.mu.Lock()
		if m.active[taskID] == run {
			delete(m.active, taskID)
		}
		_, rerun := m.autoPending[taskID]
		delete(m.autoPending, taskID)
		m.mu.Unlock()
		close(run.done)
		if rerun && m.ctx.Err() == nil {
			if _, _, err := m.ensureAuto(taskID, false); err != nil && !errors.Is(err, db.ErrKatanaTaskInactive) {
				log.Printf("[katana] task %d pending automatic discovery failed: %v", taskID, err)
			}
		}
		m.mu.Lock()
		hook := m.onComplete
		m.mu.Unlock()
		if hook != nil {
			hook(taskID, job.ID)
		}
	}()
	return job, nil
}

// EnsureAuto starts the default full discovery exactly once for the current
// scope/config/origin fingerprint. A completed run or a failed automatic run
// suppresses another unchanged automatic attempt. Canceled/interrupted runs
// remain retryable, changed fingerprints still trigger follow-up discovery and
// Start remains the explicit rerun path. A changed fingerprint observed during
// an active run is coalesced into one follow-up evaluation.
func (m *Manager) EnsureAuto(taskID int64) (*db.KatanaJob, bool, error) {
	return m.ensureAuto(taskID, true)
}

func (m *Manager) ensureAuto(taskID int64, unblock bool) (*db.KatanaJob, bool, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	if err := m.ctx.Err(); err != nil {
		return nil, false, fmt.Errorf("Katana manager is stopping: %w", err)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, false, errors.New("Katana manager is closed")
	}
	if unblock {
		delete(m.autoBlocked, taskID)
	} else if _, blocked := m.autoBlocked[taskID]; blocked {
		m.mu.Unlock()
		return nil, false, nil
	}
	m.mu.Unlock()
	// The durable operator choice covers admission, review, resume, scope edges
	// and coalesced follow-ups. It does not change Start(..., "tool", ...).
	skipped, err := m.pg.InitialKatanaSkipped(m.ctx, taskID)
	if err != nil {
		return nil, false, fmt.Errorf("read automatic Katana startup choice: %w", err)
	}
	if skipped {
		return nil, false, nil
	}
	if ok, reason := m.Available(); !ok {
		return nil, false, errors.New(reason)
	}
	options := DefaultOptions()
	prepared, err := m.prepareRun(taskID, options)
	if err != nil {
		return nil, false, err
	}
	active, err := m.pg.ActiveKatanaJob(context.Background(), taskID)
	if err != nil {
		return nil, false, err
	}
	if active != nil {
		m.mu.Lock()
		run := m.active[taskID]
		if run != nil && (active.ScopeHash != prepared.scopeHash || active.ConfigHash != prepared.configHash || active.SeedHash != prepared.seedHash) {
			m.autoPending[taskID] = struct{}{}
		}
		m.mu.Unlock()
		if run != nil {
			return active, false, nil
		}
		// The process may have finalized between the database read and the
		// in-memory lookup. Re-read before deciding that the unique active row
		// still owns this task.
		active, err = m.pg.ActiveKatanaJob(context.Background(), taskID)
		if err != nil || active != nil {
			return active, false, err
		}
	}
	settled, err := m.pg.HasSettledKatanaJobForAuto(context.Background(), taskID, prepared.scopeHash, prepared.configHash, prepared.seedHash)
	if err != nil || settled {
		return nil, false, err
	}
	job, err := m.startPrepared(taskID, "auto", prepared)
	if errors.Is(err, db.ErrKatanaJobActive) {
		active, activeErr := m.pg.ActiveKatanaJob(context.Background(), taskID)
		if activeErr != nil {
			return nil, false, activeErr
		}
		if active != nil {
			return active, false, nil
		}
	}
	return job, err == nil, err
}

func normalizeSeeds(values []string, scope traffic.NetworkScopeSnapshot) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		seed, ok := sanitizeExecutionURL(value)
		if ok && scope.AllowsURL(seed) {
			set[seed] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for seed := range set {
		out = append(out, seed)
	}
	sort.Strings(out)
	return out
}

func redactedSeeds(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		if seed, ok := sanitizeDiscoveryURL(value); ok {
			set[seed] = struct{}{}
		}
	}
	out := make([]string, 0, len(set))
	for seed := range set {
		out = append(out, seed)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) execute(parent context.Context, job *db.KatanaJob, scope traffic.NetworkScopeSnapshot, seeds []string, options Options) {
	runCtx := parent
	cancel := func() {}
	if options.MaxDurationSeconds > 0 {
		runCtx, cancel = context.WithTimeout(parent, time.Duration(options.MaxDurationSeconds)*time.Second)
	}
	defer cancel()
	finish := db.KatanaJobFinish{Status: "failed"}
	finishCtx := context.Background()
	defer func() {
		if err := m.pg.FinishKatanaJob(finishCtx, job.ID, finish); err != nil {
			log.Printf("[katana] job %d finalization failed: %v", job.ID, err)
		}
	}()
	proxy, err := m.traffic.StartScopedProxy(runCtx, traffic.ScopedProxyPolicy{
		RunID: strconv.FormatInt(job.ID, 10), Scope: scope,
		AllowedMethods: options.AllowedMethods, MaxRequests: options.MaxRequests,
	})
	if err != nil {
		finish.Error = "start scoped proxy: " + err.Error()
		return
	}
	defer proxy.Close() //nolint:errcheck
	args, err := BuildArgs(options, seeds, proxy.ProxyURL(), m.chrome)
	if err != nil {
		finish.Error = err.Error()
		return
	}
	if err := m.pg.MarkKatanaJobRunning(context.Background(), job.ID); err != nil {
		finish.Error = err.Error()
		return
	}
	runtimeDir, err := os.MkdirTemp("", fmt.Sprintf("uroboros-katana-%d-", job.ID))
	if err != nil {
		finish.Error = "create isolated Katana runtime: " + err.Error()
		return
	}
	defer func() {
		if err := os.RemoveAll(runtimeDir); err != nil {
			log.Printf("[katana] job %d runtime cleanup failed: %v", job.ID, err)
		}
	}()
	if options.KnowledgeBase {
		if err := m.stageKnowledgeModel(runtimeDir); err != nil {
			finish.Error = err.Error()
			return
		}
	}

	cmd := exec.CommandContext(runCtx, m.binary, args...)
	configureProcessTree(cmd)
	cmd.Dir = runtimeDir
	cmd.Env = discoveryEnvironment(os.Environ(), proxy.ProxyURL(), m.traffic.CACertPath(), runtimeDir)
	cmd.Stdin = strings.NewReader(strings.Join(seeds, "\n") + "\n")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		finish.Error = err.Error()
		return
	}
	stderr := &boundedBuffer{limit: maxStderrBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		finish.Error = "start Katana: " + err.Error()
		return
	}
	parseDone := make(chan parseSummary, 1)
	go func() { parseDone <- m.consumeOutput(runCtx, job, scope, stdout) }()
	parsed := <-parseDone
	// StdoutPipe must be fully consumed before Wait closes its read handle.
	// Fast native processes can otherwise lose the final JSONL record.
	waitErr := cmd.Wait()
	closeErr := proxy.Close()
	proxyStats := proxy.Stats()

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	finish.ExitCode = &exitCode
	finish.Stderr = redactDiagnostic(stderr.String())
	stats := map[string]any{
		"proxy": proxyStats, "stdout_lines": parsed.lines, "candidate_lines": parsed.candidates,
		"runtime_os": runtime.GOOS,
	}
	if err := m.importVerified(context.Background(), job, proxy.CapturedExchanges()); err != nil && parsed.err == nil {
		parsed.err = err
	}
	finish.Stats = stats
	if cause := context.Cause(parent); cause != nil || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		finish.Status = "canceled"
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			finish.Error = "configured Katana duration elapsed"
		} else {
			finish.Error = cause.Error()
		}
		return
	}
	if closeErr != nil {
		finish.Error = "close scoped proxy: " + closeErr.Error()
		return
	}
	if parsed.err != nil {
		finish.Error = parsed.err.Error()
		return
	}
	if waitErr != nil {
		finish.Error = "Katana exited unsuccessfully: " + waitErr.Error()
		return
	}
	if err := proxy.Err(); err != nil {
		finish.Error = "scoped proxy stopped unexpectedly: " + err.Error()
		return
	}
	if proxyStats.Requests == 0 {
		finish.Error = "Katana exited without issuing an HTTP request through the task-scoped proxy"
		return
	}
	if proxyStats.Allowed == 0 || proxyStats.Captured == 0 {
		finish.Error = "Katana did not complete a scope-approved HTTP exchange through the task-scoped proxy"
		return
	}
	finish.Status = "completed"
	finish.Error = ""
}

type parseSummary struct {
	lines      int64
	candidates int64
	err        error
}

func (m *Manager) consumeOutput(ctx context.Context, job *db.KatanaJob, scope traffic.NetworkScopeSnapshot, reader io.Reader) parseSummary {
	var summary parseSummary
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxOutputLineBytes)
	for scanner.Scan() {
		summary.lines++
		candidates, err := parseOutputCandidates(scanner.Bytes())
		if err != nil {
			if summary.err == nil {
				summary.err = fmt.Errorf("parse Katana JSONL line %d: %w", summary.lines, err)
			}
			continue
		}
		for _, item := range candidates {
			if !scope.AllowsURL(item.URL) {
				continue
			}
			if _, err := m.pg.UpsertKatanaDiscovery(ctx, db.KatanaDiscoveryUpsert{
				JobID: job.ID, TaskID: job.TaskID, Source: item.Source, Method: item.Method,
				URL: item.URL, StatusCode: item.StatusCode, Technologies: item.Technologies,
				KnowledgeBase: item.KnowledgeBase,
			}); err != nil {
				if summary.err == nil {
					summary.err = fmt.Errorf("store Katana candidate: %w", err)
				}
				continue
			}
			summary.candidates++
		}
	}
	if err := scanner.Err(); err != nil {
		if summary.err == nil {
			summary.err = fmt.Errorf("read Katana JSONL: %w", err)
		}
		// Scanner stops at an oversized/malformed token. Keep draining stdout so
		// the child cannot block on a full pipe while execute waits in cmd.Wait.
		_, _ = io.Copy(io.Discard, reader)
	}
	return summary
}

func (m *Manager) importVerified(ctx context.Context, job *db.KatanaJob, exchanges []traffic.ScopedExchange) error {
	for _, exchange := range exchanges {
		cleanURL, ok := sanitizeDiscoveryURL(exchange.URL)
		if !ok {
			continue
		}
		status := exchange.Status
		service, ok := serviceURL(cleanURL)
		if !ok {
			continue
		}
		var serviceStatus *int
		if isOriginRootURL(cleanURL) {
			serviceStatus = &status
		}
		serviceID, err := m.assets.UpsertHTTPService(db.UpsertHTTPServiceReq{
			URL: service, StatusCode: serviceStatus, TaskID: job.TaskID,
		})
		if err != nil {
			return fmt.Errorf("import Katana HTTP service: %w", err)
		}
		if err := m.assets.SetTaskAssetSource(job.TaskID, serviceID, "katana", "verified by task-scoped Katana traffic", nil); err != nil {
			return fmt.Errorf("record Katana HTTP service provenance: %w", err)
		}
		method := strings.ToUpper(strings.TrimSpace(exchange.Method))
		if method == "" {
			method = "GET"
		}
		endpointID, err := m.assets.UpsertEndpoint(db.UpsertEndpointReq{
			URL: cleanURL, Method: method, Params: endpointParams(cleanURL), TaskID: job.TaskID,
		})
		if err != nil {
			return fmt.Errorf("import Katana endpoint: %w", err)
		}
		if err := m.assets.SetTaskAssetSource(job.TaskID, endpointID, "katana", "verified by task-scoped Katana traffic "+exchange.TrafficID, nil); err != nil {
			return fmt.Errorf("record Katana endpoint provenance: %w", err)
		}
		if _, err := m.pg.UpsertKatanaDiscovery(ctx, db.KatanaDiscoveryUpsert{
			JobID: job.ID, TaskID: job.TaskID, Source: "proxy", Method: method, URL: cleanURL,
			StatusCode: &status, Verified: true, TrafficID: exchange.TrafficID, AssetID: &endpointID,
		}); err != nil {
			return fmt.Errorf("store verified Katana discovery: %w", err)
		}
	}
	return nil
}

func (m *Manager) LatestJob(taskID int64) (*db.KatanaJob, error) {
	if m == nil || m.pg == nil {
		return nil, errors.New("Katana job store is unavailable")
	}
	return m.pg.LatestKatanaJob(context.Background(), taskID)
}

func (m *Manager) Job(ctx context.Context, jobID int64) (*db.KatanaJob, error) {
	if m == nil || m.pg == nil {
		return nil, errors.New("Katana job store is unavailable")
	}
	return m.pg.KatanaJob(ctx, jobID)
}

func (m *Manager) Results(jobID int64, limit, offset int) ([]db.KatanaDiscovery, error) {
	if m == nil || m.pg == nil {
		return nil, errors.New("Katana discovery store is unavailable")
	}
	return m.pg.ListKatanaDiscoveries(context.Background(), jobID, limit, offset)
}

func (m *Manager) CancelTask(ctx context.Context, taskID int64, reason error) error {
	if reason == nil {
		reason = errors.New("Katana run canceled")
	}
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	m.autoBlocked[taskID] = struct{}{}
	delete(m.autoPending, taskID)
	run := m.active[taskID]
	m.mu.Unlock()
	if run == nil {
		return nil
	}
	run.cancel(reason)
	select {
	case <-run.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Close(ctx context.Context) error {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	m.closed = true
	clear(m.autoPending)
	clear(m.autoBlocked)
	runs := make([]*activeRun, 0, len(m.active))
	for _, run := range m.active {
		runs = append(runs, run)
	}
	m.mu.Unlock()
	for _, run := range runs {
		run.cancel(errors.New("application is stopping"))
	}
	for _, run := range runs {
		select {
		case <-run.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func discoveryEnvironment(existing []string, proxyURL, caPath, runtimeDir string) []string {
	// The crawler needs process/runtime locations, locale and display metadata,
	// not the parent application's LLM, cloud or captcha credentials. An allowlist
	// also prevents Katana's AUTH_CREDENTIALS/CAPTCHA_* environment bindings from
	// silently enabling network behavior that was not present in typed options.
	allowed := map[string]struct{}{
		"PATH": {}, "PROGRAMDATA": {},
		"SYSTEMROOT": {}, "WINDIR": {}, "COMSPEC": {}, "PATHEXT": {},
		"LANG": {}, "LANGUAGE": {}, "LC_ALL": {}, "TZ": {},
		"DISPLAY": {}, "WAYLAND_DISPLAY": {}, "XAUTHORITY": {},
		"FONTCONFIG_PATH": {},
		"LD_LIBRARY_PATH": {}, "DYLD_LIBRARY_PATH": {}, "SSL_CERT_DIR": {},
	}
	out := make([]string, 0, len(allowed)+10)
	for _, item := range existing {
		key := item
		if index := strings.IndexByte(item, '='); index >= 0 {
			key = item[:index]
		}
		if _, keep := allowed[strings.ToUpper(key)]; keep {
			out = append(out, item)
		}
	}
	out = append(out,
		"HOME="+runtimeDir, "USERPROFILE="+runtimeDir,
		"APPDATA="+filepath.Join(runtimeDir, "appdata"), "LOCALAPPDATA="+filepath.Join(runtimeDir, "localappdata"),
		"XDG_CONFIG_HOME="+filepath.Join(runtimeDir, "config"), "XDG_CACHE_HOME="+filepath.Join(runtimeDir, "cache"),
		"XDG_RUNTIME_DIR="+runtimeDir, "TEMP="+runtimeDir, "TMP="+runtimeDir, "TMPDIR="+runtimeDir,
		"HTTP_PROXY="+proxyURL, "HTTPS_PROXY="+proxyURL, "ALL_PROXY="+proxyURL,
		"http_proxy="+proxyURL, "https_proxy="+proxyURL, "all_proxy="+proxyURL,
		"NO_PROXY=", "no_proxy=", "SSL_CERT_FILE="+caPath, "CURL_CA_BUNDLE="+caPath,
	)
	return out
}

type boundedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
			b.truncated = true
		}
		_, _ = b.buf.Write(p)
	} else {
		b.truncated = true
	}
	return written, nil
}

func (b *boundedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := b.buf.String()
	if b.truncated {
		value += "\n[stderr truncated by Uroboros]"
	}
	return value
}
