import type { ArchiveBatchItem, Asset, Finding, Task, TaskArchive } from "../types";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";
import { type TaskRegistry, taskIDs } from "./task-registry";

type AssetSource = Pick<Asset, "task_source" | "task_source_summary" | "task_source_node_id">;
type ArchiveLinks = { assetIDs: number[]; assetSources: Array<[number, AssetSource]> };
type ArchiveSnapshot = ArchiveLinks & { task: Task; numericTaskID: number };
type ColdTask = TaskArchive & { snapshot: ArchiveSnapshot };
type ArchivePorts = {
  taskAssetID: (id: string) => number | undefined;
  capture: (id: string, numericID: number) => ArchiveLinks;
  retireAssets: (id: string, numericID: number) => void;
  restoreAssets: (snapshot: ArchiveSnapshot) => void;
  findings: Pick<Finding, "task_id" | "severity">[];
  records: { task_id: string }[];
};
type Queued = "archive_queued" | "restore_queued" | "delete_queued";
const phase = "uroboros.lab demonstration record.";
const transitions: Record<
  Queued,
  { working: TaskArchive["state"]; progress: number; completeAt: number; touchWorking: boolean; guardComplete: boolean }
> = {
  archive_queued: { working: "archiving", progress: 55, completeAt: 500, touchWorking: true, guardComplete: true },
  restore_queued: { working: "restoring", progress: 60, completeAt: 500, touchWorking: true, guardComplete: true },
  delete_queued: { working: "deleting", progress: 70, completeAt: 450, touchWorking: false, guardComplete: false },
};
const archiveError = (context = "") => new Error(context ? `${phase} Context: ${context}.` : phase);

/** Cold task records own snapshots and timed transitions; linked live stores remain explicit ports. */
export class TaskArchives {
  private rows: ColdTask[] = [];
  private sequence = 1;
  constructor(
    private registry: TaskRegistry,
    private ports: ArchivePorts,
  ) {}

