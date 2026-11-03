import type { ExplorationNodePage } from "@/lib/types";

import { type BroadcastSelection, broadcastAtLatest, broadcastStream, broadcastWindow } from "./broadcast-document";

export type BroadcastSnapshot = {
  document: ExplorationNodePage | null;
  documentKey: string;
  readKey: string;
  total: number;
  fresh: ReadonlySet<string>;
  pending: number;
  loading: boolean;
  error: string;
  checkedAt: number;
};
type Read = (selection: BroadcastSelection) => Promise<ExplorationNodePage>;

/** A reading window and a live-stream baseline have deliberately different lifetimes. */
export class BroadcastJournal {
  private value: BroadcastSnapshot = {
    document: null,
    documentKey: "",
    readKey: "",
    total: 0,
    fresh: new Set(),
    pending: 0,
    loading: true,
    error: "",
    checkedAt: 0,
  };
  private listeners = new Set<() => void>();
  private selection: BroadcastSelection | null = null;
  private generation = 0;
  private busyGeneration: number | null = null;
  private active = false;
  private rendered = false;
  private seen = new Set<string>();
  private baseline: number | null = null;

  constructor(
    private read: Read,
    private clock: () => number = Date.now,
  ) {}
  snapshot = () => this.value;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(change: Partial<BroadcastSnapshot>) {
    this.value = { ...this.value, ...change };
    for (const listener of this.listeners) listener();
  }
  configure(selection: BroadcastSelection) {
    const changed = !this.selection || broadcastStream(this.selection) !== broadcastStream(selection);
    this.generation++;
    this.active = true;
    this.rendered = false;
    this.selection = { ...selection, kinds: [...selection.kinds] };
    if (changed) {
      this.seen = new Set();
      this.baseline = null;
    }
    this.publish({ loading: true, error: "", ...(changed ? { pending: 0, fresh: new Set<string>() } : {}) });
  }
  suspend() {
    this.active = false;
    this.generation++;
  }
  refresh = async () => {
    const selection = this.selection;
    const generation = this.generation;
    if (!this.active || !selection || this.busyGeneration === generation) return;
    this.busyGeneration = generation;
    this.publish({ loading: true });
    try {
      const page = await this.read(selection);
      if (!this.active || this.generation !== generation) return;
      const key = broadcastWindow(selection);
      const latest = broadcastAtLatest(selection);
      const replacement = latest || !this.rendered;
      this.rendered = true;
      let fresh = this.value.fresh;
      let pending = this.baseline === null ? 0 : Math.max(0, page.total - this.baseline);
      if (latest) {
        fresh = new Set(
          this.seen.size ? page.items.filter((node) => !this.seen.has(node.id)).map((node) => node.id) : [],
        );
        this.seen = new Set(page.items.map((node) => node.id));
        this.baseline = page.total;
        pending = 0;
      }
      this.publish({
        ...(replacement ? { document: page, documentKey: key } : {}),
        readKey: key,
        total: page.total,
        fresh,
        pending,
        loading: false,
        error: "",
        checkedAt: this.clock(),
      });
    } catch (error) {
      if (this.active && this.generation === generation) {
        this.publish({
          loading: false,
          error: error instanceof Error ? error.message : "Broadcast could not be loaded",
        });
      }
    } finally {
      if (this.busyGeneration === generation) this.busyGeneration = null;
    }
  };
}
