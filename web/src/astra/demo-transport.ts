type DemoHandler = (method: string, path: string, body?: BodyInit | null) => Promise<unknown>;

/** Explicit HTTP failure metadata crosses the demo adapters; message text is never a status signal. */
export function demoErrorStatus(error: unknown): number | undefined {
  if (!(error instanceof Error) || !("status" in error)) return undefined;
  const status = error.status;
  return typeof status === "number" && Number.isInteger(status) && status >= 400 && status <= 599 ? status : undefined;
}

/** Adapt the in-browser demonstration API to the same Response contract as live HTTP. */
export function createDemoTransport(resolve: () => Promise<DemoHandler>): typeof fetch {
  return async (input, options = {}) => {
    if (typeof input !== "string" || !input.startsWith("/api/") || input.startsWith("/api//")) {
      throw new Error("A local demonstration API path is required");
    }
    const signal = options.signal;
    const check = () => {
      if (signal?.aborted) throw signal.reason ?? new DOMException("The request was aborted", "AbortError");
    };
    check();
    const handle = await resolve();
    check();
    let result: unknown;
    let status = 200;
    try {
      result = await handle(options.method ?? "GET", input.slice(4), options.body);
    } catch (error) {
      check();
      status = demoErrorStatus(error) ?? 500;
      result = { error: error instanceof Error ? error.message : String(error) };
    }
    check();
    if (result === undefined) return new Response(null, { status: 204 });
    return new Response(JSON.stringify(result), { status, headers: { "Content-Type": "application/json" } });
  };
}
