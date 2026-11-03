package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/harness"
	"github.com/nobanic/norma/llm"
)

// isFKViolation reports whether err is a Postgres foreign-key violation (SQLSTATE
// 23503) — e.g. an activity insert whose exploration_id has no parent row.
func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// dropReason classifies why an activity write was dropped, so the log can be
// grouped/analysed by cause rather than by raw error text.
func dropReason(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23503":
			return "fk_violation(23503,parent exploration missing)"
		case "23505":
			return "unique_violation(23505)"
		default:
			return "pg_error(" + pgErr.Code + ")"
		}
	}
	return "write_error"
}

// bumpDrop increments and returns the running count of dropped (unpersistable)
// activity records for a task. Concurrent planner + worker emits race here, so the
// counter is an atomic behind sync.Map. The count in the log shows loss scale at a
// glance instead of forcing a grep-and-count.
func (e *Engine) bumpDrop(taskID string) int64 {
	v, _ := e.dropCnt.LoadOrStore(taskID, new(int64))
	return atomic.AddInt64(v.(*int64), 1)
}

// preview collapses newlines and trims s to a short rune-safe snippet for one-line
// log output (avoids dumping a multi-KB summary/detail into the log).
func preview(s string, n int) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

// model_error (a provider/API fault: the LLM layer's transient retries were exhausted,
// or the stream broke mid-way after it had already started). Work that ends this way is
// neither "tried but unfinished" nor a true failure — it's external flakiness. Treating it
// as permanently blocked by default would waste an intent, so this terminal state is re-run
// a few extra times with a backoff between attempts to give the provider time to recover;
// if it is paused/terminated/cancelled during retries it yields immediately to that branch.
const (
	modelErrorRetries      = 2               // extra retries after a model_error outcome
	modelErrorRetryBackoff = 3 * time.Second // backoff before each retry
	workControlWaitTimeout = 30 * time.Second
)

var errWorkControlConflict = errors.New("work control conflict")

// retryableWorkerModelError excludes errors already handled by the task router.
// In particular, a quota error after partial streaming advances the task cursor
// for the next LLM call but must not replay this whole intent on the backup.
func retryableWorkerModelError(reason harness.TerminalReason, err error) bool {
	return reason == harness.ReasonModelError && !isTaskLLMRuntimeError(err)
}

// Engine drives the event-driven exploration loop with real LLM agents
// (docs §4.3/§4.4): on asset/exploration-graph change (debounced) it wakes the
// planner, which reads the route, queries assets, judges goals and emits intents;
// N concurrent work agents claim intents and execute them. There is no
// simulation mode — an LLM provider is required. The planner/worker can be
// (re)installed at runtime (LLM configured from the UI); the loops always run
// but idle until an LLM is set.
type Engine struct {
	m        *Manager
	debounce time.Duration

	bc *Broadcaster // live activity pub/sub (SSE)

	started  sync.Map // taskID -> bool, so Run is idempotent per task
	lastAct  sync.Map // taskID -> int64 unix, last planner/worker activity (heartbeat)
	llmCalls sync.Map // taskID -> *int64, actual planner/worker/main-agent LLM calls
	paused   sync.Map // taskID -> bool, user-paused (planner + workers idle but loops alive)
	deleting sync.Map // taskID -> bool, delete barrier (no new task-owned writes)
	dropCnt  sync.Map // taskID -> *int64, running count of dropped (unpersistable) activity records
	// resultShadow holds task-local exact input/result hashes for efficiency
	// analysis. It is observation-only and never affects dispatch or tool output.
	resultShadow sync.Map // taskID -> *resultShadowRegistry

	// deleteMu makes installing the delete barrier atomic with registering a new
	// task operation. Once BeginDelete returns, every admitted writer is reflected
	// in inflight and every later writer is rejected.
	deleteMu sync.RWMutex

	// Every long-lived task goroutine (planner, workers and deadline coordinator)
	// runs under one task-scoped context. Successful deletion cancels that context,
	// waits for all goroutines, then releases every task-level Engine reference.
	runtimeMu sync.Mutex
	runtimes  map[string]*taskRuntime

	// per-task execution context: each planner.Plan / worker.Execute runs under it,
	// so pausing can CANCEL an in-flight run (not just skip the next one). Recreated
	// on resume since cancelling is one-shot. Every cancellation carries a named
	// cause so the activity trace can identify the initiating control path.
	execMu     sync.Mutex
	execCancel map[string]context.CancelCauseFunc
	execCtx    map[string]context.Context

	// Per-work control lets the planner kill a worker and lets the UI pause/cancel
	// one intent without pausing the whole task. The done channel closes only after
	// runWorkerStep has stopped writing and committed its final state.
	workMu sync.Mutex
	work   map[int64]*workExecution

	// steerBox queues planner course-corrections for a running work (keyed by intent
	// id). The worker's PreToolUse hook drains it before its next tool call and hands
	// the message to the model (blocking that call) so it re-plans — no kill needed.
	steerMu  sync.Mutex
	steerBox map[int64][]string

	plannerRound sync.Map // taskID -> int, planner round counter (for UI round separators)

	// task-level timeout (see docs/task-timeout-and-wrapup-design.md):
	settling     sync.Map // taskID -> bool, task has entered the settle sequence (stop dispatching/claiming new intents)
	deadline     sync.Map // taskID -> int64 unix, absolute deadline (stamped at the first provider call; 0/absent = no limit)
	stamped      sync.Map // taskID -> bool, whether the first-provider transition was mirrored in this process
	inflight     sync.Map // taskID -> *int64, count of in-flight planner.Plan + worker.Execute (used for drain)
	coordStarted sync.Map // taskID -> bool, whether the deadline coordinator has started (dedup across Run/reload)

	// resolve returns a task's dedicated planner/worker (wired by the server as the
	// authoritative task-router). nil,nil means this task is deliberately unavailable
	// (for example an exhausted failover chain) — there is no global-pair fallback.
	resolve              func(t *Task) (*agent.Planner, *agent.Worker)
	resolveAuthoritative bool
	// readiness reports whether a global LLM provider is configured — the signal behind
	// Ready()/the llm_configured indicator. Wired once at startup; nil → not ready.
	readiness func() bool
	// Re-evaluates the task's immutable scope and HTTP-origin fingerprint after
	// graph changes. This infrastructure callback does not depend on an LLM.
	katanaAuto func(taskID string)
	// discoveryReviewGate is the durable source of truth; discoveryReviewPending
	// caches its result so idle worker pools do not poll PostgreSQL forever.
	discoveryReviewGate  func(context.Context, int64) (required, reviewable bool, err error)
	discoveryReviewState sync.Map // taskID -> discoveryReviewGateState
}

