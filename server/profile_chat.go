package server

import (
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/transcript"
	"path/filepath"
)

// profileChat retains binding/pin/global precedence and borrowed cache locks.
type profileChat struct{ server *Server }

func (chat profileChat) configure(runner *agent.ChatAgent, configuration agent.Config) {
	runner.SetProxy(chat.server.m.ProxyAddr(), chat.server.m.ProxyCACert())
	runner.SetWebSearch(chat.server.chatWebSearchOpts())
	runner.SetGuard(chat.server.chatGuard())
	runner.SetNonStreaming(nonStreamingResolver(configuration))
	runner.SetMaxTokens(maxTokensResolver(configuration))
	runner.SetNoaEnabled(chat.server.m.NoaCompactionEnabled)
}
func (chat profileChat) effective(agentKey string, pinID *int64) *int64 {
	if chat.server.m.pg != nil && agentKey != "" {
		if a, _ := chat.server.m.pg.GetAgentByKey(agentKey); a != nil && a.LLMProfileID != nil {
			return a.LLMProfileID
		}
	}
	return pinID
}
func (chat profileChat) resolve(c *db.Conversation) *agent.ChatAgent {
	runner := chat.server.chatAgentRef()
	if eff := chat.server.effectiveProfileForAgent(c.AgentKey, c.LLMProfileID); eff != nil {
		if pa := chat.server.chatAgentForProfile(*eff); pa != nil {
			runner = pa
		}
	}
	return runner
}
func (chat profileChat) unavailable() string {
	if chat.server.m.pg != nil {
		if profiles, err := chat.server.m.pg.ListProfiles(); err == nil && len(profiles) == 0 {
			return "Model Fabric is not configured yet: open System → Model Fabric and create a model profile"
		}
		if active, err := chat.server.m.pg.ActiveProfile(); err == nil && active == nil {
			return "No active model profile: open System → Model Fabric and set one active, or assign a profile to this conversation"
		}
	}
	return "Model Fabric is not ready for chat: check System → Model Fabric for an available, active profile"
}
func (chat profileChat) cached(id int64) *agent.ChatAgent {
	chat.server.profMu.Lock()
	cached := chat.server.profChatAgents[id]
	chat.server.profMu.Unlock()
	if cached != nil {
		return cached
	}
	backend, configuration, ok := chat.server.providerForProfile(id)
	if !ok {
		return nil
	}
	tx := transcript.NewStore(filepath.Join(chat.server.m.dir, "transcripts"))
	runner := agent.NewChatAgent(chat.server.poolForBinding(id, backend, configuration), configuration.Model, chat.server.m.dir, tx, configuration.CompactionWindow())
	chat.configure(runner, configuration) // experimental feature: noa context compaction (read per run)
	chat.server.profMu.Lock()
	if ex := chat.server.profChatAgents[id]; ex != nil { // lost the race → keep the winner
		runner = ex
	} else {
		chat.server.profChatAgents[id] = runner
	}
	chat.server.profMu.Unlock()
	return runner
}
