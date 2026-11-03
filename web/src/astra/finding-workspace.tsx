"use client";

import { useCallback, useEffect, useRef, useState, type ReactNode } from "react";
import type {
  ActiveFindingRetest,
  Finding,
  FindingAssetTree,
  FindingDeepenResponse,
  FindingGroup,
  FindingGroupsPage,
  FindingRetest,
  FindingsPage,
  FindingStats,
} from "@/lib/types";
import { Markdown } from "@/components/markdown";
import { ApiFailure } from "./client";
import { consoleDownload, consoleRequest } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import { FormFields } from "./form-fields";
import type { FormValues } from "./forms";
import {
  defaultFindingFilters,
  findingAssetRows,
  findingAssetTrail,
  findingExportPath,
  findingFilterParameters,
  findingFollowupDocument,
  findingHandle,
  findingIdentity,
  findingMetadataDocument,
  findingMetadataDraft,
  findingMetadataForm,
  findingPagePath,
  findingPreferences,
  findingPreferencesKey,
  findingSeverities,
  findingStatuses,
  findingsPath,
  findingViews,
  unassignedFindingTask,
  type FindingFilters,
  type FindingView,
} from "./finding-document";
import { useConsoleResource } from "./use-console-resource";
import "./forms.css";
import "./finding-workspace.css";

type FindingActions = {
  busy: boolean;
  selected: ReadonlySet<string>;
  active: ReadonlyMap<string, ActiveFindingRetest>;
  toggle: (ids: string[], checked: boolean) => void;
  status: (finding: Finding, status: string) => void;
  edit: (finding: Finding) => void;
  remove: (finding: Finding) => void;
  retest: (finding: Finding) => void;
  deepen: (finding: Finding) => void;
};
type FindingModal =
  | { kind: "edit" | "delete" | "retest" | "deepen"; finding: Finding; draft: FormValues }
  | { kind: "export"; draft: FormValues };
const observedTime = (value?: string) => (value ? new Date(value).toLocaleString() : "—");
const statusLabel = (value: string) => value.replaceAll("_", " ");

function FindingError({ error }: { error?: Error }) {
  return error ? (
    <p className="astra-form-error" role="alert">
      {error.message}
    </p>
  ) : null;
}

function FindingPager({
  page,
  size,
  total,
  busy,
  change,
  label,
}: {
  page: number;
  size: number;
  total: number;
  busy: boolean;
  change: (page: number, size: number) => void;
  label: string;
}) {
  const pages = Math.max(1, Math.ceil(total / size));
  return (
    <div className="astra-finding-pager" aria-label={`${label} pagination`}>
      <span>
        {total} total · Page {page} of {pages}
      </span>
      <button aria-label={`Previous ${label} page`} disabled={busy || page <= 1} onClick={() => change(page - 1, size)}>
        Previous
      </button>
      <button aria-label={`Next ${label} page`} disabled={busy || page >= pages} onClick={() => change(page + 1, size)}>
        Next
      </button>
      <label>
        Page size
        <select
          aria-label={`${label} page size`}
          disabled={busy}
          value={size}
          onChange={(event) => change(1, Number(event.target.value))}
        >
          {[10, 20, 50, 100].map((value) => (
            <option key={value} value={value}>
              {value}
            </option>
          ))}
        </select>
      </label>
    </div>
  );
}

function FindingButtons({ finding, actions }: { finding: Finding; actions: FindingActions }) {
  const handle = finding.finding_id,
    retest = handle ? actions.active.get(handle) : undefined;
  return (
    <div className="astra-finding-actions">
      {handle && <a href={`/function/findings/detail?id=${encodeURIComponent(handle)}`}>Open finding and evidence</a>}
      {retest ? (
        <a href={`/chat?c=${retest.conversation_id}`}>Retesting #{retest.id}</a>
      ) : (
        handle &&
        !finding.inherited && (
          <button disabled={actions.busy} onClick={() => actions.retest(finding)}>
            Retest finding
          </button>
        )
      )}
      {handle && finding.task_id && (
        <button disabled={actions.busy} onClick={() => actions.deepen(finding)}>
          Deepen finding
        </button>
      )}
      {handle && (
        <>
          <button disabled={actions.busy} onClick={() => actions.edit(finding)}>
            Edit finding
          </button>
          <button disabled={actions.busy} onClick={() => actions.remove(finding)}>
            Delete finding
          </button>
        </>
      )}
    </div>
  );
}

