import type { TrafficExchange } from "@/lib/types";

export type TrafficFilters = {
  host: string;
  query: string;
  method: string;
  body: string;
  path: string;
  status: string;
  minimum: string;
  maximum: string;
};
export type TrafficView = "stream" | "ledger";
export type TrafficSort = "ts" | "status" | "resp_len";
export type TrafficPreference = { view: TrafficView; sort: TrafficSort; order: "asc" | "desc"; size: number };
export const trafficSizes = [25, 50, 100, 200];
export const trafficMethods = ["GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS"];
export const emptyTrafficFilters: TrafficFilters = {
  host: "",
  query: "",
  method: "all",
  body: "",
  path: "",
  status: "all",
  minimum: "",
  maximum: "",
};
export const defaultTrafficPreference: TrafficPreference = { view: "stream", sort: "ts", order: "desc", size: 50 };

export function trafficPagePath(filters: TrafficFilters, preference: TrafficPreference, page: number) {
  const parameters = new URLSearchParams({
    page: String(Math.max(0, page)),
    size: String(preference.size),
    sort: preference.sort,
    order: preference.order,
  });
  for (const [key, parameter] of [
    ["host", "host"],
    ["query", "q"],
    ["method", "method"],
    ["body", "body"],
    ["path", "path"],
    ["status", "status"],
    ["minimum", "resp_min"],
    ["maximum", "resp_max"],
  ] as const) {
    const value = filters[key].trim();
    if (value && !(["method", "status"].includes(key) && value === "all")) parameters.set(parameter, value);
  }
  return `/traffic?${parameters}`;
}

export function trafficPreferences(
  value: unknown,
  legacyView?: string | null,
  legacySort?: unknown,
): TrafficPreference {
  const result = { ...defaultTrafficPreference };
  const source = value && typeof value === "object" ? (value as Record<string, unknown>) : {};
  const old = legacySort && typeof legacySort === "object" ? (legacySort as Record<string, unknown>) : {};
  const view = source.view ?? legacyView,
    sort = source.sort ?? old.field,
    order = source.order ?? old.direction;
  if (view === "stream" || view === "ledger") result.view = view;
  if (sort === "ts" || sort === "status" || sort === "resp_len") result.sort = sort;
  if (order === "asc" || order === "desc") result.order = order;
  if (typeof source.size === "number" && trafficSizes.includes(source.size)) result.size = source.size;
  return result;
}

// Presentation repair for old captures only. Stored packets and finding binds
// always keep the original server bytes, including their original line endings.
export function displayTrafficRequest(raw: string, exchange: Pick<TrafficExchange, "url" | "host">) {
  if (!raw.trim() || /^host\s*:/im.test(raw)) return raw;
  let authority = exchange.host;
  try {
    authority = new URL(exchange.url).host || authority;
  } catch {
    /* legacy indexed host */
  }
  const newline = raw.includes("\r\n") ? "\r\n" : "\n",
    end = raw.indexOf(newline);
  return end < 0
    ? `${raw}${newline}Host: ${authority}`
    : `${raw.slice(0, end + newline.length)}Host: ${authority}${newline}${raw.slice(end + newline.length)}`;
}

export function trafficPulse(exchanges: TrafficExchange[]) {
  return exchanges.reduce(
    (counts, exchange) => {
      const bucket = Math.floor(exchange.status / 100);
      if (bucket >= 2 && bucket <= 5) counts[bucket - 2]++;
      return counts;
    },
    [0, 0, 0, 0],
  );
}

export type TrafficDelete = { kind: "host"; host: string } | { kind: "hosts"; hosts: string[] } | { kind: "all" };
export function trafficDeletion(mode: TrafficDelete) {
  if (mode.kind === "all")
    return {
      path: "/traffic/all",
      body: undefined,
      description:
        "Every global traffic record and its unbound capture data. Evidence already saved to findings is retained.",
    };
  if (mode.kind === "host") {
    const host = mode.host.trim();
    if (!host) throw new Error("Choose a nonempty host filter before deleting traffic.");
    return {
      path: `/traffic?${new URLSearchParams({ host })}`,
      body: undefined,
      description: `All traffic whose recorded host matches “${host}” as a substring; % and _ act as wildcards. Other list filters do not limit this deletion.`,
    };
  }
  const hosts = [...new Set(mode.hosts.map((host) => host.trim()).filter(Boolean))];
  if (!hosts.length) throw new Error("Select at least one recorded host.");
  return {
    path: "/traffic/hosts",
    body: { hosts },
    description: `All traffic for ${hosts.length} exact recorded host${hosts.length === 1 ? "" : "s"}, across their captured ports. Other list filters do not limit this deletion.`,
  };
}

export function trafficBinding(finding: { finding_id?: string; inherited?: boolean }, ids: string[]) {
  if (!finding.finding_id || finding.inherited) throw new Error("Choose a writable standalone finding.");
  const selected = [...new Set(ids.filter(Boolean))];
  if (!selected.length) throw new Error("Select captured traffic before linking evidence.");
  return {
    path: `/exploration/findings/${encodeURIComponent(finding.finding_id)}/traffic`,
    body: { traffic_refs: selected.map((traffic_id) => ({ traffic_id })) },
  };
}

export function trafficBytes(value: number) {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"],
    unit = Math.min(units.length - 1, Math.floor(Math.log(value) / Math.log(1024)));
  return `${unit ? (value / 1024 ** unit).toFixed(1) : value} ${units[unit]}`;
}
