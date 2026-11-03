import type { CommandRecord, ToolStat } from "@/lib/types";
import {
  toolLedgerFilter,
  toolLedgerFilterKey,
  toolLedgerQueryKey,
  toolLedgerRows,
  toolPageSizes,
  type ToolLedgerFilter,
  type ToolLedgerQuery,
  type ToolLedgerRow,
} from "./tool-ledger-document";

export type ToolLedgerPorts = {
  list: (query: {
    task?: string;
    q?: string;
    page: number;
    size: number;
  }) => Promise<{ commands: CommandRecord[]; total: number }>;
  stats: (query: { task?: string; q?: string }) => Promise<{ stats: ToolStat[] }>;
};
export type ToolLedgerState = {
  search: string;
  query: ToolLedgerQuery;
  rows: ToolLedgerRow[];
  total: number;
  dataQuery: ToolLedgerQuery | null;
  selected: string | null;
  loading: boolean;
  error: string;
  statsOpen: boolean;
  stats: ToolStat[];
  statsFilter: ToolLedgerFilter | null;
  statsLoading: boolean;
  statsError: string;
};
const failureText = (error: unknown) => (error instanceof Error ? error.message : String(error));

export class ToolLedgerJournal {
  private value: ToolLedgerState = {
    search: "",
    query: { task: "", q: "", page: 0, size: 50 },
    rows: [],
    total: 0,
    dataQuery: null,
    selected: null,
    loading: false,
    error: "",
    statsOpen: false,
    stats: [],
    statsFilter: null,
    statsLoading: false,
    statsError: "",
  };
  private listeners = new Set<() => void>();
  private active = false;
  private epoch = 0;
  private listGeneration = 0;
  private statsGeneration = 0;
  private debounce: ReturnType<typeof setTimeout> | null = null;
  constructor(private ports: ToolLedgerPorts) {}
  snapshot = () => this.value;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<ToolLedgerState>) {
    this.value = { ...this.value, ...patch };
    for (const listener of this.listeners) listener();
  }
  activate() {
    this.active = true;
    this.epoch++;
    if (this.value.search.trim() !== this.value.query.q) this.search(this.value.search);
    if (this.value.statsOpen) void this.refreshStats();
  }
  dispose() {
    this.active = false;
    this.epoch++;
    this.listGeneration++;
    this.statsGeneration++;
    if (this.debounce) clearTimeout(this.debounce);
  }
  search(text: string) {
    this.publish({ search: text });
    if (this.debounce) clearTimeout(this.debounce);
    this.debounce = setTimeout(() => {
      this.debounce = null;
      if (this.active) this.filter({ q: text.trim() });
    }, 300);
  }
  task(text: string) {
    this.filter({ task: text.replace(/\D/g, "") });
  }
  size(value: number) {
    if (toolPageSizes.some((size) => size === value)) this.filter({ size: value });
  }
  page(value: number) {
    const maximum = Math.max(0, Math.ceil(this.value.total / this.value.query.size) - 1);
    this.filter({ page: Math.max(0, Math.min(maximum, value)) });
  }
  select(key: string) {
    if (this.value.rows.some((row) => row.key === key)) this.publish({ selected: key });
  }
  private filter(patch: Partial<ToolLedgerQuery>) {
    const before = this.value.query,
      query = { ...before, ...patch };
    if (patch.task !== undefined || patch.q !== undefined || patch.size !== undefined) query.page = 0;
    if (toolLedgerQueryKey(before) === toolLedgerQueryKey(query)) return;
    this.publish({ query });
    void this.refresh();
    if (this.value.statsOpen && toolLedgerFilterKey(before) !== toolLedgerFilterKey(query)) void this.refreshStats();
  }
  async refresh() {
    if (!this.active) return;
    const query = { ...this.value.query },
      epoch = this.epoch,
      generation = ++this.listGeneration;
    const owns = () =>
      this.active &&
      epoch === this.epoch &&
      generation === this.listGeneration &&
      toolLedgerQueryKey(query) === toolLedgerQueryKey(this.value.query);
    this.publish({ loading: true, error: "" });
    try {
      const response = await this.ports.list({ ...toolLedgerFilter(query), page: query.page, size: query.size });
      if (!owns()) return;
      const records = response.commands ?? [],
        total = response.total ?? 0;
      if (
        !Array.isArray(records) ||
        records.some((row) => !row || typeof row.command !== "string" || typeof row.created_at !== "string") ||
        typeof total !== "number" ||
        !Number.isFinite(total) ||
        total < 0
      )
        throw Error("Invalid recorded execution response");
      const rows = toolLedgerRows(records);
      const selected = rows.some((row) => row.key === this.value.selected)
        ? this.value.selected
        : (rows[0]?.key ?? null);
      this.publish({ rows, total, dataQuery: query, selected });
    } catch (error) {
      if (owns()) this.publish({ error: `Failed to read tool executions: ${failureText(error)}` });
    } finally {
      if (owns()) this.publish({ loading: false });
    }
  }
  statistics(open: boolean) {
    if (open === this.value.statsOpen) return;
    this.publish({ statsOpen: open });
    if (open) void this.refreshStats();
    else {
      this.statsGeneration++;
      this.publish({ statsLoading: false });
    }
  }
  async refreshStats() {
    if (!this.active || !this.value.statsOpen) return;
    const filter = { task: this.value.query.task, q: this.value.query.q },
      epoch = this.epoch,
      generation = ++this.statsGeneration;
    const owns = () =>
      this.active &&
      epoch === this.epoch &&
      generation === this.statsGeneration &&
      this.value.statsOpen &&
      toolLedgerFilterKey(filter) === toolLedgerFilterKey(this.value.query);
    this.publish({ statsLoading: true, statsError: "" });
    try {
      const response = await this.ports.stats(toolLedgerFilter(filter));
      if (!owns()) return;
      const stats = response.stats ?? [];
      if (
        !Array.isArray(stats) ||
        stats.some((row) => !row || typeof row.total !== "number" || typeof row.errors !== "number")
      )
        throw Error("Invalid recorded statistics response");
      this.publish({ stats, statsFilter: filter });
    } catch (error) {
      if (owns()) this.publish({ statsError: `Failed to read tool statistics: ${failureText(error)}` });
    } finally {
      if (owns()) this.publish({ statsLoading: false });
    }
  }
}
