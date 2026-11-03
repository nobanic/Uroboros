import type { ModelTokenStat, TaskNode, TaskScopeRow, Finding } from "@/lib/types";

export const overviewScopeKinds: [TaskScopeRow["kind"], string][] = [
  ["root_domain", "Root domain"],
  ["subdomain", "Subdomain"],
  ["ip", "IP"],
  ["cidr", "Network range"],
  ["icp", "ICP"],
  ["keyword", "Keyword"],
  ["company", "Company"],
];
export const overviewRuleKinds = [
  ["exact_domain", "Domain (exact)"],
  ["exact_ip", "IP (exact)"],
  ["exact_url", "URL (exact)"],
  ["fuzzy_domain", "Domain (fuzzy)"],
  ["fuzzy_ip", "IP (fuzzy)"],
  ["fuzzy_url", "URL (fuzzy)"],
  ["cidr", "CIDR range"],
] as const;

export function overviewScopeText(row: TaskScopeRow) {
  return (
    row.value ||
    row.domain ||
    row.net ||
    (row.company_id ? (row.company_name?.trim() ? row.company_name : `Company #${row.company_id}`) : "—")
  );
}

export function overviewWork(id: string, intents: TaskNode[], findings: Finding[]) {
  const running: TaskNode[] = [],
    open: TaskNode[] = [],
    blocked: TaskNode[] = [];
  for (const intent of intents) {
    if (intent.state === "running") running.push(intent);
    if (intent.state === "open") open.push(intent);
    if (intent.state === "blocked") blocked.push(intent);
  }
  return { running, open, blocked, findings: findings.filter((row) => row.task_id === id), total: intents.length };
}

export function overviewUsage(models: ModelTokenStat[]) {
  const totals = { input: 0, output: 0, cacheRead: 0, cacheWrite: 0, calls: 0 };
  for (const model of models) {
    totals.input += model.input_tokens;
    totals.output += model.output_tokens;
    totals.cacheRead += model.cache_read_tokens;
    totals.cacheWrite += model.cache_write_tokens;
    totals.calls += model.calls;
  }
  return totals;
}

export function overviewCount(value: number) {
  const scale = value >= 1_000_000 ? 1_000_000 : value >= 1000 ? 1000 : 1;
  return scale === 1
    ? String(value)
    : (value / scale).toFixed(value >= scale * 10 ? 0 : 1) + (scale === 1000 ? "k" : "M");
}
export const overviewCacheRate = (read: number, input: number) =>
  input <= 0 ? "—" : `${Math.round((read / input) * 100)}%`;
