"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { ArchiveBatchItem, TaskArchive, TaskArchivePage } from "@/lib/types";
import { ApiFailure } from "./client";
import { consoleRequest } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import { OutcomeList } from "./operation-controls";
import { archiveInProgress, archiveRestorable, batchOutcomes, type ItemOutcome } from "./operations-domain";

export function OperationArchives({
  changed,
  failure,
}: {
  changed: () => Promise<void>;
  failure: (error: unknown) => void;
}) {
  const [data, setData] = useState<TaskArchivePage>({ items: [], total: 0, page: 1, size: 20 }),
    [page, setPage] = useState(1),
    [size, setSize] = useState(20),
    [query, search] = useState(""),
    [state, filter] = useState("");
  const [selected, select] = useState<Set<number>>(new Set()),
    [busy, pending] = useState(false),
    [deleting, confirm] = useState<TaskArchive[]>(),
    [outcomes, report] = useState<ItemOutcome[]>([]);
  const pendingRestores = useRef(new Set<number>()),
    sequence = useRef(0),
    signature = useRef("");
  const load = useCallback(async () => {
    const request = ++sequence.current;
    try {
      const params = new URLSearchParams({ page: String(page), size: String(size), q: query, state });
      const result = await consoleRequest<TaskArchivePage>(`/task-archives?${params}`);
      if (request !== sequence.current) return;
      setData({ ...result, items: result.items || [] });
      select((current) => new Set([...current].filter((id) => result.items?.some((item) => item.id === id))));
      const nextSignature = JSON.stringify((result.items || []).map((item) => [item.id, item.state]));
      if (signature.current && signature.current !== nextSignature) void changed();
      signature.current = nextSignature;
      if (page > 1 && (page - 1) * size >= result.total) setPage(Math.max(1, Math.ceil(result.total / size)));
      await Promise.all(
        [...pendingRestores.current].map(async (id) => {
          try {
            const archive = await consoleRequest<TaskArchive>(`/task-archives/${id}`);
            if (archive.state === "restore_failed") pendingRestores.current.delete(id);
          } catch (error) {
            if (error instanceof ApiFailure && error.status === 404) {
              pendingRestores.current.delete(id);
              await changed();
            } else failure(error);
          }
        }),
      );
    } catch (error) {
      if (request === sequence.current) failure(error);
    }
  }, [changed, failure, page, size, query, state]);
  useEffect(() => {
    void load();
    const timer = setInterval(() => void load(), 2000);
    return () => {
      clearInterval(timer);
      sequence.current++;
    };
  }, [load]);
  const chosen = data.items.filter((item) => selected.has(item.id));
  async function run(items: TaskArchive[], action: "restore" | "delete") {
    if (!items.length || busy) return;
    pending(true);
    try {
      const result =
        items.length === 1
          ? {
              items: [
                {
                  id: String(items[0].id),
                  ok: true,
                  queued: true,
                  archive_id: (
                    await consoleRequest<TaskArchive>(
                      `/task-archives/${items[0].id}${action === "restore" ? "/restore" : ""}`,
                      action === "restore" ? "POST" : "DELETE",
                    )
                  ).id,
                },
              ],
            }
          : await consoleRequest<{ items: ArchiveBatchItem[] }>(`/task-archives/${action}/batch`, "POST", {
              archive_ids: items.map((item) => item.id),
            });
      const rows = batchOutcomes(
        items.map((item) => String(item.id)),
        result.items,
      );
      if (action === "restore")
        for (const row of rows) if (row.ok) pendingRestores.current.add(row.archive_id || Number(row.id));
      report(rows);
      select(new Set(rows.filter((row) => !row.ok).map((row) => Number(row.id))));
      confirm(undefined);
      await load();
      await changed();
    } catch (error) {
      failure(error);
    } finally {
      pending(false);
    }
  }
  async function retry(item: TaskArchive) {
    if (item.state === "restore_failed") return run([item], "restore");
    if (item.state === "delete_failed") {
      confirm([item]);
      return;
    }
    pending(true);
    try {
      await consoleRequest(`/tasks/${item.task_id}/archive`, "POST");
      await load();
      await changed();
    } catch (error) {
      failure(error);
    } finally {
      pending(false);
    }
  }
  return (
    <section className="astra-operation-archives" aria-label="Operation archives">
      <header className="astra-operation-toolbar">
        <h2>Archives</h2>
        <button type="button" disabled={busy} onClick={() => void load()}>
          Refresh archives
        </button>
      </header>
      <div className="astra-operation-toolbar">
        <label>
          Search archives
          <input
            type="search"
            value={query}
            onChange={(event) => {
              search(event.target.value);
              setPage(1);
            }}
          />
        </label>
        <label>
          Archive state
          <select
            value={state}
            onChange={(event) => {
              filter(event.target.value);
              setPage(1);
            }}
          >
            <option value="">All states</option>
            {[
              "archive_queued",
              "archiving",
              "archive_failed",
              "ready",
              "restore_queued",
              "restoring",
              "restore_failed",
              "delete_queued",
              "deleting",
              "delete_failed",
            ].map((state) => (
              <option key={state}>{state}</option>
            ))}
          </select>
        </label>
        <button
          type="button"
          onClick={() =>
            select(new Set(data.items.filter((item) => !archiveInProgress(item.state)).map((item) => item.id)))
          }
        >
          Select this archive page
        </button>
        <button type="button" onClick={() => select(new Set())}>
          Clear archive selection
        </button>
        <button
          type="button"
          disabled={busy || !chosen.some((item) => archiveRestorable(item.state))}
          onClick={() =>
            void run(
              chosen.filter((item) => archiveRestorable(item.state)),
              "restore",
            )
          }
        >
          Restore selected archives
        </button>
        <button
          type="button"
          disabled={busy || !chosen.some((item) => !archiveInProgress(item.state))}
          onClick={() => confirm(chosen.filter((item) => !archiveInProgress(item.state)))}
        >
          Delete selected archives
        </button>
      </div>
      <OutcomeList items={outcomes} />
      <div className="astra-archive-cards">
        {data.items.map((item) => (
          <article key={item.id}>
            <header>
              <label className="astra-operation-check">
                <input
                  type="checkbox"
                  aria-label={`Select archive ${item.id}`}
                  disabled={archiveInProgress(item.state)}
                  checked={selected.has(item.id)}
                  onChange={(event) =>
                    select((current) => {
                      const next = new Set(current);
                      if (event.target.checked) next.add(item.id);
                      else next.delete(item.id);
                      return next;
                    })
                  }
                />
                <strong>{item.task_name || `Operation ${item.task_id}`}</strong>
              </label>
              <span>{item.state}</span>
            </header>
            <p>
              #{item.task_id} · {item.category_name || "Uncategorized"} · {item.phase || "Waiting"}
            </p>
            <progress max={100} value={item.progress} aria-label={`Archive ${item.id} progress`} />
            <span>{item.progress}%</span>
            {item.error && <p role="alert">{item.error}</p>}
            {item.warnings?.map((warning, index) => (
              <p role="alert" key={index}>
                {warning}
              </p>
            ))}
            <p>
              {item.original_size.toLocaleString()} bytes → {item.compressed_size.toLocaleString()} bytes · Format{" "}
              {item.format_version}
            </p>
            <details>
              <summary>Archive contents and integrity</summary>
              <p>SHA-256: {item.sha256 || "Pending"}</p>
              <pre>
                {JSON.stringify(
                  {
                    assignment: item.task_description,
                    goal: item.task_goal,
                    status: item.original_status,
                    sources: item.source_task_ids,
                    remaining_timeout_seconds: item.remaining_timeout_seconds,
                    counts: item.data_counts,
                    aggregates: item.aggregate_stats,
                    requested_at: item.requested_at,
                    archived_at: item.archived_at,
                    updated_at: item.updated_at,
                  },
                  null,
                  2,
                )}
              </pre>
            </details>
            <footer className="astra-operation-toolbar">
              <button
                type="button"
                disabled={busy || !archiveRestorable(item.state)}
                onClick={() => void run([item], "restore")}
              >
                Restore archive
              </button>
              <button type="button" disabled={busy || archiveInProgress(item.state)} onClick={() => confirm([item])}>
                Delete archive
              </button>
              {item.state.endsWith("_failed") && (
                <button type="button" disabled={busy} onClick={() => void retry(item)}>
                  Retry archive operation
                </button>
              )}
            </footer>
          </article>
        ))}
      </div>
      {!data.items.length && <p>No archives match this view.</p>}
      <div className="astra-operation-toolbar">
        <button disabled={page <= 1} onClick={() => setPage(page - 1)}>
          Previous archive page
        </button>
        <span>
          Page {page} · {data.total} archives
        </span>
        <button disabled={page * size >= data.total} onClick={() => setPage(page + 1)}>
          Next archive page
        </button>
        <label>
          Archives per page
          <select
            value={size}
            onChange={(event) => {
              setSize(Number(event.target.value));
              setPage(1);
            }}
          >
            {[10, 20, 50, 100].map((size) => (
              <option key={size}>{size}</option>
            ))}
          </select>
        </label>
      </div>
      {deleting && (
        <EditorDialog
          title="Permanently delete archives"
          eyebrow="Archive deletion"
          busy={busy}
          close={() => confirm(undefined)}
        >
          <p>Delete {deleting.length} archive packages and their archived data permanently?</p>
          <div className="astra-form-actions">
            <button disabled={busy} onClick={() => void run(deleting, "delete")}>
              Confirm archive deletion
            </button>
            <button disabled={busy} onClick={() => confirm(undefined)}>
              Keep archives
            </button>
          </div>
        </EditorDialog>
      )}
    </section>
  );
}
