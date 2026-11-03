import type { InterceptRule, JudgeConfig, JudgeUsage, LLMProfile, Tool } from "@/lib/types";

import { type PolicyDraft, policyDraft, policyDraftError, policyJudgeDefaults } from "./interception-document";

export type InterceptionPorts = {
  rules: () => Promise<InterceptRule[]>;
  create: (draft: PolicyDraft) => Promise<unknown>;
  update: (id: number, draft: PolicyDraft) => Promise<unknown>;
  remove: (id: number) => Promise<unknown>;
  toggle: (id: number, enabled: boolean) => Promise<unknown>;
  scope: () => Promise<{ enabled_tools: string[] }>;
  tools: () => Promise<Tool[]>;
  setScope: (keys: string[]) => Promise<unknown>;
  judge: () => Promise<JudgeConfig>;
  profiles: () => Promise<LLMProfile[]>;
  setJudge: (config: JudgeConfig) => Promise<unknown>;
  usage: (days: number) => Promise<JudgeUsage>;
};
type InterceptionState = {
  rules: InterceptRule[];
  rulesLoading: boolean;
  rulesError: string;
  scopeKeys: string[];
  scopeInfoError: string;
  editor: { id: number | null; draft: PolicyDraft; error: string } | null;
  scope: {
    open: boolean;
    loading: boolean;
    ready: boolean;
    tools: Tool[];
    selected: ReadonlySet<string>;
    error: string;
  };
  judge: {
    loaded: boolean;
    loading: boolean;
    config: JudgeConfig;
    profiles: LLMProfile[];
    error: string;
    usage: JudgeUsage | null;
    usageError: string;
    usageLoading: boolean;
  };
  writing: string;
  notice: string;
};
const reason = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** One mounted policy session owns independent read generations and a single acknowledged write. */
export class InterceptionJournal {
  private state: InterceptionState = {
    rules: [],
    rulesLoading: true,
    rulesError: "",
    scopeKeys: [],
    scopeInfoError: "",
    editor: null,
    scope: { open: false, loading: false, ready: false, tools: [], selected: new Set(), error: "" },
    judge: {
      loaded: false,
      loading: false,
      config: policyJudgeDefaults(),
      profiles: [],
      error: "",
      usage: null,
      usageError: "",
      usageLoading: false,
    },
    writing: "",
    notice: "",
  };
  private lifetime = { active: false };
  private epoch = 0;
  private tickets = { rules: 0, info: 0, scope: 0, judge: 0, usage: 0 };
  private listeners = new Set<() => void>();
  constructor(private ports: InterceptionPorts) {}
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<InterceptionState>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }
  private token(channel: keyof typeof this.tickets) {
    const epoch = this.epoch,
      ticket = ++this.tickets[channel];
    return () => this.lifetime.active && epoch === this.epoch && ticket === this.tickets[channel];
  }
  activate() {
    this.lifetime.active = true;
    this.epoch++;
  }
  dispose() {
    this.lifetime.active = false;
    this.epoch++;
  }
  start = () => Promise.all([this.refreshRules(), this.refreshScopeInfo()]);
  refreshRules = async () => {
    const owns = this.token("rules");
    this.publish({ rulesLoading: true, rulesError: "" });
    try {
      const rules = await this.ports.rules();
      if (owns()) this.publish({ rules, rulesError: "" });
    } catch (e) {
      if (owns()) this.publish({ rulesError: reason(e) });
    } finally {
      if (owns()) this.publish({ rulesLoading: false });
    }
  };
  refreshScopeInfo = async () => {
    const owns = this.token("info");
    try {
      const value = await this.ports.scope();
      if (owns()) this.publish({ scopeKeys: [...value.enabled_tools], scopeInfoError: "" });
    } catch (e) {
      if (owns()) this.publish({ scopeInfoError: reason(e) });
    }
  };
  openEditor = (rule?: InterceptRule) => {
    if (!this.state.writing)
      this.publish({ editor: { id: rule?.id ?? null, draft: policyDraft(rule), error: "" }, notice: "" });
  };
  closeEditor = () => this.publish({ editor: null });
  patchRule = (patch: Partial<PolicyDraft>) => {
    const editor = this.state.editor;
    if (editor && !this.state.writing)
      this.publish({ editor: { ...editor, draft: { ...editor.draft, ...patch }, error: "" } });
  };
  private write = async (key: string, work: () => Promise<void>, failed: (error: string) => void) => {
    if (!this.lifetime.active || this.state.writing) return;
    const epoch = this.epoch;
    this.publish({ writing: key, notice: "" });
    try {
      await work();
    } catch (e) {
      if (this.lifetime.active && epoch === this.epoch) failed(reason(e));
    } finally {
      if (this.lifetime.active && epoch === this.epoch) this.publish({ writing: "" });
    }
  };
  saveRule = async () => {
    const editor = this.state.editor;
    if (!editor) return;
    const error = policyDraftError(editor.draft);
    if (error) {
      this.publish({ editor: { ...editor, error } });
      return;
    }
    const draft = { ...editor.draft },
      epoch = this.epoch;
    await this.write(
      "rule",
      async () => {
        if (editor.id === null) await this.ports.create(draft);
        else await this.ports.update(editor.id, draft);
        if (!this.lifetime.active || epoch !== this.epoch) return;
        this.publish({ editor: null, notice: editor.id === null ? "Rule created" : "Rule updated" });
        await this.refreshRules();
      },
      (error) => {
        if (this.state.editor) this.publish({ editor: { ...this.state.editor, error } });
      },
    );
  };
  changeRule = (rule: InterceptRule, remove = false) => {
    const epoch = this.epoch;
    return this.write(
      `rule:${rule.id}`,
      async () => {
        if (remove) await this.ports.remove(rule.id);
        else await this.ports.toggle(rule.id, !rule.enabled);
        if (!this.lifetime.active || epoch !== this.epoch) return;
        this.publish({ notice: remove ? "Rule deleted" : "Rule state saved" });
        await this.refreshRules();
      },
      (error) => this.publish({ rulesError: error }),
    );
  };
  openScope = async () => {
    if (this.state.writing) return;
    const owns = this.token("scope");
    this.publish({ scope: { ...this.state.scope, open: true, loading: true, ready: false, error: "" } });
    try {
      const [tools, config] = await Promise.all([this.ports.tools(), this.ports.scope()]);
      if (owns())
        this.publish({
          scope: { open: true, loading: false, ready: true, tools, selected: new Set(config.enabled_tools), error: "" },
        });
    } catch (e) {
      if (owns()) this.publish({ scope: { ...this.state.scope, loading: false, ready: false, error: reason(e) } });
    }
  };
  closeScope = () => {
    this.tickets.scope++;
    this.publish({ scope: { ...this.state.scope, open: false } });
  };
  selectTool = (key: string, enabled: boolean) => {
    if (!this.state.scope.ready || this.state.writing) return;
    const selected = new Set(this.state.scope.selected);
    if (enabled) selected.add(key);
    else selected.delete(key);
    this.publish({ scope: { ...this.state.scope, selected } });
  };
  saveScope = () => {
    if (!this.state.scope.ready) return Promise.resolve();
    const keys = [...this.state.scope.selected],
      epoch = this.epoch;
    return this.write(
      "scope",
      async () => {
        await this.ports.setScope(keys);
        if (!this.lifetime.active || epoch !== this.epoch) return;
        this.tickets.info++;
        this.tickets.scope++;
        this.publish({
          scopeKeys: keys,
          scopeInfoError: "",
          scope: { ...this.state.scope, open: false },
          notice: "Interception scope saved",
        });
      },
      (error) => this.publish({ scope: { ...this.state.scope, error } }),
    );
  };
  loadJudge = async () => {
    const owns = this.token("judge");
    this.publish({ judge: { ...this.state.judge, loading: true, error: "" } });
    try {
      const [config, profiles] = await Promise.all([this.ports.judge(), this.ports.profiles()]);
      if (owns()) {
        this.publish({ judge: { ...this.state.judge, loaded: true, config: { ...config }, profiles, error: "" } });
        if (config.enabled) void this.loadUsage();
      }
    } catch (e) {
      if (owns()) this.publish({ judge: { ...this.state.judge, error: reason(e) } });
    } finally {
      if (owns()) this.publish({ judge: { ...this.state.judge, loading: false } });
    }
  };
  patchJudge = (patch: Partial<JudgeConfig>) => {
    if (!this.state.judge.loaded || this.state.writing) return;
    const wasEnabled = this.state.judge.config.enabled,
      config = { ...this.state.judge.config, ...patch };
    this.publish({ judge: { ...this.state.judge, config, error: "" } });
    if (!wasEnabled && config.enabled) void this.loadUsage();
  };
  loadUsage = async () => {
    const owns = this.token("usage");
    this.publish({ judge: { ...this.state.judge, usageLoading: true, usageError: "" } });
    try {
      const usage = await this.ports.usage(30);
      if (owns()) this.publish({ judge: { ...this.state.judge, usage, usageError: "" } });
    } catch (e) {
      if (owns()) this.publish({ judge: { ...this.state.judge, usageError: reason(e) } });
    } finally {
      if (owns()) this.publish({ judge: { ...this.state.judge, usageLoading: false } });
    }
  };
  saveJudge = (restore = false) => {
    if (!this.state.judge.loaded || this.state.judge.loading) return Promise.resolve();
    const config = { ...this.state.judge.config, ...(restore ? { prompt: "" } : {}) },
      epoch = this.epoch;
    return this.write(
      "judge",
      async () => {
        await this.ports.setJudge(config);
        if (!this.lifetime.active || epoch !== this.epoch) return;
        if (restore) {
          const next = await this.ports.judge();
          if (this.lifetime.active && epoch === this.epoch)
            this.publish({
              judge: { ...this.state.judge, config: next, error: "" },
              notice: "Restored the built-in default template",
            });
        } else {
          this.publish({ notice: "Model fallback config saved" });
          await this.loadJudge();
        }
      },
      (error) => this.publish({ judge: { ...this.state.judge, error } }),
    );
  };
}
