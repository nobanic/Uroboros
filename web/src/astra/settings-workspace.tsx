"use client";
import { type KeyboardEvent, type ReactNode, useState } from "react";

import { toast } from "sonner";

import { ThemeSwitcher } from "@/app/(main)/_components/sidebar/theme-switcher";
import { UpdateCard } from "@/app/(main)/system/settings/_components/update-card";
import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { CHAT_SEND_MODE_OPTIONS, type ChatSendMode, setChatSendMode, useChatSendMode } from "@/lib/chat-send-mode";
import type { Settings } from "@/lib/types";

import {
  type SettingsDomain,
  type SettingsField,
  type SettingsGroup,
  searchProviders,
  settingsDomains,
} from "./settings-document";
import { useSettings } from "./use-settings";
import "./settings.css";

type Workspace = ReturnType<typeof useSettings>;
function SettingCard({
  icon,
  title,
  description,
  badge,
  children,
}: {
  icon: UroIconName;
  title: string;
  description: string;
  badge?: string;
  children: ReactNode;
}) {
  return (
    <article className="astra-setting-card">
      <header>
        <span>
          <UroIcon name={icon} />
        </span>
        <div>
          <h3>{title}</h3>
          <p>{description}</p>
        </div>
        {badge && <small>{badge}</small>}
      </header>
      <div className="astra-setting-content">{children}</div>
    </article>
  );
}
function SettingError({ state, name }: { state: Workspace; name: string }) {
  return state.state.errors[name] ? (
    <p role="alert" className="astra-setting-error">
      <UroIcon name="info" />
      {state.state.errors[name]}
    </p>
  ) : null;
}
function Toggle({
  id,
  label,
  value,
  disabled,
  change,
  detail,
}: {
  id: string;
  label: string;
  value: boolean;
  disabled: boolean;
  change: (value: boolean) => void;
  detail?: string;
}) {
  return (
    <label className="astra-setting-toggle" htmlFor={id}>
      <span>
        <strong>{label}</strong>
        {detail && <small>{detail}</small>}
      </span>
      <input
        id={id}
        type="checkbox"
        role="switch"
        aria-checked={value}
        checked={value}
        disabled={disabled}
        onChange={(event) => change(event.target.checked)}
      />
      <i aria-hidden="true" />
    </label>
  );
}
function ProviderSelect({
  id,
  value,
  disabled,
  change,
  optional = false,
}: {
  id: string;
  value: string;
  disabled: boolean;
  change: (value: string) => void;
  optional?: boolean;
}) {
  const known = searchProviders.some(([key]) => key === value);
  return (
    <select id={id} value={value} disabled={disabled} onChange={(event) => change(event.target.value)}>
      {optional && <option value="">No fallback</option>}
      {!known && value && <option value={value}>{value}</option>}
      {searchProviders.map(([key, text]) => (
        <option key={key} value={key}>
          {text}
        </option>
      ))}
    </select>
  );
}
const groupFields: Record<SettingsGroup, SettingsField[]> = {
  globalProxy: ["globalProxy"],
  searchProxy: ["searchProxy"],
  python: ["python"],
  workers: ["workers"],
  braveKey: ["braveKey"],
  tavilyKey: ["tavilyKey"],
  research: ["fallback", "requestBudget", "paidBudget", "rate", "cacheTTL"],
};
function SaveActions({ state, group, label }: { state: Workspace; group: SettingsGroup; label: string }) {
  const changed = groupFields[group].some((field) => state.state.dirty.has(field));
  const secret = group === "braveKey" || group === "tavilyKey";
  return (
    <div className="astra-setting-save">
      <button
        type="submit"
        className="primary"
        disabled={state.disabled || (secret && !state.state.draft[group].trim())}
      >
        {state.state.busy === group ? "Saving…" : label}
      </button>
      {changed && (
        <>
          <button type="button" disabled={!!state.state.busy} onClick={() => state.journal.reset(groupFields[group])}>
            Reset draft
          </button>
          <small>Unsaved changes</small>
        </>
      )}
    </div>
  );
}
function DraftInput({
  state,
  field,
  id,
  label,
  type = "text",
  placeholder,
}: {
  state: Workspace;
  field: SettingsField;
  id: string;
  label: string;
  type?: "text" | "password" | "number";
  placeholder?: string;
}) {
  return (
    <label className="astra-setting-field" htmlFor={id}>
      <span>{label}</span>
      <input
        id={id}
        type={type}
        value={state.state.draft[field]}
        autoComplete="off"
        placeholder={placeholder}
        disabled={!state.state.server}
        onChange={(event) => state.journal.edit(field, event.target.value)}
      />
    </label>
  );
}
function SettingForm({
  state,
  group,
  children,
  label,
}: {
  state: Workspace;
  group: SettingsGroup;
  children: ReactNode;
  label: string;
}) {
  return (
    <form
      noValidate
      onSubmit={(event) => {
        event.preventDefault();
        void state.journal.save(group).then((saved) => {
          if (saved) toast.success(`${label} saved`);
        });
      }}
    >
      {children}
      <SaveActions state={state} group={group} label={`Save ${label.toLowerCase()}`} />
      <SettingError state={state} name={group} />
    </form>
  );
}
function NetworkSettings({ state }: { state: Workspace }) {
  const current = state.effective;
  function change(name: string, patch: Partial<Settings>) {
    void state.journal.change(name, patch);
  }
  return (
    <>
      <SettingCard
        icon="traffic"
        title="Traffic capture"
        badge="Immediate"
        description="Persist agent HTTP exchanges through the recording proxy and enable traffic_search / traffic_get. Turning capture off removes proxy and traffic-tool injection."
      >
        <Toggle
          id="traffic-capture"
          label="Record traffic"
          value={!!current?.traffic_capture}
          disabled={state.disabled}
          change={(value) => change("traffic", { traffic_capture: value })}
          detail={current?.traffic_capture ? "Recording and injecting the proxy" : "Direct traffic without recording"}
        />
        <SettingError state={state} name="traffic" />
      </SettingCard>
      <SettingCard
        icon="findings"
        title="Agent auto-bind traffic"
        badge="Next agent round"
        description="Inspect captured HTTP when a vulnerability is reported and link matching exchanges. The additional inspection consumes tokens; manual binding and saved evidence remain available."
      >
        <Toggle
          id="agent-traffic-binding"
          label="Automatically bind matching evidence"
          value={!!current?.agent_traffic_binding}
          disabled={state.disabled}
          change={(value) => change("binding", { agent_traffic_binding: value })}
        />
        <SettingError state={state} name="binding" />
      </SettingCard>
      <SettingCard
        icon="route"
        title="Global egress proxy"
        description="Route target traffic through an HTTP/SOCKS proxy, upstream of capture or directly when capture is off. Leave empty for direct egress."
      >
        <SettingForm state={state} group="globalProxy" label="Global proxy">
          <DraftInput
            state={state}
            field="globalProxy"
            id="global-proxy"
            label="Proxy address"
            placeholder="socks5://user:pass@host:1080 or http://host:port"
          />
        </SettingForm>
      </SettingCard>
    </>
  );
}
function RuntimeSettings({ state }: { state: Workspace }) {
  const current = state.effective,
    sendMode = useChatSendMode();
  return (
    <>
      <SettingCard
        icon="shield"
        title="Operation-constraint injection"
        badge="Next round"
        description="Include the task's allow/deny instructions in the planner and worker system prompts. Both controls are enabled by default."
      >
        <Toggle
          id="inject-planner"
          label="Inject into planner"
          value={current?.constraints_inject_planner !== false}
          disabled={state.disabled}
          change={(value) => void state.journal.change("planner", { constraints_inject_planner: value })}
        />
        <SettingError state={state} name="planner" />
        <Toggle
          id="inject-worker"
          label="Inject into worker"
          value={current?.constraints_inject_worker !== false}
          disabled={state.disabled}
          change={(value) => void state.journal.change("worker", { constraints_inject_worker: value })}
        />
        <SettingError state={state} name="worker" />
      </SettingCard>
      <SettingCard
        icon="tools"
        title="Custom scripts · Python interpreter"
        description="Select the interpreter used by Python script tools. An empty path keeps runtime auto-detection; re-detection updates this draft before you save it."
      >
        <SettingForm state={state} group="python" label="Python interpreter">
          <DraftInput
            state={state}
            field="python"
            id="settings-python"
            label="Interpreter path"
            placeholder="/usr/bin/python3 (empty = auto-detect)"
          />
          <button type="button" disabled={state.disabled} onClick={() => void state.journal.detect()}>
            <UroIcon name="sync" />
            {state.state.busy === "python-detect" ? "Detecting…" : "Re-detect Python"}
          </button>
        </SettingForm>
      </SettingCard>
      <SettingCard
        icon="agents"
        title="Work concurrency"
        badge="New tasks"
        description="Number of concurrent Work Agents per task. The default is three; changes apply to tasks started afterward."
      >
        <SettingForm state={state} group="workers" label="Worker concurrency">
          <DraftInput state={state} field="workers" id="settings-workers" label="Work Agents per task" type="number" />
        </SettingForm>
      </SettingCard>
      <SettingCard
        icon="chat"
        title="Session input send key"
        badge="This browser"
        description="Chat and the task's main-agent input share this preference immediately. It is stored in this browser and does not sync with the account."
      >
        <label className="astra-setting-field" htmlFor="chat-send-mode">
          <span>Send method</span>
          <select
            id="chat-send-mode"
            value={sendMode}
            onChange={(event) => setChatSendMode(event.target.value as ChatSendMode)}
          >
            {CHAT_SEND_MODE_OPTIONS.map((option) => (
              <option key={option.value} value={option.value}>
                {option.label}
              </option>
            ))}
          </select>
        </label>
      </SettingCard>
    </>
  );
}
function ResearchSettings({ state }: { state: Workspace }) {
  const current = state.effective,
    enabled = !!current?.web_search_enabled,
    backend = current?.web_search_backend || "ddgs",
    key = backend === "brave-free" ? "braveKey" : "tavilyKey",
    configured = backend === "brave-free" ? current?.brave_key_set : current?.tavily_key_set;
  return (
    <>
      <SettingCard
        icon="search"
        title="Web search"
        badge="Host broker"
        description="Use the existing audited research broker. Each external attempt and provider transition retains its receipt, task budget and disclosure boundary."
      >
        <Toggle
          id="web-search"
          label="Enable web search"
          value={enabled}
          disabled={state.disabled}
          change={(value) => void state.journal.change("web-search", { web_search_enabled: value }, true)}
        />
        <SettingError state={state} name="web-search" />
        {enabled && (
          <>
            <label className="astra-setting-field" htmlFor="settings-search-provider">
              <span>Search source</span>
              <ProviderSelect
                id="settings-search-provider"
                value={backend}
                disabled={state.disabled}
                change={(value) => void state.journal.change("search-backend", { web_search_backend: value }, true)}
              />
            </label>
            <SettingError state={state} name="search-backend" />
            {(backend === "brave-free" || backend === "tavily") && (
              <div className="astra-setting-credential" key={key}>
                <h4>
                  {backend === "brave-free" ? "Brave" : "Tavily"} Search API Key{" "}
                  <small data-configured={!!configured}>{configured ? "Configured" : "Missing"}</small>
                </h4>
                <SettingForm state={state} group={key} label={backend === "brave-free" ? "Brave key" : "Tavily key"}>
                  <DraftInput
                    state={state}
                    field={key}
                    id={backend === "brave-free" ? "brave-key" : "tavily-key"}
                    label="Replacement API key"
                    type="password"
                    placeholder={configured ? "Leave empty to keep the saved key" : "Enter API key"}
                  />
                </SettingForm>
                {!configured && (
                  <p className="astra-setting-notice">
                    This provider is skipped until a key is saved. An eligible configured fallback can still run.
                  </p>
                )}
                <a
                  href={backend === "brave-free" ? "https://brave.com/search/api/" : "https://tavily.com"}
                  target="_blank"
                  rel="noreferrer"
                >
                  Provider access and pricing <UroIcon name="arrow-up-right" />
                </a>
              </div>
            )}
          </>
        )}
      </SettingCard>
      {enabled && (
        <>
          <SettingCard
            icon="route"
            title="Search egress proxy"
            description="A separate HTTP/SOCKS egress for search endpoints. It is independent of the traffic recording proxy; leave empty for direct search connections."
          >
            <SettingForm state={state} group="searchProxy" label="Search proxy">
              <DraftInput
                state={state}
                field="searchProxy"
                id="ws-proxy"
                label="Egress proxy"
                placeholder="http://host:port or socks5://host:port"
              />
            </SettingForm>
          </SettingCard>
          <SettingCard
            icon="records"
            title="Recorded failover & budgets"
            description="Configure fallback and the request, key-backed, rate and cache limits used by the audited research broker."
          >
            <SettingForm state={state} group="research" label="Research policy">
              <label className="astra-setting-field" htmlFor="settings-search-fallback">
                <span>Fallback provider</span>
                <ProviderSelect
                  id="settings-search-fallback"
                  value={state.state.draft.fallback}
                  disabled={!state.state.server}
                  optional
                  change={(value) => state.journal.edit("fallback", value)}
                />
              </label>
              <div className="astra-setting-numbers">
                {(
                  [
                    ["requestBudget", "research-task-budget", "Requests per task/session"],
                    ["paidBudget", "research-paid-budget", "Key-backed request units"],
                    ["rate", "research-rate", "Attempts per minute"],
                    ["cacheTTL", "research-cache", "Cache TTL (seconds)"],
                  ] as const
                ).map(([field, id, label]) => (
                  <DraftInput key={field} state={state} field={field} id={id} label={label} />
                ))}
              </div>
            </SettingForm>
          </SettingCard>
          <SettingCard
            icon="shield"
            title="Tor read-only research profile"
            description="GET/HEAD only, remote DNS, bounded redirects/body and no direct fallback. Each task still needs an operator seed grant; proxy coordinates stay hidden."
          >
            <Toggle
              id="tor-research"
              label="Enable Tor research"
              value={!!current?.tor_research_enabled}
              disabled={state.disabled}
              change={(value) => void state.journal.change("tor", { tor_research_enabled: value }, true)}
              detail={
                current?.tor_socks_configured
                  ? "Host-owned sidecar endpoint configured"
                  : "No host-owned SOCKS endpoint configured; remains fail-closed"
              }
            />
            <SettingError state={state} name="tor" />
          </SettingCard>
          <SettingCard
            icon="pulse"
            title="Verify search configuration"
            description="Test with the current provider, proxy and entered keys. Saved credentials remain the server-side fallback; this action does not save your drafts."
          >
            <button
              type="button"
              disabled={!state.state.server || state.state.loading || state.state.probing || !!state.state.busy}
              onClick={() => void state.journal.test()}
            >
              <UroIcon name="replay" />
              {state.state.probing ? "Testing search…" : "Test search"}
            </button>
            {state.state.probe && (
              <p role="status" className={state.state.probe.ok ? "astra-setting-result" : "astra-setting-error"}>
                {state.state.probe.ok
                  ? `Search test succeeded · ${state.state.probe.backend ?? backend} returned ${state.state.probe.count ?? 0} results`
                  : `Search test failed: ${state.state.probe.error || "unknown error"}`}
              </p>
            )}
          </SettingCard>
        </>
      )}
    </>
  );
}
export function SettingsWorkspace() {
  const state = useSettings(),
    current = state.effective;
  const [domain, selectDomain] = useState<SettingsDomain>("network");
  function navigate(event: KeyboardEvent<HTMLButtonElement>, index: number) {
    const deltas: Record<string, number> = { ArrowRight: 1, ArrowDown: 1, ArrowLeft: -1, ArrowUp: -1 };
    let next: number | undefined;
    if (event.key === "Home") next = 0;
    else if (event.key === "End") next = settingsDomains.length - 1;
    else if (event.key in deltas) next = (index + deltas[event.key] + settingsDomains.length) % settingsDomains.length;
    if (next !== undefined) {
      event.preventDefault();
      const target = settingsDomains[next].id;
      selectDomain(target);
      document.getElementById(`settings-domain-${target}`)?.focus();
    }
  }
  return (
    <section className="astra-settings" aria-label="Uroboros configuration">
      <header className="astra-settings-command">
        <div>
          <span>
            <UroIcon name="settings" />
          </span>
          <div>
            <small>Control plane</small>
            <h2>Configuration</h2>
            <p>Keep evidence, runtime and research in tune.</p>
          </div>
        </div>
        <aside>
          <span data-loaded={!!state.state.server}>
            {state.state.loading ? "Reading settings…" : state.state.readError ? "Read failed" : "State loaded"}
          </span>
          <ThemeSwitcher />
          <button
            type="button"
            aria-label="Reload settings"
            disabled={state.state.loading || !!state.state.busy}
            onClick={() => void state.journal.refresh()}
          >
            <UroIcon name="sync" />
            Reload
          </button>
        </aside>
      </header>
      <div className="astra-settings-summary">
        <div>
          <UroIcon name="traffic" />
          <span>Traffic</span>
          <strong>{current ? (current.traffic_capture ? "Capture" : "Direct") : "—"}</strong>
        </div>
        <div>
          <UroIcon name="agents" />
          <span>Workers per task</span>
          <strong>{current?.workers ?? (current ? 3 : "—")}</strong>
        </div>
        <div>
          <UroIcon name="search" />
          <span>Search</span>
          <strong>{current ? (current.web_search_enabled ? current.web_search_backend || "ddgs" : "Off") : "—"}</strong>
        </div>
        <div>
          <UroIcon name="records" />
          <span>Context</span>
          <strong>{current ? (current.noa_compaction ? "noa" : "Built-in") : "—"}</strong>
        </div>
      </div>
      {state.state.readError && (
        <div role="alert" className="astra-settings-read-error">
          <UroIcon name="info" />
          <div>
            <strong>Unable to load configuration</strong>
            <p>{state.state.readError}</p>
            {state.state.server && <small>Last known values and unsaved drafts are retained.</small>}
          </div>
          <button
            type="button"
            disabled={state.state.loading || !!state.state.busy}
            onClick={() => void state.journal.refresh()}
          >
            Retry settings
          </button>
        </div>
      )}
      <div className="astra-settings-layout">
        <aside className="astra-settings-rail">
          <div role="tablist" aria-label="Configuration domains" aria-orientation="vertical">
            {settingsDomains.map((item, index) => (
              <button
                key={item.id}
                id={`settings-domain-${item.id}`}
                type="button"
                role="tab"
                aria-selected={domain === item.id}
                aria-controls={`settings-panel-${item.id}`}
                tabIndex={domain === item.id ? 0 : -1}
                onClick={() => selectDomain(item.id)}
                onKeyDown={(event) => navigate(event, index)}
              >
                <span>
                  <UroIcon name={item.icon} />
                </span>
                <div>
                  <strong>{item.label}</strong>
                  <small>{item.about}</small>
                </div>
                <UroIcon name="chevron" />
              </button>
            ))}
          </div>
          <p>
            <UroIcon name="info" />
            {state.state.dirty.size
              ? `${state.state.dirty.size} unsaved fields. Other settings can be saved without discarding these drafts.`
              : "Server controls save per action. The send-key preference stays in this browser."}
          </p>
        </aside>
        <div className="astra-settings-panels">
          {settingsDomains.map((item) => (
            <section
              key={item.id}
              id={`settings-panel-${item.id}`}
              className="astra-settings-panel"
              role="tabpanel"
              aria-labelledby={`settings-domain-${item.id}`}
              hidden={domain !== item.id}
            >
              <header>
                <small>{item.label}</small>
                <h3>{item.about}</h3>
                <p>{item.impact}</p>
              </header>
              {item.id === "network" && <NetworkSettings state={state} />}
              {item.id === "runtime" && <RuntimeSettings state={state} />}
              {item.id === "intelligence" && <ResearchSettings state={state} />}
              {item.id === "lifecycle" && (
                <>
                  <div className="astra-settings-lifecycle">
                    <UpdateCard />
                  </div>
                  <SettingCard
                    icon="records"
                    title="Experimental · noa context compaction"
                    badge="New runs"
                    description="Switch planner, worker, main agent and chat to model-driven context compaction. Pre-compaction originals remain archived in the task workspace. Turning it off restores built-in compaction."
                  >
                    <Toggle
                      id="noa-compaction"
                      label="Use noa context compaction"
                      value={!!current?.noa_compaction}
                      disabled={state.disabled}
                      change={(value) => void state.journal.change("compaction", { noa_compaction: value })}
                    />
                    <SettingError state={state} name="compaction" />
                  </SettingCard>
                </>
              )}
            </section>
          ))}
        </div>
      </div>
    </section>
  );
}
