"use client";
import { TaskOverviewRoute } from "@/astra/task-overview";

export function OverviewTab({ taskId }: { taskId: string }) {
  return <TaskOverviewRoute taskId={taskId} />;
}
