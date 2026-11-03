import type { DataRow } from "./client";

export type Section =
  | "operations"
  | "findings"
  | "assets"
  | "traffic"
  | "models"
  | "agents"
  | "tools"
  | "mcp"
  | "skills";
export type Resource = {
  key: Section;
  title: string;
  subtitle: string;
  endpoint: string;
  collections: readonly string[];
  columns: readonly string[];
  legacy: string;
};

export const resources: Resource[] = [
  {
    key: "operations",
    title: "Operations",
    subtitle: "Assignments, execution and the evidence graph",
    endpoint: "/tasks",
    collections: ["tasks"],
    columns: ["name", "status", "goal", "created_at"],
    legacy: "/function/tasks",
  },
  {
    key: "findings",
    title: "Findings",
    subtitle: "Security claims and their recorded evidence",
    endpoint: "/exploration/findings",
    collections: ["items", "findings"],
    columns: ["name", "severity", "status", "summary"],
    legacy: "/function/findings",
  },
  {
    key: "assets",
    title: "Assets",
    subtitle: "Observed hosts, services and application endpoints",
    endpoint: "/assets?limit=100&offset=0",
    collections: ["assets"],
    columns: ["type", "label", "value", "created_at"],
    legacy: "/function/assets",
  },
  {
    key: "traffic",
    title: "Traffic",
    subtitle: "Captured exchanges available for investigation",
    endpoint: "/traffic?page=0&size=100",
    collections: ["exchanges", "items"],
    columns: ["method", "host", "url", "status"],
    legacy: "/function/traffic",
  },
  {
    key: "models",
    title: "Model profiles",
    subtitle: "Provider configuration and execution choices",
    endpoint: "/llm/profiles",
    collections: ["profiles"],
    columns: ["name", "format", "model", "active"],
    legacy: "/system/llm",
  },
  {
    key: "agents",
    title: "Agents",
    subtitle: "Agent roles and available capabilities",
    endpoint: "/agents",
    collections: ["agents"],
    columns: ["name", "key", "role", "enabled"],
    legacy: "/system/agents",
  },
  {
    key: "tools",
    title: "Tools",
    subtitle: "Registered capabilities and agent bindings",
    endpoint: "/tools",
    collections: ["tools"],
    columns: ["key", "kind", "enabled", "description"],
    legacy: "/system/tools",
  },
  {
    key: "mcp",
    title: "MCP connections",
    subtitle: "External tool services and their status",
    endpoint: "/mcp",
    collections: ["servers"],
    columns: ["name", "transport", "enabled", "url"],
    legacy: "/system/mcp",
  },
  {
    key: "skills",
    title: "Skills",
    subtitle: "Installed procedures and supporting instructions",
    endpoint: "/skills",
    collections: ["skills"],
    columns: ["name", "description", "license"],
    legacy: "/system/skills",
  },
];

export function text(value: unknown): string {
  if (value === null || value === undefined || value === "") return "—";
  if (typeof value === "boolean") return value ? "Yes" : "No";
  if (Array.isArray(value)) return value.map(text).join(", ");
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}

export function rowTitle(row: DataRow): string {
  return text(row.name || row.summary || row.description || row.label || row.key || row.url || row.id);
}

export function filtered(rows: DataRow[], query: string): DataRow[] {
  const terms = query.toLocaleLowerCase().trim().split(/\s+/).filter(Boolean);
  return rows.filter((row) =>
    terms.every((term) =>
      Object.entries(row)
        .filter(([key]) => !/^(api_key|secret|password|token|authorization)$/i.test(key))
        .some(([, value]) => text(value).toLocaleLowerCase().includes(term)),
    ),
  );
}
