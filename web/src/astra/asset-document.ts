import type { Asset, Company, CompanyScopeMutation, CompanyScopeRule } from "@/lib/types";
import { parseCompanyScopeText } from "@/lib/company-scope";

export const assetClasses = [
  ["company", "Companies"],
  ["root_domain", "Root domains"],
  ["ip", "IP addresses"],
  ["subdomain", "Subdomains"],
  ["app", "Applications"],
  ["service", "Services"],
  ["endpoint", "Endpoints"],
] as const;
export type AssetClass = (typeof assetClasses)[number][0];
export const assetPageSizes = [25, 50, 100, 200];

export const assetColumns: Record<Exclude<AssetClass, "company">, readonly (readonly [keyof Asset, string])[]> = {
  root_domain: [
    ["domain", "Domain"],
    ["icp", "ICP filing"],
    ["company_id", "Company"],
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
    ["bundle_id", "Bundle ID"],
    ["category", "Category"],
    ["app_icp", "ICP filing"],
  ],
  service: [
    ["service_name", "Service"],
    ["domain", "Domain / URL"],
    ["ip", "IP address"],
    ["port", "Port"],
    ["status_code", "Status"],
    ["page_title", "Title"],
    ["technologies", "Technologies"],
    ["auth", "Auth"],
  ],
  endpoint: [
    ["method", "Method"],
    ["url", "URL"],
    ["params", "Parameters"],
  ],
};

export function assetPagePath(kind: AssetClass, page: number, size: number, query: string): string | null {
  if (kind === "company") return null;
  const parameters = new URLSearchParams({ type: kind, limit: String(size), offset: String(Math.max(0, page) * size) });
  if (query.trim()) parameters.set("dsl", query.trim());
  return `/assets?${parameters}`;
}

export function assetTitle(asset: Asset): string {
  return asset.url || asset.domain || asset.ip || asset.app_name || asset.bundle_id || `Asset #${asset.id}`;
}

export function assetWebLink(asset: Asset): string | undefined {
  if (asset.service_type !== "http" || !asset.url) return;
  try {
    const url = new URL(asset.url);
    if (url.protocol === "http:" || url.protocol === "https:") return asset.url;
  } catch {
    /* A malformed stored URL remains visible as text. */
  }
}

export function assetCell(asset: Asset, key: keyof Asset, companies: ReadonlyMap<number, string> = new Map()): string {
  const value = asset[key];
  if (key === "company_id")
    return asset.company_id ? companies.get(asset.company_id) || `Company #${asset.company_id}` : "Unassigned";
  if (key === "service_name")
    return asset.service_name || (asset.service_type === "http" ? "http" : asset.service_type) || "—";
  if (key === "domain" && asset.type === "service") return asset.domain || asset.url || "—";
  if (key === "open_ports")
    return asset.open_ports?.map((item) => `${item.port}${item.service ? `/${item.service}` : ""}`).join(", ") || "—";
  if (key === "auth") return asset.auth?.map((item) => String(item.type || item.username || "Auth")).join(", ") || "—";
  if (key === "params")
    return asset.params?.map((item) => (item.name ? `${item.name}(${item.location || "?"})` : "?")).join(", ") || "—";
  if (Array.isArray(value)) return value.length ? value.join(", ") : "—";
  return value === null || value === undefined || value === "" ? "—" : String(value);
}

export function savedCompanyRules(company?: Pick<Company, "scope">): CompanyScopeRule[] {
  return (company?.scope || [])
    .map((rule) => ({ kind: rule.kind, value: rule.raw.trim() }))
    .filter((rule) => rule.value);
}

export function companyScopeDocument(text: string, company?: Pick<Company, "scope">) {
  // Exact legacy raw values retain their explicit kind when editing. An
  // ambiguous saved keyword must not silently become a domain authority rule.
  return parseCompanyScopeText(text, { preservedRules: savedCompanyRules(company) });
}

export type CompanyWriteResult = Partial<CompanyScopeMutation> & {
  id?: number;
  created?: boolean;
  scope_added?: number;
  scope_skipped?: number;
  scope_invalid?: number;
  scope_errors?: string[];
};

export function companyWriteIncomplete(result: CompanyWriteResult): boolean {
  return (result.invalid ?? 0) > 0 || (result.scope_invalid ?? 0) > 0 || Boolean(result.errors?.length || result.scope_errors?.length);
}

export function companyWriteFeedback(result: CompanyWriteResult): string[] {
  const added = result.added ?? result.scope_added ?? 0,
    skipped = result.skipped ?? result.scope_skipped ?? 0,
    invalid = result.invalid ?? result.scope_invalid ?? 0;
  return [
    `Saved ${added} scope rules${skipped ? `; ${skipped} already present` : ""}${invalid ? `; ${invalid} invalid` : ""}.`,
    ...(result.errors || result.scope_errors || []),
    ...(result.warnings || []),
  ];
}
