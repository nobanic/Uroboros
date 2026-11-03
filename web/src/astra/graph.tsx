"use client";

import {
  useCallback,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  type ButtonHTMLAttributes,
  type CSSProperties,
  type FormEvent,
  type ReactNode,
} from "react";
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
import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import {
  arrangeEvidence,
  cardSize,
  evidenceMatches,
  evidenceView,
  findingAncestry,
  relationStyle,
  type EvidenceNode,
  type Point,
} from "./graph-engine";
import { GraphInspector } from "./graph-inspector";
import { GraphRecordPreview } from "./graph-record-preview";
import { GraphRecordIndex } from "./graph-record-index";
import { GraphOverviewCard } from "./graph-overview-card";
import { graphPaint } from "./graph-paint";
import {
  graphNeighborhood,
  graphNodeAppearance,
  graphStatistics,
  graphViewStorageKey,
  nextLiveGraphNode,
  readSavedGraphView,
  type GraphMode,
  type GraphLabels,
  type SavedGraphView,
} from "./graph-workbench";
import "./graph.css";

type Card = Node<
  { evidence: EvidenceNode; faded: boolean; matched: boolean; mode: GraphMode; pinned: boolean },
  "evidence"
>;

function EvidenceCard({ data, selected }: NodeProps<Card>) {
  const node = data.evidence,
    meta = graphNodeAppearance(node),
    inherited = Boolean(node.source.inherited);
  const live = !inherited && node.kind === "intent" && node.state === "running";
  return (
    <article
      className="astra-evidence-card"
      data-kind={node.kind}
      data-selected={selected}
      data-muted={data.faded}
      data-match={data.matched}
      data-live={live}
      data-pinned={data.pinned}
      data-evidence={data.mode === "coverage" && ["fact", "finding", "digest"].includes(node.kind)}
      style={{ "--evidence-color": meta.color, width: cardSize.width, height: cardSize.height } as CSSProperties}
      title={node.title}
    >
      <Handle type="target" position={Position.Left} />
      <GraphOverviewCard node={node} />
      <header>
        <span className="astra-graph-node-icon">
          <UroIcon name={meta.icon} aria-hidden="true" />
        </span>
        <strong>{meta.title}</strong>
        {live && <i className="astra-graph-live-dot" title="Active intent" />}
        {data.pinned && (
          <span className="astra-graph-pin" title="Position kept">
            <UroIcon name="panel" aria-hidden="true" />
          </span>
        )}
        {Number(node.source.priority) > 0 && (
          <span className="astra-graph-priority">P{String(node.source.priority)}</span>
        )}
      </header>
      <p>{node.title}</p>
      <footer>
        <span className="astra-graph-state" data-state={node.state}>
          {inherited ? `Source #${node.source.source_task_id}` : node.state || meta.title}
        </span>
        {node.payload.confidence === "inferred" && <span className="astra-graph-inferred">Inferred</span>}
        <code>#{node.id}</code>
      </footer>
      <Handle type="source" position={Position.Right} />
    </article>
  );
}
const cardTypes = { evidence: EvidenceCard };

function GraphButton({
  icon,
  children,
  ...props
}: ButtonHTMLAttributes<HTMLButtonElement> & { icon: UroIconName; children?: ReactNode }) {
  return (
    <button type="button" {...props}>
      <UroIcon name={icon} aria-hidden="true" />
      {children}
    </button>
  );
}

