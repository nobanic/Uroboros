package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/internal/toolinput"
	actool "github.com/nobanic/norma/tool"
	"strconv"
	"strings"
	"time"
)

// orchestrationTasks constructs the original callable documents and callbacks.
type orchestrationTasks struct{ server *Server }

func (tasks orchestrationTasks) list() actool.CoreTool {
	return roTool("list_tasks",
		"List all tasks (id/description/goal/status/run-time/parent-task/LLM config); the orchestration agent uses it to grasp the whole picture, see which tasks are stuck too long, and which LLM each one uses. Run-time: running = created->now, terminal = created->last-activity (seconds). llm_profile: the config name the task's planner/worker uses, (active config) = follows the global active one.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			lastAct, _ := tasks.server.m.PG().LastActivityAll()
			// id -> name to resolve each task's pinned LLM profile.
			profileNames := map[int64]string{}
			if profiles, err := tasks.server.m.pg.ListProfiles(); err == nil {
				for _, profile := range profiles {
					profileNames[profile.ID] = profile.Name
				}
			}
			rows := make([]map[string]any, 0)
			for _, task := range tasks.server.m.List() {
				status := tasks.server.deriveTaskStatus(task)
				lastAt := lastAct[task.ExpID]
				if live := tasks.server.engine.LastActivity(task.ID); live > lastAt {
					lastAt = live
				}
				seconds := int64(0)
				if status == "running" {
					seconds = time.Now().Unix() - task.CreatedAt
				} else if lastAt > task.CreatedAt {
					seconds = lastAt - task.CreatedAt
				}
				row := map[string]any{"id": task.ID, "description": task.Description, "goal": task.Goal, "status": status, "run_seconds": seconds}
				if task.ParentRef != "" {
					row["parent_ref"] = task.ParentRef
				}
				llmState := task.llmStateSnapshot()
				if llmState.ProfileID == nil {
					row["llm_profile"] = "(active config)"
				} else if n, ok := profileNames[*llmState.ProfileID]; ok {
					row["llm_profile"] = n
				} else {
					row["llm_profile"] = fmt.Sprintf("#%d(deleted)", *llmState.ProfileID)
				}
				rows = append(rows, row)
			}
			return jsonResult(rows)
		})
}

func (tasks orchestrationTasks) profiles() actool.CoreTool {
	return roTool("list_llm_profiles",
		"List the available LLM configs (profiles): id, name, model, format, and whether it is the current active config. Use an id to set spawn_task's llm_profile_id parameter for a subtask-specific LLM (e.g. a cheap model for recon, a strong one for exploitation). Does not include API Key.",
		objSchema(map[string]any{}),
		func(context.Context, json.RawMessage) (actool.Result, error) {
			profs, err := tasks.server.m.pg.ListProfiles()
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			out := make([]map[string]any, 0, len(profs))
			for _, p := range profs {
				out = append(out, map[string]any{
					"id": p.ID, "name": p.Name, "model": p.Model, "format": p.Format, "is_active": p.IsDefault,
				})
			}
			return jsonResult(map[string]any{"profiles": out})
		})
}

