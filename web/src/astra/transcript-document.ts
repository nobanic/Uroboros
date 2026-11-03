import type { Activity } from "@/lib/types";

export type ToolEntry = {
  kind: "tool";
  seq: number;
  worker: string;
  call?: Activity;
  output?: Activity;
};
export type MessageEntry = { kind: "message"; seq: number; worker: string; fragments: Activity[] };
export type TranscriptEntry =
  | ToolEntry
  | MessageEntry
  | { kind: "turn" | "answer" | "approval"; seq: number; activity: Activity }
  | { kind: "round"; seq: number; label: string };

/** Tool IDs bind parallel completions; display order remains the recorded call order. */
export function projectTranscript(activity: readonly Activity[], conversational: boolean): TranscriptEntry[] {
  const entries: TranscriptEntry[] = [],
    pending = new Map<string, ToolEntry>();
  for (const record of activity) {
    const seq = record.seq;
    switch (record.kind) {
      case "usage":
        break;
      case "round":
        entries.push({ kind: "round", seq, label: record.summary || "New round" });
        break;
      case "intercept_request":
        entries.push({ kind: "approval", seq, activity: record });
        break;
      case "user":
      case "intent":
        entries.push({ kind: "turn", seq, activity: record });
        break;
      case "result":
        entries.push({ kind: "answer", seq, activity: record });
        break;
      case "tool_use": {
        const entry: ToolEntry = { kind: "tool", seq, worker: record.worker, call: record };
        entries.push(entry);
        if (record.tool_use_id) pending.set(record.tool_use_id, entry);
        break;
      }
      case "tool_result": {
        const call = record.tool_use_id ? pending.get(record.tool_use_id) : undefined;
        if (call && !call.output) call.output = record;
        else entries.push({ kind: "tool", seq, worker: record.worker, output: record });
        break;
      }
      default: {
        if (conversational && record.kind === "text") {
          entries.push({ kind: "answer", seq, activity: record });
          break;
        }
        const previous = entries.at(-1);
        if (previous?.kind === "message" && previous.worker === record.worker) previous.fragments.push(record);
        else entries.push({ kind: "message", seq, worker: record.worker, fragments: [record] });
      }
    }
  }
  return entries;
}

export function laneTone(worker: string): string {
  if (worker === "planner") return "amber";
  if (worker === "mainagent") return "blue";
  const ordinal = Number(/#(\d+)/.exec(worker)?.[1] ?? 1);
  return ["sky", "violet", "teal", "pink", "orange"][Math.max(0, (ordinal - 1) % 5)];
}

export function commandText(tool: string, raw: string): string {
  if (tool !== "Bash") return raw;
  try {
    const input = JSON.parse(raw);
    if (typeof input?.command === "string") return input.command;
  } catch {
    /* Persisted summary may end inside a JSON string. */
  }
  const body = /"command"\s*:\s*"((?:\\.|[^"\\])*)/.exec(raw)?.[1];
  if (body === undefined) return raw;
  try {
    return JSON.parse(`"${body}"`);
  } catch {
    const escapes: Record<string, string> = { n: "\n", r: "\r", t: "\t" };
    return body.replace(/\\(["\\/nrt])/g, (_, char: string) => escapes[char] ?? char);
  }
}

export function toolPresentation(entry: ToolEntry) {
  const name = entry.call?.tool || entry.output?.tool || "Tool";
  const summary = entry.call?.summary ?? "";
  return {
    name,
    preview: commandText(name, summary.startsWith(name) ? summary.slice(name.length).trimStart() : summary),
    state: !entry.output ? "running" : entry.output.is_error ? "failed" : "complete",
  } as const;
}

export type DetailPart = { seq: number; label: string; fallback: string; empty?: string; command?: string };
export type DetailPort = (seq: number) => Promise<string>;
export function toolParts(entry: ToolEntry): DetailPart[] {
  const name = toolPresentation(entry).name;
  const parts: DetailPart[] = [];
  if (entry.call)
    parts.push({ seq: entry.call.seq, label: "Command", fallback: "(load failed)", empty: "(empty)", command: name });
  if (entry.output)
    parts.push({
      seq: entry.output.seq,
      label: `Output ${entry.output.is_error ? "✕" : "✓"}`,
      fallback: "(load failed)",
      empty: "(empty)",
    });
  return parts;
}
export function messageParts(entry: MessageEntry): DetailPart[] {
  return entry.fragments.map((fragment) => ({
    seq: fragment.seq,
    label: fragment.kind === "thinking" ? "Reasoning" : fragment.kind === "result" ? "Summary" : "Note",
    fallback: fragment.summary,
  }));
}
export function messagePresentation(entry: MessageEntry) {
  const spoken = entry.fragments.filter((fragment) => fragment.kind !== "thinking");
  return {
    preview: (spoken.length ? spoken : entry.fragments).map((fragment) => fragment.summary).join("  ") || "…",
    reasoning: entry.fragments.some((fragment) => fragment.kind === "thinking"),
    error: entry.fragments.some((fragment) => fragment.is_error),
  };
}
export async function readTranscriptParts(parts: readonly DetailPart[], read: DetailPort) {
  const values = await Promise.all(
    parts.map(async (part) => {
      try {
        const raw = await read(part.seq);
        const body = raw || part.empty || part.fallback;
        return { text: part.command ? commandText(part.command, body) : body, failed: false };
      } catch {
        return { text: part.fallback, failed: true };
      }
    }),
  );
  return {
    text: values
      .map((value, index) => `${parts[index].label ? `[${parts[index].label}]\n` : ""}${value.text}`)
      .join("\n\n"),
    failed: values.some((value) => value.failed),
  };
}

export type TurnAttachment = { name: string; path: string; size: number };
export function inlineTurnBody(activity: Activity): string | null {
  if (activity.detail) return activity.detail;
  // Session headers synthesize a leading objective; negative IDs have no persisted detail endpoint.
  if (activity.kind === "intent" && activity.seq < 0) return activity.summary;
  return null;
}
export function turnBody(raw: string): { text: string; attachments: TurnAttachment[] } {
  if (raw.startsWith("{")) {
    try {
      const body = JSON.parse(raw);
      if (body && Array.isArray(body.attachments))
        return { text: typeof body.text === "string" ? body.text : "", attachments: body.attachments };
    } catch {
      /* Plain user text can start with an opening brace. */
    }
  }
  return { text: raw, attachments: [] };
}
export function attachmentSize(bytes: number) {
  const magnitude = bytes >= 1048576 ? 1048576 : bytes >= 1024 ? 1024 : 1;
  return `${magnitude === 1 ? bytes : (bytes / magnitude).toFixed(1)} ${magnitude === 1 ? "B" : magnitude === 1024 ? "KB" : "MB"}`;
}
export function approvalReference(summary: string) {
  const id = /\(#(\d+)\)/.exec(summary)?.[1];
  return { id: id ? Number(id) : null, tool: /Tool\s+(\S+)\s+requests/.exec(summary)?.[1] || summary };
}
