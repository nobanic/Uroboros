package server

import (
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/llmrec"
	"github.com/nobanic/norma/llm"
	"log"
)

// profileProvider owns one original profile key and cache-generation snapshot.
type profileProvider struct {
	server *Server
	id     int64
}

func (provider profileProvider) resolve() (llm.Provider, agent.Config, bool) {
	provider.server.provCacheMu.Lock()
	if e := provider.server.provByProfile[provider.id]; e != nil {
		provider.server.provCacheMu.Unlock()
		return e.prov, e.cfg, true
	}
	generation := provider.server.provCacheGen
	provider.server.provCacheMu.Unlock()
	configuration, ok := provider.server.loadProfileConfig(provider.id)
	if !ok {
		return nil, agent.Config{}, false
	}
	backend, err := configuration.NewProvider()
	if err != nil {
		log.Printf("[engine] build provider for LLM profile %d failed: %v", provider.id, err)
		return nil, agent.Config{}, false
	}
	// Wrap with recorder, tagged with this profile's name.
	if profile, _ := provider.server.m.pg.ProfileByID(provider.id); profile != nil {
		backend = llmrec.Wrap(backend, provider.server.m.PG(), configuration.Model, profile.Name, configuration.ThinkingType, configuration.ReasoningEffort, provider.server.m.LLMRecordEnabled)
		backend = bindSideProvider(backend, configuration, provider.id, profile.Name)
	}
	return provider.publish(generation, backend, configuration)
}

// publish retains generation retry and the winner's shared rate limiter.
func (provider profileProvider) publish(generation uint64, backend llm.Provider, configuration agent.Config) (llm.Provider, agent.Config, bool) {
	provider.server.provCacheMu.Lock()
	if generation != provider.server.provCacheGen {
		provider.server.provCacheMu.Unlock()
		return provider.server.providerForProfile(provider.id)
	}
	if e := provider.server.provByProfile[provider.id]; e != nil { // lost the race → keep the winner
		backend, configuration = e.prov, e.cfg
	} else {
		provider.server.provByProfile[provider.id] = &provEntry{prov: backend, cfg: configuration}
		log.Printf("[engine] built provider for LLM profile %d (%s / %s)", provider.id, configuration.Provider(), configuration.Model)
	}
	provider.server.provCacheMu.Unlock()
	return backend, configuration, true

}
