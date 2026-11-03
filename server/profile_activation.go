package server

import (
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/llmrec"
	"github.com/nobanic/norma/llm"
	"github.com/nobanic/norma/transcript"
	"net/url"
	"path/filepath"
	"strings"
)

// profileActivation publishes the original global provider/chat under cfgMu.
type profileActivation struct{ server *Server }

func (activation profileActivation) apply(configuration agent.Config) error {
	var backend llm.Provider
	// A persisted active profile must use the same cached provider as task chains
	// and Agent bindings, otherwise each path owns a separate rate limiter.
	if active, _ := activation.server.m.pg.ActiveProfile(); active != nil {
		if activeConfiguration, ok := activation.server.loadProfileConfig(active.ID); ok && activeConfiguration == configuration {
			backend, _, _ = activation.server.providerForProfile(active.ID)
		}
	}
	if backend == nil {
		var err error
		backend, err = configuration.NewProvider()
		if err != nil {
			return err
		}
		// Non-persisted/env configs do not have a profile cache key.
		activation.server.cfgMu.Lock()
		profileName := activation.server.llmProf
		activation.server.cfgMu.Unlock()
		backend = llmrec.Wrap(backend, activation.server.m.PG(), configuration.Model, profileName, configuration.ThinkingType, configuration.ReasoningEffort, activation.server.m.LLMRecordEnabled)
		backend = bindSideProvider(backend, configuration, 0, profileName)
	}
	activation.server.cfgMu.Lock()
	activation.server.llmDirect = backend
	activation.server.cfgMu.Unlock()
	// LLM round-robin (off by default): wraps the active configuration in a failover chain and automatically switches to the next one when the current configuration is unavailable.
	// Affects only the path that "uses the global active configuration" — agent-bound / task-pinned ones go through providerForProfile,
	// which still exclusively use that configuration by default (see poolForBinding). When disabled or with no alternative, the original provider is returned and behavior is unchanged.
	if act, err := activation.server.m.pg.ActiveProfile(); err == nil && act != nil {
		backend = activation.server.poolForActive(act.ID, backend, configuration)
	}
	// No global planner/worker pair: every task runs on its own task-routed pair
	// (agentsForTask), resolved through the engine's authoritative resolver. Global
	// readiness (Ready()/llm_configured) is reported from s.llmOn, set below.

	tx := transcript.NewStore(filepath.Join(activation.server.m.dir, "transcripts"))
	win := configuration.CompactionWindow()
	// chat stays the GLOBAL fallback since one ChatAgent serves many agent keys — its
	// per-agent binding is resolved at Chat time (runConversationSync →
	// chatAgentForProfile). The main-agent has no global instance: each task builds its
	// own via agentsForTask (task-routed), so nothing is constructed for it here.
	activation.server.cfgMu.Lock()
	// chat agent serves MANY custom agents by key → it holds the GLOBAL opts
	// (backend/key) and gates Enabled per-conversation-agent at Chat time. Conversations always use the active configuration.
	activation.server.chatAgent = agent.NewChatAgent(backend, configuration.Model, activation.server.m.dir, tx, win) // chat page runner
	(profileChat{server: activation.server}).configure(activation.server.chatAgent, configuration)                   // experimental feature: noa context compaction (read per run)
	activation.server.llmProv = backend
	activation.server.llmCfg = configuration
	activation.server.llmOn = true
	activation.server.cfgMu.Unlock()
	activation.server.invalidateTaskAgents()

	// wake the active task so a task created while idle starts exploring.
	if t := activation.server.m.ActiveTask(); t != nil {
		t.Notify()
	}
	return nil
}
func (activation profileActivation) host() string {
	activation.server.cfgMu.Lock()
	base := activation.server.llmCfg.BaseURL
	activation.server.cfgMu.Unlock()
	if base == "" {
		return ""
	}
	if u, err := url.Parse(base); err == nil {
		return strings.ToLower(u.Hostname())
	}
	return ""
}
