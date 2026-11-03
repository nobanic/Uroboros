"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { Agent, Tool } from "@/lib/types";
import { ApiFailure, type DataRow } from "./client";
import { consoleRequest } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import { FormFields } from "./form-fields";
import { OrderedChoices } from "./operation-editor";
import {
  coreToolFamilies,
  customToolDocument,
  customToolDraft,
  customToolForm,
  customToolProbe,
  schemaEdits,
  toolFamily,
  toolMetadataDocument,
  toolMetadataDraft,
  toolMetadataForm,
  trafficToolKeys,
  type SchemaEdit,
} from "./tool-document";
import "./forms.css";
import "./tool-workspace.css";

function ToolBindings({
  value,
  agents,
  change,
  disabled,
}: {
  value: string[];
  agents: Agent[];
  change: (keys: string[]) => void;
  disabled?: boolean;
}) {
  return (
    <OrderedChoices
      title="Tool agents"
      value={value}
      choices={agents.map((agent) => ({ id: agent.key, label: `${agent.name} · ${agent.key}` }))}
      change={change}
      disabled={disabled}
    />
  );
}

function SchemaMetadata({
  rows,
  change,
  disabled,
}: {
  rows: SchemaEdit[];
  change: (rows: SchemaEdit[]) => void;
  disabled: boolean;
}) {
  function patch(index: number, fields: Partial<SchemaEdit>) {
    change(rows.map((row, at) => (at === index ? { ...row, ...fields } : row)));
  }
  return (
    <fieldset disabled={disabled} className="astra-tool-schema">
      <legend>Parameter metadata</legend>
      <p>Names, types, required fields and constraints remain fixed. Descriptions and scalar defaults can be edited.</p>
      {rows.map((row, index) => (
        <article key={JSON.stringify(row.path)}>
          <header>
            <strong>{row.name}</strong>
            <code>{row.type || "reference / composite"}</code>
            {row.required && <span>Required</span>}
          </header>
          <label>
            Description
            <textarea
              name={`schema_description_${index}`}
              value={row.description}
              onChange={(event) => patch(index, { description: event.target.value })}
              rows={2}
            />
          </label>
          {row.scalar && (
            <>
              <label className="astra-form-check">
                <input
                  type="checkbox"
                  aria-label={`Use default for ${row.name}`}
                  checked={row.defaultEnabled}
                  onChange={(event) => patch(index, { defaultEnabled: event.target.checked })}
                />
                Use a default value
              </label>
              {row.defaultEnabled && (
                <label>
                  Default
                  {row.type === "boolean" ? (
                    <select
                      aria-label={`Default for ${row.name}`}
                      value={row.defaultText}
                      onChange={(event) => patch(index, { defaultText: event.target.value, defaultEdited: true })}
                    >
                      <option value="">Choose true or false</option>
                      <option value="true">true</option>
                      <option value="false">false</option>
                    </select>
                  ) : (
                    <input
                      aria-label={`Default for ${row.name}`}
                      value={row.defaultText}
                      onChange={(event) => patch(index, { defaultText: event.target.value, defaultEdited: true })}
                    />
                  )}
                </label>
              )}
            </>
          )}
        </article>
      ))}
      {!rows.length && <p>No editable parameter properties.</p>}
    </fieldset>
  );
}

