import type { Asset, Company, Finding, Task } from "../types";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";
import { DemoRows, patchDemoStrings } from "./demo-records";

const severityRank = { critical: 4, high: 3, medium: 2, low: 1 } as const;
const unassignedAsset = "__none__";
const assetKey = (id: number) => `a:${id}`;

function severityCounts(rows: readonly Finding[]) {
  const counts = { critical: 0, high: 0, medium: 0, low: 0 };
  rows.forEach((finding) => {
    if (Object.hasOwn(counts, finding.severity)) counts[finding.severity]++;
  });
  return counts;
}

function findingPage<T>(rows: T[], query: URLSearchParams, defaultSize: number, maximum: number) {
  const requestedPage = Number(query.get("page") ?? 1);
  const requestedSize = Number(query.get("limit") ?? defaultSize);
  const page = Number.isFinite(requestedPage) && requestedPage > 0 ? Math.floor(requestedPage) : 1;
  const size =
    Number.isFinite(requestedSize) && requestedSize > 0 ? Math.min(maximum, Math.floor(requestedSize)) : defaultSize;
  return { items: rows.slice((page - 1) * size, page * size), total: rows.length, page, page_size: size };
}

interface AssetTreeNode {
  key: string;
  parent?: string;
  kind: string;
  label: string;
  asset_id?: number;
  company_id?: number;
  self: number;
  total: number;
  critical: number;
  high: number;
  medium: number;
  low: number;
  last_found_at: string;
}

function treeNode(
  identity: Pick<AssetTreeNode, "key" | "kind" | "label"> & Partial<Pick<AssetTreeNode, "asset_id" | "company_id">>,
): AssetTreeNode {
  return { ...identity, self: 0, total: 0, critical: 0, high: 0, medium: 0, low: 0, last_found_at: "" };
}

function address(asset: Asset): { host: string; port: number } {
  let host = asset.domain ?? "",
    port = asset.port ?? 0;
  if (!host && asset.url) {
    try {
      const url = new URL(asset.url);
      host = url.hostname.replace(/^\[|\]$/g, "");
      if (!port) port = Number(url.port) || (url.protocol === "https:" ? 443 : 80);
    } catch {
      /* Malformed recorded URLs retain the explicit IP and port fallback. */
    }
  }
  if (!host) host = asset.ip ?? "";
  return { host, port };
}

function assetTitle(asset: Asset): string {
  if (asset.type === "service" && !asset.url) {
    const host = asset.domain || asset.ip;
    if (host && asset.port) return `${host}:${asset.port}`;
  }
  return asset.url || asset.domain || asset.ip || asset.app_name || `#${asset.id}`;
}

/** One derived tree supplies both the display and descendant-scope selection. */
class FindingAssetTree {
  constructor(
    private readonly assets: Asset[],
    private readonly companies: Company[],
  ) {}

  private closure(findings: Finding[]): Map<number, Asset> {
    const hits = new Set<number>();
    for (const finding of findings) for (const ref of finding.assets ?? []) hits.add(Number(ref.id));
    const picked = new Map<number, Asset>();
    for (const asset of this.assets) if (hits.has(asset.id)) picked.set(asset.id, asset);
    for (let round = 0; round < 4; round++) {
      const before = picked.size;
      for (const asset of [...picked.values()]) {
        const { host } = address(asset);
        const additions = this.assets.filter((candidate) => {
          if (picked.has(candidate.id)) return false;
          if (asset.type === "endpoint" && candidate.type === "service") return address(candidate).host === host;
          if (asset.type === "service" || asset.type === "endpoint")
            return (
              (candidate.type === "subdomain" && candidate.domain === host) ||
              (candidate.type === "ip" && candidate.ip === host)
            );
          return (
            asset.type === "subdomain" && candidate.type === "root_domain" && candidate.domain === asset.root_domain
          );
        });
        for (const candidate of additions) picked.set(candidate.id, candidate);
      }
      if (picked.size === before) break;
    }
    return picked;
  }