type discoveryReviewGateState struct {
	required   bool
	reviewable bool
}

type taskRuntime struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type workExecution struct {
	cancel context.CancelCauseFunc
	done   chan error
	action string // user action: pause | cancel
}

// nextPlannerRound returns the next planner round number for a task (1-based).
func (e *Engine) nextPlannerRound(taskID string) int {
	v, _ := e.plannerRound.LoadOrStore(taskID, 0)
	n := v.(int) + 1
	e.plannerRound.Store(taskID, n)
	return n
}

// Pause stops a task: marks it paused AND cancels any in-flight planner/worker run
// for it (a long worker.Execute would otherwise keep going until it finishes).
func (e *Engine) Pause(taskID string, cause error) {
	e.paused.Store(taskID, true)
	e.cancelExec(taskID, cause)
}

// BeginDelete installs an execution barrier before task data/files are removed.
// The temporary pause is not a user pause. The server serializes this transition
// with lifecycle admission and tells AbortDelete whether the persisted task is
// paused/queued if cleanup fails.
func (e *Engine) BeginDelete(taskID string) bool {
	e.deleteMu.Lock()
	if _, loaded := e.deleting.LoadOrStore(taskID, true); loaded {
		e.deleteMu.Unlock()
		return false
	}
	e.paused.Store(taskID, true)
	e.deleteMu.Unlock()
	e.cancelExec(taskID, agent.AbortTaskDeleted)
	return true
}

func (e *Engine) AbortDelete(taskID string, keepPaused bool) {
	e.deleteMu.Lock()
	if !e.IsDeleting(taskID) {
		e.deleteMu.Unlock()
		return
	}
	e.deleting.Delete(taskID)
	if !keepPaused {
		e.paused.Delete(taskID)
	}
	e.deleteMu.Unlock()
	if !keepPaused && e.m != nil {
		if t, ok := e.m.Task(taskID); ok {
			t.Notify()
		}
	}
}

func (e *Engine) IsDeleting(taskID string) bool {
	_, ok := e.deleting.Load(taskID)
	return ok
}

// registerTaskRoutines reserves count goroutines in the task runtime. Callers
// hold deleteMu for reading so StopTask cannot race WaitGroup.Add with Wait.
func (e *Engine) registerTaskRoutines(parent context.Context, taskID string, count int) *taskRuntime {
	e.runtimeMu.Lock()
	defer e.runtimeMu.Unlock()
	rt := e.runtimes[taskID]
	if rt == nil {
		ctx, cancel := context.WithCancel(parent)
		rt = &taskRuntime{ctx: ctx, cancel: cancel}
		e.runtimes[taskID] = rt
	}
	rt.wg.Add(count)
	return rt
}

func runTaskRoutine(rt *taskRuntime, fn func(context.Context)) {
	go func() {
		defer rt.wg.Done()
		fn(rt.ctx)
	}()
}

// StopTask permanently stops every long-lived goroutine and removes all Engine
// state for a successfully deleted task. The delete barrier remains installed
// until cleanup finishes, so no new task operation can race the teardown.
func (e *Engine) StopTask(taskID string) {
	e.deleteMu.Lock()
	e.deleting.Store(taskID, true)
	e.deleteMu.Unlock()

	e.cancelExec(taskID, agent.AbortTaskDeleted)
	e.runtimeMu.Lock()
	rt := e.runtimes[taskID]
	if rt != nil {
		rt.cancel()
	}
	e.runtimeMu.Unlock()
	if rt != nil {
		rt.wg.Wait()
	}

	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(agent.AbortTaskDeleted)
	}
	delete(e.execCancel, taskID)
	delete(e.execCtx, taskID)
	e.execMu.Unlock()

	e.runtimeMu.Lock()
	if e.runtimes[taskID] == rt {
		delete(e.runtimes, taskID)
	}
	e.runtimeMu.Unlock()

	e.started.Delete(taskID)
	e.lastAct.Delete(taskID)
	e.llmCalls.Delete(taskID)
	e.paused.Delete(taskID)
	e.dropCnt.Delete(taskID)
	e.resultShadow.Delete(taskID)
	e.discoveryReviewState.Delete(taskID)
	e.plannerRound.Delete(taskID)
	e.settling.Delete(taskID)
	e.deadline.Delete(taskID)
	e.stamped.Delete(taskID)
	e.inflight.Delete(taskID)
	e.coordStarted.Delete(taskID)
	e.deleteMu.Lock()
	e.deleting.Delete(taskID)
	e.deleteMu.Unlock()
}

// cancelExec cancels a task's current per-task exec context (any in-flight
// planner.Plan / worker.Execute), if present. Shared by Pause and the settle
// sequence's hard-drain backstop.
func (e *Engine) cancelExec(taskID string, cause error) {
	e.execMu.Lock()
	if cancel := e.execCancel[taskID]; cancel != nil {
		cancel(cause)
	}
	e.execMu.Unlock()
}

// Resume un-pauses a task and nudges a fresh planning round. The next exec under
// it gets a fresh (uncancelled) context.
func (e *Engine) Resume(t *Task) {
	// BeginDelete owns the pause barrier once deletion starts. A concurrent
	// resume must never clear it and let a planner/worker re-enter while cleanup
	// is waiting for task operations to drain.
	if t == nil {
		return
	}
	e.deleteMu.RLock()
	defer e.deleteMu.RUnlock()
	if e.IsDeleting(t.ID) {
		return
	}
	e.paused.Delete(t.ID)
	t.Notify()
}

