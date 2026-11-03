package server

import (
	"context"
	"fmt"
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"log"
	"strconv"
	"strings"
)

// taskBootstrap preserves seeding, goal materialization and Engine start order.
type taskBootstrap struct {
	server *Server
	t      *Task
}

// taskRunAdmission starts work only after persistent admission has committed.
type taskRunAdmission struct {
	server *Server
	t      *Task
	mode   string
}

// taskGoalMaterialization keeps the original caller context and activity sink;
// its provider phase retains the exact task runtime and balanced call accounting.
type taskGoalMaterialization struct {
	server *Server
	t      *Task
	ctx    context.Context
	emit   func(db.Activity)
}

func (command taskBootstrap) launch(seedText string, seedFirstIntent bool) {
	if !command.server.engine.beginTaskOperation(command.t.ID) {
		return
	}
	command.server.seed(command.t, seedText)
	if seedFirstIntent {
		command.server.seedFirstIntent(command.t)
	}
	command.server.engine.decInflight(command.t.ID)
	if _, err := command.server.admitTask(command.t, "bootstrap"); err != nil {
		log.Printf("[concurrency] task %s failed to start: %v", command.t.ID, err)
	}
}

func (command taskBootstrap) run() {
	ctx := command.server.engine.execContextFor(command.server.ctx, command.t.ID)
	if ctx.Err() != nil || command.server.engine.IsDeleting(command.t.ID) {
		return
	}
	command.server.engine.emitActivity(command.t, db.Activity{Worker: "planner", Kind: "round",
		Summary: "Round 0 goal decomposition"})
	goals := command.server.createGoals(ctx, command.t, func(r db.Activity) {
		command.server.engine.emitActivity(command.t, r)
	})
	if ctx.Err() != nil || command.server.engine.IsDeleting(command.t.ID) {
		return
	}
	for _, g := range goals {
		summary := g.Text
		if g.VulnClass != "" {
			summary = fmt.Sprintf("[%s] %s", g.VulnClass, g.Text)
		}
		command.server.engine.emitActivity(command.t, db.Activity{Worker: "planner", Kind: "text", Summary: summary})
	}
	if ctx.Err() != nil || command.server.engine.IsDeleting(command.t.ID) {
		return
	}
	// Optional startup experiment: after the authoritative goals and task scope
	// exist, seed one deliberately narrow, non-destructive reconnaissance lane.
	// Engine.Run normally suppresses its first planner wake whenever any intent is
	// active, so remember whether this particular intent was added and explicitly
	// wake the planner below. The scout accelerates evidence collection; it never
	// replaces planning or terminates another branch.
	bootstrapScout := false
	if command.server.initialScoutEnabled(command.t) {
		bootstrapScout = command.server.seedBootstrapScout(command.t)
	}
	command.server.engine.Run(command.server.ctx, command.t)
	if bootstrapScout {
		command.t.Notify()
	}
}

func (s *Server) initialScoutEnabled(t *Task) bool {
	if t != nil && t.InitialScout != nil {
		return *t.InitialScout
	}
	return s.m.EarlyBootstrapScoutEnabled()
}

func (command taskRunAdmission) start() {
	// Discovery is task infrastructure, not an LLM decision. Admission has
	// already committed queued=false, so automatic Katana never consumes runtime
	// resources for a task still waiting in the concurrency FIFO.
	command.server.ensureKatanaAuto(command.t.ID)
	if command.mode == "bootstrap" {
		if !command.server.engine.beginTaskOperation(command.t.ID) {
			return
		}
		// A first-run task may have kept the Engine pause barrier while waiting
		// in the concurrency queue. Clear it only after operation admission, or
		// startTaskEngine's first execContextFor call would return a cancelled
		// context and silently strand the dequeued task.
		if command.server.engine.IsPaused(command.t.ID) {
			command.server.engine.Resume(command.t)
		}
		go func() {
			defer command.server.engine.decInflight(command.t.ID)
			command.server.startTaskEngine(command.t)
		}()
		return
	}
	command.server.engine.Run(command.server.ctx, command.t)
	command.server.engine.Resume(command.t)
	// Run returns early for an already-started task. Explicitly ensure a timeout
	// coordinator exists after resume; resetTimeoutRevival cleared the completed
	// coordinator's marker and the next real call will stamp a fresh deadline.
	command.server.engine.startDeadlineCoordinator(command.server.ctx, command.t)
}

func (command taskGoalMaterialization) decompose(as *db.AssetStore) []agent.GoalSpec {
	taskID, _ := strconv.ParseInt(command.t.ID, 10, 64)
	command.server.engine.BeginLLMCall(command.t.ID)
	goalRuntime := command.server.agentsForTask(command.t).runtime
	decomposed := agent.DecomposeGoalsWithProvider(command.ctx, goalRuntime, command.server.m.dir, command.t.Goal, command.t.Description, as, command.t.Store, taskID, goalRuntime.nonStreaming(), goalRuntime.maxTokens(), command.emit)
	command.server.engine.EndLLMCall(command.t.ID)
	return decomposed
}
func (command taskGoalMaterialization) run() []goalSpec {
	if command.t == nil {
		return nil
	}
	// Use the SAME LLM the task runs on (its pinned profile, else the active profile),
	// NOT agent.FromEnv() — the LLM is configured via the UI (DB profile), not env vars,
	// so FromEnv returned empty and every task silently fell back to the crude rule
	// splitter (which shredded URLs / made meaningless 2-way splits).
	var specs []goalSpec
	var as *db.AssetStore
	if command.server.m != nil {
		as = command.server.m.Assets()
	}
	decomposed := command.decompose(as)
	for _, g := range decomposed {
		if strings.TrimSpace(g.Text) != "" {
			specs = append(specs, goalSpec{Text: g.Text, VulnClass: g.VulnClass})
		}
	}
	if len(specs) == 0 {
		// No decomposed goals (LLM error / no provider): use the raw task goal verbatim
		// as a single goal so the task still has something to judge against. This is the
		// only path that writes here — decomposed goals are already persisted by the tool.
		if g := strings.TrimSpace(command.t.Goal); g != "" {
			log.Printf("[goals] task %s: LLM goal decomposition produced no output; falling back to 'the original goal as a single goal'", command.t.ID)
			origin, _ := command.t.Store.OriginFactID()
			id, _ := command.t.Store.AddNode(db.KindGoal, map[string]any{"text": g}, 0, "open", "system", nil)
			if origin > 0 && id > 0 {
				_ = command.t.Store.Link(origin, db.RelSpawns, id) // goal descends from the task root (origin fact)
			}
			specs = []goalSpec{{Text: g}}
		}
	}
	return specs
}
