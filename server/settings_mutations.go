package server

import (
	"errors"
	"strconv"
	"strings"

	"github.com/nobanic/UroborOS/db"
)

type settingsFault struct {
	status int
	cause  error
}

func (e *settingsFault) Error() string    { return e.cause.Error() }
func invalidSetting(message string) error { return &settingsFault{400, errors.New(message)} }

// Runtime settings deliberately commit in order. A later validation/storage
// failure leaves earlier writes and completed cache/queue effects intact.
type settingsWrite struct {
	enabled bool
	status  int
	rebuild bool
	apply   func() error
}

func optionalSetting[T any](value *T, status int, rebuild bool, apply func(T) error) settingsWrite {
	return settingsWrite{value != nil, status, rebuild, func() error { return apply(*value) }}
}

func (s *Server) settingsWrites(input runtimeSettingsInput) []settingsWrite {
	m := s.m
	flag := func(key string, value *bool) settingsWrite {
		return optionalSetting(value, 500, false, func(on bool) error { return m.pg.SetBool(key, on) })
	}
	return []settingsWrite{
		flag(settingConstraintsInjectPlanner, input.ConstraintsInjectPlanner),
		flag(settingConstraintsInjectWorker, input.ConstraintsInjectWorker),
		optionalSetting(input.NoaCompaction, 500, false, m.SetNoaCompaction),
		optionalSetting(input.EarlyBootstrapScout, 500, false, m.SetEarlyBootstrapScout),
		flag(settingNotifyEnabled, input.NotifyEnabled),
		optionalSetting(input.NotifyBaseURL, 500, false, func(value string) error {
			base := trimTrailingSlash(strings.TrimSpace(value))
			if base != "" && !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
				return invalidSetting("callback link address must start with http:// or https://")
			}
			return m.pg.SetSetting(settingNotifyPublicBaseURL, base)
		}),
		optionalSetting(input.NotifyDigestMins, 500, false, func(value int) error {
			if value < 1 || value > 24*60 {
				return invalidSetting("the digest interval must be between 1 and 1440 minutes")
			}
			return m.pg.SetSetting(settingNotifyDigestMinutes, strconv.Itoa(value))
		}),
		optionalSetting(input.Workers, 400, false, m.SetWorkers),
		{enabled: input.ConcurrencyEnabled != nil || input.ConcurrencyLimit != nil, status: 400, apply: func() error {
			on, limit := m.ConcurrencyLimit()
			if limit == 0 {
				limit = defaultConcurrencyLimit
			}
			if input.ConcurrencyEnabled != nil {
				on = *input.ConcurrencyEnabled
			}
			if input.ConcurrencyLimit != nil {
				limit = *input.ConcurrencyLimit
			}
			if err := m.SetConcurrency(on, limit); err != nil {
				return err
			}
			go s.reconcileConcurrency()
			return nil
		}},
		optionalSetting(input.PythonInterp, 500, false, func(value string) error { return m.pg.SetSetting(settingPythonInterp, strings.TrimSpace(value)) }),
		optionalSetting(input.LLMRecord, 500, false, m.SetLLMRecordEnabled),
		optionalSetting(input.LLMPoolEnabled, 500, true, m.SetLLMPoolEnabled),
		optionalSetting(input.LLMPoolBindFallback, 500, true, m.SetLLMPoolBindFallback),
		{enabled: input.LLMPoolEnabled != nil || input.LLMPoolBindFallback != nil, status: 500, apply: func() error { s.invalidateProfileAgents(); return nil }},
		flag(settingAgentTrafficBinding, input.AgentTrafficBinding),
		optionalSetting(input.TrafficCapture, 500, true, m.SetTrafficEnabled),
		optionalSetting(input.GlobalProxy, 400, true, m.SetGlobalProxy),
		{enabled: input.WebSearchEnabled != nil || input.WebSearchBackend != nil || input.BraveKey != nil || input.TavilyKey != nil || input.WebSearchProxy != nil, status: 500, rebuild: true, apply: func() error {
			on, backend, _, _, _ := m.WebSearch()
			if input.WebSearchEnabled != nil {
				on = *input.WebSearchEnabled
			}
			if input.WebSearchBackend != nil {
				backend = *input.WebSearchBackend
			}
			if !db.ValidWebSearchProvider(strings.TrimSpace(backend)) {
				return invalidSetting("audited web search backend must be brave-free, tavily or ddgs")
			}
			return m.SetWebSearch(on, backend, input.BraveKey, input.TavilyKey, input.WebSearchProxy)
		}},
		{enabled: input.WebSearchFallback != nil || input.ResearchTaskBudget != nil || input.ResearchPaidBudget != nil || input.ResearchRate != nil || input.ResearchCacheTTL != nil, status: 400, apply: func() error {
			return m.SetResearchPolicy(input.WebSearchFallback, input.ResearchTaskBudget, input.ResearchPaidBudget, input.ResearchRate, input.ResearchCacheTTL)
		}},
		optionalSetting(input.TorResearchEnabled, 500, false, m.SetTorResearchEnabled),
	}
}

func (s *Server) applyRuntimeSettings(input runtimeSettingsInput) *settingsFault {
	rebuild := false
	for _, write := range s.settingsWrites(input) {
		if !write.enabled {
			continue
		}
		if err := write.apply(); err != nil {
			var fault *settingsFault
			if errors.As(err, &fault) {
				return fault
			}
			return &settingsFault{write.status, err}
		}
		rebuild = rebuild || write.rebuild
	}
	if rebuild {
		s.cfgMu.Lock()
		config, enabled := s.llmCfg, s.llmOn
		s.cfgMu.Unlock()
		if enabled {
			if err := s.applyLLM(config); err != nil {
				return &settingsFault{500, err}
			}
		}
	}
	return nil
}
