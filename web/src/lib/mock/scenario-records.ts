import type { Agent, Asset, Finding, LLMProfile, Task, Tool } from "../types";

type TaskSeed = Omit<Task, "paused" | "active" | "stalled" | "engine_mode" | "llm_failover_state"> & {
  active?: boolean;
};
type FindingSeed = Omit<Finding, "task_id" | "task_description" | "status"> & {
  status?: Finding["status"];
};
type AgentSeed = Omit<Agent, "enabled" | "max_turns"> & { max_turns?: number };
type ToolSeed = Omit<Tool, "system" | "enabled" | "kind"> & { kind?: Tool["kind"] };
type ProfileSeed = Omit<LLMProfile, "rate_per_second" | "pool_exclude">;

/** Seed-time policies only. Returned rows and nested values remain ordinary mutable records. */
export class ScenarioRecords {
  constructor(private readonly companyID: number) {}

  asset(
    id: number,
    type: Asset["type"],
    taskIDs: number[],
    lastSeen: string,
    fields: Omit<Asset, "id" | "type" | "company_id" | "task_ids" | "last_seen">,
  ): Asset {
    return { id, type, company_id: this.companyID, task_ids: taskIDs, ...fields, last_seen: lastSeen };
  }

  finding(task: Task, seed: FindingSeed): Finding {
    return {
      id: seed.id,
      ...(seed.assets !== undefined ? { assets: seed.assets } : {}),
      vulnclass: seed.vulnclass,
      ...(seed.name !== undefined ? { name: seed.name } : {}),
      severity: seed.severity,
      status: seed.status ?? "pending",
      ...(seed.report !== undefined ? { report: seed.report } : {}),
      summary: seed.summary,
      evidence: seed.evidence,
      ...(seed.intent_id !== undefined ? { intent_id: seed.intent_id } : {}),
      task_id: task.id,
      task_description: task.description,
      ts: seed.ts,
    };
  }

  task(seed: TaskSeed): Task {
    return {
      id: seed.id,
      name: seed.name,
      category_id: seed.category_id,
      category_name: seed.category_name,
      description: seed.description,
      goal: seed.goal,
      status: seed.status,
      created_at: seed.created_at,
      created_unix: seed.created_unix,
      ...(seed.completed_at !== undefined ? { completed_at: seed.completed_at } : {}),
      ...(seed.completed_unix !== undefined ? { completed_unix: seed.completed_unix } : {}),
      last_activity: seed.last_activity,
      last_activity_unix: seed.last_activity_unix,
      paused: seed.status === "paused",
      active: seed.active ?? false,
      in_flight: seed.in_flight,
      ...(seed.findings !== undefined ? { findings: seed.findings } : {}),
      stalled: false,
      goals_total: seed.goals_total,
      goals_met: seed.goals_met,
      engine_mode: seed.status === "running" ? "exploring" : seed.status === "paused" ? "paused" : "idle",
      llm_profile_id: seed.llm_profile_id,
      llm_profile_ids: seed.llm_profile_ids,
      active_llm_profile_id: seed.active_llm_profile_id,
      llm_failover_state: "ready",
      source_task_ids: seed.source_task_ids,
      tokens: seed.tokens,
    };
  }

  agent(seed: AgentSeed): Agent {
    return {
      id: seed.id,
      key: seed.key,
      name: seed.name,
      role: seed.role,
      builtin: seed.builtin,
      enabled: true,
      description: seed.description,
      max_turns: seed.max_turns ?? 0,
      ...(seed.run_seconds !== undefined ? { run_seconds: seed.run_seconds } : {}),
      ...(seed.web_search !== undefined ? { web_search: seed.web_search } : {}),
      ...(seed.interactive_shell !== undefined ? { interactive_shell: seed.interactive_shell } : {}),
      mcp_count: seed.mcp_count,
      skill_count: seed.skill_count,
      tool_count: seed.tool_count,
    };
  }

  tool(seed: ToolSeed): Tool {
    const kind = seed.kind ?? "builtin";
    return {
      key: seed.key,
      system: kind === "builtin",
      family: seed.family,
      description: seed.description,
      schema: seed.schema,
      agents: seed.agents,
      enabled: true,
      kind,
      ...(seed.exec !== undefined ? { exec: seed.exec } : {}),
      calls: seed.calls,
    };
  }

  profile(seed: ProfileSeed): LLMProfile {
    return {
      id: seed.id,
      name: seed.name,
      format: seed.format,
      ...(seed.base_url !== undefined ? { base_url: seed.base_url } : {}),
      model: seed.model,
      api_key_hint: seed.api_key_hint,
      rate_per_second: 0,
      rate_per_minute: seed.rate_per_minute,
      context_window_k: seed.context_window_k,
      thinking_type: seed.thinking_type,
      reasoning_effort: seed.reasoning_effort,
      is_default: seed.is_default,
      priority: seed.priority,
      pool_exclude: false,
      max_tokens: seed.max_tokens,
      max_tokens_field: seed.max_tokens_field,
    };
  }

  llmConfig(profile: LLMProfile) {
    return {
      configured: true,
      provider: profile.format,
      model: profile.model,
      base_url: profile.base_url ?? "",
      proxy: "",
      key_set: true,
      rate_per_second: profile.rate_per_second,
      rate_per_minute: profile.rate_per_minute,
      context_window_k: profile.context_window_k,
      thinking_type: profile.thinking_type,
      reasoning_effort: profile.reasoning_effort,
    };
  }
}