// execContextFor returns a live per-task context derived from parent, recreating
// it if a prior pause cancelled it.
func (e *Engine) execContextFor(parent context.Context, taskID string) context.Context {
	e.execMu.Lock()
	defer e.execMu.Unlock()
	if e.IsPaused(taskID) {
		// never hand out a live context while paused (guards the claim→Execute race)
		c, cancel := context.WithCancelCause(parent)
		cancel(agent.AbortPausedRaceGuard)
		return c
	}
	if c := e.execCtx[taskID]; c != nil && c.Err() == nil {
		return c
	}
	c, cancel := context.WithCancelCause(parent)
	e.execCtx[taskID] = c
	e.execCancel[taskID] = cancel
	return c
}

// IsPaused reports whether a task is user-paused.
func (e *Engine) IsPaused(taskID string) bool {
	v, ok := e.paused.Load(taskID)
	return ok && v.(bool)
}

// Started reports whether the engine loops are running for a task.
func (e *Engine) Started(taskID string) bool {
	_, ok := e.started.Load(taskID)
	return ok
}

// LastActivity returns the unix time of the last planner/worker activity for a
// task (0 if none yet).
func (e *Engine) LastActivity(taskID string) int64 {
	if v, ok := e.lastAct.Load(taskID); ok {
		return v.(int64)
	}
	return 0
}

// BeginLLMCall/EndLLMCall track actual provider calls separately from the
// scheduler's task-operation counter. A task can have live loops while all of
// them are waiting for a trigger; that state must remain idle in the UI.
func (e *Engine) BeginLLMCall(taskID string) {
	v, _ := e.llmCalls.LoadOrStore(taskID, new(int64))
	atomic.AddInt64(v.(*int64), 1)
}

func (e *Engine) EndLLMCall(taskID string) {
	if v, ok := e.llmCalls.Load(taskID); ok {
		p := v.(*int64)
		if atomic.AddInt64(p, -1) <= 0 {
			atomic.StoreInt64(p, 0)
		}
	}
}

func (e *Engine) ActiveLLMCalls(taskID string) int64 {
	if v, ok := e.llmCalls.Load(taskID); ok {
		return atomic.LoadInt64(v.(*int64))
	}
	return 0
}

func (e *Engine) touch(taskID string) { e.lastAct.Store(taskID, time.Now().Unix()) }

func NewEngine(m *Manager) *Engine {
	return &Engine{m: m, debounce: 800 * time.Millisecond, bc: NewBroadcaster(),
		execCancel: map[string]context.CancelCauseFunc{}, execCtx: map[string]context.Context{},
		work: map[int64]*workExecution{}, steerBox: map[int64][]string{},
		runtimes: map[string]*taskRuntime{}}
}

func (e *Engine) SetKatanaAutoTrigger(trigger func(taskID string)) {
	if e != nil {
		e.katanaAuto = trigger
	}
}

func (e *Engine) SetDiscoveryReviewGate(gate func(context.Context, int64) (required, reviewable bool, err error)) {
	if e != nil {
		e.discoveryReviewGate = gate
	}
}

// RefreshDiscoveryReviewGate synchronizes the in-memory worker barrier with the
// durable review token. Call it after automatic Katana scheduling/completion and
// after a planner records a decision.
func (e *Engine) RefreshDiscoveryReviewGate(taskID string) bool {
	if e == nil || e.discoveryReviewGate == nil {
		return false
	}
	numericID, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || numericID <= 0 {
		e.discoveryReviewState.Store(taskID, discoveryReviewGateState{required: true})
		return true
	}
	required, reviewable, err := e.discoveryReviewGate(context.Background(), numericID)
	if err != nil {
		log.Printf("[discovery-review] task %s gate refresh failed: %v", taskID, err)
		required = true
		reviewable = false
	}
	e.discoveryReviewState.Store(taskID, discoveryReviewGateState{required: required, reviewable: reviewable})
	return required
}

func (e *Engine) currentDiscoveryReviewGate(taskID string) discoveryReviewGateState {
	if e == nil || e.discoveryReviewGate == nil {
		return discoveryReviewGateState{reviewable: true}
	}
	if value, ok := e.discoveryReviewState.Load(taskID); ok {
		return value.(discoveryReviewGateState)
	}
	e.RefreshDiscoveryReviewGate(taskID)
	value, _ := e.discoveryReviewState.Load(taskID)
	return value.(discoveryReviewGateState)

}

func (e *Engine) discoveryReviewRequired(taskID string) bool {
	return e.currentDiscoveryReviewGate(taskID).required
}

func (e *Engine) triggerKatanaAuto(taskID string) {
	if e == nil || e.katanaAuto == nil {
		return
	}
	e.katanaAuto(taskID)
}

// registerWork records the cancel for the work currently running intentID.
func (e *Engine) registerWork(intentID int64, cancel context.CancelCauseFunc) {
	e.workMu.Lock()
	e.work[intentID] = &workExecution{cancel: cancel, done: make(chan error, 1)}
	e.workMu.Unlock()
}

// detachWork removes the live control handle once Execute has returned. complete
// must be called after the final intent state write so a waiting cancel handler can
// safely delete the worker's blackboard output without racing a late write.
func (e *Engine) detachWork(intentID int64) (action string, complete func(error)) {
	e.workMu.Lock()
	run := e.work[intentID]
	if run != nil {
		delete(e.work, intentID)
		action = run.action
		run.cancel(agent.AbortWorkFinished) // release resources (no-op if already cancelled)
	}
	e.workMu.Unlock()
	e.steerMu.Lock()
	delete(e.steerBox, intentID) // drop any undelivered steering for a finished work
	e.steerMu.Unlock()
	if run == nil {
		return action, func(error) {}
	}
	return action, func(err error) { run.done <- err }
}

