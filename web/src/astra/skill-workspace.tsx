"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { Agent, MCPServer, MissingSkill, SkillCall, SkillItem } from "@/lib/types";
import { ApiFailure, collection } from "./client";
import { consoleRequest, consoleSkillUpload } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import { FormFields } from "./form-fields";
import type { FormValues } from "./forms";
import { OrderedChoices } from "./operation-editor";
import {
  skillCreateDocument,
  skillDraft,
  skillEndpoint,
  skillFileEndpoint,
  skillForm,
  skillListedPaths,
  skillMetadataDocument,
  skillSummary,
  skillTree,
  validateSkillPath,
  type SkillEntry,
} from "./skill-document";
import "./forms.css";
import "./skill-workspace.css";

type FileDraft = { saved: string; text: string };
type Removal = { skill: string; path?: string };
type Editor = { kind: "create" | "metadata"; draft: FormValues; original: FormValues; grants: string[] };
const draftKey = (skill: string, path: string) => JSON.stringify([skill, path]);
const time = (value?: string) => (value ? new Date(value).toLocaleString() : "Never called");

function SkillFiles({
  entries,
  selected,
  select,
}: {
  entries: SkillEntry[];
  selected?: string;
  select: (path: string) => void;
}) {
  return (
    <ul className="astra-skill-tree">
      {entries.map((entry) => (
        <li key={entry.path}>
          {entry.directory ? (
            <details>
              <summary>{entry.name}/</summary>
              <SkillFiles entries={entry.children} selected={selected} select={select} />
            </details>
          ) : (
            <button
              type="button"
              title={entry.path}
              aria-pressed={entry.path === selected}
              onClick={() => select(entry.path)}
            >
              {entry.name}
            </button>
          )}
        </li>
      ))}
    </ul>
  );
}

