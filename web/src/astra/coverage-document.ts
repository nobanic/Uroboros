import type { CoverageGraphData, CoverageGraphNode } from "@/lib/types";
import type { UroIconName } from "@/components/uroboros-icons";
import { arrangeEvidence, type EvidenceView } from "./graph-engine";

export type CoverageKind = CoverageGraphNode["kind"];
export type CoverageMode = "surface" | "gaps" | "tested";
export const coverageBatch = 20;
export const coverageKinds: Record<CoverageKind, { title: string; icon: UroIconName; color: string }> = {
  company: { title: "Company", icon: "asset-company", color: "#91a9cf" },
  root_domain: { title: "Root domain", icon: "asset-domain", color: "#b29aff" },
  subdomain: { title: "Subdomain", icon: "asset-subdomain", color: "#78a5ff" },
  ip: { title: "IP address", icon: "asset-ip", color: "#60d4d4" },
  service: { title: "Service", icon: "asset-service", color: "#efba79" },
  app: { title: "Application", icon: "asset-app", color: "#d89ff1" },
  endpoint: { title: "Endpoint", icon: "asset-endpoint", color: "#ff8a9f" },
};
export type CoverageGroup = { id: string; parent: string; kind: CoverageKind; members: CoverageGraphNode[] };
export type CoverageFold = { id: string; group: CoverageGroup; hidden: CoverageGraphNode[] };
export type CoverageProjection = {
  all: Map<string, CoverageGraphNode>;
  visible: CoverageGraphNode[];
  folds: CoverageFold[];
  groups: Map<string, CoverageGroup>;
  parents: Map<string, string[]>;
  links: Array<{ source: string; target: string }>;
};
const groupKey = (parent: string, kind: CoverageKind) => JSON.stringify([parent, kind]);

// The API sends child -> parent containment, unlike exploration causality.
// Contraction affects this projection only. Every original asset stays in all.
export function projectCoverage(
  data: Partial<CoverageGraphData> | undefined,
  expanded: Map<string, number>,
  tracking = true,
): CoverageProjection {
  const all = new Map((Array.isArray(data?.nodes) ? data.nodes : []).map((node) => [node.key, node]));
  const parents = new Map<string, string[]>(),
    children = new Map<string, Set<string>>();
  const originalLinks = new Map<string, { source: string; target: string }>();
  for (const edge of Array.isArray(data?.edges) ? data.edges : []) {
    if (!all.has(edge.src) || !all.has(edge.dst)) continue;
    const identity = JSON.stringify([edge.dst, edge.src]);
    if (originalLinks.has(identity)) continue;
    originalLinks.set(identity, { source: edge.dst, target: edge.src });
    if (!parents.has(edge.src)) parents.set(edge.src, []);
    parents.get(edge.src)!.push(edge.dst);
    if (!children.has(edge.dst)) children.set(edge.dst, new Set());
    children.get(edge.dst)!.add(edge.src);
  }
  const groups = new Map<string, CoverageGroup>();
  for (const [parent, members] of children)
    for (const key of members) {
      const node = all.get(key)!,
        id = groupKey(parent, node.kind);
      if (!groups.has(id)) groups.set(id, { id, parent, kind: node.kind, members: [] });
      groups.get(id)!.members.push(node);
    }
  const allowed = new Map<string, string[]>(),
    omitted = new Map<string, CoverageGraphNode[]>();
  for (const group of groups.values()) {
    group.members.sort(
      (left, right) =>
        Number(right.in_scope) - Number(left.in_scope) ||
        (tracking ? Number(right.tested) - Number(left.tested) : 0) ||
        left.label.localeCompare(right.label) ||
        left.key.localeCompare(right.key),
    );
    const requested = expanded.get(group.id),
      limit =
        requested !== undefined && Number.isFinite(requested)
          ? Math.max(coverageBatch, Math.floor(requested))
          : coverageBatch;
    const listed = allowed.get(group.parent) || [];
    listed.push(...group.members.slice(0, limit).map((node) => node.key));
    allowed.set(group.parent, listed);
    if (group.members.length > limit) omitted.set(group.id, group.members.slice(limit));
  }
  const hiddenBranches = new Set<string>(),
    pending = [...omitted.values()].flat().map((node) => node.key);
  for (let index = 0; index < pending.length; index++) {
    const key = pending[index];
    if (hiddenBranches.has(key)) continue;
    hiddenBranches.add(key);
    for (const child of children.get(key) || []) if (!hiddenBranches.has(child)) pending.push(child);
  }
  const visible = new Set<string>();
  const walk = (seeds: string[]) => {
    const queue = [...seeds];
    for (let index = 0; index < queue.length; index++) {
      const key = queue[index];
      if (visible.has(key)) continue;
      visible.add(key);
      for (const child of allowed.get(key) || []) if (!visible.has(child)) queue.push(child);
    }
  };
  const roots = [...all.keys()].filter((key) => !parents.has(key));
  walk(roots.length ? roots : [...all.keys()].slice(0, 1));
  for (const key of all.keys()) if (!visible.has(key) && !hiddenBranches.has(key)) walk([key]);
  const folds: CoverageFold[] = [];
  const links = [...originalLinks.values()].filter((link) => visible.has(link.source) && visible.has(link.target));
  for (const [id, omittedMembers] of omitted) {
    const group = groups.get(id)!,
      hidden = omittedMembers.filter((node) => !visible.has(node.key));
    if (!visible.has(group.parent) || !hidden.length) continue;
    let foldId = `coverage:fold:${id}`;
    while (all.has(foldId)) foldId += ":fold";
    folds.push({ id: foldId, group, hidden });
    links.push({ source: group.parent, target: foldId });
  }
  return { all, visible: [...all.values()].filter((node) => visible.has(node.key)), folds, groups, parents, links };
}

