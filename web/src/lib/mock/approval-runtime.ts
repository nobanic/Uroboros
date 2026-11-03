import type { InterceptApprovalRow, InterceptAudit, InterceptPending, InterceptRule } from "../types";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";
import { OrderedRecords } from "./ordered-records";

type Seed = {
  history: InterceptApprovalRow[];
  pending: InterceptPending[];
  details: Record<number, InterceptAudit>;
  rules: InterceptRule[];
};
const rejection = () => new Error("uroboros.lab demonstration record.");

/** Pending reads and history decisions intentionally retain independent record copies and original precedence. */
export class ApprovalRuntime {
  private readonly history: OrderedRecords<InterceptApprovalRow>;
  private readonly pending: OrderedRecords<InterceptPending>;
  private readonly details: Record<number, InterceptAudit>;
  constructor(private readonly seed: Seed) {
    this.history = new OrderedRecords(seed.history);
    this.pending = new OrderedRecords(seed.pending);
    this.details = structuredClone(seed.details);
  }

  private decide(id: number, body: Record<string, unknown>) {
    const row = this.history.get(id) ?? this.pending.get(id);
    if (row?.status !== "pending") throw rejection();
    if (body.decision !== "allowed" && body.decision !== "denied") throw rejection();
    row.status = body.decision;
    row.decided_at = new Date().toISOString();
    const audit = this.details[id];
    if (audit) {
      const allowed = body.decision === "allowed";
      audit.effective_action = allowed ? "allow" : "deny";
      audit.decision_reason = "uroboros.lab demonstration record.";
      audit.execution_status = allowed ? "unknown" : "not_executed";
      audit.output = allowed ? "uroboros.lab demonstration record." : "";
    }
    return { ok: true };
  }

  private list({ segments, query }: DemoRequest) {
    const status = query.get("status") || "",
      source = query.get("decision_source") || "";
    if (status && !["pending", "allowed", "denied", "timeout"].includes(status)) throw rejection();
    if (source && !["model", "rule", "unknown"].includes(source)) throw rejection();
    const selected = this.history.rows.filter((row) => {
      const origin =
        row.decision_source ||
        (row.rule_id ? "rule" : row.reason?.startsWith("uroboros.lab demonstration record.") ? "model" : "unknown");
      return (
        (segments[1] !== "task" || row.task_id === decodeURIComponent(segments[2])) &&
        (!status || row.status === status) &&
        (!source || origin === source)
      );
    });
    if (!query.has("page") && !query.has("size") && !status && !source)
      return { items: selected, total: selected.length };
    const page = Math.max(1, Number(query.get("page")) || 1),
      size = Math.min(100, Math.max(1, Number(query.get("size")) || 20));
    const offset = (page - 1) * size;
    return { items: selected.slice(offset, offset + size), total: selected.length, page, page_size: size };
  }

  respond = (request: DemoRequest): unknown => {
    const { method, path, segments, body } = request;
    if (path === "/intercept/rules" && method === "GET") return { rules: this.seed.rules };
    if (segments[0] === "intercept" && segments[1] === "rules" && segments[3] === "toggle")
      return { ok: true, enabled: body.enabled ?? true };
    if (path === "/intercept/pending" && method === "GET")
      return { pending: this.history.rows.filter((row) => row.status === "pending") };
    if (segments[0] === "intercept" && segments[1] === "pending" && segments[3] === "decide")
      return this.decide(Number(segments[2]), body);
    if (segments[0] === "intercept" && segments[1] === "history" && segments.length === 3) {
      const row = this.history.get(Number(segments[2]));
      if (!row) throw rejection();
      return { ...row, audit: this.details[row.id] ?? null };
    }
    if (segments[0] === "intercept" && segments[1] === "pending" && segments.length === 3 && method === "GET")
      return this.pending.get(Number(segments[2])) ?? null;
    if (path === "/intercept/history" || (segments[0] === "intercept" && segments[1] === "task"))
      return this.list(request);
    if (path === "/intercept/tool-config") return { enabled_tools: ["bash"] };
    if (path === "/intercept/judge") {
      if (method === "GET")
        return {
          enabled: false,
          profile_id: 0,
          prompt: "",
          timeout_seconds: 15,
          fail_action: "allow",
          ask_timeout_seconds: 300,
          ask_timeout_action: "deny",
        };
      if (method === "PUT") return { ok: true };
    }
    if (path === "/intercept/judge/usage" && method === "GET")
      return { calls: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0, daily: [] };
    return UNHANDLED;
  };
}
