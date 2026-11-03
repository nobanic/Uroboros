package server

import (
	"encoding/json"
	"github.com/nobanic/UroborOS/agent"
	"github.com/nobanic/UroborOS/db"
	actool "github.com/nobanic/norma/tool"
	"log"
)

// orchestrationSeed preserves registration order and each original flag policy.
type orchestrationSeed struct{ server *Server }

// once only owns the original flag read. Its literal callback keeps all writes,
// deferred attempt markers, partial commits and error returns in the same order.
func (installer orchestrationSeed) once(flag string, run func()) {
	if v, _, _ := installer.server.m.pg.GetSetting(flag); v == "true" {
		return
	}
	run()
}

func (installer orchestrationSeed) all() {
	// task-op + platform tools default-bind to the built-in Auto agent (it is inherently used to
	// operate the platform). SeedTool takes effect on first insert; rows already seeded in old DBs are re-bound by seedAutoDefaultBindings.
	autoAgents, _ := json.Marshal([]string{"auto"})
	for _, t := range installer.server.orchestrationTools() {
		schema, _ := json.Marshal(t.InputSchema())
		bindings := autoAgents
		if t.Name() == "bind_finding_traffic" {
			bindings = json.RawMessage(`["reporter"]`)
		}
		_ = installer.server.m.PG().SeedTool(t.Name(), t.Description(), schema, bindings)
	}
	for _, t := range installer.server.platformTools() {
		schema, _ := json.Marshal(t.InputSchema())
		_ = installer.server.m.PG().SeedTool(t.Name(), t.Description(), schema, autoAgents)
	}
	// Shell-independent, task-scoped HTTP execution for the agents that perform
	// discovery and verification. Runtime exposure is still capture-gated in
	// hostTools, and existing installations retain any later operator edits.
	httpAgents, _ := json.Marshal([]string{"planner", "worker", "pentest"})
	plannerAgents, _ := json.Marshal([]string{"planner"})
	reviewAgents, _ := json.Marshal([]string{"planner", "mainagent"})
	for _, tool := range installer.server.discoveryReviewTools() {
		schema, _ := json.Marshal(tool.InputSchema())
		bindings := plannerAgents
		if tool.Name() == "discovery_review" {
			bindings = reviewAgents
		}
		_ = installer.server.m.PG().SeedTool(tool.Name(), tool.Description(), schema, bindings)
	}
	for _, tool := range installer.server.katanaTools() {
		schema, _ := json.Marshal(tool.InputSchema())
		_ = installer.server.m.PG().SeedTool(tool.Name(), tool.Description(), schema, httpAgents)
	}
	for _, tool := range installer.server.arsenalTools() {
		schema, _ := json.Marshal(tool.InputSchema())
		_ = installer.server.m.PG().SeedTool(tool.Name(), tool.Description(), schema, httpAgents)
	}
	for _, tool := range installer.server.binaryTools() {
		schema, _ := json.Marshal(tool.InputSchema())
		_ = installer.server.m.PG().SeedTool(tool.Name(), tool.Description(), schema, httpAgents)
	}
	requestTool := installer.server.httpRequestTool()
	requestSchema, _ := json.Marshal(requestTool.InputSchema())
	_ = installer.server.m.PG().SeedTool(requestTool.Name(), requestTool.Description(), requestSchema, httpAgents)
	transformTool := installer.server.artifactTransformTool()
	transformSchema, _ := json.Marshal(transformTool.InputSchema())
	_ = installer.server.m.PG().SeedTool(transformTool.Name(), transformTool.Description(), transformSchema, httpAgents)
	installer.server.refreshHypothesisRepeatAndFactSchemas(requestTool)
	installer.server.refreshBuiltinToolSchemas()
	installer.server.seedAutoDefaultBindings()
	installer.server.seedPlannerDefaultBindings()
	installer.server.seedPlannerListAssetsBinding()
	installer.server.seedCompanyScopeRebind()
	installer.server.seedWorkerReadToolsUnbind() // list_facts/list_companies/list_worker_traces unbound from worker by default (one-time))
	installer.server.seedWorkerReadbackRebind()  // fix an old migration's erroneous removal: re-bind search_all_worker_traces/get_worker_trace/node_detail back to worker (one-time))
	installer.server.seedAutoReportFindingBinding()
	installer.server.unbindGoalMetDefault()
	installer.server.reseedGoalsPrompt()       // goals goals prompt: add an 'extract operation constraints' step -> old DBs get a new default version appended (one-time))
	installer.server.reseedMainAgentPrompt()   // mainagent mainagent prompt: add 'after goals are met, when add_intent posts directly, ask whether to register it as a formal goal' (one-time))
	installer.server.reseedPlannerPrompt()     // planner planner prompt: rewrite the justification for '0 intents' + add quantified acceptance checks (one-time))
	installer.server.reseedWorkerPrompt()      // worker worker prompt: add an evidence threshold for negative conclusions (one-time))
	installer.server.seedReporterAgent()       // pre-provision the 'Report Writing' agent + tool bindings + finding trigger (one-time))
	installer.server.seedFindingTrafficTools() // add optional evidence parameters and read-only evidence tools, preserving user configuration
	installer.server.seedFindingWorkflowTools()
	// note: pentest's default tool bindings need no migration -- BuiltinToolSeeds, on fresh init,
	// list_assets/insert_assets/report_finding/list_findings/list_companies together with
	// pentest are seeded together (the project has no old DBs yet, so no migration).
}

