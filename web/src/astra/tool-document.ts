import type { Tool } from "@/lib/types";
import { isRow, type DataRow } from "./client";
import { formDocument, type FormGroup, type FormValues } from "./forms";

export const trafficToolKeys = new Set([
  "traffic_search",
  "traffic_get",
  "traffic_blob",
  "traffic_match",
  "traffic_routes",
  "traffic_contract_worklist",
  "http_request",
]);
export function toolFamily(tool: Tool): string {
  if (tool.family?.trim()) return tool.family.trim();
  if (!tool.system) return "Custom";
  if (tool.key.startsWith("katana_")) return "Katana";
  if (tool.key.startsWith("arsenal_") || ["arjun_discover", "archive_discover"].includes(tool.key)) return "Arsenal";
  if (tool.key.startsWith("traffic_") || ["http_request", "artifact_transform"].includes(tool.key))
    return "Traffic & evidence";
  return "Agent core";
}
export const coreToolFamilies = new Set(["Katana", "Arsenal", "Traffic & evidence"]);

export type SchemaEdit = {
  path: string[];
  name: string;
  type: string;
  required: boolean;
  description: string;
  scalar: boolean;
  defaultEnabled: boolean;
  defaultText: string;
  defaultEdited: boolean;
};
function schemaAt(schema: DataRow, path: string[]): DataRow | undefined {
  let current: unknown = schema;
  for (const part of path) {
    if (!isRow(current) || !Object.hasOwn(current, part)) return;
    current = current[part];
  }
  return isRow(current) ? current : undefined;
}

export function schemaEdits(schema: DataRow): SchemaEdit[] {
  const edits: SchemaEdit[] = [],
    queue: { node: DataRow; path: string[]; names: string[]; required: boolean }[] = [];
  function enqueue(parent: DataRow, path: string[], names: string[]) {
    if (!isRow(parent.properties)) return;
    const required = Array.isArray(parent.required) ? parent.required.map(String) : [];
    for (const [name, child] of Object.entries(parent.properties).reverse())
      if (isRow(child))
        queue.push({
          node: child,
          path: [...path, "properties", name],
          names: [...names, name],
          required: required.includes(name),
        });
  }
  enqueue(schema, [], []);
  while (queue.length) {
    const entry = queue.pop()!,
      type = String(entry.node.type || ""),
      value = entry.node.default;
    edits.push({
      path: entry.path,
      name: entry.names.join(" → "),
      type,
      required: entry.required,
      description: String(entry.node.description || ""),
      scalar: ["string", "integer", "number", "boolean"].includes(type),
      defaultEnabled: Object.hasOwn(entry.node, "default"),
      defaultText: value == null ? "" : String(value),
      defaultEdited: false,
    });
    if (isRow(entry.node.items)) enqueue(entry.node.items, [...entry.path, "items"], [...entry.names, "items"]);
    enqueue(entry.node, entry.path, entry.names);
  }
  return edits;
}
function scalarDefault(row: SchemaEdit): unknown {
  if (row.type === "string") return row.defaultText;
  const text = row.defaultText.trim();
  if (row.type === "boolean") {
    if (text !== "true" && text !== "false") throw new Error(`Default for ${row.name} must be true or false.`);
    return text === "true";
  }
  const number = Number(text);
  if (!text || !Number.isFinite(number)) throw new Error(`Default for ${row.name} must be a finite number.`);
  return number;
}
export function schemaDocument(schema: DataRow, edits: SchemaEdit[]): DataRow {
  const result = structuredClone(schema);
  for (const row of edits) {
    const target = schemaAt(result, row.path);
    if (!target) throw new Error("A schema field changed while it was being edited.");
    target.description = row.description;
    if (!row.scalar) continue;
    if (!row.defaultEnabled) delete target.default;
    else if (row.defaultEdited || !Object.hasOwn(target, "default")) target.default = scalarDefault(row);
  }
  return result;
}

export function toolMetadataDocument(tool: Tool, value: FormValues, rows: SchemaEdit[]) {
  return {
    description: String(value.description || ""),
    enabled: Boolean(value.enabled),
    agents: Array.isArray(value.agents) ? [...new Set(value.agents.map(String))] : [],
    schema: schemaDocument(tool.schema, rows),
  };
}
export function toolMetadataDraft(tool: Tool): FormValues {
  return { description: tool.description, enabled: tool.enabled, agents: [...(tool.agents || [])] };
}
export function toolMetadataForm(trafficBlocked: boolean): FormGroup[] {
  return [
    {
      title: "Model-facing metadata",
      fields: [
        { key: "description", label: "Tool description", type: "textarea" },
        { key: "enabled", label: "Tool enabled", type: "checkbox", disabled: trafficBlocked },
      ],
    },
  ];
}