function Workspace({
  graph,
  emptyHint,
  contextKey,
  expanded,
  expand,
}: {
  graph: unknown;
  emptyHint?: string;
  contextKey?: string;
  expanded: boolean;
  expand: () => void;
}) {
  const root = useRef<HTMLDivElement>(null),
    searchInput = useRef<HTMLInputElement>(null);
  const positions = useRef(new Map<string, Point>()),
    frame = useRef(0),
    focusEpoch = useRef(0),
    alive = useRef(true);
  const [mode, setMode] = useState<GraphMode>("topology"),
    [labels, setLabels] = useState<GraphLabels>("auto");
  const [query, setQuery] = useState(""),
    [selected, select] = useState<string>();
  const [grouped, group] = useState(true),
    [arrangement, rearrange] = useState(0);
  const [kind, setKind] = useState("all"),
    [state, setState] = useState("all"),
    [depth, setDepth] = useState(0);
  const [filters, showFilters] = useState(false),
    [help, showHelp] = useState(false),
    [zoom, setZoom] = useState(1);
  const [notice, notify] = useState(""),
    [storageKey, setStorageKey] = useState(""),
    [saved, setSaved] = useState<SavedGraphView>();
  const [reduced, reduceMotion] = useState(false);
  const [previewed, showPreview] = useState<string>();
  const [recordsOpen, showRecords] = useState(false);
  const recordsId = useId();
  const recordsToggle = useRef<HTMLButtonElement>(null);
  const readingStarted = useRef(false);
  const [nodes, updateNodes, onNodesChange] = useNodesState<Card>([]);
  const { fitView, setCenter, zoomIn, zoomOut, getViewport, setViewport, getNodes, viewportInitialized } = useReactFlow<Card, Edge>();
  const paint = graphPaint(zoom);
  const wireSignature = useMemo(() => JSON.stringify(graph), [graph]);
  const view = useMemo(() => evidenceView(graph, grouped), [wireSignature, grouped]);
  const sourceView = useMemo(() => evidenceView(graph, false), [wireSignature]);
  const stats = useMemo(() => graphStatistics(view), [view]);
  const ancestry = useMemo(() => findingAncestry(view), [view]);
  const vicinity = useMemo(() => graphNeighborhood(view, selected, depth), [view, selected, depth]);
  const matches = useMemo(
    () =>
      query.trim() ? [...view.all.values()].filter((node) => evidenceMatches(node, query)).map((node) => node.id) : [],
    [query, view],
  );
  const matching = useMemo(() => new Set(matches.map((id) => view.representatives.get(id) || id)), [matches, view]);
  const topologyKey = useMemo(
    () =>
      JSON.stringify([
        view.nodes.map((node) => node.id).sort(),
        view.links.map((link) => [link.source, link.target, link.relation]).sort(),
      ]),
    [view],
  );
  const layout = useMemo(() => arrangeEvidence(view), [topologyKey, arrangement]);
  const kinds = [...new Set(view.nodes.map((node) => node.kind))].sort(),
    states = [...new Set(view.nodes.map((node) => node.state))].sort();
  const duration = reduced ? 0 : 240;
  const active = selected ? view.all.get(selected) : undefined;
  const preview = !active && !nodes.some((node) => node.dragging) && view.nodes.find((node) => node.id === previewed);

  useEffect(() => {
    alive.current = true;
    const preference = window.matchMedia("(prefers-reduced-motion: reduce)"),
      update = () => reduceMotion(preference.matches);
    update();
    preference.addEventListener("change", update);
    return () => {
      alive.current = false;
      focusEpoch.current++;
      cancelAnimationFrame(frame.current);
      preference.removeEventListener("change", update);
    };
  }, []);
  useEffect(() => {
    const key = graphViewStorageKey(contextKey || `${window.location.pathname}${window.location.search}`);
    setStorageKey(key);
    try {
      setSaved(readSavedGraphView(localStorage.getItem(key)));
    } catch {
      setSaved(undefined);
    }
  }, [contextKey]);

  useEffect(() => {
    for (const id of positions.current.keys()) if (!view.all.has(id)) positions.current.delete(id);
    updateNodes(
      view.nodes.map((node) => ({
        id: node.id,
        type: "evidence",
        width: cardSize.width,
        height: cardSize.height,
        position: positions.current.get(node.id) || layout.get(node.id) || { x: 0, y: 0 },
        selected: selected === node.id,
        data: {
          evidence: node,
          mode,
          pinned: positions.current.has(node.id),
          matched: matching.has(node.id),
          faded:
            (mode === "attack" && !ancestry.has(node.id)) ||
            (kind !== "all" && kind !== node.kind) ||
            (state !== "all" && state !== node.state) ||
            (depth > 0 && !vicinity.has(node.id)),
        },
      })),
    );
    if (selected && !view.all.has(selected)) select(undefined);
    // Keep the view's node list complete; filtering is emphasis, never deletion.
  }, [view, layout, mode, kind, state, selected, matching, ancestry, vicinity, depth, updateNodes]);

  const muted = useMemo(() => new Set(nodes.filter((node) => node.data.faded).map((node) => node.id)), [nodes]);
  const distantLabels = zoom < 0.48;
  const edges: Edge[] = useMemo(
    () =>
      view.links.map((link) => {
        const inPath = ancestry.has(link.source) && ancestry.has(link.target),
          faded = muted.has(link.source) || muted.has(link.target);
        const color = relationStyle[link.relation]?.color || "#6e829f",
          relevant = selected === link.source || selected === link.target;
        return {
          id: JSON.stringify([link.source, link.relation, link.target]),
          source: link.source,
          target: link.target,
          label:
            labels === "none" || (labels === "auto" && distantLabels && !relevant && link.relation !== "proves")
              ? undefined
              : relationStyle[link.relation]?.title || link.relation,
          type: "smoothstep",
          animated:
            !reduced &&
            !faded &&
            (mode === "attack"
              ? inPath
              : view.all.get(link.target)?.state === "running" && !view.all.get(link.target)?.source.inherited),
          className: mode === "attack" && inPath ? "astra-graph-edge-path" : "",
          style: {
            stroke: color,
            opacity: faded ? 0.09 : relevant ? 1 : mode === "coverage" ? 0.4 : 0.68,
            strokeWidth: relevant || (mode === "attack" && inPath) ? "var(--graph-focus-stroke)" : "var(--graph-stroke)",
          },
          labelStyle: { fill: color, fontSize: "var(--graph-relation-font)", fontWeight: 550 },
          labelBgStyle: { fill: "var(--g-surface)", fillOpacity: 0.96 },
          labelBgPadding: [6, 4] as [number, number],
          labelBgBorderRadius: 4,
          markerEnd: { type: MarkerType.ArrowClosed, color, width: 14, height: 14 },
        };
      }),
    [view, ancestry, muted, selected, labels, distantLabels, reduced, mode],
  );

  const choose = useCallback(
    (id: string) => {
      select(id);
      const epoch = ++focusEpoch.current,
        visible = view.representatives.get(id) || id;
      const position = positions.current.get(visible) || layout.get(visible);
      if (!position) return;
      cancelAnimationFrame(frame.current);
      frame.current = requestAnimationFrame(() => {
        frame.current = requestAnimationFrame(() => {
          if (alive.current && epoch === focusEpoch.current)
            void setCenter(position.x + cardSize.width / 2, position.y + cardSize.height / 2, { zoom: 1, duration });
        });
      });
    },
    [view, layout, setCenter, duration],
  );
  const fit = useCallback(
    () => void fitView({ padding: 0.18, minZoom: 0.01, maxZoom: 1, duration }),
    [fitView, duration],
  );
  const followActive = useCallback(() => {
    const id = nextLiveGraphNode(view, selected);
    if (id) choose(id);
  }, [view, selected, choose]);
  const read = useCallback((fromStart = false) => {
    const chosen = !fromStart && selected ? view.representatives.get(selected) || selected : undefined;
    const anchor = (chosen && view.nodes.find((node) => node.id === chosen)) ||
      view.nodes.find((node) => node.state === "origin" || node.kind === "task") || view.nodes[0];
    const area = root.current?.querySelector(".react-flow")?.getBoundingClientRect();
    if (!anchor || !area || area.width < 100 || area.height < 100) return false;
    const position = positions.current.get(anchor.id) || layout.get(anchor.id);
    if (!position) return false;
    const left = area.width < 700 ? 28 : 48;
    void setViewport({
      x: chosen ? area.width / 2 - position.x - cardSize.width / 2 : left - position.x,
      y: area.height / 2 - position.y - cardSize.height / 2,
      zoom: 1,
    }, { duration: fromStart ? 0 : duration });
    if (!fromStart) notify("Full-size view. Drag the canvas or use the minimap to explore.");
    return true;
  }, [selected, view, layout, setViewport, duration]);
  useEffect(() => { readingStarted.current = false; }, [contextKey]);
  useEffect(() => {
    if (readingStarted.current || !viewportInitialized || !nodes.length) return;
    const area = root.current?.querySelector(".react-flow");
    if (!area) return;
    const observer = new ResizeObserver(() => {
      if (!readingStarted.current && read(true)) {
        readingStarted.current = true;
        observer.disconnect();
      }
    });
    observer.observe(area);
    return () => observer.disconnect();
  }, [read, nodes.length, viewportInitialized]);
  const focus = useCallback(() => {
    if (!selected) return;
    setDepth(1);
    const nearby = graphNeighborhood(view, selected, 1);
    void fitView({ nodes: [...nearby].map((id) => ({ id })), padding: 0.3, minZoom: 0.01, maxZoom: 1, duration });
  }, [selected, view, fitView, duration]);
  function find(reverse = false) {
    if (!matches.length) return;
    const index = matches.indexOf(selected || ""),
      next = reverse ? (index <= 0 ? matches.length - 1 : index - 1) : (index + 1) % matches.length;
    choose(matches[next]);
  }
  const submitSearch = (event: FormEvent) => {
    event.preventDefault();
    find();
  };
  const save = useCallback(() => {
    if (!storageKey) return;
    const document: SavedGraphView = {
      version: 1,
      mode,
      labels,
      kind,
      state,
      grouped,
      viewport: getViewport(),
      positions: getNodes().map((node) => [node.id, { ...node.position }]),
    };
    const text = JSON.stringify(document);
    if (!readSavedGraphView(text)) {
      notify("This view is too large to save locally.");
      return;
    }
    try {
      localStorage.setItem(storageKey, text);
      setSaved(document);
      notify("View saved for this graph.");
    } catch {
      notify("Local view could not save. Browser storage may be full or unavailable.");
    }
  }, [storageKey, mode, labels, kind, state, grouped, getViewport, getNodes]);
  function restore() {
    if (!saved) return;
    positions.current = new Map(saved.positions.filter(([id]) => view.all.has(id)));
    setMode(saved.mode);
    setLabels(saved.labels);
    group(saved.grouped);
    setKind(saved.kind === "all" || sourceView.nodes.some((node) => node.kind === saved.kind) ? saved.kind : "all");
    setState(
      saved.state === "all" || sourceView.nodes.some((node) => node.state === saved.state) ? saved.state : "all",
    );
    setDepth(0);
    rearrange((value) => value + 1);
    frame.current = requestAnimationFrame(() => {
      void setViewport(saved.viewport, { duration });
    });
    notify("Saved view restored.");
  }
  function reflow() {
    positions.current.clear();
    rearrange((value) => value + 1);
    notify("Layout recalculated.");
    focusEpoch.current++;
    cancelAnimationFrame(frame.current);
    frame.current = requestAnimationFrame(() => {
      frame.current = requestAnimationFrame(() => {
        if (alive.current) fit();
      });
    });
  }
  function exportGraph() {
    const url = URL.createObjectURL(new Blob([JSON.stringify(graph, null, 2)], { type: "application/json" })),
      anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = "uroboros-investigation.json";
    anchor.click();
    window.setTimeout(() => URL.revokeObjectURL(url), 1000);
  }
  useEffect(() => {
    const keyboard = (event: KeyboardEvent) => {
      if (!root.current?.contains(document.activeElement) && !expanded) return;
      const target = event.target as HTMLElement;
      if (expanded && !root.current?.contains(target) && target.closest("dialog[open]")) return;
      if (expanded && event.key === "Tab") {
        const controls = [
          ...root.current!.querySelectorAll<HTMLElement>(
            "a[href],button:not(:disabled),input:not(:disabled),select:not(:disabled),textarea:not(:disabled),[tabindex='0']",
          ),
        ].filter((node) => node.getClientRects().length > 0);
        const first = controls[0],
          last = controls.at(-1);
        if (
          event.shiftKey &&
          (document.activeElement === first || !controls.includes(document.activeElement as HTMLElement))
        ) {
          event.preventDefault();
          last?.focus();
        } else if (!event.shiftKey && document.activeElement === last) {
          event.preventDefault();
          first?.focus();
        }
        return;
      }
      if (event.key === "Escape") {
        if (help) showHelp(false);
        else if (expanded) expand();
        else {
          select(undefined);
          setDepth(0);
        }
        event.preventDefault();
        return;
      }
      if (target.closest("input,textarea,select,[contenteditable=true]")) return;
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
        event.preventDefault();
        save();
        return;
      }
      if (event.ctrlKey || event.metaKey || event.altKey) return;
      if (event.key === "/") {
        event.preventDefault();
        searchInput.current?.focus();
      } else if (event.key === "1") {
        event.preventDefault();
        read();
      } else if (event.key.toLowerCase() === "f") {
        event.preventDefault();
        fit();
      } else if (event.key.toLowerCase() === "n" && selected) {
        event.preventDefault();
        focus();
      } else if (event.key.toLowerCase() === "l") {
        event.preventDefault();
        followActive();
      } else if (event.key.toLowerCase() === "m") {
        event.preventDefault();
        expand();
      } else if (event.key === "+" || event.key === "=") {
        event.preventDefault();
        void zoomIn({ duration });
      } else if (event.key === "-") {
        event.preventDefault();
        void zoomOut({ duration });
      } else if (event.key === "?") {
        event.preventDefault();
        showHelp((value) => !value);
      }
    };
    window.addEventListener("keydown", keyboard);
    return () => window.removeEventListener("keydown", keyboard);
  }, [expanded, help, expand, save, fit, read, focus, followActive, selected, zoomIn, zoomOut, duration]);

  return (
    <div
      ref={root}
      className="astra-evidence-workspace"
      data-mode={mode}
      data-zoom-tier={paint.tier}
      style={paint.properties as CSSProperties}
      tabIndex={0}
      aria-label="Investigation graph workspace"
      onFocusCapture={(event) =>
        showPreview(event.target.closest(".react-flow__node")?.getAttribute("data-id") || undefined)
      }
      onBlurCapture={(event) => {
        if (!event.currentTarget.contains(event.relatedTarget)) showPreview(undefined);
      }}
    >
      <header className="astra-evidence-toolbar">
        <div className="astra-graph-command-row">
          <div className="astra-graph-identity">
            <span>
              <UroIcon name="topology" aria-hidden="true" />
            </span>
            <div>
              <small>Investigation topology</small>
              <strong>Exploration graph</strong>
            </div>
            {stats.live > 0 && (
              <button
                type="button"
                className="astra-graph-live-count"
                aria-label={`Follow active graph work (${stats.live} active)`}
                title="Next active intent · L"
                onClick={followActive}
              >
                <i />
                {stats.live} active
                <span aria-hidden="true">→</span>
              </button>
            )}
          </div>
          <div className="astra-graph-mode-control" role="toolbar" aria-label="Graph view mode">
            {(["topology", "attack", "coverage"] as const).map((value) => (
              <button key={value} aria-pressed={mode === value} onClick={() => setMode(value)}>
                <UroIcon
                  name={value === "topology" ? "topology" : value === "attack" ? "route" : "coverage"}
                  aria-hidden="true"
                />
                {value === "attack" ? "Attack path" : value}
              </button>
            ))}
          </div>
          <div className="astra-graph-window-controls">
            <button
              type="button"
              ref={recordsToggle}
              className="astra-graph-record-toggle"
              aria-label="Browse graph records"
              title="Browse full graph records"
              aria-controls={recordsId}
              aria-expanded={recordsOpen}
              onClick={() => showRecords((value) => !value)}
            >
              <UroIcon name="records" aria-hidden="true" /><span>Records</span>
            </button>
            <GraphButton
              icon="info"
              aria-label="Graph keyboard shortcuts"
              aria-expanded={help}
              onClick={() => showHelp((value) => !value)}
            />
            <GraphButton
              icon="panel"
              aria-label={expanded ? "Restore graph size" : "Maximize graph"}
              title="Maximize graph · M"
              onClick={expand}
            />
          </div>
        </div>
        <div className="astra-graph-command-row">
          <form onSubmit={submitSearch} className="astra-graph-search">
            <UroIcon name="search" aria-hidden="true" />
            <input
              ref={searchInput}
              aria-label="Search graph nodes"
              placeholder="Find node, state or evidence…"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
            <button
              type="button"
              disabled={!matches.length}
              aria-label="Previous graph match"
              onClick={() => find(true)}
            >
              ↑
            </button>
            <button disabled={!matches.length}>{matches.length ? `${matches.length} matches` : "Find"}</button>
          </form>
          <div className="astra-graph-zoom">
            <GraphButton icon="zoom-out" aria-label="Zoom out graph" onClick={() => void zoomOut({ duration })} />
            <span aria-label="Graph zoom">{Math.round(zoom * 100)}%</span>
            <GraphButton icon="zoom-in" aria-label="Zoom in graph" onClick={() => void zoomIn({ duration })} />
          </div>
          <GraphButton icon="fit" title="Fit graph · F" onClick={fit}>
            Fit
          </GraphButton>
          <GraphButton icon="zoom-in" aria-label="Read graph at full size" title="Read at full size · 1" onClick={() => read()}>
            Read
          </GraphButton>
          <GraphButton icon="arrange" onClick={reflow}>
            Reflow
          </GraphButton>
          <GraphButton
            icon="filter"
            aria-expanded={filters}
            aria-pressed={kind !== "all" || state !== "all" || depth > 0}
            onClick={() => showFilters((value) => !value)}
          >
            Filters
          </GraphButton>
          <GraphButton icon="workspace" aria-pressed={grouped} onClick={() => group((value) => !value)}>
            Group digests
          </GraphButton>
        </div>
        {filters && (
          <div className="astra-graph-filter-row" role="group" aria-label="Graph filters">
            <label>
              Kind
              <select aria-label="Graph kind filter" value={kind} onChange={(event) => setKind(event.target.value)}>
                <option value="all">All</option>
                {kinds.map((value) => (
                  <option key={value}>{value}</option>
                ))}
              </select>
            </label>
            <label>
              State
              <select aria-label="Graph state filter" value={state} onChange={(event) => setState(event.target.value)}>
                <option value="all">All</option>
                {states.map((value) => (
                  <option key={value}>{value}</option>
                ))}
              </select>
            </label>
            <label>
              Connections
              <select
                aria-label="Graph connection focus"
                value={depth}
                disabled={!selected}
                onChange={(event) => setDepth(Number(event.target.value))}
              >
                <option value={0}>Entire graph</option>
                <option value={1}>Direct neighbors</option>
                <option value={2}>Two hops</option>
              </select>
            </label>
            <label>
              Relation labels
              <select
                aria-label="Graph relation labels"
                value={labels}
                onChange={(event) => setLabels(event.target.value as GraphLabels)}
              >
                <option value="auto">Automatic</option>
                <option value="all">All</option>
                <option value="none">None</option>
              </select>
            </label>
            <button
              onClick={() => {
                setKind("all");
                setState("all");
                setDepth(0);
              }}
            >
              Reset filters
            </button>
          </div>
        )}
      </header>
      <div className="astra-graph-canvas">
        {recordsOpen && (
          <GraphRecordIndex
            id={recordsId}
            view={view}
            query={query}
            onQuery={setQuery}
            onChoose={(id) => {
              showRecords(false);
              choose(id);
            }}
            onClose={() => {
              showRecords(false);
              recordsToggle.current?.focus();
            }}
          />
        )}
        <ReactFlow<Card, Edge>
          nodes={nodes}
          edges={edges}
          nodeTypes={cardTypes}
          onNodesChange={onNodesChange}
          onNodeMouseEnter={(_, node) => showPreview(node.id)}
          onNodeMouseLeave={() =>
            showPreview(document.activeElement?.closest(".react-flow__node")?.getAttribute("data-id") || undefined)
          }
          onNodeDrag={(_, node) => {
            positions.current.set(node.id, { ...node.position });
          }}
          onNodeDragStop={(_, node) => {
            positions.current.set(node.id, { ...node.position });
            updateNodes((current) =>
              current.map((item) => (item.id === node.id ? { ...item, data: { ...item.data, pinned: true } } : item)),
            );
          }}
          onNodeClick={(_, node) => select(node.id)}
          onPaneClick={() => {
            select(undefined);
            setDepth(0);
          }}
          onMove={(_, viewport) => setZoom(viewport.zoom)}
          minZoom={0.01}
          maxZoom={2.5}
          panOnScroll
          selectionOnDrag
          nodesConnectable={false}
          onlyRenderVisibleElements
          proOptions={{ hideAttribution: true }}
          defaultEdgeOptions={{ interactionWidth: 18 }}
        >
          <Background variant={BackgroundVariant.Dots} gap={24} size={1} color="var(--g-grid)" />
          {preview && (
            <Panel
              position="top-left"
              className="astra-graph-preview"
              role="note"
              aria-label="Graph record preview"
              data-node-id={preview.id}
            >
              <GraphRecordPreview
                node={preview}
                connections={view.links.filter((link) => link.source === preview.id || link.target === preview.id).length}
              />
            </Panel>
          )}
          <Panel position="bottom-left" className="astra-evidence-legend">
            <div className="astra-graph-counters">
              <span>
                <strong>{stats.visible}</strong> nodes
              </span>
              <span>
                <strong>{stats.paths}</strong> paths
              </span>
              <span>
                <strong>{stats.findings}</strong> findings
              </span>
              {mode === "coverage" && <span>{stats.evidence} evidence records</span>}
            </div>
            <div>
              {["begin", "goal", "intent", "fact", "finding", "digest"].map((value) => {
                const meta = graphNodeAppearance({ kind: value, state: "" } as EvidenceNode);
                return (
                  <button
                    key={value}
                    title={`Emphasize ${meta.title.toLowerCase()} nodes`}
                    onClick={() => {
                      setKind(value === "begin" ? "fact" : value);
                      showFilters(true);
                    }}
                  >
                    <i style={{ backgroundColor: meta.color }} />
                    {meta.title}
                  </button>
                );
              })}
            </div>
          </Panel>
          <MiniMap
            pannable
            zoomable
            position="bottom-right"
            nodeColor={(node) => graphNodeAppearance((node.data as Card["data"]).evidence).color}
            nodeStrokeWidth={1}
            nodeBorderRadius={4}
            maskColor="var(--g-minimap-mask)"
          />
          {!view.nodes.length && (
            <Panel position="top-center" className="astra-evidence-empty">
              <UroIcon name="topology" aria-hidden="true" />
              <strong>Waiting for graph context</strong>
              <p>{emptyHint || "This operation has no graph nodes yet."}</p>
            </Panel>
          )}
        </ReactFlow>
        {active && (
          <GraphInspector
            node={active}
            view={sourceView}
            choose={choose}
            focus={focus}
            close={() => {
              select(undefined);
              setDepth(0);
            }}
          />
        )}
      </div>
      <footer className="astra-graph-status">
        <span>
          <UroIcon name="pulse" aria-hidden="true" />
          {stats.goals ? `${stats.metGoals}/${stats.goals} goals met` : "No goals recorded"}
          {stats.blocked > 0 ? ` · ${stats.blocked} blocked intents` : ""}
        </span>
        <p role="status">{notice}</p>
        <div>
          <button disabled={!storageKey} onClick={save}>
            Save view
          </button>
          <button disabled={!saved} onClick={restore}>
            Restore view
          </button>
          <button onClick={exportGraph}>Export</button>
        </div>
      </footer>
      {help && (
        <aside className="astra-graph-help" role="dialog" aria-label="Graph keyboard shortcuts">
          <header>
            <h3>Graph controls</h3>
            <button aria-label="Close graph shortcuts" onClick={() => showHelp(false)}>
              ×
            </button>
          </header>
          <p>Shortcuts apply while this graph has focus.</p>
          <dl>
            {[
              ["/", "Search nodes and evidence"],
              ["F", "Fit entire graph"],
              ["1", "Read at full size"],
              ["+ / −", "Zoom"],
              ["N", "Focus selected connections"],
              ["L", "Next active intent"],
              ["M", "Maximize canvas"],
              ["Ctrl / ⌘ S", "Save this view"],
              ["Esc", "Close focus or expanded view"],
            ].map(([key, text]) => (
              <div key={key}>
                <dt>
                  <kbd>{key}</kbd>
                </dt>
                <dd>{text}</dd>
              </div>
            ))}
          </dl>
          <p>Drag nodes to keep their positions. Filters emphasize context without removing graph records.</p>
        </aside>
      )}
    </div>
  );
}

export function EvidenceGraph({
  graph,
  className = "",
  emptyHint,
  contextKey,
}: {
  graph: unknown;
  className?: string;
  emptyHint?: string;
  contextKey?: string;
}) {
  const [expanded, setExpanded] = useState(false),
    toggle = useCallback(() => setExpanded((value) => !value), []);
  useEffect(() => {
    if (!expanded) return;
    const prior = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.body.style.overflow = prior;
    };
  }, [expanded]);
  return (
    <div className={`astra-evidence-surface ${expanded ? "is-expanded" : ""} ${className}`}>
      <ReactFlowProvider>
        <Workspace graph={graph} emptyHint={emptyHint} contextKey={contextKey} expanded={expanded} expand={toggle} />
      </ReactFlowProvider>
    </div>
  );
}
