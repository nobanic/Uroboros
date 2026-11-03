"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { Agent, AgentDetail, MCPServer, PromptVersion, SkillItem, Tool } from "@/lib/types";
import { ApiFailure, collection, type DataRow } from "./client";
import { consoleRequest } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import { FormFields } from "./form-fields";
import type { FormGroup, FormValues } from "./forms";
import {
  agentIdentityDocument,
  agentRuntimeDocument,
  agentRuntimeDraft,
  agentRuntimeForm,
  promptChanges,
  wholeBudget,
} from "./agent-document";
import { AgentTriggers } from "./agent-triggers";
import "./forms.css";
import "./agent-workspace.css";

type EditorTab = "Prompt" | "History" | "Wrap-up" | "Bindings" | "Triggers";
type RefreshTarget = "all" | "snapshot" | "runtime" | "prompt" | "runWrapup" | "timeoutWrapup";
type WrapDraft = { prompt: string; turns: string; timeoutPrompt: string; timeoutTurns: string };

function identityForm(create: boolean): FormGroup[] {
  return [
    {
      title: "Agent identity",
      fields: [
        ...(create
          ? [
              {
                key: "key",
                label: "Stable agent key",
                required: true,
                hint: "Lowercase letters, digits and underscores. The key stays fixed after creation.",
              },
            ]
          : []),
        { key: "name", label: "Display name", required: true },
        { key: "description", label: "Description", type: "textarea" },
      ],
    },
  ];
}

function IdentityEditor({
  agent,
  close,
  saved,
}: {
  agent?: Agent;
  close: () => void;
  saved: (key: string) => Promise<void>;
}) {
  const [draft, edit] = useState<FormValues>({
      key: "",
      name: agent?.name || "",
      description: agent?.description || "",
    }),
    [busy, pending] = useState(false),
    [error, fail] = useState("");
  return (
    <EditorDialog title={agent ? "Edit agent identity" : "New agent"} eyebrow="Agent" busy={busy} close={close}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          pending(true);
          fail("");
          void (async () => {
            try {
              const document = agentIdentityDocument(draft, !agent);
              const result = await consoleRequest<Agent>(
                agent ? `/agents/${encodeURIComponent(agent.key)}` : "/agents",
                agent ? "PATCH" : "POST",
                document,
              );
              await saved(agent?.key || result.key);
              close();
            } catch (error) {
              fail(error instanceof Error ? error.message : String(error));
            } finally {
              pending(false);
            }
          })();
        }}
      >
        {error && (
          <p role="alert" className="astra-form-error">
            {error}
          </p>
        )}
        <FormFields groups={identityForm(!agent)} value={draft} onChange={edit} disabled={busy} />
        <div className="astra-form-actions">
          <button disabled={busy}>Save agent identity</button>
          <button type="button" disabled={busy} onClick={close}>
            Cancel
          </button>
        </div>
      </form>
    </EditorDialog>
  );
}

function PromptHistory({
  versions,
  draft,
  apply,
}: {
  versions: PromptVersion[];
  draft: string;
  apply: (prompt: string) => void;
}) {
  const [selected, choose] = useState<number>(),
    [compare, showComparison] = useState(false),
    [comparisonTarget, chooseTarget] = useState("saved");
  const version = versions.find((version) => version.version === selected),
    changes =
      version && compare
        ? promptChanges(version.template_text, comparisonTarget === "draft" ? draft : versions[0]?.template_text || "")
        : [];
  return (
    <section className="astra-agent-history">
      <h2>Prompt versions</h2>
      <p>Loading a version changes the draft. Saving the draft creates a new version.</p>
      <label>
        Recorded version
        <select
          aria-label="Recorded prompt version"
          value={selected ?? ""}
          onChange={(event) => choose(event.target.value ? Number(event.target.value) : undefined)}
        >
          <option value="">Select a version</option>
          {versions.map((version) => (
            <option key={version.version} value={version.version}>
              v{version.version} · {version.note || "No note"} · {version.ts}
            </option>
          ))}
        </select>
      </label>
      {version && (
        <>
          <p>
            {version.note} · {version.ts}
          </p>
          <pre className="astra-agent-prompt-body">{version.template_text}</pre>
          <label>
            Compare selected version with
            <select value={comparisonTarget} onChange={(event) => chooseTarget(event.target.value)}>
              <option value="saved">Current saved version</option>
              <option value="draft">Current draft</option>
            </select>
          </label>
          <div className="astra-agent-section-header">
            <button onClick={() => apply(version.template_text)}>Load version into draft</button>
            <button onClick={() => showComparison(!compare)}>{compare ? "Hide comparison" : "Compare versions"}</button>
            <button
              onClick={() => {
                void navigator.clipboard?.writeText(version.template_text);
              }}
            >
              Copy version
            </button>
          </div>
          {compare && (
            <pre className="astra-agent-diff" aria-label="Prompt version comparison">
              {changes.map((line, index) => (
                <span key={index} data-change={line.kind}>
                  {line.kind === "added" ? "+" : line.kind === "removed" ? "−" : " "} {line.text}
                  {"\n"}
                </span>
              ))}
            </pre>
          )}
        </>
      )}
      {!versions.length && <p>No recorded prompt versions.</p>}
    </section>
  );
}

