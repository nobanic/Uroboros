"use client";

import { useCallback, useEffect, useMemo, useRef, useState, type CSSProperties, type FormEvent } from "react";
import {
  Background,
  BackgroundVariant,
  Handle,
  MarkerType,
  MiniMap,
  Panel,
  Position,
  ReactFlow,
  ReactFlowProvider,
  useNodesState,
  useReactFlow,
  type Edge,
  type Node,
  type NodeProps,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { UroIcon } from "@/components/uroboros-icons";
import type { CoverageGraphData, CoverageGraphNode } from "@/lib/types";
import { ApiFailure } from "./client";
import { cardSize, type Point } from "./graph-engine";
import {
  coverageBatch,
  coverageKinds,
  coverageLabel,
  coverageMatches,
  coverageStatus,
  coverageTotals,
  layoutCoverage,
  projectCoverage,
  revealCoverage,
  type CoverageFold,
  type CoverageMode,
} from "./coverage-document";
import { CoverageInspector, CoverageFoldInspector } from "./coverage-inspector";
import { useConsoleResource } from "./use-console-resource";
import "./graph.css";
import "./coverage.css";

type CoverageCard = Node<
  { asset?: CoverageGraphNode; fold?: CoverageFold; mode: CoverageMode; tracking: boolean; match: boolean },
  "coverage"
>;
function AssetCard({ data, selected }: NodeProps<CoverageCard>) {
  const kind = data.asset?.kind || data.fold!.group.kind,
    meta = coverageKinds[kind] || coverageKinds.endpoint;
  const status = data.asset ? coverageStatus(data.asset, data.tracking) : { key: "fold", title: "Collapsed branch" };
  const muted = data.mode === "gaps" ? status.key !== "gap" : data.mode === "tested" ? status.key !== "tested" : false;
  return (
    <article
      className="astra-evidence-card astra-coverage-card"
      data-selected={selected}
      data-muted={muted}
      data-match={data.match}
      data-status={status.key}
      style={{ "--evidence-color": meta.color, width: cardSize.width, height: cardSize.height } as CSSProperties}
      title={data.asset?.label}
    >
      <Handle type="target" position={Position.Left} />
      <header>
        <span className="astra-graph-node-icon">
          <UroIcon name={data.fold ? "plus" : meta.icon} aria-hidden="true" />
        </span>
        <strong>{meta.title}</strong>
      </header>
      <p>
        {data.asset ? coverageLabel(data.asset) : `${data.fold!.hidden.length} more ${meta.title.toLowerCase()} assets`}
      </p>
      <footer>
        <span className="astra-graph-state" data-state={status.key}>
          {status.title}
        </span>
        <code>{data.asset?.asset_id ? `A${data.asset.asset_id}` : data.asset?.key}</code>
      </footer>
      <Handle type="source" position={Position.Right} />
    </article>
  );
}
const types = { coverage: AssetCard };

function Surface({
  taskId,
  tracking,
  onUnauthorized,
  revision,
}: {
  taskId: string;
  tracking: boolean;
  onUnauthorized?: () => void;
  revision: number;
}) {
  const result = useConsoleResource<CoverageGraphData>(`/tasks/${encodeURIComponent(taskId)}/coverage-graph`, revision);
  const [expanded, setExpanded] = useState(new Map<string, number>()),
    [selected, select] = useState<string>();
  const [query, search] = useState(""),
    [mode, setMode] = useState<CoverageMode>("surface"),
    [arrangement, reflow] = useState(0);
  const [zoom, setZoom] = useState(1),
    [maximized, maximize] = useState(false),
    [reduced, reduce] = useState(false);
  const root = useRef<HTMLDivElement>(null),
    searchInput = useRef<HTMLInputElement>(null),
    positions = useRef(new Map<string, Point>());
  const pendingFocus = useRef<string | undefined>(undefined),
    frame = useRef(0);
  const [nodes, updateNodes, onNodesChange] = useNodesState<CoverageCard>([]);
  const { fitView, setCenter, zoomIn, zoomOut } = useReactFlow<CoverageCard, Edge>();
  const signature = useMemo(() => JSON.stringify(result.data), [result.data]);
  const projection = useMemo(() => projectCoverage(result.data, expanded, tracking), [signature, expanded, tracking]);
  const topology = useMemo(
    () =>
      JSON.stringify([
        projection.visible.map((node) => node.key).sort(),
        projection.folds.map((fold) => fold.id).sort(),
        projection.links.map((link) => [link.source, link.target]).sort(),
      ]),
    [projection],
  );
  const layout = useMemo(() => layoutCoverage(projection), [topology, arrangement]);
  const totals = coverageTotals(projection, tracking),
    effectiveMode = tracking ? mode : "surface",
    duration = reduced ? 0 : 230;
  const matches = useMemo(
    () =>
      query.trim()
        ? [...projection.all.values()].filter((node) => coverageMatches(node, query)).map((node) => node.key)
        : [],
    [projection, query],
  );
  const matched = useMemo(() => new Set(matches), [matches]);
  const selectedAsset = selected ? projection.all.get(selected) : undefined,
    selectedFold = projection.folds.find((fold) => fold.id === selected);
  const fit = useCallback(
    () => void fitView({ minZoom: 0.01, maxZoom: 1, padding: 0.18, duration }),
    [fitView, duration],
  );
  useEffect(() => {
    const preference = window.matchMedia("(prefers-reduced-motion: reduce)"),
      update = () => reduce(preference.matches);
    update();
    preference.addEventListener("change", update);
    return () => {
      preference.removeEventListener("change", update);
      cancelAnimationFrame(frame.current);
    };
  }, []);
  useEffect(() => {
    if (result.error instanceof ApiFailure && result.error.status === 401) onUnauthorized?.();
  }, [result.error, onUnauthorized]);
  useEffect(() => {
    if (!maximized) return;
    const previous = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = previous;
    };
  }, [maximized]);
  useEffect(() => {
    for (const key of positions.current.keys())
      if (!projection.all.has(key) && !projection.folds.some((fold) => fold.id === key)) positions.current.delete(key);
    updateNodes([
      ...projection.visible.map((asset) => ({
        id: asset.key,
        type: "coverage" as const,
        position: positions.current.get(asset.key) || layout.get(asset.key) || { x: 0, y: 0 },
        selected: selected === asset.key,
        data: { asset, mode: effectiveMode, tracking, match: matched.has(asset.key) },
      })),
      ...projection.folds.map((fold) => ({
        id: fold.id,
        type: "coverage" as const,
        position: positions.current.get(fold.id) || layout.get(fold.id) || { x: 0, y: 0 },
        selected: selected === fold.id,
        data: { fold, mode: effectiveMode, tracking, match: fold.hidden.some((node) => matched.has(node.key)) },
      })),
    ]);
    if (selected && !projection.all.has(selected) && !projection.folds.some((fold) => fold.id === selected))
      select(undefined);
  }, [projection, layout, effectiveMode, tracking, matched, selected, updateNodes]);
  useEffect(() => {
    cancelAnimationFrame(frame.current);
    frame.current = requestAnimationFrame(() => {
      frame.current = requestAnimationFrame(() => {
        if (!pendingFocus.current) fit();
      });
    });
    return () => cancelAnimationFrame(frame.current);
  }, [topology, arrangement, fit]);
  useEffect(() => {
    const key = pendingFocus.current,
      point = key && (positions.current.get(key) || layout.get(key));
    if (!point) return;
    pendingFocus.current = undefined;
    cancelAnimationFrame(frame.current);
    frame.current = requestAnimationFrame(() => {
      frame.current = requestAnimationFrame(
        () => void setCenter(point.x + cardSize.width / 2, point.y + cardSize.height / 2, { zoom: 1, duration }),
      );
    });
    return () => cancelAnimationFrame(frame.current);
  }, [selected, layout, setCenter, duration]);
  const edges: Edge[] = useMemo(
    () =>
      projection.links.map((link) => {
        const child = projection.all.get(link.target),
          status = child && coverageStatus(child, tracking);
        const muted =
          effectiveMode === "gaps"
            ? status?.key !== "gap"
            : effectiveMode === "tested"
              ? status?.key !== "tested"
              : false;
        const color = status?.key === "gap" ? "#d89a50" : status?.key === "tested" ? "#35b9a1" : "#61799b";
        return {
          id: JSON.stringify([link.source, link.target]),
          ...link,
          type: "smoothstep",
          animated: !reduced && effectiveMode === "gaps" && !muted,
          style: {
            stroke: color,
            strokeWidth: !muted && effectiveMode === "gaps" ? 2 : 1.3,
            opacity: muted ? 0.1 : 0.65,
          },
          markerEnd: { type: MarkerType.ArrowClosed, color, width: 13, height: 13 },
        };
      }),
    [projection, tracking, effectiveMode, reduced],
  );
  function choose(key: string) {
    pendingFocus.current = key;
    setExpanded((current) => revealCoverage(projection, key, current));
    select(key);
  }
  function find(event: FormEvent) {
    event.preventDefault();
    if (matches.length) choose(matches[(matches.indexOf(selected || "") + 1) % matches.length]);
  }
  function reveal(fold: CoverageFold) {
    setExpanded((current) =>
      new Map(current).set(fold.group.id, (current.get(fold.group.id) || coverageBatch) + coverageBatch),
    );
  }
  function exportGraph() {
    const url = URL.createObjectURL(new Blob([JSON.stringify(result.data, null, 2)], { type: "application/json" })),
      anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = `uroboros-coverage-${taskId}.json`;
    anchor.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  useEffect(() => {
    const keyboard = (event: KeyboardEvent) => {
      if (
        (!maximized && !root.current?.contains(document.activeElement)) ||
        (event.target instanceof Element && event.target.closest("dialog[open]"))
      )
        return;
      if (event.key === "Escape") {
        if (maximized) maximize(false);
        else select(undefined);
        event.preventDefault();
        return;
      }
      if (maximized && event.key === "Tab") {
        const controls = [
          ...root.current!.querySelectorAll<HTMLElement>("button:not(:disabled),input,select,[tabindex='0'],a[href]"),
        ].filter((node) => node.getClientRects().length);
        if (event.shiftKey && document.activeElement === controls[0]) {
          event.preventDefault();
          controls.at(-1)?.focus();
        } else if (!event.shiftKey && document.activeElement === controls.at(-1)) {
          event.preventDefault();
          controls[0]?.focus();
        }
        return;
      }
      if (
        (event.target as Element).closest("input,textarea,select,[contenteditable=true]") ||
        event.ctrlKey ||
        event.metaKey ||
        event.altKey
      )
        return;
      if (event.key === "/") {
        event.preventDefault();
        searchInput.current?.focus();
      } else if (event.key.toLowerCase() === "f") {
        event.preventDefault();
        fit();
      } else if (event.key.toLowerCase() === "m") {
        event.preventDefault();
        maximize((value) => !value);
      }
    };
    window.addEventListener("keydown", keyboard);
    return () => window.removeEventListener("keydown", keyboard);
  }, [maximized, fit]);
  return (
    <div className={`astra-evidence-surface astra-coverage-surface ${maximized ? "is-expanded" : ""}`}>
      <div
        ref={root}
        className="astra-evidence-workspace"
        tabIndex={0}
        aria-label="Asset coverage graph workspace"
        data-mode={effectiveMode}
      >
        <header className="astra-evidence-toolbar">
          <div className="astra-graph-command-row">
            <div className="astra-graph-identity">
              <span>
                <UroIcon name="coverage" aria-hidden="true" />
              </span>
              <div>
                <small>Attack surface · {tracking ? "coverage tracking" : "tracking off"}</small>
                <strong>Coverage lattice</strong>
              </div>
              <b className="astra-coverage-score">{totals.percent === undefined ? "—" : `${totals.percent}%`}</b>
            </div>
            <div className="astra-graph-mode-control" role="toolbar" aria-label="Coverage view mode">
              {(["surface", ...(tracking ? ["gaps", "tested"] : [])] as CoverageMode[]).map((value) => (
                <button key={value} aria-pressed={effectiveMode === value} onClick={() => setMode(value)}>
                  {value === "surface" ? "All surface" : value === "gaps" ? "Gaps" : "Tested"}
                </button>
              ))}
            </div>
            <div className="astra-graph-window-controls">
              <button
                onClick={() => maximize((value) => !value)}
                aria-label={maximized ? "Restore coverage size" : "Maximize coverage graph"}
              >
                <UroIcon name="panel" aria-hidden="true" />
              </button>
            </div>
          </div>
          <div className="astra-graph-command-row">
            <form className="astra-graph-search" onSubmit={find}>
              <UroIcon name="search" aria-hidden="true" />
              <input
                ref={searchInput}
                aria-label="Search coverage assets"
                placeholder="Find host, route, service or asset ID…"
                value={query}
                onChange={(event) => search(event.target.value)}
              />
              <button disabled={!matches.length}>{matches.length ? `${matches.length} matches` : "Find"}</button>
            </form>
            <div className="astra-graph-zoom">
              <button onClick={() => void zoomOut({ duration })} aria-label="Zoom out coverage">
                <UroIcon name="zoom-out" aria-hidden="true" />
              </button>
              <span>{Math.round(zoom * 100)}%</span>
              <button onClick={() => void zoomIn({ duration })} aria-label="Zoom in coverage">
                <UroIcon name="zoom-in" aria-hidden="true" />
              </button>
            </div>
            <button onClick={fit}>
              <UroIcon name="fit" aria-hidden="true" />
              Fit
            </button>
            <button
              onClick={() => {
                positions.current.clear();
                reflow((value) => value + 1);
              }}
            >
              <UroIcon name="arrange" aria-hidden="true" />
              Reflow
            </button>
            <button onClick={() => void result.reload()} disabled={result.busy}>
              <UroIcon name="sync" aria-hidden="true" />
              {result.busy ? "Refreshing…" : "Refresh"}
            </button>
          </div>
        </header>
        {result.error && (
          <div className="astra-coverage-error" role="alert">
            {result.error.message} · {result.data ? "Last loaded surface retained." : "Use Refresh to retry."}
          </div>
        )}
        <div className="astra-graph-canvas">
          <ReactFlow<CoverageCard, Edge>
            nodes={nodes}
            edges={edges}
            nodeTypes={types}
            onNodesChange={onNodesChange}
            onNodeClick={(_, node) => select(node.id)}
            onPaneClick={() => select(undefined)}
            onNodeDrag={(_, node) => positions.current.set(node.id, { ...node.position })}
            onNodeDragStop={(_, node) => positions.current.set(node.id, { ...node.position })}
            onMove={(_, viewport) => setZoom(viewport.zoom)}
            nodesConnectable={false}
            onlyRenderVisibleElements
            panOnScroll
            selectionOnDrag
            minZoom={0.01}
            maxZoom={2.5}
            proOptions={{ hideAttribution: true }}
          >
            <Background variant={BackgroundVariant.Dots} gap={24} size={1} color="var(--g-grid)" />
            <MiniMap
              pannable
              zoomable
              nodeColor={(node) => {
                const data = node.data as CoverageCard["data"];
                return coverageKinds[data.asset?.kind || data.fold!.group.kind]?.color || "#8198bb";
              }}
              nodeBorderRadius={4}
              maskColor="var(--g-minimap-mask)"
            />
            <Panel position="bottom-left" className="astra-evidence-legend">
              <div className="astra-graph-counters">
                <span>
                  <strong>{totals.inScope}</strong> in scope
                </span>
                {tracking ? (
                  <>
                    <span>
                      <strong>{totals.tested}</strong> tested
                    </span>
                    <span>
                      <strong>{totals.gaps}</strong> gaps
                    </span>
                  </>
                ) : (
                  <span>Tracking off</span>
                )}
                <span>
                  <strong>{totals.visible}</strong> visible
                </span>
                {totals.folded > 0 && (
                  <span>
                    <strong>{totals.folded}</strong> folded
                  </span>
                )}
              </div>
              <div>
                {Object.entries(coverageKinds).map(([kind, meta]) => (
                  <span key={kind}>
                    <i style={{ backgroundColor: meta.color }} />
                    {meta.title}
                  </span>
                ))}
              </div>
            </Panel>
            {!projection.all.size && (
              <Panel position="top-center" className="astra-evidence-empty">
                <UroIcon name="assets" aria-hidden="true" />
                <strong>{result.busy ? "Mapping attack surface" : "No coverage surface yet"}</strong>
                <p>Scoped assets and their connector context appear here.</p>
              </Panel>
            )}
          </ReactFlow>
          {selectedAsset && (
            <CoverageInspector
              node={selectedAsset}
              taskId={taskId}
              tracking={tracking}
              close={() => select(undefined)}
            />
          )}
          {selectedFold && (
            <CoverageFoldInspector
              fold={selectedFold}
              tracking={tracking}
              choose={choose}
              reveal={() => reveal(selectedFold)}
              close={() => select(undefined)}
            />
          )}
        </div>
        <footer className="astra-graph-status">
          <span>
            <UroIcon name="shield" aria-hidden="true" />
            {tracking ? "Fact anchors define tested status" : "Coverage tracking is off"}
          </span>
          <div>
            <button onClick={() => setExpanded(new Map())}>Collapse branches</button>
            <button disabled={!result.data} onClick={exportGraph}>
              Export surface
            </button>
          </div>
        </footer>
      </div>
    </div>
  );
}

export function CoverageWorkspace({
  taskId,
  coverageEnabled = true,
  onUnauthorized,
  revision = 0,
}: {
  taskId: string;
  coverageEnabled?: boolean;
  onUnauthorized?: () => void;
  revision?: number;
}) {
  return (
    <ReactFlowProvider key={taskId}>
      <Surface taskId={taskId} tracking={coverageEnabled} onUnauthorized={onUnauthorized} revision={revision} />
    </ReactFlowProvider>
  );
}
