package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nobanic/UroborOS/arsenal"
	"github.com/nobanic/UroborOS/db"
	actool "github.com/nobanic/norma/tool"
)

func (s *Server) arsenalTools() []actool.CoreTool {
	return []actool.CoreTool{s.arsenalRunTool(), s.arsenalDiscoverTool(), s.arjunDiscoverTool(), s.wafFingerprintTool(), s.archiveDiscoverTool(), s.arsenalStatusTool(), s.arsenalResultsTool(), s.arsenalCancelTool()}
}

func (s *Server) wafFingerprintTool() actool.CoreTool {
	return wrTool("waf_fingerprint",
		"Passively fingerprint WAF candidates from response headers already stored for one task-scoped GET endpoint. Uroboros selects the exact Traffic exchange and fixed WAFW00F corpus. No target request or body read occurs; results remain candidates, never findings or active verification.",
		katanaObjectSchema(map[string]any{
			"endpoint_id": map[string]any{"type": "integer", "minimum": 1, "description": "task-scoped GET endpoint asset with verified Katana or Arsenal Traffic provenance"},
		}, "endpoint_id"),
		func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("passive WAF fingerprinting requires a task-bound agent run; no task scope is attached"), nil
			}
			var args struct {
				EndpointID int64 `json:"endpoint_id"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid passive WAF fingerprint request: " + err.Error()), nil
			}
			if s.engine != nil && s.engine.IsDeleting(fmt.Sprint(taskID)) {
				return actool.Errorf("task is being archived or deleted"), nil
			}
			runner, err := s.arsenalManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			job, err := runner.Start(taskID, "tool", arsenal.StartRequest{Tool: "wafw00f", EndpointID: args.EndpointID})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(arsenalJobStatus(job))
		})
}

func (s *Server) arjunDiscoverTool() actool.CoreTool {
	return wrTool("arjun_discover",
		"Discover GET query-parameter names on one existing endpoint asset already verified through this task's Traffic recorder. Uroboros owns the URL, method, exact route, wordlist, rate and budgets. Results are Traffic-backed candidates bound to the existing endpoint; they do not create assets, expand scope or prove a vulnerability.",
		katanaObjectSchema(map[string]any{
			"endpoint_id": map[string]any{"type": "integer", "minimum": 1, "description": "task-scoped GET endpoint asset with verified Katana or Arsenal Traffic provenance"},
		}, "endpoint_id"),
		func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("Arjun discovery requires a task-bound agent run; no task scope is attached"), nil
			}
			var args struct {
				EndpointID int64 `json:"endpoint_id"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid Arjun discovery request: " + err.Error()), nil
			}
			if s.engine != nil && s.engine.IsDeleting(fmt.Sprint(taskID)) {
				return actool.Errorf("task is being archived or deleted"), nil
			}
			runner, err := s.arsenalManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			job, err := runner.Start(taskID, "tool", arsenal.StartRequest{Tool: "arjun", EndpointID: args.EndpointID})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(arsenalJobStatus(job))
		})
}

