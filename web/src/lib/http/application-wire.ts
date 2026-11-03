type WirePorts = {
  fetch: typeof fetch;
  token: () => string | null;
  expired: () => void;
  mock: { enabled: () => boolean; reply: (method: string, path: string, body: BodyInit | null) => Promise<unknown> };
};

/** The established workspace protocol owns its auth/error/body dialect separately from console requests. */
export class ApplicationWire {
  constructor(private readonly ports: WirePorts) {}

  private headers(init?: RequestInit, json = false) {
    const token = this.ports.token();
    return {
      ...(json && init?.body ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
      ...(init?.headers as Record<string, string> | undefined),
    };
  }

  /** Raw text/file operations choose their existing error and download policy at the resource boundary. */
  raw = (path: string, init?: RequestInit) => this.ports.fetch(`/api${path}`, { ...init, headers: this.headers(init) });

  json = async <T>(path: string, init?: RequestInit): Promise<T> => {
    if (this.ports.mock.enabled())
      return this.ports.mock.reply(init?.method ?? "GET", path, init?.body ?? null) as Promise<T>;
    const response = await this.ports.fetch(`/api${path}`, { ...init, headers: this.headers(init, true) });
    if (response.status === 401) {
      this.ports.expired();
      throw new Error("Unauthorized");
    }
    if (response.ok) {
      if (response.status === 204) return undefined as T;
      return response.json();
    }
    const fallback = `${init?.method ?? "GET"} ${path}: ${response.status}`;
    let rejection: unknown;
    try {
      rejection = await response.json();
    } catch {
      rejection = null;
    }
    const reason = rejection && typeof rejection === "object" && "error" in rejection ? rejection.error : null;
    throw new Error(typeof reason === "string" && reason.trim() ? reason.trim() : fallback);
  };

  read = <T>(path: string) => this.json<T>(path);

  command(method: "POST" | "PUT" | "PATCH" | "DELETE") {
    return <T>(path: string, body?: unknown) =>
      this.json<T>(path, { method, body: body ? JSON.stringify(body) : undefined });
  }
}
