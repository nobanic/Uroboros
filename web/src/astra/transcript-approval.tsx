"use client";

import { useEffect, useRef, useState } from "react";
import { toast } from "sonner";
import { ApprovalDetail } from "@/components/approval-records";
import { UroIcon } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import type { Activity, InterceptPending } from "@/lib/types";
import { approvalReference, type DetailPort } from "./transcript-document";
import { useTranscriptDetails } from "./use-transcript-details";

type Decision = "allowed" | "denied" | "timeout";

export function TranscriptApproval({ activity, read }: { activity: Activity; read: DetailPort }) {
  const reference = approvalReference(activity.summary),
    inherited = Boolean(activity.inherited);
  const detail = useTranscriptDetails([{ seq: activity.seq, label: "", fallback: "" }], read, true);
  const [pending, setPending] = useState<InterceptPending | null>(null);
  const [decision, setDecision] = useState<Decision | null>(null);
  const [failure, setFailure] = useState("");
  const [revision, setRevision] = useState(0),
    [open, setOpen] = useState(false),
    [busy, setBusy] = useState(false);
  const alive = useRef(false),
    writing = useRef(false);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  // biome-ignore lint/correctness/useExhaustiveDependencies: Explicit retry reloads the same authoritative approval status.
  useEffect(() => {
    if (!reference.id) return;
    let current = true;
    void api
      .interceptGetOne(reference.id)
      .then((row) => {
        if (!current) return;
        setPending(row);
        setFailure("");
        if (row.status !== "pending") setDecision(row.status as Decision);
      })
      .catch((error) => {
        if (current) setFailure(error instanceof Error ? error.message : "Failed to load approval details");
      });
    return () => {
      current = false;
    };
  }, [reference.id, revision]);
  async function decide(next: "allowed" | "denied") {
    if (inherited || !reference.id || writing.current) return;
    writing.current = true;
    setBusy(true);
    try {
      await api.interceptDecide(reference.id, next);
      if (alive.current) {
        setDecision(next);
        toast.success(next === "allowed" ? "Execution allowed" : "Execution denied");
      }
    } catch (error) {
      if (alive.current) toast.error(error instanceof Error ? error.message : "Approval decision failed");
    } finally {
      writing.current = false;
      if (alive.current) setBusy(false);
    }
  }
  let preview = "";
  try {
    const payload = JSON.parse(detail.text ?? "null");
    if (payload?.input) preview = JSON.stringify(payload.input).slice(0, 200);
  } catch {
    /* An approval summary and current server status remain usable without detail JSON. */
  }
  const row = pending
    ? { ...pending, status: decision ?? pending.status, conv_title: "", conv_agent_key: "", rule_name: "" }
    : null;
  return (
    <article className="astra-replay-approval" data-approval={reference.id ?? undefined}>
      <header>
        <UroIcon name="approvals" />
        <div>
          <strong>
            Approval request <code>{reference.tool}</code>
          </strong>
          {reference.id && <span>#{reference.id}</span>}
          {preview && <p title={preview}>{preview}</p>}
        </div>
        {inherited ? (
          <span className="astra-replay-state">History · read-only</span>
        ) : decision ? (
          <span className="astra-replay-state" data-state={decision}>
            {decision === "allowed" ? "Allowed" : decision === "denied" ? "Denied" : "Timed out"}
          </span>
        ) : (
          <div className="astra-replay-approval-actions">
            <button
              type="button"
              className="astra-button"
              disabled={busy || !reference.id}
              onClick={() => void decide("allowed")}
            >
              Allow
            </button>
            <button
              type="button"
              className="astra-button"
              data-danger
              disabled={busy || !reference.id}
              onClick={() => void decide("denied")}
            >
              Deny
            </button>
          </div>
        )}
      </header>
      {reference.id && (
        <>
          <button type="button" className="astra-replay-disclosure" aria-expanded={open} onClick={() => setOpen(!open)}>
            <UroIcon name="chevron" />
            {open ? "Collapse approval details" : "Expand approval details"}
          </button>
          {open &&
            (row ? (
              <ApprovalDetail
                row={row}
                busy={busy}
                decide={(_id, next) => decide(next)}
                revision={revision}
                readOnly={inherited}
                defaultExpanded
                onResolved={setDecision}
              />
            ) : failure ? (
              <p role="alert">
                {failure}
                <button type="button" className="astra-button" onClick={() => setRevision((value) => value + 1)}>
                  Retry details
                </button>
              </p>
            ) : (
              <p role="status">Loading approval details…</p>
            ))}
        </>
      )}
    </article>
  );
}
