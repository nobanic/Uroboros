import type { InterceptApprovalRow, InterceptAudit, InterceptPending, InterceptReviewInput } from "@/lib/types";

export type ApprovalDecision = "allowed" | "denied";
export type ApprovalBlock = { label: string; text: string; truncated?: boolean; notice?: boolean };
export const approvalAction = { allow: "Allow", ask: "Escalate to human approval", deny: "Deny" };
export const approvalStatus: Record<string, string> = {
  pending: "Pending",
  allowed: "Allowed",
  denied: "Denied",
  timeout: "Timed out",
};

export function approvalSource(row: InterceptApprovalRow) {
  if (row.decision_source) return row.decision_source;
  if (row.rule_id) return "rule";
  return row.reason?.startsWith("[model]") ? "model" : "unknown";
}
export const approvalReason = (text?: string) => text?.replace(/^\[model\]\s*/, "") ?? "";
export const approvalOrigin = (row: InterceptApprovalRow) =>
  row.task_id || (row.conversation_id ? `Conversation #${row.conversation_id}` : "—");
export function approvalTime(value?: string) {
  return value
    ? new Date(value).toLocaleString("en-US", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        second: "2-digit",
      })
    : "—";
}
export function approvalLocation(row: InterceptApprovalRow) {
  const query = new URLSearchParams({ approval: String(row.id) });
  if (row.conversation_id) {
    query.set("c", String(row.conversation_id));
    return `/chat?${query}`;
  }
  if (row.task_id) {
    query.set("id", row.task_id);
    return `/function/tasks/detail?${query}`;
  }
  return null;
}

/** Pending authority is independent of the selected, possibly filtered history page. */
export function approvalQueue(history: InterceptApprovalRow[], pending: InterceptPending[], taskId?: string) {
  const prior = new Map(history.map((row) => [row.id, row]));
  const queue = pending
    .filter((row) => !taskId || row.task_id === taskId)
    .map((row) => ({
      conv_title: "",
      conv_agent_key: "",
      rule_name: row.rule_id ? `Rule #${row.rule_id}` : "",
      ...prior.get(row.id),
      ...row,
    }));
  const queued = new Set(queue.map((row) => row.id));
  return [...queue, ...history.filter((row) => !queued.has(row.id))];
}

export function approvalExecution(row: InterceptApprovalRow, audit?: InterceptAudit | null) {
  let label = audit
    ? {
        not_started: "Not executed yet",
        not_executed: "Not executed",
        awaiting_result: "Allowed, awaiting execution result",
        succeeded: "Executed successfully",
        failed: "Execution failed",
        unknown: "Execution result unknown",
      }[audit.execution_status]
    : "Not recorded";
  if (row.status === "pending") label = "Not executed yet";
  if (audit && audit.correlation !== "exact" && audit.effective_action === "allow")
    label = "No associated execution result";
  return label;
}

/** Versions are immutable snapshots: newer audit context must never be presented as model input. */
export function approvalReview(input: InterceptReviewInput): ApprovalBlock[] {
  const blocks: ApprovalBlock[] = [
    {
      label: "Current call under review",
      text: JSON.stringify({ tool_name: input.tool_name, arguments: input.arguments }, null, 2),
    },
  ];
  const code = (label: string, text: string, truncated?: boolean) => blocks.push({ label, text, truncated });
  const note = (text: string) => blocks.push({ label: "Review snapshot", text, notice: true });
  if (input.version < 4)
    note("This record uses a legacy input format and keeps the content actually sent at the time.");
  if (input.version >= 2) {
    const background = input.background;
    if (background) {
      code(
        background.source === "user_message"
          ? "Background · user message"
          : "Background · Worker intent summary (legacy)",
        background.text,
        background.truncated,
      );
      note(
        background.source === "user_message"
          ? "Taken from the current user message."
          : "This is the Worker intent summary sent by the legacy version; the new Worker review no longer sends it.",
      );
    } else note("No background message was attached to this review.");
  } else {
    if (input.turn_input) code("Current turn input (legacy)", input.turn_input, input.background_truncated);
    if (input.task) {
      code("Task description (legacy)", input.task.description, input.task.truncated);
      code("Task goal (legacy)", input.task.goal, input.task.truncated);
      code("Task operation constraints (legacy)", JSON.stringify(input.task.constraints, null, 2));
    }
    if (input.worker_intent) code("Worker intent (legacy)", input.worker_intent, input.background_truncated);
  }
  if (input.working_directory) code("Working directory", input.working_directory);
  if (input.version >= 3) note("This review did not send historical calls or execution results.");
  else {
    note("Historical calls provided to the model at the time (legacy)");
    for (const entry of input.history ?? []) {
      note(`${entry.tool} · ${entry.status === "succeeded" ? "Succeeded" : "Failed (may have partial side effects)"}`);
      code("Historical call arguments", entry.arguments_preview, entry.truncated);
      code("Historical execution result", entry.result, entry.truncated);
    }
    if (!input.history?.length) note("No pairable historical tool-execution records were provided this time.");
    if (input.history_truncated) note("History is a limited window; some content is truncated.");
  }
  return blocks;
}
