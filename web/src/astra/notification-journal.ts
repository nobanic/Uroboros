import type { NotificationChannel, NotificationDelivery, NotificationMeta } from "@/lib/types";

import {
  type NotificationDraft,
  notificationDefaults,
  notificationDraft,
  notificationPayload,
} from "./notification-document";

export type NotificationQuery = { channelId?: number; state?: string; page: number; pageSize: number };
export type NotificationPorts = {
  meta: () => Promise<NotificationMeta>;
  channels: () => Promise<NotificationChannel[]>;
  deliveries: (query: NotificationQuery) => Promise<{ deliveries: NotificationDelivery[]; total: number }>;
  create: (payload: ReturnType<typeof notificationPayload>) => Promise<unknown>;
  update: (id: number, payload: Partial<ReturnType<typeof notificationPayload>>) => Promise<unknown>;
  remove: (id: number) => Promise<unknown>;
  test: (id: number) => Promise<{ latency_ms: number }>;
  retry: (id: number) => Promise<unknown>;
  settings: (patch: Record<string, string | number | boolean>) => Promise<unknown>;
};
type ReadState = { loading: boolean; error: string };
type NotificationState = {
  meta: NotificationMeta | null;
  channels: NotificationChannel[];
  reads: Record<"meta" | "channels" | "ledger", ReadState>;
  defaults: { baseURL: string; interval: string; dirty: boolean };
  editor: { id: number | null; draft: NotificationDraft; error: string; testResult: string } | null;
  tab: "channels" | "deliveries";
  ledger: { loaded: boolean; query: NotificationQuery; rows: NotificationDelivery[]; total: number };
  writing: string;
  notice: string;
  error: string;
};
const reason = (error: unknown) => (error instanceof Error ? error.message : String(error));

