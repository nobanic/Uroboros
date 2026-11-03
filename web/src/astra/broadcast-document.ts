import type { UroIconName } from "@/components/uroboros-icons";
import type { ExplorationNodePage, ExploreKind, TaskNode } from "@/lib/types";

export const broadcastKinds = ["goal", "intent", "fact", "finding", "hint", "digest"] as const;
export const broadcastSizes = [20, 50, 100] as const;
const appearances: Record<string, { label: string; icon: UroIconName; tone: string; fields: string[] }> = {
  begin: { label: "Origin", icon: "mission", tone: "slate", fields: ["summary", "description"] },
  task: { label: "Root task", icon: "mission", tone: "slate", fields: ["summary", "description"] },
  goal: { label: "Goal", icon: "tasks", tone: "green", fields: ["text"] },
  intent: { label: "Intent", icon: "route", tone: "blue", fields: ["summary"] },
  fact: { label: "Fact", icon: "records", tone: "amber", fields: ["summary"] },
  finding: { label: "Vulnerability", icon: "findings", tone: "red", fields: ["name", "summary"] },
  hint: { label: "Hint", icon: "info", tone: "violet", fields: ["text", "summary"] },
  digest: { label: "Digest", icon: "logs", tone: "teal", fields: ["body", "summary"] },
};
const states: Record<string, Record<string, [string, string]>> = {
  fact: { origin: ["Origin", "slate"], confirmed: ["Confirmed", "green"], dismissed: ["Dismissed", "slate"] },
  finding: { confirmed: ["Confirmed", "red"], dismissed: ["Excluded", "slate"] },
  hint: { active: ["Pending adoption", "violet"], consumed: ["Adopted", "slate"] },
  digest: { active: ["Active", "green"], superseded: ["Superseded", "slate"] },
};
const relations: Record<string, string> = {
  spawns: "Spawns",
  derived_from: "Intent chain",
  yields: "Yields",
  proves: "Proves",
  covers: "Covers",
};
const dayFormat = new Intl.DateTimeFormat("en-US", { month: "long", day: "numeric", weekday: "short" });
const clockFormat = new Intl.DateTimeFormat("en-US", {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

export function broadcastAppearance(kind: string) {
  return appearances[kind] || appearances.fact;
}
export function broadcastKind(node: TaskNode): ExploreKind {
  return node.type === "fact" && node.state === "origin" ? "begin" : node.type;
}
export function broadcastSummary(node: TaskNode): string {
  const raw = node.payload ?? "";
  if (!raw.trim()) return "";
  try {
    const parsed: unknown = JSON.parse(raw);
    if (parsed && typeof parsed === "object") {
      // Unknown runtime kinds keep the raw payload, just as the original board did.
      for (const field of appearances[broadcastKind(node)]?.fields ?? []) {
        const value = (parsed as Record<string, unknown>)[field];
        if (typeof value === "string" && value.trim()) return value;
      }
    }
  } catch {
    /* Raw, non-JSON observations remain readable. */
  }
  return raw;
}
export function broadcastPayload(raw?: string) {
  if (!raw?.trim()) return "(no payload)";
  try {
    return JSON.stringify(JSON.parse(raw), null, 2);
  } catch {
    return raw;
  }
}
export function broadcastState(node: TaskNode) {
  return states[node.type]?.[node.state];
}
export function broadcastRelation(relation: string) {
  return relations[relation] || relation;
}
export function broadcastRelative(timestamp: number, now: number) {
  if (!timestamp || !now) return "";
  const seconds = Math.max(0, (now - timestamp) / 1000);
  if (seconds < 60) return "just now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)} minutes ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)} hours ago`;
  return `${Math.floor(seconds / 86400)} days ago`;
}
export function broadcastTime(node: TaskNode) {
  const timestamp = Date.parse(node.ts);
  const valid = !Number.isNaN(timestamp);
  return {
    timestamp,
    day: valid ? dayFormat.format(timestamp) : "Unknown date",
    clock: valid ? clockFormat.format(timestamp) : "--:--:--",
    full: valid ? new Date(timestamp).toLocaleString("en-US") : node.ts,
  };
}
export type BroadcastNeighbor = { id: string; relation: string; key: string };
export function broadcastDocument(page: ExplorationNodePage) {
  const nodes: Record<string, TaskNode> = { ...page.refs };
  const groups: { day: string; rows: TaskNode[]; key: string }[] = [];
  const upstream = new Map<string, BroadcastNeighbor[]>();
  const downstream = new Map<string, BroadcastNeighbor[]>();
  for (const node of page.items) {
    nodes[node.id] = node; // Same-page nodes take precedence over off-page refs.
    const day = broadcastTime(node).day;
    const last = groups.at(-1);
    if (last?.day === day) last.rows.push(node);
    else groups.push({ day, rows: [node], key: "" });
  }
  // Count from the oldest end, so prepending a live day does not remount existing records.
  const dayOccurrences = new Map<string, number>();
  for (const group of [...groups].reverse()) {
    const ordinal = dayOccurrences.get(group.day) ?? 0;
    group.key = JSON.stringify([group.day, ordinal]);
    dayOccurrences.set(group.day, ordinal + 1);
  }
  const edgeOccurrences = new Map<string, number>();
  for (const edge of page.edges) {
    const identity = JSON.stringify([edge.src, edge.rel, edge.dst]);
    const ordinal = edgeOccurrences.get(identity) ?? 0;
    const key = `${identity}:${ordinal}`;
    edgeOccurrences.set(identity, ordinal + 1);
    const inbound = upstream.get(edge.dst) ?? [];
    const outbound = downstream.get(edge.src) ?? [];
    inbound.push({ id: edge.src, relation: edge.rel, key });
    outbound.push({ id: edge.dst, relation: edge.rel, key });
    upstream.set(edge.dst, inbound);
    downstream.set(edge.src, outbound);
  }
  return { nodes, groups, upstream, downstream, assets: page.assets, edgeCount: page.edges.length };
}

export type BroadcastSelection = {
  task: string;
  page: number;
  size: number;
  kinds: ExploreKind[];
  query: string;
  order: "asc" | "desc";
  live: boolean;
};
export function broadcastStream(selection: BroadcastSelection) {
  return JSON.stringify([selection.task, selection.kinds, selection.query, selection.order]);
}
export function broadcastWindow(selection: BroadcastSelection) {
  return JSON.stringify([broadcastStream(selection), selection.page, selection.size]);
}
export function broadcastAtLatest(selection: BroadcastSelection) {
  return selection.page === 1 && selection.order === "desc";
}
export function broadcastLastPage(total: number, size: number) {
  return Math.max(1, Math.ceil(total / size));
}
