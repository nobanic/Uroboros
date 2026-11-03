import type { MCPServer, MCPTool, Tool } from '../types';
import type { ScenarioRecords } from './scenario-records';

function objectSchema(properties: Record<string, string>, required?: string[]) {
  return {
    type: 'object', properties: Object.fromEntries(Object.entries(properties).map(([name, type]) => [name, { type }])),
    ...(required === undefined ? {} : { required }),
  };
}

/** The catalog owns schemas and metadata only; it cannot register or execute real tools. */
export function createToolCatalog(records: ScenarioRecords): Tool[] {
  function builtin(
    key: string, family: string, description: string, fields: Record<string, string>,
    agents: string[], calls: number, required?: string[],
  ) {
    return records.tool({ key, family, description, schema: objectSchema(fields, required), agents, calls });
  }
  return [
    builtin("katana_run", "Katana", "Start task-scoped Katana discovery through the frozen scope and fail-closed evidence proxy. Verified exchanges are imported as assets.",
      { "mode": "string", "max_depth": "integer", "max_requests": "integer", "max_duration_seconds": "integer" }, ["planner", "worker", "pentest"], 42),
    builtin("katana_status", "Katana", "Return the latest Katana discovery job, immutable scope fingerprints, and runtime counters.",
      {}, ["planner", "worker", "pentest"], 118),
    builtin("katana_results", "Katana", "List candidate and proxy-verified endpoint discoveries from a task-bound Katana job.",
      { "job_id": "integer", "limit": "integer", "offset": "integer" }, ["planner", "worker", "pentest"], 73),
    builtin("katana_cancel", "Katana", "Cancel the active task-scoped Katana process while retaining completed evidence and discoveries.",
      {}, ["planner", "worker", "pentest"], 3),
    builtin("arsenal_discover", "Arsenal", "Run the owned subfinder, DNS, naabu, and httpx discovery workflow against an authorized root domain.",
      { "domain": "string", "ports": "array", "budget_seconds": "integer", "max_requests": "integer" }, ["planner", "worker", "pentest"], 27, ["domain"]),
    builtin("arjun_discover", "Arsenal", "Discover query-parameter names on a task-scoped endpoint with verified Traffic provenance.",
      { "endpoint_id": "integer" }, ["planner", "worker", "pentest"], 16, ["endpoint_id"]),
    builtin("http_request", "Traffic & evidence", "Send a task-scoped HTTP request through the recording proxy and retain its evidence reference.",
      { "method": "string", "url": "string", "body": "string" }, ["planner", "worker", "pentest"], 1842, ["method", "url"]),
    builtin("discovery_review", "Discovery governance", "Review deterministic discovery evidence before promotion into the planning workflow.",
      {}, ["planner", "mainagent"], 21),
    builtin("report_finding", "Finding lifecycle", "Create a security finding and attach it to the supporting exploration and evidence nodes.",
      { "vulnclass": "string", "severity": "string", "summary": "string", "evidence": "string" }, ["worker"], 96, ["vulnclass", "severity", "summary"]),
    builtin("upsert_asset", "Agent core", "Create or update a normalized asset node in the active operation graph.",
      { "type": "string", "value": "string" }, ["worker"], 417),
    builtin("list_goals", "Agent core", "List the active operation goals and their current proof or completion state.",
      {}, ["planner"], 238),
    records.tool({
      "key": "nuclei_scan",
      "family": "Custom",
      "description": "Run a bounded Nuclei template against an explicitly scoped target.",
      "schema": {
        "type": "object",
        "properties": {
          "target": {
            "type": "string"
          },
          "template": {
            "type": "string"
          }
        },
        "required": [
          "target"
        ]
      },
      "agents": [
        "worker"
      ],
      "exec": {
        "command": "nuclei",
        "args": [
          "-u",
          "{{target}}"
        ]
      },
      "calls": 31,
      "kind": "command"
    }),
  ];
}

type ConnectorLocation = { http: string } | { command: string; args: string[] };
type ConnectorTool = readonly [name: string, description: string];

export function createConnectorCatalog() {
  const mcpServers: MCPServer[] = [];
  const mcpToolsById: Record<number, MCPTool[]> = {};
  function connector(id: number, name: string, location: ConnectorLocation, tools: ConnectorTool[]) {
    const rows = tools.map(([name, description]) => ({ name, description }));
    mcpToolsById[id] = rows;
    mcpServers.push({
      id, name, transport: 'http' in location ? 'http' : 'stdio',
      ...('http' in location ? { url: location.http } : { command: location.command }),
      args: 'http' in location ? [] : [...location.args], env: {}, enabled: true,
      tools: rows.map(row => row.name),
    });
  }
  connector(1, "shodan", { "http": "https://mcp.shodan.io/sse" }, [
    ["host_info", "Retrieve the Shodan host profile for an IP address."],
    ["search", "Search Shodan's indexed services and banners."],
    ["dns_resolve", "Resolve a hostname to its current DNS records."],
  ]);
  connector(2, "playwright", { "command": "npx", "args": ["-y", "@playwright/mcp"] }, [
    ["browser_navigate", "Navigate the controlled browser to a URL."],
    ["browser_click", "Activate a visible element in the current page."],
    ["browser_snapshot", "Capture an accessibility snapshot of the rendered page."],
  ]);
  return { mcpServers, mcpToolsById };
}
