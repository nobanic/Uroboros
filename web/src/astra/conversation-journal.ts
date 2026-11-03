import type { Activity } from "@/lib/types";

export type ConversationPage = { items: Activity[]; cursor: number; running: boolean; hasMore?: boolean };
export type ConversationHistory = {
  items: Activity[];
  cursor: number;
  before: number;
  more: boolean;
  ready: boolean;
  running: boolean;
  loading: boolean;
  error: string;
  olderError: string;
  tailError: string;
};
type Ports = {
  history: (before: number) => Promise<ConversationPage>;
  tail: (since: number) => Promise<ConversationPage>;
  changed: (snapshot: ConversationHistory) => void;
  settled: () => void;
};
export class ConversationJournal {
  state: ConversationHistory = {
    items: [],
    cursor: 0,
    before: 0,
    more: false,
    ready: false,
    running: false,
    loading: false,
    error: "",
    olderError: "",
    tailError: "",
  };
  private active = true;
  private recentTicket = 0;
  private tailPending = false;
  private olderPending = false;
  private tailVersion = 0;
  constructor(private ports: Ports) {}
  private publish(change: Partial<ConversationHistory>) {
    if (this.active) {
      if (
        Object.entries(change).every(([key, value]) => Object.is(this.state[key as keyof ConversationHistory], value))
      )
        return;
      this.state = { ...this.state, ...change };
      this.ports.changed(this.state);
    }
  }
  private union(items: Activity[], history = false) {
    if (!items.length) return this.state.items;
    const rows = new Map((history ? items : this.state.items).map((row) => [row.seq, row]));
    for (const row of history ? this.state.items : items) rows.set(row.seq, row);
    return [...rows.values()].sort((a, b) => a.seq - b.seq);
  }
  run() {
    this.publish({ running: true });
  }
  async recent() {
    const ticket = ++this.recentTicket,
      version = this.tailVersion;
    this.publish({ error: "" });
    try {
      const page = await this.ports.history(0);
      if (!this.active || ticket !== this.recentTicket) return;
      // A completed tail is newer than the original history snapshot.
      const bySeq = new Map(page.items.map((row) => [row.seq, row]));
      for (const row of this.state.items) bySeq.set(row.seq, row);
      const items = [...bySeq.values()].sort((a, b) => a.seq - b.seq);
      this.publish({
        items,
        cursor: Math.max(this.state.cursor, page.cursor),
        before: items[0]?.seq || 0,
        more: !!page.hasMore,
        ready: true,
        running: version === this.tailVersion ? this.state.running || page.running : this.state.running,
        error: "",
      });
    } catch (error) {
      if (ticket === this.recentTicket) this.publish({ ready: true, error: (error as Error).message });
    }
  }
  mergeHistory(page: { items: Activity[]; hasMore?: boolean }) {
    this.publish({
      items: this.union(page.items, true),
      before: page.items[0]?.seq ?? this.state.before,
      more: !!page.hasMore,
      olderError: "",
    });
  }
  async earlier() {
    if (!this.active || this.olderPending || !this.state.more) return false;
    this.olderPending = true;
    this.publish({ loading: true, olderError: "" });
    try {
      const page = await this.ports.history(this.state.before);
      if (!this.active) return false;
      this.mergeHistory(page);
      return true;
    } catch (error) {
      this.publish({ olderError: (error as Error).message });
      return false;
    } finally {
      this.olderPending = false;
      this.publish({ loading: false });
    }
  }
  async tail() {
    if (!this.active || this.tailPending) return;
    this.tailPending = true;
    try {
      const page = await this.ports.tail(this.state.cursor);
      if (!this.active) return;
      this.tailVersion++;
      this.publish({
        items: this.union(page.items),
        cursor: Math.max(this.state.cursor, page.cursor),
        running: page.running,
        tailError: "",
      });
      if (!page.running) this.ports.settled();
    } catch (error) {
      this.publish({ tailError: (error as Error).message });
    } finally {
      this.tailPending = false;
    }
  }
  dispose() {
    this.active = false;
    this.recentTicket++;
  }
}