// ControlWork requests a user-visible pause or cancellation and waits until the
// worker has fully stopped writing. Cancellation cleanup is performed by the API
// handler after this returns; pause state is committed by runWorkerStep itself.
func (e *Engine) ControlWork(ctx context.Context, intentID int64, action string) error {
	if action != "pause" && action != "cancel" {
		return fmt.Errorf("unsupported work action %q", action)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	e.workMu.Lock()
	run := e.work[intentID]
	if run == nil {
		e.workMu.Unlock()
		return fmt.Errorf("%w: intent %d has no work currently running (it may have finished or not been claimed)", errWorkControlConflict, intentID)
	}
	if run.action != "" {
		e.workMu.Unlock()
		return fmt.Errorf("%w: intent %d is already performing a %s operation", errWorkControlConflict, intentID, run.action)
	}
	run.action = action
	done := run.done
	cause := error(agent.AbortWorkPausedByUser)
	if action == "cancel" {
		cause = agent.AbortWorkCancelledByUser
	}
	run.cancel(cause)
	e.workMu.Unlock()

	timer := time.NewTimer(workControlWaitTimeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("waiting for intent %d %s to settle: %w", intentID, action, ctx.Err())
	case <-timer.C:
		e.releaseWorkControl(intentID, run, action)
		return fmt.Errorf("waiting for intent %d %s to settle: %w", intentID, action, context.DeadlineExceeded)
	}
}

// releaseWorkControl drops only this caller's reservation after its wait is
// cancelled. The work context stays cancelled; runWorkerStep recognizes the
// named cancellation cause and settles the intent into the recoverable paused
// state even if the HTTP caller has gone away.
func (e *Engine) releaseWorkControl(intentID int64, run *workExecution, action string) {
	e.workMu.Lock()
	if current := e.work[intentID]; current == run && current.action == action {
		current.action = ""
	}
	e.workMu.Unlock()
}

func transitionIntentState(store *db.ExplorationStore, intentID int64, expected, state string) error {
	changed, err := store.CompareAndSetIntentState(intentID, expected, state)
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: intent %d is no longer in state %s", db.ErrIntentStateConflict, intentID, expected)
	}
	return nil
}

// SteerWork queues a mid-run course-correction for the work running intentID (the
// planner's steer_work tool). The worker delivers it before its next tool call and
// re-plans — no kill. Errors if no work is currently running that intent.
func (e *Engine) SteerWork(intentID int64, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("steering message must not be empty")
	}
	e.workMu.Lock()
	running := e.work[intentID] != nil
	e.workMu.Unlock()
	if !running {
		return fmt.Errorf("intent %d has no work currently running (it may have finished or not been claimed)", intentID)
	}
	e.steerMu.Lock()
	e.steerBox[intentID] = append(e.steerBox[intentID], msg)
	e.steerMu.Unlock()
	return nil
}

// drainSteer pops the oldest queued steering message for intentID (FIFO), if any.
func (e *Engine) drainSteer(intentID int64) (string, bool) {
	e.steerMu.Lock()
	defer e.steerMu.Unlock()
	q := e.steerBox[intentID]
	if len(q) == 0 {
		return "", false
	}
	msg := q[0]
	if len(q) == 1 {
		delete(e.steerBox, intentID)
	} else {
		e.steerBox[intentID] = q[1:]
	}
	return msg, true
}

// steerHooks wraps the guard's hook runner so the planner can steer a running work:
// before each tool call it drains a queued course-correction (if any) and blocks the
// call, handing the message back to the model — which re-plans its next step instead
// of running the tool. No queued message → the guard behaves exactly as before.
// It also handles the "empty-turn" continuation nudge; see Stop.
type steerHooks struct {
	inner harness.HookRunner
	drain func() (string, bool)
	// nudges is how many empty-turn continuation nudges this intent has already been
	// given, capped at limit. Pointer: harness holds a value copy of steerHooks, so the
	// counter must be shared.
	nudges *atomic.Int64
	// limit is the cap on empty-turn continuation nudges, parsed by
	// Engine.emptyTurnNudgeLimit() from the "empty-response retries" count. <=0 = don't
	// intervene (the user explicitly disabled this layer).
	limit int
	// label looks like "worker-1 · #42"; used only for logging.
	label string
}

// Default number of continuation nudges for empty turns (thinking only, no body text and
// no tool call), kept consistent with the SDK's built-in empty-response-retry default
// (emptyResponseRetries in norma/llm/openai.go) — both layers share the same knob, so the
// unconfigured behavior should line up too. Parsing lives in Engine.emptyTurnNudgeLimit.
//
// Note this count is the "total per intent", not "consecutive": harness's own
// stopHookActive already limits consecutive empty turns to a single nudge — if the turn
// after a nudge is still empty, the Stop hook is no longer called and the run ends; the
// quota only refreshes once a real tool turn has happened (norma/harness/query.go:534). So
// this gate guards against the "tool → empty → nudge → tool → empty" pathological loop,
// keeping it from draining the intent budget.
const defaultEmptyTurnNudges = 2

// emptyTurnNudge is the continuation instruction injected on an empty turn.
//
// To harness such a turn looks like a natural end (stop_reason=end_turn with no tool_use),
// so none of the five LLM retry layers apply — it isn't an error, the model "finished
// thinking but didn't act". The SDK's empty-response retry can't reach it either: it judges
// emptiness by "did any event get yielded", and a thinking delta is itself an event
// (SEThinkingDelta in norma/llm/openai.go), so thinking-only doesn't count as empty.
// Besides, that layer resends the whole prompt verbatim, and for an empty turn shaped by the
// context, resending just makes the model think again. Here we instead append one
// instruction so it continues with the thinking it already produced — only a changed input
// gives it reason to behave differently.
const emptyTurnNudge = "Empty-turn reminder: your previous turn contained reasoning only, with no response and no tool call. " +
	"Execute the next step you already identified: call a tool or provide the conclusion. Do not repeat the reasoning."

