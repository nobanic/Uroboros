"use client";

import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import Link from "next/link";
import { BarChart3, Check, ChevronLeft, ChevronRight, Copy, Download, RefreshCw, Search, Terminal } from "lucide-react";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { copyText } from "@/lib/utils";
import { consoleModuleForPath } from "@/components/uroboros-module-workspace";
import {
  recordedInput,
  recordedPreview,
  recordedTime,
  toolLedgerFilterKey,
  toolLedgerPulse,
  toolLedgerStatistics,
  toolLedgerWindow,
  toolPageSizes,
  type ToolLedgerQuery,
  type ToolLedgerRow,
} from "./tool-ledger-document";
import { useToolLedger } from "./use-tool-ledger";
import "./tool-ledger.css";
const context = consoleModuleForPath("/function/commands");

function describe(query: Pick<ToolLedgerQuery, "task" | "q">) {
  return `${query.task ? `Task #${query.task}` : "All tasks"}${query.q ? ` · ${query.q}` : ""}`;
}
function ReadError({ text, retry }: { text: string; retry: () => void }) {
  return text ? (
    <div className="astra-tool-error" role="alert">
      <span>{text}</span>
      <button type="button" onClick={retry}>
        Try again
      </button>
    </div>
  ) : null;
}
function selectPayload(event: KeyboardEvent<HTMLPreElement>) {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "a") {
    event.preventDefault();
    const range = document.createRange();
    range.selectNodeContents(event.currentTarget);
    const selection = window.getSelection();
    selection?.removeAllRanges();
    selection?.addRange(range);
  }
}
function PayloadCopy({ text, label }: { text: string; label: string }) {
  const [status, setStatus] = useState<"idle" | "pending" | "copied" | "failed">("idle");
  const lifetime = useRef(false),
    timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    lifetime.current = true;
    return () => {
      lifetime.current = false;
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);
  const copy = async () => {
    if (timer.current) clearTimeout(timer.current);
    setStatus("pending");
    const copied = await copyText(text);
    if (!lifetime.current) return;
    setStatus(copied ? "copied" : "failed");
    timer.current = setTimeout(() => {
      if (lifetime.current) setStatus("idle");
    }, 2200);
  };
  return (
    <button
      type="button"
      aria-label={label}
      disabled={status === "pending"}
      onClick={() => void copy()}
      title={status === "failed" ? "Copy failed; select the payload to copy it manually" : label}
    >
      {status === "copied" ? <Check size={13} /> : <Copy size={13} />}{" "}
      {status === "copied" ? "Copied" : status === "failed" ? "Copy failed" : label}
    </button>
  );
}
function ExecutionInspector({ row, repeated }: { row: ToolLedgerRow; repeated: boolean }) {
  const record = row.record,
    input = recordedInput(record.command);
  return (
    <aside data-command-inspector className="astra-tool-inspector">
      <header>
        <div>
          <span>Execution #{record.id}</span>
          <time dateTime={record.created_at}>{recordedTime(record.created_at)}</time>
        </div>
        <strong>{record.tool || "Unknown tool"}</strong>
        <p>
          {record.is_error
            ? "An error was recorded. Inspect the output before deciding what follows."
            : "No error flag was recorded for this returned row."}
        </p>
      </header>
      <dl className="astra-tool-source">
        <div>
          <dt>Source</dt>
          <dd>#{record.exploration_id}</dd>
        </div>
        <div>
          <dt>Worker</dt>
          <dd>{record.worker || "Not reported"}</dd>
        </div>
        <div>
          <dt>Disposition</dt>
          <dd data-error={record.is_error}>{record.is_error ? "Failed" : "No error recorded"}</dd>
        </div>
      </dl>
      {repeated && (
        <p className="astra-tool-row-note">
          Multiple returned rows share this record ID. Each output remains available separately.
        </p>
      )}
      <section className="astra-tool-payload" aria-label="Recorded tool input">
        <header>
          <div>
            <span>01</span>
            <h2>Input</h2>
          </div>
          <div>
            <PayloadCopy text={input} label="Copy input" />
            <PayloadCopy text={record.command} label="Copy original JSON" />
          </div>
        </header>
        <pre
          // biome-ignore lint/a11y/noNoninteractiveTabindex: Read-only payload supports keyboard scrolling and scoped Ctrl/Cmd+A.
          tabIndex={0}
          data-command-payload="input"
          onKeyDown={selectPayload}
        >
          {input}
        </pre>
      </section>
      <section className="astra-tool-payload" data-error={record.is_error} aria-label="Recorded tool output">
        <header>
          <div>
            <span>02</span>
            <h2>Output</h2>
          </div>
          <PayloadCopy text={record.output || ""} label="Copy output" />
        </header>
        <pre
          // biome-ignore lint/a11y/noNoninteractiveTabindex: Read-only payload supports keyboard scrolling and scoped Ctrl/Cmd+A.
          tabIndex={0}
          data-command-payload="output"
          onKeyDown={selectPayload}
        >
          {record.output || "(empty output)"}
        </pre>
      </section>
      <footer>Recorded evidence · read only</footer>
    </aside>
  );
}

