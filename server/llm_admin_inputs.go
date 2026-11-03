package server

import "github.com/nobanic/UroborOS/db"

// Anonymous aliases preserve the established JSON decoder type/error identity.
type legacyLLMInput = struct {
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	BaseURL         string  `json:"base_url"`
	Proxy           string  `json:"proxy"`
	APIKey          string  `json:"api_key"`
	RatePerSecond   float64 `json:"rate_per_second"`
	RatePerMinute   float64 `json:"rate_per_minute"`
	ContextWindowK  int     `json:"context_window_k"`
	ThinkingType    string  `json:"thinking_type"`
	ReasoningEffort string  `json:"reasoning_effort"`
}

type modelConnectionInput = struct {
	Provider         string `json:"provider"`
	Model            string `json:"model"`
	BaseURL          string `json:"base_url"`
	Proxy            string `json:"proxy"`
	APIKey           string `json:"api_key"`
	ThinkingType     string `json:"thinking_type"`
	ReasoningEffort  string `json:"reasoning_effort"`
	ProfileID        *int64 `json:"profile_id"`
	Streaming        *bool  `json:"streaming"`
	SessionHeaderKey string `json:"session_header_key"`
}

type profileSaveInput = struct {
	db.LLMProfile
	APIKey    string `json:"api_key"`
	Streaming *bool  `json:"streaming"`
}

type profileIDInput = struct {
	ID int64 `json:"id"`
}

type modelDiscoveryInput = struct {
	Provider  string `json:"provider"`
	BaseURL   string `json:"base_url"`
	APIKey    string `json:"api_key"`
	Proxy     string `json:"proxy"`
	ProfileID *int64 `json:"profile_id"`
}