  private parent(asset: Asset, picked: Map<number, Asset>): string {
    const first = (test: (candidate: Asset) => boolean) => {
      const found = [...picked.values()].find(test);
      return found ? assetKey(found.id) : "";
    };
    const { host, port } = address(asset);
    const root = () => first((candidate) => candidate.type === "root_domain" && candidate.domain === asset.root_domain);
    if (asset.type === "subdomain") return root();
    if (asset.type === "service")
      return (
        first(
          (candidate) =>
            candidate.type === "subdomain" && (candidate.domain === asset.domain || candidate.domain === host),
        ) ||
        first((candidate) => candidate.type === "ip" && (candidate.ip === asset.ip || candidate.ip === host)) ||
        root()
      );
    if (asset.type === "endpoint")
      return (
        first((candidate) => {
          if (candidate.type !== "service") return false;
          const service = address(candidate);
          return service.host === host && service.port === port;
        }) ||
        first((candidate) => candidate.type === "service" && address(candidate).host === host) ||
        first(
          (candidate) =>
            candidate.type === "subdomain" && (candidate.domain === host || candidate.domain === asset.domain),
        ) ||
        first((candidate) => candidate.type === "ip" && (candidate.ip === host || candidate.ip === asset.ip)) ||
        root()
      );
    return "";
  }

  build(findings: Finding[]): AssetTreeNode[] {
    const picked = this.closure(findings),
      nodes = new Map<string, AssetTreeNode>(),
      parents = new Map<string, string>();
    const link = (node: AssetTreeNode, parent: string) => {
      node.parent = parent;
      parents.set(node.key, parent);
    };
    for (const asset of picked.values())
      nodes.set(
        assetKey(asset.id),
        treeNode({
          key: assetKey(asset.id),
          kind: asset.type,
          label: assetTitle(asset),
          asset_id: asset.id,
          company_id: asset.company_id,
        }),
      );
    for (const asset of picked.values()) {
      const parent = this.parent(asset, picked),
        key = assetKey(asset.id);
      if (parent && parent !== key) link(nodes.get(key)!, parent);
    }
    for (const node of [...nodes.values()]) {
      if (node.parent || !node.company_id || !["root_domain", "ip", "app"].includes(node.kind)) continue;
      const company = this.companies.find((candidate) => candidate.id === node.company_id);
      if (!company) continue;
      const key = `c:${company.id}`;
      if (!nodes.has(key))
        nodes.set(key, treeNode({ key, kind: "company", label: company.name, company_id: company.id }));
      link(node, key);
    }
    const unassigned = treeNode({ key: unassignedAsset, kind: "none", label: "uroboros.lab demo record" });
    const bump = (node: AssetTreeNode, finding: Finding) => {
      node.total++;
      if (Object.hasOwn(severityRank, finding.severity)) node[finding.severity]++;
      if (finding.ts > node.last_found_at) node.last_found_at = finding.ts;
    };
    for (const finding of findings) {
      const direct = (finding.assets ?? []).map((ref) => nodes.get(`a:${ref.id}`)).filter((node) => node !== undefined);
      if (!direct.length) {
        bump(unassigned, finding);
        unassigned.self++;
        continue;
      }
      const touched = new Set<string>();
      for (const node of direct) {
        node.self++;
        for (let key: string | undefined = node.key; key; key = parents.get(key)) touched.add(key);
      }
      for (const key of touched) {
        const node = nodes.get(key);
        if (node) bump(node, finding);
      }
    }
    const result = [...nodes.values()].filter((node) => node.total > 0);
    if (unassigned.total > 0) result.push(unassigned);
    return result.sort((left, right) =>
      (left.kind === "none") !== (right.kind === "none")
        ? left.kind === "none"
          ? 1
          : -1
        : right.total - left.total || left.label.localeCompare(right.label),
    );
  }

