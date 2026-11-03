import type { SideExchange, SideHistory } from "@/lib/side-questions";

type SideQuestionPorts = {
  history: (parent: string, before?: number) => Promise<SideHistory>;
  ask: (parent: string, question: string, requestID: string) => Promise<SideExchange>;
  clear: (parent: string) => Promise<unknown>;
  cancel: (id: string) => Promise<unknown>;
  requestID: () => string;
  isCommand: (text: string) => boolean;
  notify: (message: string) => void;
};
type SideQuestionState = {
  parent: string | null;
  open: boolean;
  items: SideExchange[];
  snapshot: SideHistory["snapshot"];
  draft: string;
  busy: boolean;
  loading: boolean;
  nextCursor: number;
  error: string;
  loadError: string;
  streamEpoch: number;
};

export function mergeSideExchanges(existing: SideExchange[], incoming: SideExchange[]) {
  const records = new Map(existing.map((item) => [item.id, item]));
  for (const item of incoming) {
    if ((records.get(item.id)?.sequence ?? -1) <= item.sequence) records.set(item.id, item);
  }
  return Array.from(records.values()).sort((left, right) => left.ordinal - right.ordinal);
}

/** One side channel, independent of main-run admission and tool execution. */
export class SideQuestionJournal {
  private state: SideQuestionState = {
    parent: null,
    open: false,
    items: [],
    snapshot: null,
    draft: "",
    busy: false,
    loading: false,
    nextCursor: 0,
    error: "",
    loadError: "",
    streamEpoch: 0,
  };
  private listeners = new Set<() => void>();
  private active = false;
  private generation = 0;
  private submitting = false;
  private retry: { question: string; id: string } | null = null;
  private accepted: { question: string; id: string } | null = null;
  constructor(private ports: SideQuestionPorts) {}
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<SideQuestionState>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }
  version = () => this.generation;
  private owns(version: number) {
    return this.active && version === this.generation;
  }
  connect(parent: string | null) {
    this.active = true;
    this.generation++;
    this.submitting = false;
    this.retry = this.accepted = null;
    this.publish({
      parent,
      open: parent ? this.state.open : false,
      items: [],
      snapshot: null,
      draft: "",
      busy: false,
      loading: Boolean(parent),
      nextCursor: 0,
      error: "",
      loadError: "",
    });
    if (parent) void this.load(0, true);
  }
  disconnect() {
    this.active = false;
    this.generation++;
  }
  setOpen = (value: boolean | ((previous: boolean) => boolean)) => {
    this.publish({ open: typeof value === "function" ? value(this.state.open) : value });
  };
  setDraft = (value: string | ((previous: string) => string)) => {
    this.publish({ draft: typeof value === "function" ? value(this.state.draft) : value });
  };
  private recoverDraft(incoming: SideExchange[]) {
    const pending = this.accepted;
    if (!pending) return this.state.draft;
    const item = incoming.find((entry) => entry.id === pending.id);
    if (!item || item.status === "running") return this.state.draft;
    this.accepted = null;
    return item.status === "failed" || item.status === "interrupted"
      ? this.state.draft || pending.question
      : this.state.draft;
  }
  load = async (before = 0, initial = false) => {
    const { parent } = this.state,
      version = this.generation;
    if (!parent) return;
    try {
      const history = await this.ports.history(parent, before);
      if (!this.owns(version)) return;
      this.publish({
        items: mergeSideExchanges(this.state.items, history.items),
        draft: initial ? this.state.draft : this.recoverDraft(history.items),
        snapshot: history.snapshot,
        ...(initial ? {} : { loadError: "" }),
        nextCursor: initial || before || !this.state.nextCursor ? history.next_cursor : this.state.nextCursor,
      });
    } catch (error) {
      if (this.owns(version))
        this.publish(initial ? { error: (error as Error).message } : { loadError: (error as Error).message });
    } finally {
      if (initial && this.owns(version)) this.publish({ loading: false });
    }
  };
  receive(version: number, id: string, data: string) {
    if (!this.owns(version)) return false;
    try {
      const item = JSON.parse(data) as SideExchange;
      if (item.id !== id) return false;
      this.publish({ items: mergeSideExchanges(this.state.items, [item]), draft: this.recoverDraft([item]) });
      return item.status !== "running";
    } catch {
      this.publish({ error: "Failed to parse side-question data, please reopen the panel" });
      return false;
    }
  }
  cleared(version: number) {
    if (this.owns(version)) this.publish({ items: [] });
  }
  ask = async (input: string) => {
    const question = input.trim(),
      { parent } = this.state,
      version = this.generation;
    if (!parent || !question || this.submitting || this.state.items.some((item) => item.status === "running")) return;
    this.submitting = true;
    this.publish({ busy: true, error: "", draft: question, open: true });
    if (this.retry?.question !== question) this.retry = { question, id: this.ports.requestID() };
    try {
      const item = await this.ports.ask(parent, question, this.retry.id);
      if (!this.owns(version)) return;
      this.accepted = { id: item.id, question };
      this.retry = null;
      this.publish({ items: mergeSideExchanges(this.state.items, [item]), draft: "", error: "" });
      void this.load();
    } catch (error) {
      if (this.owns(version)) this.publish({ error: (error as Error).message });
    } finally {
      if (this.owns(version)) {
        this.submitting = false;
        this.publish({ busy: false });
      }
    }
  };
  handleCommand = (text: string, clearMain: () => void) => {
    if (!this.state.parent || !this.ports.isCommand(text)) return false;
    const question = text.trim().slice(4).trim();
    this.setOpen(true);
    clearMain();
    if (question) {
      this.setDraft(question);
      void this.ask(question);
    }
    return true;
  };
  clear = async () => {
    const { parent } = this.state;
    if (!parent || this.submitting) return;
    this.submitting = true;
    const version = ++this.generation;
    this.publish({ busy: true });
    try {
      await this.ports.clear(parent);
      if (!this.owns(version)) return;
      this.retry = this.accepted = null;
      this.publish({ items: [], nextCursor: 0, error: "" });
    } catch (error) {
      if (this.owns(version)) this.ports.notify((error as Error).message);
    } finally {
      if (this.owns(version)) {
        this.submitting = false;
        this.publish({ busy: false, streamEpoch: this.state.streamEpoch + 1 });
        void this.load();
      }
    }
  };
  stop = async () => {
    const running = this.state.items.find((item) => item.status === "running");
    if (!running) return;
    try {
      await this.ports.cancel(running.id);
    } catch (error) {
      this.ports.notify((error as Error).message);
    }
  };
}
