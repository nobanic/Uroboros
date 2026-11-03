export const UNHANDLED = Symbol("unhandled demonstration request");

export type DemoRequest = Readonly<{
  method: string;
  path: string;
  segments: string[];
  query: URLSearchParams;
  body: Record<string, unknown>;
}>;

export type DemoResponder = (request: DemoRequest) => unknown;
type Ports = {
  beforeRequest: () => void;
  resolve: DemoResponder;
  present: <T>(value: T, key?: string) => T;
};

const aliases: readonly (readonly [RegExp, string])[] = [
  [/t-uroboros\.lab-/gi, "t-acme-"],
  [/ss-uroboros\.lab/gi, "ss-acme"],
  [/uroboros_lab_prod/gi, "acme_prod"],
];
const canonical = (value: string) =>
  aliases.reduce((text, [pattern, replacement]) => text.replace(pattern, replacement), value);

/** A demo request preserves raw-path matching, filtered segments, decoder quirks and virtual-clock ordering. */
export class DemoProtocol {
  constructor(private readonly ports: Ports) {}

  private decode(method: string, rawPath: string, body?: BodyInit | null): DemoRequest {
    const [path, query] = rawPath.split("?");
    const params = new URLSearchParams(query ?? "");
    const segments = path.split("/").filter(Boolean);
    const verb = method.toUpperCase();
    let value: Record<string, unknown> = {};
    if (typeof body === "string") {
      try {
        value = JSON.parse(body) as Record<string, unknown>;
      } catch {
        /* Invalid JSON retains the established empty-body fallback. */
      }
    }
    return Object.freeze({ method: verb, path, segments, query: params, body: value });
  }

  private fallback({ method, path }: DemoRequest): unknown {
    if (["POST", "PUT", "PATCH", "DELETE"].includes(method)) return { ok: true };
    const collection =
      /(\/(tasks|profiles|conversations|rules|history|projects|tokens|agents|servers|skills|tools|findings|intents)s?$)|s$/;
    return collection.test(path) ? [] : {};
  }

  handle = async <T>(method: string, rawPath: string, body?: BodyInit | null): Promise<T> => {
    const path = canonical(rawPath);
    const input = typeof body === "string" ? canonical(body) : body;
    try {
      this.ports.beforeRequest();
      await new Promise((resolve) => setTimeout(resolve, 120));
      const request = this.decode(method, path, input);
      const result = this.ports.resolve(request);
      return this.ports.present(result === UNHANDLED ? this.fallback(request) : result) as T;
    } catch (error) {
      if (error instanceof Error) error.message = this.ports.present(error.message, "message");
      throw error;
    }
  };
}
