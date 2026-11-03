import type { MCPServer, MCPTool, MissingSkill, SkillCall, SkillItem, Tool } from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { Resource, slice } from "./resources";

type Ack = { ok: boolean };
type CustomTool = Pick<Tool, "description" | "schema" | "agents" | "enabled" | "kind" | "exec" | "deferred">;
type Skill = {
  name: string;
  description: string;
  license?: string;
  compatibility?: string;
  mcps?: string[];
  instructions?: string;
};

/** Catalog reads and explicit mutations share scoped paths; none of these adapters executes or auto-registers a capability. */
export function capabilityService(wire: ApplicationWire) {
  const tools = new Resource(wire, "/tools");
  const custom = tools.at("custom");
  const mcp = new Resource(wire, "/mcp");
  const skills = new Resource(wire, "/skills");
  const skill = (name: string) => skills.at(String(name));
  const files = (name: string) => skill(name).at("files");
  const visibility = new Resource(wire, "/visibility");
  const toolVisibility = (id: number) => mcp.at(String(id), "tool-visibility");
  return {
    tools: () => tools.list<Tool>("tools"),
    saveTool: (key: string, patch: Pick<Tool, "description" | "schema" | "agents" | "enabled">) =>
      tools.at(String(key)).send<Ack>("PUT", patch),
    resetTool: (key: string) => tools.at(String(key), "reset").send<Ack>("POST", {}),
    createCustomTool: (input: CustomTool & Pick<Tool, "key">) => custom.send<{ key: string }>("POST", input),
    updateCustomTool: (key: string, input: CustomTool) => custom.at(String(key)).send<Ack>("PUT", input),
    deleteCustomTool: (key: string) => custom.at(String(key)).send<{ deleted: string }>("DELETE"),
    testCustomTool: (body: { kind: string; exec: Record<string, unknown>; params: Record<string, unknown> }) =>
      custom.at("test").send<{ output: string; is_error: boolean }>("POST", body),
    mcpServers: () => mcp.list<MCPServer>("servers"),
    saveMcpServer: (input: Partial<MCPServer>) => mcp.send<{ id: number }>("POST", input),
    deleteMcpServer: (id: number) => mcp.at(String(id)).send<{ deleted: number }>("DELETE"),
    mcpTools: (id: number) => mcp.at(String(id), "tools").list<MCPTool>("tools"),
    mcpToolVisibility: (id: number) =>
      toolVisibility(id)
        .read<{ policies: Record<string, Record<string, boolean>> }>()
        .then((value) => value.policies),
    setMcpToolVisibility: (id: number, agentId: string, toolName: string, enabled: boolean) =>
      toolVisibility(id).send<Ack>("PUT", { agent_id: agentId, tool_name: toolName, enabled }),
    refreshMcpServer: (id: number) =>
      mcp
        .at(String(id), "refresh")
        .send<{ tools: MCPTool[] }>("POST", {})
        .then((value) => slice(value.tools)),
    skills: () => skills.list<SkillItem>("skills"),
    createSkill: (input: Skill) => skills.send<{ name: string }>("POST", input),
    deleteSkill: (name: string) => skill(name).send<{ deleted: string }>("DELETE"),
    updateSkillMeta: (
      name: string,
      meta: { mcps?: string[]; description?: string; license?: string; compatibility?: string },
    ) => skill(name).at("meta").send<Ack>("PUT", meta),
    createSkillDir: (name: string, path: string) => skill(name).at("dirs").send<{ dir: string }>("POST", { path }),
    skillFiles: (name: string) =>
      files(name)
        .read<{ files: string[] }>()
        .then((value) => value.files),
    readSkillFile: (name: string, file: string) =>
      files(name)
        .at(String(file))
        .read<{ content: string; file: string }>()
        .then((value) => value.content),
    writeSkillFile: (name: string, file: string, content: string) =>
      files(name).at(String(file)).send<Ack>("PUT", { content }),
    deleteSkillPath: (name: string, path: string) => files(name).at(String(path)).send<{ deleted: string }>("DELETE"),
    skillUsage: (name: string, limit = 50) =>
      skill(name)
        .at("usage")
        .query([["limit", limit]], "component")
        .list<SkillCall>("calls"),
    missingSkills: (limit = 20) =>
      skills
        .at("missing")
        .query([["limit", limit]], "component")
        .list<MissingSkill>("missing"),
    resourceVisibility: (kind: string, id: number) => visibility.at(String(kind), String(id)).list<string>("agents"),
    toggleVisibility: (agentId: string, kind: string, resourceId: number, visible: boolean) =>
      visibility.at("toggle").send<Ack>("POST", { agent_id: agentId, kind, resource_id: resourceId, visible }),
    skillVisibility: (name: string) => visibility.at("skill", String(name)).list<string>("agents"),
    toggleSkillVisibility: (agentId: string, skillName: string, visible: boolean) =>
      visibility.at("skill", "toggle").send<Ack>("POST", { agent_id: agentId, skill_name: skillName, visible }),
  };
}
