import type { Activity, Conversation, Finding, FindingRetest } from "../types";
import type * as SeedData from "./data";
import { activityRecord } from "./demo-activity";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";
import { DemoRows } from "./demo-records";

type ConversationProperties = Pick<Conversation, "llm_profile_id" | "pinned" | "pinned_at">;

/** Seed records and live commands share the same optional-field-preserving constructor. */
export function conversationRecord(
  id: number,
  agentKey: string,
  title: string,
  created: string,
  updated: string,
  properties: ConversationProperties = {},
): Conversation {
  return { id, agent_key: agentKey, title, ...properties, created_at: created, updated_at: updated };
}

const liveRetest = (retest: FindingRetest) => ["pending", "running"].includes(retest.status);

/** Conversations and retests share identity and history without coupling them to tasks. */
export class DemoSessions {
  private readonly conversations: DemoRows<Conversation, number>;
  private readonly retests: FindingRetest[] = [];
  private readonly messages: Record<number, Activity[]> = {};

  constructor(
    private readonly data: typeof SeedData,
    private readonly finding: (id: string) => Finding | undefined,
  ) {
    this.conversations = new DemoRows(structuredClone(data.conversations), (row) => row.id);
  }

  private sort(): void {
    this.conversations.rows.sort((left, right) => {
      const pinned = Number(!!right.pinned_at) - Number(!!left.pinned_at);
      return (
        pinned ||
        (right.pinned_at ?? right.updated_at).localeCompare(left.pinned_at ?? left.updated_at) ||
        right.id - left.id
      );
    });
  }

  private activity(sequence: number, time: string, kind: Activity["kind"], summary: string, detail: string): Activity {
    return activityRecord(sequence, "retester", time, kind, { summary, detail });
  }

  advance(): void {
    for (const retest of this.retests) {
      if (retest.status !== "running" || retest.conversation_id == null) continue;
      if (Date.now() - Date.parse(retest.created_at) < 15000) continue;
      retest.status = "completed";
      retest.verdict = "inconclusive";
      retest.summary = "uroboros.lab demonstration record.";
      retest.evidence = "uroboros.lab demonstration record. Context: Agent.";
      retest.finished_at = new Date().toISOString();
      this.messages[retest.conversation_id].push(
        this.activity(3, retest.finished_at!, "text", retest.summary, retest.evidence),
      );
    }
  }

  private stop(id: number): void {
    for (const retest of this.retests) {
      if (retest.conversation_id !== id || !liveRetest(retest)) continue;
      retest.status = "stopped";
      retest.finished_at = new Date().toISOString();
      retest.error = "uroboros.lab demonstration record.";
    }
  }

  private unlink(id: number): void {
    for (const retest of this.retests) if (retest.conversation_id === id) retest.conversation_id = null;
  }

  private history(id: number): Activity[] {
    return this.messages[id] ?? this.data.conversationMessages[id] ?? [];
  }

  private start(findingID: number, body: Record<string, unknown>): { retest: FindingRetest; created: boolean } {
    const current = this.retests.find((retest) => retest.finding_id === findingID && liveRetest(retest));
    if (current) return { retest: structuredClone(current), created: false };
    const stamp = new Date().toISOString(),
      conversation = this.conversations.nextNumber();
    this.conversations.rows.unshift(
      conversationRecord(
        conversation,
        "retester",
        "uroboros.lab security record Context: finding.id, finding.name, finding.vulnclass.",
        stamp,
        stamp,
        { pinned: false },
      ),
    );
    const retest: FindingRetest = {
      id: this.retests.length + 1,
      finding_id: findingID,
      conversation_id: conversation,
      status: "running",
      verdict: "",
      notes: String(body.notes ?? ""),
      summary: "",
      evidence: "",
      error: "",
      created_at: stamp,
      started_at: stamp,
      finished_at: null,
    };
    this.retests.unshift(retest);
    this.messages[conversation] = [
      this.activity(
        1,
        stamp,
        "user",
        "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: finding.id.",
        retest.notes,
      ),
      this.activity(
        2,
        stamp,
        "text",
        "Verified security evidence captured during the uroboros.lab demonstration assessment.",
        "Reproducible evidence was captured and attached to this demonstration record.",
      ),
    ];
    return { retest: structuredClone(retest), created: true };
  }

