package server

import (
	"context"
	"errors"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/llm"
)

func (admin llmAdministration) profiles() {
	if store := admin.server.pg(admin.writer); store != nil {
		profiles, err := store.ListProfiles()
		if admin.accept(500, err) {
			admin.reply(map[string]any{"profiles": llmProfileDTOs(profiles)})
		}
	}
}

func (admin llmAdministration) saveProfile() {
	store := admin.server.pg(admin.writer)
	if store == nil {
		return
	}
	var form profileSaveInput
	if !admin.input(&form) {
		return
	}
	profile := form.LLMProfile
	profile.APIKey, profile.Streaming = form.APIKey, form.Streaming == nil || *form.Streaming
	profile.MaxTokens = max(profile.MaxTokens, 0)
	if profile.Format != "openai" || profile.MaxTokensField != llm.MaxTokensFieldCompletion {
		profile.MaxTokensField = ""
	}
	id, err := store.SaveProfile(&profile)
	if !admin.accept(500, err) {
		return
	}
	admin.refreshProfiles()
	admin.reply(map[string]any{"id": id})
}

func (admin llmAdministration) refreshProfiles() {
	admin.server.invalidateProfileAgents()
	admin.server.reapplyActiveProfile()
}

func (admin llmAdministration) retryPolicy(save bool) {
	store := admin.server.pg(admin.writer)
	if store == nil {
		return
	}
	if save {
		var policy db.LLMRetryPolicy
		if !admin.input(&policy) || !admin.accept(500, store.SetLLMRetryPolicy(policy)) {
			return
		}
		admin.server.applyRetryPolicy()
		admin.refreshProfiles()
	}
	admin.reply(store.LLMRetryPolicy())
}

func (admin llmAdministration) activateProfile() {
	store := admin.server.pg(admin.writer)
	if store == nil {
		return
	}
	var form profileIDInput
	if !admin.input(&form) || !admin.accept(500, store.SetActiveProfile(form.ID)) {
		return
	}
	admin.refreshProfiles()
	admin.reply(map[string]any{"ok": true})
}

func profileDeletionReply(err error) (int, string) {
	switch {
	case errors.Is(err, db.ErrActiveLLMProfileDelete):
		return 409, "The currently active LLM configuration cannot be deleted; please activate another configuration first"
	case errors.Is(err, db.ErrLLMProfileReferencesChanged):
		return 409, "LLM configuration is being modified by a task or session; please retry"
	case errors.Is(err, context.DeadlineExceeded):
		return 409, "Timed out waiting for the LLM configuration reference to be released; please retry"
	case errors.Is(err, db.ErrLLMProfileNotFound):
		return 404, err.Error()
	default:
		return 500, err.Error()
	}
}

func (admin llmAdministration) deleteProfile() {
	store := admin.server.pg(admin.writer)
	if store == nil {
		return
	}
	id, _ := pathInt(admin.request, "id")
	if err := store.DeleteProfileContext(admin.request.Context(), id); err != nil {
		status, message := profileDeletionReply(err)
		writeErr(admin.writer, status, message)
		return
	}
	s := admin.server
	s.invalidateProfileAgents()
	s.llmHealth.Reset(id)
	s.restoreTasksAfterProfileDelete(store)
	s.reapplyActiveProfile()
	admin.reply(map[string]any{"deleted": id})
}

func (admin llmAdministration) pool(reset bool) {
	if admin.server.pg(admin.writer) == nil {
		return
	}
	if reset {
		var form profileIDInput
		if !admin.input(&form) {
			return
		}
		registry := admin.server.llmHealth
		if form.ID > 0 {
			registry.Reset(form.ID)
		} else {
			for id := range registry.Snapshot() {
				registry.Reset(id)
			}
		}
	}
	admin.reply(admin.server.llmPoolStatus())
}
