"use client";

import { EvidenceGraph } from "@/astra/graph";
import type { Edge, TaskNode } from "@/lib/types";

// Existing task and finding routes share Astra's evidence workspace. Keep the
// established component contract while the surrounding editors are replaced.
export function ExplorationGraph({
  nodes,
  edges,
  className,
  emptyHint,
  contextKey,
}: {
  nodes: TaskNode[];
  edges: Edge[];
  className?: string;
  emptyHint?: string;
  contextKey?: string;
}) {
  return <EvidenceGraph graph={{ nodes, edges }} className={className} emptyHint={emptyHint} contextKey={contextKey} />;
}