  scope(findings: Finding[], key: string | null): Finding[] {
    if (!key) return findings;
    if (key === unassignedAsset)
      return findings.filter((finding) => {
        const refs = finding.assets ?? [];
        return !refs.length || refs.every((ref) => !this.assets.some((asset) => String(asset.id) === ref.id));
      });
    const nodes = this.build(findings);
    if (!nodes.some((node) => node.key === key)) return [];
    const children = new Map<string, AssetTreeNode[]>();
    for (const node of nodes) if (node.parent) children.set(node.parent, [...(children.get(node.parent) ?? []), node]);
    const ids = new Set<string>(),
      queue = [key],
      seen = new Set(queue);
    while (queue.length) {
      const current = queue.shift()!,
        node = nodes.find((candidate) => candidate.key === current);
      if (node?.asset_id) ids.add(String(node.asset_id));
      for (const child of children.get(current) ?? [])
        if (!seen.has(child.key)) {
          seen.add(child.key);
          queue.push(child.key);
        }
    }
    return ids.size ? findings.filter((finding) => (finding.assets ?? []).some((ref) => ids.has(ref.id))) : [];
  }
}

/** Borrow live findings; evidence counts remain lazy and owned by FindingEvidence. */
export class DemoFindings {
  private readonly rows: DemoRows<Finding, string>;
  private readonly tree: FindingAssetTree;
  constructor(
    findings: Finding[],
    private readonly tasks: Task[],
    assets: Asset[],
    companies: Company[],
    private readonly trafficCount: (id: string) => number,
  ) {
    this.rows = new DemoRows(findings, (finding) => finding.id);
    this.tree = new FindingAssetTree(assets, companies);
  }

  private select(query: URLSearchParams): Finding[] {
    let rows = this.rows.rows.filter((finding) => {
      const needle = query.get("q")?.trim().toLowerCase();
      return (
        !needle ||
        [finding.name, finding.vulnclass, finding.summary, finding.evidence, finding.report].some((value) =>
          String(value ?? "")
            .toLowerCase()
            .includes(needle),
        )
      );
    });
    for (const field of ["severity", "status", "vulnclass"] as const) {
      const value = query.get(field);
      if (value) rows = rows.filter((finding) => finding[field] === value);
    }
    const task = query.get("task_id");
    if (task === "__unassigned__") rows = rows.filter((finding) => !finding.task_id);
    else if (task) rows = rows.filter((finding) => finding.task_id === task);
    return rows;
  }

  private project(finding: Finding, summary = false) {
    return {
      ...finding,
      ...(summary ? { report: undefined } : {}),
      finding_id: finding.id,
      traffic_count: this.trafficCount(finding.id),
    };
  }

  private statistics() {
    const findings = this.rows.rows,
      tasks = new Map<string, { name: string; description: string; count: number }>();
    const vulnclasses = Array.from(new Set(findings.map((finding) => finding.vulnclass))).sort();
    for (const finding of findings) {
      if (!finding.task_id) continue;
      const owner = this.tasks.find((task) => task.id === finding.task_id);
      const row = tasks.get(finding.task_id) ?? {
        name: owner?.name ?? "",
        description: finding.task_description ?? "",
        count: 0,
      };
      row.count++;
      tasks.set(finding.task_id, row);
    }
    return {
      total: findings.length,
      pending: findings.filter((finding) => finding.status === "pending").length,
      ...severityCounts(findings),
      vulnclasses,
      tasks: Array.from(tasks, ([id, row]) => ({ id, ...row })),
    };
  }