// isThinkingOnlyTurn reports whether the latest assistant turn produced neither
// text nor a tool call — i.e. the model spent the whole round thinking.
func isThinkingOnlyTurn(messages []llm.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		m := messages[i]
		if m.Role != llm.RoleAssistant {
			continue
		}
		return strings.TrimSpace(m.Text()) == "" && len(m.ToolUses()) == 0
	}
	return false
}

func (h steerHooks) PreToolUse(ctx context.Context, name string, input []byte) (bool, string, []byte) {
	if msg, ok := h.drain(); ok {
		return true, "Planner correction: " + msg +
			"\nThis is the planner's current instruction for the intent. The tool call was not executed; adjust the next step and follow this instruction if it conflicts with the previous plan.", nil
	}
	if h.inner != nil {
		return h.inner.PreToolUse(ctx, name, input)
	}
	return false, "", nil
}

func (h steerHooks) PostToolUse(ctx context.Context, name string, input, result []byte, isErr bool) {
	if h.inner != nil {
		h.inner.PostToolUse(ctx, name, input, result, isErr)
	}
}

// Stop adds an "empty-turn" continuation layer on top of the guard's existing semantics:
// when the model outputs only thinking — no body text and no tool call — harness treats it
// as a natural end and settles with an empty summary (ReasonCompleted + asst.Text() in
// query.go), leaving an unfinished intent broken mid-way. In that case we inject a
// continuation instruction so the model carries on from the thinking it already has.
func (h steerHooks) Stop(ctx context.Context, messages []llm.Message) (bool, []string, string) {
	var (
		prevent  bool
		blocking []string
		msg      string
	)
	if h.inner != nil {
		prevent, blocking, msg = h.inner.Stop(ctx, messages)
	}
	// inner already decided to hard-stop, or is already injecting its own continuation
	// message → respect it, don't stack on top.
	// limit<=0 = the user set "empty-response retries" to -1, i.e. explicitly disabled this layer.
	if prevent || len(blocking) > 0 || h.nudges == nil || h.limit <= 0 || !isThinkingOnlyTurn(messages) {
		return prevent, blocking, msg
	}
	n := h.nudges.Add(1)
	if n > int64(h.limit) {
		log.Printf("[work %s] empty turn (thinking only, no body, no tool) hit the continuation-nudge cap %d, letting it settle", h.label, h.limit)
		return prevent, blocking, msg
	}
	log.Printf("[work %s] empty turn (thinking only, no body, no tool), injecting continuation instruction (%d/%d)", h.label, n, h.limit)
	return false, []string{emptyTurnNudge}, ""
}

// KillWork cancels the in-flight work running intentID (planner's kill_work tool).
// The work's agent-core session honors ctx cancellation and aborts promptly.
func (e *Engine) KillWork(intentID int64) error {
	e.workMu.Lock()
	run := e.work[intentID]
	e.workMu.Unlock()
	if run == nil {
		return fmt.Errorf("intent %d has no work currently running (it may have finished or not been claimed)", intentID)
	}
	run.cancel(agent.AbortKilledByPlanner)
	return nil
}

// Broadcaster exposes the engine's live activity pub/sub (used by the SSE handler).
func (e *Engine) Broadcaster() *Broadcaster { return e.bc }

// emitActivity persists one captured step AND fans it out to live subscribers,
// from a single point so storage and the SSE stream never diverge.
func (e *Engine) emitActivity(t *Task, r db.Activity) db.Activity {
	if t != nil && (r.Kind == "tool_use" || r.Kind == "tool_result") {
		r = e.resultShadowFor(t.ID).observe(r)
	}
	id, err := e.appendActivity(t, r)
	if err != nil {
		// NO LONGER SILENT: dropping a record breaks command↔result pairing in the
		// trace — a tool_use whose tool_result was lost shows as "in progress" forever, and
		// a lost 'result'/'round' record leaves the session with no summary ("no summary").
		// Everything needed to analyze the root cause goes into ONE error-level line: reason class,
		// summary preview, running drop count for this task, and — on the FK case — a
		// live probe of WHY the parent exploration is unreachable.
		n := e.bumpDrop(t.ID)
		diag := ""
		// On the FK-parent failure (23503) probe the live DB so the log records WHY the
		// exploration is unreachable (row gone / wrong expID) instead of just that it is.
		if isFKViolation(err) {
			storeID := t.Store.ID()
			if exists, refs, maxID, dErr := e.m.pg.ExplorationDiag(storeID); dErr != nil {
				diag = fmt.Sprintf(" | FK diagnostic query failed (store.expID=%d task.ExpID=%d): %v", storeID, t.ExpID, dErr)
			} else {
				diag = fmt.Sprintf(" | FK diagnostic: store.expID=%d task.ExpID=%d exploration_exists=%v tasks_referencing_it=%d MAX(exploration.id)=%d",
					storeID, t.ExpID, exists, refs, maxID)
			}
		}
		log.Printf("[activity] task %s dropped activity record (%d-th for this task) worker=%s kind=%s tool=%s tuid=%s reason=%s summary=%q: %v%s",
			t.ID, n, r.Worker, r.Kind, r.Tool, r.ToolUseID, dropReason(err), preview(r.Summary, 80), err, diag)
		e.touch(t.ID)
		return r
	}
	r.ID = id
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now()
	}
	e.bc.Publish(t.ID, r)
	e.touch(t.ID)
	return r
}

// appendActivity persists one activity row, retrying briefly on write failure.
// Concurrent planner + worker inserts into the same exploration's activity log
// occasionally fail; a couple of quick retries recover most. Crucially, every
// failure is now LOGGED (it used to be swallowed by an `if err == nil`), so the
// underlying DB error is finally visible for diagnosis.
func (e *Engine) appendActivity(t *Task, r db.Activity) (int64, error) {
	var id int64
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if id, err = t.Store.AppendActivity(r); err == nil {
			if attempt > 1 {
				log.Printf("[activity] task %s write succeeded on retry %d (worker=%s kind=%s tool=%s)",
					t.ID, attempt, r.Worker, r.Kind, r.Tool)
			}
			return id, nil
		}
		log.Printf("[activity] task %s write failed (attempt %d/3, worker=%s kind=%s tool=%s expID=%d): %v",
			t.ID, attempt, r.Worker, r.Kind, r.Tool, t.Store.ID(), err)
		time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
	}
	return 0, err
}