function jsonObject(value: unknown, title: string): DataRow {
  let result: unknown;
  try {
    result = String(value || "").trim() ? JSON.parse(String(value)) : {};
  } catch {
    throw new Error(`${title} must contain valid JSON.`);
  }
  if (!isRow(result)) throw new Error(`${title} must be a JSON object.`);
  return result;
}
export function customToolDraft(tool?: Tool): FormValues {
  const exec = isRow(tool?.exec) ? tool.exec : {};
  return {
    key: tool?.key || "",
    description: tool?.description || "",
    kind: tool?.kind || "shell",
    agents: [...(tool?.agents || [])],
    enabled: tool?.enabled ?? true,
    deferred: tool?.deferred ?? false,
    schemaText: tool?.schema && Object.keys(tool.schema).length ? JSON.stringify(tool.schema, null, 2) : "",
    exec: {
      command: String(exec.command || ""),
      code: String(exec.code || ""),
      method: String(exec.method || "GET"),
      url: String(exec.url || ""),
      headersText: exec.headers ? JSON.stringify(exec.headers, null, 2) : "",
      body: String(exec.body || ""),
      timeout_ms: exec.timeout_ms == null ? "" : String(exec.timeout_ms),
      proxy: String(exec.proxy || ""),
      use_recording_proxy: Boolean(exec.use_recording_proxy),
    },
  };
}
const isKind = (kind: string) => (value: FormValues) => value.kind === kind;
export function customToolForm(create: boolean): FormGroup[] {
  return [
    {
      title: "Custom tool",
      fields: [
        { key: "key", label: "Stable tool key", required: true, disabled: !create },
        { key: "description", label: "Description sent to the model", type: "textarea" },
        {
          key: "kind",
          label: "Execution adapter",
          options: [
            ["shell", "Shell environment declaration"],
            ["command", "Command template"],
            ["script", "Python script"],
            ["http", "HTTP request"],
          ],
        },
        { key: "enabled", label: "Tool enabled", type: "checkbox" },
        { key: "deferred", label: "Load schema on demand", type: "checkbox", when: (value) => value.kind !== "shell" },
        {
          key: "schemaText",
          label: "Parameter JSON Schema",
          type: "textarea",
          when: (value) => value.kind !== "shell",
        },
      ],
    },
    {
      title: "Execution",
      fields: [
        { key: "exec.command", label: "Command template", type: "textarea", required: true, when: isKind("command") },
        { key: "exec.code", label: "Python script", type: "textarea", required: true, when: isKind("script") },
        { key: "exec.method", label: "HTTP method", when: isKind("http") },
        { key: "exec.url", label: "URL template", required: true, when: isKind("http") },
        { key: "exec.headersText", label: "HTTP headers as JSON", type: "textarea", when: isKind("http") },
        { key: "exec.body", label: "Request body", type: "textarea", when: isKind("http") },
        { key: "exec.proxy", label: "Proxy URL", when: isKind("http") },
        { key: "exec.use_recording_proxy", label: "Use the recording proxy", type: "checkbox", when: isKind("http") },
        {
          key: "exec.timeout_ms",
          label: "Timeout in milliseconds (blank uses adapter default)",
          type: "number",
          min: 0,
          step: 1,
          when: (value) => value.kind !== "shell",
        },
      ],
    },
  ];
}

export function customExecution(value: FormValues): DataRow {
  const kind = String(value.kind),
    exec = isRow(value.exec) ? value.exec : {};
  if (kind === "shell") return {};
  const timeout = String(exec.timeout_ms ?? "").trim(),
    common: DataRow = {};
  if (timeout) {
    const number = Number(timeout);
    if (!Number.isSafeInteger(number) || number < 0) throw new Error("Timeout must be a non-negative whole number.");
    common.timeout_ms = number;
  }
  if (kind === "command") return { command: String(exec.command || ""), ...common };
  if (kind === "script") return { code: String(exec.code || ""), ...common };
  if (kind !== "http") throw new Error("Choose a supported execution adapter.");
  const headers = jsonObject(exec.headersText, "HTTP headers");
  if (Object.values(headers).some((value) => typeof value !== "string"))
    throw new Error("HTTP header values must be strings.");
  return {
    method: String(exec.method || "GET"),
    url: String(exec.url || ""),
    headers,
    body: String(exec.body || ""),
    ...common,
    proxy: String(exec.proxy || ""),
    use_recording_proxy: Boolean(exec.use_recording_proxy),
  };
}
export function customToolDocument(value: FormValues, create: boolean) {
  const fields = formDocument(customToolForm(create), value),
    key = String(value.key || "").trim(),
    kind = String(fields.kind),
    schema = jsonObject(value.schemaText, "Parameter schema");
  if (create && !/^[a-z][a-z0-9_]*$/.test(key))
    throw new Error(
      "The tool key must start with a lowercase letter and contain lowercase letters, digits or underscores.",
    );
  if (kind === "http" && (!isRow(schema.properties) || !Object.keys(schema.properties).length))
    throw new Error("HTTP tools need a parameter schema with at least one property.");
  return {
    ...(create ? { key } : {}),
    description: String(fields.description || ""),
    kind,
    enabled: Boolean(fields.enabled),
    deferred: kind === "shell" ? false : Boolean(fields.deferred),
    agents: Array.isArray(value.agents) ? [...new Set(value.agents.map(String))] : [],
    schema: kind === "shell" ? {} : schema,
    exec: customExecution(value),
  };
}
export function customToolProbe(value: FormValues, params: string) {
  return { kind: String(value.kind), exec: customExecution(value), params: jsonObject(params, "Test parameters") };
}
