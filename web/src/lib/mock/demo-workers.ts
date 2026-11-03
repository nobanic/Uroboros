import type { Activity, TaskNode } from "../types";
import type * as SeedData from "./data";
import { activityRecord } from "./demo-activity";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";
import { DemoRows } from "./demo-records";

type ControlResult = { ok: boolean; state?: string; error?: string; activitySeq?: number; requestId?: string };
type AcceptedMessage = { intentId: string; message: string; state: "open" | "running"; activitySeq: number };
const workerError = "uroboros.lab demonstration record. Context: Worker.";
const missing = "uroboros.lab demonstration record.";
const numericIntent = (id: string) => Number(id.replace(/\D/g, "")) || 0;

/** Local worker edits and the public graph intentionally retain separate row identities. */
export class DemoWorkers {
  private readonly intents: DemoRows<TaskNode, string>;
  private readonly activity: DemoRows<Activity, number>;
  private readonly messages = new Map<string, AcceptedMessage>();
  private nextSequence: number;

  constructor(
    intents: TaskNode[],
    activity: Activity[],
    private readonly data: typeof SeedData,
  ) {
    this.intents = new DemoRows(intents, (intent) => intent.id);
    this.activity = new DemoRows(activity, (row) => row.seq);
    this.nextSequence = this.activity.nextNumber();
  }

  private editable(intent: TaskNode, states: readonly string[]): boolean {
    return !intent.inherited && states.includes(intent.state);
  }

  private transition(id: string, action: "pause" | "resume"): ControlResult {
    const intent = this.intents.get(id);
    if (!intent) return { ok: false, error: missing };
    if (!this.editable(intent, [action === "pause" ? "running" : "paused"]))
      return { ok: false, state: intent.state, error: workerError };
    intent.state = action === "pause" ? "paused" : "open";
    return { ok: true, state: intent.state };
  }

  private cancel(id: string, body: Record<string, unknown>) {
    const intent = this.intents.get(id);
    if (!intent || !this.editable(intent, ["running", "paused", "open"])) throw new Error(workerError);
    const number = numericIntent(id);
    if (body.mode === "hard") {
      this.intents.remove(id);
      return {
        id: number,
        state: "",
        deleted: {
          intents: 1,
          facts: 1,
          findings: 1,
          activities: this.activity.rows.filter((row) => row.intent_id === id).length,
        },
      };
    }
    intent.state = "deleted";
    intent.delete_reason = String(body.reason ?? "");
    return { id: number, state: "deleted" };
  }

  private message(id: string, message: string, requestId: string): ControlResult {
    const text = message.trim(),
      key = requestId.trim();
    if (!key) return { ok: false, error: "uroboros.lab demonstration record. Context: request_id." };
    const accepted = this.messages.get(key);
    if (accepted) {
      if (accepted.intentId !== id || accepted.message !== text)
        return { ok: false, error: "uroboros.lab demonstration record. Context: request_id, Worker." };
      return { ok: true, state: accepted.state, activitySeq: accepted.activitySeq, requestId: key };
    }
    const intent = this.intents.get(id);
    if (!intent) return { ok: false, error: missing };
    if (!this.editable(intent, ["paused"])) return { ok: false, state: intent.state, error: workerError };
    if (!text) return { ok: false, state: intent.state, error: missing };
    if (Array.from(text).length > 4000)
      return { ok: false, state: intent.state, error: "uroboros.lab demonstration record. Context: 4000." };
    intent.state = "running";
    const record: AcceptedMessage = { intentId: id, message: text, state: "running", activitySeq: this.nextSequence++ };
    this.messages.set(key, record);
    this.activity.rows.push(
      activityRecord(
        record.activitySeq,
        "user",
        new Date().toISOString(),
        "user",
        { summary: text, detail: text },
        { intent_id: id },
      ),
    );
    return { ok: true, state: record.state, activitySeq: record.activitySeq, requestId: key };
  }