function FindingDisposition({ finding, actions }: { finding: Finding; actions: FindingActions }) {
  return (
    <label className="astra-finding-disposition">
      Disposition
      <select
        aria-label={`Disposition of finding ${finding.finding_id || findingIdentity(finding)}`}
        value={finding.status}
        disabled={actions.busy || !finding.finding_id}
        onChange={(event) => actions.status(finding, event.target.value)}
      >
        {!findingStatuses.includes(finding.status) && (
          <option value={finding.status}>{finding.status || "Unknown"}</option>
        )}
        {findingStatuses.map((status) => (
          <option key={status} value={status}>
            {statusLabel(status)}
          </option>
        ))}
      </select>
    </label>
  );
}

function FindingDetails({ finding }: { finding: Finding }) {
  const full = useConsoleResource<Finding>(finding.finding_id ? `${findingsPath}/${findingHandle(finding)}` : null);
  const [copyError, setCopyError] = useState("");
  const report = full.data?.report ?? finding.report ?? "";
  return (
    <div className="astra-finding-details">
      <FindingError error={full.error} />
      {copyError && <p role="alert">{copyError}</p>}
      <h3>Summary</h3>
      <p>{finding.summary || "No summary"}</p>
      <h3>Evidence</h3>
      <pre>{finding.evidence || "No evidence text"}</pre>
      <button
        onClick={() => {
          void navigator.clipboard.writeText(finding.evidence).catch((error) => setCopyError(String(error)));
        }}
      >
        Copy evidence
      </button>
      <h3>Detailed report</h3>
      {full.busy && !full.data ? (
        <p>Loading report…</p>
      ) : report ? (
        <Markdown text={report} />
      ) : (
        <p>No report has been written.</p>
      )}
      {report && (
        <button
          onClick={() => {
            void navigator.clipboard.writeText(report).catch((error) => setCopyError(String(error)));
          }}
        >
          Copy report
        </button>
      )}
      {full.error && <button onClick={() => void full.reload()}>Retry report</button>}
      {full.data?.report_stale && (
        <p className="astra-form-note">The report was written for an older evidence version.</p>
      )}
    </div>
  );
}

