"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { ArchiveBatchItem, Task, TaskArchive, TaskCategory } from "@/lib/types";
import { ApiFailure } from "./client";
import { consoleRequest } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import { OperationEditor } from "./operation-editor";
import {
  CategoriesDialog,
  ConcurrencyDialog,
  DeleteOperationsDialog,
  MoveOperationsDialog,
  OutcomeList,
} from "./operation-controls";
import { OperationArchives } from "./operation-archives";
import {
  batchOutcomes,
  elapsed,
  initialOperationView,
  operationArchiveBlock,
  operationControl,
  operationPinned,
  operationPreferences,
  operationSeconds,
  operationSlice,
  operationStates,
  operationTitle,
  type ItemOutcome,
  type OperationView,
} from "./operations-domain";
import "./forms.css";
import "./operations.css";

type Dialog = {
  kind: "create" | "categories" | "concurrency" | "delete" | "move" | "archive" | "rename";
  ids: string[];
  category?: string;
};
const preferenceKey = "astra.operations.view";

export function OperationsWorkspace({
  onOpen = (id) => {
    window.location.href = `/function/tasks/detail?id=${encodeURIComponent(id)}`;
  },
  onUnauthorized,
}: {
  onOpen?: (id: string) => void;
  onUnauthorized?: () => void;
}) {
  const [tasks, setTasks] = useState<Task[]>([]),
    [categories, setCategories] = useState<TaskCategory[]>([]),
    [view, setView] = useState(initialOperationView),
    [hydrated, hydrate] = useState(false),
    [page, setPage] = useState(1);
  const [selected, select] = useState(new Set<string>()),
    [dialog, open] = useState<Dialog>(),
    [busy, pending] = useState(false),
    [loading, setLoading] = useState(true),
    [error, fail] = useState(""),
    [outcomes, report] = useState<ItemOutcome[]>([]),
    [archives, showArchives] = useState(false),
    [now, setNow] = useState(Date.now() / 1000),
    [rename, setRename] = useState("");
  const sequence = useRef(0);
  const failure = useCallback(
    (error: unknown) => {
      if (error instanceof ApiFailure && error.status === 401) onUnauthorized?.();
      fail(error instanceof Error ? error.message : String(error));
    },
    [onUnauthorized],
  );
  const load = useCallback(async () => {
    const request = ++sequence.current;
    const results = await Promise.allSettled([
      consoleRequest<{ tasks: Task[] }>("/tasks"),
      consoleRequest<{ categories: TaskCategory[] }>("/task-categories"),
    ]);
    if (request !== sequence.current) return;
    if (results[0].status === "fulfilled") {
      const items = results[0].value.tasks || [];
      setTasks(items);
      select((current) => new Set([...current].filter((id) => items.some((task) => String(task.id) === id))));
    } else failure(results[0].reason);
    if (results[1].status === "fulfilled") setCategories(results[1].value.categories || []);
    else failure(results[1].reason);
    setLoading(false);
  }, [failure]);
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 10000),
      clock = setInterval(() => setNow(Date.now() / 1000), 1000);
    return () => {
      sequence.current++;
      clearInterval(timer);
      clearInterval(clock);
    };
  }, [load]);
  useEffect(() => {
    try {
      setView(operationPreferences(JSON.parse(localStorage.getItem(preferenceKey) || "null")));
    } catch {
      /* Invalid preferences do not affect server data. */
    }
    hydrate(true);
  }, []);
  useEffect(() => {
    if (hydrated)
      try {
        localStorage.setItem(preferenceKey, JSON.stringify(view));
      } catch {
        /* Storage may be unavailable in private sessions. */
      }
  }, [hydrated, view]);
  const ordered = operationSlice(tasks, view, now),
    pages = Math.max(1, Math.ceil(ordered.length / view.size)),
    currentPage = Math.min(page, pages),
    visible = ordered.slice((currentPage - 1) * view.size, currentPage * view.size),
    chosen = tasks.filter((task) => selected.has(String(task.id)));
  function configure(patch: Partial<OperationView>) {
    setView((current) => ({ ...current, ...patch }));
    setPage(1);
  }
  function toggle(id: string) {
    select((current) => {
      const next = new Set(current);
      next.has(id) ? next.delete(id) : next.add(id);
      return next;
    });
  }
  async function action(work: () => Promise<void>) {
    pending(true);
    fail("");
    report([]);
    try {
      await work();
    } catch (error) {
      failure(error);
    } finally {
      pending(false);
    }
  }
  async function mutate(task: Task, patch: object) {
    await action(async () => {
      await consoleRequest(`/tasks/${encodeURIComponent(task.id)}`, "PATCH", patch);
      await load();
    });
  }
  function batchLimit(ids: string[]) {
    if (ids.length > 100) throw new Error("Select at most 100 operations for this batch action.");
  }
  async function control(ids: string[], operation: "pause" | "resume") {
    await action(async () => {
      batchLimit(ids);
      if (ids.length === 1) {
        await consoleRequest(`/tasks/${encodeURIComponent(ids[0])}/control`, "POST", { action: operation });
        report([{ id: ids[0], ok: true }]);
      } else {
        const result = await consoleRequest<{ items: ItemOutcome[] }>("/tasks/control/batch", "POST", {
          task_ids: ids,
          action: operation,
        });
        report(batchOutcomes(ids, result.items));
      }
      await load();
    });
  }
  async function move(ids: string[], category: number | null) {
    batchLimit(ids);
    fail("");
    if (ids.length === 1) {
      await consoleRequest(`/tasks/${encodeURIComponent(ids[0])}/category`, "PATCH", { category_id: category });
      report([{ id: ids[0], ok: true }]);
    } else {
      const result = await consoleRequest<{ items: ItemOutcome[] }>("/tasks/category/batch", "POST", {
        task_ids: ids,
        category_id: category,
      });
      const items = batchOutcomes(ids, result.items);
      report(items);
      select(new Set(items.filter((item) => !item.ok).map((item) => item.id)));
    }
    await load();
  }
  async function archive(ids: string[]) {
    await action(async () => {
      batchLimit(ids);
      const result =
        ids.length === 1
          ? {
              items: [
                {
                  id: ids[0],
                  ok: true,
                  queued: true,
                  archive_id: (
                    await consoleRequest<TaskArchive>(`/tasks/${encodeURIComponent(ids[0])}/archive`, "POST")
                  ).id,
                },
              ],
            }
          : await consoleRequest<{ items: ArchiveBatchItem[] }>("/tasks/archive/batch", "POST", { task_ids: ids });
      report(batchOutcomes(ids, result.items));
      open(undefined);
      showArchives(true);
      await load();
    });
  }
  const actionIDs = dialog?.ids || [];
  function itemActions(task: Task) {
    const id = String(task.id),
      operation = operationControl(task),
      block = operationArchiveBlock(task);
    return (
      <div className="astra-operation-actions">
        <button
          type="button"
          disabled={busy}
          onClick={() => {
            setRename(task.name || "");
            open({ kind: "rename", ids: [id] });
          }}
          aria-label={`Rename operation ${id}`}
        >
          Rename
        </button>
        <button type="button" disabled={busy} onClick={() => void mutate(task, { pinned: !operationPinned(task) })}>
          {operationPinned(task) ? "Unpin" : "Pin"}
        </button>
        {operation && (
          <button type="button" disabled={busy} onClick={() => void control([id], operation)}>
            {operation === "pause" ? "Pause" : "Resume"}
          </button>
        )}
        <button
          type="button"
          disabled={busy}
          onClick={() => open({ kind: "move", ids: [id], category: task.category_id ? String(task.category_id) : "" })}
        >
          Move
        </button>
        <button
          type="button"
          disabled={busy || Boolean(block)}
          title={block || "Move this operation into cold storage"}
          onClick={() => open({ kind: "archive", ids: [id] })}
        >
          Archive
        </button>
        <button type="button" disabled={busy} onClick={() => open({ kind: "delete", ids: [id] })}>
          Delete
        </button>
        <a href={`/function/tasks/detail?id=${encodeURIComponent(id)}`}>Full workspace</a>
      </div>
    );
  }
  function identity(task: Task) {
    return (
      <>
        <label className="astra-operation-check">
          <input
            type="checkbox"
            aria-label={`Select operation ${task.id}`}
            checked={selected.has(String(task.id))}
            onChange={() => toggle(String(task.id))}
          />
          <span>
            #{task.id}
            {operationPinned(task) ? " · Pinned" : ""}
          </span>
        </label>
        <button type="button" className="astra-operation-title" onClick={() => onOpen(String(task.id))}>
          {operationTitle(task)}
        </button>
      </>
    );
  }
  return (
    <div className="astra-operations astra-model-workspace">
      <header className="astra-model-header">
        <div>
          <small>Investigation workspace</small>
          <h1>Operations</h1>
          <p>Assign work, follow execution and retain the evidence.</p>
        </div>
        <div className="astra-operation-toolbar">
          <button
            disabled={busy}
            onClick={() => {
              fail("");
              void load();
            }}
          >
            Refresh
          </button>
          <button onClick={() => open({ kind: "categories", ids: [] })}>Categories</button>
          <button onClick={() => open({ kind: "concurrency", ids: [] })}>Concurrency</button>
          <button onClick={() => showArchives(!archives)} aria-expanded={archives}>
            Archives
          </button>
          <button className="is-primary" onClick={() => open({ kind: "create", ids: [] })}>
            New operation
          </button>
        </div>
      </header>
      {error && (
        <p role="alert" className="astra-form-error">
          {error}
        </p>
      )}
      <div className="astra-operation-pulse">
        {operationStates.map((state) => (
          <button
            type="button"
            key={state}
            aria-pressed={view.status === state}
            onClick={() => configure({ status: view.status === state ? "all" : state })}
          >
            <strong>{tasks.filter((task) => task.status === state).length}</strong> {state}
          </button>
        ))}
      </div>
      <div className="astra-operation-toolbar astra-operation-filters">
        <label>
          Search operations
          <input type="search" value={view.query} onChange={(event) => configure({ query: event.target.value })} />
        </label>
        <label>
          Status
          <select value={view.status} onChange={(event) => configure({ status: event.target.value })}>
            <option value="all">All statuses</option>
            {operationStates.map((state) => (
              <option key={state}>{state}</option>
            ))}
          </select>
        </label>
        <label>
          Category
          <select value={view.category} onChange={(event) => configure({ category: event.target.value })}>
            <option value="all">All categories</option>
            <option value="none">Uncategorized</option>
            {categories.map((category) => (
              <option key={category.id} value={category.id}>
                {category.name}
              </option>
            ))}
          </select>
        </label>
        <label>
          Sort by
          <select
            value={view.sort}
            onChange={(event) => configure({ sort: event.target.value as OperationView["sort"] })}
          >
            {["id", "created", "duration", "status"].map((sort) => (
              <option key={sort}>{sort}</option>
            ))}
          </select>
        </label>
        <button onClick={() => configure({ ascending: !view.ascending })}>
          {view.ascending ? "Ascending" : "Descending"}
        </button>
        <button onClick={() => configure({ layout: view.layout === "board" ? "ledger" : "board" })}>
          {view.layout === "board" ? "Ledger view" : "Board view"}
        </button>
      </div>
      <div className="astra-operation-toolbar">
        <span>
          {selected.size} selected · {ordered.length} matching / {tasks.length} total
        </span>
        <button onClick={() => select((current) => new Set([...current, ...visible.map((task) => String(task.id))]))}>
          Select page
        </button>
        <button onClick={() => select((current) => new Set([...current, ...ordered.map((task) => String(task.id))]))}>
          Select matching
        </button>
        <button onClick={() => select(new Set())}>Clear selection</button>
        <button
          disabled={busy || !chosen.some((task) => operationControl(task) === "pause")}
          onClick={() =>
            void control(
              chosen.filter((task) => operationControl(task) === "pause").map((task) => String(task.id)),
              "pause",
            )
          }
        >
          Pause selected
        </button>
        <button
          disabled={busy || !chosen.some((task) => operationControl(task) === "resume")}
          onClick={() =>
            void control(
              chosen.filter((task) => operationControl(task) === "resume").map((task) => String(task.id)),
              "resume",
            )
          }
        >
          Resume selected
        </button>
        <button disabled={busy || !selected.size} onClick={() => open({ kind: "move", ids: [...selected] })}>
          Move selected
        </button>
        <button
          disabled={busy || !chosen.some((task) => !operationArchiveBlock(task))}
          onClick={() =>
            open({
              kind: "archive",
              ids: chosen.filter((task) => !operationArchiveBlock(task)).map((task) => String(task.id)),
            })
          }
        >
          Archive selected
        </button>
        <button disabled={busy || !selected.size} onClick={() => open({ kind: "delete", ids: [...selected] })}>
          Delete selected
        </button>
      </div>
      <OutcomeList items={outcomes} />
      <div className="astra-category-destinations" aria-label="Drag operations to a category">
        {[{ id: 0, name: "Uncategorized" }, ...categories].map((category) => (
          <div
            key={category.id}
            onDragOver={(event) => {
              if (!busy && event.dataTransfer.types.includes("application/x-astra-operations")) event.preventDefault();
            }}
            onDrop={(event) => {
              event.preventDefault();
              try {
                const ids: unknown = JSON.parse(event.dataTransfer.getData("application/x-astra-operations"));
                if (
                  !Array.isArray(ids) ||
                  !ids.every((id) => typeof id === "string" && tasks.some((task) => String(task.id) === id))
                )
                  return;
                void action(() => move(ids, category.id || null));
              } catch (error) {
                failure(error);
              }
            }}
          >
            <span>{category.name}</span>
            <small>Drop to move category</small>
          </div>
        ))}
      </div>
      {loading && <p role="status">Loading operations…</p>}
      {view.layout === "ledger" ? (
        <div className="astra-operation-table">
          <table>
            <thead>
              <tr>
                <th>Operation</th>
                <th>State</th>
                <th>Category</th>
                <th>Runtime</th>
                <th>Evidence</th>
                <th>Actions</th>
              </tr>
            </thead>
            <tbody>
              {visible.map((task) => (
                <tr
                  key={task.id}
                  draggable={!busy}
                  onDragStart={(event) =>
                    event.dataTransfer.setData(
                      "application/x-astra-operations",
                      JSON.stringify(selected.has(String(task.id)) ? [...selected] : [String(task.id)]),
                    )
                  }
                >
                  <td>
                    {identity(task)}
                    <p>{task.description}</p>
                  </td>
                  <td>
                    {task.status}
                    {task.stalled && " · Stalled"}
                  </td>
                  <td>{task.category_name || "Uncategorized"}</td>
                  <td>{elapsed(operationSeconds(task, now))}</td>
                  <td>
                    {task.goals_met || 0}/{task.goals_total || 0} goals
                    <br />
                    {task.tokens?.input_tokens || 0} in / {task.tokens?.output_tokens || 0} out
                  </td>
                  <td>{itemActions(task)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      ) : (
        <div className="astra-operation-board">
          {[
            { label: "Staged", states: ["created", "queued"] },
            { label: "Executing", states: ["running"] },
            { label: "Intervention", states: ["paused", "failed", "timeout"] },
            { label: "Complete", states: ["done"] },
          ].map((lane) => (
            <section key={lane.label}>
              <h2>
                {lane.label} <small>{visible.filter((task) => lane.states.includes(task.status)).length}</small>
              </h2>
              {visible
                .filter((task) => lane.states.includes(task.status))
                .map((task) => (
                  <article
                    key={task.id}
                    draggable={!busy}
                    onDragStart={(event) =>
                      event.dataTransfer.setData(
                        "application/x-astra-operations",
                        JSON.stringify(selected.has(String(task.id)) ? [...selected] : [String(task.id)]),
                      )
                    }
                  >
                    {identity(task)}
                    <p>{task.description}</p>
                    <p>
                      {task.status} · {task.category_name || "Uncategorized"} · {elapsed(operationSeconds(task, now))}
                    </p>
                    <p>
                      {task.goals_met || 0}/{task.goals_total || 0} goals ·{" "}
                      {task.findings ? Object.values(task.findings).reduce((sum, count) => sum + count, 0) : 0} findings
                      {task.stalled ? " · Stalled" : ""}
                    </p>
                    {task.last_activity && <p>{task.last_activity}</p>}
                    {itemActions(task)}
                  </article>
                ))}
            </section>
          ))}
        </div>
      )}
      {!loading && !ordered.length && <p>No operations match this view.</p>}
      <div className="astra-operation-toolbar">
        <button disabled={currentPage <= 1} onClick={() => setPage(currentPage - 1)}>
          Previous page
        </button>
        <span>
          Page {currentPage} of {pages}
        </span>
        <button disabled={currentPage >= pages} onClick={() => setPage(currentPage + 1)}>
          Next page
        </button>
        <label>
          Operations per page
          <select value={view.size} onChange={(event) => configure({ size: Number(event.target.value) })}>
            {[10, 20, 50, 100].map((size) => (
              <option key={size}>{size}</option>
            ))}
          </select>
        </label>
      </div>
      {archives && <OperationArchives changed={load} failure={failure} />}
      {dialog?.kind === "create" && (
        <OperationEditor
          tasks={tasks}
          categories={categories}
          close={() => open(undefined)}
          created={(id) => {
            open(undefined);
            void load();
            onOpen(id);
          }}
        />
      )}
      {dialog?.kind === "categories" && (
        <CategoriesDialog categories={categories} changed={load} close={() => open(undefined)} />
      )}
      {dialog?.kind === "concurrency" && <ConcurrencyDialog close={() => open(undefined)} />}
      {dialog?.kind === "delete" && (
        <DeleteOperationsDialog
          ids={actionIDs}
          close={() => open(undefined)}
          complete={async (results) => {
            report(results);
            await load();
          }}
        />
      )}
      {dialog?.kind === "move" && (
        <MoveOperationsDialog
          count={actionIDs.length}
          categories={categories}
          initial={dialog.category}
          close={() => open(undefined)}
          move={(category) => move(actionIDs, category)}
        />
      )}
      {dialog?.kind === "archive" && (
        <EditorDialog title="Archive operations" eyebrow="Archive" busy={busy} close={() => open(undefined)}>
          <p>
            Move {actionIDs.length} operations into cold storage? Their evidence and files remain in the archive package
            and can be restored.
          </p>
          <div className="astra-form-actions">
            <button disabled={busy} onClick={() => void archive(actionIDs)}>
              Confirm archiving
            </button>
            <button disabled={busy} onClick={() => open(undefined)}>
              Cancel
            </button>
          </div>
        </EditorDialog>
      )}
      {dialog?.kind === "rename" && (
        <EditorDialog title="Rename operation" eyebrow="Operation name" busy={busy} close={() => open(undefined)}>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void action(async () => {
                await consoleRequest(`/tasks/${encodeURIComponent(actionIDs[0])}`, "PATCH", { name: rename.trim() });
                open(undefined);
                await load();
              });
            }}
          >
            <label>
              Name
              <input autoFocus value={rename} onChange={(event) => setRename(event.target.value)} disabled={busy} />
            </label>
            <div className="astra-form-actions">
              <button disabled={busy}>Save operation name</button>
              <button type="button" disabled={busy} onClick={() => open(undefined)}>
                Cancel
              </button>
            </div>
          </form>
        </EditorDialog>
      )}
    </div>
  );
}
