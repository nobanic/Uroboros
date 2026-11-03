"use client";

import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { ApiFailure, collection, type DataRow } from "./client";
import { consoleRequest } from "./console-api";
import { FormFields } from "./form-fields";
import type { FormValues } from "./forms";
import { EditorDialog } from "./editor-dialog";
import { mcpDocument, mcpDraft, mcpForm, mcpToolName } from "./mcp-document";
import "./forms.css";

type ToolDetail = {
  server: string;
  tools: DataRow[];
  policies: Record<string, Record<string, boolean>>;
  loading: boolean;
};

function ConnectionEditor({
  server,
  close,
  save,
}: {
  server?: DataRow;
  close: () => void;
  save: (document: FormValues) => Promise<void>;
}) {
  const [value, edit] = useState(() => mcpDraft(server)),
    [busy, pending] = useState(false),
    [error, fail] = useState<string>();
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    pending(true);
    fail(undefined);
    try {
      await save(mcpDocument(value));
      close();
    } catch (error) {
      fail(error instanceof Error ? error.message : String(error));
    } finally {
      pending(false);
    }
  };
  return (
    <EditorDialog
      title={server ? String(server.name) : "New MCP connection"}
      eyebrow="MCP connection"
      busy={busy}
      close={close}
    >
      <form onSubmit={(event) => void submit(event)}>
        {error && (
          <p role="alert" className="astra-form-error">
            {error}
          </p>
        )}
        <FormFields groups={mcpForm} value={value} onChange={edit} disabled={busy} />
        <div className="astra-form-actions">
          <button type="submit" disabled={busy}>
            {busy ? "Saving…" : "Save connection"}
          </button>
          <button type="button" onClick={close} disabled={busy}>
            Cancel
          </button>
        </div>
      </form>
    </EditorDialog>
  );
}

