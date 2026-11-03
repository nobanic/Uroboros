import type { InterceptRule, JudgeConfig, Tool } from "@/lib/types";

export type PolicyDraft = Omit<InterceptRule, "id" | "created_at" | "updated_at">;
export type PolicyTool = Pick<Tool, "key" | "description" | "kind">;
export const policyActions = { allow: "Allow execution", ask: "Request approval", deny: "Block execution" } as const;
export function policyDraft(rule?: InterceptRule): PolicyDraft {
  const source = rule ?? {
    name: "",
    enabled: true,
    priority: 0,
    match_target: "tool_name",
    match_type: "string",
    pattern: "",
    action: "deny",
    message: "",
    timeout_enabled: true,
    timeout_seconds: 60,
    timeout_action: "deny",
  };
  return {
    name: source.name,
    enabled: source.enabled,
    priority: source.priority,
    match_target: source.match_target,
    match_type: source.match_type,
    pattern: source.pattern,
    action: source.action,
    message: source.message,
    timeout_enabled: source.timeout_enabled,
    timeout_seconds: source.timeout_seconds,
    timeout_action: source.timeout_action,
  };
}
export function policyDraftError(draft: PolicyDraft) {
  if (!draft.name.trim()) return "Name cannot be empty";
  if (!draft.pattern.trim()) return "Pattern cannot be empty";
  return "";
}
export function policyRegexNotice(draft: PolicyDraft) {
  if (draft.match_type !== "regex" || !draft.pattern) return false;
  try {
    new RegExp(draft.pattern);
    return false;
  } catch {
    return true;
  }
}
export function policyRules(rows: InterceptRule[], search = "", action = "all") {
  const needle = search.trim().toLocaleLowerCase();
  return [...rows]
    .sort((a, b) => b.priority - a.priority)
    .filter(
      (row) =>
        (action === "all" || row.action === action) &&
        (!needle ||
          [row.name, row.pattern, row.message, row.match_target].some((value) =>
            value.toLocaleLowerCase().includes(needle),
          )),
    );
}
export function policyCounts(rows: InterceptRule[]) {
  const enabled = rows.filter((row) => row.enabled);
  return {
    total: rows.length,
    enabled: enabled.length,
    allow: enabled.filter((row) => row.action === "allow").length,
    ask: enabled.filter((row) => row.action === "ask").length,
    deny: enabled.filter((row) => row.action === "deny").length,
  };
}
const sdkGroups = [
  {
    label: "Execution",
    tools: [
      ["Bash", "Run a command in the shell"],
      ["WebFetch", "Make an HTTP/HTTPS request (with proxy support)"],
      ["web_search", "Web search"],
      ["vuln_lookup", "Look up CVE/GHSA intelligence"],
      ["vuln_match", "Match package, purl, or CPE version intelligence"],
      ["vuln_prioritize", "Prioritize CVEs with KEV and EPSS"],
      ["shell_open", "Open a persistent PTY interactive session"],
      ["shell_send", "Send input to an interactive session"],
      ["shell_read", "Read output from an interactive session"],
      ["shell_close", "Close an interactive session"],
      ["shell_list", "List all interactive sessions"],
    ],
  },
  {
    label: "Write / Edit",
    tools: [
      ["Write", "Write a file"],
      ["Edit", "Edit a file (exact replacement)"],
      ["MultiEdit", "Batch-edit files"],
    ],
  },
] as const;
export function policyToolGroups(tools: Tool[], selected: ReadonlySet<string>, search = "") {
  const builtin = sdkGroups.map((group) => ({
    label: group.label as string,
    tools: group.tools.map(([key, description]) => ({ key, description, kind: "builtin" }) as PolicyTool),
  }));
  const known = new Set(builtin.flatMap((group) => group.tools.map((tool) => tool.key)));
  const system: PolicyTool[] = [],
    custom: PolicyTool[] = [];
  for (const tool of tools) {
    if (known.has(tool.key)) continue;
    known.add(tool.key);
    (tool.system ? system : custom).push(tool);
  }
  const unknown = [...selected]
    .filter((key) => !known.has(key))
    .map((key) => ({
      key,
      description: "Configured key absent from this catalog. Kept until explicitly removed.",
      kind: "unlisted",
    }));
  const needle = search.trim().toLocaleLowerCase();
  return [
    ...builtin,
    { label: "System tools", tools: system },
    { label: "Custom tools", tools: custom },
    { label: "Unlisted configured tools", tools: unknown },
  ]
    .map((group) => ({
      ...group,
      tools: group.tools.filter(
        (tool) => !needle || `${tool.key} ${tool.description}`.toLocaleLowerCase().includes(needle),
      ),
    }))
    .filter((group) => group.tools.length);
}
export function policyJudgeDefaults(): JudgeConfig {
  return {
    enabled: false,
    profile_id: 0,
    prompt: "",
    timeout_seconds: 15,
    fail_action: "allow",
    ask_timeout_seconds: 300,
    ask_timeout_action: "deny",
  };
}
export function policyTokens(value: number) {
  if (value >= 1e6) return `${(value / 1e6).toFixed(1)}M`;
  if (value >= 1000) return `${(value / 1000).toFixed(1)}k`;
  return String(value);
}