function FindingCollection({
  items,
  actions,
  mode,
  expanded,
  expand,
}: {
  items: Finding[];
  actions: FindingActions;
  mode: "table" | "cards";
  expanded: string | null;
  expand: (key: string) => void;
}) {
  const selectable = items.flatMap((finding) => (finding.finding_id ? [finding.finding_id] : [])),
    count = selectable.filter((id) => actions.selected.has(id)).length;
  const content = (finding: Finding): ReactNode => (
    <>
      <header>
        <div>
          {finding.finding_id && (
            <input
              type="checkbox"
              aria-label={`Select finding ${finding.finding_id}`}
              checked={actions.selected.has(finding.finding_id)}
              onChange={(event) => actions.toggle([finding.finding_id!], event.target.checked)}
            />
          )}
          <code>{finding.finding_id ? `F-${finding.finding_id}` : finding.id}</code>
          <span className="astra-badge" data-state={finding.severity}>
            {finding.severity || "Unknown severity"}
          </span>
        </div>
        <button
          className="astra-row-link"
          aria-expanded={expanded === findingIdentity(finding)}
          onClick={() => expand(findingIdentity(finding))}
        >
          {finding.name || finding.vulnclass || "Untitled finding"}
        </button>
      </header>
      <p>
        {finding.vulnclass} · {finding.summary}
      </p>
      <div className="astra-finding-assets">
        {(finding.assets || []).map((asset) => (
          <span key={asset.id} title={`${asset.type} · ${asset.label}`}>
            {asset.label}
          </span>
        ))}
      </div>
      <div className="astra-finding-context">
        <span>{finding.traffic_count || 0} traffic references</span>
        <span>{observedTime(finding.ts)}</span>
        {finding.task_id ? (
          <a href={`/function/tasks/detail?id=${encodeURIComponent(finding.task_id)}`}>
            Task #{finding.task_id}
            {finding.task_description ? ` · ${finding.task_description}` : ""}
          </a>
        ) : (
          <span>Unassigned / deleted task</span>
        )}
        {finding.inherited && <span>Inherited from task {finding.source_task_id || finding.task_id}</span>}
      </div>
      <FindingDisposition finding={finding} actions={actions} />
      <FindingButtons finding={finding} actions={actions} />
      {expanded === findingIdentity(finding) && <FindingDetails key={findingIdentity(finding)} finding={finding} />}
    </>
  );
  if (mode === "cards") {
    const levels: string[] = [...findingSeverities];
    if (items.some((finding) => !findingSeverities.includes(finding.severity))) levels.push("other");
    return (
      <div className="astra-finding-lanes">
        {levels.map((level) => (
          <section key={level} data-severity={level}>
            <h2>
              {level} ·{" "}
              {
                items.filter((finding) =>
                  level === "other" ? !findingSeverities.includes(finding.severity) : finding.severity === level,
                ).length
              }
            </h2>
            {items
              .filter((finding) =>
                level === "other" ? !findingSeverities.includes(finding.severity) : finding.severity === level,
              )
              .map((finding) => (
                <article key={findingIdentity(finding)} data-finding={finding.finding_id}>
                  {content(finding)}
                </article>
              ))}
          </section>
        ))}
      </div>
    );
  }
  return (
    <div className="astra-finding-records">
      <label className="astra-form-check">
        <input
          type="checkbox"
          aria-label="Select all findings on this page"
          checked={selectable.length > 0 && count === selectable.length}
          ref={(node) => {
            if (node) node.indeterminate = count > 0 && count < selectable.length;
          }}
          onChange={(event) => actions.toggle(selectable, event.target.checked)}
        />
        Select all on this page
      </label>
      {items.map((finding) => (
        <article key={findingIdentity(finding)} data-finding={finding.finding_id}>
          {content(finding)}
        </article>
      ))}
      {!items.length && <p>No findings match this page.</p>}
    </div>
  );
}

function FindingGroupPage({
  group,
  filters,
  revision,
  actions,
  expanded,
  expand,
  onError,
  position,
  onPage,
}: {
  group: FindingGroup;
  filters: FindingFilters;
  revision: number;
  actions: FindingActions;
  expanded: string | null;
  expand: (key: string) => void;
  onError: (error?: Error) => void;
  position: { page: number; size: number };
  onPage: (page: number, size: number) => void;
}) {
  const { page, size } = position;
  const key = group.task_id === null ? unassignedFindingTask : String(group.task_id);
  const list = useConsoleResource<FindingsPage>(findingPagePath({ ...filters, task: key }, page, size), revision, 5000);
  useEffect(() => {
    onError(list.error);
  }, [list.error, onError]);
  useEffect(() => {
    if (list.data && !list.busy) {
      const last = Math.max(1, Math.ceil(list.data.total / size));
      if (page > last) onPage(last, size);
    }
  }, [list.data, list.busy, page, size, onPage]);
  return (
    <div className="astra-finding-group-page" data-group={key}>
      <FindingError error={list.error} />
      <FindingCollection
        items={list.data?.items || []}
        actions={actions}
        mode="table"
        expanded={expanded}
        expand={expand}
      />
      <FindingPager
        label={`group ${key}`}
        page={page}
        size={size}
        total={list.data?.total || 0}
        busy={list.busy}
        change={onPage}
      />
    </div>
  );
}