  private groups(query: URLSearchParams) {
    const selected = this.tree.scope(this.select(query), query.get("asset_scope"));
    const grouped = new Map<string, Finding[]>();
    for (const finding of selected) {
      const key = finding.task_id ?? "__unassigned__";
      grouped.set(key, [...(grouped.get(key) ?? []), finding]);
    }
    const groups = Array.from(grouped, ([key, items]) => {
      const task = this.tasks.find((candidate) => candidate.id === key);
      return {
        task_id: key === "__unassigned__" ? null : key,
        task_name: task?.name ?? "",
        task_description: task?.description ?? items[0]?.task_description ?? "",
        task_status: task?.status ?? "",
        count: items.length,
        ...severityCounts(items),
        last_found_at: items.reduce((latest, finding) => (finding.ts > latest ? finding.ts : latest), ""),
        max_severity: Math.max(...items.map((finding) => severityRank[finding.severity])),
      };
    });
    groups.sort((left, right) =>
      query.get("sort") === "severity"
        ? right.max_severity - left.max_severity || right.last_found_at.localeCompare(left.last_found_at)
        : right.last_found_at.localeCompare(left.last_found_at),
    );
    const { items, total, page, page_size } = findingPage(groups, query, 10, 100);
    return { items, total, finding_total: selected.length, page, page_size };
  }

  private list(query: URLSearchParams) {
    const task = query.get("task") ?? undefined;
    if (task) {
      const owner = this.tasks.find((candidate) => candidate.id === task),
        sources = new Set(owner?.source_task_ids ?? []);
      return this.rows.rows
        .filter((finding) => finding.task_id === task || (!!finding.task_id && sources.has(finding.task_id)))
        .map((finding) => ({
          ...this.project(finding, true),
          ...(finding.task_id !== task ? { inherited: true, source_task_id: finding.task_id } : {}),
        }));
    }
    if (!query.has("page") && !query.has("limit")) return this.rows.rows.map((finding) => this.project(finding, true));
    const selected = this.tree.scope(this.select(query), query.get("asset_scope"));
    selected.sort((left, right) =>
      query.get("sort") === "severity"
        ? severityRank[right.severity] - severityRank[left.severity] || +new Date(right.ts) - +new Date(left.ts)
        : +new Date(right.ts) - +new Date(left.ts),
    );
    const page = findingPage(selected, query, 20, 200);
    return { ...page, items: page.items.map((finding) => this.project(finding, true)) };
  }

  respond({ method, path, segments, query, body }: DemoRequest): unknown {
    if (path === "/exploration/findings/stats") return this.statistics();
    if (path === "/exploration/findings/asset-tree") {
      const selected = this.select(query);
      return { nodes: this.tree.build(selected), finding_total: selected.length, truncated: false };
    }
    if (path === "/exploration/findings/groups") return this.groups(query);
    if (segments[0] === "exploration" && segments[1] === "findings" && segments[3] === "deepen" && method === "POST") {
      const finding = this.rows.get(segments[2]),
        description = String(body.description ?? "").trim();
      if (!description) throw new Error("description is required");
      if ([...description].length > 4000) throw new Error("description must be at most 4000 characters");
      if (!finding) throw new Error("finding not found");
      if (!finding.task_id || !this.tasks.some((task) => task.id === finding.task_id))
        throw new Error("finding origin task or node is no longer available");
      return { task_id: finding.task_id, intent_id: `mock-deepen-${Date.now()}`, state: "open", queued: false };
    }
    if (
      segments[0] === "exploration" &&
      segments[1] === "findings" &&
      segments.length === 3 &&
      segments[2] !== "stats"
    ) {
      const finding = this.rows.get(segments[2]);
      if (!finding) return {};
      if (method === "PATCH") patchDemoStrings(finding, body, ["status", "severity", "name", "vulnclass"]);
      const context = query.get("context_task"),
        owner = context ? this.tasks.find((task) => task.id === context) : undefined;
      const inherited = !!(
        owner &&
        finding.task_id &&
        finding.task_id !== owner.id &&
        owner.source_task_ids?.includes(finding.task_id)
      );
      return { ...this.project(finding), ...(inherited ? { inherited: true, source_task_id: finding.task_id } : {}) };
    }
    if (path === "/exploration/findings") return this.list(query);
    return UNHANDLED;
  }
}
