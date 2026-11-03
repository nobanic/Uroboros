const wsClean = (p: string) => p.replace(/^\/+|\/+$/g, "");

type ScenarioEntry = { name: string; dir: boolean; size: number; content?: string };

/** A borrowed directory catalog serves fresh listing views and exact recorded file content. */
export class ScenarioWorkspace {
  constructor(private readonly tree: Record<string, ScenarioEntry[]>) {}
  list(path: string) {
    const WS_TREE = this.tree;

    const key = wsClean(path);
    const items = WS_TREE[key] ?? [];
    const now = Date.parse("2026-08-09T03:00:00Z");
    return {
      path: key,
      entries: items.map((it, i) => ({
        name: it.name,
        path: key ? `${key}/${it.name}` : it.name,
        dir: it.dir,
        size: it.size,
        mtime: now - i * 3_600_000,
      })),
    };
  }
  read(path: string) {
    const WS_TREE = this.tree;

    const key = wsClean(path);
    const slash = key.lastIndexOf("/");
    const parent = slash >= 0 ? key.slice(0, slash) : "";
    const name = slash >= 0 ? key.slice(slash + 1) : key;
    const f = (WS_TREE[parent] ?? []).find((x) => x.name === name && !x.dir);
    return { path: key, size: f?.size ?? 0, binary: false, content: f?.content ?? "" };
  }
}
