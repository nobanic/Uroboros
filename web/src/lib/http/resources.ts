import type { ApplicationWire } from "./application-wire";

export type QueryEntry = readonly [name: string, value: string | number | boolean | null | undefined];
export type QueryEncoding = "form" | "component";

/** Ordered query fields: nullish entries are absent; zero, false and empty strings remain explicit. */
export function encodeQuery(entries: readonly QueryEntry[], encoding: QueryEncoding = "form"): string {
  const fields = entries.filter((entry): entry is readonly [string, string | number | boolean] => entry[1] != null);
  if (encoding === "form") return new URLSearchParams(fields.map(([key, value]) => [key, String(value)])).toString();
  return fields.map(([key, value]) => `${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`).join("&");
}

export const slice = <T>(rows: T[] | null | undefined): T[] => rows ?? [];

/** Resource instances bind a route and its query before selecting a response or issuing a command.
 * Segment encoding stays explicit: existing endpoints intentionally use different ID dialects.
 */
export class Resource {
  constructor(
    private readonly wire: ApplicationWire,
    readonly path: string,
  ) {}

  at(...segments: (string | number)[]): Resource {
    return new Resource(this.wire, [this.path, ...segments].join("/"));
  }

  query(entries: readonly QueryEntry[], encoding: QueryEncoding = "form"): Resource {
    return new Resource(this.wire, `${this.path}?${encodeQuery(entries, encoding)}`);
  }

  read<T>(init?: RequestInit): Promise<T> {
    return this.wire.json<T>(this.path, init);
  }

  /** Go nil slices are normalized only at the declared slice, never across arbitrary JSON fields. */
  async list<T>(field?: string): Promise<T[]> {
    const value = await this.read<T[] | Record<string, T[] | null | undefined> | null>();
    return slice(field === undefined ? (value as T[] | null) : (value as Record<string, T[]>)[field]);
  }

  /** Retain envelope metadata while normalizing only its declared slice fields. */
  async slices<T extends object>(fields: readonly (keyof T)[]): Promise<T> {
    const value = (await this.read<T>()) as T;
    const result = { ...value };
    for (const field of fields) result[field] = slice(value[field] as unknown[] | null | undefined) as T[keyof T];
    return result;
  }

  send<T>(method: "POST" | "PUT" | "PATCH" | "DELETE", body?: unknown): Promise<T> {
    return this.wire.command(method)<T>(this.path, body);
  }

  raw(init?: RequestInit): Promise<Response> {
    return this.wire.raw(this.path, init);
  }
}

/** Named input mappings preserve omission versus explicit null/default values and insertion order. */
export function inputFields<Input extends object>(
  input: Input,
  fields: readonly (readonly [target: string, source: keyof Input, fallback?: unknown])[],
): Record<string, unknown> {
  return Object.fromEntries(
    fields.map(([target, source, ...fallback]) => [
      target,
      fallback.length ? (input[source] ?? fallback[0]) : input[source],
    ]),
  );
}

export function multipartFiles(files: readonly File[]): FormData {
  const form = new FormData();
  for (const file of files) form.append("file", file);
  return form;
}

/** Downloads keep distinct lifetime policies: attached/immediate files and detached/deferred evidence. */
export function saveDownload(
  blob: Blob,
  filename: string | (() => string),
  lifetime: "immediate" | "evidence" = "immediate",
): void {
  const url = URL.createObjectURL(blob);
  const link = document.createElement("a");
  link.href = url;
  link.download = typeof filename === "function" ? filename() : filename;
  if (lifetime === "immediate") document.body.appendChild(link);
  link.click();
  if (lifetime === "immediate") {
    link.remove();
    URL.revokeObjectURL(url);
  } else setTimeout(() => URL.revokeObjectURL(url), 1000);
}
