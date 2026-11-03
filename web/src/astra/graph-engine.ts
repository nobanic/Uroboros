import { collection, type DataRow } from "./client";
import { payloadObject } from "./graph-model";

export type EvidenceNode = {
  id: string;
  kind: string;
  state: string;
  title: string;
  payload: DataRow;
  source: DataRow;
};
export type EvidenceLink = { source: string; target: string; relation: string };
export type Point = { x: number; y: number };
export type EvidenceView = {
  all: Map<string, EvidenceNode>;
  nodes: EvidenceNode[];
  links: EvidenceLink[];
  representatives: Map<string, string>;
};

export const cardSize = { width: 252, height: 132 };
export const relationStyle: Record<string, { title: string; color: string }> = {
  spawns: { title: "Spawns", color: "#4068ad" },
  derived_from: { title: "Derived", color: "#7960a8" },
  yields: { title: "Yields", color: "#ac6a21" },
  proves: { title: "Proves", color: "#af465c" },
};

function label(record: DataRow, kind: string, payload: DataRow): string {
  if (typeof record.display_title === "string" && record.display_title.trim()) return record.display_title.trim();
  const fields =
    kind === "goal"
      ? ["text", "summary"]
      : kind === "finding"
        ? ["name", "summary"]
        : ["summary", "description", "body", "text"];
  for (const key of fields) if (typeof payload[key] === "string" && payload[key]) return String(payload[key]);
  return typeof record.payload === "string" ? record.payload : kind;
}

// Preserve the complete wire record alongside the parsed presentation model.
// Digest contraction is a view operation; it never edits the evidence records.
export function evidenceView(value: unknown, collapse = true): EvidenceView {
  const all = new Map<string, EvidenceNode>();
  for (const source of collection(value, ["nodes"])) {
    const id = String(source.id);
    const kind = String(source.kind ?? source.type ?? "hint");
    const payload = payloadObject(source.payload);
    all.set(id, { id, kind, state: String(source.state ?? ""), title: label(source, kind, payload), payload, source });
  }
  const links = collection(value, ["edges"]).map((edge) => ({
    source: String(edge.from ?? edge.src),
    target: String(edge.to ?? edge.dst),
    relation: String(edge.rel ?? ""),
  }));
  const representatives = new Map<string, string>();
  if (collapse)
    for (const link of links) {
      const owner = all.get(link.source);
      if (link.relation === "covers" && owner?.kind === "digest" && owner.state === "active" && all.has(link.target)) {
        representatives.set(link.target, owner.id);
      }
    }
  const resolve = (id: string) => {
    const visited = new Set<string>();
    while (representatives.has(id) && !visited.has(id)) {
      visited.add(id);
      id = representatives.get(id)!;
    }
    return id;
  };
  for (const [id] of representatives) representatives.set(id, resolve(id));
  const nodes = [...all.values()].filter((node) =>
    collapse ? (node.kind === "digest" ? node.state === "active" : !representatives.has(node.id)) : true,
  );
  const visible = new Set(nodes.map((node) => node.id));
  const unique = new Map<string, EvidenceLink>();
  for (const link of links) {
    if (collapse && link.relation === "covers") continue;
    const next = { ...link, source: resolve(link.source), target: resolve(link.target) };
    if (!visible.has(next.source) || !visible.has(next.target) || next.source === next.target) continue;
    unique.set(JSON.stringify([next.source, next.relation, next.target]), next);
  }
  const yielded = new Set(
    [...unique.values()]
      .filter((link) => link.relation === "yields")
      .map((link) => JSON.stringify([link.source, link.target])),
  );
  return {
    all,
    nodes,
    representatives,
    links: [...unique.values()].filter(
      (link) => link.relation !== "derived_from" || !yielded.has(JSON.stringify([link.target, link.source])),
    ),
  };
}

function adjacency(ids: string[], links: EvidenceLink[], reverse: boolean) {
  const result = new Map(ids.map((id) => [id, [] as string[]]));
  for (const link of links) {
    const from = reverse ? link.target : link.source,
      to = reverse ? link.source : link.target;
    if (result.has(from) && result.has(to)) result.get(from)!.push(to);
  }
  return result;
}

export function findingAncestry(view: EvidenceView): Set<string> {
  const roots = view.nodes.filter((node) => node.kind === "finding").map((node) => node.id);
  if (!roots.length) return new Set(view.nodes.map((node) => node.id));
  const parents = adjacency(
    view.nodes.map((node) => node.id),
    view.links,
    true,
  );
  const included = new Set(roots);
  for (let index = 0; index < roots.length; index++)
    for (const parent of parents.get(roots[index]) ?? []) {
      if (!included.has(parent)) {
        included.add(parent);
        roots.push(parent);
      }
    }
  return included;
}