export function ToolLedgerWorkspace() {
  const contextPanel = useRef<HTMLDetailsElement>(null);
  const { journal, state } = useToolLedger(),
    search = useRef<HTMLInputElement>(null);
  const [exportError, setExportError] = useState("");
  const selected = state.rows.find((row) => row.key === state.selected),
    pulse = toolLedgerPulse(state.rows),
    statistics = toolLedgerStatistics(state.stats);
  const pageWindow = toolLedgerWindow(state.dataQuery ?? state.query, state.total, state.rows.length);
  const sourceChanged =
    state.dataQuery &&
    (state.dataQuery.task !== state.query.task ||
      state.dataQuery.q !== state.query.q ||
      state.dataQuery.page !== state.query.page ||
      state.dataQuery.size !== state.query.size);
  useEffect(() => {
    const handler = (event: globalThis.KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      if (
        (event.key === "/" || event.key === "]") &&
        !event.ctrlKey &&
        !event.metaKey &&
        !event.altKey &&
        !target?.closest("input,textarea,select,[contenteditable=true],[role=dialog]")
      ) {
        event.preventDefault();
        if (event.key === "/") search.current?.focus();
        else if (contextPanel.current) contextPanel.current.open = !contextPanel.current.open;
      }
    };
    document.addEventListener("keydown", handler);
    return () => document.removeEventListener("keydown", handler);
  }, []);
  const exportPage = () => {
    setExportError("");
    try {
      const blob = new Blob(
        [
          JSON.stringify(
            { query: state.dataQuery, total: state.total, commands: state.rows.map((row) => row.record) },
            null,
            2,
          ),
        ],
        { type: "application/json" },
      );
      const url = URL.createObjectURL(blob),
        link = document.createElement("a");
      link.href = url;
      link.download = "uroboros-tool-records.json";
      link.click();
      setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (error) {
      setExportError(`Export failed: ${error instanceof Error ? error.message : String(error)}`);
    }
  };
  return (
    <div data-command-workspace className="astra-model-workspace astra-tool-ledger">
      <header className="astra-tool-heading">
        <div>
          <span>Uroboros / recorded execution</span>
          <h1>Tool executions</h1>
          <p>Follow the recorded input, inspect the returned output and keep the source in view.</p>
        </div>
        <div>
          <span>
            <Terminal size={14} /> Read-only ledger
          </span>
          <strong>
            {state.total}
            <small>recorded calls</small>
          </strong>
        </div>
      </header>
      {context && (
        <details ref={contextPanel} className="astra-tool-context">
          <summary>
            Inspection context <kbd>]</kbd>
          </summary>
          <p>{context.directive}</p>
          <ol>
            {context.workflow.map((step) => (
              <li key={step.label}>
                <strong>{step.label}</strong>
                <span>{step.detail}</span>
              </li>
            ))}
          </ol>
          <nav>
            {context.related.map((link) => (
              <Link key={link.href} href={link.href}>
                {link.label}
              </Link>
            ))}
          </nav>
        </details>
      )}
      <div className="astra-tool-controls" data-command-filters>
        <label className="astra-tool-search">
          <Search size={15} />
          <span className="sr-only">Search recorded tools and input</span>
          <input
            ref={search}
            aria-label="Filter recorded executions"
            placeholder="Search tool / arguments..."
            value={state.search}
            onChange={(event) => journal.search(event.target.value)}
          />
          <kbd>/</kbd>
        </label>
        <label>
          Task ID
          <input
            aria-label="Filter execution exploration"
            placeholder="Task ID"
            value={state.query.task}
            onChange={(event) => journal.task(event.target.value)}
          />
        </label>
        <label>
          Page size
          <select
            data-command-size
            value={state.query.size}
            onChange={(event) => journal.size(Number(event.target.value))}
          >
            {toolPageSizes.map((size) => (
              <option key={size} value={size}>
                {size} / page
              </option>
            ))}
          </select>
        </label>
        <button type="button" onClick={() => journal.statistics(true)}>
          <BarChart3 size={15} /> Stats
        </button>
        <button
          type="button"
          aria-label="Refresh recorded executions"
          disabled={state.loading}
          onClick={() => void journal.refresh()}
        >
          <RefreshCw size={15} className={state.loading ? "astra-tool-spin" : ""} /> Refresh
        </button>
        <button type="button" disabled={!state.rows.length} onClick={exportPage}>
          <Download size={15} /> Export page
        </button>
      </div>
      <ReadError text={state.error} retry={() => void journal.refresh()} />
      <ReadError text={exportError} retry={exportPage} />
      <div className="astra-tool-read-state" role="status">
        <span>
          {state.loading
            ? "Reading execution history…"
            : state.error
              ? "Last successful snapshot retained"
              : state.dataQuery
                ? describe(state.dataQuery)
                : "Recorded execution history"}
        </span>
        {sourceChanged && <span>Displayed rows belong to the previous snapshot.</span>}
        <span>
          {pageWindow.first}–{pageWindow.last} / {state.total}
        </span>
      </div>
      <div className="astra-tool-grid">
        <section className="astra-tool-ledger-list">
          <header>
            <div>
              <span>Execution pulse</span>
              <strong>{state.rows.length} calls on this page</strong>
            </div>
            <dl>
              <div>
                <dt>No error flag</dt>
                <dd>{pulse.withoutError}</dd>
              </div>
              <div data-error={pulse.failures > 0}>
                <dt>Failed</dt>
                <dd>{pulse.failures}</dd>
              </div>
              <div>
                <dt>Tools</dt>
                <dd>{pulse.tools}</dd>
              </div>
              <div>
                <dt>Workers</dt>
                <dd>{pulse.workers}</dd>
              </div>
            </dl>
          </header>
          <section className="astra-tool-events" aria-label="Recorded executions">
            {state.rows.map((row) => (
              <button
                type="button"
                data-command-record={row.record.id}
                data-command-row-key={row.key}
                data-selected={row.key === state.selected}
                data-error={row.record.is_error}
                key={row.key}
                onClick={() => journal.select(row.key)}
              >
                <span className="astra-tool-event-dot" />
                <span className="astra-tool-event-body">
                  <span>
                    <strong>{row.record.tool || "unknown tool"}</strong>
                    <small>{row.record.worker || "worker"}</small>
                  </span>
                  <code>{recordedPreview(row.record.command)}</code>
                  <span className="astra-tool-event-meta">
                    <time dateTime={row.record.created_at}>{recordedTime(row.record.created_at)}</time>
                    <span>
                      Task #{row.record.exploration_id} · #{row.record.id}
                    </span>
                  </span>
                </span>
                <ChevronRight size={15} />
              </button>
            ))}
            {!state.rows.length && (
              <div className="astra-tool-empty">
                <Terminal size={30} />
                <h2>
                  {state.loading
                    ? "Reading execution history…"
                    : state.error
                      ? "Execution history unavailable"
                      : "No tool execution records match this view."}
                </h2>
                <p>
                  {state.error
                    ? "Retry the read without changing the recorded data."
                    : "Recorded tool calls and their output appear here."}
                </p>
              </div>
            )}
          </section>
          <footer>
            <span>{state.rows.length} returned rows</span>
            <nav aria-label="Execution pagination">
              <button
                type="button"
                aria-label="Previous execution page"
                disabled={state.query.page <= 0 || state.loading}
                onClick={() => journal.page(state.query.page - 1)}
              >
                <ChevronLeft size={16} />
              </button>
              <span>
                {state.query.page + 1} / {pageWindow.pages}
              </span>
              <button
                type="button"
                aria-label="Next execution page"
                disabled={state.query.page + 1 >= pageWindow.pages || state.loading}
                onClick={() => journal.page(state.query.page + 1)}
              >
                <ChevronRight size={16} />
              </button>
            </nav>
          </footer>
        </section>
        {selected ? (
          <ExecutionInspector
            key={selected.key}
            row={selected}
            repeated={state.rows.filter((row) => row.record.id === selected.record.id).length > 1}
          />
        ) : (
          <aside className="astra-tool-empty astra-tool-inspector">
            <Terminal size={30} />
            <h2>Inspect an execution</h2>
            <p>Select a returned row to read its input and output.</p>
          </aside>
        )}
      </div>
      <Dialog open={state.statsOpen} onOpenChange={(open) => journal.statistics(open)}>
        <DialogContent className="astra-tool-statistics sm:max-w-lg">
          <DialogHeader>
            <DialogTitle>Tool-call statistics</DialogTitle>
            <DialogDescription>
              {state.statsFilter ? describe(state.statsFilter) : describe(state.query)}
              {state.stats.length > 0 && (
                <>
                  {" "}
                  · {state.stats.length} tools · {statistics.total} calls
                  {statistics.errors > 0 && <> · failed {statistics.errors}</>}
                </>
              )}
            </DialogDescription>
          </DialogHeader>
          <ReadError text={state.statsError} retry={() => void journal.refreshStats()} />
          {state.statsLoading && (
            <p role="status" className="astra-tool-stats-read">
              Reading recorded statistics…
            </p>
          )}
          {state.statsFilter && toolLedgerFilterKey(state.statsFilter) !== toolLedgerFilterKey(state.query) && (
            <p className="astra-tool-stats-read">Statistics belong to the previous filter snapshot.</p>
          )}
          {!state.statsLoading && !state.stats.length ? (
            <p className="astra-tool-empty-stats">No statistics yet</p>
          ) : (
            <div className="astra-tool-stat-rows">
              {state.stats.map((stat) => (
                <div key={stat.tool}>
                  <div>
                    <strong>{stat.tool || "-"}</strong>
                    <span>
                      {stat.errors > 0 ? `${stat.errors} failed · ` : ""}
                      {stat.total} calls
                    </span>
                  </div>
                  <div className="astra-tool-stat-track">
                    <span style={{ width: `${statistics.max > 0 ? (stat.total / statistics.max) * 100 : 0}%` }} />
                  </div>
                  <small>
                    {statistics.total > 0 ? ((stat.total / statistics.total) * 100).toFixed(1) : "0.0"}% of this filter
                  </small>
                </div>
              ))}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}
