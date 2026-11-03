package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/katana"
	actool "github.com/nobanic/norma/tool"
)

func (s *Server) katanaTools() []actool.CoreTool {
	return []actool.CoreTool{s.katanaRunTool(), s.katanaStatusTool(), s.katanaResultsTool(), s.katanaCancelTool()}
}

func (s *Server) katanaManager() (*katana.Manager, error) {
	if s == nil || s.m == nil || s.m.Katana() == nil {
		return nil, errors.New("Katana discovery is unavailable")
	}
	return s.m.Katana(), nil
}

func katanaObjectSchema(properties map[string]any, required ...string) map[string]any {
	schema := objSchema(properties, required...)
	schema["additionalProperties"] = false
	return schema
}

func decodeKatanaJSON(input json.RawMessage, target any) error {
	trimmed := bytes.TrimSpace(input)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, []byte("null")) {
		return errors.New("expected a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("expected one JSON object")
		}
		return err
	}
	return nil
}

func validateKatanaEmptyInput(input json.RawMessage) error {
	var value struct{}
	return decodeKatanaJSON(input, &value)
}

func katanaTaskID(ctx context.Context) (int64, error) {
	taskID := agent.RunInfoFrom(ctx).TaskID
	if taskID <= 0 {
		return 0, errors.New("Katana requires a task-bound agent run; no task scope is attached")
	}
	return taskID, nil
}

func katanaOptionSchema() map[string]any {
	unlimited := func(description string) map[string]any {
		return map[string]any{"type": "integer", "minimum": 0, "description": description + "; 0 means unlimited"}
	}
	nonNegative := func(description string) map[string]any {
		return map[string]any{"type": "integer", "minimum": 0, "description": description}
	}
	return map[string]any{
		"mode":                           map[string]any{"type": "string", "enum": []string{"standard", "headless", "hybrid"}},
		"max_depth":                      unlimited("crawl depth"),
		"max_requests":                   unlimited("requests admitted by the scoped evidence proxy"),
		"max_duration_seconds":           unlimited("wall-clock crawl duration"),
		"max_domain_pages":               unlimited("pages per domain"),
		"max_response_bytes":             unlimited("response bytes parsed per response"),
		"allowed_methods":                map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "optional HTTP method allowlist; empty allows all methods"},
		"js_crawl":                       map[string]any{"type": "boolean"},
		"jsluice":                        map[string]any{"type": "boolean", "description": "memory-intensive JavaScript endpoint extraction"},
		"known_files":                    map[string]any{"type": "string", "enum": []string{"", "all", "robotstxt", "sitemapxml"}},
		"automatic_form_fill":            map[string]any{"type": "boolean", "description": "experimental automatic form filling; may submit requests"},
		"form_extraction":                map[string]any{"type": "boolean"},
		"tech_detect":                    map[string]any{"type": "boolean"},
		"strategy":                       map[string]any{"type": "string", "enum": []string{"depth-first", "breadth-first"}},
		"ignore_query_params":            map[string]any{"type": "boolean"},
		"filter_similar":                 map[string]any{"type": "boolean"},
		"filter_similar_threshold":       nonNegative("distinct path values before similarity filtering"),
		"tls_impersonate":                map[string]any{"type": "boolean"},
		"disable_redirects":              map[string]any{"type": "boolean"},
		"path_climb":                     map[string]any{"type": "boolean"},
		"knowledge_base":                 map[string]any{"type": "boolean"},
		"extract_secrets":                map[string]any{"type": "boolean", "description": "local secret-pattern extraction only; live provider validation is never performed"},
		"extract_endpoints":              map[string]any{"type": "boolean"},
		"xhr_extraction":                 map[string]any{"type": "boolean", "description": "headless/hybrid only"},
		"concurrency":                    nonNegative("concurrent fetchers; 0 uses Katana's default"),
		"parallelism":                    nonNegative("concurrent input seeds; 0 uses Katana's default"),
		"delay_seconds":                  nonNegative("delay between requests"),
		"rate_limit_per_second":          unlimited("global requests per second"),
		"rate_limit_per_minute":          unlimited("global requests per minute"),
		"host_rate_limit_per_second":     unlimited("per-host requests per second"),
		"host_rate_limit_per_minute":     unlimited("per-host requests per minute"),
		"timeout_seconds":                nonNegative("per-request timeout; 0 uses Katana's default"),
		"retries":                        nonNegative("request retries; 0 disables retries"),
		"time_stable_seconds":            nonNegative("headless page stability wait"),
		"max_failure_count":              unlimited("headless consecutive action failures"),
		"page_load_strategy":             map[string]any{"type": "string", "enum": []string{"heuristic", "load", "domcontentloaded", "networkidle", "none"}},
		"dom_wait_seconds":               nonNegative("headless DOM wait"),
		"headless_no_sandbox":            map[string]any{"type": "boolean"},
		"page_content_similar":           map[string]any{"type": "boolean"},
		"page_content_similar_mode":      map[string]any{"type": "string", "enum": []string{"simhash", "tfidf", "bm25"}},
		"page_content_similar_distance":  nonNegative("simhash Hamming distance"),
		"page_content_similar_threshold": map[string]any{"type": "string", "description": "tfidf/bm25 score from 0 to 1"},
		"page_content_similar_budget":    nonNegative("pages fully processed per similarity cluster"),
	}
}

