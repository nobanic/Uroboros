package db

import "database/sql"

func profileFields(profile *LLMProfile, secret bool) []recordField {
	credential := field("api_key_hint", &profile.APIKeyHint).defaultRead("''")
	if secret {
		credential = field("api_key", &profile.APIKey).defaultRead("''")
	}
	return []recordField{
		field("id", &profile.ID).immutable(), field("name", &profile.Name), field("format", &profile.Format),
		field("base_url", &profile.BaseURL).defaultRead("''").nullableText(), field("proxy", &profile.Proxy).defaultRead("''").nullableText(),
		field("model", &profile.Model), credential,
		field("rate_per_second", &profile.RatePerSecond), field("rate_per_minute", &profile.RatePerMinute),
		field("context_window_k", &profile.ContextWindowK), field("reasoning_effort", &profile.ReasoningEffort).defaultRead("''"),
		field("is_default", &profile.IsDefault).immutable(), field("priority", &profile.Priority), field("pool_exclude", &profile.PoolExclude),
		field("thinking_type", &profile.ThinkingType).defaultRead("''"), field("streaming", &profile.Streaming).defaultRead("true"),
		field("max_tokens", &profile.MaxTokens).defaultRead("0"), field("max_tokens_field", &profile.MaxTokensField).defaultRead("''"),
		field("session_header_key", &profile.SessionHeaderKey).defaultRead("''"),
		field("retry_connect_attempts", &profile.Retry.Connect.Attempts).defaultRead("0"),
		field("retry_connect_interval_ms", &profile.Retry.Connect.IntervalMS).defaultRead("0"),
		field("retry_empty_attempts", &profile.Retry.Empty.Attempts).defaultRead("0"),
		field("retry_empty_interval_ms", &profile.Retry.Empty.IntervalMS).defaultRead("0"),
		field("retry_stream_attempts", &profile.Retry.Stream.Attempts).defaultRead("0"),
		field("retry_stream_interval_ms", &profile.Retry.Stream.IntervalMS).defaultRead("0"),
	}
}

func decodeProfile(row rowDecoder, secret bool) (*LLMProfile, error) {
	profile := new(LLMProfile)
	if failure := decodeRecord(row, profileFields(profile, secret)); failure != nil {
		return profile, failure
	}
	return profile, nil
}

func (database *DB) readProfiles(secret bool, suffix string, values ...any) ([]*LLMProfile, error) {
	statement := "SELECT " + recordProjection(profileFields(new(LLMProfile), secret)) + " FROM llm_profiles " + suffix
	return graphRows(database, statement, values, func(row rowDecoder) (*LLMProfile, error) { return decodeProfile(row, secret) })
}

func (database *DB) readProfile(suffix string, values ...any) (*LLMProfile, error) {
	statement := "SELECT " + recordProjection(profileFields(new(LLMProfile), true)) + " FROM llm_profiles " + suffix
	profile, failure := decodeProfile(database.QueryRow(statement, values...), true)
	if failure == sql.ErrNoRows {
		return nil, nil
	}
	return profile, failure
}

func (database *DB) storeProfile(input *LLMProfile) (int64, error) {
	profile := *input
	profile.Retry = profile.Retry.Clamped()
	if len(profile.APIKey) >= 4 {
		profile.APIKeyHint = "…" + profile.APIKey[len(profile.APIKey)-4:]
	}
	var columns []recordField
	for _, column := range profileFields(&profile, true) {
		if column.name == "api_key" {
			if profile.ID != 0 && profile.APIKey == "" {
				continue
			}
			if profile.ID == 0 {
				column = column.nullableText()
			}
		}
		columns = append(columns, column)
	}
	if profile.ID == 0 || profile.APIKey != "" {
		hint := field("api_key_hint", &profile.APIKeyHint)
		if profile.ID == 0 {
			hint = hint.nullableText()
		}
		columns = append(columns, hint)
	}
	return persistRecord(database, "llm_profiles", profile.ID, columns)
}
