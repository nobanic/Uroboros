"use client";

import { Fragment, useEffect, useState, type FormEvent, type ReactNode } from "react";
import { ThemeSwitcher } from "@/app/(main)/_components/sidebar/theme-switcher";
import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { APP_CONFIG } from "@/config/app-config";
import { MOCK } from "@/lib/mock/enabled";
import { consoleConnectionView } from "./console-connection-view";
import { ApiFailure, browserSession, collection, isRow, request, safeDetail, type DataRow } from "./client";
import { EvidenceGraph } from "./graph";
import { CoverageWorkspace } from "./coverage-workspace";
import { ModelWorkspace } from "./model-workspace";
import { MCPWorkspace } from "./mcp-workspace";
import { AgentsWorkspace } from "./agent-workspace";
import { ToolsWorkspace } from "./tool-workspace";
import { SkillsWorkspace } from "./skill-workspace";
import { FindingsWorkspace } from "./finding-workspace";
import { AssetsWorkspace } from "./asset-workspace";
import { TrafficWorkspace } from "./traffic-workspace";
import { OperationsWorkspace } from "./operations-workspace";
import type { InvestigationGraph } from "./graph-model";
import { resources, rowTitle, text, type Section } from "./resources";
import { useRemote } from "./use-remote";
import { useConsoleResource } from "./use-console-resource";
import { ConsoleJump, consoleDestinations } from "./console-jump";
import "./workspace.css";

const navigationIcons: Record<Section, UroIconName> = {
  operations: "mission",
  findings: "findings",
  assets: "assets",
  traffic: "traffic",
  models: "llm",
  agents: "agents",
  tools: "tools",
  mcp: "mcp",
  skills: "skills",
};

function Notice({ error }: { error?: Error }) {
  return error ? (
    <div className="astra-notice" role="alert">
      {error.message}
    </div>
  ) : null;
}

function Badge({ value }: { value: unknown }) {
  const label = text(value);
  return (
    <span className="astra-badge" data-state={label.toLowerCase()}>
      {label}
    </span>
  );
}

function ConsoleBrand() {
  return (
    <>
      <img src="/uroboros/dark-blue/logo-symbol.png" alt="" width={40} height={40} />
      <span>{APP_CONFIG.name}</span>
    </>
  );
}

function SignIn({ onSession }: { onSession: () => void }) {
  const [revision, refresh] = useState(0);
  const status = useRemote<{ initialized: boolean }>("/auth/status", revision);
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<Error>();
  const [password, setPassword] = useState("");
  async function submit(event: FormEvent) {
    event.preventDefault();
    setBusy(true);
    setFailure(undefined);
    try {
      const result = await request<{ token: string }>(status.data?.initialized ? "/auth/login" : "/auth/init", {
        method: "POST",
        body: status.data?.initialized ? { username: "Uroboros", password } : { password },
      });
      if (!result.token) throw new Error("The server did not issue a session");
      browserSession.save(result.token);
      setPassword("");
      onSession();
    } catch (error) {
      setFailure(error instanceof Error ? error : new Error(String(error)));
    } finally {
      setBusy(false);
    }
  }
  return (
    <main className="astra-workspace astra-auth">
      <div className="astra-auth-copy">
        <span className="astra-wordmark">
          <ConsoleBrand />
        </span>
        <p className="astra-eyebrow">Investigation workspace</p>
        <h1>Follow the evidence.</h1>
        <p>Plan operations, inspect the graph and keep every result connected to its source.</p>
      </div>
      <form className="astra-auth-card" onSubmit={submit}>
        <h2>{status.data?.initialized ? "Sign in" : `Initialize ${APP_CONFIG.name}`}</h2>
        <p>
          {status.data?.initialized
            ? "Enter your operator password."
            : "Choose a password for this independent workspace."}
        </p>
        <Notice error={status.error || failure} />
        {status.error ? (
          <button type="button" className="astra-button" onClick={() => refresh((value) => value + 1)}>
            Retry connection
          </button>
        ) : null}
        <label>
          Password
          <input
            type="password"
            value={password}
            onChange={(event) => setPassword(event.target.value)}
            autoComplete={status.data?.initialized ? "current-password" : "new-password"}
            minLength={8}
            required
            disabled={!status.data || busy}
          />
        </label>
        <button className="astra-button is-primary" disabled={!status.data || busy}>
          {busy
            ? "Connecting…"
            : status.busy
              ? "Checking workspace…"
              : status.data?.initialized
                ? "Sign in"
                : "Create workspace"}
        </button>
      </form>
    </main>
  );
}

