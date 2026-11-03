"use client";
import { BroadcastWorkspace } from "@/astra/broadcast-workspace";

export function BroadcastTab({ taskId }: { taskId: string }) {
  return <BroadcastWorkspace key={taskId} task={taskId} />;
}
