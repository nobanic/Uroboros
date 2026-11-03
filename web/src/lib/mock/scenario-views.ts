import type { Agent, AgentDetail, Asset, Finding, FindingAsset, PromptVar, PromptVersion, Stats, Task } from "../types";

export interface ScenarioSources {
  tasks(): Task[];
  assets(): Asset[];
  findings(): Finding[];
  agents(): Agent[];
  promptVars(): PromptVar[];
  promptVersions(): PromptVersion[];
  nodeAssets(): Record<string, number[]>;
  activeTask(): string;
}

/** Read borrowed source rows. Do not replace them with cached indexes or clones. */
export class ScenarioViews {
  constructor(private readonly source: ScenarioSources) {}

  task(id: string): Task | undefined {
    return this.source.tasks().find((t) => t.id === id);
  }

  stats(
    taskId?: string,
    state: { tasks?: readonly Task[]; findings?: readonly Finding[]; activeTask?: string } = {},
  ): Stats & { active_task?: unknown } {
    const tasks = this.source.tasks(),
      findings = this.source.findings(),
      assets = this.source.assets(),
      ACTIVE_TASK = this.source.activeTask();

    const taskList = state.tasks ?? tasks;
    const findingList = state.findings ?? findings;
    const base: Stats = {
      assets: assets.length,
      engine_mode: "exploring",
      llm_configured: true,
      roe_enabled: true,
      findings_confirmed: findingList.filter((f) => f.severity !== "low").length,
    };
    const selectedTaskID = taskId ?? state.activeTask ?? ACTIVE_TASK;
    const t = taskList.find((item) => item.id === selectedTaskID);
    if (!t) return base;
    return {
      ...base,
      engine_mode: t.engine_mode ?? "idle",
      findings_confirmed: findingList.filter((f) => f.task_id === t.id).length,
      active_task: {
        id: t.id,
        in_flight: t.in_flight,
        goals_total: t.goals_total,
        goals_met: t.goals_met,
        engine_mode: t.engine_mode,
        paused: t.paused,
      },
    };
  }

  private asset(id: number, required: boolean): FindingAsset | undefined {
    const asset = this.source.assets().find((candidate) => candidate.id === id);
    if (!asset) {
      if (required) throw new Error(`mock assetRef: unknown asset ${id}`);
      return undefined;
    }
    return {
      id: String(id),
      type: asset.type,
      label: asset.url || asset.domain || asset.ip || asset.app_name || String(id),
    };
  }

  assetRef(id: number): FindingAsset {
    return this.asset(id, true)!;
  }

  nodeAssetsFor(nodeId: string): FindingAsset[] {
    const NODE_ASSETS = this.source.nodeAssets();
    return (NODE_ASSETS[nodeId] ?? []).flatMap((id) => {
      const reference = this.asset(id, false);
      return reference ? [reference] : [];
    });
  }

  assetReferences() {
    return {
      intents: [
        referenceRow(
          12,
          "intent",
          "done",
          "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
        ),
        referenceRow(
          18,
          "intent",
          "running",
          "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check.",
        ),
      ],
      facts: [
        referenceRow(
          34,
          "fact",
          "confirmed",
          "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
        ),
      ],
      findings: [
        referenceRow(
          41,
          "finding",
          "confirmed",
          "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
        ),
      ],
    };
  }

  agentDetail(key: string): AgentDetail {
    const agents = this.source.agents(),
      promptVars = this.source.promptVars(),
      promptVersions = this.source.promptVersions();

    const a = agents.find((x) => x.key === key) ?? agents[1];
    return {
      agent: a,
      prompt: `You are the Uroboros ${a.name}.\nObjective: {{.Goal}}\nAsset overview: {{.AssetSummary}}\nRoute hint: {{.RouteHint}}\nAdvance the authorized assessment and write only verified results back to the exploration graph.`,
      variables: promptVars,
      versions: promptVersions,
      visibility: { mcp: [1, 2], skill: ["api-recon", "playwright-cli"] },
      wrapup_prompt: "",
      wrapup_default:
        "Time or turn budget is nearly exhausted. Summarize verified findings and mark the intent's final state.",
      wrapup_max_turns: 0,
      wrapup_max_turns_default: 3,
      task_timeout_wrapup_supported: a.key === "worker" || a.key === "planner",
      task_timeout_wrapup_default:
        "The mission timed out. Stop execution, preserve evidence, and record the current conclusion.",
      task_timeout_wrapup_max_turns_default: 2,
    };
  }
}

function referenceRow(id: number, kind: string, state: string, summary: string) {
  return { id, kind, state, summary };
}
