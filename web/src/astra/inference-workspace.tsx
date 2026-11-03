"use client";
import { useCallback, useEffect, useRef, useState } from "react";

import Link from "next/link";

import { ArrowRight, BrainCircuit, ChevronLeft, ChevronRight, Clock3, Database, RefreshCw, Trash2 } from "lucide-react";
import { Switch as SwitchPrimitive } from "radix-ui";

import { consoleModuleForPath } from "@/components/uroboros-module-workspace";

import {
  InferenceDelete,
  InferenceError,
  InferencePayloadPane,
  InferenceWrap,
  type InferenceView,
} from "./inference-controls";
import {
  inferenceLatency,
  inferencePage,
  inferencePageSizes,
  inferencePayload,
  inferenceTaskOptions,
  inferenceTime,
  inferenceTokens,
} from "./inference-document";
import { useInference } from "./use-inference";
import "./inference-workspace.css";

const context = consoleModuleForPath("/function/llm-records");
function InferenceRecords({ journal, state }: InferenceView) {
  const metrics = inferencePage(state.rows, state.total, state.filter.page, state.filter.size);
  return (
    <section className="astra-inference-stream" aria-label="Recorded model calls">
      <header>
        <div>
          <span>Returned page</span>
          <strong>{state.rows.length} calls</strong>
        </div>
        <dl>
          <div>
            <dt>Failed</dt>
            <dd data-error={metrics.failedCalls > 0}>{metrics.failedCalls}</dd>
          </div>
          <div>
            <dt>Avg latency</dt>
            <dd>{inferenceLatency(metrics.averageLatency)}</dd>
          </div>
          <div>
            <dt>Tokens</dt>
            <dd>{inferenceTokens(metrics.pageTokens)}</dd>
          </div>
        </dl>
      </header>
      <div
        className="astra-inference-records"
        role="listbox"
        aria-label="Recorded calls"
        aria-busy={state.reads.list.loading}
        onKeyDown={(event) => {
          if (
            event.ctrlKey ||
            event.metaKey ||
            event.altKey ||
            !["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)
          )
            return;
          const current = Math.max(
            0,
            state.rows.findIndex((row) => row.id === state.selectedID),
          );
          let index =
            event.key === "ArrowDown" ? Math.min(state.rows.length - 1, current + 1) : Math.max(0, current - 1);
          if (event.key === "Home") index = 0;
          if (event.key === "End") index = state.rows.length - 1;
          const row = state.rows[index];
          if (row) {
            event.preventDefault();
            event.stopPropagation();
            journal.select(row.id);
            event.currentTarget.querySelector<HTMLButtonElement>(`[data-inference-record="${row.id}"]`)?.focus();
          }
        }}
      >
        {state.rows.map((row) => (
          <button
            type="button"
            role="option"
            aria-selected={state.selectedID === row.id}
            tabIndex={state.selectedID === row.id ? 0 : -1}
            key={row.id}
            data-inference-record={row.id}
            data-error={row.status !== "ok"}
            onClick={() => journal.select(row.id)}
          >
            <span className="astra-inference-record-status">
              <BrainCircuit size={17} />
            </span>
            <span className="astra-inference-record-identity">
              <span>
                <strong>{row.model || "Unknown model"}</strong>
                <code>#{row.id}</code>
              </span>
              <small>
                {row.profile_name || "Default profile"} · {row.worker || "system"}
              </small>
              <code>{row.session_id || "No session identifier"}</code>
            </span>
            <span className="astra-inference-record-metrics">
              <span>
                <Clock3 size={11} />
                {inferenceLatency(row.latency_ms)}
              </span>
              <small>
                {inferenceTokens(row.input_tokens)} in / {inferenceTokens(row.output_tokens)} out
              </small>
              <time title={row.ts}>{inferenceTime(row.ts)}</time>
            </span>
          </button>
        ))}
        {!state.rows.length && (
          <div className="astra-inference-empty">
            <Database size={28} />
            <strong>
              {state.reads.list.loading
                ? "Loading recorded calls…"
                : state.reads.list.error
                  ? "Records could not be loaded"
                  : "No model calls match this view"}
            </strong>
            <p>Adjust the filters or enable full recording for future calls.</p>
          </div>
        )}
      </div>
      <footer>Page statistics cover the returned calls. Task counts and total come from the server.</footer>
    </section>
  );
}
function InferenceInspector({ journal, state }: InferenceView) {
  const [wrap, setWrap] = useState(true),
    selected = state.rows.find((row) => row.id === state.selectedID),
    payload = inferencePayload(state.detail, state.rawRequested);
  if (!selected)
    return (
      <aside className="astra-inference-inspector astra-inference-empty">
        <BrainCircuit size={30} />
        <strong>Select an inference call</strong>
        <p>Its recorded request and response will appear here.</p>
      </aside>
    );
  return (
    <aside className="astra-inference-inspector" aria-label="Recorded exchange">
      <header>
        <div>
          <span>Exchange #{selected.id}</span>
          <time title={selected.ts}>{inferenceTime(selected.ts)}</time>
        </div>
        <h2>{selected.model || "Unknown model"}</h2>
        <p>
          {selected.profile_name || "Default routing profile"} · {selected.worker || "system worker"}
        </p>
      </header>
      <dl className="astra-inference-facts">
        <div>
          <dt>Task</dt>
          <dd>{selected.task_id ? `#${selected.task_id}` : "Unassigned"}</dd>
        </div>
        <div>
          <dt>Latency</dt>
          <dd data-warning={selected.latency_ms > 30_000}>{inferenceLatency(selected.latency_ms)}</dd>
        </div>
        <div>
          <dt>Tokens in / out</dt>
          <dd>
            {inferenceTokens(selected.input_tokens)} / {inferenceTokens(selected.output_tokens)}
          </dd>
        </div>
        <div>
          <dt>Disposition</dt>
          <dd data-error={selected.status !== "ok"}>{selected.status === "ok" ? "Completed" : "Failed"}</dd>
        </div>
      </dl>
      {selected.error && <InferenceError text={selected.error} />}
      <div className="astra-inference-mode">
        <div>
          <strong>{payload.showRaw ? "Provider wire payload" : "Normalized exchange"}</strong>
          <small>
            {payload.showRaw
              ? "Full recorded tool schema and original provider response"
              : "Parsed messages; raw tool/schema detail may be absent"}
          </small>
        </div>
        <InferenceWrap wrap={wrap} change={() => setWrap(!wrap)} />
        <button
          type="button"
          disabled={!payload.hasRaw}
          title={
            payload.hasRaw
              ? "Switch between normalized and recorded provider payloads"
              : "This older record has no raw provider payload"
          }
          onClick={() => journal.requestRaw(!state.rawRequested)}
        >
          {payload.showRaw ? "Show parsed" : "Show raw"}
        </button>
      </div>
      <InferenceError text={state.reads.detail.error} retry={() => void journal.refreshDetail()} />
      <div className="astra-inference-payloads">
        <InferencePayloadPane
          key={`${selected.id}:${payload.showRaw}:request`}
          side="request"
          id={selected.id}
          text={payload.requestCopy}
          display={payload.requestDisplay}
          original={payload.showRaw ? (state.detail?.raw_request ?? "") : undefined}
          wrap={wrap}
          loading={state.reads.detail.loading}
        />
        <InferencePayloadPane
          key={`${selected.id}:${payload.showRaw}:response`}
          side="response"
          id={selected.id}
          text={payload.responseCopy}
          display={payload.responseDisplay}
          original={payload.showRaw ? (state.detail?.raw_response ?? "") : undefined}
          wrap={wrap}
          loading={state.reads.detail.loading}
        />
      </div>
      <p className="astra-inference-wire-note">
        Raw response text is shown without JSON or SSE rewriting. Original downloads keep recorded strings; clipboard
        fallbacks may normalize line endings.
      </p>
    </aside>
  );
}
export function InferenceWorkspace() {
  const { journal, state } = useInference(),
    [contextOpen, setContextOpen] = useState(false),
    search = useRef<HTMLInputElement>(null);
  const busy = Boolean(state.writing),
    metrics = inferencePage(state.rows, state.total, state.filter.page, state.filter.size),
    tasks = inferenceTaskOptions(state.tasks, state.filter.task);
  const toggleContext = useCallback(
    () =>
      setContextOpen((open) => {
        localStorage.setItem("uroboros-module-rail", open ? "closed" : "open");
        return !open;
      }),
    [],
  );
  useEffect(() => {
    setContextOpen(localStorage.getItem("uroboros-module-rail") === "open");
  }, []);
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (
        state.confirmTask !== null ||
        event.ctrlKey ||
        event.metaKey ||
        event.altKey ||
        (event.target instanceof HTMLElement &&
          (event.target.matches("input,textarea,select,pre,[role=textbox]") || event.target.isContentEditable))
      )
        return;
      if (event.key === "/") {
        event.preventDefault();
        search.current?.focus();
      }
      if (event.key === "]") {
        event.preventDefault();
        toggleContext();
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [state.confirmTask, toggleContext]);
  return (
    <main data-inference-workspace className="astra-inference">
      <header className="astra-inference-heading">
        <span className="astra-inference-heading-icon">
          <BrainCircuit size={25} />
        </span>
        <div>
          <span>Uroboros / model exchange history</span>
          <h1>Inference observatory</h1>
          <p>Inspect the model call, its tools and the response that actually arrived.</p>
        </div>
        <div className="astra-inference-recording">
          <label htmlFor="llm-rec-toggle">
            Full recording{" "}
            <small>{state.recording.loaded ? (state.recording.enabled ? "Enabled" : "Off") : "Not loaded"}</small>
          </label>
          <SwitchPrimitive.Root
            id="llm-rec-toggle"
            aria-label="Record LLM calls"
            checked={state.recording.enabled}
            disabled={busy || !state.recording.loaded}
            onCheckedChange={(enabled) => void journal.setRecording(enabled)}
          >
            <SwitchPrimitive.Thumb />
          </SwitchPrimitive.Root>
        </div>
      </header>
      <div className="astra-inference-contextbar">
        <button type="button" onClick={() => search.current?.focus()}>
          Focus filter <kbd>/</kbd>
        </button>
        <button type="button" aria-expanded={contextOpen} aria-controls="inference-context" onClick={toggleContext}>
          Inspection context <kbd>]</kbd>
        </button>
        <span>Trace → compare → inspect</span>
      </div>
      {contextOpen && context && (
        <section id="inference-context" className="astra-inference-context" aria-label="Inference inspection context">
          <p>{context.directive}</p>
          <ol>
            {context.workflow.map((step) => (
              <li key={step.label}>
                <strong>{step.label}</strong>
                {step.detail}
              </li>
            ))}
          </ol>
          <nav>
            {context.related.map((link) => (
              <Link href={link.href} key={link.href}>
                {link.label}
                <ArrowRight size={12} />
              </Link>
            ))}
          </nav>
        </section>
      )}
      <p className="astra-inference-recording-note">
        Full recording controls future request/response storage. Token metering runs independently.
      </p>
      <InferenceError text={state.reads.settings.error} retry={() => void journal.refreshRecording()} />
      <InferenceError text={state.recording.error} />
      {state.notice && (
        <p role="status" className="astra-inference-notice">
          {state.notice}
        </p>
      )}
      <section className="astra-inference-board">
        <div data-inference-filters className="astra-inference-filters">
          <label>
            <span>Session</span>
            <input
              ref={search}
              aria-label="Filter recorded session"
              placeholder="Search Session ID..."
              value={state.sessionDraft}
              onChange={(event) => journal.draftSession(event.target.value)}
            />
          </label>
          <label>
            <span>Model</span>
            <input
              aria-label="Filter recorded model"
              placeholder="Model"
              value={state.filter.model}
              onChange={(event) => journal.setFilter({ model: event.target.value })}
            />
          </label>
          <label>
            <span>Recorded task</span>
            <select
              aria-label="Recorded task"
              value={state.filter.task}
              onChange={(event) => journal.setFilter({ task: event.target.value })}
            >
              <option value="">All tasks</option>
              {tasks.map((task) => (
                <option value={task.value} key={task.value}>
                  {task.label}
                </option>
              ))}
            </select>
          </label>
          <label>
            <span>Page size</span>
            <select
              aria-label="Record page size"
              value={state.filter.size}
              onChange={(event) => journal.setFilter({ size: Number(event.target.value) })}
            >
              {inferencePageSizes.map((size) => (
                <option value={size} key={size}>
                  {size} / page
                </option>
              ))}
            </select>
          </label>
          <div>
            <button
              type="button"
              disabled={state.reads.list.loading}
              onClick={() => void journal.refresh()}
              aria-label="Refresh recorded calls"
            >
              <RefreshCw size={14} />
            </button>
            <button
              type="button"
              className="is-danger"
              data-inference-delete-trigger
              disabled={!state.filter.task || busy}
              onClick={journal.openDelete}
            >
              <Trash2 size={14} />
              Delete task records
            </button>
          </div>
        </div>
        <InferenceError
          text={state.reads.list.error ? `${state.reads.list.error}. Previous results remain visible.` : ""}
          retry={() => void journal.refresh()}
        />
        <InferenceError text={state.reads.tasks.error} retry={() => void journal.refreshTasks()} />
        <div className="astra-inference-pagination">
          <span>{state.reads.list.loading ? "Reading recorded calls…" : "Saved calls"}</span>
          <output>
            {metrics.rangeStart}–{metrics.rangeEnd} / {state.total}
          </output>
          <button
            type="button"
            aria-label="Previous record page"
            disabled={state.filter.page <= 0 || state.reads.list.loading}
            onClick={() => journal.setFilter({ page: state.filter.page - 1 })}
          >
            <ChevronLeft size={15} />
          </button>
          <span>
            {state.filter.page + 1} / {metrics.totalPages}
          </span>
          <button
            type="button"
            aria-label="Next record page"
            disabled={state.filter.page + 1 >= metrics.totalPages || state.reads.list.loading}
            onClick={() => journal.setFilter({ page: state.filter.page + 1 })}
          >
            <ChevronRight size={15} />
          </button>
        </div>
        <div className="astra-inference-layout">
          <InferenceRecords journal={journal} state={state} />
          <InferenceInspector journal={journal} state={state} />
        </div>
      </section>
      <InferenceDelete journal={journal} state={state} />
    </main>
  );
}
