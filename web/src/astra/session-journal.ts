import type { Activity } from "@/lib/types";

export type SessionCache = {
  items: Activity[];
  loaded: boolean;
  loading: boolean;
  loadingMore: boolean;
  hasMore: boolean;
  earliestSeq: number;
  unread: number;
  lastTs: string;
  error?: string;
};
export type SessionPages = Record<string, SessionCache>;
export type HistorySlice = { items: Activity[]; snapshotCursor: number; hasMore: boolean };
export type MainSegments = { sessions: { seq: number; created_at: string }[]; current: number };
export type JournalPorts = {
  history: (key: string, before: number, limit: number) => Promise<HistorySlice>;
  activity: (since: number, limit: number) => Promise<{ items: Activity[]; cursor: number }>;
  segments: () => Promise<MainSegments>;
  stream: (
    since: number,
    receive: (activity: Activity) => void,
    connected: (value: boolean) => void,
  ) => { close: () => void };
  changed: (pages: SessionPages, connected: boolean) => void;
  resolved: (segments: MainSegments) => void;
  audit: (activity: Activity) => void;
  pinned: () => boolean;
  mock?: boolean;
};

export const sessionHistorySize = 200;
const auditPageSize = 500,
  liveCacheLimit = 4000;
export function sessionCache(): SessionCache {
  return {
    items: [],
    loaded: false,
    loading: false,
    loadingMore: false,
    hasMore: false,
    earliestSeq: 0,
    unread: 0,
    lastTs: "",
  };
}
export function activitySession(item: Activity) {
  if (item.worker === "system" || item.kind === "llm_switch" || item.kind === "llm_failover") return "system";
  if (item.worker === "mainagent") return `main:${item.main_seg ?? 0}`;
  if (item.worker === "planner") return "plan";
  return item.intent_id ? `intent:${item.intent_id}` : "unknown";
}
export function sessionUnion(older: Activity[], newer: Activity[]) {
  const records = new Map<number, Activity>();
  for (const item of older) records.set(item.seq, item);
  for (const item of newer) records.set(item.seq, item);
  return [...records.values()].sort((left, right) => left.seq - right.seq);
}

// One controller belongs to one mounted task. Its async operations can only
// update that controller; dispose invalidates every outstanding read and stream.
export class SessionJournal {
  pages: SessionPages = {};
  connected = false;
  private closed = false;
  private active = "main:0";
  private bootKey = "main:0";
  private booting?: Promise<void>;
  private tail?: { close: () => void };
  private reads = new Map<string, Promise<void>>();
  private past = new Map<string, Promise<boolean>>();
  private delivered = new Set<number>();

  constructor(private ports: JournalPorts) {}

  private publish() {
    if (!this.closed) this.ports.changed(this.pages, this.connected);
  }
  patch = (key: string, update: (cache: SessionCache) => SessionCache) => {
    if (this.closed) return;
    this.pages = { ...this.pages, [key]: update(this.pages[key] ?? sessionCache()) };
    this.publish();
  };
  seed = (key: string) => this.patch(key, (cache) => ({ ...cache, loaded: true, loading: false, error: undefined }));
  activate = (key: string) => {
    this.active = key;
    if (this.pages[key]?.unread) this.patch(key, (cache) => ({ ...cache, unread: 0 }));
    if (!this.pages[key]?.loaded && !this.pages[key]?.loading) void this.load(key);
  };

  start = (): Promise<void> => {
    if (this.closed || this.tail) return Promise.resolve();
    if (this.booting) return this.booting;
    this.patch(this.bootKey, (cache) => ({ ...cache, loading: true, error: undefined }));
    this.booting = (async () => {
      if (this.ports.mock) {
        const snapshot = await this.ports.activity(0, 0);
        if (this.closed) return;
        const buckets: Record<string, Activity[]> = {};
        for (const item of snapshot.items) (buckets[activitySession(item)] ??= []).push(item);
        for (const [key, items] of Object.entries(buckets))
          this.acceptPage(key, { items, snapshotCursor: snapshot.cursor, hasMore: false });
        this.seed("main:0");
        this.seed("system");
        return;
      }
      const segments = await this.ports.segments();
      if (this.closed) return;
      const previous = this.bootKey;
      this.bootKey = `main:${segments.current}`;
      if (previous !== this.bootKey) this.patch(previous, (cache) => ({ ...cache, loading: false }));
      this.patch(this.bootKey, (cache) => ({ ...cache, loading: true, error: undefined }));
      this.ports.resolved({
        ...segments,
        sessions: segments.sessions.length ? segments.sessions : [{ seq: 0, created_at: "" }],
      });
      const page = await this.ports.history(this.bootKey, 0, sessionHistorySize);
      if (this.closed) return;
      this.acceptPage(this.bootKey, page);
      // Use this exact task snapshot. Other sessions' concurrent reads must not
      // advance the live cursor and create a gap in unopened sessions.
      this.tail = this.ports.stream(page.snapshotCursor, this.receive, (connected) => {
        if (this.closed) return;
        this.connected = connected;
        this.publish();
      });
    })()
      .catch((error) => this.recordError(this.bootKey, error))
      .finally(() => {
        this.booting = undefined;
      });
    return this.booting;
  };

