import { formDocument, type FormGroup, type FormValues } from "./forms";
import { payloadObject } from "./graph-model";

const local = (values: FormValues) => values.transport === "stdio";
const remote = (values: FormValues) => !local(values);
export const mcpForm: FormGroup[] = [
  {
    title: "Connection",
    fields: [
      { key: "name", label: "Name", required: true },
      {
        key: "transport",
        label: "Transport",
        options: [
          ["stdio", "Local process (stdio)"],
          ["http", "Streamable HTTP"],
          ["sse", "Legacy SSE"],
        ],
      },
      { key: "command", label: "Command", required: true, when: local },
      {
        key: "arguments",
        label: "Arguments",
        type: "list",
        when: local,
        hint: "Each entry is one argument. Spaces, empty values and argument order are preserved.",
      },
      { key: "url", label: "Remote URL", required: true, when: remote },
      {
        key: "environment",
        label: "Environment / request headers",
        type: "textarea",
        hint: "One KEY=value per line. Local connections use environment variables; remote connections send HTTP headers.",
      },
      { key: "insecure", label: "Allow an untrusted TLS certificate", type: "checkbox", when: remote },
      {
        key: "request_timeout_seconds",
        label: "Request timeout (seconds)",
        type: "number",
        min: 1,
        max: 3600,
        step: 1,
        when: (values) => values.transport === "http",
      },
      { key: "enabled", label: "Enabled", type: "checkbox" },
    ],
  },
];

export function mcpDraft(server?: FormValues): FormValues {
  const args = Array.isArray(server?.args) ? server.args.map(String) : [];
  const env = payloadObject(server?.env);
  return {
    name: "",
    transport: "stdio",
    command: "",
    url: "",
    insecure: false,
    enabled: true,
    request_timeout_seconds: 120,
    ...server,
    arguments: args,
    environment: Object.entries(env)
      .map(([key, value]) => `${key}=${value}`)
      .join("\n"),
  };
}

export function mcpDocument(values: FormValues): FormValues {
  const validated = formDocument(mcpForm, values);
  const env: Record<string, string> = {};
  for (const input of String(values.environment ?? "").split(/\r?\n/)) {
    const line = input.trim(),
      separator = line.indexOf("=");
    if (separator > 0) {
      const key = line.slice(0, separator).trim();
      if (key)
        Object.defineProperty(env, key, {
          value: line.slice(separator + 1).trim(),
          enumerable: true,
          configurable: true,
          writable: true,
        });
    }
  }
  const result: FormValues = {
    name: String(validated.name).trim(),
    transport: validated.transport,
    enabled: validated.enabled,
    env,
  };
  if (values.id) result.id = Number(values.id);
  if (local(values))
    Object.assign(result, { command: String(validated.command).trim(), args: validated.arguments, insecure: false });
  else {
    Object.assign(result, { url: String(validated.url).trim(), command: "", args: [], insecure: validated.insecure });
    const seconds = Number(values.request_timeout_seconds ?? 120);
    if (!Number.isInteger(seconds) || seconds < (values.transport === "http" ? 1 : 0) || seconds > 3600)
      throw new Error("Request timeout must be between 1 and 3600 seconds");
    result.request_timeout_seconds = seconds;
  }
  return result;
}

export function mcpToolName(serverName: string, qualified: string): string {
  const prefix = `mcp__${serverName}__`;
  return qualified.startsWith(prefix) ? qualified.slice(prefix.length) : qualified;
}
