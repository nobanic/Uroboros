import type { AssetInterceptRule } from "../types";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";
import { OrderedRecords } from "./ordered-records";

const fields = (body: Record<string, unknown>) => ({
  enabled: body.enabled !== false,
  kind: body.kind as AssetInterceptRule["kind"],
  pattern: String(body.pattern ?? ""),
  note: String(body.note ?? ""),
});

export class AssetGuardRuntime {
  private readonly rules: OrderedRecords<AssetInterceptRule>;
  constructor(seed: AssetInterceptRule[]) {
    this.rules = new OrderedRecords(seed);
  }

  respond = ({ method, path, segments, body }: DemoRequest): unknown => {
    if (path === "/asset-intercept/rules") {
      if (method === "GET") return { rules: this.rules.rows };
      if (method === "POST") {
        const now = new Date().toISOString();
        return this.rules.append((id) => ({ id, ...fields(body), builtin: false, created_at: now, updated_at: now }));
      }
    }
    if (segments[0] !== "asset-intercept" || segments[1] !== "rules" || segments.length < 3) return UNHANDLED;
    const id = Number(segments[2]),
      rule = this.rules.get(id);
    if (!rule) throw new Error("Asset interception rule not found");
    if (segments[3] === "toggle" && method === "POST") {
      const next = { ...rule, enabled: body.enabled !== false, updated_at: new Date().toISOString() };
      this.rules.replace(id, next);
      return { ok: true, enabled: next.enabled };
    }
    if (segments.length === 3 && method === "PUT") {
      const next = { ...rule, ...fields(body), updated_at: new Date().toISOString() };
      this.rules.replace(id, next);
      return next;
    }
    if (segments.length === 3 && method === "DELETE") {
      this.rules.remove(id);
      return { deleted: id };
    }
    return UNHANDLED;
  };
}
