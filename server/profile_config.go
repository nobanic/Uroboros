package server

import (
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"log"
)

// profileCatalog owns profile reads, legacy persistence and invalidation.
type profileCatalog struct{ server *Server }

func (catalog profileCatalog) configuration(profile *db.LLMProfile) agent.Config {
	configuration := agent.ConfigFrom(profile.Format, profile.Model, profile.BaseURL, profile.APIKey, profile.Proxy)
	configuration.RatePerSecond, configuration.RatePerMinute = profile.RatePerSecond, profile.RatePerMinute
	configuration.ContextWindowK = profile.ContextWindowK
	configuration.ThinkingType = profile.ThinkingType
	configuration.ReasoningEffort = profile.ReasoningEffort
	configuration.Stream = profile.Streaming
	configuration.MaxTokens, configuration.MaxTokensField = profile.MaxTokens, profile.MaxTokensField
	configuration.SessionHeaderKey = profile.SessionHeaderKey
	catalog.server.applyProfileRetry(&configuration, profile)
	return configuration
}
func (catalog profileCatalog) active() (agent.Config, bool) {
	profile, err := catalog.server.m.pg.ActiveProfile()
	if err != nil || profile == nil {
		return agent.Config{}, false
	}
	configuration := catalog.configuration(profile)
	if configuration.APIKey == "" {
		return configuration, false
	}
	catalog.server.cfgMu.Lock()
	catalog.server.llmProf = profile.Name
	catalog.server.cfgMu.Unlock()
	return configuration, true
}
func (catalog profileCatalog) byID(id int64) (agent.Config, bool) {
	profile, err := catalog.server.m.pg.ProfileByID(id)
	if err != nil || profile == nil {
		return agent.Config{}, false
	}
	configuration := catalog.configuration(profile)
	if configuration.APIKey == "" {
		return configuration, false
	}
	return configuration, true
}
func (catalog profileCatalog) save(configuration agent.Config) error {
	// cfg.Provider() already returns one of the three valid format strings
	// (anthropic / openai / openai-responses), matching the DB CHECK constraint.
	format := configuration.Provider()
	var id int64
	// This legacy endpoint's request body has no round-robin/send-receive/output-limit parameters, so the values already stored in the DB are carried back as-is —
	// otherwise every save would silently reset the profile's priority, pool_exclude, streaming, and the output limit
	// (max_tokens / max_tokens_field) to zero values.
	var priority int
	var poolExclude bool
	streaming := true // legacy DB / new records default to streaming
	var maxTokens int
	var maxTokensField string
	var sessionHeaderKey string
	var retry db.RetryOverride
	if records, _ := catalog.server.m.pg.ListProfiles(); records != nil {
		for _, profile := range records {
			if profile.Name == "default" {
				id, priority, poolExclude, streaming = profile.ID, profile.Priority, profile.PoolExclude, profile.Streaming
				maxTokens, maxTokensField = profile.MaxTokens, profile.MaxTokensField
				sessionHeaderKey = profile.SessionHeaderKey
				retry = profile.Retry
				break
			}
		}
	}
	newID, err := catalog.server.m.pg.SaveProfile(&db.LLMProfile{
		ID: id, Name: "default", Format: format, Model: configuration.Model, BaseURL: configuration.BaseURL, Proxy: configuration.Proxy,
		APIKey: configuration.APIKey, RatePerSecond: configuration.RatePerSecond, RatePerMinute: configuration.RatePerMinute,
		ContextWindowK: configuration.ContextWindowK, ThinkingType: configuration.ThinkingType, ReasoningEffort: configuration.ReasoningEffort, IsDefault: true,
		Priority: priority, PoolExclude: poolExclude, Streaming: streaming,
		MaxTokens: maxTokens, MaxTokensField: maxTokensField, SessionHeaderKey: sessionHeaderKey,
		Retry: retry,
	})
	if err != nil {
		return err
	}
	return catalog.server.m.pg.SetActiveProfile(newID)
}
func (catalog profileCatalog) reapply() {
	configuration, ok := catalog.server.loadLLMConfig()
	if !ok {
		return
	}
	if err := catalog.server.applyLLM(configuration); err != nil {
		log.Printf("[engine] reapply active profile failed: %v", err)
		return
	}
	log.Printf("[engine] LLM reapplied from active profile: %s / %s", configuration.Provider(), configuration.Model)
}
func (catalog profileCatalog) invalidate() {
	catalog.server.profMu.Lock()
	catalog.server.profChatAgents = map[int64]*agent.ChatAgent{}
	catalog.server.profMu.Unlock()
	catalog.server.provCacheMu.Lock()
	catalog.server.provCacheGen++
	catalog.server.provByProfile = map[int64]*provEntry{}
	catalog.server.provCacheMu.Unlock()
	catalog.server.invalidateTaskAgents()
}
