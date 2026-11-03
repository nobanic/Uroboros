package server

import (
	"context"
	"fmt"
	"log"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/binaryintake"
)

// serverStartup retains constructor inputs independently of mutable Server
// options. Services that previously used the input manager/context must keep
// those inputs, while agent augmentation observes the configured skillDir.
type serverStartup struct {
	context context.Context
	manager *Manager
	skills  string
	data    string
	keys    string
	options []Option
}

func (startup serverStartup) start() *Server {
	s := startup.allocate()
	startup.configureAuthentication(s)
	startup.initializeServices(s)
	startup.connectExecution(s)
	startup.connectPersistence(s)
	startup.configureProvider(s)
	s.restoreTaskRuntimes()
	go s.reconcileConcurrency()
	s.startTaskArchiveWorker()
	s.wireInterceptReviewer()
	return s
}

func (startup serverStartup) allocate() *Server {
	s := &Server{m: startup.manager, ctx: startup.context, skillDir: startup.skills}
	s.engine = NewEngine(startup.manager)
	s.chatBusy = make(map[string]bool)
	s.chatCancel = make(map[string]context.CancelCauseFunc)
	s.triggerQ = make(map[string][]triggeredRun)
	s.triggerActive = make(map[string]int)
	s.triggerCfg = make(map[string]triggerBehavior)
	s.profChatAgents = make(map[int64]*agent.ChatAgent)
	s.provByProfile = make(map[int64]*provEntry)
	s.llmHealth = newLLMHealthRegistry(startup.manager.pg)
	s.taskAgents = make(map[string]*taskAgentBundle)
	s.archiveWake = make(chan struct{}, 1)
	for _, configure := range startup.options {
		if configure != nil {
			configure(s)
		}
	}
	return s
}

func (startup serverStartup) configureAuthentication(s *Server) {
	keyDir, dataDir := startup.keys, startup.data
	if s.authDisabled {
		log.Printf("[auth] WARNING: authentication is disabled; bind this runtime to loopback only")
	} else {
		key, err := loadOrCreateJWTKey(keyDir, dataDir)
		if err != nil {
			log.Fatalf("[auth] JWT key: %v", err)
		}
		s.jwtKey = key
	}
}

func (startup serverStartup) initializeServices(s *Server) {
	m, ctx := startup.manager, startup.context
	s.research = newResearchBroker(m.pg, m.ResearchRuntimeConfig)
	s.vulnerability = newVulnerabilityBroker(m.pg, m.ResearchRuntimeConfig)
	s.torResearch = newTorResearchBroker(m.pg, m.ResearchRuntimeConfig)
	if s.binaryIntake != nil {
		s.binaryTrafficQ = make(chan binaryintake.TrafficOrigin, 256)
		go s.runBinaryIntakeQueue()
	}
	if s.binaryRunner != nil {
		go s.binaryRunner.Run(ctx)
	}
	s.initSideQuestions()
	s.applyRetryPolicy()
}

func (startup serverStartup) connectExecution(s *Server) {
	m := startup.manager
	s.engine.SetAuthoritativeAgentResolver(func(t *Task) (*agent.Planner, *agent.Worker) {
		if !s.taskRuntimeAvailable(t, "planner", "worker") {
			return nil, nil
		}
		b := s.agentsForTask(t)
		return b.pl, b.wk
	})
	s.engine.SetReadiness(func() bool {
		s.cfgMu.Lock()
		defer s.cfgMu.Unlock()
		return s.llmOn
	})
	s.engine.SetKatanaAutoTrigger(s.ensureKatanaAuto)
	s.engine.SetDiscoveryReviewGate(s.DiscoveryReviewGate)
	if runner := m.Katana(); runner != nil {
		runner.SetCompletionHook(func(taskID, jobID int64) {
			s.enqueueKatanaBinaryTraffic(taskID, jobID)
			id := fmt.Sprint(taskID)
			s.engine.RefreshDiscoveryReviewGate(id)
			if task, ok := s.m.Task(id); ok {
				task.Notify()
			}
		})
	}
	if runner := m.Arsenal(); runner != nil {
		runner.SetCompletionHook(func(taskID, jobID int64) {
			s.enqueueArsenalBinaryTraffic(taskID, jobID)
			id := fmt.Sprint(taskID)
			s.ensureKatanaAuto(id)
			s.engine.RefreshDiscoveryReviewGate(id)
			if task, ok := s.m.Task(id); ok {
				task.Notify()
			}
		})
	}
}

