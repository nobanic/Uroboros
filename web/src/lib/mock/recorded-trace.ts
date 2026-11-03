import type { Activity } from "../types";

type Narrative = "round" | "thinking" | "text" | "user" | "result";
type Fields = Omit<
  Partial<Activity>,
  "seq" | "worker" | "intent_id" | "ts" | "kind" | "summary" | "tool" | "tool_use_id"
>;
export type RecordedCall = Readonly<{ tool: string; id: string }>;

/** A seed session supplies its identity once; replies carry their original call reference. */
export class RecordedTrace {
  private readonly rows: Activity[] = [];
  private readonly sequences = new Set<number>();

  constructor(
    private readonly actor: string,
    private readonly day: string,
    private readonly intent?: string,
  ) {
    const stamp = Date.parse(`${day}T00:00:00Z`);
    if (
      !/^\d{4}-\d{2}-\d{2}$/.test(day) ||
      !Number.isFinite(stamp) ||
      new Date(stamp).toISOString().slice(0, 10) !== day
    ) {
      throw new Error("A recorded trace needs an ISO calendar day");
    }
  }

  private append(seq: number, time: string, kind: Activity["kind"], summary: string, fields: Partial<Activity>) {
    if (!Number.isSafeInteger(seq) || this.sequences.has(seq))
      throw new Error(`Duplicate or invalid recorded sequence: ${seq}`);
    if (!/^(?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d$/.test(time))
      throw new Error("A recorded trace needs an exact clock time");
    this.sequences.add(seq);
    this.rows.push({
      seq,
      worker: this.actor,
      ...(this.intent === undefined ? {} : { intent_id: this.intent }),
      ts: `${this.day}T${time}Z`,
      kind,
      summary,
      ...fields,
    });
  }

  message(kind: Narrative, seq: number, time: string, summary: string, fields: Fields = {}) {
    this.append(seq, time, kind, summary, fields);
  }

  call(seq: number, time: string, tool: string, id: string, summary: string, fields: Fields = {}): RecordedCall {
    if (!tool || !id) throw new Error("A recorded call needs a tool and stable call ID");
    this.append(seq, time, "tool_use", summary, { ...fields, tool, tool_use_id: id });
    return Object.freeze({ tool, id });
  }

  reply(seq: number, time: string, call: RecordedCall, summary: string, fields: Fields = {}) {
    this.append(seq, time, "tool_result", summary, { ...fields, tool: call.tool, tool_use_id: call.id });
  }

  entries(): Activity[] {
    return this.rows;
  }
}
