"use client";

import { useMemo, useState, type CSSProperties } from "react";
import { UroIcon } from "@/components/uroboros-icons";
import { evidenceMatches, type EvidenceView } from "./graph-engine";
import { graphNodeAppearance } from "./graph-workbench";
import "./graph-record-index.css";

// Read the current projection directly. The index owns no graph records, layout,
// evidence status, persistence, or alternative selection/camera policy.
export function GraphRecordIndex({
  id,
  view,
  query,
  onQuery,
  onChoose,
  onClose,
}: {
  id: string;
  view: EvidenceView;
  query: string;
  onQuery: (value: string) => void;
  onChoose: (id: string) => void;
  onClose: () => void;
}) {
  const [page, setPage] = useState({ query, count: 100 });
  const records = useMemo(
    () => [...view.all.values()].filter((node) => evidenceMatches(node, query)),
    [view, query],
  );
  const count = page.query === query ? page.count : 100;
  return (
    <section
      id={id}
      className="astra-graph-record-index"
      aria-label="Graph record index"
      onKeyDown={(event) => {
        if (event.key === "Escape") {
          event.preventDefault();
          event.stopPropagation();
          onClose();
        }
      }}
    >
      <header>
        <span className="astra-graph-record-index-mark"><UroIcon name="records" aria-hidden="true" /></span>
        <div><small>Investigation records</small><h3>Read the graph</h3></div>
        <button type="button" aria-label="Close graph record index" onClick={onClose}>
          <UroIcon name="close" aria-hidden="true" />
        </button>
      </header>
      <label className="astra-graph-record-index-search">
        <UroIcon name="search" aria-hidden="true" />
        <input
          aria-label="Search graph records"
          value={query}
          placeholder="Title, ID, state or evidence…"
          onChange={(event) => onQuery(event.target.value)}
        />
      </label>
      <div className="astra-graph-record-index-count" role="status">
        <strong>{records.length}</strong> of {view.all.size} records
        <span>Original source order</span>
      </div>
      <div className="astra-graph-record-index-scroll">
        {records.length === 0 ? (
          <p className="astra-graph-record-index-empty">No matching records. Try a title, ID or state.</p>
        ) : (
          <ol>
            {records.slice(0, count).map((node) => {
              const appearance = graphNodeAppearance(node);
              const representative = view.representatives.get(node.id);
              return (
                <li key={node.id} style={{ "--record-color": appearance.color } as CSSProperties}>
                  <button type="button" data-record-id={node.id} onClick={() => onChoose(node.id)}>
                    <span className="astra-graph-record-index-kind">
                      <UroIcon name={appearance.icon} aria-hidden="true" />
                      {appearance.title}<code>#{node.id}</code>
                    </span>
                    <p>{node.title}</p>
                    <span className="astra-graph-record-index-meta">
                      {node.state && <span className="astra-graph-state" data-state={node.state}>{node.state}</span>}
                      {Boolean(node.source.inherited) && <span>Source #{String(node.source.source_task_id)}</span>}
                      {representative && representative !== node.id && <span>Grouped in #{representative}</span>}
                    </span>
                  </button>
                </li>
              );
            })}
          </ol>
        )}
        {records.length > count && (
          <button className="astra-graph-record-index-more" type="button" onClick={() => setPage({ query, count: count + 100 })}>
            Show {Math.min(100, records.length - count)} more records
          </button>
        )}
      </div>
      <footer><UroIcon name="info" aria-hidden="true" />Choose a record to open its evidence inspector.</footer>
    </section>
  );
}
