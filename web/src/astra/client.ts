export type DataRow = Record<string, unknown>;

export class ApiFailure extends Error {
  status: number;
  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiFailure";
    this.status = status;
  }
}

export type SessionStore = {
  read(): string | null;
  save(value: string): void;
  clear(): void;
};

export const browserSession: SessionStore = {
  read: () =>
    typeof window === "undefined"
      ? null
      : window.localStorage.getItem("astra.session") ||
        window.localStorage.getItem("astra_token") ||
        (process.env.NEXT_PUBLIC_MOCK === "1" ? "mock-demo" : null),
  save: (value) => {
    window.localStorage.setItem("astra.session", value);
    window.localStorage.setItem("astra_token", value);
    document.cookie = `astra_token=${encodeURIComponent(value)}; Path=/; Max-Age=604800; SameSite=Lax${window.location.protocol === "https:" ? "; Secure" : ""}`;
  },
  clear: () => {
    window.localStorage.removeItem("astra.session");
    window.localStorage.removeItem("astra_token");
    document.cookie = "astra_token=; Path=/; Max-Age=0; SameSite=Lax";
  },
};

export function createClient(transport: typeof fetch, session: SessionStore) {
  return async function request<T>(
    endpoint: string,
    options: {
      method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
      body?: unknown;
      signal?: AbortSignal;
    } = {},
  ): Promise<T> {
    if (!endpoint.startsWith("/") || endpoint.startsWith("//")) throw new Error("An application API path is required");
    const headers = new Headers({ Accept: "application/json" });
    const credential = session.read();
    if (credential) headers.set("Authorization", `Bearer ${credential}`);
    if (options.body !== undefined) headers.set("Content-Type", "application/json");
    const response = await transport(`/api${endpoint}`, {
      // biome-ignore lint/nursery/useNullishCoalescing: Preserve the existing empty-method fallback at the transport boundary.
      method: options.method || "GET",
      headers,
      signal: options.signal,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      credentials: "same-origin",
      cache: "no-store",
    });
    if (response.status === 401) session.clear();
    if (response.status === 204) return undefined as T;
    let payload: unknown;
    try {
      payload = await response.json();
    } catch {
      throw new ApiFailure(
        response.status,
        response.ok ? "The server returned an unreadable response" : `Request failed (${response.status})`,
      );
    }
    if (!response.ok) {
      const detail =
        isRow(payload) && typeof payload.error === "string" ? payload.error : `Request failed (${response.status})`;
      throw new ApiFailure(response.status, detail);
    }
    return payload as T;
  };
}

export function isRow(value: unknown): value is DataRow {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

export function collection(payload: unknown, keys: readonly string[]): DataRow[] {
  if (Array.isArray(payload)) return payload.filter(isRow);
  if (!isRow(payload)) return [];
  for (const key of keys) if (Array.isArray(payload[key])) return (payload[key] as unknown[]).filter(isRow);
  return [];
}

export function safeDetail(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(safeDetail);
  if (!isRow(value)) return value;
  return Object.fromEntries(
    Object.entries(value).map(([key, item]) => [
      key,
      /^(api_key|password|secret|token|authorization|jwt_key)$/i.test(key) && item ? "••••••••" : safeDetail(item),
    ]),
  );
}

const browserTransport: typeof fetch = async (...args) => {
  if (process.env.NEXT_PUBLIC_MOCK !== "1") return fetch(...args);
  const { createDemoTransport } = await import("./demo-transport");
  return createDemoTransport(async () => (await import("@/lib/mock/handler")).mockHandle)(...args);
};

export const request = createClient(browserTransport, browserSession);
