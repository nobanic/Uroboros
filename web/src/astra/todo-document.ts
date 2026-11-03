export type TodoEntry = { content: string; status?: unknown };

/** Retain the original raw/prefixed TodoWrite JSON boundary and row order. */
export function todoEntries(detail: string): TodoEntry[] {
  const opening = detail.indexOf("{");
  const payload = JSON.parse(opening < 0 ? detail : detail.slice(opening));
  if (!Array.isArray(payload?.todos)) return [];
  for (const entry of payload.todos) {
    if (!entry || typeof entry !== "object" || typeof entry.content !== "string") {
      throw new Error("Invalid Todo row");
    }
  }
  return payload.todos;
}

export function todoSummary(entries: TodoEntry[]) {
  const counts = { pending: 0, running: 0, completed: 0, other: 0 };
  for (const entry of entries) {
    if (entry.status === "pending") counts.pending++;
    else if (entry.status === "in_progress") counts.running++;
    else if (entry.status === "completed") counts.completed++;
    else counts.other++;
  }
  return counts;
}

export const todoMarkers: Record<string, string> = { pending: "☐", in_progress: "▶", completed: "✔" };

export function todoStatus(status: unknown) {
  if (status === "pending") return "Pending";
  if (status === "in_progress") return "In progress";
  if (status === "completed") return "Completed";
  return typeof status === "string" && status ? status : "Unspecified";
}

export function todoMarker(status: unknown) {
  return typeof status === "string" ? (todoMarkers[status] ?? "☐") : "☐";
}
