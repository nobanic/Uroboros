import type { LLMRecordDetail, LLMRecordItem } from "@/lib/types";

export type InferenceFilter = { model: string; session: string; task: string; page: number; size: number };
export const inferencePageSizes = [25, 50, 100] as const;
export const inferenceFilter = (): InferenceFilter => ({ model: "", session: "", task: "", page: 0, size: 50 });
export const inferenceQuery = (filter: InferenceFilter) => ({
  model: filter.model || undefined,
  session: filter.session || undefined,
  task: filter.task || undefined,
  page: filter.page,
  size: filter.size,
});
export const inferenceTime = (stamp: string) =>
  new Date(stamp).toLocaleString("en-US", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
export const inferenceLatency = (milliseconds: number) =>
  milliseconds < 1_000 ? `${milliseconds}ms` : `${(milliseconds / 1_000).toFixed(1)}s`;
export const inferenceTokens = (count: number) => (count >= 1_000 ? `${(count / 1_000).toFixed(1)}k` : String(count));
export function inferenceJSON(text: string): string {
  try {
    return JSON.stringify(JSON.parse(text), null, 2);
  } catch {
    return text;
  }
}

/** Page metrics describe only this returned slice; stored total remains server-authoritative. */
export function inferencePage(records: LLMRecordItem[], total: number, page: number, size: number) {
  const totals = records.reduce(
    (value, row) => ({
      failed: value.failed + Number(row.status !== "ok"),
      latency: value.latency + row.latency_ms,
      tokens: value.tokens + row.input_tokens + row.output_tokens,
    }),
    { failed: 0, latency: 0, tokens: 0 },
  );
  return {
    totalPages: Math.max(1, Math.ceil(total / size)),
    rangeStart: total === 0 ? 0 : page * size + 1,
    rangeEnd: page * size + records.length,
    failedCalls: totals.failed,
    averageLatency: records.length ? Math.round(totals.latency / records.length) : 0,
    pageTokens: totals.tokens,
  };
}

/** Raw response is opaque. An absent raw half never borrows normalized evidence. */
export function inferencePayload(detail: LLMRecordDetail | null, rawView: boolean) {
  const hasRaw = Boolean(detail?.raw_request || detail?.raw_response),
    showRaw = rawView && hasRaw;
  const requestSource = showRaw ? detail?.raw_request : detail?.request_body;
  const responseSource = showRaw ? detail?.raw_response : detail?.response_body;
  const requestCopy = requestSource ? inferenceJSON(requestSource) : "";
  const responseCopy = responseSource ? (showRaw ? responseSource : inferenceJSON(responseSource)) : "";
  return {
    hasRaw,
    showRaw,
    requestCopy,
    responseCopy,
    requestDisplay: requestCopy || "(empty)",
    responseDisplay: responseCopy || "(empty)",
  };
}

export function inferenceTaskOptions(tasks: { task_id: string; count: number }[], selected: string) {
  const options = tasks.map((task) => ({ value: task.task_id, label: `#${task.task_id} (${task.count})` }));
  if (selected && !options.some((task) => task.value === selected))
    options.push({ value: selected, label: `#${selected} (not in current task list)` });
  return options;
}
