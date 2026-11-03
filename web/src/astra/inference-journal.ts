import type { LLMRecordDetail, LLMRecordItem, LLMTask } from "@/lib/types";

import { type InferenceFilter, inferenceFilter, inferenceQuery } from "./inference-document";

export type InferencePorts = {
  list: (
    query: ReturnType<typeof inferenceQuery>,
  ) => Promise<{ records?: LLMRecordItem[] | null; total?: number | null }>;
  tasks: () => Promise<{ tasks?: LLMTask[] | null }>;
  detail: (id: number) => Promise<LLMRecordDetail>;
  settings: () => Promise<{ llm_record?: boolean }>;
  recording: (enabled: boolean) => Promise<{ llm_record?: boolean }>;
  remove: (task: string) => Promise<{ deleted: number }>;
};
type ReadState = { loading: boolean; error: string };
type InferenceState = {
  filter: InferenceFilter;
  sessionDraft: string;
  rows: LLMRecordItem[];
  total: number;
  tasks: LLMTask[];
  selectedID: number | null;
  detail: LLMRecordDetail | null;
  rawRequested: boolean;
  reads: Record<"list" | "tasks" | "detail" | "settings", ReadState>;
  recording: { loaded: boolean; enabled: boolean; error: string };
  confirmTask: string | null;
  deleteError: string;
  writing: string;
  notice: string;
};
const message = (error: unknown) => (error instanceof Error ? error.message : String(error));