/** A mounted session owns read generations, unsaved defaults and one acknowledged mutation. */
export class NotificationJournal {
  private state: NotificationState = {
    meta: null,
    channels: [],
    reads: {
      meta: { loading: true, error: "" },
      channels: { loading: true, error: "" },
      ledger: { loading: false, error: "" },
    },
    defaults: { baseURL: "", interval: "", dirty: false },
    editor: null,
    tab: "channels",
    ledger: { loaded: false, query: { page: 1, pageSize: 50 }, rows: [], total: 0 },
    writing: "",
    notice: "",
    error: "",
  };
  private lifetime = { active: false, epoch: 0 };
  private generations = { meta: 0, channels: 0, ledger: 0 };
  private listeners = new Set<() => void>();
  constructor(private ports: NotificationPorts) {}
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<NotificationState>) {
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
      ticket = ++this.generations[key];
    const owns = () => alive() && ticket === this.generations[key];
    this.publish({ reads: { ...this.state.reads, [key]: { loading: true, error: "" } } });
    try {
      const value = await fetch();
      if (owns()) accept(value);
    } catch (error) {
      if (owns()) this.publish({ reads: { ...this.state.reads, [key]: { loading: true, error: reason(error) } } });
    } finally {
      if (owns()) this.publish({ reads: { ...this.state.reads, [key]: { ...this.state.reads[key], loading: false } } });
    }
  };
  refreshMeta = () =>
    this.read("meta", this.ports.meta, (meta) => {
      this.publish({
        meta,
        defaults: this.state.defaults.dirty
          ? this.state.defaults
          : { baseURL: meta.public_base_url, interval: meta.digest_interval_min, dirty: false },
      });
    });
  refreshChannels = () => this.read("channels", this.ports.channels, (channels) => this.publish({ channels }));
  refresh = () => Promise.all([this.refreshMeta(), this.refreshChannels()]);
  refreshLedger = () => {
    const query = { ...this.state.ledger.query };
    return this.read(
      "ledger",
      () => this.ports.deliveries(query),
      (value) =>
        this.publish({ ledger: { ...this.state.ledger, loaded: true, rows: value.deliveries, total: value.total } }),
    );
  };
  selectTab = (tab: NotificationState["tab"]) => {
    this.publish({ tab });
    if (tab === "deliveries" && !this.state.ledger.loaded && !this.state.reads.ledger.loading)
      void this.refreshLedger();
  };
  queryLedger = (patch: Partial<NotificationQuery>) => {
    this.publish({ ledger: { ...this.state.ledger, query: { ...this.state.ledger.query, page: 1, ...patch } } });
    void this.refreshLedger();
  };
  patchDefaults = (patch: Partial<NotificationState["defaults"]>) => {
    if (!this.state.writing) this.publish({ defaults: { ...this.state.defaults, ...patch, dirty: true } });
  };
  openEditor = (channel?: NotificationChannel) => {
    if (this.state.writing) return;
    const kind = channel?.kind ?? this.state.meta?.kinds[0]?.kind ?? "dingtalk";
    this.publish({
      editor: { id: channel?.id ?? null, draft: notificationDraft(kind, channel), error: "", testResult: "" },
      notice: "",
    });
  };
  closeEditor = () => {
    if (!this.state.writing) this.publish({ editor: null });
  };
  patchDraft = (patch: Partial<NotificationDraft>) => {
    const editor = this.state.editor;
    if (!editor || this.state.writing) return;
    // A channel kind is immutable once saved. Switching a new draft clears only its config.
    if (patch.kind && editor.id !== null && patch.kind !== editor.draft.kind) return;
    const draft = { ...editor.draft, ...patch };
    if (patch.kind && patch.kind !== editor.draft.kind) draft.config = {};
    this.publish({ editor: { ...editor, draft, error: "" } });
  };
  config = (key: string, value: unknown) => {
    if (this.state.editor) this.patchDraft({ config: { ...this.state.editor.draft.config, [key]: value } });
  };
  private mutate = async <T>(
    key: string,
    work: () => Promise<T>,
    accepted: (value: T) => void | Promise<unknown>,
    failed: (error: string) => void = (error) => this.publish({ error }),
  ) => {
    if (!this.lifetime.active || this.state.writing) return;
    const alive = this.lease();
    this.publish({ writing: key, notice: "", error: "" });
    try {
      const value = await work();
      if (alive()) await accepted(value);
    } catch (error) {
      if (alive()) failed(reason(error));
    } finally {
      if (alive()) this.publish({ writing: "" });
    }
  };
  saveChannel = () => {
    const editor = this.state.editor;
    if (!editor) return Promise.resolve();
    if (!editor.draft.name.trim()) {
      this.publish({ editor: { ...editor, error: "Please fill in a channel name" } });
      return Promise.resolve();
    }
    const payload = notificationPayload(editor.draft);
    return this.mutate(
      "channel:save",
      () => (editor.id === null ? this.ports.create(payload) : this.ports.update(editor.id, payload)),
      () => {
        this.publish({ editor: null, notice: editor.id === null ? "Channel added" : "Saved" });
        return this.refresh();
      },
      (error) => {
        if (this.state.editor) this.publish({ editor: { ...this.state.editor, error: `Save failed: ${error}` } });
      },
    );
  };
  changeChannel = (channel: NotificationChannel, remove = false) =>
    this.mutate(
      `channel:${channel.id}`,
      () => (remove ? this.ports.remove(channel.id) : this.ports.update(channel.id, { enabled: !channel.enabled })),
      () => {
        this.publish({ editor: null, notice: remove ? `Deleted: ${channel.name}` : "Channel state saved" });
        return this.refresh();
      },
    );
  testChannel = () => {
    const id = this.state.editor?.id;
    if (id === null || id === undefined) return Promise.resolve();
    return this.mutate(
      "channel:test",
      () => this.ports.test(id),
      (result) => {
        if (this.state.editor?.id === id)
          this.publish({
            editor: {
              ...this.state.editor,
              error: "",
              testResult: `Test message sent (${result.latency_ms} ms). The saved channel was used; direct tests do not enter the delivery ledger.`,
            },
          });
      },
      (error) => {
        if (this.state.editor?.id === id)
          this.publish({ editor: { ...this.state.editor, error: `Test failed: ${error}`, testResult: "" } });
      },
    );
  };
  toggleMaster = (enabled: boolean) => {
    if (!this.state.meta || this.state.writing) return Promise.resolve();
    this.generations.meta++;
    this.publish({ reads: { ...this.state.reads, meta: { ...this.state.reads.meta, loading: false } } });
    return this.mutate(
      "defaults:master",
      () => this.ports.settings({ notify_enabled: enabled }),
      () => {
        if (this.state.meta)
          this.publish({
            meta: { ...this.state.meta, enabled },
            notice: enabled ? "Notifications enabled" : "Notifications paused",
            reads: { ...this.state.reads, meta: { ...this.state.reads.meta, loading: false } },
          });
      },
    );
  };
  saveDefaults = () => {
    if (!this.state.meta) return Promise.resolve();
    const draft = this.state.defaults;
    return this.mutate(
      "defaults:save",
      () => this.ports.settings(notificationDefaults(draft.baseURL, draft.interval)),
      () => {
        this.publish({ defaults: { ...draft, dirty: false }, notice: "Router defaults saved" });
        return this.refreshMeta();
      },
    );
  };
  retry = (id: number) =>
    this.mutate(
      `delivery:${id}`,
      () => this.ports.retry(id),
      () => {
        this.publish({ notice: "Re-queued" });
        return this.refreshLedger();
      },
      (error) => this.publish({ error: `Resend failed: ${error}` }),
    );
}