export function AgentBlueprint({
  agentKey,
  onSaved,
  onUnauthorized,
}: {
  agentKey: string;
  onSaved?: () => void;
  onUnauthorized?: () => void;
}) {
  const [detail, setDetail] = useState<AgentDetail>(),
    [runtime, editRuntime] = useState<FormValues>({}),
    [prompt, editPrompt] = useState(""),
    [note, editNote] = useState(""),
    [preview, showPreview] = useState<string>(),
    [wrapup, editWrapup] = useState<WrapDraft>({ prompt: "", turns: "0", timeoutPrompt: "", timeoutTurns: "0" });
  const [tab, chooseTab] = useState<EditorTab>("Prompt"),
    [busy, pending] = useState(false),
    [error, fail] = useState(""),
    [notice, notify] = useState(""),
    [identity, showIdentity] = useState(false);
  const [connections, setConnections] = useState<MCPServer[]>([]),
    [skills, setSkills] = useState<SkillItem[]>([]),
    [tools, setTools] = useState<Tool[]>([]),
    [settings, setSettings] = useState<DataRow>({});
  const [metadataErrors, setMetadataErrors] = useState<string[]>([]),
    [bindingQuery, filterBindings] = useState("");
  const sequence = useRef(0);
  const failure = useCallback(
    (error: unknown) => {
      if (error instanceof ApiFailure && error.status === 401) onUnauthorized?.();
      fail(error instanceof Error ? error.message : String(error));
    },
    [onUnauthorized],
  );
  const load = useCallback(
    async (target: RefreshTarget = "snapshot") => {
      const request = ++sequence.current,
        result = await consoleRequest<AgentDetail>(`/agents/${encodeURIComponent(agentKey)}`);
      if (request !== sequence.current) return;
      if (!result.agent) throw new Error("The server did not return an agent record.");
      setDetail({
        ...result,
        versions: result.versions || [],
        variables: result.variables || [],
        visibility: { mcp: result.visibility?.mcp || [], skill: result.visibility?.skill || [] },
      });
      if (target === "all" || target === "runtime") editRuntime(agentRuntimeDraft(result));
      if (target === "all" || target === "prompt") {
        editPrompt(result.prompt || "");
        editNote("");
      }
      if (["all", "runWrapup", "timeoutWrapup"].includes(target))
        editWrapup((current) => ({
          ...current,
          ...(target !== "timeoutWrapup"
            ? { prompt: result.wrapup_prompt || "", turns: String(result.wrapup_max_turns || 0) }
            : {}),
          ...(target !== "runWrapup"
            ? {
                timeoutPrompt: result.task_timeout_wrapup_prompt || "",
                timeoutTurns: String(result.task_timeout_wrapup_max_turns || 0),
              }
            : {}),
        }));
    },
    [agentKey],
  );
  useEffect(() => {
    void load("all").catch(failure);
    return () => {
      sequence.current++;
    };
  }, [load, failure]);
  useEffect(() => {
    let alive = true;
    void Promise.allSettled([
      consoleRequest<unknown>("/mcp"),
      consoleRequest<unknown>("/skills"),
      consoleRequest<unknown>("/tools"),
      consoleRequest<DataRow>("/settings"),
    ]).then((results) => {
      if (!alive) return;
      const errors: string[] = [];
      for (const [index, result] of results.entries()) {
        if (result.status === "rejected") {
          errors.push(`${["MCP", "Skills", "Tools", "Settings"][index]}: ${result.reason?.message || result.reason}`);
          if (result.reason instanceof ApiFailure && result.reason.status === 401) failure(result.reason);
          continue;
        }
        if (index === 0) setConnections(collection(result.value, ["servers"]) as unknown as MCPServer[]);
        if (index === 1) setSkills(collection(result.value, ["skills"]) as unknown as SkillItem[]);
        if (index === 2) setTools(collection(result.value, ["tools"]) as unknown as Tool[]);
        if (index === 3) setSettings(result.value as DataRow);
      }
      setMetadataErrors(errors);
    });
    return () => {
      alive = false;
    };
  }, [failure]);
  async function action(work: () => Promise<void>, message = "Saved") {
    pending(true);
    fail("");
    notify("");
    try {
      await work();
      notify(message);
      onSaved?.();
    } catch (error) {
      failure(error);
    } finally {
      pending(false);
    }
  }
  if (!detail)
    return (
      <div className="astra-model-workspace astra-agent-workspace">
        {error ? (
          <p role="alert" className="astra-form-error">
            {error}
          </p>
        ) : (
          <p role="status">Loading agent…</p>
        )}
        <button onClick={() => void load("all").catch(failure)}>Retry agent</button>
      </div>
    );
  const agent = detail.agent,
    matches = (value: string) => value.toLocaleLowerCase().includes(bindingQuery.toLocaleLowerCase());
  async function visibility(kind: "mcp" | "skill", id: number | string, enabled: boolean) {
    if (!detail) return;
    const current = detail.visibility,
      mcp =
        kind === "mcp"
          ? enabled
            ? [...current.mcp, Number(id)]
            : current.mcp.filter((item) => item !== id)
          : current.mcp,
      skill =
        kind === "skill"
          ? enabled
            ? [...current.skill, String(id)]
            : current.skill.filter((item) => item !== id)
          : current.skill;
    await action(async () => {
      await consoleRequest(`/agents/${encodeURIComponent(agentKey)}/visibility`, "PUT", {
        mcp: [...new Set(mcp)],
        skill: [...new Set(skill)],
      });
      await load();
    }, "Capability visibility saved");
  }
  const wrapupForm: FormGroup[] = [
    {
      title: "Per-run settlement",
      description: `Empty prompt and 0 turns use the built-in defaults (${detail.wrapup_max_turns_default ?? 5} turns).`,
      fields: [
        { key: "prompt", label: "Wrap-up prompt", type: "textarea" },
        { key: "turns", label: "Wrap-up turn budget (0 uses default)", type: "number", min: 0, step: 1 },
      ],
    },
    ...(detail.task_timeout_wrapup_supported
      ? [
          {
            title: "Operation deadline settlement",
            description: `Independent from the per-run settlement (${detail.task_timeout_wrapup_max_turns_default ?? 5} default turns).`,
            fields: [
              { key: "timeoutPrompt", label: "Operation timeout prompt", type: "textarea" as const },
              {
                key: "timeoutTurns",
                label: "Operation timeout turn budget (0 uses default)",
                type: "number" as const,
                min: 0,
                step: 1,
              },
            ],
          },
        ]
      : []),
  ];
  return (
    <div className="astra-model-workspace astra-agent-workspace">
      <header className="astra-model-header">
        <div>
          <small>
            {agent.builtin ? "Built-in agent" : "Custom agent"} · {agent.key}
          </small>
          <h1>{agent.name}</h1>
          <p>{agent.description || agent.role}</p>
        </div>
        <div>
          <button disabled={busy} onClick={() => void action(() => load("all"), "Agent reloaded")}>
            Reload agent
          </button>
          {!agent.builtin && (
            <button disabled={busy} onClick={() => showIdentity(true)}>
              Edit identity
            </button>
          )}
        </div>
      </header>
      {error && (
        <p role="alert" className="astra-form-error">
          {error}
        </p>
      )}
      {notice && (
        <p role="status" className="astra-form-note">
          {notice}
        </p>
      )}
      {metadataErrors.length > 0 && (
        <p role="alert" className="astra-form-error">
          Some capability metadata could not be loaded: {metadataErrors.join("; ")}
        </p>
      )}
      <nav aria-label="Agent sections">
        {(["Prompt", "History", "Wrap-up", "Bindings", ...(!agent.builtin ? ["Triggers"] : [])] as EditorTab[]).map(
          (section) => (
            <button key={section} aria-pressed={tab === section} disabled={busy} onClick={() => chooseTab(section)}>
              {section}
            </button>
          ),
        )}
      </nav>
      {tab === "Prompt" && (
        <section className="astra-model-section">
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void action(async () => {
                await consoleRequest(
                  `/agents/${encodeURIComponent(agentKey)}/config`,
                  "PUT",
                  agentRuntimeDocument(detail, runtime),
                );
                await load("runtime");
              }, "Runtime settings saved");
            }}
          >
            <FormFields
              groups={agentRuntimeForm(detail, Boolean(settings.web_search_enabled))}
              value={runtime}
              onChange={editRuntime}
              disabled={busy}
            />
            {agent.key !== "goals" && (
              <p>
                Workspace web search: {settings.web_search_enabled ? "enabled" : "disabled"}. Agent preference is
                retained independently.
              </p>
            )}
            <button disabled={busy}>Save runtime settings</button>
          </form>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void action(async () => {
                await consoleRequest(`/agents/${encodeURIComponent(agentKey)}/prompt`, "PUT", {
                  template: prompt,
                  note,
                });
                await load("prompt");
              }, "Prompt saved as a new version");
            }}
          >
            <header className="astra-agent-section-header">
              <h2>System prompt</h2>
              <span>{prompt.length.toLocaleString()} characters</span>
            </header>
            <label>
              Prompt
              <textarea
                name="agent_prompt"
                rows={18}
                value={prompt}
                disabled={busy}
                onChange={(event) => editPrompt(event.target.value)}
              />
            </label>
            <label>
              Version note
              <input
                name="prompt_note"
                value={note}
                disabled={busy}
                onChange={(event) => editNote(event.target.value)}
              />
            </label>
            <div className="astra-form-actions">
              <button disabled={busy}>Save prompt version</button>
              <button
                type="button"
                disabled={busy}
                onClick={() =>
                  void action(async () => {
                    const response = await consoleRequest<{ rendered: string; error?: string }>(
                      `/agents/${encodeURIComponent(agentKey)}/prompt/preview`,
                      "POST",
                      { template: prompt },
                    );
                    showPreview(response.rendered);
                    if (response.error) throw new Error(response.error);
                  }, "Prompt preview rendered")
                }
              >
                Preview prompt
              </button>
              {agent.builtin && (
                <button
                  type="button"
                  disabled={busy}
                  onClick={() =>
                    void action(async () => {
                      await consoleRequest(`/agents/${encodeURIComponent(agentKey)}/prompt/reset`, "POST", {});
                      await load("prompt");
                    }, "Built-in prompt restored as a new version")
                  }
                >
                  Restore built-in prompt
                </button>
              )}
            </div>
          </form>
          {preview !== undefined && (
            <section aria-label="Rendered prompt preview">
              <header className="astra-agent-section-header">
                <h2>Rendered prompt preview</h2>
                <button onClick={() => showPreview(undefined)}>Close preview</button>
              </header>
              <pre className="astra-agent-prompt-body">{preview}</pre>
            </section>
          )}
          <details>
            <summary>Prompt variables ({detail.variables.length})</summary>
            {detail.variables.map((variable) => (
              <article key={variable.name}>
                <button
                  disabled={busy}
                  onClick={() => editPrompt((current) => `${current}{{.${variable.name}}}`)}
                >{`{{.${variable.name}}}`}</button>
                <small>{variable.source}</small>
                <p>{variable.description}</p>
                <pre>{variable.example}</pre>
              </article>
            ))}
          </details>
        </section>
      )}
      {tab === "History" && (
        <PromptHistory
          versions={detail.versions}
          draft={prompt}
          apply={(text) => {
            editPrompt(text);
            chooseTab("Prompt");
            notify("Version loaded into the draft; save it to create a new version.");
          }}
        />
      )}
      {tab === "Wrap-up" && (
        <section className="astra-model-section">
          <FormFields
            groups={wrapupForm}
            value={wrapup}
            onChange={(value) => editWrapup(value as WrapDraft)}
            disabled={busy}
          />
          <div className="astra-form-actions">
            <button
              disabled={busy}
              onClick={() =>
                void action(async () => {
                  await consoleRequest(`/agents/${encodeURIComponent(agentKey)}/wrapup`, "PUT", {
                    prompt: wrapup.prompt,
                    max_turns: wholeBudget(wrapup.turns, "Wrap-up turns"),
                  });
                  await load("runWrapup");
                }, "Run wrap-up saved")
              }
            >
              Save run wrap-up
            </button>
            <button
              disabled={busy}
              onClick={() =>
                void action(async () => {
                  await consoleRequest(`/agents/${encodeURIComponent(agentKey)}/wrapup/reset`, "POST", {});
                  await load("runWrapup");
                }, "Run wrap-up defaults restored")
              }
            >
              Reset run wrap-up
            </button>
          </div>
          <details>
            <summary>Built-in run wrap-up</summary>
            <pre className="astra-agent-prompt-body">{detail.wrapup_default || "No built-in prompt"}</pre>
          </details>
          {detail.task_timeout_wrapup_supported && (
            <>
              <div className="astra-form-actions">
                <button
                  disabled={busy}
                  onClick={() =>
                    void action(async () => {
                      await consoleRequest(`/agents/${encodeURIComponent(agentKey)}/wrapup/task-timeout`, "PUT", {
                        prompt: wrapup.timeoutPrompt,
                        max_turns: wholeBudget(wrapup.timeoutTurns, "Operation timeout turns"),
                      });
                      await load("timeoutWrapup");
                    }, "Operation timeout wrap-up saved")
                  }
                >
                  Save timeout wrap-up
                </button>
                <button
                  disabled={busy}
                  onClick={() =>
                    void action(async () => {
                      await consoleRequest(
                        `/agents/${encodeURIComponent(agentKey)}/wrapup/task-timeout/reset`,
                        "POST",
                        {},
                      );
                      await load("timeoutWrapup");
                    }, "Operation timeout defaults restored")
                  }
                >
                  Reset timeout wrap-up
                </button>
              </div>
              <details>
                <summary>Built-in operation timeout wrap-up</summary>
                <pre className="astra-agent-prompt-body">{detail.task_timeout_wrapup_default}</pre>
              </details>
            </>
          )}
        </section>
      )}
      {tab === "Bindings" && (
        <section className="astra-model-section">
          <label>
            Filter capabilities
            <input type="search" value={bindingQuery} onChange={(event) => filterBindings(event.target.value)} />
          </label>
          <h2>MCP connections</h2>
          {connections
            .filter((server) => matches(server.name))
            .map((server) => (
              <article className="astra-agent-binding" key={server.id}>
                <label className="astra-form-check">
                  <input
                    type="checkbox"
                    aria-label={`Grant MCP ${server.name}`}
                    checked={detail.visibility.mcp.includes(Number(server.id))}
                    disabled={busy}
                    onChange={(event) => void visibility("mcp", Number(server.id), event.target.checked)}
                  />
                  {server.name} · {server.enabled ? "Enabled" : "Disabled"}
                </label>
                <small>{server.tools?.length || 0} cached tools</small>
                {server.tools?.length ? (
                  <details>
                    <summary>Cached tool names</summary>
                    <pre>{server.tools.join("\n")}</pre>
                  </details>
                ) : null}
              </article>
            ))}
          <h2>Skills</h2>
          {skills
            .filter((skill) => matches(skill.name))
            .map((skill) => (
              <article className="astra-agent-binding" key={skill.name}>
                <label className="astra-form-check">
                  <input
                    type="checkbox"
                    aria-label={`Grant skill ${skill.name}`}
                    checked={detail.visibility.skill.includes(skill.name)}
                    disabled={busy}
                    onChange={(event) => void visibility("skill", skill.name, event.target.checked)}
                  />
                  {skill.name}
                </label>
                <p>{skill.description}</p>
              </article>
            ))}
          <h2>Tools</h2>
          {tools
            .filter((tool) => matches(`${tool.key} ${tool.description}`))
            .map((tool) => {
              const gated = ["traffic_search", "traffic_get"].includes(tool.key) && !settings.traffic_capture;
              return (
                <article className="astra-agent-binding" key={tool.key}>
                  <label className="astra-form-check">
                    <input
                      type="checkbox"
                      aria-label={`Bind tool ${tool.key}`}
                      checked={tool.agents.includes(agentKey)}
                      disabled={busy || gated}
                      onChange={(event) => {
                        const enabled = event.target.checked;
                        void action(async () => {
                          const agents = enabled
                            ? [...new Set([...tool.agents, agentKey])]
                            : tool.agents.filter((key) => key !== agentKey);
                          await consoleRequest(`/tools/${encodeURIComponent(tool.key)}`, "PUT", {
                            description: tool.description,
                            schema: tool.schema,
                            agents,
                            enabled: tool.enabled,
                          });
                          setTools((current) =>
                            current.map((item) => (item.key === tool.key ? { ...item, agents } : item)),
                          );
                        }, "Tool binding saved");
                      }}
                    />
                    {tool.key} · {tool.enabled ? "Enabled" : "Disabled"}
                  </label>
                  <p>{tool.description}</p>
                  {gated && <small>Enable workspace traffic capture to change this binding.</small>}
                </article>
              );
            })}
        </section>
      )}
      {tab === "Triggers" && !agent.builtin && (
        <AgentTriggers agent={agent} tools={tools} changed={load} failure={failure} />
      )}
      {identity && (
        <IdentityEditor
          agent={agent}
          close={() => showIdentity(false)}
          saved={async () => {
            await load();
            onSaved?.();
          }}
        />
      )}
    </div>
  );
}

