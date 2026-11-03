import type { Settings } from "@/lib/types";

import {
  type SettingsDraft,
  type SettingsField,
  type SettingsGroup,
  settingsDraftValues,
  settingsPatch,
  settingsSearchProbe,
  settingsSnapshot,
} from "./settings-document";

type SearchProbe = { ok: boolean; error?: string; count?: number; backend?: string };
type SettingsPorts = {
  read: () => Promise<Settings>;
  write: (patch: Partial<Settings>) => Promise<Settings>;
  detect: () => Promise<{ python_interpreter: string }>;
  probe: (patch: ReturnType<typeof settingsSearchProbe>) => Promise<SearchProbe>;
};
export type SettingsState = {
  server: Settings | null;
  draft: SettingsDraft;
  dirty: ReadonlySet<SettingsField>;
  loading: boolean;
  busy: string;
  readError: string;
  errors: Record<string, string>;
  optimistic: Partial<Settings>;
  probing: boolean;
  probe: SearchProbe | null;
};
const message = (error: unknown) => (error instanceof Error ? error.message : String(error));

/** The server snapshot and unsaved groups advance independently; secrets are write-only drafts. */
export class SettingsJournal {
  private value: SettingsState = {
    server: null,
    draft: settingsDraftValues(null),
    dirty: new Set(),
    loading: true,
    busy: "",
    readError: "",
    errors: {},
    optimistic: {},
    probing: false,
    probe: null,
  };
  private listeners = new Set<() => void>();
  private generation = 0;
  private readTicket = 0;
  private reading = false;
  private active = false;
  constructor(private ports: SettingsPorts) {}
  snapshot = () => this.value;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<SettingsState>) {
    this.value = { ...this.value, ...patch };
    for (const listener of this.listeners) listener();
  }
  activate() {
    this.active = true;
    this.generation++;
    this.reading = false;
    this.publish({ busy: "", optimistic: {}, probing: false });
  }
  dispose() {
    this.active = false;
    this.generation++;
    this.readTicket++;
  }
  private owns(generation: number) {
    return this.active && this.generation === generation;
  }
  private accept(settings: Settings, submitted?: { fields: SettingsField[]; draft: SettingsDraft }) {
    const server = settingsSnapshot(settings),
      base = settingsDraftValues(server),
      draft = { ...this.value.draft },
      dirty = new Set(this.value.dirty);
    for (const field of Object.keys(base) as SettingsField[]) {
      const accepted = submitted?.fields.includes(field) && draft[field] === submitted.draft[field];
      if (!dirty.has(field) || accepted) {
        draft[field] = base[field];
        dirty.delete(field);
      } else if (draft[field] === base[field]) dirty.delete(field);
    }
    this.publish({ server, draft, dirty, readError: "" });
  }
  edit = (field: SettingsField, text: string) => {
    const dirty = new Set(this.value.dirty),
      base = settingsDraftValues(this.value.server);
    if (text === base[field]) dirty.delete(field);
    else dirty.add(field);
    this.publish({ draft: { ...this.value.draft, [field]: text }, dirty });
  };
  reset = (fields: SettingsField[]) => {
    const base = settingsDraftValues(this.value.server),
      draft = { ...this.value.draft },
      dirty = new Set(this.value.dirty);
    for (const field of fields) {
      draft[field] = base[field];
      dirty.delete(field);
    }
    this.publish({ draft, dirty });
  };
  refresh = async () => {
    if (!this.active || this.reading || this.value.busy) return false;
    const generation = this.generation,
      ticket = ++this.readTicket;
    this.reading = true;
    this.publish({ loading: true });
    try {
      const server = await this.ports.read();
      if (!this.owns(generation) || ticket !== this.readTicket) return false;
      this.accept(server);
      this.publish({ loading: false });
      return true;
    } catch (error) {
      if (this.owns(generation) && ticket === this.readTicket)
        this.publish({ loading: false, readError: message(error) });
      return false;
    } finally {
      if (this.owns(generation) && ticket === this.readTicket) this.reading = false;
    }
  };
  save = async (group: SettingsGroup) => {
    try {
      const { patch, fields } = settingsPatch(group, this.value.draft);
      return await this.commit(group, patch, fields);
    } catch (error) {
      if (this.active) this.publish({ errors: { ...this.value.errors, [group]: message(error) } });
      return false;
    }
  };
  change = (group: string, patch: Partial<Settings>, refetchOnError = false) =>
    this.commit(group, patch, [], refetchOnError);
  private async commit(group: string, patch: Partial<Settings>, fields: SettingsField[], refetchOnError = false) {
    if (!this.active || !this.value.server || this.value.loading || this.value.busy) return false;
    const generation = this.generation,
      submitted = { fields, draft: { ...this.value.draft } };
    this.publish({ busy: group, optimistic: patch, errors: { ...this.value.errors, [group]: "" } });
    try {
      const server = await this.ports.write(patch);
      if (!this.owns(generation)) return false;
      this.accept(server, submitted);
      return true;
    } catch (error) {
      if (this.owns(generation)) {
        this.publish({ errors: { ...this.value.errors, [group]: message(error) } });
        // The original search controls refetched after rejection. Settings writes may
        // have persisted before a later server step failed; do not infer rollback.
        if (refetchOnError) {
          try {
            const server = await this.ports.read();
            if (this.owns(generation)) this.accept(server);
          } catch {
            /* Keep the last known snapshot and explicit write error. */
          }
        }
      }
      return false;
    } finally {
      if (this.owns(generation)) this.publish({ busy: "", optimistic: {} });
    }
  }
  detect = async () => {
    if (!this.active || !this.value.server || this.value.loading || this.value.busy) return false;
    const generation = this.generation,
      previous = this.value.draft.python;
    this.publish({ busy: "python-detect", errors: { ...this.value.errors, python: "" } });
    try {
      const result = await this.ports.detect();
      if (!this.owns(generation)) return false;
      if (this.value.draft.python === previous) this.edit("python", result.python_interpreter);
      return true;
    } catch (error) {
      if (this.owns(generation)) this.publish({ errors: { ...this.value.errors, python: message(error) } });
      return false;
    } finally {
      if (this.owns(generation)) this.publish({ busy: "" });
    }
  };
  test = async () => {
    if (!this.active || !this.value.server || this.value.loading || this.value.probing) return false;
    const generation = this.generation,
      input = settingsSearchProbe({ ...this.value.server, ...this.value.optimistic }, this.value.draft);
    this.publish({ probing: true, probe: null });
    try {
      const probe = await this.ports.probe(input);
      if (!this.owns(generation)) return false;
      this.publish({ probe });
      return probe.ok;
    } catch (error) {
      if (this.owns(generation)) this.publish({ probe: { ok: false, error: message(error) } });
      return false;
    } finally {
      if (this.owns(generation)) this.publish({ probing: false });
    }
  };
}
