import type { Finding, FindingAssetNode, FindingStatus, Severity } from "@/lib/types";
import type { FormGroup, FormValues } from "./forms";

export const findingStatuses: FindingStatus[] = [
  "pending",
  "in_progress",
  "confirmed",
  "resolved",
  "fixed",
  "false_positive",
  "ignored",
  "duplicate",
  "risk_accepted",
];
export const findingSeverities: Severity[] = ["critical", "high", "medium", "low"];
export const findingViews = ["triage", "flat", "grouped", "asset"] as const;
export const unassignedFindingTask = "__unassigned__";
export const findingsPath = "/exploration/findings";
export type FindingView = (typeof findingViews)[number];
export type FindingFilters = {
  severity: string;
  status: string;
  vulnclass: string;
  task: string;
  query: string;
  sort: string;
  assetScope?: string;
};
export const defaultFindingFilters: FindingFilters = {
  severity: "all",
  status: "all",
  vulnclass: "all",
  task: "all",
  query: "",
  sort: "severity",
};
export const findingPreferencesKey = "astra.findings.preferences";

export function findingIdentity(finding: Finding): string {
  return finding.finding_id
    ? `finding:${finding.finding_id}`
    : `node:${finding.task_id || unassignedFindingTask}:${finding.id}`;
}

export function findingHandle(finding: Finding): string {
  if (!finding.finding_id) throw new Error("This graph record has no standalone finding handle.");
  return encodeURIComponent(finding.finding_id);
}

export function findingFilterParameters(filters: Partial<FindingFilters>): URLSearchParams {
  const parameters = new URLSearchParams();
  for (const [key, parameter] of [
    ["severity", "severity"],
    ["status", "status"],
    ["vulnclass", "vulnclass"],
    ["task", "task_id"],
  ] as const) {
    const value = filters[key];
    if (value && value !== "all") parameters.set(parameter, value);
  }
  if (filters.query?.trim()) parameters.set("q", filters.query.trim());
  if (filters.sort) parameters.set("sort", filters.sort);
  if (filters.assetScope) parameters.set("asset_scope", filters.assetScope);
  return parameters;
}

export function findingPagePath(filters: Partial<FindingFilters>, page: number, size: number, grouped = false) {
  const query = findingFilterParameters(filters);
  query.set("page", String(page));
  query.set("limit", String(size));
  return `${findingsPath}${grouped ? "/groups" : ""}?${query}`;
}

export function findingPreferences(raw?: string | null): { view: FindingView; filters: FindingFilters } {
  let values: Record<string, unknown> = {};
  try {
    const parsed: unknown = JSON.parse(raw || "{}");
    if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) values = parsed as Record<string, unknown>;
  } catch {}
  const filters = { ...defaultFindingFilters };
  if (values.severity === "all" || findingSeverities.includes(values.severity as Severity))
    filters.severity = String(values.severity);
  if (values.status === "all" || findingStatuses.includes(values.status as FindingStatus))
    filters.status = String(values.status);
  for (const key of ["vulnclass", "task"] as const)
    if (typeof values[key] === "string" && values[key]) filters[key] = values[key];
  if (values.sort === "severity" || values.sort === "time") filters.sort = values.sort;
  return { view: findingViews.includes(values.view as FindingView) ? (values.view as FindingView) : "triage", filters };
}

export function findingMetadataDraft(finding: Finding): FormValues {
  return { name: finding.name || "", vulnclass: finding.vulnclass, severity: finding.severity };
}

export const findingMetadataForm: FormGroup[] = [
  {
    title: "Finding identity",
    fields: [
      { key: "name", label: "Finding name", hint: "An empty name displays the vulnerability class." },
      { key: "vulnclass", label: "Vulnerability class" },
      { key: "severity", label: "Severity", options: findingSeverities.map((value) => [value, value] as const) },
    ],
  },
];

export function findingMetadataDocument(draft: FormValues) {
  const severity = String(draft.severity) as Severity,
    vulnclass = String(draft.vulnclass || "").trim();
  if (!findingSeverities.includes(severity)) throw new Error("Choose a valid severity.");
  return { name: String(draft.name || "").trim(), vulnclass, severity };
}

