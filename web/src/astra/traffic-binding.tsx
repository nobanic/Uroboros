"use client";

import { useEffect, useState } from "react";
import type { Finding, FindingsPage } from "@/lib/types";
import { EditorDialog } from "./editor-dialog";
import { consoleRequest } from "./console-api";
import { findingIdentity, findingPagePath } from "./finding-document";
import { trafficBinding } from "./traffic-document";
import { useConsoleResource } from "./use-console-resource";

export function TrafficBinding({ ids, close, saved }: { ids: string[]; close: () => void; saved: () => void }) {
  const [query, search] = useState(""),
    [applied, apply] = useState(""),
    [page, setPage] = useState(1);
  const [selected, select] = useState<Finding>(),
    [busy, pending] = useState(false),
    [error, fail] = useState("");
  useEffect(() => {
    const timer = window.setTimeout(() => {
      apply(query);
      setPage(1);
    }, 250);
    return () => window.clearTimeout(timer);
  }, [query]);
  const findings = useConsoleResource<FindingsPage>(findingPagePath({ query: applied }, page, 20));
  async function bind() {
    if (!selected) return;
    pending(true);
    fail("");
    try {
      const document = trafficBinding(selected, ids);
      await consoleRequest(document.path, "POST", document.body);
      saved();
      close();
    } catch (error) {
      fail(error instanceof Error ? error.message : String(error));
    } finally {
      pending(false);
    }
  }
  return (
    <EditorDialog title="Link traffic to finding" eyebrow="Traffic evidence" busy={busy} close={close}>
      <p>
        Save {ids.length} captured exchange{ids.length === 1 ? "" : "s"} as evidence on a writable finding. The server
        preserves its captured bytes.
      </p>
      <label>
        Find vulnerability
        <input
          aria-label="Find traffic finding"
          value={query}
          onChange={(event) => search(event.target.value)}
          placeholder="Name, summary or vulnerability class"
          disabled={busy}
        />
      </label>
      {(error || findings.error) && (
        <p className="astra-form-error" role="alert">
          {error || findings.error?.message}
        </p>
      )}
      {findings.error && (
        <button className="astra-button" onClick={() => void findings.reload()}>
          Retry finding search
        </button>
      )}
      <div className="astra-traffic-finding-choices" aria-busy={findings.busy}>
        {(findings.data?.items || []).map((finding) => (
          <button
            key={findingIdentity(finding)}
            type="button"
            disabled={busy || !finding.finding_id || finding.inherited}
            aria-pressed={selected?.finding_id === finding.finding_id}
            onClick={() => select(finding)}
          >
            <strong>
              #{finding.finding_id || finding.id} · {finding.name || finding.vulnclass}
            </strong>
            <span>{finding.summary}</span>
            {finding.inherited && <small>Inherited · read only</small>}
          </button>
        ))}
        {!findings.data?.items?.length && (
          <p>
            {findings.busy
              ? "Loading findings…"
              : findings.error
                ? "Finding list unavailable."
                : "No matching findings."}
          </p>
        )}
      </div>
      <div className="astra-traffic-binding-pages">
        <button
          className="astra-button"
          disabled={busy || findings.busy || page <= 1}
          onClick={() => setPage((value) => value - 1)}
        >
          Previous findings
        </button>
        <span>
          Page {page} · {findings.data?.total || 0} findings
        </span>
        <button
          className="astra-button"
          disabled={busy || findings.busy || page * 20 >= (findings.data?.total || 0)}
          onClick={() => setPage((value) => value + 1)}
        >
          Next findings
        </button>
      </div>
      <p role="status">
        {selected
          ? `Selected finding #${selected.finding_id} · ${selected.name || selected.vulnclass}`
          : "Select a finding."}
      </p>
      <footer className="astra-editor-actions">
        <button className="astra-button" disabled={busy} onClick={close}>
          Cancel
        </button>
        <button className="astra-button is-primary" disabled={busy || !selected} onClick={() => void bind()}>
          {busy ? "Saving evidence…" : "Confirm traffic link"}
        </button>
      </footer>
    </EditorDialog>
  );
}
