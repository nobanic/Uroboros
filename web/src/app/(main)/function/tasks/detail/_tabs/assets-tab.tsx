"use client";
import { TaskAssetsRoute } from "@/astra/task-assets";

export function AssetsTab({ taskId }: { taskId: string }) {
  return <TaskAssetsRoute taskId={taskId} />;
}
