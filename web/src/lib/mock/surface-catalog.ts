import type { CommandRecord, LLMRecordItem } from "../types";
import * as D from "./data";
import { RouteBook } from "./route-book";

const envelope =
  <K extends string, T>(field: K, rows: () => T) =>
  () => ({ [field]: rows() });
const counted =
  <K extends string, T>(field: K, rows: () => T[]) =>
  () => {
    const items = rows();
    return { [field]: items, total: items.length };
  };
const ok = () => ({ ok: true });

function toolTotals(rows: CommandRecord[]) {
  const groups = new Map<string, { tool: string; total: number; errors: number }>();
  for (const row of rows) {
    const key = row.tool || "-",
      previous = groups.get(key);
    groups.set(key, {
      tool: key,
      total: (previous?.total ?? 0) + 1,
      errors: (previous?.errors ?? 0) + (row.is_error ? 1 : 0),
    });
  }
  return [...groups.values()].sort((left, right) => right.total - left.total || left.tool.localeCompare(right.tool));
}

/** Recorded catalog surfaces expose fixtures only. Stubs keep their established method gates and never execute a provider/tool. */
export function surfaceCatalog(records: () => LLMRecordItem[]) {
  const book = new RouteBook();
  const fixed = [
    ["/exploration/frontier", () => D.frontier],
    ["/exploration/graph", () => D.explorationGraph],
    ["/tokens/daily", () => D.dailyTokens],
    ["/tokens/conversations", () => D.convTokens],
    ["/audit", () => D.audit],
    ["/traffic/exchange", () => D.trafficDetail],
    ["/auth/status", () => ({ initialized: true })],
    ["/auth/login", () => ({ token: "mock-demo" })],
    ["/auth/init", () => ({ token: "mock-demo" })],
    ["/auth/change-password", ok],
  ] as const;
  for (const [path, respond] of fixed) book.any(path, respond);
  book.any("/workspace/list", ({ query }) => D.workspaceList(query.get("path") ?? ""));
  book.any("/workspace/read", ({ query }) => D.workspaceRead(query.get("path") ?? ""));
  book.prefix(["tasks", null, "coverage"], () => D.coverage, { length: 3 });
  book.prefix(["tasks", null, "coverage-graph"], () => D.coverageGraph);
  book.prefix(["tasks", null, "asset-refs"], ({ query }) => D.assetRefsFor(Number(query.get("asset_id") ?? 0)));

  book.exact("/traffic", { DELETE: () => ({ deleted: 0 }), "*": () => D.traffic });
  book.exact("/traffic/hosts", {
    DELETE: ({ body }) => ({ deleted: (body.hosts as unknown[])?.length ?? 0 }),
    "*": envelope("hosts", () => D.trafficHosts),
  });
  book.exact("/settings", { GET: () => D.settings, PUT: ({ body }) => ({ ...D.settings, ...body }) });
  book.any("/settings/web-search/test", () => ({ ok: true, count: 5, backend: D.settings.web_search_backend }));
  book.any("/settings/python/detect", () => ({ python_interpreter: "/usr/bin/python3" }));
  book.any("/chat", () => ({ reply: "uroboros.lab demonstration record. Context: demo, work, agent.", mode: "hint" }));
  book.any("/gc", () => ({ removed: 0 }));
  book.exact("/commands", { GET: counted("commands", () => D.commandRecords) });
  book.exact("/commands/stats", { GET: () => ({ stats: toolTotals(D.commandRecords) }) });

  book.exact("/llm/records", { GET: counted("records", records), DELETE: () => ({ deleted: 0 }) });
  book.any("/llm/records/tasks", () => {
    const tasks = new Map<string, number>();
    for (const record of records()) if (record.task_id) tasks.set(record.task_id, (tasks.get(record.task_id) ?? 0) + 1);
    return { tasks: [...tasks].map(([task_id, count]) => ({ task_id, count })) };
  });
  book.prefix(["llm", "records"], ({ segments }) => D.llmRecordDetail(Number(segments[2]), records()), {
    length: 3,
    method: "GET",
  });
  book.exact("/llm", { GET: () => D.llmConfig, POST: ok });
  book.any("/llm/test", ({ body }) => ({
    ok: true,
    latency_ms: 128,
    model: String(body.model ?? "claude-opus-4-8"),
    reply: "OK",
  }));
  book.exact("/llm/profiles", {
    GET: envelope("profiles", () => D.llmProfiles),
    POST: ({ body }) => ({ id: Number(body.id) || 3 }),
  });
  book.any("/llm/profiles/active", ok);
  book.exact("/llm/pool", { GET: () => D.llmPool });
  book.any("/llm/pool/reset", () => ({
    ...D.llmPool,
    chain: D.llmPool.chain.map((row) => ({ ...row, state: "ok", fails: 0, cooldown_secs: 0 })),
  }));
  book.prefix(["llm", "profiles"], ({ segments }) => ({ deleted: Number(segments[2]) }), {
    length: 3,
    method: "DELETE",
  });

  book.exact("/agents", {
    GET: envelope("agents", () => D.agents),
    POST: ({ body }) => ({
      id: "9",
      key: String(body.key ?? "custom"),
      name: String(body.name ?? ""),
      role: "custom",
      builtin: false,
      enabled: true,
    }),
  });
  book.prefix(["agents"], ({ segments }) => D.agentDetail(segments[1]), { length: 2, method: "GET" });
  book.prefix(["agents", null, "triggers"], () => ({ triggers: [] }), { method: "GET" });
  book.prefix(["agents", null, "prompts"], ({ segments }) => ({ versions: D.agentDetail(segments[1]).versions }));
  book.prefix(["agents", null, "variables"], ({ segments }) => ({ variables: D.agentDetail(segments[1]).variables }));
  book.prefix(["agents", null, "prompt", "preview"], ({ body }) => ({
    rendered: String(body.template ?? "").replace(/\{\{\.(\w+)\}\}/g, "\u00ab$1\u00bb"),
  }));
  book.prefix(["agents", null, "visibility"], ({ segments }) => D.agentDetail(segments[1]).visibility, {
    method: "GET",
  });

  book.exact("/tools", { GET: envelope("tools", () => D.tools) });
  book.exact("/tools/custom", { POST: ({ body }) => ({ key: String(body.key ?? "custom-tool") }) });
  book.any("/tools/custom/test", () => ({
    output: "The mock tool completed successfully and returned demonstration evidence. Context: demo.",
    is_error: false,
  }));
  book.exact("/mcp", { GET: envelope("servers", () => D.mcpServers), POST: () => ({ id: 3 }) });
  const mcpTools = ({ segments }: { segments: string[] }) => ({ tools: D.mcpToolsById[Number(segments[1])] ?? [] });
  book.prefix(["mcp", null, "tools"], mcpTools);
  book.prefix(["mcp", null, "refresh"], mcpTools);
  book.prefix(["mcp"], ({ segments }) => ({ deleted: Number(segments[1]) }), { length: 2, method: "DELETE" });
  book.exact("/skills", {
    GET: envelope("skills", () => D.skills),
    POST: ({ body }) => ({ name: String(body.name ?? "new-skill") }),
  });
  book.any(
    "/skills/missing",
    envelope("missing", () => D.missingSkills),
  );
  book.prefix(
    ["skills", null, "usage"],
    envelope("calls", () => D.skillCalls),
  );
  book.prefix(["skills", null, "files"], () => ({ files: ["SKILL.md"] }), { length: 3 });
  book.prefix(
    ["skills", null, "files"],
    ({ segments }) => ({
      content:
        "Demo operator note: review the linked evidence and continue the verified assessment workflow. Context: SKILL.md, demo, skill.",
      file: segments.slice(3).join("/"),
    }),
    { minimum: 4 },
  );
  book.prefix(["visibility"], () => ({ agents: [] }), { method: "GET" });
  // This literal resource must precede the numeric-record prefix for every method.
  book.any("/llm/records/by-model", () => ({
    models: [
      {
        model: "claude-opus-4-6",
        calls: 42,
        input_tokens: 1250000,
        output_tokens: 86000,
        cache_read_tokens: 940000,
        cache_write_tokens: 120000,
      },
      {
        model: "claude-haiku-4-5",
        calls: 18,
        input_tokens: 320000,
        output_tokens: 24000,
        cache_read_tokens: 180000,
        cache_write_tokens: 20000,
      },
    ],
  }));
  return book;
}
