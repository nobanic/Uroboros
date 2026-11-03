import type { Activity, Agent, Conversation } from "@/lib/types";

export const conversationPageSize = 100;
export const collapsedConversationGroupsKey = "uroboros.chat.collapsed-agents";
export const conversationPinned = (row: Conversation) => row.pinned ?? Boolean(row.pinned_at);
export const conversationalAgents = (agents: Agent[]) =>
  agents.filter((agent) => !agent.builtin || agent.role === "assistant");
export function conversationGroups(rows: Conversation[], agents: Agent[]) {
  const names = new Map(agents.map((agent) => [agent.key, agent.name]));
  const groups = new Map<string, { key: string; name: string; items: Conversation[]; running: number }>();
  for (const row of rows) {
    if (conversationPinned(row)) continue;
    const group = groups.get(row.agent_key) || {
      key: row.agent_key,
      name: names.get(row.agent_key) || row.agent_key,
      items: [],
      running: 0,
    };
    group.items.push(row);
    group.running += row.running ? 1 : 0;
    groups.set(row.agent_key, group);
  }
  return [...groups.values()];
}
export function conversationFilters(rows: Conversation[], agents: Agent[], current: string) {
  const keys = new Set([
    ...conversationalAgents(agents).map((agent) => agent.key),
    ...rows.map((row) => row.agent_key),
    ...(current ? [current] : []),
  ]);
  return [...keys]
    .map((key) => ({
      key,
      name: agents.find((agent) => agent.key === key)?.name || key,
      count: rows.filter((row) => row.agent_key === key).length,
    }))
    .sort((a, b) => a.name.localeCompare(b.name, "en-US"));
}
export function matchingConversations(rows: Conversation[], agent: string, query: string) {
  const search = query.trim().toLocaleLowerCase();
  return rows.filter(
    (row) =>
      (!agent || row.agent_key === agent) &&
      (!search || `${row.id} ${row.title} ${row.agent_key}`.toLocaleLowerCase().includes(search)),
  );
}
export function conversationAddress(value: string | null) {
  const id = Number(value);
  return value && Number.isSafeInteger(id) && id > 0 ? id : null;
}
export function conversationMeter(items: Activity[]) {
  let input = 0,
    output = 0,
    cache = 0,
    turns = 0,
    pending = [0, 0, 0],
    first = Infinity,
    last = 0;
  for (const item of items) {
    const stamp = Date.parse(item.ts);
    if (Number.isFinite(stamp)) {
      first = Math.min(first, stamp);
      last = Math.max(last, stamp);
    }
    if (item.kind === "result") {
      input += item.input_tokens || 0;
      output += item.output_tokens || 0;
      cache += item.cache_read_tokens || 0;
      pending = [0, 0, 0];
    }
    if (item.kind === "usage") {
      turns++;
      pending = [item.input_tokens || 0, item.output_tokens || 0, item.cache_read_tokens || 0];
    }
  }
  return {
    input: input + pending[0],
    output: output + pending[1],
    cache: cache + pending[2],
    turns,
    duration: Number.isFinite(first) ? Math.max(0, last - first) : 0,
  };
}
export function restoreConversationGroups(raw: string | null): Set<string> {
  try {
    const value = JSON.parse(raw || "null");
    return new Set(Array.isArray(value) ? value.filter((key) => typeof key === "string") : []);
  } catch {
    return new Set();
  }
}
