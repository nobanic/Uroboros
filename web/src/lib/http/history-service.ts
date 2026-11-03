import type {
  Activity,
  ConvTokenSummary,
  DailyTokenBucket,
  Edge,
  ExplorationNodePage,
  ExplorationNodeQuery,
  Finding,
  SessionTokenUsage,
  TaskNode,
  TokenTotal,
  TokenUsage,
} from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { type QueryEntry, Resource, slice } from "./resources";

/** Session cursors and live tails are distinct: negative/zero before values never become older-history filters. */
export function historyService(wire: ApplicationWire) {
  const root = new Resource(wire, "/exploration");
  const scoped = (kind: string, task?: string) =>
    task ? root.at(kind).query([["task", task]], "component") : root.at(kind);
  const taskField = (task?: string): QueryEntry => ["task", task ? task : undefined];
  return {
    frontier: (task?: string) => scoped("frontier", task).list<TaskNode>(),
    findings: (task?: string) => scoped("findings", task).list<Finding>(),
    intents: (task?: string) => scoped("intents", task).list<TaskNode>(),
    explorationGraph: (task?: string) => scoped("graph", task).read<{ nodes: TaskNode[]; edges: Edge[] }>(),
    tokenStats: (task?: string) =>
      scoped("tokens", task)
        .read<{ workers: TokenUsage[]; sessions: SessionTokenUsage[]; total: TokenTotal }>()
        .then((value) => ({ workers: slice(value.workers), sessions: slice(value.sessions), total: value.total })),
    tokenDaily: (days = 30) =>
      new Resource(wire, "/tokens/daily").query([["days", days]], "component").list<DailyTokenBucket>(),
    conversationTokens: () =>
      new Resource(wire, "/tokens/conversations").list<ConvTokenSummary>().catch(() => [] as ConvTokenSummary[]),
    explorationNodes: (task: string, query: ExplorationNodeQuery = {}) =>
      root
        .at("nodes")
        .query([
          taskField(task),
          ["page", query.page ?? 1],
          ["size", query.size ?? 20],
          ["kind", query.kinds?.length ? query.kinds.join(",") : undefined],
          ["state", query.states?.length ? query.states.join(",") : undefined],
          ["q", query.q?.trim() ? query.q.trim() : undefined],
          ["order", query.order === "asc" ? "asc" : undefined],
        ])
        .read<ExplorationNodePage>()
        .then((value) => ({
          items: slice(value.items),
          total: value.total ?? 0,
          page: value.page ?? 1,
          size: value.size ?? query.size ?? 20,
          edges: slice(value.edges),
          refs: value.refs ?? {},
          assets: value.assets ?? {},
        })),
    activity: (task?: string, options?: { intent?: string; since?: number; limit?: number }) =>
      root
        .at("activity")
        .query([
          taskField(task),
          ["intent", options?.intent ? options.intent : undefined],
          ["since", options?.since ? options.since : undefined],
          ["limit", options?.limit ? options.limit : undefined],
        ])
        .read<{ items: Activity[]; cursor: number }>()
        .then((value) => ({ items: slice(value.items), cursor: value.cursor ?? 0 })),
    activityDetail: (id: number, task?: string) => {
      const detail = root.at("activity", String(id));
      return (task ? detail.query([["task", task]], "component") : detail).read<{ detail: string }>();
    },
    activityHistory: (task: string, session: string, before = 0, limit = 200) =>
      root
        .at("activity", "history")
        .query([
          ["session", String(session)],
          ["limit", limit],
          taskField(task),
          ["before", before > 0 ? before : undefined],
        ])
        .read<{ items: Activity[]; snapshot_cursor: number; earliest_cursor: number; has_more: boolean }>()
        .then((value) => ({
          items: slice(value.items),
          snapshotCursor: value.snapshot_cursor ?? 0,
          earliestCursor: value.earliest_cursor ?? 0,
          hasMore: !!value.has_more,
        })),
    intentsPage: (task: string, before = 0, limit = 300) =>
      root
        .at("intents")
        .query([["limit", limit], taskField(task), ["before", before > 0 ? before : 0], ["page", "1"]])
        .read<{ items: TaskNode[]; has_more: boolean }>()
        .then((value) => ({ items: slice(value.items), hasMore: !!value.has_more })),
    mainSessions: (task: string) =>
      scoped("main-sessions", task)
        .read<{ sessions: { seq: number; created_at: string }[]; current: number }>()
        .then((value) => ({ sessions: slice(value.sessions), current: value.current ?? 0 })),
    newMainSession: (task: string) =>
      scoped("main-session/new", task).send<{ seq: number; created_at: string; current: number }>("POST"),
  };
}
