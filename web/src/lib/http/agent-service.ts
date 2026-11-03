import type { Agent, AgentDetail, AgentTrigger, PromptVar, PromptVersion } from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { Resource } from "./resources";

type Trigger = Omit<AgentTrigger, "id" | "agent_key" | "last_fire">;
type AgentConfig = {
  llm_profile_id?: number | null;
  max_turns?: number;
  run_seconds?: number;
  web_search?: boolean;
  interactive_shell?: boolean;
  trigger_run_mode?: "serial" | "parallel";
  trigger_merge_mode?: "by_task" | "all" | "none";
  trigger_max_parallel?: number;
};
type Ack = { ok: boolean };

/** Agent resources preserve sparse config patches, explicit profile unbinding and independent prompt reset endpoints. */
export function agentService(wire: ApplicationWire) {
  const root = new Resource(wire, "/agents");
  const agent = (key: string) => root.at(String(key));
  const triggers = new Resource(wire, "/triggers");
  const wrapup = (key: string, timeout: boolean) =>
    timeout ? agent(key).at("wrapup", "task-timeout") : agent(key).at("wrapup");
  const saveWrapup = (key: string, timeout: boolean, prompt: string, maxTurns?: number) =>
    wrapup(key, timeout).send<Ack>("PUT", { prompt, max_turns: maxTurns });
  return {
    agents: () => root.list<Agent>("agents"),
    getAgent: (key: string) => agent(key).read<AgentDetail>(),
    createAgent: (key: string, name: string, description = "") => root.send<Agent>("POST", { key, name, description }),
    updateAgent: (key: string, name: string, description = "") => agent(key).send<Ack>("PATCH", { name, description }),
    deleteAgent: (key: string) => agent(key).send<{ deleted: string }>("DELETE"),
    saveAgentPrompt: (key: string, template: string, note = "") =>
      agent(key).at("prompt").send<{ version: number }>("PUT", { template, note }),
    resetAgentPrompt: (key: string) => agent(key).at("prompt", "reset").send<{ version: number }>("POST", {}),
    saveAgentWrapup: (key: string, prompt: string, maxTurns?: number) => saveWrapup(key, false, prompt, maxTurns),
    resetAgentWrapup: (key: string) =>
      wrapup(key, false)
        .at("reset")
        .send<Ack & { wrapup_default: string; wrapup_max_turns_default: number }>("POST", {}),
    saveAgentTaskTimeoutWrapup: (key: string, prompt: string, maxTurns?: number) =>
      saveWrapup(key, true, prompt, maxTurns),
    resetAgentTaskTimeoutWrapup: (key: string) =>
      wrapup(key, true)
        .at("reset")
        .send<Ack & { task_timeout_wrapup_default: string; task_timeout_wrapup_max_turns_default: number }>("POST", {}),
    agentTriggers: (key: string) => agent(key).at("triggers").list<AgentTrigger>("triggers"),
    createTrigger: (key: string, input: Trigger) => agent(key).at("triggers").send<AgentTrigger>("POST", input),
    updateTrigger: (id: number, input: Trigger) => triggers.at(String(id)).send<Ack>("PATCH", input),
    deleteTrigger: (id: number) => triggers.at(String(id)).send<{ deleted: number }>("DELETE"),
    saveAgentConfig: (key: string, patch: AgentConfig) => agent(key).at("config").send<Ack>("PUT", patch),
    agentPromptVersions: (key: string) => agent(key).at("prompts").list<PromptVersion>("versions"),
    agentVariables: (key: string) => agent(key).at("variables").list<PromptVar>("variables"),
    previewAgentPrompt: (key: string, template: string, sample?: Record<string, string>) =>
      agent(key).at("prompt", "preview").send<{ rendered: string; error?: string }>("POST", { template, sample }),
    getAgentVisibility: (key: string) => agent(key).at("visibility").read<{ mcp: number[]; skill: string[] }>(),
    setAgentVisibility: (key: string, mcp: number[], skill: string[]) =>
      agent(key).at("visibility").send<Ack>("PUT", { mcp, skill }),
  };
}
