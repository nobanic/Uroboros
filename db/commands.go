package db

import "time"

// CommandRecord is a paired tool_use + tool_result from the activity table
// (any tool, not just Bash). Command holds the raw tool input (JSON).
type CommandRecord struct {
	ID        int64     `json:"id"`
	ExpID     int64     `json:"exploration_id"`
	Worker    string    `json:"worker"`
	Tool      string    `json:"tool"`
	Command   string    `json:"command"`
	Output    string    `json:"output"`
	IsError   bool      `json:"is_error"`
	CreatedAt time.Time `json:"created_at"`
}

// ToolStat is one tool's execution tally for the usage summary.
type ToolStat struct {
	Tool   string `json:"tool"`
	Total  int    `json:"total"`
	Errors int    `json:"errors"`
}

// ToolStats counts executions grouped by tool under the same filters
// ListCommands takes. Unpaginated on purpose: the tally describes the whole
// filtered set, not the page currently on screen.
func (d *DB) ToolStats(expID *int64, q string) ([]ToolStat, error) {
	return executionCommandStats.all(d, executionCommandCriteria(expID, q))
}

// ListCommands returns tool executions (tool_use + paired tool_result) across all
// explorations, with optional filtering and pagination. Covers every tool, not
// just Bash; q matches the tool name or its input.
func (d *DB) ListCommands(expID *int64, q string, page, size int) ([]CommandRecord, int, error) {
	return executionCommandRows.page(d, executionCommandCriteria(expID, q), page, size)
}

// LLMRecord is one recorded LLM API call (request + response).
type LLMRecord struct {
	ID           int64     `json:"id"`
	Ts           time.Time `json:"ts"`
	Model        string    `json:"model"`
	ProfileName  string    `json:"profile_name"`
	SessionID    string    `json:"session_id"`
	TaskID       string    `json:"task_id"`
	Worker       string    `json:"worker"`
	LatencyMs    int       `json:"latency_ms"`
	InputTokens  int       `json:"input_tokens"`
	OutputTokens int       `json:"output_tokens"`
	CacheRead    int       `json:"cache_read"`
	CacheWrite   int       `json:"cache_write"`
	Status       string    `json:"status"`
	Error        string    `json:"error,omitempty"`
	RequestBody  string    `json:"request_body,omitempty"`
	ResponseBody string    `json:"response_body,omitempty"`
	// RawRequest / RawResponse are the untouched HTTP bodies exchanged with the
	// provider — the request as buildBody() sent it (full tool schemas included)
	// and the raw SSE frames. RequestBody/ResponseBody above are the normalized
	// view, which drops tool schemas and tool_use blocks entirely. Empty for
	// records written before this was added, or when the call never reached HTTP.
	RawRequest  string `json:"raw_request,omitempty"`
	RawResponse string `json:"raw_response,omitempty"`
}

const llmRecordsSchema = `
CREATE TABLE IF NOT EXISTS llm_records (
    id            BIGSERIAL PRIMARY KEY,
    ts            TIMESTAMPTZ DEFAULT now(),
    model         TEXT,
    profile_name  TEXT,
    session_id    TEXT,
    task_id       TEXT,
    worker        TEXT,
    latency_ms    INTEGER,
    input_tokens  INTEGER,
    output_tokens INTEGER,
    cache_read    INTEGER,
    cache_write   INTEGER,
    status        TEXT,
    error         TEXT,
    request_body  TEXT,
    response_body TEXT,
    raw_request   TEXT,
    raw_response  TEXT
);
CREATE INDEX IF NOT EXISTS idx_llm_records_ts ON llm_records(ts);
CREATE INDEX IF NOT EXISTS idx_llm_records_session ON llm_records(session_id);
`

// llmRecordsMigrate adds new columns to existing tables.
const llmRecordsMigrate = `
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS task_id TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS worker TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS profile_name TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS raw_request TEXT;
ALTER TABLE llm_records ADD COLUMN IF NOT EXISTS raw_response TEXT;
`

// EnsureLLMRecordsTable creates the llm_records table if it does not exist.
func (d *DB) EnsureLLMRecordsTable() error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := coordinateWithSchemaMigration(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(llmRecordsSchema); err != nil {
		return err
	}
	if _, err := tx.Exec(llmRecordsMigrate); err != nil {
		return err
	}
	return tx.Commit()
}

// InsertLLMRecord stores one LLM call record.
func (d *DB) InsertLLMRecord(r *LLMRecord) error {
	_, err := d.Exec(`
INSERT INTO llm_records(model, profile_name, session_id, task_id, worker, latency_ms, input_tokens, output_tokens, cache_read, cache_write, status, error, request_body, response_body, raw_request, raw_response)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		r.Model, nullIfEmpty(r.ProfileName), r.SessionID, nullIfEmpty(r.TaskID), nullIfEmpty(r.Worker),
		r.LatencyMs, r.InputTokens, r.OutputTokens, r.CacheRead, r.CacheWrite,
		r.Status, nullIfEmpty(r.Error), nullIfEmpty(r.RequestBody), nullIfEmpty(r.ResponseBody),
		nullIfEmpty(r.RawRequest), nullIfEmpty(r.RawResponse))
	return err
}

// ListLLMRecords returns paginated LLM records with optional filters.
func (d *DB) ListLLMRecords(model, session, task string, page, size int) ([]LLMRecord, int, error) {
	return executionLLMSummary.page(d, executionLLMCriteria(model, session, task), page, size)
}

// ModelTokenStat is one model's aggregated token usage for a task, summed from the
// llm_usage metering ledger (see db/llm_usage.go). Calls is the number of LLM calls
// that hit this model.
type ModelTokenStat struct {
	Model            string `json:"model"`
	Calls            int    `json:"calls"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// LLMTask is one distinct task with its LLM-record count.
type LLMTask struct {
	TaskID string `json:"task_id"`
	Count  int    `json:"count"`
}

// LLMTasks returns distinct non-empty task_ids with record counts, most recent
// first — powers the LLM-records page's task picker.
func (d *DB) LLMTasks() ([]LLMTask, error) {
	return executionLLMTasks.all(d, compileRecordCriteria("COALESCE(task_id,'') <> ''"))
}

// DeleteLLMRecords removes every LLM record for one exact task_id — the same
// match the page's task picker/filter uses. Returns rows deleted.
func (d *DB) DeleteLLMRecords(task string) (int64, error) {
	res, err := d.Exec(`DELETE FROM llm_records WHERE COALESCE(task_id,'') = $1`, task)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// GetLLMRecord returns a single LLM record with full request/response bodies.
func (d *DB) GetLLMRecord(id int64) (*LLMRecord, error) {
	return executionLLMDetail.one(d, compileRecordCriteria("true", recordEquals("id", id, true)))
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
