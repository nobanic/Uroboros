import type { Asset, NewAssetType } from "@/lib/types";
import { assetCell } from "./asset-document";

export const taskAssetKinds: readonly [NewAssetType, string][] = [
  ["root_domain", "Root domains"],
  ["ip", "IP addresses"],
  ["subdomain", "Subdomains"],
  ["app", "Applications"],
  ["service", "Services"],
  ["endpoint", "Endpoints"],
];
export type TaskAssetColumn = keyof Asset | "address" | "response_size";
export const taskAssetColumns: Record<NewAssetType, readonly [TaskAssetColumn, string][]> = {
  root_domain: [
    ["domain", "Domain"],
    ["icp", "ICP filing"],
  ],
  ip: [
    ["ip", "IP address"],
    ["c_segment", "Network"],
    ["bound_domains", "Bound domains"],
    ["open_ports", "Open ports"],
  ],
  subdomain: [
    ["domain", "Domain"],
    ["root_domain", "Root domain"],
    ["record_type", "Record type"],
    ["record_value", "Record values"],
  ],
  app: [
    ["app_name", "Application"],
    ["url", "Address"],
    ["category", "Category"],
    ["page_title", "Title"],
    ["technologies", "Fingerprint"],
  ],
  service: [
    ["address", "Address / service"],
    ["status_code", "Status"],
    ["page_title", "Title"],
    ["response_size", "Response size"],
    ["technologies", "Fingerprint"],
    ["auth", "Auth"],
  ],
  endpoint: [
    ["method", "Method"],
    ["url", "Full URL"],
    ["params", "Parameters"],
  ],
};

function meaningful(values: (string | undefined)[], fallback: string) {
  return values.find((value) => value?.trim()) ?? fallback;
}

export function taskAssetName(asset: Asset) {
  const fallback = `#${asset.id}`;
  if (asset.type === "ip") return meaningful([asset.ip], fallback);
  if (asset.type === "app") return meaningful([asset.app_name, asset.url], fallback);
  if (asset.type === "endpoint") return meaningful([[asset.method, asset.url].filter(Boolean).join(" ")], fallback);
  if (asset.type === "service") {
    const host = meaningful([asset.ip, asset.domain], "");
    return meaningful(
      [
        asset.url,
        [host, asset.port].filter((value) => value !== undefined && value !== "").join(":"),
        asset.service_name,
      ],
      fallback,
    );
  }
  return meaningful([asset.domain], fallback);
}

export function taskAssetBytes(value?: number | null) {
  if (!value || !Number.isFinite(value) || value <= 0) return "—";
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), 2);
  return `${index === 0 ? value : (value / 1024 ** index).toFixed(1)} ${["B", "KB", "MB"][index]}`;
}

export function taskAssetCell(asset: Asset, column: TaskAssetColumn) {
  if (column === "method") return asset.method?.toUpperCase() || "—";
  if (column === "address")
    return asset.service_type === "http"
      ? asset.url || "—"
      : asset.service_name || [asset.ip || asset.domain, asset.port].filter(Boolean).join(":") || "—";
  if (column === "response_size") return taskAssetBytes(asset.content_length);
  if (column === "params")
    return (
      asset.params?.map((item) => (item.name ? `${item.name}(${item.location || item.in || "?"})` : "?")).join(", ") ||
      "—"
    );
  return assetCell(asset, column);
}

export type TaskAssetSelection = { kind: NewAssetType; page: number; size: number; query: string };
export const taskAssetKey = (selection: TaskAssetSelection) =>
  JSON.stringify([selection.kind, selection.page, selection.size, selection.query.trim()]);
export const taskAssetLastPage = (total: number, size: number) => Math.max(0, Math.ceil(total / size) - 1);
export const taskAssetTotal = (counts: Record<string, number>) =>
  taskAssetKinds.reduce((sum, [kind]) => sum + (counts[kind] ?? 0), 0);

export function taskAssetStatus(code: number) {
  return code >= 500 ? "error" : code >= 400 ? "warning" : code >= 300 ? "redirect" : code >= 200 ? "success" : "muted";
}
