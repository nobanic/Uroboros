"use client";
import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { toast } from "sonner";
import { AssetDslSearch } from "@/components/asset-dsl-search";
import { ScopeTextEditor } from "@/components/scope-text-editor";
import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import { parseCompanyScopeText } from "@/lib/company-scope";
import { taskAssetSourceLabel } from "@/lib/task-assets";
import type { Asset, NewAssetType } from "@/lib/types";
import { assetPageSizes } from "./asset-document";
import { safeDetail } from "./client";
import { EditorDialog } from "./editor-dialog";
import {
  taskAssetCell,
  taskAssetColumns,
  taskAssetKinds,
  taskAssetLastPage,
  taskAssetName,
  taskAssetStatus,
  taskAssetTotal,
} from "./task-asset-document";
import { useTaskAssets } from "./use-task-assets";
import "./forms.css";
import "./task-assets.css";

const icons: Record<NewAssetType, UroIconName> = {
  root_domain: "asset-domain",
  ip: "asset-ip",
  subdomain: "asset-subdomain",
  app: "asset-app",
  service: "asset-service",
  endpoint: "asset-endpoint",
};
type AssetsState = ReturnType<typeof useTaskAssets>;

function RegisterTaskAssets({ id, state, close }: { id: string; state: AssetsState; close: () => void }) {
  const [text, update] = useState("");
  const parsed = useMemo(() => parseCompanyScopeText(text), [text]);
  useEffect(() => {
    const focus = requestAnimationFrame(() => document.getElementById("task-asset-scope")?.focus());
    return () => cancelAnimationFrame(focus);
  }, []);
  async function register() {
    if (!parsed.rules.length || parsed.errors.length) return;
    const outcome = await state.write(() => api.registerTaskAssetScopes(id, parsed.rules));
    if (outcome.ok) {
      const result = outcome.result;
      toast.success(
        `Registered ${result.requested} scopes, linked ${result.assets_linked + result.assets_existing} domain/IP assets`,
      );
      close();
    }
  }
  return (
    <EditorDialog title="Add Test Assets" eyebrow="Task assets" busy={state.busy} close={close}>
      <p className="astra-task-assets-dialog-note">
        Domains and IPs create or reuse global assets. CIDR, ICP and keywords retain their role as task scope.
      </p>
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void register();
        }}
      >
        <fieldset disabled={state.busy}>
          <ScopeTextEditor
            id="task-asset-scope"
            value={text}
            onValueChange={update}
            parsed={parsed}
            label="Test assets and scope"
            description="One entry per line. Existing detection, validation and deduplication apply."
          />
        </fieldset>
        {state.writeError && (
          <div role="alert" className="astra-task-assets-error">
            {state.writeError}
          </div>
        )}
        <footer>
          <button type="button" disabled={state.busy} onClick={close}>
            Cancel
          </button>
          <button
            type="submit"
            className="primary"
            disabled={state.busy || !parsed.rules.length || !!parsed.errors.length}
          >
            {state.busy ? "Registering…" : `Register ${parsed.rules.length || ""}`}
          </button>
        </footer>
      </form>
    </EditorDialog>
  );
}

function AssetRecord({ asset, close }: { asset: Asset; close: () => void }) {
  const source = asset.task_source || "legacy";
  const document = JSON.stringify(safeDetail(asset), null, 2);
  return (
    <EditorDialog title={`Asset #${asset.id}`} eyebrow="Asset record" close={close}>
      <div className="astra-task-asset-record">
        <UroIcon name={icons[asset.type]} />
        <h3>{taskAssetName(asset)}</h3>
      </div>
      <dl className="astra-task-asset-provenance">
        <dt>Source</dt>
        <dd>{taskAssetSourceLabel(source)}</dd>
        <dt>Provenance</dt>
        <dd>
          {asset.task_source_summary ||
            "Migrated from a legacy task-asset association; no more detailed source information available"}
        </dd>
        {!!asset.task_source_node_id && (
          <>
            <dt>Source node</dt>
            <dd>#{asset.task_source_node_id}</dd>
          </>
        )}
      </dl>
      <details>
        <summary>Stored record</summary>
        <pre className="astra-task-asset-json">{document}</pre>
        <button
          type="button"
          onClick={async () => {
            try {
              await navigator.clipboard.writeText(document);
              toast.success("Asset record copied");
            } catch {
              toast.error("Clipboard is unavailable");
            }
          }}
        >
          Copy record
        </button>
      </details>
    </EditorDialog>
  );
}

