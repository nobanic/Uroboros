"use client";

import { Suspense } from "react";
import { useSearchParams } from "next/navigation";
import { AgentBlueprint } from "@/astra/agent-workspace";

function AgentPage() {
  const key = useSearchParams().get("key") || "";
  return (
    <>
      <a href="/system/agents" className="astra-agent-back">
        Back to agents
      </a>
      {key ? <AgentBlueprint key={key} agentKey={key} /> : <p>Select an agent from the roster.</p>}
    </>
  );
}

export default function AgentDetailPage() {
  return (
    <Suspense fallback={<p role="status">Loading agent…</p>}>
      <AgentPage />
    </Suspense>
  );
}