function SkillUsage({ calls, more, busy }: { calls: SkillCall[]; more: () => void; busy: boolean }) {
  return (
    <section className="astra-model-section">
      <h3>Recent skill calls</h3>
      <div className="astra-table-scroll">
        <table className="astra-table">
          <thead>
            <tr>
              <th>Time</th>
              <th>Agent</th>
              <th>Context</th>
              <th>Arguments</th>
            </tr>
          </thead>
          <tbody>
            {calls.map((call, index) => (
              <tr key={`${call.ts}-${index}`}>
                <td>{time(call.ts)}</td>
                <td>{call.agent_key}</td>
                <td>
                  {call.task_id ? (
                    <a href={`/function/tasks/detail?id=${call.task_id}`}>Task {call.task_id}</a>
                  ) : (
                    `Chat ${call.session_id || "—"}`
                  )}
                </td>
                <td>{call.args_len} characters</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {!calls.length && <p>No recorded calls.</p>}
      <button type="button" disabled={busy} onClick={more}>
        Load more calls
      </button>
    </section>
  );
}

function SkillDetail({
  skill,
  agents,
  connections,
  busy,
  run,
  refresh,
  drafts,
  changeDraft,
  onRemove,
  onMetadata,
  onUnauthorized,
}: {
  skill: SkillItem;
  agents: Agent[];
  connections: MCPServer[];
  busy: boolean;
  run: (work: () => Promise<void>, message: string) => Promise<boolean>;
  refresh: () => Promise<void>;
  drafts: ReadonlyMap<string, FileDraft>;
  changeDraft: (key: string, value?: FileDraft) => void;
  onRemove: (value: Removal) => void;
  onMetadata: (skill: SkillItem) => void;
  onUnauthorized?: () => void;
}) {
  const [path, setPath] = useState<string>();
  const [files, setFiles] = useState(skillListedPaths(skill.files || []));
  const [grants, setGrants] = useState<string[]>();
  const [calls, setCalls] = useState<SkillCall[]>([]);
  const [limit, setLimit] = useState(20);
  const [newPath, setNewPath] = useState("");
  const [kind, setKind] = useState("file");
  const [loadingFile, setLoadingFile] = useState(false);
  const [failure, setFailure] = useState("");
  const [revision, setRevision] = useState(0);
  const selectedKey = path ? draftKey(skill.name, path) : "";
  const current = drafts.get(selectedKey);
  const pending = [...drafts.entries()].filter(
    ([key, draft]) => JSON.parse(key)[0] === skill.name && draft.saved !== draft.text,
  );
  const instructionDraft = drafts.get(draftKey(skill.name, "SKILL.md"));
  const dirtyInstructions = instructionDraft && instructionDraft.text !== instructionDraft.saved;

  useEffect(() => {
    let active = true;
    void Promise.allSettled([
      consoleRequest<{ files: string[] }>(skillEndpoint(skill.name, "/files")),
      consoleRequest<{ agents: string[] }>(`/visibility/skill/${encodeURIComponent(skill.name)}`),
      consoleRequest<{ calls: SkillCall[] }>(skillEndpoint(skill.name, `/usage?limit=${limit}`)),
    ]).then((results) => {
      if (!active) return;
      if (results[0].status === "fulfilled") setFiles(skillListedPaths(results[0].value.files || []));
      if (results[1].status === "fulfilled") setGrants(results[1].value.agents || []);
      if (results[2].status === "fulfilled") setCalls(results[2].value.calls || []);
      const errors = results.flatMap((result, index) =>
        result.status === "rejected" ? [`${["Files", "Permissions", "Usage"][index]}: ${String(result.reason)}`] : [],
      );
      if (
        results.some(
          (result) =>
            result.status === "rejected" && result.reason instanceof ApiFailure && result.reason.status === 401,
        )
      )
        onUnauthorized?.();
      setFailure(errors.join("; "));
    });
    return () => {
      active = false;
    };
  }, [skill.name, skill.files, limit, revision, onUnauthorized]);

  useEffect(() => {
    let active = true;
    setLoadingFile(false);
    if (!path || drafts.has(draftKey(skill.name, path))) return;
    setLoadingFile(true);
    void Promise.resolve()
      .then(() => consoleRequest<{ content: string }>(skillFileEndpoint(skill.name, path)))
      .then((result) => {
        if (!active) return;
        changeDraft(draftKey(skill.name, path), { text: result.content, saved: result.content });
      })
      .catch((error) => {
        if (active) {
          setFailure(String(error));
          if (error instanceof ApiFailure && error.status === 401) onUnauthorized?.();
        }
      })
      .finally(() => {
        if (active) setLoadingFile(false);
      });
    return () => {
      active = false;
    };
  }, [skill.name, path, drafts, changeDraft, revision, onUnauthorized]);

  async function reloadFile() {
    if (!path) return;
    await run(async () => {
      const result = await consoleRequest<{ content: string }>(skillFileEndpoint(skill.name, path));
      changeDraft(selectedKey, { text: result.content, saved: result.content });
    }, "File reloaded; its draft was discarded");
  }

  return (
    <article className="astra-skill-detail">
      <header className="astra-model-header">
        <div>
          <small>Installed skill</small>
          <h2>{skill.name}</h2>
          <p>{skill.description || "No description"}</p>
        </div>
        <div>
          <button disabled={busy || Boolean(dirtyInstructions)} onClick={() => onMetadata(skill)}>
            Edit skill metadata
          </button>
          <button disabled={busy} onClick={() => onRemove({ skill: skill.name })}>
            Delete skill
          </button>
        </div>
      </header>
      {dirtyInstructions && (
        <p className="astra-form-note">Save or discard the SKILL.md draft before editing its metadata.</p>
      )}
      {failure && (
        <p role="alert" className="astra-form-error">
          {failure}
        </p>
      )}
      <dl className="astra-skill-facts">
        <div>
          <dt>Calls</dt>
          <dd>{skill.calls || 0}</dd>
        </div>
        <div>
          <dt>Tasks</dt>
          <dd>{skill.tasks || 0}</dd>
        </div>
        <div>
          <dt>Last used</dt>
          <dd>{time(skill.last_used)}</dd>
        </div>
        <div>
          <dt>Usage agents</dt>
          <dd>{(skill.usage_agents || []).join(", ") || "—"}</dd>
        </div>
        <div>
          <dt>License</dt>
          <dd>{skill.license || "—"}</dd>
        </div>
        <div>
          <dt>Compatibility</dt>
          <dd>{skill.compatibility || "—"}</dd>
        </div>
      </dl>
      <section className="astra-model-section">
        <h3>MCP access on load</h3>
        <p>{(skill.mcps || []).join(", ") || "No skill-specific MCP connections"}</p>
        {(skill.mcps || [])
          .filter((name) => !connections.some((connection) => connection.name === name))
          .map((name) => (
            <p className="astra-form-note" key={name}>
              Unavailable connection: {name}. The stored reference is retained.
            </p>
          ))}
      </section>
      <section className="astra-model-section">
        <h3>Agent visibility</h3>
        <p>Changes apply separately to each agent. Existing hidden or unavailable agent grants are retained.</p>
        {grants === undefined ? (
          <p>Permissions have not loaded. Reload the skill to retry.</p>
        ) : (
          <div className="astra-skill-grants">
            {agents.map((agent) => (
              <label key={agent.id}>
                <input
                  type="checkbox"
                  aria-label={`Allow skill for ${agent.name}`}
                  checked={grants.includes(agent.id)}
                  disabled={busy}
                  onChange={(event) => {
                    const visible = event.target.checked;
                    void run(async () => {
                      await consoleRequest("/visibility/skill/toggle", "POST", {
                        agent_id: agent.id,
                        skill_name: skill.name,
                        visible,
                      });
                      setGrants((previous) =>
                        visible
                          ? [...(previous || []).filter((id) => id !== agent.id), agent.id]
                          : (previous || []).filter((id) => id !== agent.id),
                      );
                    }, "Skill permission saved");
                  }}
                />
                {agent.name}
              </label>
            ))}
          </div>
        )}
        <button disabled={busy} onClick={() => setRevision((value) => value + 1)}>
          Reload skill details
        </button>
      </section>
      <section className="astra-model-section">
        <header className="astra-agent-section-header">
          <h3>Skill files</h3>
          <span>{pending.length} unsaved drafts</span>
        </header>
        {pending.length > 0 && (
          <p className="astra-form-note">Drafts remain available when you switch files or skills in this workspace.</p>
        )}
        <form
          className="astra-operation-toolbar"
          onSubmit={(event) => {
            event.preventDefault();
            void run(async () => {
              const normalized = validateSkillPath(newPath);
              if (files.some((file) => file.replace(/\/$/, "") === normalized))
                throw new Error("That file or directory already exists. Select it to edit.");
              if (kind === "directory")
                await consoleRequest(skillEndpoint(skill.name, "/dirs"), "POST", { path: normalized });
              else {
                await consoleRequest(skillFileEndpoint(skill.name, normalized), "PUT", { content: "" });
                changeDraft(draftKey(skill.name, normalized), { text: "", saved: "" });
                setPath(normalized);
              }
              setNewPath("");
              setRevision((value) => value + 1);
              await refresh();
            }, "Skill entry created");
          }}
        >
          <label>
            New path
            <input
              name="skill_new_path"
              value={newPath}
              onChange={(event) => setNewPath(event.target.value)}
              disabled={busy}
              required
              placeholder="references/notes.md"
            />
          </label>
          <label>
            Entry type
            <select
              name="skill_entry_kind"
              value={kind}
              disabled={busy}
              onChange={(event) => setKind(event.target.value)}
            >
              <option value="file">File</option>
              <option value="directory">Directory</option>
            </select>
          </label>
          <button disabled={busy}>Create skill entry</button>
        </form>
        <div className="astra-skill-files">
          <nav aria-label="Skill files">
            <SkillFiles entries={skillTree(files)} selected={path} select={setPath} />
            <details>
              <summary>Remove a file or directory</summary>
              {files.map((file) => (
                <button
                  key={file}
                  disabled={busy}
                  title={`Delete ${file}`}
                  onClick={() => onRemove({ skill: skill.name, path: file.replace(/\/$/, "") })}
                >
                  Delete {file}
                </button>
              ))}
            </details>
          </nav>
          <div className="astra-skill-file-editor">
            {path ? (
              <>
                <header>
                  <h4>{path}</h4>
                  {current && current.saved !== current.text && <span>Unsaved</span>}
                </header>
                {loadingFile ? (
                  <p>Loading file…</p>
                ) : current ? (
                  <>
                    <label>
                      File content
                      <textarea
                        name="skill_file_content"
                        rows={20}
                        spellCheck={false}
                        value={current.text}
                        disabled={busy}
                        onChange={(event) => {
                          changeDraft(selectedKey, { ...current, text: event.target.value });
                        }}
                      />
                    </label>
                    <div className="astra-operation-toolbar">
                      <button
                        disabled={busy || current.saved === current.text}
                        onClick={() =>
                          void run(async () => {
                            const text = current.text;
                            await consoleRequest(skillFileEndpoint(skill.name, path), "PUT", { content: text });
                            changeDraft(selectedKey, { text, saved: text });
                            await refresh();
                          }, "Skill file saved")
                        }
                      >
                        Save skill file
                      </button>
                      <button
                        disabled={busy}
                        onClick={() => {
                          if (
                            current.saved === current.text ||
                            window.confirm("Discard this file draft and reload the saved file?")
                          )
                            void reloadFile();
                        }}
                      >
                        Reload saved file
                      </button>
                      <button disabled={busy} onClick={() => onRemove({ skill: skill.name, path })}>
                        Delete selected file
                      </button>
                    </div>
                  </>
                ) : (
                  <p>The file could not be loaded. Choose another file or reload the skill.</p>
                )}
              </>
            ) : (
              <p>Select a file to edit. Empty directories are shown in the tree.</p>
            )}
          </div>
        </div>
      </section>
      <SkillUsage calls={calls} more={() => setLimit((value) => value + 20)} busy={busy} />
    </article>
  );
}

export function SkillsWorkspace({ onUnauthorized }: { onUnauthorized?: () => void }) {
  const [skills, setSkills] = useState<SkillItem[]>([]),
    [agents, setAgents] = useState<Agent[]>([]),
    [connections, setConnections] = useState<MCPServer[]>([]),
    [missing, setMissing] = useState<MissingSkill[]>([]);
  const [selected, setSelected] = useState<string>(),
    [query, setQuery] = useState("");
  const [editor, setEditor] = useState<Editor>(),
    [removal, setRemoval] = useState<Removal>(),
    [replacement, setReplacement] = useState<File>();
  const [busy, setBusy] = useState(false),
    [loading, setLoading] = useState(true),
    [error, setError] = useState(""),
    [notice, setNotice] = useState(""),
    [metadataError, setMetadataError] = useState("");
  const [detailRevision, setDetailRevision] = useState(0);
  const [drafts, setDrafts] = useState(new Map<string, FileDraft>());
  const changeDraft = useCallback(
    (key: string, value?: FileDraft) =>
      setDrafts((previous) => {
        const next = new Map(previous);
        if (value) next.set(key, value);
        else next.delete(key);
        return next;
      }),
    [],
  );
  const alive = useRef(true),
    sequence = useRef(0),
    actionLock = useRef(false);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
      sequence.current++;
    };
  }, []);

  const refresh = useCallback(async () => {
    const request = ++sequence.current;
    setLoading(true);
    const results = await Promise.allSettled([
      consoleRequest<unknown>("/skills"),
      consoleRequest<unknown>("/agents"),
      consoleRequest<unknown>("/mcp"),
      consoleRequest<{ missing: MissingSkill[] }>("/skills/missing?limit=20"),
    ]);
    if (!alive.current || request !== sequence.current) return;
    const main = results[0];
    if (main.status === "fulfilled") {
      setSkills(
        (collection(main.value, ["skills"]) as unknown as SkillItem[]).map((skill) => ({
          ...skill,
          files: skillListedPaths(skill.files || []),
        })),
      );
      setError("");
    } else {
      setError(String(main.reason));
    }
    if (results[1].status === "fulfilled") setAgents(collection(results[1].value, ["agents"]) as unknown as Agent[]);
    if (results[2].status === "fulfilled")
      setConnections(collection(results[2].value, ["servers"]) as unknown as MCPServer[]);
    if (results[3].status === "fulfilled") setMissing(results[3].value.missing || []);
    setMetadataError(
      results
        .flatMap((result, index) =>
          result.status === "rejected" && index
            ? [`${["", "Agents", "MCP", "Missing skills"][index]}: ${String(result.reason)}`]
            : [],
        )
        .join("; "),
    );
    if (
      results.some(
        (result) => result.status === "rejected" && result.reason instanceof ApiFailure && result.reason.status === 401,
      )
    )
      onUnauthorized?.();
    setLoading(false);
  }, [onUnauthorized]);
  useEffect(() => {
    void refresh();
  }, [refresh]);
  async function run(work: () => Promise<void>, message: string) {
    if (actionLock.current) return false;
    actionLock.current = true;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      await work();
      if (alive.current) setNotice(message);
      return true;
    } catch (failure) {
      if (alive.current) {
        setError(String(failure));
        if (failure instanceof ApiFailure && failure.status === 401) onUnauthorized?.();
      }
      return false;
    } finally {
      actionLock.current = false;
      if (alive.current) setBusy(false);
    }
  }
  function clearSkillDrafts(name: string, path?: string) {
    setDrafts(
      (previous) =>
        new Map(
          [...previous].filter(([key]) => {
            const [owner, file] = JSON.parse(key) as string[];
            return owner !== name || Boolean(path && file !== path && !file.startsWith(`${path}/`));
          }),
        ),
    );
  }
  async function upload(file: File, overwrite: boolean) {
    let completed = false;
    const succeeded = await run(async () => {
      try {
        const result = await consoleSkillUpload(file, overwrite);
        clearSkillDrafts(result.name);
        setReplacement(undefined);
        setSelected(result.name);
        setDetailRevision((value) => value + 1);
        await refresh();
        completed = true;
      } catch (failure) {
        if (!overwrite && failure instanceof Error && /already exists/i.test(failure.message)) {
          setReplacement(file);
          return;
        }
        throw failure;
      }
    }, "");
    if (succeeded && completed) setNotice(overwrite ? "Skill archive replaced" : "Skill archive imported");
  }
  const selectedSkill = skills.find((skill) => skill.name === selected);
  const summary = skillSummary(skills);
  return (
    <div className="astra-model-workspace astra-skill-workspace">
      <header className="astra-model-header">
        <div>
          <small>Procedures and reference files</small>
          <h1>Skills</h1>
          <p>Manage instructions, capability access and their recorded use.</p>
        </div>
        <div>
          <button
            className="is-primary"
            disabled={busy || loading}
            onClick={() => {
              const draft = skillDraft();
              setEditor({ kind: "create", original: draft, draft, grants: [] });
            }}
          >
            New skill
          </button>
          <label className="astra-skill-upload">
            Upload ZIP
            <input
              type="file"
              name="skill_archive"
              accept=".zip,application/zip"
              disabled={busy}
              onChange={(event) => {
                const file = event.target.files?.[0];
                event.target.value = "";
                if (file) void upload(file, false);
              }}
            />
          </label>
          <button disabled={busy || loading} onClick={() => void refresh()}>
            Refresh skills
          </button>
        </div>
      </header>
      {error && (
        <p role="alert" className="astra-form-error">
          {error}
        </p>
      )}
      {metadataError && (
        <p role="alert" className="astra-form-error">
          Some supporting data is unavailable: {metadataError}
        </p>
      )}
      {notice && (
        <p role="status" className="astra-form-note">
          {notice}
        </p>
      )}
      <div className="astra-skill-library">
        <aside>
          <label>
            Find a skill
            <input name="skill_query" value={query} onChange={(event) => setQuery(event.target.value)} />
          </label>
          <button onClick={() => setSelected(undefined)}>Library overview</button>
          <nav aria-label="Installed skills">
            {skills
              .filter((skill) =>
                `${skill.name} ${skill.description || ""}`.toLocaleLowerCase().includes(query.toLocaleLowerCase()),
              )
              .map((skill) => (
                <button key={skill.name} aria-pressed={skill.name === selected} onClick={() => setSelected(skill.name)}>
                  <strong>{skill.name}</strong>
                  <span>{skill.calls || 0} calls</span>
                </button>
              ))}
          </nav>
          {loading && <p>Loading skills…</p>}
          {!loading && !skills.length && <p>No installed skills.</p>}
        </aside>
        <div>
          {selectedSkill ? (
            <SkillDetail
              key={`${selectedSkill.name}-${detailRevision}`}
              skill={selectedSkill}
              agents={agents}
              connections={connections}
              busy={busy}
              run={run}
              refresh={refresh}
              drafts={drafts}
              changeDraft={changeDraft}
              onRemove={setRemoval}
              onUnauthorized={onUnauthorized}
              onMetadata={(skill) => {
                const draft = skillDraft(skill);
                setEditor({ kind: "metadata", original: draft, draft, grants: [] });
              }}
            />
          ) : (
            <section className="astra-model-section">
              <h2>Skill library overview</h2>
              <p>
                {skills.length} skills · {summary.calls} calls · {summary.unused.length} unused ·{" "}
                {missing.reduce((count, item) => count + item.calls, 0)} missed calls
              </p>
              <h3>Call ranking</h3>
              {summary.ranked.map((skill) => (
                <button key={skill.name} onClick={() => setSelected(skill.name)}>
                  {skill.name} · {skill.calls} calls
                </button>
              ))}
              {!summary.ranked.length && <p>No recorded skill use.</p>}
              <h3>Recently used</h3>
              {summary.recent.map((skill) => (
                <p key={skill.name}>
                  <button onClick={() => setSelected(skill.name)}>{skill.name}</button> {time(skill.last_used)}
                </p>
              ))}
              <h3>Unused skills</h3>
              {summary.unused.map((skill) => (
                <button key={skill.name} onClick={() => setSelected(skill.name)}>
                  {skill.name}
                </button>
              ))}
              <h3>Requested but missing</h3>
              {missing.map((item) => (
                <p key={item.skill}>
                  <code>{item.skill}</code> · {item.calls} calls · {item.agents.join(", ")} · {time(item.last_used)}
                </p>
              ))}
              {!missing.length && <p>No missing skills recorded.</p>}
            </section>
          )}
        </div>
      </div>
      {editor && (
        <EditorDialog
          eyebrow="Skill"
          title={editor.kind === "create" ? "Create skill" : `Edit ${selectedSkill?.name || "skill"}`}
          busy={busy}
          close={() => setEditor(undefined)}
        >
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void run(
                async () => {
                  if (editor.kind === "create") {
                    const document = skillCreateDocument(editor.draft);
                    const result = await consoleRequest<{ name: string }>("/skills", "POST", document);
                    // Creation succeeded even if a later grant fails. Close creation now so
                    // retrying permissions cannot accidentally create the same skill twice.
                    setEditor(undefined);
                    setSelected(result.name);
                    const outcomes = await Promise.allSettled(
                      editor.grants.map((id) =>
                        consoleRequest("/visibility/skill/toggle", "POST", {
                          agent_id: id,
                          skill_name: result.name,
                          visible: true,
                        }),
                      ),
                    );
                    const failed = outcomes.flatMap((outcome, index) =>
                      outcome.status === "rejected" ? [`${editor.grants[index]}: ${String(outcome.reason)}`] : [],
                    );
                    await refresh();
                    if (failed.length)
                      throw new Error(
                        `Skill created. Some initial agent permissions failed; retry them in Agent visibility: ${failed.join("; ")}`,
                      );
                  } else {
                    const name = String(editor.original.name);
                    await consoleRequest(
                      skillEndpoint(name, "/meta"),
                      "PUT",
                      skillMetadataDocument(editor.original, editor.draft),
                    );
                    changeDraft(draftKey(name, "SKILL.md"));
                    setEditor(undefined);
                    setDetailRevision((value) => value + 1);
                    await refresh();
                  }
                },
                editor.kind === "create" ? "Skill created" : "Skill metadata saved",
              );
            }}
          >
            <FormFields
              groups={skillForm(editor.kind === "create")}
              value={editor.draft}
              onChange={(draft) => setEditor({ ...editor, draft })}
              disabled={busy}
            />
            <OrderedChoices
              title="Skill MCP connections"
              value={Array.isArray(editor.draft.mcps) ? (editor.draft.mcps as string[]) : []}
              choices={connections.map((connection) => ({ id: connection.name, label: connection.name }))}
              change={(mcps) => setEditor({ ...editor, draft: { ...editor.draft, mcps } })}
              disabled={busy}
            />
            {editor.kind === "create" && (
              <OrderedChoices
                title="Initial skill agents"
                value={editor.grants}
                choices={agents.map((agent) => ({ id: agent.id, label: agent.name }))}
                change={(grants) => setEditor({ ...editor, grants })}
                disabled={busy}
              />
            )}
            {error && (
              <p role="alert" className="astra-form-error">
                {error}
              </p>
            )}
            <footer className="astra-form-actions">
              <button type="button" disabled={busy} onClick={() => setEditor(undefined)}>
                Cancel
              </button>
              <button disabled={busy}>{editor.kind === "create" ? "Create skill" : "Save skill metadata"}</button>
            </footer>
          </form>
        </EditorDialog>
      )}
      {removal && (
        <EditorDialog
          eyebrow="Skill removal"
          title={removal.path ? `Delete ${removal.path}` : `Delete ${removal.skill}`}
          busy={busy}
          close={() => setRemoval(undefined)}
        >
          <p>
            {removal.path
              ? "This removes the selected file or directory and its contents."
              : "This removes the skill directory and its agent visibility grants."}{" "}
            Local drafts for the removed paths will also be discarded.
          </p>
          {error && (
            <p role="alert" className="astra-form-error">
              {error}
            </p>
          )}
          <div className="astra-form-actions">
            <button disabled={busy} onClick={() => setRemoval(undefined)}>
              Cancel
            </button>
            <button
              disabled={busy}
              onClick={() =>
                void run(async () => {
                  await consoleRequest(
                    removal.path ? skillFileEndpoint(removal.skill, removal.path) : skillEndpoint(removal.skill),
                    "DELETE",
                  );
                  clearSkillDrafts(removal.skill, removal.path);
                  if (!removal.path) setSelected(undefined);
                  setRemoval(undefined);
                  setDetailRevision((value) => value + 1);
                  await refresh();
                }, "Skill content deleted")
              }
            >
              Confirm skill deletion
            </button>
          </div>
        </EditorDialog>
      )}
      {replacement && (
        <EditorDialog
          eyebrow="Skill archive"
          title="Replace existing skill?"
          busy={busy}
          close={() => setReplacement(undefined)}
        >
          <p>
            {replacement.name} contains an existing skill. Replacing it overwrites its files and discards local drafts
            for that skill.
          </p>
          {error && (
            <p role="alert" className="astra-form-error">
              {error}
            </p>
          )}
          <div className="astra-form-actions">
            <button disabled={busy} onClick={() => setReplacement(undefined)}>
              Cancel
            </button>
            <button disabled={busy} onClick={() => void upload(replacement, true)}>
              Replace skill archive
            </button>
          </div>
        </EditorDialog>
      )}
    </div>
  );
}
