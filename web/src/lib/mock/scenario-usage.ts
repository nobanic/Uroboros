import type { Activity, DailyTokenBucket, SessionTokenUsage, TokenTotal } from "../types";

function emptyUsage(): TokenTotal {
  return { input_tokens: 0, output_tokens: 0, cache_read_tokens: 0, cache_write_tokens: 0 };
}

function addUsage(total: TokenTotal, value: Partial<TokenTotal> | undefined): void {
  total.input_tokens += value?.input_tokens ?? 0;
  total.output_tokens += value?.output_tokens ?? 0;
  total.cache_read_tokens += value?.cache_read_tokens ?? 0;
  total.cache_write_tokens += value?.cache_write_tokens ?? 0;
}

/** These are seed-time snapshots; changing live demo rows does not recompute them. */
export function totalUsage(values: Iterable<Partial<TokenTotal> | undefined>): TokenTotal {
  const result = emptyUsage();
  for (const value of values) addUsage(result, value);
  return result;
}

export function sessionUsage(activity: readonly Activity[]): SessionTokenUsage[] {
  const sessions = new Map<string, SessionTokenUsage>();
  for (const row of activity) {
    if (row.kind !== "result") continue;
    const key =
      row.worker === "mainagent"
        ? "main"
        : row.worker === "planner"
          ? "plan"
          : row.intent_id
            ? `intent:${row.intent_id}`
            : "";
    if (!key) continue;
    const total = sessions.get(key) ?? { session: key, ...emptyUsage() };
    addUsage(total, row);
    sessions.set(key, total);
  }
  return [...sessions.values()];
}

export function countRows<T>(rows: readonly T[], key: (row: T) => string | number): Record<string, number> {
  const counts: Record<string, number> = {};
  for (const row of rows) {
    const value = key(row);
    counts[value] = (counts[value] ?? 0) + 1;
  }
  return counts;
}

export function dailyUsage(lastDay: string, count: number): DailyTokenBucket[] {
  return Array.from({ length: count }, (_, index) => {
    const day = new Date(lastDay);
    day.setUTCDate(day.getUTCDate() - (count - 1 - index));
    const wave = 40000 + Math.round(30000 * Math.abs(Math.sin(index / 3)));
    return {
      date: day.toISOString().slice(0, 10),
      input_tokens: wave,
      output_tokens: Math.round(wave * 0.08),
      cache_read_tokens: Math.round(wave * 0.7),
      cache_write_tokens: Math.round(wave * 0.12),
    };
  });
}