function DataTable({
  rows,
  columns,
  onSelect,
}: {
  rows: DataRow[];
  columns: readonly string[];
  onSelect: (row: DataRow) => void;
}) {
  return (
    <div className="astra-table-scroll">
      <table className="astra-table">
        <thead>
          <tr>
            {columns.map((column) => (
              <th key={column}>{column.replaceAll("_", " ")}</th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row, index) => (
            <tr key={String(row.id ?? row.key ?? row.name ?? index)}>
              {columns.map((column, columnIndex) => (
                <td key={column}>
                  {columnIndex === 0 ? (
                    <button className="astra-row-link" onClick={() => onSelect(row)}>
                      {text(row[column] || row.name || row.id)}
                    </button>
                  ) : /^(state|status|severity)$/.test(column) ? (
                    <Badge value={row[column]} />
                  ) : (
                    <span title={text(row[column])}>{text(row[column])}</span>
                  )}
                </td>
              ))}
            </tr>
          ))}
        </tbody>
      </table>
      {!rows.length ? <p className="astra-empty">No records match this view.</p> : null}
    </div>
  );
}

function Operation({ id, onBack, onUnauthorized }: { id: string; onBack: () => void; onUnauthorized: () => void }) {
  const [revision, refresh] = useState(0),
    [tab, setTab] = useState("graph"),
    [pending, setPending] = useState(false),
    [failure, setFailure] = useState<Error>();
  const item = useRemote<DataRow>(`/tasks/${encodeURIComponent(id)}`, revision);
  const query = `task=${encodeURIComponent(id)}`;
  const endpoint =
    tab === "coverage"
      ? null
      : tab === "graph"
        ? `/exploration/graph?${query}`
        : tab === "activity"
          ? `/exploration/activity?${query}&limit=100`
          : tab === "goals"
            ? `/tasks/${encodeURIComponent(id)}/goals`
            : `/tasks/${encodeURIComponent(id)}/scope`;
  const detail = useConsoleResource<unknown>(endpoint, revision, tab === "graph" ? 20_000 : 0);
  const [selected, select] = useState<DataRow>();
  useEffect(() => {
    if (
      (item.error instanceof ApiFailure && item.error.status === 401) ||
      (detail.error instanceof ApiFailure && detail.error.status === 401)
    )
      onUnauthorized();
  }, [item.error, detail.error, onUnauthorized]);
  async function control(action: "pause" | "resume") {
    setPending(true);
    setFailure(undefined);
    try {
      await request(`/tasks/${encodeURIComponent(id)}/control`, { method: "POST", body: { action } });
      refresh((value) => value + 1);
    } catch (error) {
      setFailure(error instanceof Error ? error : new Error(String(error)));
    } finally {
      setPending(false);
    }
  }
  async function addGoal(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = event.currentTarget;
    const data = new FormData(form);
    setPending(true);
    setFailure(undefined);
    try {
      await request(`/tasks/${encodeURIComponent(id)}/goals`, {
        method: "POST",
        body: { text: String(data.get("goal")), vulnclass: "" },
      });
      form.reset();
      refresh((value) => value + 1);
    } catch (error) {
      setFailure(error instanceof Error ? error : new Error(String(error)));
    } finally {
      setPending(false);
    }
  }
  const operation = item.data;
  const rows = collection(detail.data, ["items", "goals", "scope"]);
  return (
    <div
      className="astra-operation-focus"
      data-section={tab}
      data-graph-surface={tab === "graph" || tab === "coverage"}
    >
      <button className="astra-link" onClick={onBack}>
        ← Operations
      </button>
      <div className="astra-heading">
        <div>
          <p className="astra-eyebrow">Operation {id}</p>
          <h1>{operation ? rowTitle(operation) : "Loading operation…"}</h1>
          <p>{text(operation?.goal)}</p>
        </div>
        <div className="astra-toolbar">
          <a
            className="astra-button"
            href={`/function/tasks/detail?id=${encodeURIComponent(id)}`}
            title="Open the full operation workspace"
          >
            <UroIcon name="arrow-up-right" aria-hidden="true" />
            Full workspace
          </a>
          <Badge value={operation?.paused ? "paused" : operation?.status} />
          <button
            className="astra-button"
            onClick={() => control(operation?.paused ? "resume" : "pause")}
            disabled={!operation || pending}
          >
            {operation?.paused ? "Resume" : "Pause"}
          </button>
          <button
            className="astra-button"
            onClick={() => refresh((value) => value + 1)}
            disabled={item.busy || detail.busy}
          >
            Refresh
          </button>
        </div>
      </div>
      <Notice error={failure || item.error || detail.error} />
      <nav className="astra-tabs" aria-label="Operation sections">
        {["graph", "coverage", "activity", "goals", "scope"].map((name) => (
          <button
            key={name}
            aria-current={tab === name ? "page" : undefined}
            onClick={() => {
              setTab(name);
              select(undefined);
            }}
          >
            {name}
          </button>
        ))}
      </nav>
      <section className="astra-panel" aria-busy={detail.busy}>
        <div className={`astra-panel-heading ${tab === "graph" || tab === "coverage" ? "astra-sr-only" : ""}`}>
          <h2>
            {tab === "graph"
              ? "Evidence and reasoning"
              : tab === "coverage"
                ? "Asset coverage"
                : tab === "activity"
                  ? "Execution history"
                  : tab === "goals"
                    ? "Goal management"
                    : "Authorized scope"}
          </h2>
          <span>{detail.busy ? "Refreshing…" : ""}</span>
        </div>
        {tab === "graph" && isRow(detail.data) ? (
          <EvidenceGraph graph={detail.data as InvestigationGraph} contextKey={`task:${id}`} />
        ) : null}
        {tab === "graph" && !detail.data ? (
          <p className="astra-empty" role="status">
            {detail.error ? "Graph unavailable. Use Refresh to retry." : "Loading exploration graph…"}
          </p>
        ) : null}
        {tab === "coverage" && (
          <CoverageWorkspace
            taskId={id}
            coverageEnabled={operation?.coverage_enabled !== false}
            onUnauthorized={onUnauthorized}
            revision={revision}
          />
        )}
        {tab !== "graph" && tab !== "coverage" ? (
          <DataTable
            rows={rows}
            columns={
              tab === "activity"
                ? ["worker", "kind", "summary", "created_at"]
                : tab === "goals"
                  ? ["text", "state", "origin"]
                  : ["kind", "value", "source"]
            }
            onSelect={select}
          />
        ) : null}
        {tab === "goals" ? (
          <form className="astra-inline-form" onSubmit={addGoal}>
            <label>
              Additional goal
              <input name="goal" required placeholder="An observable outcome" />
            </label>
            <button className="astra-button is-primary" disabled={pending}>
              Add goal
            </button>
          </form>
        ) : null}
      </section>
      {selected ? <RecordInspector row={selected} onClose={() => select(undefined)} /> : null}
    </div>
  );
}

