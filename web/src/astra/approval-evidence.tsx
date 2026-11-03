"use client";
import { useRef, useState } from "react";

import { toast } from "sonner";

import { UroIcon } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import type { InterceptApprovalRow, InterceptAudit } from "@/lib/types";

import {
  type ApprovalDecision,
  approvalAction,
  approvalExecution,
  approvalLocation,
  approvalOrigin,
  approvalReason,
  approvalReview,
  approvalSource,
  approvalStatus,
  approvalTime,
} from "./approval-document";
import { useApprovalEvidence } from "./use-approval-evidence";
import "./approvals.css";

export function ApprovalStatus({ status }: { status: string }) {
  return (
    <span className="astra-approval-status" data-status={status}>
      {approvalStatus[status] ?? status}
    </span>
  );
}

export function ApprovalSourceLink({ row }: { row: InterceptApprovalRow }) {
  const [busy, setBusy] = useState(false);
  const lock = useRef(false);
  const href = approvalLocation(row),
    label = approvalOrigin(row);
  if (!href) return <span>{label}</span>;
  return (
    <a
      href={href}
      aria-busy={busy}
      aria-label={`Locate the source of approval #${row.id}: ${label}`}
      onClick={async (event) => {
        event.stopPropagation();
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
        event.preventDefault();
        if (lock.current) return;
        lock.current = true;
        setBusy(true);
        try {
          await api.interceptExecution(row.id, row.conversation_id ?? undefined);
          window.location.assign(href);
        } catch (error) {
          toast.error(error instanceof Error ? error.message : "Could not locate the corresponding execution");
          lock.current = false;
          setBusy(false);
        }
      }}
    >
      {label}
      <UroIcon name="arrow-up-right" />
    </a>
  );
}

function EvidenceText({ label, text, truncated }: { label: string; text: string; truncated?: boolean }) {
  return (
    <section className="astra-approval-code" aria-label={label}>
      <header>
        <h4>{label}</h4>
        {text && (
          <button
            type="button"
            className="astra-button"
            aria-label={`Copy ${label}`}
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(text);
                toast.success("Copied");
              } catch {
                toast.error("Copy failed; please select the content and copy manually");
              }
            }}
          >
            Copy
          </button>
        )}
      </header>
      <pre>{text || "Not recorded"}</pre>
      {truncated && <p className="astra-approval-note">Content truncated; the above is the saved excerpt.</p>}
    </section>
  );
}

function ConversationExcerpt({ audit }: { audit: InterceptAudit }) {
  const names: Record<string, string> = {
    user: "User message",
    assistant: "Agent message",
    text: "Agent message",
    tool_use: "Tool request",
    tool_result: "Tool output",
  };
  if (!audit.user_message && !audit.context?.length) return null;
  return (
    <details className="astra-approval-fold">
      <summary>
        {audit.model_input && audit.model_input.version >= 3
          ? "View conversation audit excerpt (not sent to model)"
          : "View conversation audit excerpt"}
      </summary>
      {audit.user_message && (
        <EvidenceText
          label="Conversation current-turn input (audit excerpt)"
          text={audit.user_message}
          truncated={audit.user_truncated}
        />
      )}
      <h4>Visible conversation context</h4>
      <p className="astra-approval-note">
        Conversation-record excerpt saved at {approvalTime(audit.captured_at)}. The content actually used by the model
        is authoritatively shown under "Model review input".
      </p>
      {audit.context_truncated && (
        <p className="astra-approval-note">Only the most recent context is saved; some content is truncated.</p>
      )}
      {audit.context?.length ? (
        audit.context.map((entry, index) => (
          <EvidenceText
            key={`${index}:${entry.kind}:${entry.tool_use_id}`}
            label={`${names[entry.kind] ?? entry.kind}${entry.tool ? ` · ${entry.tool}` : ""}${entry.is_error ? " · error" : ""}`}
            text={entry.text}
            truncated={entry.truncated}
          />
        ))
      ) : (
        <p>No associatable context recorded</p>
      )}
    </details>
  );
}

function AuditEvidence({ audit, row }: { audit: InterceptAudit; row: InterceptApprovalRow }) {
  const initial = audit.model_fallback
    ? "Model error fallback"
    : approvalSource(row) === "model"
      ? "Model initial decision"
      : "Rule initial decision";
  return (
    <div className="astra-approval-audit">
      {audit.model_input ? (
        <section aria-label="Model review context">
          <h3>Model review context</h3>
          <p className="astra-approval-note">
            Snapshot of the input actually sent to the review model. Background explains the current action; the verdict
            is based on the review policy.
          </p>
          {approvalReview(audit.model_input).map((block, index) =>
            block.notice ? (
              <p key={`${index}:${block.label}`} className="astra-approval-note">
                {block.text}
              </p>
            ) : (
              <EvidenceText key={`${index}:${block.label}`} {...block} />
            ),
          )}
          <details className="astra-approval-fold">
            <summary>View the full model review input JSON</summary>
            <EvidenceText label="Model review input" text={JSON.stringify(audit.model_input, null, 2)} />
          </details>
        </section>
      ) : (
        <p className="astra-approval-notice">
          {audit.model_input_digest
            ? "This historical record only stored a fingerprint of the review input, not the original input, so the context sent to the model at the time cannot be reconstructed. This does not mean there was no context during review; newly produced model verdicts keep an input snapshot."
            : "This record has no saved model review input; it may have been decided directly by a rule, errored before the model call, or been produced by an older version."}
        </p>
      )}
      <ConversationExcerpt audit={audit} />
      <EvidenceText
        label={`${initial}: ${approvalAction[audit.initial_action] ?? audit.initial_action}`}
        text={approvalReason(audit.initial_reason)}
      />
      <EvidenceText
        label="Execution output"
        text={
          audit.output ??
          (audit.execution_status === "not_executed" ? "The tool was not executed." : "No execution output yet")
        }
        truncated={audit.output_truncated}
      />
      {audit.correlation !== "exact" && (
        <p className="astra-approval-notice">
          {audit.correlation === "ambiguous"
            ? "There were concurrent calls with the same arguments, so the tool call cannot be uniquely associated; this record does not show a guessed execution result."
            : "No uniquely associatable tool-call ID was recorded."}
        </p>
      )}
      <dl className="astra-approval-fingerprints">
        <div>
          <dt>Tool call ID</dt>
          <dd>{audit.tool_use_id ? audit.tool_use_id : "Not recorded"}</dd>
        </div>
        <div>
          <dt>Arguments digest SHA-256</dt>
          <dd>{audit.input_digest}</dd>
        </div>
        <div>
          <dt>Review config fingerprint SHA-256</dt>
          <dd>{audit.config_digest ? audit.config_digest : "Not recorded"}</dd>
        </div>
        {audit.model_input_digest && (
          <div>
            <dt>Model review input SHA-256</dt>
            <dd>{audit.model_input_digest}</dd>
          </div>
        )}
        {audit.execution_ended_at && (
          <div>
            <dt>Result recorded at</dt>
            <dd>{approvalTime(audit.execution_ended_at)}</dd>
          </div>
        )}
      </dl>
    </div>
  );
}

