package server

import (
	"context"

	"log"

	"strconv"

	"github.com/nobanic/UroborOS/db"
)

type goalSpec struct {
	Text      string
	VulnClass string
}

// launchTask runs the shared post-creation sequence for a task created via ANY
// path (HTTP createTask or orchestration spawn_task), avoiding copy-paste in two places::
//  1. seed root asset, feeding the event-driven loop; loop;
//  2. optional seed intent, so the worker starts right away without waiting for the first planner round;;
//  3. in the background, asynchronously decompose the goal (emit a 'round 0 goal decomposition' round + LLM decomposition steps + per-goal, visible on the page),),
//     after decomposition, call engine.Run -- the engine starts only once the goal nodes are ready, avoiding the race where the planner runs before the goals.
//
// asynchronous (goroutine), so the caller returns immediately; both paths behave the same: the task is created instantly and goals are decomposed in the background.
func (s *Server) launchTask(t *Task, seedText string, seedFirstIntent bool) {
	(taskBootstrap{server: s, t: t}).launch(seedText, seedFirstIntent)
}

func (s *Server) ensureKatanaAuto(taskID string) {
	if s == nil || s.m == nil || s.m.Katana() == nil {
		return
	}
	if s.engine != nil && s.engine.IsDeleting(taskID) {
		return
	}
	if task, ok := s.m.Task(taskID); ok {
		if task.SkipInitialKatana {
			return
		}
		lifecycle := task.lifecycleSnapshot()
		if lifecycle.Paused || lifecycle.Queued || isTerminalStatus(lifecycle.Status) {
			return
		}
	}
	numericID, err := strconv.ParseInt(taskID, 10, 64)
	if err != nil || numericID <= 0 {
		return
	}
	if ok, reason := s.m.Katana().Available(); !ok {
		log.Printf("[katana] task %s auto-discovery unavailable: %s", taskID, reason)
		return
	}
	job, started, err := s.m.Katana().EnsureAuto(numericID)
	if err != nil {
		log.Printf("[katana] task %s auto-discovery not started: %v", taskID, err)
		if s.engine != nil {
			s.engine.RefreshDiscoveryReviewGate(taskID)
		}
		return
	}
	if started && job != nil {
		log.Printf("[katana] task %s started core discovery job %d", taskID, job.ID)
	}
	if s.engine != nil {
		s.engine.RefreshDiscoveryReviewGate(taskID)
	}
}

func (s *Server) startTaskEngine(t *Task) { (taskBootstrap{server: s, t: t}).run() }

// seedBootstrapScout adds one bounded read-only surface-mapping intent after
// goal decomposition. It refuses to add work when a caller already supplied a
// seed intent, preserving explicit startup choices and preventing duplicate
// lanes. The bool tells startTaskEngine whether it must independently wake the
// planner alongside the worker.
func (s *Server) seedBootstrapScout(t *Task) bool {
	if t == nil || t.Store == nil {
		return false
	}
	if active, err := t.Store.HasActiveIntent(); err != nil {
		log.Printf("[bootstrap-scout] task %s: could not inspect active intents: %v", t.ID, err)
		return false
	} else if active {
		return false
	}

	payload := map[string]any{
		"summary":         "Build one compact initial evidence capsule for the explicitly scoped target: inspect existing assets and captured traffic first, then make only the minimal non-destructive requests needed to establish reachability, exposed services, application/framework/version clues, the authentication boundary, and concrete follow-up leads. Record the new evidence and finish this intent after that initial pass; do not brute-force, exploit, mutate application state, or widen scope.",
		"hypothesis_key":  "bootstrap-readonly-surface-map",
		"attack_role":     "surface_mapper",
		"stop_condition":  "Finish once the initial reachability, service/application/authentication fingerprint, and any observable version clues have been recorded as a compact fact; return deeper hypotheses to the planner instead of pursuing them in this intent.",
		"bootstrap_scout": true,
	}
	id, err := t.Store.AddIntent(payload, 10, nil, "bootstrap_scout")
	if err != nil {
		log.Printf("[bootstrap-scout] task %s: failed to issue intent: %v", t.ID, err)
		return false
	}
	if origin, _ := t.Store.OriginFactID(); origin > 0 {
		_ = t.Store.Link(origin, db.RelDerivedFrom, id)
	}
	log.Printf("[bootstrap-scout] task %s: issued bounded intent #%d; planner remains enabled", t.ID, id)
	return true
}

func (s *Server) occupiesConcurrencySlot(t *Task) bool { return (taskSchedule{server: s}).slot(t) }

func (s *Server) runningTaskCount(excludeID string) int {
	return (taskSchedule{server: s}).running(excludeID)
}

// admitTask is the single admission path for new, resumed, rerun and follow-up
// work. It atomically either starts the task or appends it to the persistent FIFO
// queue. mode is bootstrap for a freshly-created task and resume otherwise.
func (s *Server) admitTask(t *Task, mode string) (queued bool, err error) {
	return (taskSchedule{server: s}).admit(t, mode)
}

// admitPausedTask is the task-control resume path. The paused precondition is
// checked under the same scheduler lock as admission so a concurrent pause,
// dequeue or FIFO promotion cannot leave database and Engine state divergent.
func (s *Server) admitPausedTask(t *Task) (queued bool, err error) {
	return (taskSchedule{server: s}).resume(t)
}

func (s *Server) admitTaskWhen(t *Task, mode string, requirePaused bool) (queued bool, err error) {
	return (taskAdmission{server: s, t: t, mode: mode, requirePaused: requirePaused}).run()
}

func (s *Server) hasReadyQueuedTask(excludeID string) bool {
	return (taskSchedule{server: s}).backlog(excludeID)
}

func (s *Server) startAdmittedTask(t *Task, mode string) {
	(taskRunAdmission{server: s, t: t, mode: mode}).start()
}

func (s *Server) reconcileConcurrency() { (taskQueueSweep{server: s}).run() }

// reviveTask makes a stopped task run again: pull a terminal state (done/failed/timeout) back to running,
// release the pause, and (re)start the engine loop + wake. For a task already running and not paused: only the single
// Notify,, nearly no side effects. Used in two places: 'the main agent's set_goals adding a goal' and 'rerunning blocked intents'.
//
// why explicit revival is required: the planner/worker loop's terminal gate (engine.go) swallows ordinary notifies -- merely changing
// the graph + Notify will not wake a task already judged complete; after a restart the terminal task's goroutine may no longer exist, so Run is also needed.
func (s *Server) reviveTask(t *Task) { (taskSchedule{server: s}).revive(t) }

// createGoals materializes the goal node(s) under the task root (rel objective).
// Decomposition is done ENTIRELY by the LLM (the project requires an LLM). There is
// no rule-based fallback splitter — it only ever produced garbage (shredded URLs,
// meaningless 2-way splits). If the LLM yields nothing (an error), the raw task goal
// is used verbatim as a single goal so the task still has something to judge against.
// Returns the seeded specs so callers can emit activity records for them.
// emit, when non-nil, is forwarded to DecomposeGoals so LLM steps are visible in the UI.
func (s *Server) createGoals(ctx context.Context, t *Task, emit func(db.Activity)) []goalSpec {
	return (taskGoalMaterialization{server: s, t: t, ctx: ctx, emit: emit}).run()
}
