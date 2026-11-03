import type { CSSProperties } from "react";
import { UroIcon } from "@/components/uroboros-icons";
import type { EvidenceNode } from "./graph-engine";
import { graphNodeAppearance } from "./graph-workbench";

// Presentation of the current view record; selection and evidence authority stay with the inspector.
export function GraphRecordPreview({ node, connections }: { node: EvidenceNode; connections: number }) {
  const appearance = graphNodeAppearance(node);
  return (
    <article style={{ "--preview-color": appearance.color } as CSSProperties}>
      <header>
        <span className="astra-graph-preview-kind">
          <UroIcon name={appearance.icon} aria-hidden="true" />
          {appearance.title}
        </span>
        <code>#{node.id}</code>
      </header>
      <p>{node.title}</p>
      <footer>
        <span>{node.state || appearance.title}</span>
        {Boolean(node.source.inherited) && <span>Source #{String(node.source.source_task_id)}</span>}
        {node.payload.confidence === "inferred" && <span>Inferred</span>}
        <span>{connections} connections in current view</span>
      </footer>
    </article>
  );
}