export function AgentsWorkspace({ onUnauthorized }: { onUnauthorized?: () => void }) {
  const [items, setItems] = useState<Agent[]>([]),
    [selected, choose] = useState<string>(),
    [query, search] = useState(""),
    [creating, create] = useState(false),
    [deleting, confirm] = useState<Agent>(),
    [busy, pending] = useState(false),
    [error, fail] = useState("");
  const failure = useCallback(
    (error: unknown) => {
      if (error instanceof ApiFailure && error.status === 401) onUnauthorized?.();
      fail(error instanceof Error ? error.message : String(error));
    },
    [onUnauthorized],
  );
  const load = useCallback(async () => {
    const response = await consoleRequest<{ agents: Agent[] }>("/agents");
    setItems(response.agents || []);
  }, []);
  useEffect(() => {
    void load().catch(failure);
  }, [load, failure]);
  if (selected)
    return (
      <>
        <button
          className="astra-agent-back"
          onClick={() => {
            choose(undefined);
            void load().catch(failure);
          }}
        >
          Back to agents
        </button>
        <AgentBlueprint key={selected} agentKey={selected} onUnauthorized={onUnauthorized} />
      </>
    );
  return (
    <div className="astra-model-workspace astra-agent-workspace">
      <header className="astra-model-header">
        <div>
          <small>Runtime and capability control</small>
          <h1>Agents</h1>
          <p>Configure prompts, execution budgets and access.</p>
        </div>
        <div>
          <button disabled={busy} onClick={() => void load().catch(failure)}>
            Refresh agents
          </button>
          <button className="is-primary" disabled={busy} onClick={() => create(true)}>
            New agent
          </button>
        </div>
      </header>
      {error && (
        <p role="alert" className="astra-form-error">
          {error}
        </p>
      )}
      <label>
        Search agents
        <input type="search" value={query} onChange={(event) => search(event.target.value)} />
      </label>
      <div className="astra-model-cards">
        {items
          .filter((agent) =>
            [agent.name, agent.key, agent.description || ""].some((value) =>
              value.toLocaleLowerCase().includes(query.toLocaleLowerCase()),
            ),
          )
          .map((agent) => (
            <article key={agent.key}>
              <header>
                <button onClick={() => choose(agent.key)}>{agent.name}</button>
                <small>{agent.key}</small>
              </header>
              <p>{agent.description || agent.role}</p>
              <p>
                {agent.builtin ? "Built-in" : "Custom"} · {agent.enabled ? "Ready" : "Disabled"}
              </p>
              <p>
                {agent.mcp_count || 0} MCP · {agent.skill_count || 0} skills · {agent.tool_count || 0} tools
              </p>
              <footer>
                <button onClick={() => choose(agent.key)}>Configure agent</button>
                <a href={`/system/agents/detail?key=${encodeURIComponent(agent.key)}`}>Open agent page</a>
                {!agent.builtin && <button onClick={() => confirm(agent)}>Delete agent</button>}
              </footer>
            </article>
          ))}
      </div>
      {creating && (
        <IdentityEditor
          close={() => create(false)}
          saved={async (key) => {
            await load();
            choose(key);
          }}
        />
      )}
      {deleting && (
        <EditorDialog
          title={`Delete ${deleting.name}`}
          eyebrow="Agent deletion"
          busy={busy}
          close={() => confirm(undefined)}
        >
          <p>Remove this custom agent, its prompts, variables, visibility and tool bindings?</p>
          <div className="astra-form-actions">
            <button
              disabled={busy}
              onClick={() => {
                pending(true);
                void consoleRequest(`/agents/${encodeURIComponent(deleting.key)}`, "DELETE")
                  .then(async () => {
                    confirm(undefined);
                    await load();
                  }, failure)
                  .finally(() => pending(false));
              }}
            >
              Confirm agent deletion
            </button>
            <button disabled={busy} onClick={() => confirm(undefined)}>
              Keep agent
            </button>
          </div>
        </EditorDialog>
      )}
    </div>
  );
}