  load = (key: string): Promise<void> => {
    if (this.closed) return Promise.resolve();
    if (this.booting && (key === this.bootKey || key === "main:0")) return this.booting;
    // A failed bootstrap retry must also establish the task stream.
    if (!this.ports.mock && !this.tail && key === this.bootKey) return this.start();
    const pending = this.reads.get(key);
    if (pending) return pending;
    if (this.ports.mock) {
      this.seed(key);
      return Promise.resolve();
    }
    this.patch(key, (cache) => ({ ...cache, loading: true, error: undefined }));
    const work = (async () => {
      if (key !== "system") {
        const page = await this.ports.history(key, 0, sessionHistorySize);
        if (!this.closed) this.acceptPage(key, page);
        return;
      }
      let cursor = 0,
        items: Activity[] = [];
      for (;;) {
        const page = await this.ports.activity(cursor, auditPageSize);
        if (this.closed) return;
        items = sessionUnion(
          items,
          page.items.filter((item) => activitySession(item) === "system"),
        );
        if (page.items.length < auditPageSize || page.cursor <= cursor) break;
        cursor = page.cursor;
      }
      this.acceptPage(key, { items, snapshotCursor: cursor, hasMore: false });
    })()
      .catch((error) => this.recordError(key, error))
      .finally(() => this.reads.delete(key));
    this.reads.set(key, work);
    return work;
  };

  earlier = (key: string): Promise<boolean> => {
    const pending = this.past.get(key);
    if (pending) return pending;
    const cache = this.pages[key];
    if (this.closed || !cache?.hasMore || !cache.earliestSeq || this.ports.mock) return Promise.resolve(false);
    this.patch(key, (current) => ({ ...current, loadingMore: true, error: undefined }));
    const work = this.ports
      .history(key, cache.earliestSeq, sessionHistorySize)
      .then((page) => {
        if (this.closed) return false;
        this.acceptPage(key, page, true);
        return true;
      })
      .catch((error) => {
        this.recordError(key, error);
        return false;
      })
      .finally(() => this.past.delete(key));
    this.past.set(key, work);
    return work;
  };

  private acceptPage(key: string, page: HistorySlice, earlier = false) {
    this.patch(key, (cache) => {
      const items = sessionUnion(page.items, cache.items);
      return {
        ...cache,
        items,
        loaded: true,
        loading: false,
        loadingMore: false,
        hasMore: page.hasMore,
        earliestSeq: items[0]?.seq ?? cache.earliestSeq,
        unread: earlier ? cache.unread : 0,
        // Ordinary history must not turn on recent-live/busy indicators. Only
        // actual tail frames establish liveness; audit/mock scans keep their
        // original timestamp behavior.
        lastTs: key === "system" || this.ports.mock ? (items.at(-1)?.ts ?? cache.lastTs) : cache.lastTs,
        error: undefined,
      };
    });
  }
  private recordError(key: string, error: unknown) {
    this.patch(key, (cache) => ({
      ...cache,
      loading: false,
      loadingMore: false,
      error: error instanceof Error ? error.message : String(error),
    }));
  }

  receive = (item: Activity) => {
    if (this.closed || !Number.isSafeInteger(item.seq) || item.seq <= 0 || this.delivered.has(item.seq)) return;
    this.delivered.add(item.seq);
    // Reconnection can replay a short tail. Bound only the dedup ledger; cached
    // history and server IDs retain their own ownership and pagination rules.
    if (this.delivered.size > liveCacheLimit * 2) this.delivered.delete(this.delivered.values().next().value!);
    if (item.kind === "llm_switch" || item.kind === "llm_failover") this.ports.audit(item);
    const key = activitySession(item);
    this.patch(key, (cache) => {
      const unread = key === this.active ? 0 : cache.unread + 1;
      if (!cache.loaded && !cache.loading && key !== this.active) {
        const items =
          item.kind === "usage" || item.kind === "result" ? sessionUnion(cache.items, [item]).slice(-4) : cache.items;
        return { ...cache, items, unread, lastTs: item.ts };
      }
      let items = sessionUnion(cache.items, [item]),
        hasMore = cache.hasMore;
      if (items.length > liveCacheLimit && (key !== this.active || this.ports.pinned())) {
        items = items.slice(-liveCacheLimit);
        hasMore = true;
      }
      return { ...cache, items, unread, hasMore, lastTs: item.ts, earliestSeq: items[0]?.seq ?? cache.earliestSeq };
    });
  };

  dispose = () => {
    this.closed = true;
    this.tail?.close();
    this.tail = undefined;
  };
}
