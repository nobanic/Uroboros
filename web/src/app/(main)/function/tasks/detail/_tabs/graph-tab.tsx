"use client";

import * as React from "react";

import { ExplorationGraph } from "@/components/exploration-graph";
import { api } from "@/lib/api";
import type { Edge, TaskNode } from "@/lib/types";

export function GraphTab({ taskId }: { taskId: string }) {
  const [nodes, setNodes] = React.useState<TaskNode[]>([]);
  const [edges, setEdges] = React.useState<Edge[]>([]);
  const [loadedTask, setLoadedTask] = React.useState(taskId);
  // Signature of the previous graph data: when polling returns identical data, skip setState to
  // avoid needlessly rebuilding the whole graph (so dragging isn't interrupted and stuttered by
  // the 20s poll). Include provenance/metadata used by the evidence inspector.
  const sigRef = React.useRef("");

  React.useEffect(() => {
    let cancelled = false;
    sigRef.current = ""; // switching tasks: force the next refresh
    const load = () => {
      api
        .explorationGraph(taskId)
        .then((g) => {
          if (cancelled) return;
          const ns = g.nodes ?? [];
          const es = g.edges ?? [];
          const sig = JSON.stringify([ns, es]);
          if (sig === sigRef.current) return; // no change → don't rebuild
          sigRef.current = sig;
          setNodes(ns);
          setEdges(es);
          setLoadedTask(taskId);
        })
        .catch(() => {
          /* keep last good data */
        });
    };
    load();
    const timer = setInterval(load, 20000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  }, [taskId]);

  return (
    <div className="uro-task-graph-frame">
      <ExplorationGraph
        key={taskId}
        nodes={loadedTask === taskId ? nodes : []}
        edges={loadedTask === taskId ? edges : []}
        contextKey={`task:${taskId}`}
        className="h-[76vh] min-h-[38rem]"
      />
    </div>
  );
}
