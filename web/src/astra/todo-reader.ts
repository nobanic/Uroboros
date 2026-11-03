import { type TodoEntry, todoEntries } from "./todo-document";

type TodoState = {
  selected: number | null;
  source: number | null;
  entries: TodoEntry[] | null;
  loading: boolean;
  error: string;
};

/** Lazy read ownership; no TodoWrite mutation or main-run control port exists. */
export class TodoReader {
  private state: TodoState = { selected: null, source: null, entries: null, loading: false, error: "" };
  private generation = 0;
  private listeners = new Set<() => void>();
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<TodoState>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }
  cancel() {
    this.generation++;
    if (this.state.loading) this.publish({ loading: false });
  }
  async read(seq: number, fetchDetail: (seq: number) => Promise<string>) {
    const version = ++this.generation;
    this.publish({ selected: seq, loading: true, error: "" });
    try {
      const detail = await fetchDetail(seq);
      if (version !== this.generation) return;
      this.publish({ entries: todoEntries(detail), source: seq });
    } catch {
      if (version === this.generation) this.publish({ entries: null, source: null, error: "Failed to parse Todo" });
    } finally {
      if (version === this.generation) this.publish({ loading: false });
    }
  }
}
