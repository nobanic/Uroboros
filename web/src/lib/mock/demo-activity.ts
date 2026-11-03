import type { Activity } from "../types";

type ActivityContent = Omit<Activity, "seq" | "worker" | "ts" | "kind" | "intent_id">;

/** Recorded and newly generated turns keep optional content fields absent until supplied. */
export function activityRecord(
  sequence: number,
  worker: string,
  time: string,
  kind: Activity["kind"],
  content: ActivityContent,
  context: Pick<Activity, "intent_id"> = {},
): Activity {
  return { seq: sequence, ...context, worker, ts: time, kind, ...content };
}
