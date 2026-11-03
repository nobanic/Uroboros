"use client";

import * as React from "react";

import { ExplorationGraph } from "@/components/exploration-graph";
import { api } from "@/lib/api";
import type { Edge, TaskNode } from "@/lib/types";

// FindingLineageView renders the exploration sub-graph from the task's initial
// node down to this finding's node — the same attack-chain graph canvas as the task graph,
// scoped to just this finding's lineage.
export function FindingLineageView({ findingId }: { findingId: string }) {
  const [nodes, setNodes] = React.useState<TaskNode[]>([]);
  const [edges, setEdges] = React.useState<Edge[]>([]);
  const [loaded, setLoaded] = React.useState(false);
  const [loadedFinding, setLoadedFinding] = React.useState(findingId);

  React.useEffect(() => {
    let alive = true;
    api
      .findingLineage(findingId)
      .then((g) => {
        if (!alive) return;
        setNodes(g.nodes ?? []);
        setEdges(g.edges ?? []);
        setLoadedFinding(findingId);
      })
      .catch(() => {})
      .finally(() => {
        if (alive) setLoaded(true);
      });
    return () => {
      alive = false;
    };
  }, [findingId]);

  if (loaded && nodes.length === 0) {
    return (
      <p className="text-muted-foreground p-6 text-sm">
        No chain to display (this vulnerability isn't linked to an exploration node, or its task has been deleted).
      </p>
    );
  }

  return (
    <ExplorationGraph
      key={findingId}
      contextKey={`finding:${findingId}`}
      nodes={loadedFinding === findingId ? nodes : []}
      edges={loadedFinding === findingId ? edges : []}
      className="h-[68vh]"
      emptyHint={loaded ? "No chain" : "Loading…"}
    />
  );
}
