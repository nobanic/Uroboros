"use client";

import { useEffect, useState } from "react";
import type { DeleteTaskOptions, DeleteTaskResult, TaskCategory } from "@/lib/types";
import { consoleRequest } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import type { ItemOutcome } from "./operations-domain";

export function OutcomeList({ items }: { items: ItemOutcome[] }) {
  if (!items.length) return null;
  return (
    <section className="astra-operation-outcomes" aria-label="Action results" aria-live="polite">
      <p>
        {items.filter((item) => item.ok).length} succeeded · {items.filter((item) => !item.ok).length} failed
      </p>
      <ul>
        {items.map((item) => (
          <li key={item.id} data-ok={item.ok}>
            #{item.id}: {item.ok ? (item.queued ? "Queued" : "Completed") : item.error || "Failed"}
            {item.warning && <p role="alert">Cleanup warning: {item.warning}</p>}
            {item.detail && <p>{item.detail}</p>}
          </li>
        ))}
      </ul>
    </section>
  );
}

export function MoveOperationsDialog({
  count,
  categories,
  move,
  close,
  initial = "",
}: {
  count: number;
  categories: TaskCategory[];
  move: (category: number | null) => Promise<void>;
  close: () => void;
  initial?: string;
}) {
  const [category, choose] = useState(initial),
    [busy, pending] = useState(false),
    [error, fail] = useState("");
  return (
    <EditorDialog
      title={`Move ${count} operation${count === 1 ? "" : "s"}`}
      eyebrow="Category"
      busy={busy}
      close={close}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          pending(true);
          fail("");
          void move(category ? Number(category) : null)
            .then(close, (error) => fail(error.message || String(error)))
            .finally(() => pending(false));
        }}
      >
        {error && <p role="alert">{error}</p>}
        <label>
          Destination
          <select value={category} disabled={busy} onChange={(event) => choose(event.target.value)}>
            <option value="">Uncategorized</option>
            {categories.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </select>
        </label>
        <div className="astra-form-actions">
          <button disabled={busy} type="submit">
            Move operations
          </button>
          <button disabled={busy} type="button" onClick={close}>
            Cancel
          </button>
        </div>
      </form>
    </EditorDialog>
  );
}

const deletionFields: [keyof DeleteTaskOptions, string][] = [
  ["delete_assets", "Delete related assets (shared assets are detached)"],
  ["delete_traffic", "Delete captured traffic"],
  ["delete_files", "Delete working files"],
  ["delete_findings", "Delete findings"],
  ["delete_llm_records", "Delete model request records"],
];
export function DeleteOperationsDialog({
  ids,
  close,
  complete,
}: {
  ids: string[];
  close: () => void;
  complete: (results: ItemOutcome[]) => Promise<void>;
}) {
  const [options, configure] = useState<DeleteTaskOptions>({
    delete_assets: false,
    delete_traffic: false,
    delete_files: false,
    delete_findings: false,
    delete_llm_records: false,
  });
  const [busy, pending] = useState(false),
    [progress, setProgress] = useState(0),
    [results, setResults] = useState<ItemOutcome[]>([]);
  async function remove() {
    pending(true);
    const outcomes: ItemOutcome[] = [];
    try {
      for (const id of ids) {
        try {
          const result = await consoleRequest<DeleteTaskResult>(`/tasks/${encodeURIComponent(id)}`, "DELETE", options);
          const detail = `${result.assets_deleted || 0} assets deleted, ${result.assets_detached || 0} detached; ${result.traffic_deleted || 0} traffic, ${result.findings_deleted || 0} findings, ${result.llm_records_deleted || 0} model records; working files ${result.files_deleted ? "deleted" : "retained"}.`;
          outcomes.push({ id, ok: true, warning: result.cleanup_warning, detail });
        } catch (error) {
          outcomes.push({ id, ok: false, error: error instanceof Error ? error.message : String(error) });
        }
        setResults([...outcomes]);
        setProgress(outcomes.length);
      }
      await complete(outcomes);
    } finally {
      pending(false);
    }
  }
  return (
    <EditorDialog
      title={`Delete ${ids.length} operation${ids.length === 1 ? "" : "s"}`}
      eyebrow="Deletion"
      busy={busy}
      close={close}
    >
      <p>
        This removes the selected operation records. Choose which related data to remove. Unchecked data is retained
        according to the server cleanup policy.
      </p>
      <fieldset disabled={busy || progress > 0}>
        <legend>Related data</legend>
        {deletionFields.map(([key, label]) => (
          <label key={key} className="astra-operation-check">
            <input
              type="checkbox"
              checked={options[key]}
              onChange={(event) => configure((current) => ({ ...current, [key]: event.target.checked }))}
            />
            {label}
          </label>
        ))}
        <button
          type="button"
          onClick={() =>
            configure({
              delete_assets: true,
              delete_traffic: true,
              delete_files: true,
              delete_findings: true,
              delete_llm_records: true,
            })
          }
        >
          Select all related data
        </button>
      </fieldset>
      {progress > 0 && (
        <p role="status">
          Processed {progress} of {ids.length}
        </p>
      )}
      <OutcomeList items={results} />
      <div className="astra-form-actions">
        <button type="button" disabled={busy || progress > 0} onClick={() => void remove()}>
          Delete operations
        </button>
        <button type="button" disabled={busy} onClick={close}>
          {progress ? "Close results" : "Cancel"}
        </button>
      </div>
    </EditorDialog>
  );
}