func (tasks orchestrationTasks) spawn() actool.CoreTool {
	return wrTool("spawn_task",
		"Create a subtask and start the exploration engine, returning task_id. Used to dispatch one thing (e.g. a challenge / a target) as an independent task. parent_ref optional: set the parent task id the current orchestration is associated with to establish a parent-child link.",
		objSchema(map[string]any{
			"description":            strParam("task description (short title))"),
			"goal":                   strParam("task goal (what to achieve))"),
			"parent_ref":             strParam("optional: parent task id (to establish a parent-child link))"),
			"source_task_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": fmt.Sprintf("optional: list of read-only inherited source task ids (at most %d). A subtask can read-only reference these tasks' already-discovered assets/conclusions as a starting point; unlike parent_ref's pure parent-child pointer, this is content inheritance.", db.MaxTaskSourceCount)},
			"llm_profile_id":         map[string]any{"type": "integer", "description": "optional: the LLM config id the subtask's planner/worker uses (see list_llm_profiles); leave empty to inherit the parent task, then fall back to the global active config"},
			"timeout_seconds":        map[string]any{"type": "integer", "description": "optional: task-level timeout (seconds). On expiry it triggers a graceful wrap-up and enters the timeout terminal state; empty or 0 = no limit"},
			"plan_heartbeat_seconds": map[string]any{"type": "integer", "description": "optional: planner heartbeat trigger interval (seconds). If this many seconds pass since the last planning round ended / the task started with no trigger in between -> trigger one planning round (deadlock backstop + wake to supervise in-flight workers). Empty or 0 = default 600 (10min);"},
			"seed_first_intent":      map[string]any{"type": "boolean", "description": "optional: for simple tasks you may enable it; on creation, directly issue one seed intent (content = description + goal) so the worker starts testing right away without waiting for the first planner round; default false (standard plan-then-execute)."},
		}, "description", "goal"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			return toolinput.Partial(in, func(input struct {
				Description          string          `json:"description"`
				Goal                 string          `json:"goal"`
				ParentRef            string          `json:"parent_ref"`
				SourceTaskIDs        []string        `json:"source_task_ids"`
				LLMProfileID         json.RawMessage `json:"llm_profile_id"`
				TimeoutSeconds       int             `json:"timeout_seconds"`
				PlanHeartbeatSeconds int             `json:"plan_heartbeat_seconds"`
				SeedFirstIntent      bool            `json:"seed_first_intent"`
			}) (actool.Result, error) {
				if strings.TrimSpace(input.Description) == "" {
					input.Description = "Unnamed task"
				}
				if strings.TrimSpace(input.Goal) == "" {
					return actool.Errorf("goal is required"), nil
				}
				if input.TimeoutSeconds < 0 {
					input.TimeoutSeconds = 0
				}
				// read-only inherited source tasks: count cap + each id valid/deduplicated/existing; validation rules match HTTP task creation.
				if len(input.SourceTaskIDs) > db.MaxTaskSourceCount {
					return actool.Errorf(fmt.Sprintf("At most %d linked tasks can be selected", db.MaxTaskSourceCount)), nil
				}
				inheritedIDs := make([]int64, 0, len(input.SourceTaskIDs))
				seenInherited := map[int64]bool{}
				for _, raw := range input.SourceTaskIDs {
					id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
					if err != nil || id <= 0 || seenInherited[id] {
						return actool.Errorf("Linked task id is invalid or duplicated"), nil
					}
					if _, ok := tasks.server.m.Task(strconv.FormatInt(id, 10)); !ok {
						return actool.Errorf(fmt.Sprintf("Linked task #%d does not exist", id)), nil
					}
					seenInherited[id] = true
					inheritedIDs = append(inheritedIDs, id)
				}
				// LLM profile resolution: explicit id > inherit parent's pin > active(nil).
				var profilePin *int64
				if id := parseProfileID(input.LLMProfileID); id > 0 {
					if _, ok := tasks.server.loadProfileConfig(id); !ok {
						return actool.Errorf(fmt.Sprintf("LLM configuration #%d does not exist or has no API Key set", id)), nil
					}
					profilePin = &id
				} else if input.ParentRef != "" {
					if pt, ok := tasks.server.m.Task(input.ParentRef); ok {
						profilePin = pt.LLMProfileID
					}
				}
				var profileIDs []int64
				if profilePin != nil {
					profileIDs = []int64{*profilePin}
				}
				task, err := tasks.server.m.CreateTaskWithOptions(input.Description, input.Goal, db.TaskCreateOptions{
					SourceTaskIDs:        inheritedIDs,
					LLMProfileIDs:        profileIDs,
					TimeoutSeconds:       input.TimeoutSeconds,
					PlanHeartbeatSeconds: input.PlanHeartbeatSeconds,
				})
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if input.ParentRef != "" {
					task.ParentRef = input.ParentRef
					if id, e := strconv.ParseInt(task.ID, 10, 64); e == nil {
						_ = tasks.server.m.PG().SetParentRef(id, input.ParentRef)
					}
				}
				// shared post-creation flow, reusing the same launchTask block as HTTP task creation (server.go createTask): launchTask:
				// seed + in the background, visibly decompose the goal (round 0 / LLM steps / per-goal) + engine.Run.
				// seed_first_intent defaults to false (standard plan-then-execute); simple tasks may enable it to issue one work directly for testing.
				tasks.server.launchTask(task, input.Description+" "+input.Goal, input.SeedFirstIntent)
				return actool.Text(fmt.Sprintf("task created: %s", task.ID)), nil

			})
		})
}