  respond({ method, path, segments, body }: DemoRequest): unknown {
    if (path === "/exploration/findings/retests/active" && method === "GET") {
      return {
        retests: this.retests
          .filter((retest) => liveRetest(retest) && retest.conversation_id != null)
          .map((retest) => ({
            id: retest.id,
            finding_id: this.data.findings[retest.finding_id - 1].id,
            conversation_id: retest.conversation_id,
            status: retest.status,
          })),
      };
    }
    if (segments[0] === "exploration" && segments[1] === "findings" && segments[3] === "retests") {
      const finding = this.finding(segments[2]);
      if (!finding) throw new Error("uroboros.lab demonstration record.");
      const index = this.data.findings.findIndex((candidate) => candidate.id === finding.id) + 1;
      if (method === "GET")
        return { retests: structuredClone(this.retests.filter((retest) => retest.finding_id === index)) };
      if (method === "POST") return this.start(index, body);
    }

    if (path === "/conversations" && method === "GET") {
      this.sort();
      return {
        conversations: structuredClone(
          this.conversations.rows.map((row) => ({
            ...row,
            running: this.retests.some((retest) => retest.conversation_id === row.id && liveRetest(retest)),
          })),
        ),
      };
    }
    if (path === "/conversations" && method === "POST") {
      const stamp = new Date().toISOString();
      const title = String(body.title ?? "").trim() || "uroboros.lab security record";
      const created = conversationRecord(
        this.conversations.nextNumber(),
        String(body.agent_key ?? "mainagent"),
        title,
        stamp,
        stamp,
        {
          llm_profile_id: typeof body.llm_profile_id === "number" ? body.llm_profile_id : undefined,
          pinned: false,
        },
      );
      this.conversations.rows.unshift(created);
      return structuredClone(created);
    }
    if (segments[0] !== "conversations") return UNHANDLED;
    const id = Number(segments[1]);
    if (segments.length === 2 && method === "PATCH") {
      const conversation = this.conversations.get(id);
      if (!conversation) return {};
      if (typeof body.title === "string") conversation.title = body.title.trim();
      if (typeof body.pinned === "boolean") {
        conversation.pinned = body.pinned;
        conversation.pinned_at = body.pinned ? (conversation.pinned_at ?? new Date().toISOString()) : null;
      }
      conversation.updated_at = new Date().toISOString();
      this.sort();
      return structuredClone(conversation);
    }
    if (segments.length === 2 && method === "DELETE") {
      this.stop(id);
      this.conversations.remove(id);
      this.unlink(id);
      return { deleted: id };
    }
    if (path === "/conversations/delete/batch" && method === "POST") {
      const ids = Array.isArray(body.ids)
        ? [...new Set(body.ids.map(Number).filter((key) => Number.isInteger(key) && key > 0))]
        : [];
      return {
        items: ids.map((key) => {
          if (!this.conversations.remove(key)) return { id: key, ok: false, error: "conversation not found" };
          this.stop(key);
          this.unlink(key);
          return { id: key, ok: true };
        }),
      };
    }
    if (segments[2] === "messages") {
      if (segments.length === 3 && method === "GET") {
        const items = this.history(id);
        return {
          items,
          cursor: items.length ? items[items.length - 1].seq : 0,
          running: this.retests.some((retest) => retest.conversation_id === id && liveRetest(retest)),
        };
      }
      if (segments.length === 4) {
        const entry = this.history(id).find((row) => row.seq === Number(segments[3]));
        return { detail: entry?.detail ?? entry?.summary ?? "" };
      }
      if (method === "POST") return { status: "ok" };
    }
    if (segments[2] === "stop") {
      this.stop(id);
      return { status: "stopped" };
    }
    return UNHANDLED;
  }
}