func (s *Server) archiveDiscoverTool() actool.CoreTool {
	return wrTool("archive_discover",
		"Query Internet Archive CDX index metadata for one registrable root domain already authorized in this task. Requires a separate active operator disclosure grant created outside the model. Results are sanitized historical URL candidates only: they do not create assets, send target requests, expand scope or prove a vulnerability.",
		katanaObjectSchema(map[string]any{
			"domain": map[string]any{"type": "string", "description": "registrable root domain exactly present in this task's frozen root-domain scope and operator grant"},
			"from":   map[string]any{"type": "string", "description": "optional inclusive capture start: YYYY, YYYYMM, YYYYMMDD or YYYYMMDDhhmmss"},
			"to":     map[string]any{"type": "string", "description": "optional inclusive capture end: YYYY, YYYYMM, YYYYMMDD or YYYYMMDDhhmmss"},
		}, "domain"),
		func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("Archive discovery requires a task-bound agent run; no task scope is attached"), nil
			}
			var args struct {
				Domain string `json:"domain"`
				From   string `json:"from"`
				To     string `json:"to"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid archive discovery request: " + err.Error()), nil
			}
			if s.engine != nil && s.engine.IsDeleting(fmt.Sprint(taskID)) {
				return actool.Errorf("task is being archived or deleted"), nil
			}
			runner, err := s.arsenalManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			job, err := runner.Start(taskID, "tool", arsenal.StartRequest{
				Tool: "archive", Target: args.Domain,
				Config: map[string]any{"from": args.From, "to": args.To},
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(arsenalJobStatus(job))
		})
}

func (s *Server) arsenalDiscoverTool() actool.CoreTool {
	return wrTool("arsenal_discover",
		"Start one task-bound subfinder → DNS → naabu → httpx workflow for an authorized registrable domain. The domain is disclosed only to the fixed audited passive providers anubisdb.com and rapiddns.io. DNS answers and TCP destinations are frozen; only Traffic-verified httpx services become assets. Intermediate subdomains, DNS bindings and ports remain candidates. Zero max_requests means unlimited HTTP requests.",
		katanaObjectSchema(map[string]any{
			"domain": map[string]any{"type": "string", "description": "registrable domain exactly present in this task's root-domain scope"},
			"ports": map[string]any{
				"type": "array", "items": map[string]any{"type": "integer", "minimum": 1, "maximum": 65535},
				"minItems": 1, "maxItems": 8, "uniqueItems": true,
				"description": "bounded TCP ports; defaults to 80,443,8080,8443",
			},
			"max_subdomains":         map[string]any{"type": "integer", "minimum": 1, "maximum": 50, "description": "bounded passive candidates; defaults to 50"},
			"max_addresses_per_host": map[string]any{"type": "integer", "minimum": 1, "maximum": 2, "description": "frozen A/AAAA answers retained per host; defaults to 2"},
			"budget_seconds":         map[string]any{"type": "integer", "minimum": 10, "maximum": 600, "description": "total workflow budget; defaults to 180"},
			"max_requests":           map[string]any{"type": "integer", "minimum": 0, "description": "optional Traffic-admitted httpx request budget; 0 means unlimited"},
		}, "domain"),
		func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("Arsenal discovery requires a task-bound agent run; no task scope is attached"), nil
			}
			var args struct {
				Domain              string `json:"domain"`
				Ports               []int  `json:"ports"`
				MaxSubdomains       *int   `json:"max_subdomains"`
				MaxAddressesPerHost *int   `json:"max_addresses_per_host"`
				BudgetSeconds       *int   `json:"budget_seconds"`
				MaxRequests         int64  `json:"max_requests"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid Arsenal discovery request: " + err.Error()), nil
			}
			if s.engine != nil && s.engine.IsDeleting(fmt.Sprint(taskID)) {
				return actool.Errorf("task is being archived or deleted"), nil
			}
			config := map[string]any{}
			if args.Ports != nil {
				config["ports"] = args.Ports
			}
			for key, value := range map[string]*int{
				"max_subdomains": args.MaxSubdomains, "max_addresses_per_host": args.MaxAddressesPerHost,
				"budget_seconds": args.BudgetSeconds,
			} {
				if value != nil {
					config[key] = *value
				}
			}
			runner, err := s.arsenalManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			job, err := runner.Start(taskID, "tool", arsenal.StartRequest{
				Tool: "discovery", Target: args.Domain, Config: config, MaxRequests: args.MaxRequests,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(arsenalJobStatus(job))
		})
}

func (s *Server) arsenalManager() (*arsenal.Manager, error) {
	if s == nil || s.m == nil || s.m.Arsenal() == nil {
		return nil, errors.New("Arsenal scanning is unavailable")
	}
	return s.m.Arsenal(), nil
}

