import type { Agent, AgentDetail, AgentTrigger } from "@/lib/types";
import { formDocument, type FormGroup, type FormValues } from "./forms";

export function agentRuntimeForm(detail: AgentDetail, webSearchEnabled = true): FormGroup[] {
  const agent = detail.agent,
    profiles: [string, string][] = [
      ["", "Follow operation, conversation or workspace default"],
      ...(detail.llm_profiles || []).map(
        (profile) => [String(profile.id), `${profile.name} · ${profile.model}`] as [string, string],
      ),
    ];
  if (agent.llm_profile_id && !profiles.some(([id]) => id === String(agent.llm_profile_id)))
    profiles.push([String(agent.llm_profile_id), `Unavailable profile #${agent.llm_profile_id}`]);
  return [
    {
      title: "Runtime",
      description:
        "Agent model bindings take precedence over operation and conversation choices. Zero runtime budgets are unlimited.",
      fields: [
        { key: "llm_profile_id", label: "Model binding", options: profiles },
        ...(!["mainagent", "goals"].includes(agent.key)
          ? [
              { key: "max_turns", label: "Maximum turns (0 is unlimited)", type: "number" as const, min: 0, step: 1 },
              {
                key: "run_seconds",
                label: "Run budget in seconds (0 is unlimited)",
                type: "number" as const,
                min: 0,
                step: 1,
              },
            ]
          : []),
        ...(agent.key !== "goals"
          ? [
              {
                key: "web_search",
                label: "Web search",
                disabled: !webSearchEnabled,
                type: "checkbox" as const,
                hint: "The workspace master switch and provider configuration also apply.",
              },
              { key: "interactive_shell", label: "Persistent interactive shell", type: "checkbox" as const },
            ]
          : []),
      ],
    },
  ];
}

export function agentRuntimeDraft(detail: AgentDetail): FormValues {
  return {
    llm_profile_id: detail.agent.llm_profile_id == null ? "" : String(detail.agent.llm_profile_id),
    max_turns: String(detail.agent.max_turns ?? 0),
    run_seconds: String(detail.agent.run_seconds ?? 600),
    web_search: Boolean(detail.agent.web_search),
    interactive_shell: Boolean(detail.agent.interactive_shell),
  };
}
export function wholeBudget(value: unknown, title: string): number {
  const number = Number(value || 0);
  if (!Number.isSafeInteger(number) || number < 0) throw new Error(`${title} must be a non-negative whole number.`);
  return number;
}
export function agentRuntimeDocument(detail: AgentDetail, values: FormValues) {
  const document = formDocument(agentRuntimeForm(detail), values);
  const binding = String(document.llm_profile_id || "");
  const result: Record<string, unknown> = {
    ...document,
    llm_profile_id: binding ? wholeBudget(binding, "Model ID") : null,
  };
  for (const field of ["max_turns", "run_seconds"])
    if (field in document) result[field] = wholeBudget(document[field], field);
  return result;
}
export function agentIdentityDocument(values: FormValues, create: boolean) {
  const name = String(values.name || "").trim(),
    key = String(values.key || "").trim();
  if (!name) throw new Error("Enter an agent name.");
  if (create && !/^[a-z][a-z0-9_]*$/.test(key))
    throw new Error(
      "The agent key must start with a lowercase letter and contain lowercase letters, digits or underscores.",
    );
  return { ...(create ? { key } : {}), name, description: String(values.description || "") };
}

