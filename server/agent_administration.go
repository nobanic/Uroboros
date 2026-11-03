package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
)

// Agent administration owns configuration and prompt commands, while the existing
// DB setters retain locking, normalization, versioning and persistence authority.
type agentAdministration struct {
	server *Server
	store  *db.DB
	record *db.Agent
}

type agentAdministrationError struct {
	status  int
	message string
}

func (e *agentAdministrationError) Error() string { return e.message }
func rejectAgentAdministration(status int, message string) error {
	return &agentAdministrationError{status: status, message: message}
}

func (s *Server) runAgentAdministration(w http.ResponseWriter, r *http.Request, command func(*agentAdministration) (any, error)) {
	store, record, found := s.agentByKey(w, r)
	if !found {
		return
	}
	result, failure := command(&agentAdministration{server: s, store: store, record: record})
	if failure == nil {
		writeJSON(w, http.StatusOK, result)
		return
	}
	status := http.StatusInternalServerError
	var rejection *agentAdministrationError
	if errors.As(failure, &rejection) {
		status = rejection.status
	}
	writeErr(w, status, failure.Error())
}

func agentAdministrationInput[T any](s *Server, w http.ResponseWriter, r *http.Request, command func(*agentAdministration, T) (any, error)) {
	s.runAgentAdministration(w, r, func(owner *agentAdministration) (any, error) {
		var input T
		if failure := decode(r, &input); failure != nil {
			return nil, rejectAgentAdministration(http.StatusBadRequest, failure.Error())
		}
		return command(owner, input)
	})
}

// Aliases preserve the original anonymous request types, including decoder error
// text, case-insensitive field matching, ignored unknown fields and JSON null.
type agentConfigurationInput = struct {
	MaxTurns           *int            `json:"max_turns"`
	RunSeconds         *int            `json:"run_seconds"`
	WebSearch          *bool           `json:"web_search"`
	InteractiveShell   *bool           `json:"interactive_shell"`
	LLMProfileID       json.RawMessage `json:"llm_profile_id"`
	TriggerRunMode     *string         `json:"trigger_run_mode"`
	TriggerMergeMode   *string         `json:"trigger_merge_mode"`
	TriggerMaxParallel *int            `json:"trigger_max_parallel"`
}
type agentPromptInput = struct{ Template, Note string }
type agentWrapupInput = struct {
	Prompt   string `json:"prompt"`
	MaxTurns *int   `json:"max_turns"`
}
type agentPreviewInput = struct {
	Template string            `json:"template"`
	Sample   map[string]string `json:"sample"`
}
type agentVisibilityInput = struct {
	MCP   []int64  `json:"mcp"`
	Skill []string `json:"skill"`
}

// These commands intentionally keep ordered partial persistence. A later failure
// does not roll back earlier successful setters or activate a partially saved edit.
func applyAgentAdministration(steps ...func() error) error {
	for _, step := range steps {
		if failure := step(); failure != nil {
			return failure
		}
	}
	return nil
}
func optionalAgentAdministration[T any](steps *[]func() error, input *T, persist func(T) error) {
	if input != nil {
		value := *input
		*steps = append(*steps, func() error { return persist(value) })
	}
}
func agentPatchValue[T any](previous T, input *T) T {
	if input != nil {
		return *input
	}
	return previous
}
func agentAdministrationOK(failure error) (any, error) { return map[string]any{"ok": true}, failure }

