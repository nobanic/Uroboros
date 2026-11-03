"use client";

import { CoverageWorkspace } from "@/astra/coverage-workspace";

// Preserve the task surface contract; reads still use the existing task-scoped
// coverage and evidence-reference endpoints. The backend owns tested status.
export function CoverageGraphTab({ taskId, coverageEnabled = true }: { taskId: string; coverageEnabled?: boolean }) {
  return <CoverageWorkspace taskId={taskId} coverageEnabled={coverageEnabled} />;
}