func (s *Server) arsenalRunTool() actool.CoreTool {
	return wrTool("arsenal_run",
		"Start one task-scoped scanner job through Uroboros' authenticated, fail-closed evidence proxy. httpx verifies a scoped HTTP candidate, ffuf performs gap-driven content discovery, and Nuclei emits hypotheses only. Results never become findings automatically. Zero max_requests means unlimited.",
		katanaObjectSchema(map[string]any{
			"tool":           map[string]any{"type": "string", "enum": []string{"httpx", "ffuf", "nuclei"}},
			"target":         map[string]any{"type": "string", "description": "absolute HTTP(S) target already covered by this task's frozen scope"},
			"max_requests":   map[string]any{"type": "integer", "minimum": 0, "description": "optional proxy-admitted request budget; 0 means unlimited"},
			"budget_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 7200},
			"extensions":     map[string]any{"type": "string", "description": "ffuf only; comma-separated dotted suffixes such as .php,.txt"},
			"profile":        map[string]any{"type": "string", "enum": []string{"light", "full"}, "description": "Nuclei only"},
			"template_ids":   map[string]any{"type": "string", "description": "Nuclei only; IDs within the pinned HTTP corpus"},
			"severity":       map[string]any{"type": "string", "enum": []string{"", "info", "low", "medium", "high", "critical", "unknown"}, "description": "Nuclei only"},
			"tags":           map[string]any{"type": "string", "description": "Nuclei only; tags within the pinned HTTP corpus"},
		}, "tool", "target"),
		func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("Arsenal requires a task-bound agent run; no task scope is attached"), nil
			}
			var args struct {
				Tool          string  `json:"tool"`
				Target        string  `json:"target"`
				MaxRequests   int64   `json:"max_requests"`
				BudgetSeconds *int    `json:"budget_seconds"`
				Extensions    *string `json:"extensions"`
				Profile       *string `json:"profile"`
				TemplateIDs   *string `json:"template_ids"`
				Severity      *string `json:"severity"`
				Tags          *string `json:"tags"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid Arsenal request: " + err.Error()), nil
			}
			if s.engine != nil && s.engine.IsDeleting(fmt.Sprint(taskID)) {
				return actool.Errorf("task is being archived or deleted"), nil
			}
			config := map[string]any{}
			if args.BudgetSeconds != nil {
				config["budget_seconds"] = *args.BudgetSeconds
			}
			for key, value := range map[string]*string{
				"extensions": args.Extensions, "profile": args.Profile, "template_ids": args.TemplateIDs,
				"severity": args.Severity, "tags": args.Tags,
			} {
				if value != nil {
					config[key] = *value
				}
			}
			runner, err := s.arsenalManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			job, err := runner.Start(taskID, "tool", arsenal.StartRequest{
				Tool: args.Tool, Target: args.Target, Config: config, MaxRequests: args.MaxRequests,
			})
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(arsenalJobStatus(job))
		})
}

func (s *Server) arsenalStatusTool() actool.CoreTool {
	return roTool("arsenal_status", "Return an Arsenal scanner, passive fingerprint or archive-metadata job owned by this task, including immutable scope/config and runtime provenance fingerprints.",
		katanaObjectSchema(map[string]any{
			"job_id": map[string]any{"type": "integer", "minimum": 1, "description": "job id; omit for this task's latest job"},
		}), func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("Arsenal requires a task-bound agent run; no task scope is attached"), nil
			}
			var args struct {
				JobID int64 `json:"job_id"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid Arsenal status query: " + err.Error()), nil
			}
			runner, err := s.arsenalManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var job *db.ArsenalJob
			if args.JobID > 0 {
				job, err = runner.Job(ctx, args.JobID)
			} else {
				job, err = runner.LatestJob(taskID)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if job == nil || job.TaskID != taskID {
				if args.JobID > 0 {
					return actool.Errorf("Arsenal job not found in this task"), nil
				}
				return jsonResult(map[string]any{"status": "not_started"})
			}
			return jsonResult(arsenalJobStatus(job))
		})
}