export const triggerConditions = [
  ["on_finding", "finding_message", "New finding"],
  ["on_goal_met", "goal_message", "Goal met"],
  ["on_task_timeout", "task_timeout_message", "Operation timeout"],
  ["on_tool_call", "tool_call_message", "Tool completed"],
  ["on_task_create", "task_create_message", "Operation created"],
] as const;
export function triggerDraft(trigger?: AgentTrigger): FormValues {
  return {
    enabled: trigger?.enabled ?? true,
    timer: (trigger?.interval_sec || 0) > 0,
    interval_sec: String(trigger?.interval_sec || 60),
    interval_message: trigger?.interval_message || "",
    ...Object.fromEntries(
      triggerConditions.flatMap(([flag, message]) => [
        [flag, Boolean(trigger?.[flag])],
        [message, trigger?.[message] || ""],
      ]),
    ),
    tool_names: [...(trigger?.tool_names || [])],
  };
}
export const triggerForm: FormGroup[] = [
  {
    title: "Timer",
    fields: [
      { key: "enabled", label: "Trigger enabled", type: "checkbox" },
      { key: "timer", label: "Run on a schedule", type: "checkbox" },
      {
        key: "interval_sec",
        label: "Interval in seconds",
        type: "number",
        min: 1,
        step: 1,
        when: (value) => Boolean(value.timer),
      },
      { key: "interval_message", label: "Timer message", type: "textarea" },
    ],
  },
  ...triggerConditions.map(([flag, message, label]) => ({
    title: label,
    fields: [
      { key: flag, label: `On ${label.toLowerCase()}`, type: "checkbox" as const },
      { key: message, label: `${label} message`, type: "textarea" as const },
    ],
  })),
];
export function triggerDocument(values: FormValues) {
  const interval = values.timer ? wholeBudget(values.interval_sec, "Timer interval") : 0;
  if (values.timer && interval < 1) throw new Error("Timer interval must be at least one second.");
  if (!interval && !triggerConditions.some(([flag]) => values[flag]))
    throw new Error("Select at least one trigger condition.");
  const tool_names =
    values.on_tool_call && Array.isArray(values.tool_names) ? [...new Set(values.tool_names.map(String))] : [];
  if (values.on_tool_call && !tool_names.length)
    throw new Error("Select at least one tool for the tool-call condition.");
  return {
    enabled: Boolean(values.enabled),
    interval_sec: interval,
    interval_message: String(values.interval_message || "").trim(),
    ...Object.fromEntries(
      triggerConditions.flatMap(([flag, message]) => [
        [flag, Boolean(values[flag])],
        [message, String(values[message] || "").trim()],
      ]),
    ),
    tool_names,
  };
}
export const triggerPolicyForm: FormGroup[] = [
  {
    title: "Trigger execution policy",
    fields: [
      {
        key: "trigger_run_mode",
        label: "Run mode",
        options: [
          ["serial", "Serial queue"],
          ["parallel", "Parallel sessions"],
        ],
      },
      {
        key: "trigger_merge_mode",
        label: "Serial event merging",
        options: [
          ["by_task", "Per operation"],
          ["all", "All events"],
          ["none", "Keep separate"],
        ],
      },
      {
        key: "trigger_max_parallel",
        label: "Parallel session limit (0 is unlimited)",
        type: "number",
        min: 0,
        step: 1,
      },
    ],
  },
];
export function triggerPolicyDraft(agent: Agent): FormValues {
  return {
    trigger_run_mode: agent.trigger_run_mode || "serial",
    trigger_merge_mode: agent.trigger_merge_mode || "all",
    trigger_max_parallel: String(agent.trigger_max_parallel ?? 5),
  };
}
export function triggerPolicyDocument(values: FormValues) {
  const document = formDocument(triggerPolicyForm, values);
  return { ...document, trigger_max_parallel: wholeBudget(document.trigger_max_parallel, "Parallel session limit") };
}

export type PromptChange = { kind: "same" | "removed" | "added"; text: string };
export function promptChanges(before: string, after: string): PromptChange[] {
  const left = before.split("\n"),
    right = after.split("\n"),
    result: PromptChange[] = [];
  let prefix = 0,
    suffix = 0;
  while (prefix < left.length && prefix < right.length && left[prefix] === right[prefix]) {
    result.push({ kind: "same", text: left[prefix] });
    prefix++;
  }
  while (
    suffix < left.length - prefix &&
    suffix < right.length - prefix &&
    left[left.length - suffix - 1] === right[right.length - suffix - 1]
  )
    suffix++;
  const a = left.slice(prefix, left.length - suffix),
    b = right.slice(prefix, right.length - suffix);
  // Bound comparison work for very large prompts. The fallback is a complete,
  // valid replacement diff; it never truncates or hides either prompt body.
  if ((a.length + 1) * (b.length + 1) > 1_000_000) {
    for (const text of a) result.push({ kind: "removed", text });
    for (const text of b) result.push({ kind: "added", text });
  } else {
    const grid = Array.from({ length: a.length + 1 }, () => new Uint32Array(b.length + 1));
    for (let i = a.length - 1; i >= 0; i--)
      for (let j = b.length - 1; j >= 0; j--)
        grid[i][j] = a[i] === b[j] ? grid[i + 1][j + 1] + 1 : Math.max(grid[i + 1][j], grid[i][j + 1]);
    let i = 0,
      j = 0;
    while (i < a.length || j < b.length) {
      if (i < a.length && j < b.length && a[i] === b[j]) {
        result.push({ kind: "same", text: a[i++] });
        j++;
      } else if (i < a.length && (j === b.length || grid[i + 1][j] >= grid[i][j + 1]))
        result.push({ kind: "removed", text: a[i++] });
      else result.push({ kind: "added", text: b[j++] });
    }
  }
  for (const text of left.slice(left.length - suffix)) result.push({ kind: "same", text });
  return result;
}
