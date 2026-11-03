import type { AssetInterceptRuleInput, Task, TaskTemplate } from "@/lib/types";

export const operationStates = ["created", "queued", "running", "paused", "done", "failed", "timeout"] as const;
export type OperationView = {
  query: string;
  status: string;
  category: string;
  sort: "id" | "created" | "duration" | "status";
  ascending: boolean;
  layout: "board" | "ledger";
  size: number;
};
export const initialOperationView: OperationView = {
  query: "",
  status: "all",
  category: "all",
  sort: "id",
  ascending: false,
  layout: "board",
  size: 20,
};

export function operationPreferences(value: unknown): OperationView {
  const source = value && typeof value === "object" ? (value as Partial<OperationView>) : {};
  return {
    ...initialOperationView,
    query: typeof source.query === "string" ? source.query : "",
    status: operationStates.some((state) => state === source.status) ? source.status! : "all",
    category: typeof source.category === "string" && /^(all|none|\d+)$/.test(source.category) ? source.category : "all",
    sort: ["id", "created", "duration", "status"].includes(source.sort || "") ? source.sort! : "id",
    ascending: source.ascending === true,
    layout: source.layout === "ledger" ? "ledger" : "board",
    size: [10, 20, 50, 100].includes(source.size || 0) ? source.size! : 20,
  };
}

export const operationTitle = (task: Task) => task.name?.trim() || `Operation ${task.id}`;
export const operationPinned = (task: Task) => task.pinned ?? Boolean(task.pinned_at);
export function operationSeconds(task: Task, now: number): number {
  const start = task.created_unix || 0;
  const finish = task.status === "running" ? now : task.completed_unix || task.last_activity_unix || 0;
  return start > 0 && finish > start ? finish - start : 0;
}
export function elapsed(seconds: number): string {
  if (seconds < 60) return `${Math.floor(seconds)}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ${Math.floor(seconds % 60)}s`;
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
}
export function operationControl(task: Task): "pause" | "resume" | undefined {
  if (task.status === "paused") return "resume";
  if (["created", "queued", "running"].includes(task.status)) return "pause";
}
export function operationArchiveBlock(task: Task): string | undefined {
  if (task.queued) return "Pause this queued operation before archiving.";
  if (!["paused", "done", "failed", "timeout"].includes(task.status)) return "Pause this operation before archiving.";
  if (task.archive_blocked_by_task_id) return `Archive dependent operation #${task.archive_blocked_by_task_id} first.`;
}
const compareIDs = (left: string, right: string) =>
  String(left).localeCompare(String(right), undefined, { numeric: true, sensitivity: "base" });
export function operationSlice(tasks: readonly Task[], view: OperationView, now: number): Task[] {
  const query = view.query.trim().toLocaleLowerCase();
  const numeric = (task: Task) => {
    switch (view.sort) {
      case "created":
        return task.created_unix || Date.parse(task.created_at) / 1000 || 0;
      case "duration":
        return operationSeconds(task, now);
      case "status":
        return operationStates.indexOf(task.status);
      default:
        return 0;
    }
  };
  return tasks
    .filter((task) => {
      if (view.status !== "all" && task.status !== view.status) return false;
      if (view.category === "none" && task.category_id != null) return false;
      if (!["none", "all"].includes(view.category) && String(task.category_id) !== view.category) return false;
      return (
        !query ||
        [task.id, task.name, task.category_name, task.description, task.goal].some((part) =>
          String(part ?? "")
            .toLocaleLowerCase()
            .includes(query),
        )
      );
    })
    .sort((left, right) => {
      if (operationPinned(left) !== operationPinned(right)) return operationPinned(left) ? -1 : 1;
      if (operationPinned(left) && left.pinned_at !== right.pinned_at)
        return (right.pinned_at || "").localeCompare(left.pinned_at || "");
      const difference = view.sort === "id" ? compareIDs(left.id, right.id) : numeric(left) - numeric(right);
      return difference ? difference * (view.ascending ? 1 : -1) : compareIDs(right.id, left.id);
    });
}