/** Owns one mounted record view. Records, task counts, bodies and recording state have separate read leases. */
export class InferenceJournal {
  private state: InferenceState = {
    filter: inferenceFilter(),
    sessionDraft: "",
    rows: [],
    total: 0,
    tasks: [],
    selectedID: null,
    detail: null,
    rawRequested: false,
    reads: {
      list: { loading: true, error: "" },
      tasks: { loading: true, error: "" },
      detail: { loading: false, error: "" },
      settings: { loading: true, error: "" },
    },
    recording: { loaded: false, enabled: false, error: "" },
    confirmTask: null,
    deleteError: "",
    writing: "",
    notice: "",
  };
  private lifetime = { active: false, epoch: 0 };
  private generations = { list: 0, tasks: 0, detail: 0, settings: 0 };
  private listeners = new Set<() => void>();
  constructor(private ports: InferencePorts) {}
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<InferenceState>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }
  activate() {
    this.lifetime.active = true;
    this.lifetime.epoch++;
  }
  dispose() {
    this.lifetime.active = false;
    this.lifetime.epoch++;
  }
  private lease() {
    const epoch = this.lifetime.epoch;
    return () => this.lifetime.active && this.lifetime.epoch === epoch;
  }
  private read = async <T>(key: keyof typeof this.generations, fetch: () => Promise<T>, accept: (value: T) => void) => {
    const alive = this.lease(),
      generation = ++this.generations[key];
    const owns = () => alive() && this.generations[key] === generation;
    this.publish({ reads: { ...this.state.reads, [key]: { loading: true, error: "" } } });
    try {
      const value = await fetch();
      if (owns()) accept(value);
    } catch (error) {
      if (owns()) this.publish({ reads: { ...this.state.reads, [key]: { loading: true, error: message(error) } } });
    } finally {
      if (owns()) this.publish({ reads: { ...this.state.reads, [key]: { ...this.state.reads[key], loading: false } } });
    }
  };
  refreshTasks = () => this.read("tasks", this.ports.tasks, (result) => this.publish({ tasks: result.tasks ?? [] }));
  refreshRecording = () =>
    this.read("settings", this.ports.settings, (result) =>
      this.publish({ recording: { loaded: true, enabled: Boolean(result.llm_record), error: "" } }),
    );
  refreshDetail = () => {
    const id = this.state.selectedID;
    if (id === null) return Promise.resolve();
    return this.read(
      "detail",
      () => this.ports.detail(id),
      (detail) => {
        if (this.state.selectedID === id) this.publish({ detail });
      },
    );
  };
  select = (id: number | null) => {
    if (id === this.state.selectedID) return;
    if (id !== null && !this.state.rows.some((row) => row.id === id)) return;
    this.generations.detail++;
    this.publish({
      selectedID: id,
      detail: null,
      reads: { ...this.state.reads, detail: { loading: id !== null, error: "" } },
    });
    if (id !== null) void this.refreshDetail();
  };
  refreshList = () => {
    const query = inferenceQuery({ ...this.state.filter });
    return this.read(
      "list",
      () => this.ports.list(query),
      (result) => {
        const rows = result.records ?? [],
          current = this.state.selectedID;
        const selectedID = rows.some((row) => row.id === current) ? current : (rows[0]?.id ?? null);
        const changed = selectedID !== current;
        if (changed) this.generations.detail++;
        this.publish({
          rows,
          total: result.total ?? 0,
          selectedID,
          detail: changed ? null : this.state.detail,
          reads: changed
            ? { ...this.state.reads, detail: { loading: selectedID !== null, error: "" } }
            : this.state.reads,
        });
        if (changed && selectedID !== null) void this.refreshDetail();
      },
    );
  };
  refresh = () => Promise.all([this.refreshList(), this.refreshTasks()]);
  start = () => Promise.all([this.refresh(), this.refreshRecording()]);
  setFilter = (patch: Partial<InferenceFilter>) => {
    const filter = { ...this.state.filter, page: 0, ...patch };
    if ((Object.keys(filter) as (keyof InferenceFilter)[]).every((key) => filter[key] === this.state.filter[key]))
      return;
    this.publish({ filter });
    void this.refresh();
  };
  draftSession = (sessionDraft: string) => this.publish({ sessionDraft });
  requestRaw = (rawRequested: boolean) => this.publish({ rawRequested });
  openDelete = () => {
    if (this.state.filter.task && !this.state.writing)
      this.publish({ confirmTask: this.state.filter.task, deleteError: "", notice: "" });
  };
  closeDelete = () => {
    if (!this.state.writing) this.publish({ confirmTask: null, deleteError: "" });
  };
  private mutate = async <T>(
    key: string,
    run: () => Promise<T>,
    accept: (value: T) => unknown,
    fail: (error: string) => void,
  ) => {
    if (!this.lifetime.active || this.state.writing) return;
    const alive = this.lease();
    this.publish({ writing: key, notice: "" });
    try {
      const value = await run();
      if (alive()) await accept(value);
    } catch (error) {
      if (alive()) fail(message(error));
    } finally {
      if (alive()) this.publish({ writing: "" });
    }
  };
  setRecording = (enabled: boolean) => {
    if (!this.state.recording.loaded || this.state.writing) return Promise.resolve();
    this.generations.settings++;
    this.publish({
      recording: { ...this.state.recording, error: "" },
      reads: { ...this.state.reads, settings: { loading: false, error: "" } },
    });
    return this.mutate(
      "recording",
      () => this.ports.recording(enabled),
      (result) => {
        this.publish({
          recording: { loaded: true, enabled: Boolean(result.llm_record), error: "" },
          notice: result.llm_record
            ? "Recording enabled for future calls"
            : "Full recording disabled; token metering continues",
        });
      },
      (error) => this.publish({ recording: { ...this.state.recording, error } }),
    );
  };
  deleteTask = () => {
    const task = this.state.confirmTask;
    if (!task) return Promise.resolve();
    return this.mutate(
      "delete",
      () => this.ports.remove(task),
      (result) => {
        this.generations.detail++;
        this.publish({
          confirmTask: null,
          deleteError: "",
          detail: null,
          selectedID: null,
          reads: { ...this.state.reads, detail: { loading: false, error: "" } },
          filter: { ...this.state.filter, task: "", page: 0 },
          notice: `${result.deleted} LLM records deleted. Conversations and token usage are unchanged.`,
        });
        return this.refresh();
      },
      (error) => this.publish({ deleteError: error }),
    );
  };
}