// SetReadiness wires the global "an LLM provider is configured" predicate (read by
// Ready() / the llm_configured indicator). Called once at startup.
func (e *Engine) SetReadiness(fn func() bool) { e.readiness = fn }

// SetAgentResolver installs a per-task planner/worker resolver (wired by the server).
// Called once at startup before any task loop runs, so no lock is needed on reads.
func (e *Engine) SetAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
	e.resolveAuthoritative = false
}

// SetAuthoritativeAgentResolver installs a resolver whose nil result must not
// fall through to the global provider. Task-level failover chains use this so a
// fully exhausted chain cannot silently bypass its configured boundary.
func (e *Engine) SetAuthoritativeAgentResolver(fn func(t *Task) (*agent.Planner, *agent.Worker)) {
	e.resolve = fn
	e.resolveAuthoritative = true
}

// snapshotFor returns the planner/worker a task should run on, from the task-router
// resolver. nil,nil means the task is deliberately unavailable (e.g. an exhausted
// failover chain); there is no global-pair fallback.
func (e *Engine) snapshotFor(t *Task) (*agent.Planner, *agent.Worker) {
	if e.resolve != nil {
		p, w := e.resolve(t)
		if (p != nil && w != nil) || e.resolveAuthoritative {
			return p, w
		}
	}
	return nil, nil
}

// Ready reports whether a global LLM provider is configured (via the readiness
// predicate wired at startup).
func (e *Engine) Ready() bool {
	return e.readiness != nil && e.readiness()
}

// ReadyFor reports whether a specific task can resolve a planner/worker pair.
// An explicit task profile chain can be runnable even when no global default
// provider is configured, so task status must not rely on Ready alone.
func (e *Engine) ReadyFor(t *Task) bool {
	p, w := e.snapshotFor(t)
	return p != nil && w != nil
}

// Run starts the planner loop + N worker loops for a task. The loops always run
// but no-op until an LLM is configured (so a task created while idle picks up
// automatically once LLM is set from the UI).
func (e *Engine) Run(ctx context.Context, t *Task) {
	workers := e.m.Workers()
	e.deleteMu.RLock()
	if e.IsDeleting(t.ID) {
		e.deleteMu.RUnlock()
		return
	}
	// Recovery can load a non-terminal task after prove_goal committed but before
	// the old process persisted task=done. Settle it before any worker is started,
	// even when an active intent would otherwise suppress the first planner wake.
	if e.completeIfPersistedGoalsMet(t) {
		e.deleteMu.RUnlock()
		return
	}
	if _, loaded := e.started.LoadOrStore(t.ID, true); loaded {
		e.deleteMu.RUnlock()
		t.Notify() // already running — just nudge a planning round
		return
	}
	rt := e.registerTaskRoutines(ctx, t.ID, 1+workers)
	e.deleteMu.RUnlock()
	e.touch(t.ID)
	reviewRequired := e.RefreshDiscoveryReviewGate(t.ID)
	runTaskRoutine(rt, func(loopCtx context.Context) { e.plannerLoop(loopCtx, t) })
	for i := 0; i < workers; i++ {
		name := fmt.Sprintf("work#%d", i+1)
		runTaskRoutine(rt, func(loopCtx context.Context) { e.workerLoop(loopCtx, t, name) })
	}
	e.startDeadlineCoordinator(ctx, t) // task-level timeout timer (only when timeout>0; deduped)
	// Only kick the first planning round when there is "no active intent at all (open+running)".
	// For a task with seed intents: the seed is already open, or a worker just started above has
	// already claimed it into running — both count as "has work to do", so skip the first planner
	// round; the worker claims the seed and runs it, and on completion NotifyDone/the heartbeat
	// wakes the planner.
	// ⚠️ Don't use Frontier (it only counts open): a worker's claim (open→running) races this
	// check and would cause a spurious kick.
	// On auto-recovery after restart only running intents may remain, which should also be skipped.
	gate := e.currentDiscoveryReviewGate(t.ID)
	if has, _ := t.Store.HasActiveIntent(); (reviewRequired && gate.reviewable) || (!reviewRequired && !has) {
		t.Notify() // kick the first planning round (acted on once LLM is ready)
	}
}

// plannerHeartbeatInterval resolves a task's planner heartbeat interval. db.CreateTask
// already normalizes it (anything below 600 is raised to 600); this is a second guard
// against an abnormal in-memory value.
func plannerHeartbeatInterval(t *Task) time.Duration {
	sec := t.PlanHeartbeatSeconds
	if sec < db.MinPlanHeartbeatSeconds { // floor = default = 600 (10min)
		sec = db.MinPlanHeartbeatSeconds
	}
	return time.Duration(sec) * time.Second
}

// resetPlannerTimer safely re-arms a Timer that may have already fired (the standard
// Stop→drain→Reset pattern).
func resetPlannerTimer(timer *time.Timer, d time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(d)
}

// completeTask persists success with first-terminal-writer-wins semantics and
// stops any remaining task work. An explicit planner verdict and the persistent
// all-goals-met predicate therefore have identical lifecycle behavior.
func (e *Engine) completeTask(t *Task, reason string) bool {
	if e == nil || e.m == nil || t == nil {
		return false
	}
	won, err := e.m.SetTaskStatusGuarded(t.ID, "done")
	if err != nil {
		log.Printf("[planner] task %s failed to persist completion: %v", t.ID, err)
		return false
	}
	if won {
		log.Printf("[planner] task %s completed: %s", t.ID, reason)
		e.emitActivity(t, db.Activity{Worker: "system", Kind: "text", Summary: reason})
		// Completion closes the whole task, so no sibling can claim more work and
		// in-flight workers settle as stopped rather than reopening their intents.
		e.cancelExec(t.ID, agent.AbortGoalMet)
	}
	return true
}

