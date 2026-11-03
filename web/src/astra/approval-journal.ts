import type { InterceptApprovalFilter, InterceptApprovalRow, InterceptPending } from "@/lib/types";

import type { ApprovalDecision } from "./approval-document";

type ApprovalPorts = {
  history: (
    task: string | undefined,
    page: number,
    size: number,
    filter: InterceptApprovalFilter,
  ) => Promise<{ items: InterceptApprovalRow[]; total: number }>;
  pending: () => Promise<InterceptPending[]>;
  decide: (id: number, decision: ApprovalDecision) => Promise<unknown>;
};
export type ApprovalState = {
  rows: InterceptApprovalRow[];
  pending: InterceptPending[];
  page: number;
  size: number;
  total: number;
  filter: InterceptApprovalFilter;
  loading: boolean;
  refreshing: boolean;
  writing: boolean;
  historyError: string;
  pendingError: string;
  revision: number;
  selected: number | null;
};
const failure = (error: unknown) => (error instanceof Error ? error.message || "Failed to load" : "Failed to load");

/** One scoped queue owns independent read outcomes, stale-response tickets and a single decision write. */
export class ApprovalJournal {
  private state: ApprovalState = {
    rows: [],
    pending: [],
    page: 1,
    size: 20,
    total: 0,
    filter: {},
    loading: true,
    refreshing: false,
    writing: false,
    historyError: "",
    pendingError: "",
    revision: 0,
    selected: null,
  };
  private listeners = new Set<() => void>();
  private active = false;
  private generation = 0;
  private ticket = 0;
  private locked = false;
  private owns(generation: number) {
    return this.active && generation === this.generation;
  }
  constructor(
    private ports: ApprovalPorts,
    readonly taskId?: string,
  ) {}
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<ApprovalState>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }
  activate() {
    this.active = true;
    this.generation++;
  }
  dispose() {
    this.active = false;
    this.generation++;
    this.ticket++;
  }
  select = (id: number) => this.publish({ selected: id });
  page = (page: number) => {
    this.ticket++;
    this.publish({ page });
    void this.refresh();
  };
  resize = (size: number) => {
    this.ticket++;
    this.publish({ size, page: 1 });
    void this.refresh();
  };
  filter = (filter: InterceptApprovalFilter) => {
    this.ticket++;
    this.publish({ filter, page: 1, rows: [], total: 0, historyError: "", loading: true });
    void this.refresh();
  };
  refresh = async (manual = false) => {
    if (!this.active) return;
    const ticket = ++this.ticket,
      generation = this.generation,
      { page, size, filter } = this.state;
    if (manual) this.publish({ refreshing: true });
    const [history, pending] = await Promise.allSettled([
      this.ports.history(this.taskId, page, size, filter),
      this.ports.pending(),
    ]);
    if (!this.owns(generation) || ticket !== this.ticket) return;
    this.publish({
      ...(history.status === "fulfilled"
        ? { rows: history.value.items, total: history.value.total, historyError: "" }
        : { historyError: failure(history.reason) }),
      ...(pending.status === "fulfilled"
        ? { pending: pending.value, pendingError: "" }
        : { pendingError: failure(pending.reason) }),
      loading: false,
      refreshing: false,
      revision: this.state.revision + (manual ? 1 : 0),
    });
  };
  decide = async (id: number, decision: ApprovalDecision) => {
    if (!this.active || this.locked) return null;
    this.locked = true;
    const generation = this.generation;
    this.publish({ writing: true });
    let error = "";
    try {
      await this.ports.decide(id, decision);
      if (!this.owns(generation)) return null;
      this.ticket++;
      this.publish({
        rows: this.state.rows.map((row) =>
          row.id === id ? { ...row, status: decision, decided_at: new Date().toISOString() } : row,
        ),
        pending: this.state.pending.filter((row) => row.id !== id),
        revision: this.state.revision + 1,
      });
    } catch (reason) {
      error = failure(reason);
    } finally {
      if (this.owns(generation)) {
        await this.refresh(true);
        if (this.owns(generation)) this.publish({ writing: false });
      }
      this.locked = false;
    }
    return this.owns(generation) ? { error } : null;
  };
}