func (installer orchestrationSeed) closure(requestTool actool.CoreTool) {
	const flag = "tool_schema_refresh_v13_roles_and_provenance"
	installer.once(flag, func() {
		requestSchema, _ := json.Marshal(requestTool.InputSchema())
		if err := installer.server.m.pg.RefreshToolDefaults(requestTool.Name(), requestTool.Description(), requestSchema); err != nil {
			log.Printf("[tools] refresh %s schema failed: %v", requestTool.Name(), err)
			return
		}
		wanted := map[string]bool{"add_intent": true, "record_fact": true}
		refreshed := 0
		for _, seed := range agent.BuiltinToolSeeds() {
			if !wanted[seed.Key] {
				continue
			}
			schema, _ := json.Marshal(seed.Schema)
			if err := installer.server.m.pg.RefreshToolDefaults(seed.Key, seed.Desc, schema); err != nil {
				log.Printf("[tools] refresh %s schema failed: %v", seed.Key, err)
				return
			}
			refreshed++
		}
		if refreshed != len(wanted) {
			log.Printf("[tools] add_intent/record_fact seed unavailable; closure schema refresh will retry on next start")
			return
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
		log.Printf("[tools] refreshed add_intent/http_request/record_fact schemas for role-aware dispatch, semantic repeats, bounded previews and provenance-gated fact closure (one-time)")
	})
}

func (installer orchestrationSeed) defaults() {
	const flag = "tool_schema_refresh_v7_list_facts_paging"
	installer.once(flag, func() {
		tools := append(installer.server.orchestrationTools(), installer.server.platformTools()...)
		for _, t := range tools {
			schema, _ := json.Marshal(t.InputSchema())
			if err := installer.server.m.pg.RefreshToolDefaults(t.Name(), t.Description(), schema); err != nil {
				log.Printf("[tools] refresh %s schema failed: %v", t.Name(), err)
			}
		}
		// also refresh some built-in agent tools to the code defaults:
		//   - goal_metold DBs' seeded description has the misleading phrase 'end this planning round', which makes the planner treat it as
		//     a means to 'end an empty round', causing it to wrongly judge the whole task complete right after starting.
		//   - insert_assetsadd a related parameter (marks whether an asset is relevant to the current task, deciding whether it counts toward coverage),
		//     SeedTool is first-insert only, otherwise an old DB's already-seeded schema would not receive this new parameter.
		//   - list_factsswitch to pagination, add limit/before/q parameters; otherwise an old DB's empty seeded schema
		//     would show 'no parameters' on the tool management page, and the model would not get these parameter descriptions either.
		refreshBuiltin := map[string]bool{"goal_met": true, "insert_assets": true, "list_facts": true}
		for _, sd := range agent.BuiltinToolSeeds() {
			if !refreshBuiltin[sd.Key] {
				continue
			}
			schema, _ := json.Marshal(sd.Schema)
			if err := installer.server.m.pg.RefreshToolDefaults(sd.Key, sd.Desc, schema); err != nil {
				log.Printf("[tools] refresh %s desc failed: %v", sd.Key, err)
			}
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
		log.Printf("[tools] refreshed orchestration/platform tool schemas to code defaults (one-time))")
	})
}

func (installer orchestrationSeed) removeGoalMet() {
	const flag = "goal_met_unbind_default_v1"
	installer.once(flag, func() {
		if err := installer.server.m.pg.RemoveAgentFromTool("planner", "goal_met"); err != nil {
			log.Printf("[tools] goal_met failed to unbind from planner: %v", err)
			return
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
	})
}

func (installer orchestrationSeed) reporter() {
	const flag = "reporter_agent_seed_v1"
	installer.once(flag, func() {
		defer func() { _ = installer.server.m.pg.SetSetting(flag, "true") }() // try only once, regardless of success

		if exist, _ := installer.server.m.pg.GetAgentByKey("reporter"); exist != nil {
			return // key already taken (user created it manually) -- do not overwrite
		}
		a, err := installer.server.m.pg.CreateAgent("reporter", "Report Writing",
			"Writes detailed vulnerability reports: automatically triggered when a vulnerability is found; after fetching evidence and the execution trace, it writes a Markdown report and saves it back.")
		if err != nil {
			log.Printf("[reporter] failed to create agent: %v", err)
			return
		}
		if err := installer.server.m.pg.SeedPromptIfEmpty(a.ID, agent.ReporterDefaultPrompt); err != nil {
			log.Printf("[reporter] seed prompt failed: %v", err)
		}
		// trigger run policy: parallel + none -- one report per vulnerability; multiple findings run concurrently, each writing its own.
		// merge must be none: otherwise (default all) a wave of findings would be merged into one run, making parallelism pointless.
		// maxParallel=5: at most 5 report sessions at once, to avoid too many simultaneous LLM calls.
		if err := installer.server.m.pg.SetAgentTriggerBehavior("reporter", "parallel", "none", 5); err != nil {
			log.Printf("[reporter] failed to set the trigger run policy: %v", err)
		}
		// bind the tools it needs: write reports + read evidence/execution-trace/situation.
		if err := installer.server.m.pg.AddAgentToToolBinding("reporter", []string{
			"update_finding_report", "get_task_node_detail", "list_task_findings",
			"get_task_worker_trace", "list_task_worker_traces", "search_task_worker_traces",
			"get_task_graph",
		}); err != nil {
			log.Printf("[reporter] failed to bind tools: %v", err)
		}
		// trigger: fires when report_finding is called (the tool returns "finding recorded: <id>" carrying finding_id,
		// the task id is also in the trigger message).
		if _, err := installer.server.m.pg.CreateTrigger(&db.AgentTrigger{
			AgentKey:        "reporter",
			Enabled:         true,
			OnToolCall:      true,
			ToolNames:       []string{"report_finding"},
			ToolCallMessage: reporterToolCallMessage,
		}); err != nil {
			log.Printf("[reporter] failed to create trigger: %v", err)
		}
		log.Printf("[reporter] pre-provisioned the 'Report Writing' agent + finding trigger")
	})
}

func (installer orchestrationSeed) autoFinding() {
	const flag = "auto_report_finding_v1"
	installer.once(flag, func() {
		if err := installer.server.m.pg.AddAgentToToolBinding("auto", []string{"report_finding"}); err != nil {
			log.Printf("[auto] report_finding default binding failed: %v", err)
			return
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
	})
}

func (installer orchestrationSeed) plannerFinding() {
	const flag = "planner_report_finding_v1"
	installer.once(flag, func() {
		if err := installer.server.m.pg.AddAgentToToolBinding("planner", []string{"report_finding"}); err != nil {
			log.Printf("[planner] report_finding default binding failed: %v", err)
			return
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
	})
}

func (installer orchestrationSeed) plannerAssets() {
	const flag = "planner_list_assets_v1"
	installer.once(flag, func() {
		if err := installer.server.m.pg.AddAgentToToolBinding("planner", []string{"list_assets"}); err != nil {
			log.Printf("[planner] list_assets default binding failed: %v", err)
			return
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
	})
}

func (installer orchestrationSeed) companyScope() {
	const flag = "company_scope_rebind_v1"
	installer.once(flag, func() {
		if err := installer.server.m.pg.AddAgentToToolBinding("planner", []string{"add_company_scope"}); err != nil {
			log.Printf("[planner] add_company_scope default binding failed: %v", err)
			return
		}
		if err := installer.server.m.pg.RemoveAgentFromTool("worker", "add_company_scope"); err != nil {
			log.Printf("[worker] add_company_scope unbind failed: %v", err)
			return
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
	})
}

func (installer orchestrationSeed) workerReads() {
	const flag = "worker_readtools_unbind_v1"
	installer.once(flag, func() {
		for _, k := range []string{
			"list_facts", "list_companies", "list_worker_traces",
		} {
			if err := installer.server.m.pg.RemoveAgentFromTool("worker", k); err != nil {
				log.Printf("[worker] %s unbind from worker failed: %v", k, err)
				return // on error, do not persist the flag; retry on next startup
			}
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
	})
}

func (installer orchestrationSeed) workerReadback() {
	const flag = "worker_readback_rebind_v2"
	installer.once(flag, func() {
		if err := installer.server.m.pg.AddAgentToToolBinding("worker", []string{
			"search_all_worker_traces", "get_worker_trace", "node_detail",
		}); err != nil {
			log.Printf("[worker] failed to re-bind readback/detail tools: %v", err)
			return // on error, do not persist the flag; retry on next startup
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
	})
}

func (installer orchestrationSeed) autoDefaults() {
	const flag = "auto_default_bindings_v3"
	installer.once(flag, func() {
		keys := make([]string, 0, len(platformToolKeys)+12)
		for _, t := range installer.server.orchestrationTools() {
			keys = append(keys, t.Name())
		}
		keys = append(keys, platformToolKeys...)
		// asset tools: Auto operating the platform often needs to view/register assets and manage company scope.
		keys = append(keys, "insert_assets", "add_company_scope", "list_assets")
		if err := installer.server.m.pg.AddAgentToToolBinding("auto", keys); err != nil {
			log.Printf("[auto] default binding failed: %v", err)
			return
		}
		_ = installer.server.m.pg.SetSetting(flag, "true")
	})
}
