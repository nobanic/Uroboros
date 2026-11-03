package server

import (
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/norma/transcript"
	"path/filepath"
	"time"
)

func (s *Server) invalidateTaskAgents() {
	s.taskAgentMu.Lock()
	s.taskAgents = map[string]*taskAgentBundle{}
	s.taskAgentMu.Unlock()
}

func (s *Server) agentsForTask(t *Task) *taskAgentBundle {
	s.taskAgentMu.Lock()
	defer s.taskAgentMu.Unlock()
	if bundle := s.taskAgents[t.ID]; bundle != nil {
		return bundle
	}
	goalRuntime := &taskLLMRuntime{s: s, taskID: t.ID, agentKey: "goals"}
	plannerRuntime := &taskLLMRuntime{s: s, taskID: t.ID, agentKey: "planner"}
	workerRuntime := &taskLLMRuntime{s: s, taskID: t.ID, agentKey: "worker"}
	mainRuntime := &taskLLMRuntime{s: s, taskID: t.ID, agentKey: "mainagent"}
	tx := transcript.NewStore(filepath.Join(s.m.dir, "transcripts"))
	window := workerRuntime.CompactionWindow()
	wk := agent.NewWorker(workerRuntime, "task-router", s.m.dir, tx, window, s.agentMaxTurns("worker"))
	s.configureTaskAgentRuntime(wk, workerRuntime)
	wk.SetRunTimeout(time.Duration(s.agentRunSeconds("worker")) * time.Second)
	wk.SetProxy(s.m.ProxyAddr(), s.m.ProxyCACert())
	wk.SetWebSearch(s.webSearchFor("worker"))
	wk.SetConstraintInject(s.constraintInjectWorker) // inject operating constraints into the worker (configurable, on by default)
	pl := agent.NewPlanner(plannerRuntime, "task-router", s.m.dir, tx, plannerRuntime.CompactionWindow(), s.agentMaxTurns("planner"))
	s.configureTaskAgentRuntime(pl, plannerRuntime)
	pl.SetKillWork(s.engine.KillWork)
	pl.SetSteerWork(s.engine.SteerWork)
	pl.SetProxy(s.m.ProxyAddr(), s.m.ProxyCACert())
	pl.SetWebSearch(s.webSearchFor("planner"))
	pl.SetConstraintInject(s.constraintInjectPlanner) // inject operating constraints into the planner (configurable, on by default)
	// cold-digest §7: background compaction of cold nodes. What the engine actually drives via the
	// authoritative resolver is this per-task planner (agentsForTask), so the Compactor must attach
	// here. It uses the task-routed planner provider (§4: same model as the agent, resolved from the
	// task LLM chain), generating the body in one shot via Complete.
	pl.SetCompactor(agent.NewCompactor(plannerRuntime, "task-router"))
	main := agent.NewMainAgent(mainRuntime, "task-router", s.m.dir, tx, mainRuntime.CompactionWindow(), s.agentMaxTurns("mainagent"))
	s.configureTaskAgentRuntime(main, mainRuntime)
	main.SetProxy(s.m.ProxyAddr(), s.m.ProxyCACert())
	main.SetWebSearch(s.webSearchFor("mainagent"))
	main.SetSteerWork(s.engine.SteerWork) // steer_work: a human steering a running work in real time
	bundle := &taskAgentBundle{
		runtime: goalRuntime, plannerRuntime: plannerRuntime, workerRuntime: workerRuntime,
		mainRuntime: mainRuntime, pl: pl, wk: wk, main: main,
	}
	s.taskAgents[t.ID] = bundle
	return bundle
}

type taskRuntimeAgent interface {
	SetFindingRecorder(agent.FindingRecorder)
	SetCompactionWindowResolver(func() int)
	SetNonStreaming(func() bool)
	SetMaxTokens(func() int)
	SetNoaEnabled(func() bool)
}

// The five original contiguous setters remain at each role's original site.
// Proxy/search/control/timeout settings retain their individual positions.
func (s *Server) configureTaskAgentRuntime(target taskRuntimeAgent, runtime *taskLLMRuntime) {
	target.SetFindingRecorder(s.evidenceStore())
	target.SetCompactionWindowResolver(runtime.CompactionWindow)
	target.SetNonStreaming(runtime.nonStreaming)
	target.SetMaxTokens(runtime.maxTokens)
	target.SetNoaEnabled(s.m.NoaCompactionEnabled)
}