  private graph(query: URLSearchParams) {
    const all = this.data.explorationGraph.nodes;
    const kinds = new Set((query.get("kind") ?? "").split(",").filter(Boolean));
    const states = new Set((query.get("state") ?? "").split(",").filter(Boolean));
    const needle = (query.get("q") ?? "").trim().toLowerCase(),
      ascending = query.get("order") === "asc";
    const page = Math.max(1, Number(query.get("page") ?? 1)),
      size = Math.min(200, Math.max(1, Number(query.get("size") ?? 20)));
    const matched = all
      .filter((node) => (!kinds.size || kinds.has(node.type)) && (!states.size || states.has(node.state)))
      .filter(
        (node) =>
          !needle || `${node.payload ?? ""} ${node.origin} ${node.id}`.toLowerCase().includes(needle.replace(/^#/, "")),
      )
      .sort((left, right) => {
        const difference =
          Date.parse(left.ts) - Date.parse(right.ts) ||
          all.findIndex((node) => node.id === left.id) - all.findIndex((node) => node.id === right.id);
        return ascending ? difference : -difference;
      });
    const items = matched.slice((page - 1) * size, page * size),
      onPage = new Set(items.map((node) => node.id));
    const edges = this.data.explorationGraph.edges.filter((edge) => onPage.has(edge.src) || onPage.has(edge.dst));
    const refs: Record<string, TaskNode> = {},
      rows = new DemoRows(all, (node) => node.id);
    for (const edge of edges)
      for (const id of [edge.src, edge.dst]) {
        if (onPage.has(id) || refs[id]) continue;
        const node = rows.get(id);
        if (node) refs[id] = node;
      }
    const assets: Record<string, ReturnType<typeof SeedData.nodeAssetsFor>> = {};
    for (const id of new Set([...onPage, ...Object.keys(refs)])) {
      const anchored = this.data.nodeAssetsFor(id);
      if (anchored.length) assets[id] = anchored;
    }
    return { items, total: matched.length, page, size, edges, refs, assets };
  }

  respond({ method, path, segments, query, body }: DemoRequest): unknown {
    if (segments[0] === "tasks" && segments[2] === "intents" && segments[4] === "control" && method === "POST") {
      const id = segments[3];
      if (body.action === "cancel") return this.cancel(id, body);
      const result = this.transition(id, body.action === "resume" ? "resume" : "pause");
      if (!result.ok) throw new Error(result.error ?? workerError);
      return { id: numericIntent(id), state: result.state };
    }
    if (segments[0] === "tasks" && segments[2] === "intents" && segments[4] === "messages" && method === "POST") {
      const id = segments[3],
        result = this.message(id, String(body.message ?? ""), String(body.request_id ?? ""));
      if (!result.ok) throw new Error(result.error ?? workerError);
      return {
        id: numericIntent(id),
        state: result.state ?? "running",
        accepted: true,
        request_id: result.requestId ?? "",
      };
    }
    if (path === "/exploration/intents") {
      if (!query.has("page")) return this.intents.rows;
      const before = Number(query.get("before") ?? 0),
        limit = Math.max(1, Number(query.get("limit") ?? 300));
      const rows =
        before > 0
          ? this.intents.rows.filter((intent) => Number(intent.id.replace(/\D/g, "") || intent.id) < before)
          : this.intents.rows;
      return { items: rows.slice(0, limit), has_more: rows.length > limit };
    }
    if (path === "/exploration/nodes") return this.graph(query);
    if (path === "/exploration/activity" && segments.length === 2) {
      const since = Number(query.get("since") ?? 0),
        limit = Math.max(1, Number(query.get("limit") ?? 300));
      const items = this.activity.rows.filter((row) => row.seq > since).slice(0, limit);
      return { items, cursor: items.length ? items[items.length - 1].seq : since };
    }
    if (segments[0] === "exploration" && segments[1] === "activity" && segments.length === 3) {
      const activity = this.activity.get(Number(segments[2]));
      return { detail: activity?.detail ?? activity?.summary ?? "" };
    }
    return UNHANDLED;
  }
}