function TaskAssets({ id }: { id: string }) {
  const state = useTaskAssets(id),
    { selection, snapshot } = state;
  const [adding, openRegister] = useState(false),
    [remove, confirmRemove] = useState<Asset>(),
    [inspect, showRecord] = useState<Asset>();
  const tabs = useRef<HTMLDivElement>(null);
  const rows = state.stale ? [] : snapshot?.assets || [],
    total = snapshot?.total || 0,
    columns = taskAssetColumns[selection.kind];
  const last = taskAssetLastPage(total, selection.size),
    current = snapshot !== undefined && !state.stale;
  function kind(value: NewAssetType) {
    showRecord(undefined);
    state.change({ ...selection, kind: value, query: "", page: 0 });
  }
  function keyboard(event: KeyboardEvent) {
    let next = taskAssetKinds.findIndex(([value]) => value === selection.kind);
    if (event.key === "Home") next = 0;
    else if (event.key === "End") next = taskAssetKinds.length - 1;
    else if (event.key === "ArrowRight") next = (next + 1) % taskAssetKinds.length;
    else if (event.key === "ArrowLeft") next = (next + taskAssetKinds.length - 1) % taskAssetKinds.length;
    else return;
    event.preventDefault();
    if (!state.busy) {
      kind(taskAssetKinds[next][0]);
      tabs.current?.querySelectorAll<HTMLButtonElement>("button")[next]?.focus();
    }
  }
  async function detach() {
    if (!remove) return;
    const target = remove;
    const outcome = await state.write(
      () => api.detachTaskAsset(id, target.id),
      (previous, result) =>
        result.detached > 0
          ? {
              ...previous,
              assets: previous.assets.filter((row) => row.id !== target.id),
              total: Math.max(0, previous.total - 1),
              counts: { ...previous.counts, [target.type]: Math.max(0, (previous.counts[target.type] || 0) - 1) },
            }
          : previous,
    );
    if (outcome.ok) {
      toast.success(
        outcome.result.detached > 0
          ? `Removed ${taskAssetName(target)} from the current task`
          : "No local association was removed. Task assets refreshed.",
      );
      confirmRemove(undefined);
      showRecord(undefined);
    }
  }
  return (
    <div className="astra-task-assets" data-task-assets={id}>
      <header className="astra-task-assets-command">
        <div>
          <small>Operation asset registry</small>
          <h2>Test Assets</h2>
          <p>
            {snapshot ? (
              <>
                This task is linked to <strong>{taskAssetTotal(snapshot.counts)}</strong> assets in total.
              </>
            ) : (
              "Loading task associations…"
            )}
          </p>
        </div>
        <div>
          <button type="button" disabled={state.busy} onClick={() => void state.load(true)}>
            <UroIcon name="sync" />
            Refresh assets
          </button>
          <button type="button" className="primary" disabled={state.busy} onClick={() => openRegister(true)}>
            <UroIcon name="plus" />
            Add Test Assets
          </button>
        </div>
      </header>
      <div
        ref={tabs}
        className="astra-task-asset-kinds"
        role="tablist"
        aria-label="Task asset classes"
        onKeyDown={keyboard}
      >
        {taskAssetKinds.map(([value, label]) => (
          <button
            key={value}
            type="button"
            id={`task-asset-tab-${value}`}
            role="tab"
            aria-selected={selection.kind === value}
            aria-controls="task-asset-panel"
            tabIndex={selection.kind === value ? 0 : -1}
            disabled={state.busy}
            onClick={() => kind(value)}
          >
            <UroIcon name={icons[value]} />
            <span>{label}</span>
            <strong>{snapshot?.counts[value] ?? "—"}</strong>
          </button>
        ))}
      </div>
      <div className="astra-task-asset-search">
        <AssetDslSearch
          query={selection.query}
          onChange={(query) => {
            showRecord(undefined);
            state.change({ ...selection, query, page: 0 });
          }}
          loading={state.loading}
          error={state.error}
          count={selection.query.trim() && current ? total : undefined}
        />
        <span>
          <UroIcon name="shield" /> Search remains inside this task
        </span>
      </div>
      {state.error && (
        <div className="astra-task-assets-error" role="alert">
          <span>{state.error}</span>
          <button type="button" disabled={state.busy} onClick={() => void state.load(true)}>
            Retry task assets
          </button>
        </div>
      )}
      {state.writeError && !adding && !remove && (
        <div className="astra-task-assets-error" role="alert">
          {state.writeError}
        </div>
      )}
      {state.stale && (
        <p className="astra-task-assets-stale" role="status">
          The last successful page is retained. The current selection has not loaded yet.{" "}
          {state.error && "Retry or adjust the filter."}
        </p>
      )}
      <section
        id="task-asset-panel"
        role="tabpanel"
        aria-labelledby={`task-asset-tab-${selection.kind}`}
        aria-busy={state.loading}
      >
        <div className="astra-task-assets-table">
          <table>
            <thead>
              <tr>
                {columns.map(([key, label]) => (
                  <th key={key}>{label}</th>
                ))}
                <th>Source</th>
                <th>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((asset) => (
                <tr key={asset.id} data-task-asset={asset.id}>
                  {columns.map(([key], index) => (
                    <td
                      key={key}
                      data-field={key}
                      data-status-tone={key === "status_code" ? taskAssetStatus(asset.status_code ?? 0) : undefined}
                    >
                      {index === 0 ? (
                        <button
                          type="button"
                          className={`astra-task-asset-identity${key === "method" ? " astra-task-asset-method" : ""}`}
                          data-method={key === "method" ? asset.method?.toUpperCase() : undefined}
                          aria-label={`Inspect task asset ${asset.id}`}
                          onClick={() => showRecord(asset)}
                        >
                          {taskAssetCell(asset, key)}
                        </button>
                      ) : (
                        <span
                          title={taskAssetCell(asset, key)}
                          className={key === "method" ? "astra-task-asset-method" : undefined}
                        >
                          {taskAssetCell(asset, key)}
                        </span>
                      )}
                    </td>
                  ))}
                  <td>
                    <button
                      type="button"
                      className="astra-task-asset-source"
                      onClick={() => showRecord(asset)}
                      title={asset.task_source_summary || "Inspect source provenance"}
                    >
                      {taskAssetSourceLabel(asset.task_source || "legacy")}
                      {!!asset.task_source_node_id && <small>#{asset.task_source_node_id}</small>}
                    </button>
                  </td>
                  <td>
                    <button
                      type="button"
                      className="astra-task-asset-detach"
                      aria-label={`Remove asset ${taskAssetName(asset)} from the task`}
                      title="Remove from task"
                      disabled={state.busy || state.stale}
                      onClick={() => confirmRemove(asset)}
                    >
                      <UroIcon name="close" />
                      <span>Detach</span>
                    </button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {!rows.length && (
          <div className="astra-task-asset-empty">
            <UroIcon name={icons[selection.kind]} />
            <h3>
              {state.loading ? "Reading task assets…" : state.error ? "Task assets unavailable" : "No matching assets"}
            </h3>
            <p>
              {state.error
                ? "Retry to recover this view."
                : selection.query.trim()
                  ? "Adjust the DSL query to explore another part of this task."
                  : "Register task scope or retain discoveries from the existing tools."}
            </p>
          </div>
        )}
        <footer className="astra-task-asset-pager">
          <span>
            {current
              ? `${total ? selection.page * selection.size + 1 : 0}–${Math.min(total, selection.page * selection.size + rows.length)} of ${total}`
              : "Page pending"}
          </span>
          <label>
            Rows per page
            <select
              aria-label="Task asset page size"
              value={selection.size}
              disabled={state.busy}
              onChange={(event) => {
                showRecord(undefined);
                state.change({ ...selection, size: Number(event.target.value), page: 0 });
              }}
            >
              {assetPageSizes.map((size) => (
                <option key={size} value={size}>
                  {size}
                </option>
              ))}
            </select>
          </label>
          <div>
            <button
              type="button"
              aria-label="Previous task asset page"
              disabled={state.busy || state.loading || !current || selection.page === 0}
              onClick={() => {
                showRecord(undefined);
                state.change({ ...selection, page: selection.page - 1 });
              }}
            >
              <UroIcon name="chevron" />
              <span>Previous</span>
            </button>
            <strong>
              {selection.page + 1} / {current ? last + 1 : "—"}
            </strong>
            <button
              type="button"
              aria-label="Next task asset page"
              disabled={state.busy || state.loading || !current || selection.page >= last}
              onClick={() => {
                showRecord(undefined);
                state.change({ ...selection, page: selection.page + 1 });
              }}
            >
              <span>Next</span>
              <UroIcon name="chevron" />
            </button>
          </div>
        </footer>
      </section>
      {adding && <RegisterTaskAssets id={id} state={state} close={() => openRegister(false)} />}
      {inspect && (
        <AssetRecord
          asset={rows.find((asset) => asset.id === inspect.id) || inspect}
          close={() => showRecord(undefined)}
        />
      )}
      {remove && (
        <EditorDialog
          title="Remove from the current task?"
          eyebrow="Task association"
          busy={state.busy}
          close={() => confirmRemove(undefined)}
        >
          <p>Remove &quot;{taskAssetName(remove)}&quot; from the current task&apos;s test assets.</p>
          <p>Global assets, associated traffic and historical blackboard anchors are kept.</p>
          {state.writeError && (
            <div role="alert" className="astra-task-assets-error">
              {state.writeError}
            </div>
          )}
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void detach();
            }}
          >
            <footer>
              <button type="button" disabled={state.busy} onClick={() => confirmRemove(undefined)}>
                Cancel
              </button>
              <button type="submit" className="danger" disabled={state.busy}>
                {state.busy ? "Removing…" : "Confirm removal"}
              </button>
            </footer>
          </form>
        </EditorDialog>
      )}
    </div>
  );
}

export function TaskAssetsRoute({ taskId }: { taskId: string }) {
  return <TaskAssets key={taskId} id={taskId} />;
}
