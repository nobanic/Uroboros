import type {
  Activity,
  InterceptApprovalRow,
  InterceptExecution,
  Session,
  SessionTokenUsage,
  TaskNode,
  TokenTotal,
} from "@/lib/types";
import type { MainSegments, SessionPages } from "./session-journal";

export type SessionEntry = Session & { key: string; record?: TaskNode; raw?: unknown; reason?: string };
export const sessionRoles = ["mainagent", "planner", "system", "worker"] as const;
export const sessionRoleNames = {
  mainagent: "Main Agent",
  planner: "Planner",
  system: "System audit",
  worker: "Workers",
};
export function mainEntry(seg: number): SessionEntry {
  return {
    key: `main:${seg}`,
    id: `s-main-${seg}`,
    role: "mainagent",
    title: `Main Agent · Session #${seg + 1}`,
    seg,
    status: "running",
    live: false,
    last_activity: "",
  };
}
export function fixedEntry(key: "plan" | "system"): SessionEntry {
  return {
    key,
    id: key === "plan" ? "s-planner" : "s-system",
    role: key === "plan" ? "planner" : "system",
    title: key === "plan" ? "Planner · Situation assessment" : "System events · LLM failover",
    status: key === "plan" ? "running" : "done",
    live: false,
    last_activity: "",
  };
}
export function workerEntry(node: TaskNode): SessionEntry {
  let raw: unknown = node.payload,
    title = `Intent ${node.id}`;
  try {
    const parsed = JSON.parse(node.payload || "null");
    raw = parsed;
    if (parsed?.summary) title = String(parsed.summary);
  } catch {
    title = node.payload?.trim() || title;
  }
  const status: Session["status"] = ["done", "blocked", "exhausted", "stopped", "paused", "deleted"].includes(
    node.state,
  )
    ? (node.state as Session["status"])
    : node.state === "open"
      ? "pending"
      : "running";
  return {
    key: `intent:${node.id}`,
    id: node.id,
    intent_id: node.id,
    role: "worker",
    title,
    status,
    live: !node.inherited && status === "running",
    last_activity: node.ts,
    inherited: node.inherited,
    source_task_id: node.source_task_id,
    record: node,
    raw: { ...node, payload: raw },
    reason: node.delete_reason,
  };
}
export function locatedEntry(source?: InterceptExecution): SessionEntry | undefined {
  if (!source) return;
  if (/^main:\d+$/.test(source.session)) return mainEntry(Number(source.session.slice(5)));
  if (source.session === "plan") return fixedEntry("plan");
  if (source.session.startsWith("intent:")) {
    const id = source.session.slice(7);
    return {
      key: source.session,
      id,
      intent_id: id,
      role: "worker",
      title: `Worker #${id}`,
      status: "done",
      live: false,
      last_activity: source.items[0]?.ts || "",
    };
  }
}
export function settled(items: Activity[]) {
  const last = items.at(-1);
  return !!last && (last.kind === "result" || (last.kind === "text" && !!last.is_error));
}
export function recentSession(pages: SessionPages, key: string, now = Date.now()) {
  const time = Date.parse(pages[key]?.lastTs || "");
  return time > 0 && now - time < 5000;
}
export function sessionEntries(
  segments: MainSegments,
  workers: TaskNode[],
  pages: SessionPages,
  sending: boolean,
  running: boolean | null,
  located?: SessionEntry,
) {
  let liveSegment: number | null = null;
  if (sending || running) {
    liveSegment = segments.current;
    let newest = -1;
    for (const row of segments.sessions) {
      const stamp = Date.parse(pages[`main:${row.seq}`]?.lastTs || "");
      if (stamp > newest) {
        newest = stamp;
        liveSegment = row.seq;
      }
    }
    if (settled(pages[`main:${liveSegment}`]?.items || [])) liveSegment = null;
  }
  const entries = (segments.sessions.length ? segments.sessions : [{ seq: 0, created_at: "" }]).map((row) => ({
    ...mainEntry(row.seq),
    last_activity: row.created_at,
    live: row.seq === liveSegment,
  }));
  const all: SessionEntry[] = [
    ...entries,
    { ...fixedEntry("plan"), live: recentSession(pages, "plan") },
    fixedEntry("system"),
    ...workers.map(workerEntry),
  ];
  if (located && !all.some((entry) => entry.key === located.key)) all.push(located);
  return all;
}
export function sessionBusy(entry: SessionEntry, pages: SessionPages, sending: boolean, running: boolean | null) {
  return (
    entry.role === "mainagent" &&
    (sending || (!settled(pages[entry.key]?.items || []) && (running ?? recentSession(pages, entry.key))))
  );
}
export function sessionReview(entry: SessionEntry, approvals: InterceptApprovalRow[]) {
  if (entry.inherited) return false;
  return approvals.some(
    (row) =>
      row.status === "pending" &&
      (entry.role === "mainagent"
        ? row.agent_name === "mainagent"
        : entry.role === "planner"
          ? row.agent_name === "planner"
          : entry.role === "worker" && row.agent_name.match(/\u00b7\s*#(\d+)$/)?.[1] === entry.intent_id),
  );
}
export function sessionTokens(saved: SessionTokenUsage | undefined, items: Activity[]): TokenTotal {
  const total = {
    input_tokens: saved?.input_tokens || 0,
    output_tokens: saved?.output_tokens || 0,
    cache_read_tokens: saved?.cache_read_tokens || 0,
    cache_write_tokens: saved?.cache_write_tokens || 0,
  };
  for (let index = items.length - 1; index >= 0; index--) {
    if (items[index].kind === "result") break;
    if (items[index].kind !== "usage") continue;
    for (const key of Object.keys(total) as (keyof TokenTotal)[]) total[key] += items[index][key] || 0;
    break;
  }
  return total;
}
export function sessionActivity(entry: SessionEntry, items: Activity[]): Activity[] {
  return entry.role !== "worker"
    ? items
    : [
        {
          seq: -1,
          worker: items[0]?.worker || entry.id,
          kind: "intent",
          summary: entry.title,
          ts: entry.last_activity,
          inherited: entry.inherited,
          source_task_id: entry.source_task_id,
        },
        ...items,
      ];
}
export function sessionMatches(entry: SessionEntry, query: string) {
  return [entry.title, entry.id, entry.role, entry.status, entry.source_task_id, entry.reason]
    .join(" ")
    .toLocaleLowerCase()
    .includes(query.trim().toLocaleLowerCase());
}
export function sessionFilter(entry: SessionEntry, filter: string, approvals: InterceptApprovalRow[]) {
  if (filter === "all") return true;
  if (filter === "review") return sessionReview(entry, approvals);
  if (filter === "inherited") return !!entry.inherited;
  return entry.role === "worker" ? entry.status === filter : filter === "running" && entry.live;
}
export function compactSessionTokens(value: number) {
  return value >= 1e6
    ? `${(value / 1e6).toFixed(value >= 1e7 ? 0 : 1)}M`
    : value >= 1000
      ? `${(value / 1000).toFixed(value >= 10000 ? 0 : 1)}k`
      : String(value);
}
export function sessionDuration(items: Activity[], live: boolean) {
  let earliest = Infinity,
    latest = 0;
  for (const item of items) {
    const time = Date.parse(item.ts);
    if (Number.isFinite(time)) {
      earliest = Math.min(earliest, time);
      latest = Math.max(latest, time);
    }
  }
  if (!Number.isFinite(earliest) || latest === 0) return "";
  const seconds = Math.max(0, Math.floor(((live ? Date.now() : latest) - earliest) / 1000));
  return seconds < 60
    ? `${seconds}s`
    : seconds < 3600
      ? `${Math.floor(seconds / 60)}m${String(seconds % 60).padStart(2, "0")}s`
      : `${Math.floor(seconds / 3600)}h${String(Math.floor(seconds / 60) % 60).padStart(2, "0")}m`;
}
export const workerMessageLimit = 4000;
export function workerMessageLength(value: string) {
  return Array.from(value.trim()).length;
}
