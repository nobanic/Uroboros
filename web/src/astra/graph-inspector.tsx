"use client";

import { useEffect, useState, type CSSProperties } from "react";
import { UroIcon } from "@/components/uroboros-icons";
import { digestMembers, relationStyle, type EvidenceNode, type EvidenceView } from "./graph-engine";
import { graphNodeAppearance, graphReference } from "./graph-workbench";

export function GraphInspector({
  node,
  view,
  choose,
  focus,
  close,
}: {
  node: EvidenceNode;
  view: EvidenceView;
  choose: (id: string) => void;
  focus: () => void;
  close: () => void;
}) {
  const [tab, setTab] = useState("overview"),
    [copyState, setCopyState] = useState(""),
    [limit, setLimit] = useState(60);
  const meta = graphNodeAppearance(node),
    raw = JSON.stringify(node.source, null, 2);
  const links = view.links.filter((link) => link.source === node.id || link.target === node.id),
    members = digestMembers(node, view);
  const reference = graphReference(node);
  useEffect(() => {
    setTab("overview");
    setCopyState("");
    setLimit(60);
  }, [node.id]);
  async function copy() {
    try {
      await navigator.clipboard.writeText(raw);
      setCopyState("Copied");
    } catch {
      setCopyState("Clipboard unavailable; select the JSON below");
      setTab("raw");
    }
  }
  return (
    <aside
      className="astra-evidence-inspector"
      aria-label={`${node.kind} inspector`}
      style={{ "--evidence-color": meta.color } as CSSProperties}
    >
      <header>
        <span className="astra-evidence-inspector-icon">
          <UroIcon name={meta.icon} aria-hidden="true" />
        </span>
        <div>
          <small>Evidence inspector</small>
          <h3>
            {meta.title} <code>#{node.id}</code>
          </h3>
        </div>
        <button onClick={close} aria-label="Close inspector">
          <UroIcon name="close" aria-hidden="true" />
        </button>
      </header>
      <div className="astra-evidence-inspector-summary">
        <h4>{node.title}</h4>
        <span className="astra-graph-state" data-state={node.state}>
          {node.state || meta.title}
        </span>
        {Boolean(node.source.inherited) && (
          <span className="astra-graph-source">Inherited · source #{String(node.source.source_task_id ?? "—")}</span>
        )}
      </div>
      <nav aria-label="Inspector sections">
        {["overview", "lineage", "raw"].map((value) => (
          <button key={value} aria-pressed={tab === value} onClick={() => setTab(value)}>
            {value}
          </button>
        ))}
      </nav>
      <div className="astra-evidence-inspector-body">
        {tab === "overview" && (
          <>
            <div className="astra-evidence-inspector-actions">
              <button onClick={focus}>
                <UroIcon name="topology" aria-hidden="true" />
                Focus connections
              </button>
              {reference && (
                <a href={reference}>
                  Open {node.kind === "finding" ? "finding" : "source task"}
                  <UroIcon name="arrow-up-right" aria-hidden="true" />
                </a>
              )}
            </div>
            <dl>
              {[
                "state",
                "origin",
                "owner",
                "priority",
                "source_task_id",
                "inherited",
                "blocked_reason",
                "delete_reason",
                "ts",
                "created_at",
                "finding_id",
                "traffic_count",
                "anchors",
              ].map((key) => {
                const value = key === "state" ? node.state : node.source[key];
                return (
                  value !== undefined &&
                  value !== "" && (
                    <div key={key}>
                      <dt>{key.replaceAll("_", " ")}</dt>
                      <dd>{typeof value === "object" ? JSON.stringify(value) : String(value)}</dd>
                    </div>
                  )
                );
              })}
            </dl>
            {Object.entries(node.payload).map(([key, value]) => (
              <section key={key}>
                <h5>{key.replaceAll("_", " ")}</h5>
                <pre>{typeof value === "string" ? value : JSON.stringify(value, null, 2)}</pre>
              </section>
            ))}
          </>
        )}
        {tab === "lineage" && (
          <>
            <h4>
              Connected evidence <span>{links.length}</span>
            </h4>
            {links.slice(0, limit).map((link, index) => {
              const id = link.source === node.id ? link.target : link.source,
                neighbor = view.all.get(id);
              return (
                <button
                  className="astra-evidence-neighbor"
                  key={`${link.source}:${link.target}:${index}`}
                  onClick={() => choose(id)}
                >
                  <small>
                    <UroIcon name="route" aria-hidden="true" />
                    {link.source === node.id ? "Outgoing" : "Incoming"} ·{" "}
                    {relationStyle[link.relation]?.title ?? link.relation}
                  </small>
                  <span>
                    #{id} {neighbor?.title}
                  </span>
                </button>
              );
            })}
            {links.length > limit && (
              <button onClick={() => setLimit((value) => value + 60)}>
                Show more connections ({links.length - limit})
              </button>
            )}
            {!links.length && <p>No recorded neighboring links.</p>}
            {members.length > 0 && (
              <>
                <h4>Digest members</h4>
                {members.map((member) => (
                  <button className="astra-evidence-neighbor" key={member.id} onClick={() => choose(member.id)}>
                    #{member.id} {member.title}
                  </button>
                ))}
              </>
            )}
          </>
        )}
        {tab === "raw" && (
          <pre className="astra-evidence-raw" tabIndex={0}>
            {raw}
          </pre>
        )}
        <button className="astra-evidence-copy" onClick={() => void copy()}>
          <UroIcon name="records" aria-hidden="true" />
          Copy JSON
        </button>
        <p role="status">{copyState}</p>
      </div>
      <footer>
        <UroIcon name="shield" aria-hidden="true" />
        Recorded graph context
      </footer>
    </aside>
  );
}
