package llmrec

import (
	"encoding/json"
	"log"
	"time"

	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/norma/llm"
)

// recordingUsage is the always-on lightweight projection; it never holds bodies.
type recordingUsage struct {
	recorder       *Recorder
	taskID, worker string
	explorationID  int64
	usage          llm.Usage
	latencyMs      int
	status         string
}

func (u recordingUsage) persist() {
	if u.recorder.pg == nil {
		return
	}
	if u.recorder.model == "" && u.usage.InputTokens == 0 && u.usage.OutputTokens == 0 &&
		u.usage.CacheReadTokens == 0 && u.usage.CacheWriteTokens == 0 {
		return
	}
	row := &db.LLMUsage{TaskID: u.taskID, ExplorationID: u.explorationID, Worker: u.worker,
		Model: u.recorder.model, ProfileName: u.recorder.prof, LatencyMs: u.latencyMs,
		InputTokens: u.usage.InputTokens, OutputTokens: u.usage.OutputTokens,
		CacheRead: u.usage.CacheReadTokens, CacheWrite: u.usage.CacheWriteTokens, Status: u.status}
	if err := u.recorder.pg.InsertLLMUsage(row); err != nil {
		log.Printf("[llmusage] insert: %v", err)
	}
}

// recordingExchange owns one synchronous body write. A failed observational
// write is logged, leaving the provider's return value and error untouched.
type recordingExchange struct {
	recorder                *Recorder
	session, taskID, worker string
	requestBody             string
	capture                 *Capture
	start                   time.Time
	text, thinking          string
	usage                   llm.Usage
	stopReason              string
	err                     error
}

func (e recordingExchange) persist() {
	latency := int(time.Since(e.start).Milliseconds())
	status, message := "ok", ""
	if e.err != nil {
		status, message = "error", e.err.Error()
	}
	response := map[string]any{"text": e.text, "stop_reason": e.stopReason,
		"usage": map[string]int{"input_tokens": e.usage.InputTokens, "output_tokens": e.usage.OutputTokens,
			"cache_read_tokens": e.usage.CacheReadTokens, "cache_write_tokens": e.usage.CacheWriteTokens}}
	if e.thinking != "" {
		response["thinking"] = e.thinking
	}
	encoded, _ := json.Marshal(response)
	row := &db.LLMRecord{SessionID: e.session, TaskID: e.taskID, Worker: e.worker, Model: e.recorder.model,
		ProfileName: e.recorder.prof, LatencyMs: latency, InputTokens: e.usage.InputTokens, OutputTokens: e.usage.OutputTokens,
		CacheRead: e.usage.CacheReadTokens, CacheWrite: e.usage.CacheWriteTokens, Status: status, Error: message,
		RequestBody: e.requestBody, ResponseBody: string(encoded), RawRequest: e.capture.RawRequest(), RawResponse: e.capture.RawResponse()}
	if err := e.recorder.pg.InsertLLMRecord(row); err != nil {
		log.Printf("[llmrec] insert: %v", err)
	}
}

// recordingRequest retains the existing normalized document; full tool schemas
// and untouched provider framing remain exclusively in the raw Capture.
type recordingRequest struct {
	request                       llm.CompletionRequest
	thinkingType, reasoningEffort string
}

func (r recordingRequest) json() string {
	document := map[string]any{"system": r.request.System, "messages": r.request.Messages, "max_tokens": r.request.MaxTokens}
	thinking := r.thinkingType
	if r.request.Thinking != "" {
		thinking = r.request.Thinking
	}
	if thinking != "" || r.reasoningEffort != "" {
		document["thinking"] = map[string]string{"type": thinking, "effort": r.reasoningEffort}
	}
	if len(r.request.Tools) > 0 {
		names := make([]string, len(r.request.Tools))
		for index, definition := range r.request.Tools {
			names[index] = definition.Name
		}
		document["tools"], document["tools_count"] = names, len(r.request.Tools)
	}
	if r.request.Temperature != nil {
		document["temperature"] = *r.request.Temperature
	}
	if len(r.request.Stop) > 0 {
		document["stop"] = r.request.Stop
	}
	encoded, _ := json.Marshal(document)
	return string(encoded)
}