export function FindingsWorkspace({ onUnauthorized }: { onUnauthorized?: () => void }) {
  const [view, setView] = useState<FindingView>("triage"),
    [filters, setFilters] = useState<FindingFilters>(defaultFindingFilters),
    [ready, setReady] = useState(false),
    [search, setSearch] = useState("");
  const [page, setPage] = useState(1),
    [size, setSize] = useState(20),
    [groupPage, setGroupPage] = useState(1),
    [groupSize, setGroupSize] = useState(10);
  const [scope, setScope] = useState<string>(),
    [expandedGroups, setExpandedGroups] = useState(new Set<string>()),
    [expandedAssets, setExpandedAssets] = useState(new Set<string>()),
    [assetSearch, setAssetSearch] = useState("");
  const [selected, setSelected] = useState(new Set<string>()),
    [expanded, setExpanded] = useState<string | null>(null),
    [revision, setRevision] = useState(0);
  const [groupPositions, setGroupPositions] = useState<Record<string, { page: number; size: number }>>({});
  const [modal, setModal] = useState<FindingModal>(),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [notice, setNotice] = useState("");
  const locked = useRef(false),
    alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    try {
      const saved = findingPreferences(
        localStorage.getItem(findingPreferencesKey) ||
          localStorage.getItem("uroboros_finding_workspace_preferences_v2"),
      );
      setView(saved.view);
      setFilters(saved.filters);
    } catch {}
    setReady(true);
  }, []);
  useEffect(() => {
    if (ready) {
      try {
        localStorage.setItem(findingPreferencesKey, JSON.stringify({ ...filters, view }));
      } catch {}
    }
  }, [ready, filters, view]);
  useEffect(() => {
    const timer = window.setTimeout(
      () =>
        setFilters((previous) => (previous.query === search.trim() ? previous : { ...previous, query: search.trim() })),
      300,
    );
    return () => window.clearTimeout(timer);
  }, [search]);
  const fingerprint = JSON.stringify(filters);
  useEffect(() => {
    setPage(1);
    setGroupPage(1);
    setExpandedGroups(new Set());
    setGroupPositions({});
    setExpanded(null);
    setScope(undefined);
  }, [fingerprint]);
  const activeFilters = { ...filters, assetScope: view === "asset" ? scope : undefined };
  const list = useConsoleResource<FindingsPage>(
    ready && view !== "grouped" ? findingPagePath(activeFilters, page, size) : null,
    revision,
    view === "asset" ? 0 : 5000,
  );
  const groups = useConsoleResource<FindingGroupsPage>(
    ready && view === "grouped" ? findingPagePath(filters, groupPage, groupSize, true) : null,
    revision,
    5000,
  );
  const tree = useConsoleResource<FindingAssetTree>(
    ready && view === "asset" ? `${findingsPath}/asset-tree?${findingFilterParameters(filters)}` : null,
    revision,
  );
  const stats = useConsoleResource<FindingStats>(ready ? `${findingsPath}/stats` : null, 0, 5000);
  const retests = useConsoleResource<{ retests: ActiveFindingRetest[] }>(
    ready ? `${findingsPath}/retests/active` : null,
    0,
    3000,
  );
  const active = new Map((retests.data?.retests || []).map((retest) => [retest.finding_id, retest]));
  const retestFingerprint = JSON.stringify(
    (retests.data?.retests || []).map((retest) => [retest.id, retest.finding_id, retest.status]),
  );
  useEffect(() => {
    if (ready) setRevision((value) => value + 1);
  }, [retestFingerprint, ready]);
  useEffect(() => {
    if (list.data && !list.busy) {
      const last = Math.max(1, Math.ceil(list.data.total / size));
      if (page > last) setPage(last);
    }
  }, [list.data, list.busy, page, size]);
  useEffect(() => {
    if (groups.data && !groups.busy) {
      const last = Math.max(1, Math.ceil(groups.data.total / groupSize));
      if (groupPage > last) setGroupPage(last);
    }
  }, [groups.data, groups.busy, groupPage, groupSize]);
  useEffect(() => {
    if (tree.data && scope && !tree.data.nodes.some((node) => node.key === scope)) {
      setScope(undefined);
      setPage(1);
    }
  }, [tree.data, scope]);
  useEffect(() => {
    if (stats.data)
      setFilters((previous) => ({
        ...previous,
        vulnclass:
          previous.vulnclass === "all" || stats.data!.vulnclasses.includes(previous.vulnclass)
            ? previous.vulnclass
            : "all",
        task:
          previous.task === "all" ||
          previous.task === unassignedFindingTask ||
          stats.data!.tasks.some((task) => String(task.id) === previous.task)
            ? previous.task
            : "all",
      }));
  }, [stats.data]);
  const checkError = useCallback(
    (error?: Error) => {
      if (error instanceof ApiFailure && error.status === 401) onUnauthorized?.();
    },
    [onUnauthorized],
  );
  useEffect(() => {
    for (const result of [list, groups, tree, stats, retests]) checkError(result.error);
  }, [list.error, groups.error, tree.error, stats.error, retests.error, checkError]);
  async function refresh() {
    const results = await Promise.all([
      list.reload(),
      groups.reload(),
      tree.reload(),
      stats.reload(),
      retests.reload(),
    ]);
    if (results.some((result) => !result))
      throw new Error("Some finding data could not reload. Any completed write is saved; retry Refresh findings.");
    setRevision((value) => value + 1);
  }
  async function run(work: () => Promise<void | string>, message: string) {
    if (locked.current) return;
    locked.current = true;
    setBusy(true);
    setError("");
    setNotice("");
    try {
      const outcome = await work();
      if (alive.current) setNotice(typeof outcome === "string" ? outcome : message);
    } catch (error) {
      if (alive.current) {
        setError(String(error));
        checkError(error instanceof Error ? error : undefined);
      }
    } finally {
      locked.current = false;
      if (alive.current) setBusy(false);
    }
  }
  function open(kind: "edit" | "delete" | "retest" | "deepen", finding: Finding) {
    setError("");
    setModal({ kind, finding, draft: kind === "edit" ? findingMetadataDraft(finding) : { text: "" } });
  }
  const actions: FindingActions = {
    busy,
    selected,
    active,
    toggle: (ids, checked) =>
      setSelected((previous) => {
        const next = new Set(previous);
        for (const id of ids) {
          if (checked) next.add(id);
          else next.delete(id);
        }
        return next;
      }),
    status: (finding, status) => {
      if (status === finding.status || !findingStatuses.includes(status as Finding["status"])) return;
      void run(async () => {
        await consoleRequest(`${findingsPath}/${findingHandle(finding)}`, "PATCH", { status });
        await refresh();
      }, "Finding disposition saved");
    },
    edit: (finding) => open("edit", finding),
    remove: (finding) => open("delete", finding),
    retest: (finding) => open("retest", finding),
    deepen: (finding) => open("deepen", finding),
  };
  const trail = findingAssetTrail(tree.data?.nodes || [], scope);
  return (
    <div className="astra-model-workspace astra-finding-workspace">
      <header className="astra-model-header">
        <div>
          <small>Evidence registry</small>
          <h1>Findings</h1>
          <p>Review evidence, manage disposition and follow up from the original context.</p>
        </div>
        <div>
          <button disabled={busy || !ready} onClick={() => void run(refresh, "Findings refreshed")}>
            Refresh findings
          </button>
          <button
            className="is-primary"
            disabled={busy}
            onClick={() => {
              setError("");
              setModal({
                kind: "export",
                draft: { format: "md-single", scope: selected.size ? "selected" : "filtered" },
              });
            }}
          >
            Export findings
          </button>
        </div>
      </header>
      {error && (
        <p className="astra-form-error" role="alert">
          {error}
        </p>
      )}
      {notice && (
        <p className="astra-form-note" role="status">
          {notice}
        </p>
      )}
      {[list.error, groups.error, tree.error, stats.error, retests.error].map((failure, index) => (
        <FindingError key={index} error={failure} />
      ))}
      <dl className="astra-finding-stats">
        {["total", "pending", "critical", "high", "medium", "low"].map((key) => (
          <div key={key}>
            <dt>{statusLabel(key)}</dt>
            <dd>
              {stats.data?.[
                key as keyof Pick<FindingStats, "total" | "pending" | "critical" | "high" | "medium" | "low">
              ] ?? "—"}
            </dd>
          </div>
        ))}
      </dl>
      <div className="astra-finding-filters">
        <label>
          Search findings
          <input
            name="finding_search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="Name, class, summary, evidence or report"
          />
        </label>
        {(
          [
            { key: "severity", label: "Severity", options: findingSeverities.map((value) => [value, value]) },
            {
              key: "status",
              label: "Disposition",
              options: findingStatuses.map((value) => [value, statusLabel(value)]),
            },
            {
              key: "vulnclass",
              label: "Vulnerability class",
              options: (stats.data?.vulnclasses || []).map((value) => [value, value]),
            },
            {
              key: "task",
              label: "Source task",
              options: [
                [unassignedFindingTask, "Unassigned / deleted"],
                ...(stats.data?.tasks || []).map((task) => [
                  String(task.id),
                  task.name || task.description || `Task #${task.id}`,
                ]),
              ],
            },
          ] as { key: keyof FindingFilters; label: string; options: string[][] }[]
        ).map((field) => (
          <label key={field.key}>
            {field.label}
            <select
              name={`finding_filter_${field.key}`}
              value={filters[field.key] || "all"}
              onChange={(event) => setFilters((previous) => ({ ...previous, [field.key]: event.target.value }))}
            >
              <option value="all">All</option>
              {field.options.map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </select>
          </label>
        ))}
        <label>
          Sort
          <select
            name="finding_sort"
            value={filters.sort}
            onChange={(event) => setFilters((previous) => ({ ...previous, sort: event.target.value }))}
          >
            <option value="severity">Severity, newest</option>
            <option value="time">Newest</option>
          </select>
        </label>
      </div>
      <div className="astra-finding-toolbar">
        <nav aria-label="Finding views">
          {findingViews.map((mode) => (
            <button key={mode} aria-pressed={view === mode} onClick={() => setView(mode)}>
              {{ triage: "Triage", flat: "List", grouped: "By task", asset: "By asset" }[mode]}
            </button>
          ))}
        </nav>
        <span>{selected.size} selected across pages</span>
        <button disabled={!selected.size || busy} onClick={() => setSelected(new Set())}>
          Clear finding selection
        </button>
      </div>
      {view === "grouped" ? (
        <section className="astra-finding-groups">
          <p>
            {groups.data?.finding_total ?? 0} findings across {groups.data?.total ?? 0} task groups
          </p>
          {(groups.data?.items || []).map((group) => {
            const key = group.task_id === null ? unassignedFindingTask : String(group.task_id),
              shown = expandedGroups.has(key);
            return (
              <article key={key}>
                <header>
                  <button
                    aria-expanded={shown}
                    aria-label={`Toggle finding group ${key}`}
                    onClick={() =>
                      setExpandedGroups((previous) => {
                        const next = new Set(previous);
                        if (next.has(key)) next.delete(key);
                        else next.add(key);
                        return next;
                      })
                    }
                  >
                    {group.task_id === null
                      ? "Unassigned / deleted task"
                      : group.task_name || group.task_description || `Task #${group.task_id}`}
                  </button>
                  <span>
                    {group.count} findings · {group.critical} critical · {group.high} high · {group.medium} medium ·{" "}
                    {group.low} low · {group.task_status}
                  </span>
                  <time>{observedTime(group.last_found_at)}</time>
                  {group.task_id !== null && (
                    <a href={`/function/tasks/detail?id=${encodeURIComponent(String(group.task_id))}`}>Open task</a>
                  )}
                </header>
                {shown && (
                  <FindingGroupPage
                    key={`${fingerprint}:${key}`}
                    group={group}
                    filters={filters}
                    revision={revision}
                    actions={actions}
                    expanded={expanded}
                    expand={(key) => setExpanded((previous) => (previous === key ? null : key))}
                    onError={checkError}
                    position={groupPositions[key] || { page: 1, size: 10 }}
                    onPage={(page, size) => setGroupPositions((previous) => ({ ...previous, [key]: { page, size } }))}
                  />
                )}
              </article>
            );
          })}
          <FindingPager
            label="task groups"
            page={groupPage}
            size={groupSize}
            total={groups.data?.total || 0}
            busy={groups.busy}
            change={(page, size) => {
              setGroupPage(page);
              setGroupSize(size);
            }}
          />
        </section>
      ) : (
        <div className={view === "asset" ? "astra-finding-asset-layout" : ""}>
          {view === "asset" && (
            <aside aria-label="Finding assets">
              <h2>Assets</h2>
              <label>
                Find an asset
                <input
                  name="finding_asset_search"
                  value={assetSearch}
                  onChange={(event) => setAssetSearch(event.target.value)}
                />
              </label>
              <div>
                <button onClick={() => setExpandedAssets(new Set((tree.data?.nodes || []).map((node) => node.key)))}>
                  Expand assets
                </button>
                <button onClick={() => setExpandedAssets(new Set())}>Collapse assets</button>
              </div>
              <button
                aria-pressed={!scope}
                onClick={() => {
                  setScope(undefined);
                  setPage(1);
                }}
              >
                All assets · {tree.data?.finding_total || 0}
              </button>
              {tree.data?.truncated && (
                <p className="astra-form-note">
                  Asset tree truncated. Hidden layers: {(tree.data.dropped_kinds || []).join(", ")}. Parent counts
                  retain their findings.
                </p>
              )}
              <ul className="astra-finding-asset-tree">
                {findingAssetRows(tree.data?.nodes || [], expandedAssets, assetSearch).map(
                  ({ node, depth, children }) => (
                    <li key={node.key} style={{ paddingInlineStart: Math.min(depth, 12) * 12 }} title={node.label}>
                      {children > 0 && (
                        <button
                          aria-label={`Expand asset ${node.key}`}
                          aria-expanded={expandedAssets.has(node.key)}
                          onClick={() =>
                            setExpandedAssets((previous) => {
                              const next = new Set(previous);
                              if (next.has(node.key)) next.delete(node.key);
                              else next.add(node.key);
                              return next;
                            })
                          }
                        >
                          {expandedAssets.has(node.key) ? "−" : "+"}
                        </button>
                      )}
                      <button
                        aria-label={`Select asset ${node.key}`}
                        aria-pressed={scope === node.key}
                        onClick={() => {
                          setScope(node.key);
                          setPage(1);
                        }}
                      >
                        {node.label} · {node.total}
                      </button>
                    </li>
                  ),
                )}
              </ul>
            </aside>
          )}
          <section>
            <nav className="astra-finding-breadcrumb" aria-label="Finding asset path">
              {trail.map((node) => (
                <button
                  key={node.key}
                  title={node.label}
                  onClick={() => {
                    setScope(node.key);
                    setPage(1);
                  }}
                >
                  {node.label}
                </button>
              ))}
            </nav>
            {list.busy && !list.data ? (
              <p>Loading findings…</p>
            ) : (
              <FindingCollection
                items={list.data?.items || []}
                mode={view === "triage" ? "cards" : "table"}
                actions={actions}
                expanded={expanded}
                expand={(key) => setExpanded((previous) => (previous === key ? null : key))}
              />
            )}
            <FindingPager
              label="findings"
              page={page}
              size={size}
              total={list.data?.total || 0}
              busy={list.busy}
              change={(page, size) => {
                setPage(page);
                setSize(size);
              }}
            />
          </section>
        </div>
      )}
      {modal && (
        <EditorDialog
          eyebrow="Finding"
          title={
            modal.kind === "export"
              ? "Export findings"
              : `${{ edit: "Edit", delete: "Delete", retest: "Retest", deepen: "Deepen" }[modal.kind]} finding ${modal.finding.finding_id}`
          }
          busy={busy}
          close={() => setModal(undefined)}
        >
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void run(
                async () => {
                  if (modal.kind === "export") {
                    await consoleDownload(
                      findingExportPath(String(modal.draft.format), String(modal.draft.scope), activeFilters, selected),
                      "findings-export",
                    );
                    setModal(undefined);
                    return;
                  }
                  const endpoint = `${findingsPath}/${findingHandle(modal.finding)}`;
                  let outcome: string | undefined;
                  if (modal.kind === "edit")
                    await consoleRequest(endpoint, "PATCH", findingMetadataDocument(modal.draft));
                  if (modal.kind === "delete") {
                    await consoleRequest(endpoint, "DELETE");
                    setSelected((previous) => new Set([...previous].filter((id) => id !== modal.finding.finding_id)));
                    if (expanded === findingIdentity(modal.finding)) setExpanded(null);
                  }
                  if (modal.kind === "retest") {
                    const result = await consoleRequest<{ retest: FindingRetest; created: boolean }>(
                      `${endpoint}/retests`,
                      "POST",
                      findingFollowupDocument("retest", String(modal.draft.text || "")),
                    );
                    outcome = result.created
                      ? `Retest #${result.retest.id} started in its own session`
                      : "A retest already exists; its session remains available.";
                  }
                  if (modal.kind === "deepen") {
                    const result = await consoleRequest<FindingDeepenResponse>(
                      `${endpoint}/deepen`,
                      "POST",
                      findingFollowupDocument("deepen", String(modal.draft.text || "")),
                    );
                    outcome = `Follow-up intent #${result.intent_id}${result.queued ? " entered the task queue" : " created"}`;
                  }
                  setModal(undefined);
                  await refresh();
                  return outcome;
                },
                modal.kind === "export"
                  ? "Finding export downloaded"
                  : modal.kind === "retest" || modal.kind === "deepen"
                    ? "Finding follow-up saved; session and queue state refreshed"
                    : "Finding change saved",
              );
            }}
          >
            {modal.kind === "edit" && (
              <FormFields
                groups={findingMetadataForm}
                value={modal.draft}
                onChange={(draft) => setModal({ ...modal, draft })}
                disabled={busy}
              />
            )}
            {modal.kind === "delete" && (
              <p>
                This deletes the standalone finding and its originating finding node, including its graph links and
                evidence bindings.
              </p>
            )}
            {(modal.kind === "retest" || modal.kind === "deepen") && (
              <>
                <p>
                  {modal.kind === "retest"
                    ? "The retest runs in a separate session with the original evidence and constraints. Only successful confirmation of a fix may change the finding to Fixed."
                    : "Create a high-priority worker intent in the original task, retaining the finding lineage and asset anchors."}
                </p>
                <label>
                  {modal.kind === "retest" ? "Retest notes (optional)" : "Follow-up instructions"}
                  <textarea
                    name="finding_followup_text"
                    rows={6}
                    value={String(modal.draft.text || "")}
                    disabled={busy}
                    required={modal.kind === "deepen"}
                    onChange={(event) => setModal({ ...modal, draft: { text: event.target.value } })}
                  />
                </label>
              </>
            )}
            {modal.kind === "export" && (
              <>
                <p>
                  {selected.size} selected ·{" "}
                  {view === "grouped" ? groups.data?.finding_total || 0 : list.data?.total || 0} under the current
                  filter
                </p>
                <FormFields
                  groups={[
                    {
                      title: "Download",
                      fields: [
                        {
                          key: "format",
                          label: "Format",
                          options: [
                            ["md-single", "Combined Markdown"],
                            ["md-zip", "Markdown and evidence ZIP"],
                            ["csv", "CSV"],
                            ["json", "JSON"],
                          ],
                        },
                        {
                          key: "scope",
                          label: "Export scope",
                          options: [
                            ["selected", "Selected findings"],
                            ["filtered", "Current filter and asset subtree"],
                            ["all", "All findings"],
                          ],
                        },
                      ],
                    },
                  ]}
                  value={modal.draft}
                  onChange={(draft) => setModal({ ...modal, draft })}
                  disabled={busy}
                />
              </>
            )}
            {error && (
              <p className="astra-form-error" role="alert">
                {error}
              </p>
            )}
            <footer className="astra-form-actions">
              <button type="button" disabled={busy} onClick={() => setModal(undefined)}>
                Cancel
              </button>
              <button disabled={busy}>
                {
                  {
                    edit: "Save finding metadata",
                    delete: "Confirm finding deletion",
                    retest: "Start finding retest",
                    deepen: "Create finding follow-up",
                    export: "Download finding export",
                  }[modal.kind]
                }
              </button>
            </footer>
          </form>
        </EditorDialog>
      )}
    </div>
  );
}
