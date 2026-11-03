"use client";

import { useCallback, useEffect, useId, useMemo, useState } from "react";
import { Markdown } from "@/components/markdown";
import { UroIcon } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import type { Activity } from "@/lib/types";
import { TranscriptApproval } from "./transcript-approval";
import {
  attachmentSize,
  type DetailPort,
  inlineTurnBody,
  laneTone,
  type MessageEntry,
  messageParts,
  messagePresentation,
  projectTranscript,
  type ToolEntry,
  toolParts,
  toolPresentation,
  turnBody,
} from "./transcript-document";
import { useTranscriptDetails } from "./use-transcript-details";
import { useTranscriptCallFocus, useTranscriptFocusSpace, useTranscriptProximity } from "./use-transcript-viewport";
import "./execution-transcript.css";

export type TranscriptProps = {
  activity: Activity[];
  live?: boolean;
  taskId?: string;
  chat?: boolean;
  fetchDetail?: DetailPort;
  focusedSeq?: number;
  sourceKey?: string;
};

function RecordedTime({ value }: { value: string }) {
  const date = new Date(value);
  if (!value || Number.isNaN(date.getTime())) return null;
  return (
    <time dateTime={date.toISOString()} title={date.toLocaleString("en-US")} suppressHydrationWarning>
      {date.toLocaleString("en-US", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
        hour12: false,
      })}
    </time>
  );
}
function Lane({ worker }: { worker: string }) {
  return (
    <span className="astra-replay-lane" data-tone={laneTone(worker)}>
      {worker}
    </span>
  );
}
function RetryDetail({ failed, loading, retry }: { failed: boolean; loading: boolean; retry: () => void }) {
  return failed ? (
    <div className="astra-replay-retry" role="status">
      <span>Some details could not be loaded.</span>
      <button type="button" className="astra-button" disabled={loading} onClick={retry}>
        Retry details
      </button>
    </div>
  ) : null;
}