export type ApprovalEvidenceProps = {
  row: InterceptApprovalRow;
  busy: boolean;
  decide: (id: number, decision: ApprovalDecision) => Promise<void>;
  revision: number;
  readOnly?: boolean;
  defaultExpanded?: boolean;
  onResolved?: (status: "allowed" | "denied" | "timeout") => void;
};

export function ApprovalEvidence({
  row,
  busy,
  decide,
  revision,
  readOnly = false,
  defaultExpanded = false,
  onResolved,
}: ApprovalEvidenceProps) {
  const { detail, error, retry } = useApprovalEvidence(row, revision, onResolved);
  const [expanded, setExpanded] = useState(defaultExpanded);
  const current = detail?.status === row.status ? detail : row;
  const audit = detail?.audit,
    command = typeof row.tool_input?.command === "string" ? row.tool_input.command : "";
  return (
    <div className="astra-approval-evidence approval-detail" data-approval-evidence={row.id}>
      <div className="astra-approval-verdict">
        <section>
          <EvidenceText
            label={`${current.agent_name || current.conv_agent_key || "Agent"} · Tool request`}
            text={JSON.stringify(row.tool_input ?? {}, null, 2)}
          />
          {command && (
            <details className="astra-approval-fold">
              <summary>View command content</summary>
              <EvidenceText label="Command content" text={command} />
            </details>
          )}
          <p className="astra-approval-note">
            Execution result: <strong>{detail ? approvalExecution(row, audit) : "Loading…"}</strong>
          </p>
        </section>
        <section className="astra-approval-ruling">
          <h3>{current.status === "pending" ? "Review status" : "Approval verdict"}</h3>
          <div className="astra-approval-labels">
            <ApprovalStatus status={current.status} />
            <span>
              {approvalSource(current) === "model"
                ? "Model decision"
                : current.rule_name || "Rule not recorded or deleted"}
            </span>
          </div>
          <p className="astra-approval-reason">{approvalReason(current.reason) || "No approval reason recorded"}</p>
          {audit?.decision_reason && <p>{audit.decision_reason}</p>}
          {audit?.effective_action && <p>Final action: {approvalAction[audit.effective_action]}</p>}
          <dl className="astra-approval-facts">
            <dt>Source</dt>
            <dd>
              <ApprovalSourceLink row={current} />
            </dd>
            <dt>Requested at</dt>
            <dd>{approvalTime(row.created_at)}</dd>
            <dt>Decided at</dt>
            <dd>{approvalTime(current.decided_at)}</dd>
            {audit?.rule_name && (
              <>
                <dt>Rule snapshot</dt>
                <dd>{audit.rule_name}</dd>
              </>
            )}
            {Boolean(audit?.profile_id) && (
              <>
                <dt>Approval model profile</dt>
                <dd>#{audit?.profile_id}</dd>
              </>
            )}
          </dl>
          {!readOnly && current.status === "pending" && (
            <div className="astra-approval-actions">
              <button
                type="button"
                className="astra-button"
                disabled={busy}
                onClick={() => void decide(row.id, "allowed")}
              >
                Allow
              </button>
              <button
                type="button"
                className="astra-button"
                data-danger
                disabled={busy}
                onClick={() => void decide(row.id, "denied")}
              >
                Deny
              </button>
            </div>
          )}
        </section>
      </div>
      {error && (
        <div role="alert" className="astra-approval-notice is-error">
          Failed to load details: {error}
          <button type="button" className="astra-button" onClick={retry}>
            Retry details
          </button>
        </div>
      )}
      {!detail && !error && (
        <p role="status" className="astra-approval-note">
          Loading approval evidence…
        </p>
      )}
      {detail && !audit && (
        <p className="astra-approval-notice">
          This record has no approval-detail snapshot, so the context, model initial decision, and execution output at
          the time cannot be reconstructed.
        </p>
      )}
      {audit && (
        <section>
          <button
            type="button"
            className="astra-approval-disclosure"
            aria-expanded={expanded}
            onClick={() => setExpanded(!expanded)}
          >
            <UroIcon name="chevron" />
            {expanded ? "Collapse context" : "View context and execution result"}
          </button>
          {expanded && <AuditEvidence audit={audit} row={row} />}
        </section>
      )}
    </div>
  );
}