// Iterative strongly-connected components produce a DAG before assigning
// columns. Cycles stay visible, while every card gets a separate row. The two
// traversals avoid recursion limits on long or cyclic investigation histories.
export function arrangeEvidence(view: EvidenceView): Map<string, Point> {
  const ids = view.nodes.map((node) => node.id).sort((a, b) => a.localeCompare(b, undefined, { numeric: true }));
  const outgoing = adjacency(ids, view.links, false),
    incoming = adjacency(ids, view.links, true);
  const visited = new Set<string>(),
    finish: string[] = [];
  for (const root of ids) {
    if (visited.has(root)) continue;
    const stack: Array<[string, boolean]> = [[root, false]];
    while (stack.length) {
      const [id, exiting] = stack.pop()!;
      if (exiting) {
        finish.push(id);
        continue;
      }
      if (visited.has(id)) continue;
      visited.add(id);
      stack.push([id, true]);
      for (const child of outgoing.get(id) ?? []) if (!visited.has(child)) stack.push([child, false]);
    }
  }
  const component = new Map<string, number>(),
    groups: string[][] = [];
  for (const root of finish.reverse()) {
    if (component.has(root)) continue;
    const index = groups.length,
      members: string[] = [],
      stack = [root];
    component.set(root, index);
    while (stack.length) {
      const id = stack.pop()!;
      members.push(id);
      for (const parent of incoming.get(id) ?? [])
        if (!component.has(parent)) {
          component.set(parent, index);
          stack.push(parent);
        }
    }
    groups.push(members);
  }
  const children = groups.map(() => new Set<number>()),
    degree = groups.map(() => 0),
    column = groups.map(() => 0);
  for (const link of view.links) {
    const from = component.get(link.source),
      to = component.get(link.target);
    if (from === undefined || to === undefined || from === to || children[from].has(to)) continue;
    children[from].add(to);
    degree[to]++;
  }
  const ready = degree.flatMap((count, index) => (count === 0 ? [index] : []));
  for (let index = 0; index < ready.length; index++)
    for (const child of children[ready[index]]) {
      column[child] = Math.max(column[child], column[ready[index]] + 1);
      if (--degree[child] === 0) ready.push(child);
    }
  const lanes = new Map<number, string[]>();
  for (const id of ids) {
    const lane = column[component.get(id)!];
    if (!lanes.has(lane)) lanes.set(lane, []);
    lanes.get(lane)!.push(id);
  }
  // Order each stage around its recorded neighbors. Numeric ID is the stable
  // tiebreaker, so equivalent polling payloads cannot shuffle the canvas.
  const orderedLanes = [...lanes].sort(([left], [right]) => left - right);
  const rank = new Map<string, number>();
  for (const [, members] of orderedLanes) members.forEach((id, index) => rank.set(id, index));
  for (const reverse of [false, true]) {
    for (const [, members] of reverse ? [...orderedLanes].reverse() : orderedLanes) {
      const neighbors = reverse ? outgoing : incoming;
      const center = (id: string) => {
        const related = neighbors.get(id) || [];
        return related.length
          ? related.reduce((sum, other) => sum + (rank.get(other) || 0), 0) / related.length
          : rank.get(id) || 0;
      };
      const scores = new Map(members.map((id) => [id, center(id)]));
      members.sort(
        (left, right) =>
          scores.get(left)! - scores.get(right)! || left.localeCompare(right, undefined, { numeric: true }),
      );
      members.forEach((id, index) => rank.set(id, index));
    }
  }
  // Center narrow stages around broad branches. Long histories wrap into
  // bands, while wide/cyclic stages use a grid rather than a vertical strip.
  // This changes presentation only; every original node/relation is retained.
  const maxLane = orderedLanes.reduce((largest, [, members]) => Math.max(largest, members.length), 1);
  const rowsPerLane = Math.max(5, Math.ceil(Math.sqrt(maxLane * 1.3)));
  const xStep = cardSize.width + 88,
    yStep = cardSize.height + 40;
  const columnsPerBand = Math.max(12, Math.ceil(Math.sqrt(ids.length) * 1.4));
  type Stage = { members: string[]; columns: number; rows: number };
  const bands: Stage[][] = [];
  let band: Stage[] = [],
    columns = 0;
  for (const [, members] of orderedLanes) {
    const stage = {
      members,
      columns: Math.ceil(members.length / rowsPerLane),
      rows: Math.min(rowsPerLane, members.length),
    };
    if (band.length && columns + stage.columns > columnsPerBand) {
      bands.push(band);
      band = [];
      columns = 0;
    }
    band.push(stage);
    columns += stage.columns;
  }
  if (band.length) bands.push(band);
  const points = new Map<string, Point>();
  let top = 0;
  for (const stages of bands) {
    const height = Math.max(...stages.map((stage) => stage.rows)) * yStep;
    let left = 0;
    for (const stage of stages) {
      for (const [index, id] of stage.members.entries()) {
        const row = index % rowsPerLane,
          column = Math.floor(index / rowsPerLane);
        points.set(id, { x: left + column * xStep, y: top + (height - stage.rows * yStep) / 2 + row * yStep });
      }
      left += stage.columns * xStep;
    }
    top += height + 180;
  }
  return points;
}

export function evidenceMatches(node: EvidenceNode, query: string) {
  const haystack = [node.id, node.kind, node.state, node.title, node.source.origin, JSON.stringify(node.payload)]
    .join(" ")
    .toLocaleLowerCase();
  return haystack.includes(query.trim().toLocaleLowerCase());
}

export function digestMembers(node: EvidenceNode, view: EvidenceView): EvidenceNode[] {
  const membership = payloadObject(node.payload.member_ids);
  const declared = [membership.facts, membership.intents].flatMap((value) =>
    Array.isArray(value) ? value.map(String) : [],
  );
  const contracted = [...view.representatives].flatMap(([id, owner]) => (owner === node.id ? [id] : []));
  const covered = view.links
    .filter((link) => link.relation === "covers" && link.source === node.id)
    .map((link) => link.target);
  return [...new Set([...declared, ...contracted, ...covered])].flatMap((id) =>
    view.all.has(id) ? [view.all.get(id)!] : [],
  );
}
