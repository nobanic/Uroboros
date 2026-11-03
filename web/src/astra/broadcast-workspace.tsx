"use client";
import { useState } from "react";

import { StatusBadge } from "@/components/status-badge";
import { HoverCard, HoverCardContent, HoverCardTrigger } from "@/components/ui/hover-card";
import { UroIcon } from "@/components/uroboros-icons";
import { taskAssetTypeLabel } from "@/lib/task-assets";
import type { FindingAsset, NewAssetType, TaskNode } from "@/lib/types";

import {
  type BroadcastNeighbor,
  broadcastAppearance,
  type broadcastDocument,
  broadcastKind,
  broadcastKinds,
  broadcastPayload,
  broadcastRelation,
  broadcastRelative,
  broadcastSizes,
  broadcastState,
  broadcastSummary,
  broadcastTime,
} from "./broadcast-document";
import { EditorDialog } from "./editor-dialog";
import { useBroadcast } from "./use-broadcast";
import "./forms.css";
import "./broadcast.css";

type Context = ReturnType<typeof broadcastDocument>;
function Kind({ node }: { node: TaskNode }) {
  const appearance = broadcastAppearance(broadcastKind(node));
  return (
    <span className="astra-broadcast-kind" data-tone={appearance.tone}>
      {appearance.label}
    </span>
  );
}
function State({ node }: { node: TaskNode }) {
  if (node.type === "goal" || node.type === "intent") return <StatusBadge domain={node.type} value={node.state} dot />;
  const state = broadcastState(node);
  return state ? (
    <span className="astra-broadcast-state" data-tone={state[1]}>
      {state[0]}
    </span>
  ) : null;
}
function NodeAssets({ assets }: { assets: FindingAsset[] }) {
  if (!assets.length) return null;
  return (
    <section className="astra-broadcast-assets" aria-label="Assets involved">
      <h4>
        Assets involved <span>{assets.length}</span>
      </h4>
      <ul>
        {assets.map((asset) => {
          const label = taskAssetTypeLabel(asset.type as NewAssetType) || asset.type;
          return (
            <li key={asset.id} title={`${label} · ${asset.label}`}>
              <small>{label}</small>
              <code>{asset.label}</code>
              <span>#{asset.id}</span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
function Identity({ node }: { node: TaskNode }) {
  return (
    <dl className="astra-broadcast-identity">
      <div>
        <dt>Node</dt>
        <dd>
          <code>#{node.id}</code>
        </dd>
      </div>
      <div>
        <dt>Type</dt>
        <dd>{broadcastAppearance(broadcastKind(node)).label}</dd>
      </div>
      <div>
        <dt>Source</dt>
        <dd>{node.origin || "system"}</dd>
      </div>
      <div>
        <dt>Created</dt>
        <dd>{broadcastTime(node).full}</dd>
      </div>
      {node.inherited && (
        <div>
          <dt>Lineage</dt>
          <dd>Inherited{node.source_task_id ? ` from task #${node.source_task_id}` : ""}</dd>
        </div>
      )}
      {!node.inherited && node.source_task_id && (
        <div>
          <dt>Source task</dt>
          <dd>#{node.source_task_id}</dd>
        </div>
      )}
      <div>
        <dt>State</dt>
        <dd>{node.state || "—"}</dd>
      </div>
    </dl>
  );
}
function Record({ node, assets }: { node: TaskNode; assets: FindingAsset[] }) {
  return (
    <div className="astra-broadcast-record">
      <div className="astra-broadcast-badges">
        <Kind node={node} />
        <State node={node} />
        {node.priority > 0 && ["goal", "intent"].includes(node.type) && <span>P{node.priority}</span>}
      </div>
      <p>{broadcastSummary(node) || `(no summary) · Node #${node.id}`}</p>
      <Identity node={node} />
      {node.state === "deleted" && node.delete_reason && (
        <p className="astra-broadcast-deletion">
          <strong>Deletion reason</strong>
          {node.delete_reason}
        </p>
      )}
      <NodeAssets assets={assets} />
      <section className="astra-broadcast-payload">
        <h4>Full payload</h4>
        {/* biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need to scroll long raw observations. */}
        <pre tabIndex={0}>{broadcastPayload(node.payload)}</pre>
      </section>
    </div>
  );
}
function Neighbors({
  direction,
  rows,
  context,
  inspect,
  inspectorOpen,
}: {
  direction: "Upstream" | "Downstream";
  rows: BroadcastNeighbor[];
  context: Context;
  inspect: (id: string) => void;
  inspectorOpen: boolean;
}) {
  const [preview, showPreview] = useState<string | null>(null);
  if (!rows.length) return null;
  return (
    <section className="astra-broadcast-neighbors" aria-label={`${direction} nodes`}>
      <h4>
        <UroIcon name="route" />
        {direction}
        <span>{rows.length}</span>
      </h4>
      <ul>
        {rows.map((row) => {
          const node = context.nodes[row.id];
          return (
            <li key={row.key}>
              <small>{broadcastRelation(row.relation)}</small>
              {node ? (
                <HoverCard
                  openDelay={150}
                  closeDelay={100}
                  open={!inspectorOpen && preview === row.key}
                  onOpenChange={(open) => showPreview(open ? row.key : null)}
                >
                  <HoverCardTrigger asChild>
                    <button
                      type="button"
                      aria-label={`Inspect related node ${node.id}`}
                      onClick={() => {
                        showPreview(null);
                        inspect(node.id);
                      }}
                    >
                      <Kind node={node} />
                      <span>{broadcastSummary(node) || `Node #${node.id}`}</span>
                      <code>#{node.id}</code>
                    </button>
                  </HoverCardTrigger>
                  <HoverCardContent align="start" className="astra-broadcast-preview">
                    <Record node={node} assets={context.assets[node.id] ?? []} />
                  </HoverCardContent>
                </HoverCard>
              ) : (
                <span className="astra-broadcast-missing">Node #{row.id}</span>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}
function BroadcastRow({
  node,
  context,
  fresh,
  now,
  inspect,
  inspectorOpen,
}: {
  node: TaskNode;
  context: Context;
  fresh: boolean;
  now: number;
  inspect: (id: string) => void;
  inspectorOpen: boolean;
}) {
  const appearance = broadcastAppearance(broadcastKind(node)),
    time = broadcastTime(node);
  const upstream = context.upstream.get(node.id) ?? [],
    downstream = context.downstream.get(node.id) ?? [];
  return (
    <article
      className="astra-broadcast-row"
      data-broadcast-node={node.id}
      data-fresh={fresh}
      data-tone={appearance.tone}
    >
      <div className="astra-broadcast-clock">
        <time title={time.full}>{time.clock}</time>
        <small>{broadcastRelative(time.timestamp, now)}</small>
      </div>
      <div className="astra-broadcast-marker" aria-hidden="true">
        <span>
          <UroIcon name={appearance.icon} />
        </span>
      </div>
      <details className="astra-broadcast-entry">
        <summary aria-label={`Expand broadcast node ${node.id}`}>
          <div className="astra-broadcast-badges">
            <Kind node={node} />
            <code>#{node.id}</code>
            <State node={node} />
            {node.priority > 0 && ["goal", "intent"].includes(node.type) && (
              <span className="astra-broadcast-priority">P{node.priority}</span>
            )}
            {fresh && <b className="astra-broadcast-new">New</b>}
            <span className="astra-broadcast-source">{node.origin || "system"}</span>
            <UroIcon name="chevron" />
          </div>
          <p>{broadcastSummary(node) || `Node #${node.id}`}</p>
          <div className="astra-broadcast-context-count">
            <span>{upstream.length + downstream.length} links</span>
            <span>{context.assets[node.id]?.length || 0} assets</span>
            {node.inherited && <span>Inherited · task #{node.source_task_id ?? "?"}</span>}
          </div>
        </summary>
        <div className="astra-broadcast-expanded">
          <Identity node={node} />
          {node.state === "deleted" && node.delete_reason && (
            <p className="astra-broadcast-deletion">
              <strong>Deletion reason</strong>
              {node.delete_reason}
            </p>
          )}
          <NodeAssets assets={context.assets[node.id] ?? []} />
          <div className="astra-broadcast-connections">
            <Neighbors
              direction="Upstream"
              rows={upstream}
              context={context}
              inspect={inspect}
              inspectorOpen={inspectorOpen}
            />
            <Neighbors
              direction="Downstream"
              rows={downstream}
              context={context}
              inspect={inspect}
              inspectorOpen={inspectorOpen}
            />
          </div>
          <section className="astra-broadcast-payload">
            <h4>Full payload</h4>
            {/* biome-ignore lint/a11y/noNoninteractiveTabindex: Keyboard users need to scroll long raw observations. */}
            <pre tabIndex={0}>{broadcastPayload(node.payload)}</pre>
          </section>
        </div>
      </details>
    </article>
  );
}

export function BroadcastWorkspace({ task }: { task: string }) {
  const state = useBroadcast(task),
    { selection, select, snapshot, document, total } = state;
  const [inspected, inspect] = useState<string | null>(null);
  const selectedNode = inspected && document?.nodes[inspected];
  const start = total === 0 ? 0 : (selection.page - 1) * selection.size + 1;
  const count = document?.groups.reduce((sum, group) => sum + group.rows.length, 0) || 0;
  return (
    <section className="astra-broadcast" aria-label="Broadcast knowledge timeline">
      <header className="astra-broadcast-command">
        <div className="astra-broadcast-title">
          <span>
            <UroIcon name="notifications" />
          </span>
          <div>
            <small>Knowledge timeline</small>
            <h2>Broadcast</h2>
            <p>Trace each observation through the graph.</p>
          </div>
        </div>
        <div className="astra-broadcast-live" data-active={selection.live}>
          <i />
          {selection.live ? "Auto-refresh · 8s" : "Refresh paused"}
          <small>{state.atLatest ? "Latest window" : "Reading history"}</small>
        </div>
      </header>
      <section className="astra-broadcast-overview" aria-label="Broadcast context">
        <div>
          <UroIcon name="records" />
          <span>Stream records</span>
          <strong>{snapshot.readKey ? total.toLocaleString("en-US") : "—"}</strong>
        </div>
        <div>
          <UroIcon name="topology" />
          <span>Links in this window</span>
          <strong>{document ? document.edgeCount : "—"}</strong>
        </div>
        <div>
          <UroIcon name="clock" />
          <span>Reading position</span>
          <strong>
            {selection.page} / {state.pageCount}
          </strong>
        </div>
      </section>
      <div className="astra-broadcast-board">
        <div className="astra-broadcast-controls">
          <label className="astra-broadcast-search">
            <UroIcon name="search" />
            <input
              value={state.queryInput}
              onChange={(event) => state.setQueryInput(event.target.value)}
              aria-label="Search broadcasts"
              placeholder="Search content / source / node id"
            />
            {state.queryInput && (
              <button type="button" aria-label="Clear broadcast search" onClick={() => state.setQueryInput("")}>
                <UroIcon name="close" />
              </button>
            )}
          </label>
          <div className="astra-broadcast-actions">
            <button
              type="button"
              aria-label={
                selection.order === "desc"
                  ? "Currently newest first, click to show oldest first"
                  : "Currently oldest first, click to show newest first"
              }
              onClick={() =>
                select((current) => ({ ...current, page: 1, order: current.order === "desc" ? "asc" : "desc" }))
              }
            >
              <UroIcon name="arrange" />
              {selection.order === "desc" ? "Newest first" : "Oldest first"}
            </button>
            <button
              type="button"
              aria-label={selection.live ? "Pause auto-refresh" : "Resume auto-refresh"}
              aria-pressed={selection.live}
              onClick={() => select((current) => ({ ...current, live: !current.live }))}
            >
              <UroIcon name="pulse" />
              {selection.live ? "Auto-refresh" : "Paused"}
            </button>
          </div>
        </div>
        <fieldset className="astra-broadcast-filters" aria-label="Broadcast kinds">
          {broadcastKinds.map((kind) => {
            const appearance = broadcastAppearance(kind);
            return (
              <button
                key={kind}
                type="button"
                aria-pressed={selection.kinds.includes(kind)}
                data-tone={appearance.tone}
                onClick={() => state.toggleKind(kind)}
              >
                <UroIcon name={appearance.icon} />
                {appearance.label}
              </button>
            );
          })}
          {selection.kinds.length > 0 && (
            <button
              type="button"
              className="astra-broadcast-clear"
              onClick={() => select((current) => ({ ...current, kinds: [], page: 1 }))}
            >
              Clear kinds
            </button>
          )}
          <span>{selection.kinds.length ? `${selection.kinds.length} kinds selected` : "All node kinds"}</span>
        </fieldset>
        {!state.atLatest && snapshot.pending > 0 && (
          <button type="button" className="astra-broadcast-unread" onClick={state.latest}>
            <UroIcon name="arrow-up-right" />
            {snapshot.pending > 99 ? "99+" : snapshot.pending} new broadcasts · back to latest
          </button>
        )}
        {snapshot.error && (
          <div role="alert" className="astra-broadcast-error">
            <UroIcon name="info" />
            <p>
              {snapshot.error}
              {document && <small>Last successful window is retained.</small>}
            </p>
            <button type="button" disabled={snapshot.loading} onClick={() => void state.retry()}>
              Retry broadcast
            </button>
          </div>
        )}
        <div className="astra-broadcast-timeline" aria-busy={snapshot.loading}>
          {!document ? (
            <div className="astra-broadcast-empty">
              <UroIcon name="records" />
              <h3>{snapshot.loading ? "Loading the timeline…" : "Timeline unavailable"}</h3>
              <p>
                {snapshot.loading
                  ? "Reading graph nodes and their context."
                  : "Retry to read this task's graph history."}
              </p>
            </div>
          ) : !count ? (
            <div className="astra-broadcast-empty">
              <UroIcon name="topology" />
              <h3>
                {selection.query || selection.kinds.length
                  ? "No broadcasts match the filters."
                  : "This task hasn't produced any exploration nodes yet."}
              </h3>
              <p>
                {selection.query || selection.kinds.length
                  ? "Adjust the search or node kinds to widen this window."
                  : "Observations will appear here as the task progresses."}
              </p>
            </div>
          ) : (
            document.groups.map((group) => (
              <section key={group.key} className="astra-broadcast-day" aria-label={group.day}>
                <h3>
                  <span>{group.day}</span>
                  <small>{group.rows.length} records in window</small>
                </h3>
                {group.rows.map((node) => (
                  <BroadcastRow
                    key={node.id}
                    node={node}
                    context={document}
                    fresh={snapshot.fresh.has(node.id)}
                    now={state.now}
                    inspect={inspect}
                    inspectorOpen={Boolean(selectedNode)}
                  />
                ))}
              </section>
            ))
          )}
        </div>
        <footer className="astra-broadcast-pagination">
          <label>
            Rows{" "}
            <select
              aria-label="Broadcast page size"
              value={selection.size}
              onChange={(event) => select((current) => ({ ...current, size: Number(event.target.value), page: 1 }))}
            >
              {broadcastSizes.map((size) => (
                <option key={size} value={size}>
                  {size} / page
                </option>
              ))}
            </select>
          </label>
          <span data-broadcast-range>
            {start}–{count ? (selection.page - 1) * selection.size + count : 0} / {total}
          </span>
          <nav aria-label="Broadcast pages">
            <button
              type="button"
              aria-label="Previous broadcast page"
              disabled={selection.page <= 1}
              onClick={() => select((current) => ({ ...current, page: Math.max(1, current.page - 1) }))}
            >
              <UroIcon name="chevron" className="astra-broadcast-previous" />
            </button>
            <span>
              {selection.page} / {state.pageCount}
            </span>
            <button
              type="button"
              aria-label="Next broadcast page"
              disabled={selection.page >= state.pageCount}
              onClick={() => select((current) => ({ ...current, page: Math.min(state.pageCount, current.page + 1) }))}
            >
              <UroIcon name="chevron" />
            </button>
          </nav>
        </footer>
      </div>
      <p className="astra-broadcast-reading-note">
        <UroIcon name="info" />
        {state.atLatest
          ? "The latest window follows the stream."
          : "This history window stays still while new records are counted."}{" "}
        Expand a record to inspect its payload and graph connections.
      </p>
      {selectedNode && document && (
        <EditorDialog title={`Node #${selectedNode.id}`} eyebrow="Broadcast context" close={() => inspect(null)}>
          <Record node={selectedNode} assets={document.assets[selectedNode.id] ?? []} />
        </EditorDialog>
      )}
    </section>
  );
}