  private identity(id: string) {
    const numeric = Number(id);
    return Number.isSafeInteger(numeric) && numeric > 0 ? numeric : (this.ports.taskAssetID(id) ?? 0);
  }
  private dependent(id: string) {
    return this.registry.rows.find(
      (candidate) =>
        candidate.id !== id &&
        (candidate.source_task_ids ?? []).map(String).includes(id) &&
        !this.rows.some(
          (archive) =>
            archive.snapshot.task.id === candidate.id &&
            (archive.state === "archive_queued" || archive.state === "archiving"),
        ),
    );
  }
  blocker(id: string) {
    return this.dependent(id)?.id;
  }
  private present(archive: ColdTask): TaskArchive {
    const { snapshot: _snapshot, ...publicValue } = archive;
    return structuredClone(publicValue);
  }
  private required(id: number) {
    const archive = this.rows.find((item) => item.id === id);
    if (!archive) throw Object.assign(archiveError(), { status: 404 });
    return archive;
  }
  private schedule(archive: ColdTask, queued: Queued, complete: () => void) {
    const plan = transitions[queued];
    setTimeout(() => {
      if (archive.state !== queued) return;
      Object.assign(archive, { state: plan.working, phase, progress: plan.progress });
      if (plan.touchWorking) archive.updated_at = new Date().toISOString();
    }, 100);
    setTimeout(() => {
      if (plan.guardComplete && archive.state !== queued && archive.state !== plan.working) return;
      complete();
    }, plan.completeAt);
  }
  private begin(archive: ColdTask, state: Queued) {
    Object.assign(archive, { state, phase, progress: 0, error: undefined, updated_at: new Date().toISOString() });
  }
  private create(id: string) {
    const task = this.registry.rows.find((item) => item.id === id);
    if (!task || (!["paused", "done", "failed", "timeout"].includes(task.status) && !task.paused)) throw archiveError();
    const numericTaskID = this.identity(id);
    if (this.rows.some((item) => item.task_id === numericTaskID)) throw archiveError();
    if (this.dependent(id)) throw archiveError("dependent.id");
    const links = this.ports.capture(id, numericTaskID);
    const findings = this.ports.findings.filter((finding) => finding.task_id === id);
    const records = this.ports.records.filter((record) => record.task_id === id);
    const tokens = task.tokens ?? { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 };
    const now = new Date().toISOString();
    const archive: ColdTask = {
      id: this.sequence++,
      task_id: numericTaskID,
      state: "archive_queued",
      phase,
      progress: 0,
      format_version: 1,
      original_size: 0,
      compressed_size: 0,
      task_name: task.name ?? "",
      task_description: task.description,
      task_goal: task.goal,
      original_status: task.status,
      category_id: task.category_id,
      category_name: task.category_name,
      source_task_ids: (task.source_task_ids ?? []).map((source) => this.identity(source)).filter((value) => value > 0),
      remaining_timeout_seconds: 0,
      data_counts: { assets: links.assetIDs.length, findings: findings.length, llm_records: records.length },
      aggregate_stats: {
        tokens: {
          calls: records.length,
          input_tokens: tokens.input_tokens,
          output_tokens: tokens.output_tokens,
          cache_read_tokens: tokens.cache_read_tokens,
          cache_write_tokens: tokens.cache_write_tokens,
        },
        findings: findings.reduce<Record<string, number>>((counts, finding) => {
          counts[finding.severity] = (counts[finding.severity] ?? 0) + 1;
          return counts;
        }, {}),
      },
      requested_at: now,
      created_at: now,
      updated_at: now,
      snapshot: {
        task: structuredClone(task),
        numericTaskID,
        assetIDs: links.assetIDs,
        assetSources: links.assetSources,
      },
    };
    this.rows.unshift(archive);
    this.schedule(archive, "archive_queued", () => {
      const finishedAt = new Date().toISOString();
      Object.assign(archive, { state: "ready", phase, progress: 100, archived_at: finishedAt, updated_at: finishedAt });
      archive.original_size = Math.max(4096, JSON.stringify(archive.snapshot).length * 4);
      archive.compressed_size = Math.max(1024, Math.round(archive.original_size * 0.32));
      this.registry.removeLiveRow(id);
      this.ports.retireAssets(id, numericTaskID);
      this.registry.afterRemoval(id);
    });
    return archive;
  }
  private restore(archive: ColdTask) {
    if (archive.state !== "ready" && archive.state !== "restore_failed") throw archiveError();
    this.begin(archive, "restore_queued");
    this.schedule(archive, "restore_queued", () => {
      const restored = structuredClone(archive.snapshot.task);
      restored.active = false;
      restored.queued = false;
      this.registry.restoreLiveRow(restored);
      this.ports.restoreAssets(archive.snapshot);
      this.remove(archive);
      this.registry.sort();
    });
  }
  private remove(archive: ColdTask) {
    const index = this.rows.indexOf(archive);
    if (index >= 0) this.rows.splice(index, 1);
  }
  private delete(archive: ColdTask) {
    if (archive.state !== "ready" && archive.state !== "delete_failed") throw archiveError();
    if (
      this.rows.some((candidate) => candidate.id !== archive.id && candidate.source_task_ids.includes(archive.task_id))
    )
      throw archiveError("dependent.task_id");
    this.begin(archive, "delete_queued");
    this.schedule(archive, "delete_queued", () => this.remove(archive));
  }
  private list(query: URLSearchParams) {
    const page = Math.max(1, Number(query.get("page")) || 1),
      size = Math.min(100, Math.max(1, Number(query.get("size")) || 20));
    const text = (query.get("q") ?? "").trim().toLowerCase(),
      state = (query.get("state") ?? "").trim();
    const filtered = this.rows.filter(
      (archive) =>
        (!state || archive.state === state) &&
        (!text ||
          [archive.task_id, archive.task_name, archive.task_description].some((value) =>
            String(value ?? "")
              .toLowerCase()
              .includes(text),
          )),
    );
    const offset = (page - 1) * size;
    return {
      items: filtered.slice(offset, offset + size).map((archive) => this.present(archive)),
      total: filtered.length,
      page,
      size,
    };
  }
  private operateBatch(value: unknown, mode: "restore" | "delete") {
    return {
      items: taskIDs(value)
        .map(Number)
        .map<ArchiveBatchItem>((id) => {
          const archive = this.rows.find((item) => item.id === id);
          if (!archive) return { id: String(id), archive_id: id, ok: false, queued: false, error: phase };
          try {
            this[mode](archive);
            return { id: String(id), archive_id: id, ok: true, queued: true };
          } catch (error) {
            return { id: String(id), archive_id: id, ok: false, queued: false, error: (error as Error).message };
          }
        }),
    };
  }
  private archiveBatch(value: unknown) {
    const requested = taskIDs(value),
      selected = new Set(requested),
      ordered: string[] = [],
      visiting = new Set<string>(),
      visited = new Set<string>();
    const visit = (id: string) => {
      if (visited.has(id) || visiting.has(id)) return;
      visiting.add(id);
      for (const candidate of this.registry.rows) {
        if (!selected.has(candidate.id) || !(candidate.source_task_ids ?? []).map(String).includes(id)) continue;
        visit(candidate.id);
      }
      visiting.delete(id);
      visited.add(id);
      ordered.push(id);
    };
    for (const id of requested) visit(id);
    const results = new Map<string, ArchiveBatchItem>();
    for (const id of ordered) {
      try {
        results.set(id, { id, archive_id: this.create(id).id, ok: true, queued: true });
      } catch (error) {
        results.set(id, { id, ok: false, queued: false, error: (error as Error).message });
      }
    }
    return { items: requested.map((id) => results.get(id) ?? { id, ok: false, queued: false, error: phase }) };
  }
  respond({ method, path, segments, query, body: b }: DemoRequest): unknown {
    if (path === "/task-archives" && method === "GET") return this.list(query);
    if (segments[0] === "task-archives") {
      if (segments.length === 2) {
        if (method === "GET") return this.present(this.required(Number(segments[1])));
        if (method === "DELETE") {
          const archive = this.required(Number(segments[1]));
          this.delete(archive);
          return this.present(archive);
        }
      }
      if (segments[2] === "restore" && segments.length === 3 && method === "POST") {
        const archive = this.required(Number(segments[1]));
        this.restore(archive);
        return this.present(archive);
      }
    }
    if (path === "/task-archives/restore/batch" && method === "POST")
      return this.operateBatch(b.archive_ids, "restore");
    if (path === "/task-archives/delete/batch" && method === "POST") return this.operateBatch(b.archive_ids, "delete");
    if (path === "/tasks/archive/batch" && method === "POST") return this.archiveBatch(b.task_ids);
    if (segments[0] === "tasks" && segments[2] === "archive" && segments.length === 3 && method === "POST")
      return this.present(this.create(segments[1]));
    return UNHANDLED;
  }
}