export type OperationDraft = {
  name: string;
  description: string;
  goal: string;
  category: string;
  profiles: string[];
  sources: string[];
  companies: string[];
  timeout: string;
  heartbeat: string;
  seed: boolean;
  coverage: boolean;
  isolated: boolean;
  initialKatana: boolean;
  initialScout: "default" | "on" | "off";
  rules: AssetInterceptRuleInput[];
};
export const emptyOperation = (): OperationDraft => ({
  name: "",
  description: "",
  goal: "",
  category: "",
  profiles: [],
  sources: [],
  companies: [],
  timeout: "",
  heartbeat: "10",
  seed: false,
  coverage: true,
  isolated: false,
  initialKatana: true,
  initialScout: "default",
  rules: [],
});
export function applyOperationTemplate(draft: OperationDraft, template: TaskTemplate): OperationDraft {
  return {
    ...draft,
    description: template.description,
    goal: template.goal,
    category: template.category_id == null ? "" : String(template.category_id),
    rules: structuredClone(template.intercept_rules || []),
  };
}
export function templateDocument(draft: OperationDraft, name: string) {
  if (!name.trim()) throw new Error("Enter a template name.");
  return {
    name: name.trim(),
    description: draft.description,
    goal: draft.goal,
    category_id: draft.category ? Number(draft.category) : null,
    intercept_rules: structuredClone(draft.rules),
  };
}
const distinct = (items: string[]) => [...new Set(items)];
export function operationDocument(draft: OperationDraft) {
  if (!draft.description.trim() || !draft.goal.trim()) throw new Error("Enter an assignment and an observable goal.");
  const sources = distinct(draft.sources),
    profiles = distinct(draft.profiles),
    companies = distinct(draft.companies);
  if (sources.length > 8) throw new Error("An operation can use at most eight source operations.");
  if (draft.isolated && sources.length) throw new Error("An isolated operation cannot inherit source operations.");
  const minutes = Number(draft.timeout || 0),
    heartbeat = Number(draft.heartbeat || 10);
  if (!Number.isFinite(minutes) || minutes < 0 || !Number.isFinite(heartbeat) || heartbeat < 0)
    throw new Error("Timeout and heartbeat must be non-negative numbers.");
  for (const id of [...profiles, ...companies, ...(draft.category ? [draft.category] : [])])
    if (!/^\d+$/.test(id) || Number(id) < 1 || !Number.isSafeInteger(Number(id)))
      throw new Error("The selected record has an invalid ID.");
  return {
    name: draft.name.trim(),
    description: draft.description.trim(),
    goal: draft.goal.trim(),
    category_id: draft.category ? Number(draft.category) : null,
    llm_profile_ids: profiles.map(Number),
    source_task_ids: sources,
    company_ids: companies.map(Number),
    timeout_seconds: Math.floor(minutes) * 60,
    plan_heartbeat_seconds: Math.max(10, Math.floor(heartbeat || 10)) * 60,
    seed_first_intent: draft.seed,
    coverage_enabled: draft.coverage,
    strict_isolation: draft.isolated,
    skip_initial_katana: !draft.initialKatana,
    initial_scout: draft.initialScout === "default" ? null : draft.initialScout === "on",
    intercept_rules: draft.rules
      .map((rule) => ({ ...rule, pattern: rule.pattern.trim() }))
      .filter((rule) => rule.pattern),
  };
}

export function uploadManifest(
  description: string,
  files: { name: string; path: string; abs?: string; size: number }[],
): string {
  if (!files.length) return description;
  const marker = "[Uploaded file (absolute path)]",
    existing = description.trimEnd();
  const lines = files.map((file) => `- ${file.abs || file.path} (${file.size} bytes)`);
  return (
    [existing, existing.includes(marker) ? "" : `${marker} The worker can open these paths with Read/Bash:`, ...lines]
      .filter(Boolean)
      .join("\n") + "\n"
  );
}

export type ItemOutcome = {
  id: string;
  ok: boolean;
  error?: string;
  queued?: boolean;
  archive_id?: number;
  warning?: string;
  detail?: string;
};
export function batchOutcomes(ids: readonly string[], results: ItemOutcome[]): ItemOutcome[] {
  const returned = new Map(results.map((item) => [String(item.id), item]));
  return ids.map(
    (id) =>
      returned.get(String(id)) || {
        id: String(id),
        ok: false,
        error: "The server did not return an outcome for this record.",
      },
  );
}
export const archiveInProgress = (state: string) =>
  ["archive_queued", "archiving", "restore_queued", "restoring", "delete_queued", "deleting"].includes(state);
export const archiveRestorable = (state: string) => ["ready", "restore_failed"].includes(state);
