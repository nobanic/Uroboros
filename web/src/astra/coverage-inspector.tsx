"use client";

import { useEffect, useState } from "react";
import { UroIcon } from "@/components/uroboros-icons";
import type { CoverageAssetRefs, CoverageGraphNode } from "@/lib/types";
import { coverageBatch, coverageKinds, coverageLabel, coverageStatus, type CoverageFold } from "./coverage-document";
import { useConsoleResource } from "./use-console-resource";

export function CoverageInspector({
  node,
  taskId,
  tracking,
  close,
}: {
  node: CoverageGraphNode;
  taskId: string;
  tracking: boolean;
  close: () => void;
}) {
  const [tab, setTab] = useState("overview"),
    [copyState, copied] = useState("");
  const meta = coverageKinds[node.kind] || coverageKinds.endpoint,
    status = coverageStatus(node, tracking);
  const references = useConsoleResource<CoverageAssetRefs>(
    node.asset_id && node.asset_id > 0
      ? `/tasks/${encodeURIComponent(taskId)}/asset-refs?asset_id=${encodeURIComponent(node.asset_id)}`
      : null,
  );
  useEffect(() => {
    setTab("overview");
    copied("");
  }, [node.key]);
  async function copy() {
    try {
      await navigator.clipboard.writeText(JSON.stringify(node, null, 2));
      copied("Copied");
    } catch {
      setTab("raw");
      copied("Clipboard unavailable; select the JSON below.");
    }
  }
  return (
    <aside className="astra-evidence-inspector astra-coverage-inspector" aria-label={`${meta.title} inspector`}>
      <header>
        <span className="astra-evidence-inspector-icon">
          <UroIcon name={meta.icon} aria-hidden="true" />
        </span>
        <div>
          <small>Asset inspector</small>
          <h3>
            {meta.title} <code>{node.asset_id ? `#${node.asset_id}` : node.key}</code>
          </h3>
        </div>
        <button onClick={close} aria-label="Close coverage inspector">
          <UroIcon name="close" aria-hidden="true" />
        </button>
      </header>
      <div className="astra-evidence-inspector-summary">
        <h4>{node.label}</h4>
        <span className="astra-graph-state" data-state={status.key}>
          {status.title}
        </span>
      </div>
      <nav aria-label="Asset inspector sections">
        {["overview", "evidence", "raw"].map((value) => (
          <button key={value} aria-pressed={tab === value} onClick={() => setTab(value)}>
            {value}
          </button>
        ))}
      </nav>
      <div className="astra-evidence-inspector-body">
        {tab === "overview" && (
          <>
            <h4>{coverageLabel(node)}</h4>
            <dl>
              {Object.entries(node)
                .filter(([key]) => !["key", "label", "tested", "in_scope"].includes(key))
                .map(([key, value]) =>
                  value !== undefined && value !== null && value !== "" ? (
                    <div key={key}>
                      <dt>{key.replaceAll("_", " ")}</dt>
                      <dd>{typeof value === "object" ? JSON.stringify(value) : String(value)}</dd>
                    </div>
                  ) : null,
                )}
            </dl>
            <p>
              {tracking
                ? "Tested status comes from recorded fact anchors in this task and its configured sources."
                : "Coverage tracking is off. Stored source fields remain available in raw data."}
            </p>
          </>
        )}
        {tab === "evidence" && (
          <>
            {!node.asset_id && <p>Context nodes do not have direct evidence bindings.</p>}
            {references.busy && !references.data && <p role="status">Resolving linked evidence…</p>}
            {references.error && (
              <div role="alert">
                <p>{references.error.message}</p>
                <button onClick={() => void references.reload()}>Retry linked evidence</button>
              </div>
            )}
            {references.data &&
              (["intents", "facts", "findings"] as const).map((kind) => {
                const items = Array.isArray(references.data?.[kind]) ? references.data[kind] : [];
                return (
                  <section key={kind} className="astra-coverage-references">
                    <h5>
                      {kind} · {items.length}
                    </h5>
                    {!items.length ? (
                      <p>No linked {kind} for this asset.</p>
                    ) : (
                      items.map((item) => (
                        <article key={`${item.kind}:${item.id}`}>
                          <strong>{item.summary || "Recorded evidence"}</strong>
                          <small>
                            {item.kind} · #{item.id}
                            {item.state ? ` · ${item.state}` : ""}
                            {item.inherited ? " · Inherited" : ""}
                          </small>
                          {item.inherited && item.source_task_id && (
                            <a href={`/function/tasks/detail?id=${encodeURIComponent(item.source_task_id)}`}>
                              Source task #{item.source_task_id}
                              <UroIcon name="arrow-up-right" aria-hidden="true" />
                            </a>
                          )}
                        </article>
                      ))
                    )}
                  </section>
                );
              })}
          </>
        )}
        {tab === "raw" && (
          <pre className="astra-evidence-raw" tabIndex={0}>
            {JSON.stringify(node, null, 2)}
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
        Recorded asset context
      </footer>
    </aside>
  );
}

export function CoverageFoldInspector({
  fold,
  tracking,
  choose,
  reveal,
  close,
}: {
  fold: CoverageFold;
  tracking: boolean;
  choose: (id: string) => void;
  reveal: () => void;
  close: () => void;
}) {
  const meta = coverageKinds[fold.group.kind] || coverageKinds.endpoint;
  return (
    <aside className="astra-evidence-inspector astra-coverage-inspector" aria-label="Folded assets inspector">
      <header>
        <span className="astra-evidence-inspector-icon">
          <UroIcon name={meta.icon} aria-hidden="true" />
        </span>
        <div>
          <small>Collapsed branch</small>
          <h3>
            {fold.hidden.length} more {meta.title.toLowerCase()} assets
          </h3>
        </div>
        <button onClick={close} aria-label="Close coverage inspector">
          <UroIcon name="close" aria-hidden="true" />
        </button>
      </header>
      <div className="astra-evidence-inspector-body">
        <p>Inspect a hidden record or reveal the next batch on the canvas.</p>
        {fold.hidden.slice(0, coverageBatch).map((node) => (
          <button className="astra-evidence-neighbor" key={node.key} onClick={() => choose(node.key)}>
            <span>{node.label}</span>
            <small>{coverageStatus(node, tracking).title}</small>
          </button>
        ))}
        <button onClick={reveal}>
          <UroIcon name="plus" aria-hidden="true" />
          Reveal next {Math.min(coverageBatch, fold.hidden.length)}
        </button>
      </div>
      <footer>{fold.hidden.length} hidden siblings · full records retained</footer>
    </aside>
  );
}