export function ConcurrencyDialog({ close }: { close: () => void }) {
  const [enabled, setEnabled] = useState(false),
    [limit, setLimit] = useState("5"),
    [busy, pending] = useState(true),
    [loaded, setLoaded] = useState(false),
    [error, fail] = useState("");
  useEffect(() => {
    let alive = true;
    consoleRequest<{ task_concurrency_enabled: boolean; task_concurrency_limit: number }>("/settings")
      .then(
        (settings) => {
          if (alive) {
            setEnabled(Boolean(settings.task_concurrency_enabled));
            setLimit(String(settings.task_concurrency_limit ?? 5));
            setLoaded(true);
          }
        },
        (error) => {
          if (alive) fail(error.message);
        },
      )
      .finally(() => {
        if (alive) pending(false);
      });
    return () => {
      alive = false;
    };
  }, []);
  return (
    <EditorDialog title="Concurrent operations" eyebrow="Concurrency" busy={busy} close={close}>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          const count = Number(limit);
          if (!Number.isSafeInteger(count) || count < 1) {
            fail("Enter a positive whole-number limit.");
            return;
          }
          pending(true);
          fail("");
          void consoleRequest("/settings", "PUT", { task_concurrency_enabled: enabled, task_concurrency_limit: count })
            .then(close, (error) => fail(error.message))
            .finally(() => pending(false));
        }}
      >
        {error && <p role="alert">{error}</p>}
        <fieldset disabled={busy || !loaded}>
          <legend>Run slots</legend>
          <label className="astra-operation-check">
            <input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} />
            Limit concurrent runs
          </label>
          <label>
            Maximum concurrent runs
            <input type="number" min="1" step="1" value={limit} onChange={(event) => setLimit(event.target.value)} />
          </label>
          <p>When the limit is disabled, the number of run slots is unlimited.</p>
        </fieldset>
        <div className="astra-form-actions">
          <button disabled={busy || !loaded}>Save concurrency</button>
          <button type="button" disabled={busy} onClick={close}>
            Cancel
          </button>
        </div>
      </form>
    </EditorDialog>
  );
}

export function CategoriesDialog({
  categories,
  changed,
  close,
}: {
  categories: TaskCategory[];
  changed: () => Promise<void>;
  close: () => void;
}) {
  const [editing, select] = useState<number>(),
    [name, rename] = useState(""),
    [deleting, mark] = useState<number>(),
    [busy, pending] = useState(false),
    [error, fail] = useState("");
  async function write(path: string, method: "POST" | "PATCH" | "DELETE", body?: unknown) {
    pending(true);
    fail("");
    try {
      await consoleRequest(path, method, body);
      await changed();
      select(undefined);
      rename("");
      mark(undefined);
    } catch (error) {
      fail(error instanceof Error ? error.message : String(error));
    } finally {
      pending(false);
    }
  }
  return (
    <EditorDialog title="Operation categories" eyebrow="Categories" busy={busy} close={close}>
      {error && <p role="alert">{error}</p>}
      <ul className="astra-category-list">
        {categories.map((category) => (
          <li key={category.id}>
            <span>
              {category.name} · {category.task_count} operations
            </span>
            <button
              type="button"
              disabled={busy}
              onClick={() => {
                select(category.id);
                rename(category.name);
              }}
            >
              Rename
            </button>
            <button type="button" disabled={busy} onClick={() => mark(category.id)}>
              Delete
            </button>
            {deleting === category.id && (
              <div role="alert">
                Remove this category? Its operations become uncategorized.
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => void write(`/task-categories/${category.id}`, "DELETE")}
                >
                  Confirm category deletion
                </button>
                <button type="button" onClick={() => mark(undefined)}>
                  Keep category
                </button>
              </div>
            )}
          </li>
        ))}
      </ul>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void write(editing ? `/task-categories/${editing}` : "/task-categories", editing ? "PATCH" : "POST", {
            name: name.trim(),
          });
        }}
      >
        <label>
          {editing ? "Category name" : "New category"}
          <input
            name="category_name"
            required
            disabled={busy}
            value={name}
            onChange={(event) => rename(event.target.value)}
          />
        </label>
        <div className="astra-form-actions">
          <button disabled={busy || !name.trim()}>{editing ? "Save category name" : "Create category"}</button>
          {editing && (
            <button
              type="button"
              onClick={() => {
                select(undefined);
                rename("");
              }}
            >
              New category instead
            </button>
          )}
        </div>
      </form>
    </EditorDialog>
  );
}