func (a *agentAdministration) configure(input agentConfigurationInput) (any, error) {
	steps := []func() error{}
	profileChanged := input.LLMProfileID != nil
	if profileChanged {
		var profile *int64
		if json.Unmarshal(input.LLMProfileID, &profile) != nil {
			return nil, rejectAgentAdministration(400, "llm_profile_id has an invalid format")
		}
		if profile != nil {
			if _, valid := a.server.loadProfileConfig(*profile); !valid {
				return nil, rejectAgentAdministration(400, "The specified LLM configuration does not exist or is invalid")
			}
		}
		steps = append(steps, func() error { return a.store.SetAgentLLMProfile(a.record.Key, profile) })
	}
	optionalAgentAdministration(&steps, input.MaxTurns, func(value int) error { return a.store.SetAgentMaxTurns(a.record.Key, max(0, value)) })
	optionalAgentAdministration(&steps, input.RunSeconds, func(value int) error { return a.store.SetAgentRunSeconds(a.record.Key, max(0, value)) })
	optionalAgentAdministration(&steps, input.WebSearch, func(value bool) error { return a.store.SetAgentWebSearch(a.record.Key, value) })
	optionalAgentAdministration(&steps, input.InteractiveShell, func(value bool) error { return a.store.SetAgentInteractiveShell(a.record.Key, value) })
	if input.TriggerRunMode != nil || input.TriggerMergeMode != nil || input.TriggerMaxParallel != nil {
		steps = append(steps, func() error {
			return a.store.SetAgentTriggerBehavior(a.record.Key,
				agentPatchValue(a.record.TriggerRunMode, input.TriggerRunMode), agentPatchValue(a.record.TriggerMergeMode, input.TriggerMergeMode), agentPatchValue(a.record.TriggerMaxParallel, input.TriggerMaxParallel))
		})
	}
	if failure := applyAgentAdministration(steps...); failure != nil {
		return nil, failure
	}
	a.server.activateAgentConfiguration(profileChanged)
	response := map[string]any{"ok": true}
	// HTTP echoes the submitted limits even when storage clamps negative values.
	if input.MaxTurns != nil {
		response["max_turns"] = *input.MaxTurns
	}
	if input.RunSeconds != nil {
		response["run_seconds"] = *input.RunSeconds
	}
	return response, nil
}

func (s *Server) activateAgentConfiguration(profileChanged bool) {
	if profileChanged {
		s.invalidateProfileAgents()
	}
	s.invalidateTaskAgents()
	s.cfgMu.Lock()
	configuration, enabled := s.llmCfg, s.llmOn
	s.cfgMu.Unlock()
	if enabled {
		_ = s.applyLLM(configuration)
	}
}

func (a *agentAdministration) visibility() (any, error) {
	mcp, _ := a.store.AgentVisible(a.record.ID, "mcp")
	skills, _ := a.store.AgentSkillNames(a.record.ID)
	if skills == nil {
		skills = []string{}
	}
	return map[string]any{"mcp": mcp, "skill": skills}, nil
}
func (a *agentAdministration) saveVisibility(input agentVisibilityInput) (any, error) {
	return agentAdministrationOK(applyAgentAdministration(
		func() error { return a.store.SetAgentVisibilityKind(a.record.ID, "mcp", input.MCP) },
		func() error { return a.store.SetAgentSkillVisibility(a.record.ID, input.Skill) },
	))
}