// completeIfPersistedGoalsMet makes prove_goal durable at task level. The
// planner ToolSet flag lasts for only one model run; the exploration graph is
// the authoritative state that survives another wake or a process restart.
func (e *Engine) completeIfPersistedGoalsMet(t *Task) bool {
	if t == nil || t.Store == nil {
		return false
	}
	allMet, total, err := t.Store.AllGoalsMet()
	if err != nil {
		log.Printf("[planner] task %s failed to read persistent goal completion: %v", t.ID, err)
		return false
	}
	if !allMet {
		return false
	}
	return e.completeTask(t, fmt.Sprintf("All %d persisted goals are proved; task complete", total))
}

func (e *Engine) plannerLoop(ctx context.Context, t *Task) {
	interval := plannerHeartbeatInterval(t)
	// The heartbeat timer is armed at loop entry = timed from task start: even for a seed
	// task that skips the first planner round (Run doesn't kick when the frontier is non-empty)
	// and blocks here, the heartbeat still fires the first planning round at "task start +
	// interval". Each later wake (edge/heartbeat) re-arms it = time since the last planning trigger.
	heartbeat := time.NewTimer(interval)
	defer heartbeat.Stop()

	// runRound runs one planning round (incl. debounce coalescing + the guards). src is only
	// used to distinguish the trigger source in logs.
	runRound := func(src string) {
		// debounce: coalesce a burst of changes into one planning round
		timer := time.NewTimer(e.debounce)
	drain:
		for {
			select {
			case <-t.notify:
			case <-timer.C:
				break drain
			}
		}
		if e.IsPaused(t.ID) {
			return // user-paused: don't plan
		}
		if e.IsDeleting(t.ID) {
			return
		}
		// terminal task (goals all met → done, or failed): the run is over. A
		// resume/nudge — e.g. auto-resume of the active task on restart — must NOT
		// re-plan (it would burn an LLM round and re-confirm a settled result).
		if isTerminalStatus(t.lifecycleSnapshot().Status) {
			return
		}
		// Reconsider automatic discovery only for a real graph/lifecycle edge.
		// A persistent target or binary failure must not become a heartbeat-driven
		// rescan loop. Initial admission, restore and resume trigger it directly.
		if src == "edge" {
			e.triggerKatanaAuto(t.ID)
		}
		// A prove_goal write is sufficient even when the completing planner
		// response was interrupted, the LLM is unavailable, or this is the first
		// wake after a restart.
		if e.completeIfPersistedGoalsMet(t) {
			return
		}
		planner, _ := e.snapshotFor(t)
		if planner == nil {
			return // idle until LLM configured
		}
		// During task-level timeout settlement: drop ordinary wakes — a worker's settle
		// write-back and Resume's Notify no longer trigger a regular planning round; the final
		// round is driven directly by the coordinator (settleTask), not through here.
		if e.isSettling(t.ID) {
			return
		}
		if gate := e.currentDiscoveryReviewGate(t.ID); gate.required && !gate.reviewable {
			return // automatic Katana is still active; its completion hook supplies the review wake
		}
		// goalless (manual direct-injection) branch: when the task has no open goals the planner
		// doesn't run — running it would re-judge met→cancelExec and kill intents the user
		// injected directly via the main agent. Completion is instead decided by the frontier:
		// still has open/running intents → stay running, wait silently; all intents drained → done.
		// This whole block is pure Go, triggers no LLM call, and emits no planning-round marker.
		if open, err := t.Store.HasOpenGoal(); err == nil && !open {
			t.drainTriggers() // drop accumulated done/finding triggers to avoid unbounded growth in a long goalless session
			if active, err := t.Store.HasActiveIntent(); err == nil && !active {
				// frontier drained and no running intents → settle. Use the Guarded CAS variant to
				// avoid colliding with a concurrent pause/delete/timeout-settlement state transition.
				if won, err := e.m.SetTaskStatusGuarded(t.ID, "done"); err != nil {
					log.Printf("[goalless] task %s failed to settle to done: %v", t.ID, err)
				} else if won {
					e.emitActivity(t, db.Activity{Worker: "system", Kind: "text",
						Summary: "All goals met; directly-injected intents have finished; task complete"})
				}
			}
			return // the goalless branch never enters planner.Plan
		}
		if !e.beginTaskOperation(t.ID) {
			return
		}
		defer e.decInflight(t.ID)
		e.touch(t.ID)
		emit := func(r db.Activity) { e.emitActivity(t, r) }
		ectx := e.clockCtx(e.execContextFor(ctx, t.ID), t, false) // cancellable by Pause; carries the task deadline
		if ectx.Err() != nil || e.IsDeleting(t.ID) {
			return
		}
		log.Printf("[planner] task %s planning…(triggered by %s)", t.ID, src)
		// round marker: each Plan() is one planner round; emit a boundary so the
		// UI can separate rounds in the transcript (kind='round').
		e.emitActivity(t, db.Activity{Worker: "planner", Kind: "round",
			Summary: fmt.Sprintf("Planning round %d", e.nextPlannerRound(t.ID))})
		// what fired this round (worker done / finding; may be several — debounce
		// coalesces a burst; empty for time/heartbeat wakes).
		triggers := t.drainTriggers()
		taskIDInt, _ := strconv.ParseInt(t.ID, 10, 64)
		e.BeginLLMCall(t.ID)
		met, reason, err := planner.Plan(ectx, taskIDInt, e.m.assets, t.Store, t.Goal, triggers, emit, t.Notify)
		e.EndLLMCall(t.ID)
		switch {
		case e.completeIfPersistedGoalsMet(t):
			log.Printf("[planner] task %s completed from persistent proved-goal state", t.ID)
		case err != nil && ectx.Err() == nil:
			log.Printf("[planner] task %s planning error: %v", t.ID, err)
		case met:
			log.Printf("[planner] task %s judged goals met: %s", t.ID, reason)
			// all goals met → persist task status as done (the frontend DTO shows this terminal state first).
			e.completeTask(t, "Planner proved the whole task complete: "+reason)
			// task judged complete → immediately cancel running workers: finishing their current
			// intents is now pointless. The next worker-loop pass hits the terminal gate and stops
			// claiming new intents; this cancelled batch takes the "task already complete" branch
			// below and is marked stopped (not blocked).
		default:
			log.Printf("[planner] task %s planning complete", t.ID)
		}
		e.touch(t.ID)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-t.notify:
			runRound("edge") // worker finished / finding / kill / resume / seed first round
		case <-heartbeat.C:
			// periodic backstop: deadlock backstop + wake to supervise in-flight workers (steer/kill) + periodic re-check.
			runRound("heartbeat")
		}
		// re-arm the heartbeat after each wake (edge or heartbeat): any planning trigger resets this idle timer.
		resetPlannerTimer(heartbeat, interval)
	}
}