func (s *Server) katanaRunTool() actool.CoreTool {
	return wrTool("katana_run",
		"Start task-scoped Katana discovery. Uroboros freezes the current task scope, routes every HTTP(S) request through its fail-closed evidence proxy, and imports only proxy-verified exchanges as assets. All zero budgets are unlimited. Omit fields to use the full standard defaults.",
		katanaObjectSchema(katanaOptionSchema()),
		func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			options := katana.DefaultOptions()
			if err := decodeKatanaJSON(input, &options); err != nil {
				return actool.Errorf("invalid Katana options: " + err.Error()), nil
			}
			if s.engine != nil && s.engine.IsDeleting(fmt.Sprint(taskID)) {
				return actool.Errorf("task is being archived or deleted"), nil
			}
			runner, err := s.katanaManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			job, err := runner.Start(taskID, "tool", options)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(katanaJobStatus(job))
		})
}

func (s *Server) katanaStatusTool() actool.CoreTool {
	return roTool("katana_status", "Return the latest Katana discovery job for this task, including its immutable scope/config fingerprints and counters.",
		katanaObjectSchema(nil), func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := validateKatanaEmptyInput(input); err != nil {
				return actool.Errorf("invalid Katana status query: " + err.Error()), nil
			}
			runner, err := s.katanaManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			job, err := runner.LatestJob(taskID)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if job == nil {
				return jsonResult(map[string]any{"status": "not_started"})
			}
			return jsonResult(katanaJobStatus(job))
		})
}

func (s *Server) katanaResultsTool() actool.CoreTool {
	return roTool("katana_results", "List candidate and proxy-verified endpoint discoveries from a Katana job. Candidate rows can include bounded, redacted knowledge_base classifier metadata; verified=true includes a durable traffic_id and imported asset_id. Discoveries are not vulnerability findings.",
		katanaObjectSchema(map[string]any{
			"job_id": map[string]any{"type": "integer", "minimum": 1, "description": "job id; omit for this task's latest job"},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
			"offset": map[string]any{"type": "integer", "minimum": 0},
		}), func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			runner, err := s.katanaManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var args struct {
				JobID  int64 `json:"job_id"`
				Limit  int   `json:"limit"`
				Offset int   `json:"offset"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid Katana result query: " + err.Error()), nil
			}
			var job *db.KatanaJob
			if args.JobID > 0 {
				job, err = runner.Job(ctx, args.JobID)
			} else {
				job, err = runner.LatestJob(taskID)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if job == nil || job.TaskID != taskID {
				return actool.Errorf("Katana job not found in this task"), nil
			}
			if args.Limit <= 0 || args.Limit > 500 {
				args.Limit = 200
			}
			results, err := runner.Results(job.ID, args.Limit, args.Offset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"job": katanaJobStatus(job), "results": results})
		})
}

func (s *Server) katanaCancelTool() actool.CoreTool {
	return wrTool("katana_cancel", "Cancel and join this task's active Katana discovery process. Completed evidence and discoveries remain available.",
		katanaObjectSchema(nil), func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if err := validateKatanaEmptyInput(input); err != nil {
				return actool.Errorf("invalid Katana cancellation request: " + err.Error()), nil
			}
			runner, err := s.katanaManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			cancelCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := runner.CancelTask(cancelCtx, taskID, fmt.Errorf("canceled by task agent")); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("Katana discovery canceled"), nil
		})
}

func katanaJobStatus(job *db.KatanaJob) map[string]any {
	if job == nil {
		return map[string]any{"status": "not_started"}
	}
	status := map[string]any{
		"id": job.ID, "task_id": job.TaskID, "source": job.Source, "status": job.Status, "mode": job.Mode,
		"config": job.Config, "config_hash": job.ConfigHash, "scope": job.Scope, "scope_hash": job.ScopeHash,
		"seeds": job.Seeds, "seed_hash": job.SeedHash, "binary_version": job.BinaryVersion, "exit_code": job.ExitCode, "error": job.Error,
		"stats": job.Stats, "candidate_count": job.CandidateCount, "verified_count": job.VerifiedCount,
		"started_at": job.StartedAt, "finished_at": job.FinishedAt, "created_at": job.CreatedAt,
	}
	var stats struct {
		ScopeBlocked int64 `json:"scope_blocked"`
	}
	if json.Unmarshal(job.Stats, &stats) == nil && stats.ScopeBlocked > 0 {
		// The proxy currently persists only a counter, not a safe redacted target.
		// State that absence explicitly so an agent cannot turn nearby seeds/logs
		// into a fabricated destination attribution.
		status["scope_blocked_destination"] = "unknown"
	}
	return status
}