function RecordInspector({ row, onClose }: { row: DataRow; onClose: () => void }) {
  return (
    <aside className="astra-record-inspector">
      <div className="astra-panel-heading">
        <h2>{rowTitle(row)}</h2>
        <button className="astra-link" onClick={onClose}>
          Close
        </button>
      </div>
      <pre>{JSON.stringify(safeDetail(row), null, 2)}</pre>
    </aside>
  );
}

function Workspace({ onLogout }: { onLogout: () => void }) {
  const [section, setSection] = useState<Section>("operations"),
    [operation, setOperation] = useState<string>(),
    [menu, setMenu] = useState(false),
    [collapsed, collapse] = useState(false);
  useEffect(() => {
    try {
      collapse(localStorage.getItem("astra.navigation.collapsed") === "true");
    } catch {}
  }, []);
  function navigate(next: Section) {
    setSection(next);
    setOperation(undefined);
    setMenu(false);
  }
  function toggleRail() {
    const next = !collapsed;
    collapse(next);
    try {
      localStorage.setItem("astra.navigation.collapsed", String(next));
    } catch {}
  }
  const health = useRemote<DataRow>("/health");
  const connection = consoleConnectionView({ data: health.data, busy: health.busy, error: health.error }, MOCK);
  const resource = resources.find((item) => item.key === section) || resources[0];
  let body: ReactNode;
  if (operation)
    body = (
      <Operation key={operation} id={operation} onBack={() => setOperation(undefined)} onUnauthorized={onLogout} />
    );
  else if (section === "models") body = <ModelWorkspace onUnauthorized={onLogout} />;
  else if (section === "mcp") body = <MCPWorkspace onUnauthorized={onLogout} />;
  else if (section === "agents") body = <AgentsWorkspace onUnauthorized={onLogout} />;
  else if (section === "tools") body = <ToolsWorkspace onUnauthorized={onLogout} />;
  else if (section === "skills") body = <SkillsWorkspace onUnauthorized={onLogout} />;
  else if (section === "findings") body = <FindingsWorkspace onUnauthorized={onLogout} />;
  else if (section === "assets") body = <AssetsWorkspace onUnauthorized={onLogout} />;
  else if (section === "traffic") body = <TrafficWorkspace onUnauthorized={onLogout} />;
  else if (section === "operations") body = <OperationsWorkspace onOpen={setOperation} onUnauthorized={onLogout} />;
  else body = <OperationsWorkspace onOpen={setOperation} onUnauthorized={onLogout} />;
  return (
    <div className="astra-workspace astra-shell" data-navigation={collapsed ? "compact" : "full"}>
      <aside className={`astra-navigation ${menu ? "is-open" : ""}`}>
        <a className="astra-wordmark" href="/astra/" aria-label={`${APP_CONFIG.name} workspace`}>
          <ConsoleBrand />
        </a>
        <p className="astra-nav-caption">Investigation workspace</p>
        <ConsoleJump navigate={navigate} />
        <nav aria-label="Workspace navigation">
          {resources.map((item) => (
            <Fragment key={item.key}>
              {(item.key === "operations" || item.key === "models") && (
                <p className="astra-nav-group">{item.key === "operations" ? "Investigation" : "Configuration"}</p>
              )}
              <button
                key={item.key}
                title={item.title}
                aria-current={item.key === section && !operation ? "page" : undefined}
                onClick={() => {
                  setSection(item.key);
                  setOperation(undefined);
                  setMenu(false);
                }}
              >
                <UroIcon name={navigationIcons[item.key]} aria-hidden="true" />
                <span>{item.title}</span>
                <UroIcon name="chevron" className="astra-nav-chevron" aria-hidden="true" />
              </button>
            </Fragment>
          ))}
        </nav>
        <nav className="astra-navigation-tools" aria-label="Additional Uroboros workflows">
          <p className="astra-nav-group">Operator tools</p>
          {consoleDestinations
            .filter((item) => item.href !== "/system/settings")
            .map((item) => (
              <a key={item.href} href={item.href} title={item.title}>
                <UroIcon name={item.icon} aria-hidden="true" />
                <span>{item.title}</span>
              </a>
            ))}
        </nav>
        <div className="astra-navigation-footer">
          <button
            className="astra-rail-toggle"
            aria-label={collapsed ? "Expand navigation" : "Collapse navigation"}
            aria-expanded={!collapsed}
            onClick={toggleRail}
          >
            <UroIcon name="panel" aria-hidden="true" />
            <span>{collapsed ? "Expand navigation" : "Collapse navigation"}</span>
          </button>
          <a href="/system/settings" title="Workspace settings">
            <UroIcon name="settings" aria-hidden="true" />
            <span>Workspace settings</span>
          </a>
          <button onClick={onLogout} title="Sign out">
            <UroIcon name="user" aria-hidden="true" />
            <span>Sign out</span>
          </button>
        </div>
      </aside>
      <div className="astra-main">
        <header className="astra-topbar">
          <button
            className="astra-menu-toggle astra-button"
            onClick={() => setMenu((value) => !value)}
            aria-expanded={menu}
          >
            Menu
          </button>
          <span>
            {APP_CONFIG.name} <span className="astra-topbar-separator">/</span>{" "}
            {operation ? "Operation" : resource.title}
          </span>
          <div className="astra-topbar-actions">
            <span className="astra-connection" data-online={connection.kind === "connected"} data-connection={connection.kind} title={connection.description}>
              {connection.label}
            </span>
            <ThemeSwitcher />
          </div>
        </header>
        <main className="astra-content">{body}</main>
      </div>
    </div>
  );
}

export function AstraConsole() {
  const [authenticated, setAuthenticated] = useState<boolean>();
  useEffect(() => setAuthenticated(Boolean(browserSession.read())), []);
  if (authenticated === undefined)
    return (
      <div className="astra-workspace astra-auth" role="status">
        Opening workspace…
      </div>
    );
  if (!authenticated) return <SignIn onSession={() => setAuthenticated(true)} />;
  return (
    <Workspace
      onLogout={() => {
        browserSession.clear();
        setAuthenticated(false);
      }}
    />
  );
}