func (e *Engine) workerLoop(ctx context.Context, t *Task, name string) {
	dispatch := workerDispatch{engine: e, task: t}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		worker, delay, stop := dispatch.next()
		if stop {
			return
		}
		if delay > 0 {
			if sleepCtx(ctx, delay) {
				return
			}
			continue
		}
		if !e.beginTaskOperation(t.ID) {
			return
		}
		claimed := e.runWorkerStep(ctx, t, name, worker)
		e.decInflight(t.ID)
		if !claimed && sleepCtx(ctx, 800*time.Millisecond) {
			return
		}
	}
}

// runWorkerStep claims one intent from the frontier and fully settles it via
// runIntent. Returns false when nothing was claimable. The pool worker loop is its
// only caller.
func (e *Engine) runWorkerStep(ctx context.Context, t *Task, name string, worker *agent.Worker) bool {
	intent := e.claimNext(t, name)
	if intent == nil {
		return false
	}
	log.Printf("[worker %s] task %s claimed intent #%d", name, t.ID, intent.ID)
	return e.runIntent(ctx, t, name, worker, intent, "", "")
}

// runIntent executes and fully settles one already-claimed (state=running) intent.
// Both the pool worker loop (via runWorkerStep) and the human-message handler (via
// runDetachedIntent, a dedicated goroutine outside the worker pool) call it, so the
// execute/retry/state-write logic lives in exactly one place. A non-empty message
// is injected as this turn's input through ExecuteWithMessage; requestID keys the
// transcript marker that dedups re-injection across model_error retries. The caller
// must already hold one task-operation admission for the whole sequence so a delete
// cannot observe quiescence between the LLM return and the final DB writes.
func (e *Engine) runIntent(ctx context.Context, t *Task, name string, worker *agent.Worker, intent *db.Node, requestID, message string) bool {
	run := intentExecution{engine: e, task: t, name: name, worker: worker, intent: intent, requestID: requestID, message: message}
	return run.execute(ctx)
}

// runDetachedIntent runs one paused intent OUTSIDE the worker pool in its own
// goroutine — the human-message path. It transitions the intent paused->running
// itself (never through 'open'), so the pool, which only claims 'open', can never
// race it; the "at most one run per intent" invariant still holds because winning
// the CAS is the sole entry and work[intentID] was cleared when the pause settled.
// Because it does not compete for a frontier slot, a user message continues the
// worker immediately even when all pool slots are busy (mirroring how the
// main-agent chat handler starts its run directly). The spawned goroutine owns one
// task-operation admission for the whole run and roots its context at ctx (pass the
// server root, never the HTTP request, so a disconnect cannot strand the run while
// task pause/delete/shutdown still stops it). Returns an error if the run could not
// be started; the intent is left untouched in that case.
func (e *Engine) runDetachedIntent(ctx context.Context, t *Task, intentID int64, requestID, message, agentMessage string) error {
	if !e.beginTaskOperation(t.ID) {
		return fmt.Errorf("task is being deleted")
	}
	release := true
	defer func() {
		if release {
			e.decInflight(t.ID)
		}
	}()
	_, worker := e.snapshotFor(t)
	if worker == nil {
		return fmt.Errorf("worker not ready yet")
	}
	node, err := t.Store.GetNode(intentID)
	if err != nil {
		return err
	}
	if node == nil || node.Kind != db.KindIntent {
		return fmt.Errorf("intent not found")
	}
	changed, err := t.Store.CompareAndSetIntentState(intentID, "paused", "running")
	if err != nil {
		return err
	}
	if !changed {
		return fmt.Errorf("%w: intent is no longer in paused state", db.ErrIntentStateConflict)
	}
	node.State, node.Owner = "running", "chat"
	// Record the human turn as a visible activity BEFORE the run starts, so it is
	// ordered ahead of any worker step and never appears without the run happening.
	// Keep the UI copy concise; ExecuteWithMessage writes the server-resolved
	// reference snapshot into the intent transcript as the LLM input.
	uid := intentID
	e.emitActivity(t, db.Activity{NodeID: &uid, Worker: "user", Kind: "user", Summary: message, Detail: message})
	release = false // ownership of the admission passes to the goroutine
	go func() {
		defer e.decInflight(t.ID)
		e.runIntent(ctx, t, "chat", worker, node, requestID, agentMessage)
	}()
	return nil
}

func taskExecutionPaused(cause error) bool {
	var abort *agent.AbortCause
	if !errors.As(cause, &abort) {
		return false
	}
	switch abort.Code {
	case "paused_by_user", "paused_by_orchestrator", "paused_on_reload", "paused_race_guard",
		"queued_for_admission", "llm_unavailable_queued", "task_deleted":
		return true
	default:
		return false
	}
}

func sleepCtx(ctx context.Context, d time.Duration) (done bool) {
	select {
	case <-ctx.Done():
		return true
	case <-time.After(d):
		return false
	}
}

func (e *Engine) claimNext(t *Task, name string) *db.Node {
	fr, _ := t.Store.Frontier(20)
	for _, in := range fr {
		if ok, _ := t.Store.ClaimIntent(in.ID, name); ok {
			return in
		}
	}
	return nil
}