func (s *Server) arsenalResultsTool() actool.CoreTool {
	return roTool("arsenal_results", "List this task's scanner, passive WAF or sanitized archive-metadata observations. Discovery, archive, Arjun parameters and WAF fingerprints remain candidates. WAF rows bind to existing endpoint/Traffic evidence and contain no raw header or cookie values. Nuclei remains a hypothesis. No row is a vulnerability finding.",
		katanaObjectSchema(map[string]any{
			"job_id": map[string]any{"type": "integer", "minimum": 1, "description": "job id; omit for this task's latest job"},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
			"offset": map[string]any{"type": "integer", "minimum": 0},
		}), func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("Arsenal requires a task-bound agent run; no task scope is attached"), nil
			}
			var args struct {
				JobID  int64 `json:"job_id"`
				Limit  int   `json:"limit"`
				Offset int   `json:"offset"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid Arsenal result query: " + err.Error()), nil
			}
			runner, err := s.arsenalManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var job *db.ArsenalJob
			if args.JobID > 0 {
				job, err = runner.Job(ctx, args.JobID)
			} else {
				job, err = runner.LatestJob(taskID)
			}
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			if job == nil || job.TaskID != taskID {
				return actool.Errorf("Arsenal job not found in this task"), nil
			}
			if args.Limit <= 0 || args.Limit > 500 {
				args.Limit = 200
			}
			results, err := runner.Results(job.ID, args.Limit, args.Offset)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return jsonResult(map[string]any{"job": arsenalJobStatus(job), "results": results})
		})
}

func (s *Server) arsenalCancelTool() actool.CoreTool {
	return wrTool("arsenal_cancel", "Cancel and join this task's active Arsenal scanner, passive fingerprint or archive-metadata job. Already committed sanitized observations remain available.",
		katanaObjectSchema(map[string]any{
			"job_id": map[string]any{"type": "integer", "minimum": 1, "description": "optional exact active job id"},
		}), func(ctx context.Context, input json.RawMessage) (actool.Result, error) {
			taskID, err := katanaTaskID(ctx)
			if err != nil {
				return actool.Errorf("Arsenal requires a task-bound agent run; no task scope is attached"), nil
			}
			var args struct {
				JobID int64 `json:"job_id"`
			}
			if err := decodeKatanaJSON(input, &args); err != nil {
				return actool.Errorf("invalid Arsenal cancellation request: " + err.Error()), nil
			}
			runner, err := s.arsenalManager()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			cancelCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			if err := runner.CancelJob(cancelCtx, taskID, args.JobID, errors.New("canceled by task agent")); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			return actool.Text("Arsenal scan canceled"), nil
		})
}

func arsenalJobStatus(job *db.ArsenalJob) map[string]any {
	if job == nil {
		return map[string]any{"status": "not_started"}
	}
	return map[string]any{
		"id": job.ID, "task_id": job.TaskID, "source": job.Source, "tool": job.Tool,
		"status": job.Status, "target": job.Target, "config": job.Config, "config_hash": job.ConfigHash,
		"scope": job.Scope, "scope_hash": job.ScopeHash, "contract_version": job.ContractVersion,
		"source_revision": job.SourceRevision, "tool_version": job.ToolVersion, "tool_sha256": job.ToolSHA256,
		"template_version": job.TemplateVersion, "template_sha256": job.TemplateSHA256,
		"wordlist_sha256": job.WordlistSHA256, "exit_state": job.ExitState, "complete": job.Complete,
		"error": job.Error, "stats": job.Stats, "candidate_count": job.CandidateCount,
		"verified_count": job.VerifiedCount, "started_at": job.StartedAt,
		"finished_at": job.FinishedAt, "created_at": job.CreatedAt,
	}
}
