package server

import (
	"log"
	"net/http"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
)

// One request borrows the server's existing configuration/cache/runtime owners.
type llmAdministration struct {
	server  *Server
	writer  http.ResponseWriter
	request *http.Request
}

func (s *Server) llmAdmin(w http.ResponseWriter, r *http.Request) llmAdministration {
	return llmAdministration{server: s, writer: w, request: r}
}

func (admin llmAdministration) input(destination any) bool {
	return admin.accept(400, decode(admin.request, destination))
}

func (admin llmAdministration) accept(status int, err error) bool {
	if err == nil {
		return true
	}
	writeErr(admin.writer, status, err.Error())
	return false
}

func (admin llmAdministration) reply(document any) { writeJSON(admin.writer, 200, document) }

func (admin llmAdministration) storedProfile(id *int64) *db.LLMProfile {
	if id != nil {
		if profile, err := admin.server.m.pg.ProfileByID(*id); err == nil {
			return profile
		}
	}
	return nil
}

func (admin llmAdministration) globalKey() string {
	admin.server.cfgMu.Lock()
	defer admin.server.cfgMu.Unlock()
	return admin.server.llmCfg.APIKey
}

func (admin llmAdministration) current() {
	s := admin.server
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	cfg := s.llmCfg
	admin.reply(map[string]any{
		"configured": s.llmOn, "provider": cfg.Provider(), "model": cfg.Model,
		"base_url": cfg.BaseURL, "proxy": cfg.Proxy, "key_set": cfg.APIKey != "",
		"rate_per_second": cfg.RatePerSecond, "rate_per_minute": cfg.RatePerMinute,
		"context_window_k": cfg.ContextWindowK, "thinking_type": cfg.ThinkingType, "reasoning_effort": cfg.ReasoningEffort,
	})
}

func administrationModel(provider, model, base, key, proxy, thinking, reasoning string) agent.Config {
	cfg := agent.ConfigFrom(provider, model, base, key, proxy)
	cfg.ThinkingType, cfg.ReasoningEffort = thinking, reasoning
	return cfg
}

func (admin llmAdministration) configure() {
	var form legacyLLMInput
	if !admin.input(&form) {
		return
	}
	cfg := administrationModel(form.Provider, form.Model, form.BaseURL, form.APIKey, form.Proxy, form.ThinkingType, form.ReasoningEffort)
	cfg.RatePerSecond, cfg.RatePerMinute = form.RatePerSecond, form.RatePerMinute
	if form.ContextWindowK > 0 {
		cfg.ContextWindowK = min(form.ContextWindowK, 1000)
	}
	if cfg.APIKey == "" {
		cfg.APIKey = admin.globalKey()
	}
	if cfg.APIKey == "" {
		writeErr(admin.writer, 400, "api_key required")
		return
	}
	if _, err := cfg.NewProvider(); err != nil {
		writeErr(admin.writer, 400, "provider init failed: "+err.Error())
		return
	}
	s := admin.server
	if err := s.saveLLMConfig(cfg); err != nil {
		writeErr(admin.writer, 500, "persist provider failed: "+err.Error())
		return
	}
	s.invalidateProfileAgents()
	if _, ok := s.loadLLMConfig(); !ok {
		writeErr(admin.writer, 500, "saved provider is unavailable")
		return
	}
	if err := s.applyLLM(cfg); err != nil {
		writeErr(admin.writer, 400, "provider init failed: "+err.Error())
		return
	}
	log.Printf("[engine] LLM configured via UI: %s / %s", cfg.Provider(), cfg.Model)
	s.getLLM(admin.writer, admin.request)
}

func (admin llmAdministration) testConnection() {
	var form modelConnectionInput
	if !admin.input(&form) {
		return
	}
	cfg := administrationModel(form.Provider, form.Model, form.BaseURL, form.APIKey, form.Proxy, form.ThinkingType, form.ReasoningEffort)
	if form.Streaming != nil {
		cfg.Stream = *form.Streaming
	}
	cfg.SessionHeaderKey = form.SessionHeaderKey
	if form.ProfileID != nil && (cfg.APIKey == "" || cfg.SessionHeaderKey == "") {
		if profile := admin.storedProfile(form.ProfileID); profile != nil {
			if cfg.APIKey == "" {
				cfg.APIKey = profile.APIKey
			}
			if cfg.SessionHeaderKey == "" {
				cfg.SessionHeaderKey = profile.SessionHeaderKey
			}
		}
	}
	if cfg.APIKey == "" {
		cfg.APIKey = admin.globalKey()
	}
	if cfg.APIKey == "" {
		admin.reply(map[string]any{"ok": false, "error": "API Key not provided"})
		return
	}
	latency, reply, err := agent.TestConnection(admin.request.Context(), cfg)
	if err != nil {
		admin.reply(map[string]any{"ok": false, "error": err.Error()})
		return
	}
	admin.reply(map[string]any{"ok": true, "latency_ms": latency.Milliseconds(), "model": cfg.Model, "reply": truncateReply(reply)})
}