func (a *agentAdministration) detail() (any, error) {
	prompt, _ := a.store.CurrentPrompt(a.record.ID)
	variables, _ := a.store.PromptVars(a.record.ID)
	versions, _ := a.store.ListPromptVersions(a.record.ID)
	if versions == nil {
		versions = []db.PromptVersion{}
	}
	visible, _ := a.visibility()
	profiles, _ := a.store.ListProfiles()
	choices := make([]map[string]any, 0, len(profiles))
	for _, profile := range profiles {
		choices = append(choices, map[string]any{"id": profile.ID, "name": profile.Name, "model": profile.Model, "is_default": profile.IsDefault})
	}
	return map[string]any{
		"agent": agentDTO(a.record), "prompt": prompt, "variables": withGlobalVars(variables), "versions": versions,
		"visibility": visible, "llm_profiles": choices,
		"wrapup_prompt": a.record.WrapupPrompt, "wrapup_default": agent.WrapupDefault(a.record.Key),
		"wrapup_max_turns": a.record.WrapupMaxTurns, "wrapup_max_turns_default": agent.WrapupTurnsDefault(a.record.Key),
		"task_timeout_wrapup_supported": agent.TaskTimeoutWrapupDefault(a.record.Key) != "",
		"task_timeout_wrapup_prompt":    a.record.TaskTimeoutWrapupPrompt, "task_timeout_wrapup_default": agent.TaskTimeoutWrapupDefault(a.record.Key),
		"task_timeout_wrapup_max_turns": a.record.TaskTimeoutWrapupMaxTurns, "task_timeout_wrapup_max_turns_default": agent.WrapupTurnsDefault(a.record.Key),
	}, nil
}
func (a *agentAdministration) savePrompt(input agentPromptInput) (any, error) {
	variables, _ := a.store.PromptVars(a.record.ID)
	if rejection := validateTemplate(input.Template, withGlobalVars(variables)); rejection != "" {
		return nil, rejectAgentAdministration(400, rejection)
	}
	version, failure := a.store.SavePrompt(a.record.ID, input.Template, input.Note, "ui")
	return map[string]any{"version": version}, failure
}
func (a *agentAdministration) resetPrompt() (any, error) {
	template, exists := agent.BuiltinPromptSeeds()[a.record.Key]
	if !exists {
		return nil, rejectAgentAdministration(400, "this agent has no built-in default prompt and cannot be restored")
	}
	version, failure := a.store.ResetPromptToDefault(a.record.ID, template)
	return map[string]any{"version": version}, failure
}
func (a *agentAdministration) saveWrapup(input agentWrapupInput) (any, error) {
	steps := []func() error{func() error { return a.store.SetAgentWrapupPrompt(a.record.Key, input.Prompt) }}
	optionalAgentAdministration(&steps, input.MaxTurns, func(value int) error { return a.store.SetAgentWrapupMaxTurns(a.record.Key, max(0, value)) })
	return agentAdministrationOK(applyAgentAdministration(steps...))
}
func (a *agentAdministration) resetWrapup() (any, error) {
	zero := 0
	_, failure := a.saveWrapup(agentWrapupInput{MaxTurns: &zero})
	return map[string]any{"ok": true, "wrapup_default": agent.WrapupDefault(a.record.Key), "wrapup_max_turns_default": agent.WrapupTurnsDefault(a.record.Key)}, failure
}
func (a *agentAdministration) saveDeadlineWrapup(input agentWrapupInput) (any, error) {
	turns := a.record.TaskTimeoutWrapupMaxTurns
	if input.MaxTurns != nil {
		turns = max(0, *input.MaxTurns)
	}
	return agentAdministrationOK(a.store.SetAgentTaskTimeoutWrapup(a.record.Key, input.Prompt, turns))
}
func (a *agentAdministration) resetDeadlineWrapup() (any, error) {
	failure := a.store.SetAgentTaskTimeoutWrapup(a.record.Key, "", 0)
	return map[string]any{"ok": true, "task_timeout_wrapup_default": agent.TaskTimeoutWrapupDefault(a.record.Key), "task_timeout_wrapup_max_turns_default": agent.WrapupTurnsDefault(a.record.Key)}, failure
}
func (a *agentAdministration) versions() (any, error) {
	versions, failure := a.store.ListPromptVersions(a.record.ID)
	return map[string]any{"versions": versions}, failure
}
func (a *agentAdministration) variables() (any, error) {
	variables, failure := a.store.PromptVars(a.record.ID)
	return map[string]any{"variables": withGlobalVars(variables)}, failure
}
func (a *agentAdministration) preview(input agentPreviewInput) (any, error) {
	variables, _ := a.store.PromptVars(a.record.ID)
	if input.Template == "" {
		input.Template, _ = a.store.CurrentPrompt(a.record.ID)
	}
	rendered, failure := renderPrompt(input.Template, withGlobalVars(variables), input.Sample)
	if failure != nil {
		return map[string]any{"rendered": "", "error": failure.Error()}, nil
	}
	return map[string]any{"rendered": rendered}, nil
}
