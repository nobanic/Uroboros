import type { ChatMention } from "@/lib/chat-mentions";

type MentionPage = { items: ChatMention[]; next_cursor?: string };
type MentionSearch = (kind: string, query: string, signal: AbortSignal, cursor?: string) => Promise<MentionPage>;
type MentionResult = {
  key: string;
  items: ChatMention[];
  nextCursor?: string;
  phase: "idle" | "loading" | "ready" | "failed" | "paging";
  error: string;
  pageError: string;
};

/** One debounced query and its sequential pages share a cancellable lifetime. */
export class MentionLookup {
  private result: MentionResult = { key: "", items: [], phase: "idle", error: "", pageError: "" };
  private listeners = new Set<() => void>();
  private generation = 0;
  private session: { kind: string; query: string; controller: AbortController; version: number } | null = null;
  private timer: ReturnType<typeof setTimeout> | undefined;
  constructor(private search: MentionSearch) {}
  snapshot = () => this.result;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<MentionResult>) {
    this.result = { ...this.result, ...patch };
    for (const listener of this.listeners) listener();
  }
  cancel() {
    clearTimeout(this.timer);
    this.timer = undefined;
    this.session?.controller.abort();
    this.session = null;
    this.generation++;
  }
  begin(kind: string, query: string) {
    this.cancel();
    const session = { kind, query, controller: new AbortController(), version: this.generation };
    this.session = session;
    this.publish({
      key: `${kind}:${query}`,
      items: [],
      nextCursor: undefined,
      phase: "loading",
      error: "",
      pageError: "",
    });
    this.timer = setTimeout(() => {
      this.timer = undefined;
      void this.read(session);
    }, 180);
  }
  more = (expectedKey: string) => {
    const session = this.session,
      cursor = this.result.nextCursor;
    if (
      !session ||
      `${session.kind}:${session.query}` !== expectedKey ||
      session.controller.signal.aborted ||
      !cursor ||
      this.result.phase === "loading" ||
      this.result.phase === "paging"
    )
      return;
    this.publish({ phase: "paging", pageError: "" });
    void this.read(session, cursor);
  };
  private async read(session: NonNullable<MentionLookup["session"]>, cursor?: string) {
    const owns = () => session.version === this.generation && !session.controller.signal.aborted;
    try {
      const page = await this.search(session.kind, session.query, session.controller.signal, cursor);
      if (!owns()) return;
      if (!Array.isArray(page?.items)) throw new Error("Invalid reference lookup response");
      let items = page.items;
      if (cursor) {
        const seen = new Set(this.result.items.map((item) => `${item.kind}:${item.id}`));
        items = [...this.result.items, ...page.items.filter((item) => !seen.has(`${item.kind}:${item.id}`))];
      }
      this.publish({ items, nextCursor: page.next_cursor, phase: "ready", error: "", pageError: "" });
    } catch (failure) {
      if (!owns()) return;
      const error = failure instanceof Error ? failure.message : String(failure);
      if (cursor) this.publish({ phase: "ready", pageError: error });
      else this.publish({ items: [], nextCursor: undefined, phase: "failed", error });
    }
  }
}
