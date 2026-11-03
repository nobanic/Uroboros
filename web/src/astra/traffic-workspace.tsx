"use client";

import { useEffect, useMemo, useState } from "react";
import { UroIcon } from "@/components/uroboros-icons";
import type { TrafficExchange, TrafficHost, TrafficResp } from "@/lib/types";
import { ApiFailure } from "./client";
import { consoleRequest } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import { TrafficBinding } from "./traffic-binding";
import { TrafficReader } from "./traffic-reader";
import {
  defaultTrafficPreference,
  emptyTrafficFilters,
  trafficBytes,
  trafficDeletion,
  trafficMethods,
  trafficPagePath,
  trafficPreferences,
  trafficPulse,
  trafficSizes,
  type TrafficDelete,
  type TrafficFilters,
  type TrafficPreference,
  type TrafficSort,
} from "./traffic-document";
import { useConsoleResource } from "./use-console-resource";
import "./forms.css";
import "./traffic.css";

const preferenceKey = "astra.traffic.preferences";
export function TrafficWorkspace({ onUnauthorized }: { onUnauthorized?: () => void }) {
  const [ready, hydrate] = useState(false),
    [preference, setPreference] = useState(defaultTrafficPreference);
  const [overview, rememberOverview] = useState<Pick<TrafficResp, "enabled" | "proxy" | "count">>();
  const [draft, setDraft] = useState({ ...emptyTrafficFilters }),
    [filters, apply] = useState({ ...emptyTrafficFilters });
  const [page, setPage] = useState(0),
    [revision, refresh] = useState(0),
    [advanced, showAdvanced] = useState(false);
  const [showTargets, toggleTargets] = useState(true),
    [ascendingHosts, sortHosts] = useState(false),
    [hosts, selectHosts] = useState(new Set<string>());
  const [selected, inspect] = useState<TrafficExchange>(),
    [flows, selectFlows] = useState(new Set<string>()),
    [binding, bind] = useState<string[]>();
  const [deletion, deleteMode] = useState<TrafficDelete>(),
    [busy, pending] = useState(false),
    [failure, fail] = useState(""),
    [notice, notify] = useState("");
  useEffect(() => {
    try {
      let oldSort: unknown;
      try {
        oldSort = JSON.parse(localStorage.getItem("traffic-sort") || "null");
      } catch {}
      setPreference(
        trafficPreferences(
          JSON.parse(localStorage.getItem(preferenceKey) || "null"),
          localStorage.getItem("uroboros_traffic_workspace_view"),
          oldSort,
        ),
      );
    } catch {
      /* Valid defaults remain usable without storage. */
    }
    toggleTargets(window.innerWidth >= 1100);
    hydrate(true);
  }, []);
  useEffect(() => {
    if (ready)
      try {
        localStorage.setItem(preferenceKey, JSON.stringify(preference));
      } catch {
        /* Browser preference only. */
      }
  }, [ready, preference]);
  useEffect(() => {
    const timer = window.setTimeout(() => {
      apply((current) => ({
        ...current,
        host: draft.host.trim(),
        query: draft.query.trim(),
        body: draft.body.trim(),
        path: draft.path.trim(),
        minimum: draft.minimum.trim(),
        maximum: draft.maximum.trim(),
      }));
      setPage(0);
    }, 300);
    return () => window.clearTimeout(timer);
  }, [draft.host, draft.query, draft.body, draft.path, draft.minimum, draft.maximum]);
  const list = useConsoleResource<TrafficResp>(
    ready ? trafficPagePath(filters, preference, page) : null,
    revision,
    page === 0 ? 5000 : 0,
  );
  const targets = useConsoleResource<{ hosts: TrafficHost[] }>(
    ready ? "/traffic/hosts" : null,
    revision,
    page === 0 ? 5000 : 0,
  );
  const exchanges = useMemo(() => (Array.isArray(list.data?.exchanges) ? list.data.exchanges : []), [list.data]);
  useEffect(() => {
    if (list.data) rememberOverview({ enabled: list.data.enabled, proxy: list.data.proxy, count: list.data.count });
  }, [list.data]);
  const hostRows = useMemo(
    () =>
      [...(Array.isArray(targets.data?.hosts) ? targets.data.hosts : [])].sort(
        (left, right) =>
          (ascendingHosts ? left.count - right.count : right.count - left.count) || left.host.localeCompare(right.host),
      ),
    [targets.data, ascendingHosts],
  );
  const total = list.data?.total ?? exchanges.length,
    pages = Math.max(1, Math.ceil(total / preference.size)),
    pulse = trafficPulse(exchanges);
  useEffect(() => {
    if (list.data && page >= pages) setPage(pages - 1);
  }, [list.data, page, pages]);
  useEffect(() => {
    if (preference.view !== "stream") return;
    inspect((current) => exchanges.find((exchange) => exchange.id === current?.id) || exchanges[0]);
  }, [exchanges, preference.view]);
  useEffect(() => {
    if ([list.error, targets.error].some((error) => error instanceof ApiFailure && error.status === 401))
      onUnauthorized?.();
  }, [list.error, targets.error, onUnauthorized]);
  function immediate(key: "method" | "status", value: string) {
    setDraft((current) => ({ ...current, [key]: value }));
    apply((current) => ({ ...current, [key]: value }));
    setPage(0);
  }
  function configure(patch: Partial<TrafficPreference>) {
    setPreference((current) => ({ ...current, ...patch }));
    setPage(0);
    if (patch.view) inspect(undefined);
  }
  function sort(field: TrafficSort) {
    configure({ sort: field, order: preference.sort === field && preference.order === "desc" ? "asc" : "desc" });
  }
  function toggleFlow(id: string) {
    selectFlows((current) => {
      const next = new Set(current);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }
  function togglePage() {
    const all = exchanges.length > 0 && exchanges.every((exchange) => flows.has(exchange.id));
    selectFlows((current) => {
      const next = new Set(current);
      for (const exchange of exchanges)
        if (all) next.delete(exchange.id);
        else next.add(exchange.id);
      return next;
    });
  }
  function startDeletion(mode: TrafficDelete) {
    try {
      trafficDeletion(mode);
      fail("");
      deleteMode(mode);
    } catch (error) {
      fail(error instanceof Error ? error.message : String(error));
    }
  }
  async function remove() {
    if (!deletion) return;
    pending(true);
    fail("");
    try {
      const document = trafficDeletion(deletion),
        result = await consoleRequest<{ deleted: number; reclaimed?: number }>(document.path, "DELETE", document.body);
      deleteMode(undefined);
      inspect(undefined);
      selectFlows(new Set());
      if (deletion.kind !== "host") selectHosts(new Set());
      notify(
        `${deletion.kind === "all" ? "Purged" : "Deleted"} ${result.deleted} traffic records${result.reclaimed ? ` · freed ${trafficBytes(result.reclaimed)}` : ""}. Traffic selection cleared; saved finding evidence is retained.`,
      );
      setPage(0);
      refresh((value) => value + 1);
    } catch (error) {
      fail(error instanceof Error ? error.message : String(error));
    } finally {
      pending(false);
    }
  }
  function clearAdvanced() {
    const fields = { body: "", path: "", status: "all", minimum: "", maximum: "" };
    setDraft((current) => ({ ...current, ...fields }));
    apply((current) => ({ ...current, ...fields }));
    setPage(0);
  }
  const advancedCount = [
    filters.body,
    filters.path,
    filters.status === "all" ? "" : filters.status,
    filters.minimum,
    filters.maximum,
  ].filter(Boolean).length;
  return (
    <section className="astra-model-workspace astra-traffic-workspace">
      <header className="astra-traffic-heading">
        <div className="astra-traffic-identity">
          <span>
            <UroIcon name="traffic" aria-hidden="true" />
          </span>
          <div>
            <p className="astra-eyebrow">Network evidence</p>
            <h1>Traffic</h1>
            <p>Recorded exchanges, connected to their captured source.</p>
          </div>
        </div>
        <div className="astra-traffic-capture">
          <span data-recording={overview?.enabled === true}>
            <i />
            {!overview ? "Loading recorder…" : overview.enabled ? "Recording" : "Capture disabled"}
          </span>
          <code>{overview?.proxy || "Proxy address not reported"}</code>
          <small>{overview?.count ?? 0} recorded globally</small>
        </div>
      </header>
      <div className="astra-traffic-command">
        <div role="group" aria-label="Traffic view">
          <button aria-pressed={preference.view === "stream"} onClick={() => configure({ view: "stream" })}>
            <UroIcon name="traffic" aria-hidden="true" />
            Evidence stream
          </button>
          <button aria-pressed={preference.view === "ledger"} onClick={() => configure({ view: "ledger" })}>
            <UroIcon name="records" aria-hidden="true" />
            Packet ledger
          </button>
        </div>
        <span>{page === 0 ? "Live refresh · 5 seconds" : "Historical page · manual refresh"}</span>
        <button className="astra-button" aria-expanded={showTargets} onClick={() => toggleTargets((value) => !value)}>
          <UroIcon name="assets" aria-hidden="true" />
          Targets
        </button>
        <button
          className="astra-button"
          disabled={list.busy || targets.busy}
          onClick={() => refresh((value) => value + 1)}
        >
          <UroIcon name="sync" aria-hidden="true" />
          Refresh traffic
        </button>
        <button className="astra-button is-danger" disabled={busy} onClick={() => startDeletion({ kind: "all" })}>
          Purge traffic
        </button>
      </div>
      <div className="astra-traffic-filters">
        <label>
          Recorded host
          <input
            aria-label="Traffic host filter"
            value={draft.host}
            placeholder="Host substring…"
            onChange={(event) => setDraft((current) => ({ ...current, host: event.target.value }))}
          />
        </label>
        <label>
          Search captures
          <input
            aria-label="Search captured traffic"
            value={draft.query}
            placeholder="URL, content or metadata…"
            onChange={(event) => setDraft((current) => ({ ...current, query: event.target.value }))}
          />
        </label>
        <label>
          Method
          <select
            aria-label="Traffic method filter"
            value={draft.method}
            onChange={(event) => immediate("method", event.target.value)}
          >
            <option value="all">All methods</option>
            {trafficMethods.map((method) => (
              <option key={method}>{method}</option>
            ))}
          </select>
        </label>
        <button className="astra-button" aria-expanded={advanced} onClick={() => showAdvanced((value) => !value)}>
          <UroIcon name="filter" aria-hidden="true" />
          Advanced filters{advancedCount ? ` · ${advancedCount}` : ""}
        </button>
        <button
          className="astra-button is-danger"
          disabled={!filters.host || busy}
          onClick={() => startDeletion({ kind: "host", host: filters.host })}
        >
          Delete by host filter
        </button>
      </div>
      {advanced && (
        <div className="astra-traffic-advanced">
          <label>
            Captured text
            <input
              aria-label="Traffic body filter"
              value={draft.body}
              placeholder="At least 3 characters"
              onChange={(event) => setDraft((current) => ({ ...current, body: event.target.value }))}
            />
            <small>Searches captured headers and bodies.</small>
          </label>
          <label>
            Path template
            <input
              aria-label="Traffic path filter"
              value={draft.path}
              onChange={(event) => setDraft((current) => ({ ...current, path: event.target.value }))}
            />
          </label>
          <label>
            HTTP status
            <input
              aria-label="Traffic status filter"
              list="astra-traffic-statuses"
              value={draft.status === "all" ? "" : draft.status}
              placeholder="All / 404 / 4xx"
              onChange={(event) => immediate("status", event.target.value || "all")}
            />
            <datalist id="astra-traffic-statuses">
              {["2xx", "3xx", "4xx", "5xx"].map((status) => (
                <option key={status} value={status} />
              ))}
            </datalist>
          </label>
          <label>
            Minimum bytes
            <input
              aria-label="Traffic minimum bytes"
              type="number"
              min="0"
              value={draft.minimum}
              onChange={(event) => setDraft((current) => ({ ...current, minimum: event.target.value }))}
            />
          </label>
          <label>
            Maximum bytes
            <input
              aria-label="Traffic maximum bytes"
              type="number"
              min="0"
              value={draft.maximum}
              onChange={(event) => setDraft((current) => ({ ...current, maximum: event.target.value }))}
            />
          </label>
          <button className="astra-button" onClick={clearAdvanced}>
            Clear advanced filters
          </button>
          {draft.body.trim() && [...draft.body.trim()].length < 3 && (
            <p>Body searches shorter than 3 characters are ignored by the recorder index.</p>
          )}
        </div>
      )}
      {(failure || list.error || targets.error) && (
        <div className="astra-form-error" role="alert">
          {failure || list.error?.message || targets.error?.message}
          {list.error && list.data ? " · Last successful capture page retained." : ""}
        </div>
      )}
      {notice && (
        <p className="astra-traffic-notice" role="status">
          {notice}
        </p>
      )}
      <div className="astra-traffic-selection">
        <label>
          <input
            type="checkbox"
            aria-label="Select traffic on this page"
            checked={exchanges.length > 0 && exchanges.every((exchange) => flows.has(exchange.id))}
            onChange={togglePage}
          />
          Page selection
        </label>
        <span>{flows.size} traffic selected across pages</span>
        <button className="astra-button is-primary" disabled={!flows.size} onClick={() => bind([...flows])}>
          Link to vulnerability
        </button>
        {flows.size > 0 && (
          <button className="astra-button" onClick={() => selectFlows(new Set())}>
            Clear traffic selection
          </button>
        )}
        <div className="astra-traffic-sort">
          <label>
            Sort
            <select
              aria-label="Traffic sort field"
              value={preference.sort}
              onChange={(event) => configure({ sort: event.target.value as TrafficSort })}
            >
              <option value="ts">Time</option>
              <option value="status">Status code</option>
              <option value="resp_len">Response length</option>
            </select>
          </label>
          <button
            className="astra-button"
            aria-label="Toggle traffic sort direction"
            onClick={() => configure({ order: preference.order === "asc" ? "desc" : "asc" })}
          >
            {preference.order === "desc" ? "↓ Descending" : "↑ Ascending"}
          </button>
        </div>
      </div>
      <div className="astra-traffic-layout" data-targets={showTargets}>
        {showTargets && (
          <aside className="astra-traffic-targets">
            <header>
              <div>
                <small>Recorded targets</small>
                <strong>{hostRows.length} hosts</strong>
              </div>
              <button aria-label="Toggle host count order" onClick={() => sortHosts((value) => !value)}>
                {ascendingHosts ? "↑" : "↓"}
              </button>
            </header>
            <label className="astra-traffic-host-all">
              <input
                type="checkbox"
                aria-label="Select all traffic hosts"
                checked={hostRows.length > 0 && hostRows.every((row) => hosts.has(row.host))}
                onChange={() =>
                  selectHosts((current) =>
                    hostRows.every((row) => current.has(row.host))
                      ? new Set()
                      : new Set(hostRows.map((row) => row.host)),
                  )
                }
              />
              Select all hosts
            </label>
            <div className="astra-traffic-hosts">
              {hostRows.map((row) => (
                <div key={row.host}>
                  <input
                    type="checkbox"
                    aria-label={`Select traffic host ${row.host}`}
                    checked={hosts.has(row.host)}
                    onChange={() =>
                      selectHosts((current) => {
                        const next = new Set(current);
                        if (next.has(row.host)) next.delete(row.host);
                        else next.add(row.host);
                        return next;
                      })
                    }
                  />
                  <button
                    onClick={() => {
                      setDraft((current) => ({ ...current, host: row.host }));
                      apply((current) => ({ ...current, host: row.host }));
                      setPage(0);
                    }}
                    title={`Filter recorded host substring ${row.host}`}
                  >
                    <span>{row.host}</span>
                    <small>{row.count}</small>
                  </button>
                </div>
              ))}
              {!hostRows.length && <p>No recorded hosts.</p>}
            </div>
            <footer>
              <button
                className="astra-button is-danger"
                disabled={!hosts.size || busy}
                onClick={() => startDeletion({ kind: "hosts", hosts: [...hosts] })}
              >
                Delete {hosts.size || "selected"} exact hosts
              </button>
            </footer>
          </aside>
        )}
        <div className="astra-traffic-main">
          <div className="astra-traffic-pulse">
            <div>
              <small>Current page</small>
              <strong>{exchanges.length} exchanges</strong>
            </div>
            <dl>
              {pulse.map((count, index) => (
                <div key={index}>
                  <dt>{index + 2}xx</dt>
                  <dd data-family={index + 2}>{count}</dd>
                </div>
              ))}
            </dl>
          </div>
          {preference.view === "stream" ? (
            <div className="astra-traffic-review">
              <div
                className="astra-traffic-stream"
                aria-label="Captured exchanges"
                onKeyDown={(event) => {
                  if (!["ArrowUp", "ArrowDown"].includes(event.key) || (event.target as HTMLElement).closest("input"))
                    return;
                  event.preventDefault();
                  const index = exchanges.findIndex((exchange) => exchange.id === selected?.id),
                    next = Math.min(exchanges.length - 1, Math.max(0, index + (event.key === "ArrowDown" ? 1 : -1))),
                    exchange = exchanges[next];
                  if (!exchange) return;
                  inspect(exchange);
                  requestAnimationFrame(() =>
                    [...document.querySelectorAll<HTMLButtonElement>("button[data-traffic-open]")]
                      .find((button) => button.dataset.trafficOpen === exchange.id)
                      ?.focus(),
                  );
                }}
              >
                {exchanges.map((exchange) => (
                  <article key={exchange.id} data-selected={selected?.id === exchange.id}>
                    <input
                      type="checkbox"
                      aria-label={`Select traffic ${exchange.id}`}
                      checked={flows.has(exchange.id)}
                      onChange={() => toggleFlow(exchange.id)}
                    />
                    <button
                      data-traffic-open={exchange.id}
                      aria-pressed={selected?.id === exchange.id}
                      onClick={() => inspect(exchange)}
                    >
                      <span className="astra-traffic-event-head">
                        <b className="astra-traffic-method">{exchange.method}</b>
                        <b className="astra-traffic-code" data-family={Math.floor(exchange.status / 100)}>
                          {exchange.status}
                        </b>
                        <time>{new Date(exchange.ts).toLocaleTimeString()}</time>
                      </span>
                      <strong>{exchange.host}</strong>
                      <code>{exchange.url}</code>
                      <span className="astra-traffic-event-meta">
                        {exchange.content_type || "Unknown content type"}
                        <small>{trafficBytes(exchange.resp_len)}</small>
                      </span>
                    </button>
                  </article>
                ))}
                {!exchanges.length && (
                  <p className="astra-empty">
                    {list.busy ? "Loading traffic…" : list.error ? "Capture page unavailable." : "No matching traffic."}
                  </p>
                )}
              </div>
              {selected ? (
                <TrafficReader key={selected.id} exchange={selected} />
              ) : (
                <div className="astra-traffic-reader-empty">
                  <UroIcon name="traffic" aria-hidden="true" />
                  <strong>Select a captured exchange</strong>
                  <p>Review its request and response alongside the stream.</p>
                </div>
              )}
            </div>
          ) : (
            <div className="astra-traffic-ledger">
              <table>
                <thead>
                  <tr>
                    <th>Select</th>
                    {["ts", "host", "method", "url", "status", "content_type", "resp_len"].map((field) => (
                      <th key={field}>
                        {["ts", "status", "resp_len"].includes(field) ? (
                          <button onClick={() => sort(field as TrafficSort)}>
                            {field === "ts" ? "Time" : field === "resp_len" ? "Response length" : "Status code"}
                            {preference.sort === field ? (preference.order === "desc" ? " ↓" : " ↑") : ""}
                          </button>
                        ) : (
                          field.replaceAll("_", " ")
                        )}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {exchanges.map((exchange) => (
                    <tr key={exchange.id}>
                      <td>
                        <input
                          type="checkbox"
                          aria-label={`Select traffic ${exchange.id}`}
                          checked={flows.has(exchange.id)}
                          onChange={() => toggleFlow(exchange.id)}
                        />
                      </td>
                      <td>{new Date(exchange.ts).toLocaleString()}</td>
                      <td>{exchange.host}</td>
                      <td>{exchange.method}</td>
                      <td>
                        <button data-traffic-open={exchange.id} onClick={() => inspect(exchange)}>
                          {exchange.url}
                        </button>
                      </td>
                      <td>
                        <span className="astra-traffic-code" data-family={Math.floor(exchange.status / 100)}>
                          {exchange.status}
                        </span>
                      </td>
                      <td>{exchange.content_type}</td>
                      <td>{trafficBytes(exchange.resp_len)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {!exchanges.length && (
                <p className="astra-empty">{list.busy ? "Loading traffic…" : "No matching traffic."}</p>
              )}
            </div>
          )}
        </div>
      </div>
      <footer className="astra-traffic-pagination">
        <span>
          {total ? page * preference.size + 1 : 0}–{total ? page * preference.size + exchanges.length : 0} of {total}{" "}
          matching captures
        </span>
        <button
          className="astra-button"
          disabled={page <= 0 || list.busy}
          onClick={() => setPage((value) => value - 1)}
        >
          Previous traffic page
        </button>
        <span>{list.data ? `Page ${page + 1} of ${pages}` : "Loading capture page…"}</span>
        <button
          className="astra-button"
          disabled={page + 1 >= pages || list.busy}
          onClick={() => setPage((value) => value + 1)}
        >
          Next traffic page
        </button>
        <label>
          Per page
          <select
            aria-label="Traffic page size"
            value={preference.size}
            onChange={(event) => configure({ size: Number(event.target.value) })}
          >
            {trafficSizes.map((size) => (
              <option key={size}>{size}</option>
            ))}
          </select>
        </label>
      </footer>
      {preference.view === "ledger" && selected && (
        <EditorDialog title="Captured HTTP exchange" eyebrow="Traffic inspector" close={() => inspect(undefined)}>
          <TrafficReader key={selected.id} exchange={selected} />
        </EditorDialog>
      )}
      {binding && (
        <TrafficBinding
          ids={binding}
          close={() => bind(undefined)}
          saved={() => {
            selectFlows(new Set());
            notify(`Linked ${binding.length} captured exchanges to the selected finding.`);
          }}
        />
      )}
      {deletion && (
        <EditorDialog
          title={deletion.kind === "all" ? "Purge all traffic records?" : "Delete captured traffic?"}
          eyebrow="Traffic deletion"
          busy={busy}
          close={() => deleteMode(undefined)}
        >
          <p>{trafficDeletion(deletion).description}</p>
          {deletion.kind === "hosts" && (
            <ul>
              {deletion.hosts.map((host) => (
                <li key={host}>{host}</li>
              ))}
            </ul>
          )}
          <p>
            Finding evidence already saved by the server remains available. This action removes global capture records.
          </p>
          {failure && (
            <p className="astra-form-error" role="alert">
              {failure}
            </p>
          )}
          <footer className="astra-editor-actions">
            <button className="astra-button" disabled={busy} onClick={() => deleteMode(undefined)}>
              Cancel
            </button>
            <button className="astra-button is-danger" disabled={busy} onClick={() => void remove()}>
              {busy ? "Deleting…" : deletion.kind === "all" ? "Confirm traffic purge" : "Confirm traffic deletion"}
            </button>
          </footer>
        </EditorDialog>
      )}
    </section>
  );
}