export function revealCoverage(projection: CoverageProjection, key: string, expanded: Map<string, number>) {
  const next = new Map(expanded),
    visited = new Set<string>(),
    queue = [key];
  for (let index = 0; index < queue.length; index++) {
    const child = queue[index],
      node = projection.all.get(child);
    if (!node || visited.has(child)) continue;
    visited.add(child);
    for (const parent of projection.parents.get(child) || []) {
      const id = groupKey(parent, node.kind),
        group = projection.groups.get(id);
      const rank = group?.members.findIndex((member) => member.key === child) ?? -1;
      if (rank >= 0)
        next.set(id, Math.max(next.get(id) || coverageBatch, Math.ceil((rank + 1) / coverageBatch) * coverageBatch));
      if (!visited.has(parent)) queue.push(parent);
    }
  }
  return next;
}

export function coverageStatus(node: CoverageGraphNode, tracking: boolean) {
  if (!node.in_scope) return { key: "context", title: "Context only" };
  if (!tracking) return { key: "scope", title: "In scope" };
  return node.tested ? { key: "tested", title: "Tested" } : { key: "gap", title: "Coverage gap" };
}

export function coverageTotals(projection: CoverageProjection, tracking: boolean) {
  const scoped = [...projection.all.values()].filter((node) => node.in_scope);
  const tested = tracking ? scoped.filter((node) => node.tested).length : 0;
  return {
    total: projection.all.size,
    inScope: scoped.length,
    tested,
    gaps: tracking ? scoped.length - tested : 0,
    percent: tracking && scoped.length ? Math.round((tested / scoped.length) * 100) : undefined,
    visible: projection.visible.length,
    folded: projection.all.size - projection.visible.length,
  };
}

export function coverageLabel(node: CoverageGraphNode) {
  if (node.kind === "endpoint" && node.url) {
    try {
      const url = new URL(node.url);
      return `${url.pathname}${url.search}` || "/";
    } catch {
      return node.label;
    }
  }
  if (node.kind === "service")
    return (
      [node.port !== undefined ? `:${node.port}` : undefined, node.page_title, node.status_code]
        .filter((value) => value !== undefined && value !== null && value !== "")
        .join(" / ") || node.label
    );
  return node.label;
}

export function coverageMatches(node: CoverageGraphNode, query: string) {
  return Object.values(node)
    .filter((value) => typeof value === "string" || typeof value === "number")
    .join(" ")
    .toLocaleLowerCase()
    .includes(query.trim().toLocaleLowerCase());
}

export function layoutCoverage(projection: CoverageProjection) {
  const nodes = [
    ...projection.visible.map((node) => ({
      id: node.key,
      kind: node.kind,
      title: node.label,
      state: "",
      payload: {},
      source: {},
    })),
    ...projection.folds.map((fold) => ({
      id: fold.id,
      kind: fold.group.kind,
      title: "Folded branch",
      state: "",
      payload: {},
      source: {},
    })),
  ];
  const view: EvidenceView = {
    nodes,
    all: new Map(nodes.map((node) => [node.id, node])),
    representatives: new Map(),
    links: projection.links.map((link) => ({ ...link, relation: "contains" })),
  };
  return arrangeEvidence(view);
}