function ToolRecord({
  entry,
  read,
  lanes,
  focused,
}: {
  entry: ToolEntry;
  read: DetailPort;
  lanes: boolean;
  focused: boolean;
}) {
  const [open, setOpen] = useState(focused),
    id = useId(),
    presentation = toolPresentation(entry);
  const detail = useTranscriptDetails(toolParts(entry), read, open);
  useEffect(() => {
    if (focused) setOpen(true);
  }, [focused]);
  const element = useTranscriptCallFocus(focused, open && detail.text !== null);
  return (
    <section
      ref={element}
      className="astra-replay-tool"
      data-focused={focused}
      data-state={presentation.state}
      data-call-seq={entry.call?.seq}
      aria-label={focused ? `Located tool call #${entry.call?.seq}` : undefined}
    >
      <button
        type="button"
        className="astra-replay-disclosure"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        aria-controls={id}
      >
        <UroIcon name="chevron" />
        <UroIcon name={presentation.name === "Bash" ? "commands" : "tools"} />
        {lanes && <Lane worker={entry.worker} />}
        <strong>{presentation.name}</strong>
        <code>{presentation.preview}</code>
        <span className="astra-replay-state">
          {presentation.state === "running" ? "Running…" : presentation.state === "failed" ? "✕ Failed" : "✓"}
        </span>
      </button>
      {open && (
        <div className="astra-replay-detail" id={id}>
          <pre aria-busy={detail.loading}>{detail.text ?? "Loading…"}</pre>
          <RetryDetail {...detail} />
        </div>
      )}
    </section>
  );
}
function MessageRecord({ entry, read, lanes }: { entry: MessageEntry; read: DetailPort; lanes: boolean }) {
  const [open, setOpen] = useState(false),
    id = useId(),
    presentation = messagePresentation(entry);
  const detail = useTranscriptDetails(messageParts(entry), read, open);
  return (
    <section className="astra-replay-message" data-error={presentation.error}>
      <button
        type="button"
        className="astra-replay-disclosure"
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        aria-controls={id}
      >
        <UroIcon name="chevron" />
        <UroIcon name={presentation.error ? "blocked" : presentation.reasoning ? "llm" : "chat"} />
        {lanes && <Lane worker={entry.worker} />}
        <span className="astra-replay-message-preview">{presentation.preview}</span>
        {presentation.reasoning && <small>· incl. reasoning</small>}
      </button>
      {open && (
        <div className="astra-replay-detail" id={id}>
          <pre aria-busy={detail.loading}>{detail.text ?? "Loading…"}</pre>
          <RetryDetail {...detail} />
        </div>
      )}
    </section>
  );
}
function FullRecord({ activity, read, turn }: { activity: Activity; read: DetailPort; turn?: boolean }) {
  const proximity = useTranscriptProximity();
  const inline = turn ? inlineTurnBody(activity) : null;
  const detail = useTranscriptDetails(
    [{ seq: activity.seq, label: "", fallback: activity.summary }],
    read,
    proximity.visible && inline === null,
  );
  const body = inline ?? (detail.text?.length ? detail.text : activity.summary),
    decoded = turnBody(body);
  return (
    <div
      ref={proximity.element}
      className={turn ? "astra-replay-turn" : "astra-replay-answer"}
      data-error={Boolean(activity.is_error)}
      data-turn-kind={activity.kind}
    >
      {turn ? (
        <>
          <div className="astra-replay-turn-body">
            {decoded.attachments.length > 0 && (
              <div className="astra-replay-files">
                {decoded.attachments.map((file) => (
                  <span key={file.path} title={file.path}>
                    <UroIcon name="workspace" />
                    <strong>{file.name}</strong>
                    <small>{attachmentSize(file.size)}</small>
                  </span>
                ))}
              </div>
            )}
            {decoded.text && <div className="astra-replay-bubble">{decoded.text}</div>}
            <RecordedTime value={activity.ts} />
          </div>
          <span className="astra-replay-avatar">
            <UroIcon name={activity.kind === "intent" ? "mission" : "user"} />
          </span>
        </>
      ) : (
        <>
          <div className="astra-replay-answer-body">
            {activity.is_error ? <span>{body}</span> : <Markdown text={body} />}
          </div>
          <RecordedTime value={activity.ts} />
        </>
      )}
      <RetryDetail {...detail} />
    </div>
  );
}

function TranscriptDocument({ activity, live, taskId, chat, fetchDetail, focusedSeq }: TranscriptProps) {
  const read = useCallback(
    (seq: number) =>
      fetchDetail ? fetchDetail(seq) : api.activityDetail(seq, taskId).then((reply) => reply.detail ?? ""),
    [fetchDetail, taskId],
  );
  const entries = useMemo(() => projectTranscript(activity, Boolean(chat)), [activity, chat]);
  const lanes = new Set(activity.map((record) => record.worker)).size > 1;
  const focus = useTranscriptFocusSpace(focusedSeq);
  return (
    <div
      ref={focus.element}
      className="astra-replay"
      style={focus.padding ? { paddingBlock: focus.padding } : undefined}
    >
      {entries.map((entry) => {
        const key = `${entry.kind}:${entry.seq}`;
        switch (entry.kind) {
          case "round":
            return (
              <div className="astra-replay-round" key={key}>
                <span />
                {entry.label}
                <span />
              </div>
            );
          case "tool":
            return (
              <ToolRecord
                key={key}
                entry={entry}
                read={read}
                lanes={lanes}
                focused={focusedSeq != null && entry.call?.seq === focusedSeq}
              />
            );
          case "message":
            return <MessageRecord key={key} entry={entry} read={read} lanes={lanes} />;
          case "approval":
            return <TranscriptApproval key={key} activity={entry.activity} read={read} />;
          default:
            return <FullRecord key={key} activity={entry.activity} read={read} turn={entry.kind === "turn"} />;
        }
      })}
      {live && (
        <div className="astra-replay-live" role="status">
          <span>
            <i />
            <i />
            <i />
          </span>
          Streaming live…
        </div>
      )}
    </div>
  );
}

export function ExecutionTranscript(props: TranscriptProps) {
  return <TranscriptDocument key={props.sourceKey ?? props.taskId ?? "activity"} {...props} />;
}