function BuiltinToolEditor({
  tool,
  agents,
  trafficCapture,
  close,
  saved,
}: {
  tool: Tool;
  agents: Agent[];
  trafficCapture: boolean;
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [draft, edit] = useState(() => toolMetadataDraft(tool)),
    [rows, editRows] = useState(() => schemaEdits(tool.schema)),
    [busy, pending] = useState(false),
    [error, fail] = useState("");
  const blocked = trafficToolKeys.has(tool.key) && !trafficCapture;
  async function save(reset: boolean) {
    pending(true);
    fail("");
    try {
      if (reset) await consoleRequest(`/tools/${encodeURIComponent(tool.key)}/reset`, "POST", {});
      else
        await consoleRequest(`/tools/${encodeURIComponent(tool.key)}`, "PUT", toolMetadataDocument(tool, draft, rows));
      await saved();
      close();
    } catch (error) {
      fail(error instanceof Error ? error.message : String(error));
    } finally {
      pending(false);
    }
  }
  return (
    <EditorDialog title={tool.key} eyebrow="Tool" busy={busy} close={close}>
      <form
        className="astra-tool-editor"
        onSubmit={(event) => {
          event.preventDefault();
          void save(false);
        }}
      >
        {error && (
          <p role="alert" className="astra-form-error">
            {error}
          </p>
        )}
        {blocked && (
          <p className="astra-form-note">
            Enable workspace traffic capture to change this tool's enabled state or agent bindings. Existing values are
            retained.
          </p>
        )}
        <FormFields groups={toolMetadataForm(blocked)} value={draft} onChange={edit} disabled={busy} />
        <ToolBindings
          value={draft.agents as string[]}
          agents={agents}
          change={(names) => edit((current) => ({ ...current, agents: names }))}
          disabled={busy || blocked}
        />
        <SchemaMetadata rows={rows} change={editRows} disabled={busy} />
        <details>
          <summary>Saved schema</summary>
          <pre>{JSON.stringify(tool.schema, null, 2)}</pre>
        </details>
        <div className="astra-form-actions">
          <button disabled={busy}>Save tool metadata</button>
          <button type="button" disabled={busy} onClick={() => void save(true)}>
            Restore code defaults
          </button>
          <button type="button" disabled={busy} onClick={close}>
            Cancel
          </button>
        </div>
      </form>
    </EditorDialog>
  );
}

function CustomToolEditor({
  tool,
  agents,
  close,
  saved,
}: {
  tool?: Tool;
  agents: Agent[];
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [draft, edit] = useState(() => customToolDraft(tool)),
    [busy, pending] = useState(false),
    [error, fail] = useState(""),
    [params, setParams] = useState(""),
    [result, setResult] = useState<{ output: string; is_error: boolean }>(),
    [deleting, confirm] = useState(false);
  async function action(work: () => Promise<void>) {
    pending(true);
    fail("");
    try {
      await work();
    } catch (error) {
      fail(error instanceof Error ? error.message : String(error));
    } finally {
      pending(false);
    }
  }
  return (
    <EditorDialog title={tool?.key || "New custom tool"} eyebrow="Custom tool" busy={busy} close={close}>
      <form
        className="astra-tool-editor"
        onSubmit={(event) => {
          event.preventDefault();
          void action(async () => {
            await consoleRequest(
              tool ? `/tools/custom/${encodeURIComponent(tool.key)}` : "/tools/custom",
              tool ? "PUT" : "POST",
              customToolDocument(draft, !tool),
            );
            await saved();
            close();
          });
        }}
      >
        {error && (
          <p role="alert" className="astra-form-error">
            {error}
          </p>
        )}
        <p>
          Shell declarations describe an available Bash environment. Command, Python and HTTP adapters have an
          executable specification.
        </p>
        <FormFields groups={customToolForm(!tool)} value={draft} onChange={edit} disabled={busy} />
        <ToolBindings
          value={draft.agents as string[]}
          agents={agents}
          change={(names) => edit((current) => ({ ...current, agents: names }))}
          disabled={busy}
        />
        {draft.kind !== "shell" && (
          <details>
            <summary>Test this specification</summary>
            <p>Runs the entered specification on the server using the sample parameters. Saving is separate.</p>
            <label>
              Test parameters as JSON
              <textarea
                name="tool_test_params"
                rows={4}
                disabled={busy}
                value={params}
                onChange={(event) => setParams(event.target.value)}
              />
            </label>
            <button
              type="button"
              disabled={busy}
              onClick={() =>
                void action(async () => {
                  setResult(undefined);
                  setResult(await consoleRequest("/tools/custom/test", "POST", customToolProbe(draft, params)));
                })
              }
            >
              Run tool test
            </button>
            {result && (
              <div role={result.is_error ? "alert" : "status"}>
                <strong>{result.is_error ? "Execution returned an error" : "Execution completed"}</strong>
                <pre>{result.output}</pre>
              </div>
            )}
          </details>
        )}
        {deleting && (
          <div role="alert" className="astra-form-error">
            <p>Delete this custom tool and remove it from the catalog?</p>
            <button
              type="button"
              disabled={busy}
              onClick={() =>
                void action(async () => {
                  await consoleRequest(`/tools/custom/${encodeURIComponent(tool!.key)}`, "DELETE");
                  await saved();
                  close();
                })
              }
            >
              Confirm tool deletion
            </button>
            <button type="button" disabled={busy} onClick={() => confirm(false)}>
              Keep tool
            </button>
          </div>
        )}
        <div className="astra-form-actions">
          <button disabled={busy}>Save custom tool</button>
          {tool && (
            <button type="button" disabled={busy} onClick={() => confirm(true)}>
              Delete custom tool
            </button>
          )}
          <button type="button" disabled={busy} onClick={close}>
            Cancel
          </button>
        </div>
      </form>
    </EditorDialog>
  );
}

export function ToolsWorkspace({ onUnauthorized }: { onUnauthorized?: () => void }) {
  const [tools, setTools] = useState<Tool[]>([]),
    [agents, setAgents] = useState<Agent[]>([]),
    [capture, setCapture] = useState(false),
    [query, search] = useState(""),
    [mode, filterMode] = useState("all"),
    [family, filterFamily] = useState("all"),
    [selected, choose] = useState<string>(),
    [editor, open] = useState<Tool | null>(),
    [loading, setLoading] = useState(true),
    [error, fail] = useState("");
  const sequence = useRef(0);
  const failure = useCallback(
    (error: unknown) => {
      if (error instanceof ApiFailure && error.status === 401) onUnauthorized?.();
      fail(error instanceof Error ? error.message : String(error));
    },
    [onUnauthorized],
  );
  const load = useCallback(async () => {
    const request = ++sequence.current,
      results = await Promise.allSettled([
        consoleRequest<{ tools: Tool[] }>("/tools"),
        consoleRequest<{ agents: Agent[] }>("/agents"),
        consoleRequest<DataRow>("/settings"),
      ]);
    if (request !== sequence.current) return;
    if (results[0].status === "fulfilled") setTools(results[0].value.tools || []);
    else failure(results[0].reason);
    if (results[1].status === "fulfilled") setAgents(results[1].value.agents || []);
    else failure(results[1].reason);
    if (results[2].status === "fulfilled") setCapture(Boolean(results[2].value.traffic_capture));
    else failure(results[2].reason);
    setLoading(false);
  }, [failure]);
  useEffect(() => {
    void load();
    return () => {
      sequence.current++;
    };
  }, [load]);
  const matches = tools.filter((tool) => {
    const name = toolFamily(tool);
    if (family !== "all" && name !== family) return false;
    if (mode === "engines" && !coreToolFamilies.has(name)) return false;
    if (
      (mode === "system" && !tool.system) ||
      (mode === "custom" && tool.system) ||
      (mode === "enabled" && !tool.enabled) ||
      (mode === "disabled" && tool.enabled)
    )
      return false;
    const needle = query.trim().toLocaleLowerCase();
    return (
      !needle ||
      [tool.key, tool.description, name, ...(tool.agents || [])].some((value) =>
        value.toLocaleLowerCase().includes(needle),
      )
    );
  });
  const focused = matches.find((tool) => tool.key === selected) || matches[0],
    families = [...new Set(tools.map(toolFamily))].sort();
  return (
    <div className="astra-model-workspace astra-tool-workspace">
      <header className="astra-model-header">
        <div>
          <small>Execution catalog</small>
          <h1>Tools</h1>
          <p>
            {tools.length} tools · {tools.filter((tool) => tool.enabled).length} enabled ·{" "}
            {tools.reduce((total, tool) => total + (tool.calls || 0), 0).toLocaleString()} recorded calls
          </p>
        </div>
        <div>
          <button
            disabled={loading}
            onClick={() => {
              fail("");
              void load();
            }}
          >
            Refresh tools
          </button>
          <button className="is-primary" onClick={() => open(null)}>
            New custom tool
          </button>
        </div>
      </header>
      {error && (
        <p role="alert" className="astra-form-error">
          {error}
        </p>
      )}
      <div className="astra-tool-filters">
        <label>
          Search tools
          <input type="search" value={query} onChange={(event) => search(event.target.value)} />
        </label>
        <label>
          Catalog view
          <select name="tool_catalog_mode" value={mode} onChange={(event) => filterMode(event.target.value)}>
            {[
              ["all", "All tools"],
              ["engines", "Core engines"],
              ["system", "Built-in tools"],
              ["custom", "Custom tools"],
              ["enabled", "Enabled"],
              ["disabled", "Disabled"],
            ].map(([value, label]) => (
              <option key={value} value={value}>
                {label}
              </option>
            ))}
          </select>
        </label>
        <label>
          Tool family
          <select name="tool_family" value={family} onChange={(event) => filterFamily(event.target.value)}>
            <option value="all">All families</option>
            {families.map((family) => (
              <option key={family}>{family}</option>
            ))}
          </select>
        </label>
      </div>
      <div className="astra-tool-catalog">
        <nav aria-label="Tool catalog">
          {matches.map((tool) => (
            <button
              key={tool.key}
              aria-current={focused?.key === tool.key ? "true" : undefined}
              onClick={() => choose(tool.key)}
            >
              <strong>{tool.key}</strong>
              <small>
                {toolFamily(tool)} · {tool.enabled ? "Enabled" : "Disabled"} · {tool.calls || 0} calls
              </small>
            </button>
          ))}
        </nav>
        {focused ? (
          <section className="astra-model-section">
            <header className="astra-model-header">
              <div>
                <small>{toolFamily(focused)}</small>
                <h2>{focused.key}</h2>
              </div>
              <button onClick={() => open(focused)}>Edit tool</button>
            </header>
            <p className="astra-tool-description">{focused.description || "No description"}</p>
            <dl>
              <dt>Ownership</dt>
              <dd>{focused.system ? "Built-in" : "Custom"}</dd>
              <dt>Adapter</dt>
              <dd>{focused.kind || "builtin"}</dd>
              <dt>Schema loading</dt>
              <dd>{focused.deferred ? "On demand" : "Immediate"}</dd>
              <dt>Enabled</dt>
              <dd>{focused.enabled ? "Yes" : "No"}</dd>
              <dt>Agent bindings</dt>
              <dd>{(focused.agents || []).join(", ") || "None"}</dd>
            </dl>
            <details>
              <summary>Parameter schema</summary>
              <pre>{JSON.stringify(focused.schema, null, 2)}</pre>
            </details>
            {!focused.system && (
              <details>
                <summary>Execution specification</summary>
                <pre>{JSON.stringify(focused.exec || {}, null, 2)}</pre>
              </details>
            )}
          </section>
        ) : (
          <p>{loading ? "Loading tool catalog…" : "No tools match this view."}</p>
        )}
      </div>
      {editor !== undefined &&
        (editor?.system ? (
          <BuiltinToolEditor
            tool={editor}
            agents={agents}
            trafficCapture={capture}
            close={() => open(undefined)}
            saved={load}
          />
        ) : (
          <CustomToolEditor tool={editor || undefined} agents={agents} close={() => open(undefined)} saved={load} />
        ))}
    </div>
  );
}