export function MCPWorkspace({ onUnauthorized }: { onUnauthorized?: () => void }) {
  const [servers, setServers] = useState<DataRow[]>([]),
    [agents, setAgents] = useState<DataRow[]>([]);
  const [grants, setGrants] = useState<Record<string, string[]>>({}),
    [selected, select] = useState<string>();
  const [editor, openEditor] = useState<DataRow | null>(),
    [query, search] = useState("");
  const [pending, setPending] = useState(0),
    [error, setError] = useState<string>();
  const [detail, setDetail] = useState<ToolDetail>();
  const requests = useRef(0),
    busy = pending > 0;
  const failure = useCallback(
    (error: unknown) => {
      if (error instanceof ApiFailure && error.status === 401) onUnauthorized?.();
      setError(error instanceof Error ? error.message : String(error));
    },
    [onUnauthorized],
  );
  const load = useCallback(async () => {
    const [connections, roles] = await Promise.allSettled([
      consoleRequest<unknown>("/mcp"),
      consoleRequest<unknown>("/agents"),
    ]);
    if (roles.status === "fulfilled") setAgents(collection(roles.value, ["agents"]));
    else failure(roles.reason);
    if (connections.status !== "fulfilled") {
      failure(connections.reason);
      return;
    }
    const items = collection(connections.value, ["servers"]);
    setServers(items);
    const visibility = await Promise.all(
      items.map(async (item) => {
        try {
          const result = await consoleRequest<{ agents?: Array<string | number> }>(`/visibility/mcp/${item.id}`);
          return [String(item.id), (result.agents ?? []).map(String)] as const;
        } catch (error) {
          failure(error);
          return undefined;
        }
      }),
    );
    setGrants((current) => ({ ...current, ...Object.fromEntries(visibility.filter((item) => item !== undefined)) }));
  }, [failure]);
  const act = useCallback(
    async (operation: () => Promise<unknown>, reload = true) => {
      setPending((value) => value + 1);
      setError(undefined);
      try {
        const result = await operation();
        if (reload) await load();
        return result;
      } catch (error) {
        failure(error);
        throw error;
      } finally {
        setPending((value) => value - 1);
      }
    },
    [load, failure],
  );
  useEffect(() => {
    void act(async () => undefined).catch(() => {});
  }, [act]);
  const server = servers.find((item) => String(item.id) === selected) ?? servers[0];
  const serverId = server ? String(server.id) : undefined;
  const loadTools = useCallback(
    async (id: string, discover = false) => {
      const sequence = ++requests.current;
      setDetail({ server: id, tools: [], policies: {}, loading: true });
      try {
        const [catalog, access] = await Promise.all([
          consoleRequest<unknown>(
            `/mcp/${id}/${discover ? "refresh" : "tools"}`,
            discover ? "POST" : "GET",
            discover ? {} : undefined,
          ),
          consoleRequest<{ policies?: Record<string, Record<string, boolean>> }>(`/mcp/${id}/tool-visibility`),
        ]);
        if (requests.current === sequence)
          setDetail({
            server: id,
            tools: collection(catalog, ["tools"]),
            policies: access.policies ?? {},
            loading: false,
          });
      } catch (error) {
        if (requests.current === sequence) {
          failure(error);
          setDetail({ server: id, tools: [], policies: {}, loading: false });
        }
      }
    },
    [failure],
  );
  useEffect(() => {
    if (serverId) void loadTools(serverId);
    else setDetail(undefined);
    return () => {
      requests.current++;
    };
  }, [serverId, server?.name, loadTools]);
  const invoke = (operation: () => Promise<unknown>, reload = true) => {
    void act(operation, reload).catch(() => {});
  };
  const toggleGrant = (agent: DataRow, visible: boolean) =>
    invoke(() =>
      consoleRequest("/visibility/toggle", "POST", {
        agent_id: String(agent.id),
        kind: "mcp",
        resource_id: Number(serverId),
        visible,
      }),
    );
  const toggleTool = (agent: DataRow, tool: DataRow, enabled: boolean) => {
    if (!server || !serverId) return;
    const id = serverId,
      name = String(tool.name),
      raw = mcpToolName(String(server.name), name),
      sequence = requests.current;
    invoke(async () => {
      await consoleRequest(`/mcp/${id}/tool-visibility`, "PUT", {
        agent_id: String(agent.id),
        tool_name: name,
        enabled,
      });
      if (sequence === requests.current)
        setDetail((current) =>
          current?.server === id
            ? {
                ...current,
                policies: {
                  ...current.policies,
                  [String(agent.id)]: { ...current.policies[String(agent.id)], [raw]: enabled },
                },
              }
            : current,
        );
    }, false);
  };
  const visible = servers.filter((item) =>
    [item.name, item.transport, item.command, item.url]
      .join(" ")
      .toLocaleLowerCase()
      .includes(query.toLocaleLowerCase()),
  );
  return (
    <div className="astra-model-workspace astra-mcp-workspace">
      <header className="astra-model-header">
        <div>
          <small>Configuration</small>
          <h1>MCP connections</h1>
          <p>Connections, cached tools and per-agent access.</p>
        </div>
        <div>
          <button disabled={busy} onClick={() => invoke(async () => undefined)}>
            Refresh
          </button>
          <button className="is-primary" onClick={() => openEditor(null)}>
            New connection
          </button>
        </div>
      </header>
      {error && (
        <div role="alert" className="astra-form-error">
          {error}
          <button aria-label="Dismiss error" onClick={() => setError(undefined)}>
            ×
          </button>
        </div>
      )}
      <section className="astra-model-section">
        <label>
          Find connections
          <input type="search" value={query} onChange={(event) => search(event.target.value)} />
        </label>
        <div className="astra-model-cards">
          {visible.map((item) => (
            <article key={String(item.id)} data-selected={String(item.id) === serverId}>
              <header>
                <button onClick={() => select(String(item.id))}>{String(item.name)}</button>
                <span>{item.enabled ? "Enabled" : "Disabled"}</span>
              </header>
              <p>
                {String(item.transport)} · {String(item.transport === "stdio" ? item.command : item.url)}
              </p>
              <p>
                {Array.isArray(item.tools) ? item.tools.length : 0} cached tools ·{" "}
                {(grants[String(item.id)] ?? []).length} agent grants
              </p>
              <footer>
                <button
                  onClick={() => {
                    select(String(item.id));
                    openEditor(item);
                  }}
                >
                  Edit connection
                </button>
                <button
                  disabled={busy}
                  onClick={() => invoke(() => consoleRequest("/mcp", "POST", { ...item, enabled: !item.enabled }))}
                >
                  {item.enabled ? "Disable" : "Enable"}
                </button>
                <button disabled={busy} onClick={() => invoke(() => consoleRequest(`/mcp/${item.id}`, "DELETE"))}>
                  Delete
                </button>
              </footer>
            </article>
          ))}
        </div>
        {!visible.length && <p>{busy ? "Loading connections…" : "No matching connections."}</p>}
      </section>
      {server && (
        <section className="astra-model-section" style={{ marginTop: 20 }}>
          <h2>{String(server.name)} · Access and tools</h2>
          <h3>Server visibility</h3>
          <p>
            Server access and individual tool rules are evaluated together. Tool rules are retained when a server grant
            changes.
          </p>
          <div className="astra-grant-list">
            {agents.map((agent) => (
              <label className="astra-form-check" key={String(agent.id)}>
                <input
                  type="checkbox"
                  disabled={busy}
                  checked={(grants[serverId!] ?? []).includes(String(agent.id))}
                  aria-label={`Server access for ${agent.name}`}
                  onChange={(event) => toggleGrant(agent, event.target.checked)}
                />
                {String(agent.name)}
              </label>
            ))}
          </div>
          <div className="astra-form-actions">
            <h3>Cached tools</h3>
            <button disabled={busy || detail?.loading} onClick={() => invoke(() => loadTools(serverId!, true))}>
              Discover tools
            </button>
          </div>
          {detail?.loading ? (
            <p>Loading tool access…</p>
          ) : (
            detail &&
            detail.server === serverId && (
              <>
                {!detail.tools.length && <p>No cached tools. Discover tools to refresh the connection catalog.</p>}
                {detail.tools.map((tool) => (
                  <section className="astra-mcp-tool" key={String(tool.name)}>
                    <h4>{String(tool.name)}</h4>
                    <p>{String(tool.description ?? "")}</p>
                    <div className="astra-grant-list">
                      {agents.map((agent) => (
                        <label className="astra-form-check" key={String(agent.id)}>
                          <input
                            type="checkbox"
                            aria-label={`${tool.name} access for ${agent.name}`}
                            disabled={busy}
                            checked={
                              detail.policies[String(agent.id)]?.[
                                mcpToolName(String(server.name), String(tool.name))
                              ] ?? true
                            }
                            onChange={(event) => toggleTool(agent, tool, event.target.checked)}
                          />
                          {String(agent.name)}
                        </label>
                      ))}
                    </div>
                  </section>
                ))}
              </>
            )
          )}
        </section>
      )}
      {editor !== undefined && (
        <ConnectionEditor
          key={String(editor?.id ?? "new")}
          server={editor ?? undefined}
          close={() => openEditor(undefined)}
          save={async (document) => {
            await act(() => consoleRequest("/mcp", "POST", document));
          }}
        />
      )}
    </div>
  );
}
