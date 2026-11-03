import type { BatchControlItem, Finding, Task } from "../types";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";
import type { TaskLabels } from "./task-labels";

type TaskLinks = {
  companies: readonly { id: number }[];
  findings: Finding[];
  records: { task_id: string }[];
  attachCompanies: (taskID: string, companyIDs: number[]) => void;
  detach: (taskID: string) => void;
  blocker: (taskID: string) => string | undefined;
  resolution: (task: Task, role: "mainagent" | "planner" | "worker") => unknown;
};
export function taskIDs(value: unknown): string[] {
  if (!Array.isArray(value)) return [];
  const ids = [...new Set(value.map((item) => String(item)).filter(Boolean))];
  if (ids.length > 100) throw new Error("uroboros.lab demonstration record. Context: 100, ID.");
  return ids;
}
const missingTask = () => new Error("uroboros.lab demonstration record.");

/** Live task identity, activation and mutations share one owner; evidence stores stay explicit ports. */
export class TaskRegistry {
  private activeID: string;
  constructor(
    readonly rows: Task[],
    active: string,
    private labels: TaskLabels,
    private links: TaskLinks,
  ) {
    this.activeID = active;
  }
  get active() {
    return this.activeID;
  }
  private select(id: string) {
    this.activeID = id;
    for (const task of this.rows) task.active = task.id === id;
  }
  afterRemoval(id: string) {
    if (this.activeID === id) this.select(this.rows[0]?.id ?? "");
  }
  removeLiveRow(id: string) {
    const index = this.rows.findIndex((task) => task.id === id);
    if (index >= 0) this.rows.splice(index, 1);
  }
  restoreLiveRow(task: Task) {
    if (!this.rows.some((current) => current.id === task.id)) this.rows.push(task);
  }
  sort() {
    this.rows.sort((left, right) => {
      const pinned = Number(Boolean(right.pinned_at)) - Number(Boolean(left.pinned_at));
      if (pinned) return pinned;
      if (left.pinned_at !== right.pinned_at) return (right.pinned_at ?? "").localeCompare(left.pinned_at ?? "");
      return right.id.localeCompare(left.id, undefined, { numeric: true });
    });
  }
  private required(id: string) {
    const task = this.rows.find((item) => item.id === id);
    if (!task) throw missingTask();
    return task;
  }
  project(task: Task) {
    const result = structuredClone(task),
      blocker = this.links.blocker(task.id);
    if (blocker) result.archive_blocked_by_task_id = blocker;
    else delete result.archive_blocked_by_task_id;
    return result;
  }
  private nextID() {
    let suffix = 1;
    while (this.rows.some((task) => task.id === `t-new-${suffix}`)) suffix++;
    return `t-new-${suffix}`;
  }
  private create(b: Record<string, unknown>) {
    const id = this.nextID(),
      now = new Date();
    // Preserve iterable coercions and their error order, including malformed bodies.
    const profileIDs = [...((b.llm_profile_ids as number[] | undefined) ?? [])];
    const sourceTaskIDs = [...((b.source_task_ids as string[] | undefined) ?? [])];
    const companyIDs = [...new Set((b.company_ids as number[] | undefined) ?? [])];
    if (companyIDs.some((companyID) => !this.links.companies.some((company) => company.id === companyID)))
      throw missingTask();
    const category = this.labels.category(b.category_id);
    const created: Task = {
      id,
      name: String(b.name ?? ""),
      category_id: category?.id,
      category_name: category?.name,
      description: String(
        b.description ?? "Security assessment record for the uroboros.lab demonstration environment.",
      ),
      goal: String(b.goal ?? ""),
      status: "created",
      created_at: now.toISOString(),
      created_unix: Math.floor(now.getTime() / 1000),
      paused: false,
      active: true,
      in_flight: 0,
      findings: { critical: 0, high: 0, medium: 0, low: 0 },
      stalled: false,
      goals_total: 0,
      goals_met: 0,
      engine_mode: "idle",
      tokens: { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 },
      llm_profile_id: profileIDs[0],
      llm_profile_ids: profileIDs,
      active_llm_profile_id: profileIDs[0],
      llm_failover_state: profileIDs.length ? "ready" : "default",
      source_task_ids: sourceTaskIDs,
      company_ids: companyIDs,
      skip_initial_katana: b.skip_initial_katana === true,
      initial_scout: typeof b.initial_scout === "boolean" ? b.initial_scout : null,
    };
    this.links.attachCompanies(id, companyIDs);
    this.rows.unshift(created);
    this.select(id);
    return created;
  }
  private patch(id: string, b: Record<string, unknown>) {
    const task = this.required(id);
    if (typeof b.name === "string") task.name = b.name.trim();
    if (typeof b.pinned === "boolean") {
      task.pinned = b.pinned;
      task.pinned_at = b.pinned ? (task.pinned_at ?? new Date().toISOString()) : null;
    }
    this.sort();
    return structuredClone(task);
  }
  private remove(id: string, b: Record<string, unknown>) {
    this.removeLiveRow(id);
    this.links.detach(id);
    // The original null-body failure happens after removing task/asset links.
    let findingsDeleted = 0,
      recordsDeleted = 0;
    if (b.delete_findings) {
      for (let index = this.links.findings.length - 1; index >= 0; index--) {
        if (this.links.findings[index].task_id !== id) continue;
        this.links.findings.splice(index, 1);
        findingsDeleted++;
      }
    } else
      for (const finding of this.links.findings) {
        if (finding.task_id === id) {
          finding.task_id = undefined;
          finding.task_description = "";
        }
      }
    if (b.delete_llm_records) {
      for (let index = this.links.records.length - 1; index >= 0; index--) {
        if (this.links.records[index].task_id !== id) continue;
        this.links.records.splice(index, 1);
        recordsDeleted++;
      }
    }
    this.afterRemoval(id);
    return {
      deleted: id,
      assets_deleted: b.delete_assets ? 1 : 0,
      assets_detached: 0,
      traffic_deleted: b.delete_traffic ? 1 : 0,
      files_deleted: Boolean(b.delete_files),
      findings_deleted: findingsDeleted,
      llm_records_deleted: recordsDeleted,
    };
  }
  private profiles(id: string, b: Record<string, unknown>) {
    const ids = [...((b.llm_profile_ids as number[] | undefined) ?? [])];
    const activeID = typeof b.active_llm_profile_id === "number" ? b.active_llm_profile_id : ids[0];
    const task = this.rows.find((item) => item.id === id),
      state = ids.length ? "ready" : "default";
    if (task)
      Object.assign(task, {
        llm_profile_ids: ids,
        llm_profile_id: ids[0],
        active_llm_profile_id: activeID,
        llm_failover_state: state,
        llm_failover_reason: undefined,
      });
    return {
      id,
      llm_profile_ids: ids,
      active_llm_profile_id: activeID,
      llm_failover_state: state,
      reopened_intents: 0,
    };
  }
  control(id: string, action: "pause" | "resume"): BatchControlItem {
    const task = this.rows.find((item) => item.id === id);
    if (!task) return { id, ok: false, error: "uroboros.lab demonstration record." };
    const paused = Boolean(task.paused || task.status === "paused");
    if (["done", "failed", "timeout"].includes(task.status) || (action === "pause" ? paused : !paused))
      return { id, ok: false, status: task.status, error: "uroboros.lab demonstration record." };
    Object.assign(task, {
      paused: action === "pause",
      queued: false,
      status: action === "pause" ? "paused" : "running",
      engine_mode: action === "pause" ? "paused" : "exploring",
    });
    return { id, ok: true, status: task.status, queued: false };
  }
  respond({ method, path, segments, body: b }: DemoRequest): unknown {
    if (path === "/tasks") {
      if (method === "GET") {
        this.sort();
        return { tasks: this.rows.map((task) => this.project(task)), active: this.active };
      }
      if (method === "POST") return this.create(b);
    }
    if (segments[0] === "tasks") {
      const id = segments[1];
      if (segments.length === 2) {
        if (method === "GET") return this.project(this.required(id));
        if (method === "PATCH") return this.patch(id, b);
        if (method === "DELETE") return this.remove(id, b);
      }
      if (segments[2] === "llm") {
        if (method === "PUT") return this.profiles(id, b);
        if (segments[3] === "resolution" && method === "GET") {
          const task = this.required(id);
          return {
            mainagent: this.links.resolution(task, "mainagent"),
            planner: this.links.resolution(task, "planner"),
            worker: this.links.resolution(task, "worker"),
          };
        }
      }
      if (segments.length === 3 && segments[2] === "control" && method === "POST") {
        const result = this.control(id, b.action === "resume" ? "resume" : "pause");
        if (!result.ok) throw new Error(result.error ?? "uroboros.lab demonstration record.");
        const task = this.rows.find((item) => item.id === id);
        return { id, paused: Boolean(task?.paused), queued: Boolean(task?.queued), status: task?.status ?? "" };
      }
      if (segments[2] === "chat" && segments[3] === "status") return { running: false };
      if (segments[2] === "chat" && segments[3] === "stop") return { status: "stopped" };
    }
    if (path === "/tasks/control/batch" && method === "POST") {
      const action = b.action === "resume" ? "resume" : "pause";
      return { items: taskIDs(b.task_ids).map((id) => this.control(id, action)) };
    }
    if (path === "/active") {
      const id = String(b.id ?? this.active);
      if (this.rows.some((task) => task.id === id)) this.select(id);
      return { active: this.active };
    }
    return UNHANDLED;
  }
}