func (tasks orchestrationTasks) pause() actool.CoreTool {
	return wrTool("pause_task", "Pause the specified task (stop its planner/worker loop).",
		objSchema(map[string]any{"task_id": strParam("the id of the task to pause id")}, "task_id"),
		func(_ context.Context, in json.RawMessage) (actool.Result, error) {
			return toolinput.Partial(in, func(input struct {
				TaskID string `json:"task_id"`
			}) (actool.Result, error) {
				t, ok := tasks.server.m.Task(input.TaskID)
				if !ok {
					return actool.Errorf("task not found: " + input.TaskID), nil
				}
				if _, err := tasks.server.applyTaskControlWithCause(t, "pause", agent.AbortPausedByOrchestrator); err != nil {
					return actool.Errorf(err.Error()), nil
				}
				return actool.Text("task paused: " + input.TaskID), nil

			})
		})
}

func (tasks orchestrationTasks) graph() actool.CoreTool {
	return roTool("get_task_graph", "Read the specified task's exploration-graph overview (same as graph_overview: asset counts/frontier/findings/coverage, etc.); use task_id to specify the task.",
		objSchema(map[string]any{"task_id": strParam("task id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return tasks.server.delegateToTask(ctx, in, (*agent.ToolSet).GraphOverviewTool)
		})
}

func (tasks orchestrationTasks) findings() actool.CoreTool {
	return roTool("list_task_findings", "Read the specified task's confirmed vulnerabilities (incl. flag/PoC; each carries id/task_id/intent_id/vulnclass/severity/summary/status); use task_id to specify the task.",
		objSchema(map[string]any{"task_id": strParam("task id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return tasks.server.delegateToTask(ctx, in, (*agent.ToolSet).ListFindingsTool)
		})
}

func (tasks orchestrationTasks) hint() actool.CoreTool {
	return wrTool("add_task_hint", "Inject a strategic hint into the specified task (the task's planner reads it when generating intents next round).\n"+
		"★Prefer batching: put multiple hints into the hints array and submit at once (returns an ids array, same length and order as hints; failed items have id=0); for a single hint, omit hints and give top-level text directly.",
		objSchema(map[string]any{
			"task_id":      strParam("task id"),
			"hints":        map[string]any{"type": "array", "description": "[use this first] Hints array; each element has the same fields as the top level (text/asset_ids/traffic_refs).", "items": objSchema(map[string]any{"text": strParam("hint content"), "asset_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "traffic_refs": agent.HintTrafficSchema()})},
			"text":         strParam("[single] hint content"),
			"traffic_refs": agent.HintTrafficSchema(),
			"asset_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "anchored asset ids (optional, 0/1/many; asset ids within this task)"},
		}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return tasks.server.delegateToTask(ctx, in, (*agent.ToolSet).AddHintTool)
		})
}

func (tasks orchestrationTasks) workerTrace() actool.CoreTool {
	return roTool("get_task_worker_trace",
		"View the execution of a work (intent) in the specified task: get_task_worker_trace(task_id, intent_id) shows step summaries; add step_ids=[...] to fetch the full content of those steps (at most 5 at a time; extra ids return only the first 5).",
		objSchema(map[string]any{
			"task_id":   strParam("task id"),
			"intent_id": map[string]any{"type": "integer", "description": "intent id (a work in this task)"},
			"step_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "optional: step ids whose full content to fetch (at most 5 at a time; extra ids return only the first 5, the rest listed in omitted_step_ids)"},
		}, "task_id", "intent_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return tasks.server.delegateToTask(ctx, in, (*agent.ToolSet).GetWorkerTraceTool)
		})
}

func (tasks orchestrationTasks) workerTraces() actool.CoreTool {
	return roTool("list_task_worker_traces", "List which works (intents) have run in the specified task + each one's step count, to find which works are worth reviewing (then use get_task_worker_trace).",
		objSchema(map[string]any{"task_id": strParam("task id")}, "task_id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return tasks.server.delegateToTask(ctx, in, (*agent.ToolSet).ListWorkerTracesTool)
		})
}

func (tasks orchestrationTasks) searchTraces() actool.CoreTool {
	return roTool("search_task_worker_traces", "Search all works' execution in the specified task by keyword (returns matching step summaries + intent_id).",
		objSchema(map[string]any{"task_id": strParam("task id"), "q": strParam("search keyword")}, "task_id", "q"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return tasks.server.delegateToTask(ctx, in, (*agent.ToolSet).SearchWorkerTracesTool)
		})
}

func (tasks orchestrationTasks) node() actool.CoreTool {
	return roTool("get_task_node_detail",
		"Read the full content of an exploration-graph node in the specified task (finding/fact/intent/goal: summary + details/evidence/PoC). id is the exploration node id (e.g. returned by report_finding, or the id in list_task_findings). Use it to fetch a vulnerability's full evidence before writing its report.",
		objSchema(map[string]any{
			"task_id": strParam("task id"),
			"id":      map[string]any{"type": "integer", "description": "exploration-graph node id (not an asset id)"},
		}, "task_id", "id"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return tasks.server.delegateToTask(ctx, in, (*agent.ToolSet).NodeDetailTool)
		})
}

func (tasks orchestrationTasks) report() actool.CoreTool {
	return wrTool("update_finding_report",
		"Write/overwrite the [detailed report] for a registered vulnerability (full Markdown, replacing the old content entirely). Pass the finding_id that report_finding returned (the number in \"finding recorded: <id>\"). The report should include: vulnerability overview, impact and severity, reproduction steps, evidence/PoC, and remediation advice.",
		objSchema(map[string]any{
			"finding_id":       map[string]any{"type": "integer", "description": "target vulnerability id (the id returned by report_finding)"},
			"report":           strParam("full detailed report text, in Markdown format"),
			"evidence_version": map[string]any{"type": "integer", "description": "get_finding_traffic returns the evidence version; used to prevent the report from overwriting newer evidence changes"},
		}, "finding_id", "report"),
		func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			return toolinput.Partial(in, func(input struct {
				EvidenceVersion *int64          `json:"evidence_version"`
				FindingID       json.RawMessage `json:"finding_id"`
				Report          string          `json:"report"`
			}) (actool.Result, error) {
				nodeID := parseProfileID(input.FindingID) // reuse the 'number or numeric-string' parsing
				if nodeID <= 0 {
					return actool.Errorf("invalid finding_id"), nil
				}
				n, err := tasks.server.m.pg.SetFindingReportVersionByNodeID(ctx, nodeID, input.Report, input.EvidenceVersion)
				if err != nil {
					return actool.Errorf(err.Error()), nil
				}
				if n == 0 {
					return actool.Errorf(fmt.Sprintf("no vulnerability record found for finding_id=%d (register it first with report_finding)", nodeID)), nil
				}
				return actool.Text(fmt.Sprintf("finding %d report updated (%d chars)", nodeID, len(input.Report))), nil

			})
		})
}
