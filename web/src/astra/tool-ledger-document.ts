import type { CommandRecord, ToolStat } from "@/lib/types";

export const toolPageSizes = [25, 50, 100] as const;
export type ToolLedgerQuery = { task: string; q: string; page: number; size: number };
export type ToolLedgerRow = { key: string; record: CommandRecord };
export type ToolLedgerFilter = Pick<ToolLedgerQuery, "task" | "q">;

export function recordedInput(raw: string): string {
  try {
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed.command === "string" ? parsed.command : JSON.stringify(parsed, null, 2);
  } catch {
    return raw;
  }
}

export function recordedPreview(raw: string, width = 80) {
  const first = recordedInput(raw).split("\n")[0];
  return first.length > width ? `${first.slice(0, width)}\u2026` : first;
}

export function recordedTime(value: string) {
  return new Date(value).toLocaleString("en-US", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
}

// Result pairing can return more than one row for one tool-use id. View keys
// distinguish occurrences without replacing the public id or dropping results.
export function toolLedgerRows(records: CommandRecord[]): ToolLedgerRow[] {
  const occurrences = new Map<number, number>();
  return records.map((record) => {
    const ordinal = occurrences.get(record.id) ?? 0;
    occurrences.set(record.id, ordinal + 1);
    return { key: `${record.id}:${ordinal}`, record };
  });
}

export function toolLedgerQueryKey(query: ToolLedgerQuery) {
  return JSON.stringify([query.task, query.q, query.page, query.size]);
}
export function toolLedgerFilterKey(query: ToolLedgerFilter) {
  return JSON.stringify([query.task, query.q]);
}
export function toolLedgerFilter(query: ToolLedgerFilter) {
  return { task: query.task || undefined, q: query.q || undefined };
}

export function toolLedgerPulse(rows: ToolLedgerRow[]) {
  const failures = rows.filter((row) => row.record.is_error).length;
  return {
    failures,
    withoutError: rows.length - failures,
    tools: new Set(rows.map((row) => row.record.tool).filter(Boolean)).size,
    workers: new Set(rows.map((row) => row.record.worker).filter(Boolean)).size,
  };
}
export function toolLedgerStatistics(rows: ToolStat[]) {
  return rows.reduce(
    (sum, row) => ({
      total: sum.total + row.total,
      errors: sum.errors + row.errors,
      max: Math.max(sum.max, row.total),
    }),
    { total: 0, errors: 0, max: 0 },
  );
}
export function toolLedgerWindow(query: ToolLedgerQuery, total: number, rows: number) {
  return {
    pages: Math.max(1, Math.ceil(total / query.size)),
    first: total === 0 ? 0 : query.page * query.size + 1,
    last: query.page * query.size + rows,
  };
}
