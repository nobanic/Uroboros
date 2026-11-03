import { type DemoRequest, type DemoResponder, UNHANDLED } from "./demo-protocol";

type SegmentRule = {
  pattern: readonly (string | null)[];
  respond: DemoResponder;
  method?: string;
  length?: number;
  minimum?: number;
};

/** Exact raw routes and filtered-segment routes are separate dialects, with explicit method fallbacks. */
export class RouteBook {
  private readonly routes = new Map<string, ReadonlyMap<string, DemoResponder>>();
  private readonly segments: SegmentRule[] = [];

  exact(path: string, methods: Record<string, DemoResponder>): this {
    if (!path.startsWith("/") || this.routes.has(path))
      throw new Error(`Duplicate or invalid demonstration route: ${path}`);
    this.routes.set(path, new Map(Object.entries(methods)));
    return this;
  }

  any(path: string, respond: DemoResponder): this {
    return this.exact(path, { "*": respond });
  }

  prefix(
    pattern: readonly (string | null)[],
    respond: DemoResponder,
    bounds: Omit<SegmentRule, "pattern" | "respond"> = {},
  ): this {
    if (!pattern.length || pattern[0] === null) throw new Error("A demonstration prefix must name its root");
    this.segments.push({ ...bounds, pattern: Object.freeze([...pattern]), respond });
    return this;
  }

  respond = (request: DemoRequest): unknown => {
    const methods = this.routes.get(request.path),
      exact = methods?.get(request.method) ?? methods?.get("*");
    if (exact) return exact(request);
    for (const rule of this.segments) {
      if (rule.method !== undefined && rule.method !== request.method) continue;
      if (rule.length !== undefined && rule.length !== request.segments.length) continue;
      if (rule.minimum !== undefined && request.segments.length < rule.minimum) continue;
      if (rule.pattern.every((value, index) => value === null || request.segments[index] === value))
        return rule.respond(request);
    }
    return UNHANDLED;
  };
}