func (startup serverStartup) connectPersistence(s *Server) {
	if startup.manager.pg == nil {
		return
	}
	startup.connectAgentPrompts()
	startup.connectToolCatalog(s)
	startup.startPersistentServices(s)
}

func (startup serverStartup) connectAgentPrompts() {
	m := startup.manager
	agent.PromptOverride = func(key string) (string, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil {
			return "", false
		}
		t, err := m.pg.CurrentPrompt(a.ID)
		if err != nil || t == "" {
			return "", false
		}
		return t, true
	}
	agent.WrapupOverride = func(key string) (string, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil || a.WrapupPrompt == "" {
			return "", false
		}
		return a.WrapupPrompt, true
	}
	agent.WrapupMaxTurnsOverride = func(key string) (int, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil || a.WrapupMaxTurns <= 0 {
			return 0, false
		}
		return a.WrapupMaxTurns, true
	}
	agent.WrapupTaskTimeoutOverride = func(key string) (string, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil || a.TaskTimeoutWrapupPrompt == "" {
			return "", false
		}
		return a.TaskTimeoutWrapupPrompt, true
	}
	agent.WrapupTaskTimeoutTurnsOverride = func(key string) (int, bool) {
		a, err := m.pg.GetAgentByKey(key)
		if err != nil || a == nil || a.TaskTimeoutWrapupMaxTurns <= 0 {
			return 0, false
		}
		return a.TaskTimeoutWrapupMaxTurns, true
	}
}

func (startup serverStartup) connectToolCatalog(s *Server) {
	m := startup.manager
	wireAgentAugment(m.pg, s.skillDir, s.hostTools)
	registry := buildDomainReg(m.Assets())
	wireTools(m.pg, registry, s)
	seedPrompts(m.pg)
	s.seedOrchestrationTools()
	if err := s.seedTrafficSchemaTool(); err != nil {
		log.Printf("[traffic-schema] seed tool: %v", err)
	}
	if err := s.seedFindingRetester(); err != nil {
		log.Printf("[retester] seed: %v", err)
	}
	if err := s.seedExperimentTools(); err != nil {
		log.Printf("[api-lab] seed: %v", err)
	}
}

func (startup serverStartup) startPersistentServices(s *Server) {
	go s.evidenceStore().RunGC(s.ctx)
	s.seedPythonInterpreter()
	go newScheduler(s).Run(s.ctx)
	go newNotifier(s).Run(s.ctx)
	go s.discoverEmptyMCPsOnStartup()
	logSink.SetDB(startup.context, startup.manager.pg)
}

func (startup serverStartup) configureProvider(s *Server) {
	if cfg, ok := s.loadLLMConfig(); ok {
		if err := s.applyLLM(cfg); err != nil {
			log.Printf("[engine] saved LLM config init failed — engine idle: %v", err)
		} else {
			log.Printf("[engine] LLM configured from DB: %s / %s", cfg.Provider(), cfg.Model)
		}
	} else if cfg, ok := agent.FromEnv(); ok {
		if err := s.applyLLM(cfg); err != nil {
			log.Printf("[engine] env provider init failed — engine idle: %v", err)
		} else {
			log.Printf("[engine] LLM configured from env: %s / %s", cfg.Provider(), cfg.Model)
		}
	} else {
		log.Printf("[engine] no LLM provider configured — engine idle until set via /api/llm or env")
	}
}
