import type { LLMProfile, Stats, Task } from "@/lib/types";

export function taskSnapshot(task: Task, stats?: Stats | null): Task {
  const live = stats?.active_task;
  if (!live) return task;
  return {
    ...task,
    in_flight: live.in_flight,
    goals_total: live.goals_total,
    goals_met: live.goals_met,
    paused: live.paused,
    engine_mode: stats?.engine_mode ?? live.engine_mode,
  };
}
export function taskProfileChain(task: Task): string[] {
  return task.llm_profile_ids?.length
    ? task.llm_profile_ids.map(String)
    : task.llm_profile_id
      ? [String(task.llm_profile_id)]
      : [];
}
export function taskProfileCursor(task: Task, profiles: LLMProfile[]) {
  const chain = taskProfileChain(task),
    exhausted = task.llm_failover_state === "chain_exhausted";
  const current = task.active_llm_profile_id ? String(task.active_llm_profile_id) : exhausted ? "" : chain[0] || "";
  const profile = profiles.find((p) => String(p.id) === current),
    index = chain.indexOf(current);
  return {
    chain,
    exhausted,
    current,
    backup: index < 0 ? 0 : Math.max(0, chain.length - index - 1),
    label: exhausted
      ? "Profile chain exhausted"
      : profile?.name || (current ? `Profile #${current}` : "Follow default profile"),
    model: profile?.model,
  };
}
export function taskControls(task: Task) {
  const terminal = ["done", "failed", "timeout"].includes(task.status),
    paused = task.paused ?? false;
  const archiveEligible = terminal || paused || task.status === "paused";
  const archiveReason =
    archiveEligible && task.archive_blocked_by_task_id
      ? `This task is directly inherited by un-archived task #${task.archive_blocked_by_task_id}; archive the dependent task first`
      : task.queued
        ? "Queued tasks must be paused first"
        : "Running tasks must be paused first";
  return {
    terminal,
    paused,
    engine: paused ? "paused" : task.engine_mode || "idle",
    archive: archiveEligible && !task.archive_blocked_by_task_id,
    archiveReason,
    label: terminal ? (task.status === "done" ? "Completed" : "Ended") : paused ? "Resume" : "Pause",
    progress: task.goals_total ? Math.round(((task.goals_met || 0) / task.goals_total) * 100) : 0,
    findings: Object.values(task.findings || {}).reduce((sum, n) => sum + n, 0),
  };
}