export function findingFollowupDocument(kind: "retest" | "deepen", text: string) {
  const value = text.trim();
  if ([...value].length > 4000 || (kind === "deepen" && !value)) {
    throw new Error(
      kind === "deepen"
        ? "Follow-up instructions must contain 1–4000 characters."
        : "Retest notes must contain at most 4000 characters.",
    );
  }
  return kind === "retest" ? { notes: value } : { description: value };
}

export function findingExportPath(format: string, scope: string, filters: FindingFilters, ids: Iterable<string>) {
  if (!["md-single", "md-zip", "csv", "json"].includes(format)) throw new Error("Choose a supported export format.");
  if (!["selected", "filtered", "all"].includes(scope)) throw new Error("Choose an export scope.");
  const query = scope === "filtered" ? findingFilterParameters(filters) : new URLSearchParams();
  query.set("format", format);
  query.set("scope", scope);
  if (scope === "selected") {
    const selected = [...new Set(ids)].filter(Boolean);
    if (!selected.length) throw new Error("Select at least one finding to export.");
    query.set("ids", selected.join(","));
  }
  return `${findingsPath}/export?${query}`;
}

export type FindingAssetRow = { node: FindingAssetNode; depth: number; children: number };

// The asset snapshot is capped by the server. Missing parents become roots;
// cycles are broken for display so retained assets cannot disappear or recurse.
export function findingAssetIndex(nodes: FindingAssetNode[]) {
  const byKey = new Map(nodes.map((node) => [node.key, node]));
  const children = new Map<string, string[]>(),
    parents = new Map<string, string>();
  const roots: string[] = [];
  for (const node of byKey.values()) {
    let parent = node.parent;
    const seen = new Set([node.key]);
    while (parent && byKey.has(parent) && !seen.has(parent)) {
      seen.add(parent);
      parent = byKey.get(parent)?.parent;
    }
    if (node.parent && byKey.has(node.parent) && !seen.has(parent || "")) {
      parents.set(node.key, node.parent);
      children.set(node.parent, [...(children.get(node.parent) || []), node.key]);
    } else roots.push(node.key);
  }
  return { byKey, parents, children, roots };
}

export function findingAssetTrail(nodes: FindingAssetNode[], key?: string): FindingAssetNode[] {
  const { byKey, parents } = findingAssetIndex(nodes),
    trail: FindingAssetNode[] = [],
    seen = new Set<string>();
  while (key && !seen.has(key)) {
    seen.add(key);
    const node = byKey.get(key);
    if (!node) break;
    trail.unshift(node);
    key = parents.get(key);
  }
  return trail;
}

export function findingAssetRows(
  nodes: FindingAssetNode[],
  expanded: ReadonlySet<string>,
  query = "",
): FindingAssetRow[] {
  const { byKey, parents, children, roots } = findingAssetIndex(nodes),
    matches = new Set<string>();
  const term = query.trim().toLocaleLowerCase();
  if (term)
    for (const node of byKey.values())
      if (`${node.label} ${node.kind}`.toLocaleLowerCase().includes(term)) {
        let parent: string | undefined = node.key;
        while (parent && !matches.has(parent)) {
          matches.add(parent);
          parent = parents.get(parent);
        }
        const pending = [...(children.get(node.key) || [])];
        while (pending.length) {
          const key = pending.pop()!;
          if (matches.has(key)) continue;
          matches.add(key);
          pending.push(...(children.get(key) || []));
        }
      }
  const result: FindingAssetRow[] = [],
    pending = [...roots].reverse().map((key) => ({ key, depth: 0 }));
  while (pending.length) {
    const { key, depth } = pending.pop()!;
    if (term && !matches.has(key)) continue;
    const descendants = children.get(key) || [];
    result.push({ node: byKey.get(key)!, depth, children: descendants.length });
    if (term || expanded.has(key))
      for (const child of [...descendants].reverse()) pending.push({ key: child, depth: depth + 1 });
  }
  return result;
}
