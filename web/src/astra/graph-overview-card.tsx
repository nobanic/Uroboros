import type { EvidenceNode } from "./graph-engine";
import { UroIcon } from "@/components/uroboros-icons";
import { graphNodeAppearance } from "./graph-workbench";

// Fitted records keep a source-title excerpt primary, with compact identity and
// recorded state. The preview, inspector, index and reading view retain full text.
export function GraphOverviewCard({ node }: { node: EvidenceNode }) {
  const appearance = graphNodeAppearance(node);
  const inherited = Boolean(node.source.inherited);
  return (
    <div className="astra-graph-overview-card" aria-hidden="true" data-record-id={node.id}>
      <span className="astra-graph-overview-title">{node.title}</span>
      <div className="astra-graph-overview-identity">
        <strong title={appearance.title}>
          <UroIcon name={appearance.icon} />
          <span>{appearance.title}</span>
        </strong>
        <code>#{node.id}</code>
        <span
          className="astra-graph-overview-state"
          title={inherited ? `Source #${node.source.source_task_id}` : node.state || appearance.title}
        >
          <i data-state={node.state} data-inferred={node.payload.confidence === "inferred"} />
          <span>{inherited ? `Source #${node.source.source_task_id}` : node.state || appearance.title}</span>
          {node.payload.confidence === "inferred" && <em>Inferred</em>}
        </span>
      </div>
    </div>
  );
}
