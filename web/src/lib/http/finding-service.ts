import type {
  ActiveFindingRetest,
  Edge,
  EvidenceBodyPreview,
  Finding,
  FindingAssetTree,
  FindingDeepenResponse,
  FindingGroupsPage,
  FindingQuery,
  FindingRetest,
  FindingStats,
  FindingStatus,
  FindingsPage,
  FindingTraffic,
  FindingTrafficDetail,
  Severity,
  TaskNode,
  TrafficEvidenceRef,
  TrafficEvidenceRole,
} from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { type QueryEntry, Resource, saveDownload } from "./resources";

type Filters = Omit<FindingQuery, "page" | "pageSize">;
type Export = {
  format: "md-single" | "md-zip" | "csv" | "json";
  scope: "filtered" | "all" | "selected";
  filters?: Filters;
  ids?: string[];
};

/** One ordered filter plan serves list/group/navigation/export without granting finding authority. */
function filterFields(filter: Filters): QueryEntry[] {
  const fields: QueryEntry[] = [];
  for (const name of ["severity", "status", "vulnclass"] as const) {
    const value = filter[name];
    if (value && value !== "all") fields.push([name, value]);
  }
  if (filter.task && filter.task !== "all") fields.push(["task_id", filter.task]);
  if (filter.query?.trim()) fields.push(["q", filter.query.trim()]);
  if (filter.sort) fields.push(["sort", filter.sort]);
  if (filter.assetScope) fields.push(["asset_scope", filter.assetScope]);
  return fields;
}

export function findingService(wire: ApplicationWire) {
  const root = new Resource(wire, "/exploration/findings");
  const node = (id: string, ...parts: (string | number)[]) => root.at(String(id), ...parts);
  const contextual = (resource: Resource, task?: string) =>
    task ? resource.query([["context_task", task]], "component") : resource;
  const binding = (id: string, bindingId?: string) =>
    bindingId === undefined ? node(id, "traffic") : node(id, "traffic", bindingId);
  const page = <T>(filter: FindingQuery, kind?: string) =>
    (kind ? root.at(kind) : root)
      .query([...filterFields(filter), ["page", String(filter.page)], ["limit", String(filter.pageSize)]])
      .read<T>();
  const bodyResource = (id: string, bindingId: string, side: string, extra: QueryEntry, context?: string) =>
    node(id, "traffic", String(bindingId), "body").query(
      [["side", String(side)], extra, ["context_task", context ? context : undefined]],
      "component",
    );

  return {
    findingsPage: (q: FindingQuery) => page<FindingsPage>(q),
    findingGroups: (q: FindingQuery) => page<FindingGroupsPage>(q, "groups"),
    findingAssetTree: (q: Filters) => root.at("asset-tree").query(filterFields(q)).read<FindingAssetTree>(),
    findingStats: () => root.at("stats").read<FindingStats>(),
    exportFindings: async (options: Export) => {
      const fields: QueryEntry[] = [
        ["format", String(options.format)],
        ["scope", String(options.scope)],
      ];
      if (options.scope === "selected") fields.push(["ids", (options.ids ?? []).join(",")]);
      else if (options.scope === "filtered" && options.filters) fields.push(...filterFields(options.filters));
      const response = await root.at("export").query(fields).raw();
      if (!response.ok) throw new Error(`export: ${response.status}`);
      const content = await response.blob();
      const disposition = response.headers.get("Content-Disposition") ?? "";
      saveDownload(content, disposition.match(/filename="?([^"]+)"?/)?.[1] ?? "findings-export");
    },
    getFinding: (id: string, contextTaskId?: string) => contextual(node(id), contextTaskId).read<Finding>(),
    findingTraffic: (id: string, contextTask?: string) => contextual(binding(id), contextTask).read<FindingTraffic>(),
    bindFindingTraffic: (id: string, traffic_refs: TrafficEvidenceRef[], contextTask?: string) =>
      contextual(binding(id), contextTask).send<FindingTraffic>("POST", { traffic_refs }),
    editFindingTraffic: (
      id: string,
      bindingId: string,
      version: number,
      fields: { role: TrafficEvidenceRole; note: string },
      contextTask?: string,
    ) =>
      contextual(node(id, "traffic", String(bindingId)), contextTask).send<FindingTraffic>("PATCH", {
        version,
        ...fields,
      }),
    removeFindingTraffic: (id: string, bindingId: string, version: number, contextTask?: string) =>
      contextual(node(id, "traffic", String(bindingId)), contextTask).send<FindingTraffic>("DELETE", { version }),
    orderFindingTraffic: (id: string, binding_ids: string[], version: number, contextTask?: string) =>
      contextual(node(id, "traffic", "order"), contextTask).send<FindingTraffic>("PUT", { binding_ids, version }),
    findingTrafficDetail: (id: string, bindingId: string, contextTask?: string) =>
      contextual(node(id, "traffic", String(bindingId)), contextTask).read<FindingTrafficDetail>(),
    findingTrafficBody: (
      id: string,
      bindingId: string,
      side: "request" | "response",
      offset: number,
      contextTask?: string,
    ) => bodyResource(id, bindingId, side, ["offset", String(offset)], contextTask).read<EvidenceBodyPreview>(),
    downloadFindingTrafficBody: async (
      id: string,
      bindingId: string,
      side: "request" | "response",
      contextTask?: string,
    ) => {
      const response = await bodyResource(id, bindingId, side, ["download", 1], contextTask).raw();
      if (!response.ok) {
        const error = await response.json().catch(() => ({ error: "Download failed" }));
        throw new Error(error.error ?? "Download failed");
      }
      saveDownload(await response.blob(), `evidence-${bindingId}-${side}.bin`, "evidence");
    },
    findingLineage: (id: string) => node(id, "lineage").read<{ nodes: TaskNode[]; edges: Edge[] }>(),
    setFindingStatus: (id: string, status: FindingStatus) => node(id).send<Finding>("PATCH", { status }),
    setFindingSeverity: (id: string, severity: Severity) => node(id).send<Finding>("PATCH", { severity }),
    updateFinding: (
      id: string,
      fields: { name?: string; vulnclass?: string; severity?: Severity; status?: FindingStatus },
    ) => node(id).send<Finding>("PATCH", fields),
    deleteFinding: (id: string) => node(id).send<{ deleted: boolean; id: number }>("DELETE"),
    findingRetests: (id: string) => root.at(encodeURIComponent(id), "retests").list<FindingRetest>("retests"),
    activeFindingRetests: () => root.at("retests", "active").list<ActiveFindingRetest>("retests"),
    startFindingRetest: (id: string, notes: string) =>
      root.at(encodeURIComponent(id), "retests").send<{ retest: FindingRetest; created: boolean }>("POST", { notes }),
    deepenFinding: (id: string, description: string) =>
      node(id, "deepen").send<FindingDeepenResponse>("POST", { description }),
  };
}
