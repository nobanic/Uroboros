"use client";
import { useId } from "react";

import { toast } from "sonner";

import { TablePagination } from "@/components/table-pagination";
import { UroIcon } from "@/components/uroboros-icons";
import type { InterceptApprovalFilter, InterceptApprovalRow } from "@/lib/types";

import {
  type ApprovalDecision,
  approvalOrigin,
  approvalQueue,
  approvalSource,
  approvalTime,
} from "./approval-document";
import { ApprovalEvidence, ApprovalStatus } from "./approval-evidence";
import { useApprovalQueue } from "./use-approval-queue";
import "./approvals.css";

export function ApprovalWorkspace({ taskId }: { taskId?: string }) {
  const { journal, state } = useApprovalQueue(taskId),
    id = useId();
  const queue = approvalQueue(state.rows, state.pending, taskId),
    selected = queue.find((row) => row.id === state.selected) ?? queue[0];
  const pending = queue.filter((row) => row.status === "pending" && state.pending.some((item) => item.id === row.id));
  const filtered = Boolean(state.filter.status || state.filter.decision_source);
  async function decide(id: number, decision: ApprovalDecision) {
    const result = await journal.decide(id, decision);
    if (result)
      result.error
        ? toast.error(result.error)
        : toast.success(decision === "allowed" ? "Execution allowed" : "Execution denied");
  }
  const metrics = [
    ["Awaiting operator", pending.length],
    ["Allowed", state.rows.filter((row) => row.status === "allowed").length],
    ["Denied", state.rows.filter((row) => row.status === "denied").length],
    ["Model decisions", state.rows.filter((row) => approvalSource(row) === "model").length],
  ] as const;
  return (
    <div className="astra-approvals" data-approval-workspace={taskId ?? "global"}>
      <header className="astra-approval-heading">
        <UroIcon name="approvals" />
        <div>
          <small>{taskId ? "Operation decision queue" : "Human decision boundary"}</small>
          <h2>{taskId ? "Interception approvals" : "Approval control room"}</h2>
          <p>Inspect the exact call context before releasing or blocking execution.</p>
        </div>
        <button
          type="button"
          className="astra-button"
          disabled={state.loading || state.refreshing}
          onClick={() => void journal.refresh(true)}
        >
          <UroIcon name="sync" />
          {state.refreshing ? "Refreshing…" : "Refresh queue"}
        </button>
      </header>
      <dl className="astra-approval-metrics">
        {metrics.map(([label, count]) => (
          <div key={label}>
            <dt>{label}</dt>
            <dd>{count}</dd>
          </div>
        ))}
      </dl>
      {state.historyError && (
        <p role="alert" className="astra-approval-notice is-error">
          Failed to load records: {state.historyError}. Refresh to retry.
        </p>
      )}
      {state.pendingError && (
        <p role="alert" className="astra-approval-notice is-error">
          Failed to load pending approvals: {state.pendingError}. Refresh to retry.
        </p>
      )}
      <div className="astra-approval-workbench">
        <section className="astra-approval-queue" aria-label="Decision queue">
          <header>
            <h3>Decision queue</h3>
            <span>{queue.length} records</span>
          </header>
          <div className="astra-approval-filters">
            <UroIcon name="filter" />
            <label htmlFor={`${id}-status`}>
              Status
              <select
                id={`${id}-status`}
                aria-label="Approval status"
                value={state.filter.status ?? "all"}
                onChange={(event) =>
                  journal.filter({
                    ...state.filter,
                    status:
                      event.target.value === "all"
                        ? undefined
                        : (event.target.value as InterceptApprovalFilter["status"]),
                  })
                }
              >
                <option value="all">All statuses</option>
                <option value="pending">Pending</option>
                <option value="allowed">Allowed</option>
                <option value="denied">Denied</option>
                <option value="timeout">Timed out</option>
              </select>
            </label>
            <label htmlFor={`${id}-source`}>
              Source
              <select
                id={`${id}-source`}
                aria-label="Decision source"
                value={state.filter.decision_source ?? "all"}
                onChange={(event) =>
                  journal.filter({
                    ...state.filter,
                    decision_source:
                      event.target.value === "all"
                        ? undefined
                        : (event.target.value as InterceptApprovalFilter["decision_source"]),
                  })
                }
              >
                <option value="all">All sources</option>
                <option value="model">Model</option>
                <option value="rule">Rule</option>
                <option value="unknown">Unknown</option>
              </select>
            </label>
            {filtered && (
              <button type="button" className="astra-button" onClick={() => journal.filter({})}>
                Clear
              </button>
            )}
          </div>
          <div className="astra-approval-queue-list">
            {queue.map((row) => (
              <QueueRequest
                key={row.id}
                row={row}
                selected={selected?.id === row.id}
                choose={() => journal.select(row.id)}
              />
            ))}
            {!queue.length && (
              <div className="astra-approval-empty" role="status">
                <UroIcon name="approvals" />
                <strong>
                  {state.loading
                    ? "Loading decisions…"
                    : filtered
                      ? "No records match this lens"
                      : "The decision queue is clear"}
                </strong>
                <p>
                  {state.loading
                    ? "Reading history and independent pending queue."
                    : filtered
                      ? "Adjust or clear the filters."
                      : "New human gates will appear here."}
                </p>
              </div>
            )}
          </div>
          {!state.loading && !state.historyError && (
            <TablePagination
              page={state.page}
              pageSize={state.size}
              total={state.total}
              onPageChange={journal.page}
              onPageSizeChange={journal.resize}
              pageSizeOptions={[10, 20, 50]}
            />
          )}
        </section>
        <section className="astra-approval-inspector" aria-label="Decision evidence">
          <header>
            <h3>Decision evidence</h3>
            {selected && <ApprovalStatus status={selected.status} />}
          </header>
          {selected ? (
            <>
              <dl className="astra-approval-selection">
                <div>
                  <dt>Request</dt>
                  <dd>
                    #{selected.id} · {selected.tool_name}
                  </dd>
                </div>
                <div>
                  <dt>Authority source</dt>
                  <dd>
                    {approvalSource(selected) === "model"
                      ? "Model decision"
                      : selected.rule_name || "Rule snapshot unavailable"}
                  </dd>
                </div>
                <div>
                  <dt>Origin</dt>
                  <dd>{approvalOrigin(selected)}</dd>
                </div>
              </dl>
              <ApprovalEvidence
                key={selected.id}
                row={selected}
                busy={state.writing}
                decide={decide}
                revision={state.revision}
                defaultExpanded={selected.status === "pending"}
              />
            </>
          ) : (
            <div className="astra-approval-empty">
              <UroIcon name="shield" />
              <strong>No decision selected</strong>
              <p>Choose a request to inspect its policy context and recorded outcome.</p>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}

function QueueRequest({ row, selected, choose }: { row: InterceptApprovalRow; selected: boolean; choose: () => void }) {
  const input =
    typeof row.tool_input?.command === "string" ? row.tool_input.command : JSON.stringify(row.tool_input ?? {});
  return (
    <button
      type="button"
      className="astra-approval-request"
      data-approval-request={row.id}
      aria-pressed={selected}
      onClick={choose}
    >
      <span>
        <ApprovalStatus status={row.status} />
        <small>
          #{row.id} · {approvalSource(row)}
        </small>
      </span>
      <strong>{row.tool_name}</strong>
      <code title={input}>{input}</code>
      <footer>
        <span>
          {approvalOrigin(row)} · {row.agent_name || row.conv_agent_key || "agent"}
        </span>
        <time>{approvalTime(row.created_at)}</time>
      </footer>
    </button>
  );
}
