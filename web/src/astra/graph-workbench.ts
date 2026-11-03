import type { UroIconName } from "@/components/uroboros-icons";
import type { EvidenceNode, EvidenceView, Point } from "./graph-engine";

export type GraphMode = "topology" | "attack" | "coverage";
export type GraphLabels = "auto" | "all" | "none";
export type GraphCamera = Point & { zoom: number };
export type SavedGraphView = {
  version: 1;
  mode: GraphMode;
  labels: GraphLabels;
  kind: string;
  state: string;
  grouped: boolean;
  viewport: GraphCamera;
  positions: Array<[string, Point]>;
};

const appearance: Record<string, { title: string; icon: UroIconName; color: string }> = {
  task: { title: "Mission", icon: "mission", color: "#86a8da" },
  begin: { title: "Starting point", icon: "route", color: "#94aecf" },
  goal: { title: "Goal", icon: "tasks", color: "#47d7aa" },
  intent: { title: "Intent", icon: "route", color: "#659aff" },
  fact: { title: "Fact", icon: "records", color: "#f3bb70" },
  finding: { title: "Finding", icon: "findings", color: "#ff8196" },
  hint: { title: "Hint", icon: "info", color: "#b89cff" },
  digest: { title: "Digest", icon: "workspace", color: "#5bd5dc" },
};

export function graphNodeAppearance(node: EvidenceNode) {
  return appearance[node.state === "origin" ? "begin" : node.kind] || appearance.intent;
}

export function graphStatistics(view: EvidenceView) {
  const live = view.nodes.filter(
    (node) => node.kind === "intent" && node.state === "running" && !node.source.inherited,
  ).length;
  return {
    visible: view.nodes.length,
    total: view.all.size,
    paths: view.links.length,
    live,
    findings: view.nodes.filter((node) => node.kind === "finding").length,
    goals: view.nodes.filter((node) => node.kind === "goal").length,
    metGoals: view.nodes.filter((node) => node.kind === "goal" && node.state === "met").length,
    blocked: view.nodes.filter((node) => node.kind === "intent" && node.state === "blocked").length,
    evidence: view.nodes.filter((node) => ["fact", "finding", "digest"].includes(node.kind)).length,
  };
}

// Follow only visible local work, in the existing record order. This is camera
// navigation: it never changes a record, task state or execution authority.
export function nextLiveGraphNode(view: EvidenceView, selected?: string): string | undefined {
  const live = view.nodes.filter(
    (node) => node.kind === "intent" && node.state === "running" && !node.source.inherited,
  );
  if (!live.length) return;
  const current = live.findIndex((node) => node.id === selected);
  return live[(current + 1) % live.length].id;
}

// Focus is an undirected view of actual recorded relations, not an inferred
// evidence/proof edge. Digest members focus their visible representative.
export function graphNeighborhood(view: EvidenceView, selected: string | undefined, depth: number): Set<string> {
  if (!selected || depth <= 0) return new Set(view.nodes.map((node) => node.id));
  const root = view.representatives.get(selected) || selected;
  const adjacency = new Map(view.nodes.map((node) => [node.id, [] as string[]]));
  if (!adjacency.has(root)) return new Set();
  for (const edge of view.links) {
    adjacency.get(edge.source)?.push(edge.target);
    adjacency.get(edge.target)?.push(edge.source);
  }
  const included = new Set([root]),
    queue: Array<[string, number]> = [[root, 0]];
  for (let index = 0; index < queue.length; index++) {
    const [id, distance] = queue[index];
    if (distance >= depth) continue;
    for (const next of adjacency.get(id) || [])
      if (!included.has(next)) {
        included.add(next);
        queue.push([next, distance + 1]);
      }
  }
  return included;
}

export function graphReference(node: EvidenceNode): string {
  const finding = node.source.finding_id ?? node.payload.finding_id;
  if (
    node.kind === "finding" &&
    (typeof finding === "string" || typeof finding === "number") &&
    String(finding).trim()
  ) {
    return `/function/findings/detail?id=${encodeURIComponent(String(finding))}`;
  }
  const task = node.source.source_task_id;
  return node.source.inherited && (typeof task === "string" || typeof task === "number") && String(task).trim()
    ? `/function/tasks/detail?id=${encodeURIComponent(String(task))}`
    : "";
}

export function graphViewStorageKey(context: string) {
  return `astra.graph.view.v1:${encodeURIComponent(context)}`;
}

function point(value: unknown): value is Point {
  if (!value || typeof value !== "object") return false;
  const p = value as Point;
  return [p.x, p.y].every(
    (number) => typeof number === "number" && Number.isFinite(number) && Math.abs(number) <= 10_000_000,
  );
}

export function readSavedGraphView(text: string | null): SavedGraphView | undefined {
  if (!text || text.length > 2_000_000) return;
  try {
    const value = JSON.parse(text) as Partial<SavedGraphView>;
    if (
      value.version !== 1 ||
      !point(value.viewport) ||
      !Number.isFinite(value.viewport.zoom) ||
      value.viewport.zoom < 0.01 ||
      value.viewport.zoom > 2.5
    )
      return;
    if (
      !["topology", "attack", "coverage"].includes(String(value.mode)) ||
      !["auto", "all", "none"].includes(String(value.labels)) ||
      typeof value.grouped !== "boolean"
    )
      return;
    const positions: Array<[string, Point]> = [];
    const ids = new Set<string>();
    if (!Array.isArray(value.positions) || value.positions.length > 20_000) return;
    for (const entry of value.positions) {
      if (
        !Array.isArray(entry) ||
        entry.length !== 2 ||
        typeof entry[0] !== "string" ||
        entry[0].length > 256 ||
        !point(entry[1]) ||
        ids.has(entry[0])
      )
        return;
      ids.add(entry[0]);
      positions.push([entry[0], { x: entry[1].x, y: entry[1].y }]);
    }
    if (
      typeof value.kind !== "string" ||
      value.kind.length > 64 ||
      typeof value.state !== "string" ||
      value.state.length > 64
    )
      return;
    return {
      version: 1,
      mode: value.mode as GraphMode,
      labels: value.labels as GraphLabels,
      grouped: value.grouped,
      kind: value.kind,
      state: value.state,
      positions,
      viewport: { x: value.viewport.x, y: value.viewport.y, zoom: value.viewport.zoom },
    };
  } catch {
    return;
  }
}
