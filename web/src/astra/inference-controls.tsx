"use client";
import { useEffect, useRef, useState } from "react";

import { Check, Copy, Download, WrapText } from "lucide-react";

import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";

import type { InferenceJournal } from "./inference-journal";

export type InferenceView = { journal: InferenceJournal; state: ReturnType<InferenceJournal["snapshot"]> };
export function InferenceError({ text, retry }: { text: string; retry?: () => void }) {
  return text ? (
    <div className="astra-inference-error" role="alert">
      <span>{text}</span>
      {retry && (
        <button type="button" onClick={retry}>
          Try again
        </button>
      )}
    </div>
  ) : null;
}

async function copyPayload(text: string, active: () => boolean) {
  if (!text || !active()) return false;
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    if (!active()) return false;
    const focus = document.activeElement as HTMLElement | null,
      textarea = document.createElement("textarea");
    textarea.value = text;
    textarea.style.position = "fixed";
    textarea.style.opacity = "0";
    document.body.appendChild(textarea);
    try {
      textarea.select();
      return document.execCommand("copy");
    } catch {
      return false;
    } finally {
      textarea.remove();
      if (focus?.isConnected) focus.focus();
    }
  }
}
export function InferencePayloadPane({
  side,
  text,
  display,
  original,
  id,
  loading,
  wrap,
}: {
  side: "request" | "response";
  text: string;
  display: string;
  original?: string;
  id: number;
  loading: boolean;
  wrap: boolean;
}) {
  const [copy, setCopy] = useState<"idle" | "pending" | "copied" | "failed">("idle"),
    [downloadError, setDownloadError] = useState("");
  const lifetime = useRef(false),
    timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(() => {
    lifetime.current = true;
    return () => {
      lifetime.current = false;
      if (timer.current) clearTimeout(timer.current);
    };
  }, []);
  async function copyText() {
    if (timer.current) clearTimeout(timer.current);
    setCopy("pending");
    const copied = await copyPayload(text, () => lifetime.current);
    if (!lifetime.current) return;
    setCopy(copied ? "copied" : "failed");
    if (copied) timer.current = setTimeout(() => setCopy("idle"), 1500);
  }
  function download() {
    if (!original) return;
    try {
      const url = URL.createObjectURL(new Blob([original], { type: "text/plain;charset=utf-8" })),
        anchor = document.createElement("a");
      anchor.href = url;
      anchor.download = `uroboros-llm-${id}-raw-${side}.txt`;
      try {
        document.body.appendChild(anchor);
        anchor.click();
      } finally {
        anchor.remove();
        setTimeout(() => URL.revokeObjectURL(url), 0);
      }
      setDownloadError("");
    } catch (error) {
      setDownloadError(error instanceof Error ? error.message : String(error));
    }
  }
  return (
    <section
      data-inference-payload={side}
      aria-label={`Recorded ${side}`}
      data-wrap={wrap}
      className="astra-inference-payload"
    >
      <header>
        <span>{side === "request" ? "01" : "02"}</span>
        <h3>{side === "request" ? "Request" : "Response"}</h3>
        <button
          type="button"
          disabled={!text || loading || copy === "pending"}
          onClick={() => void copyText()}
          title={copy === "copied" ? "Copied" : "Copy content"}
          aria-label={`Copy ${side}`}
        >
          {copy === "copied" ? <Check size={14} /> : <Copy size={14} />}
          {copy === "copied" ? "Copied" : "Copy"}
        </button>
        {original !== undefined && (
          <button
            type="button"
            disabled={!original || loading}
            onClick={download}
            title="Save the original recorded string, including its line endings"
            aria-label={`Download original raw ${side}`}
          >
            <Download size={14} />
            Raw
          </button>
        )}
      </header>
      {loading ? (
        <div className="astra-inference-payload-loading" role="status">
          Loading {side}…
        </div>
      ) : (
        <pre
          // biome-ignore lint/a11y/noNoninteractiveTabindex: Read-only payloads need keyboard scrolling and scoped Ctrl/Cmd+A without an editable textbox role.
          tabIndex={0}
          onKeyDown={(event) => {
            if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "a") {
              event.preventDefault();
              const range = document.createRange();
              range.selectNodeContents(event.currentTarget);
              const selection = getSelection();
              selection?.removeAllRanges();
              selection?.addRange(range);
            }
          }}
        >
          {display}
        </pre>
      )}
      {copy === "failed" && (
        <p className="astra-inference-copy-error" role="alert">
          Copy failed. Select the payload text and copy it manually.
        </p>
      )}
      {downloadError && <InferenceError text={downloadError} />}
    </section>
  );
}
export function InferenceWrap({ wrap, change }: { wrap: boolean; change: () => void }) {
  return (
    <button type="button" aria-pressed={wrap} onClick={change}>
      <WrapText size={14} />
      Wrap
    </button>
  );
}
export function InferenceDelete({ journal, state }: InferenceView) {
  const busy = Boolean(state.writing);
  return (
    <AlertDialog
      open={state.confirmTask !== null}
      onOpenChange={(open) => {
        if (!open) journal.closeDelete();
      }}
    >
      <AlertDialogContent
        className="astra-inference-delete"
        onEscapeKeyDown={(event) => {
          if (busy) event.preventDefault();
        }}
        onCloseAutoFocus={(event) => {
          event.preventDefault();
          document.querySelector<HTMLButtonElement>("[data-inference-delete-trigger]")?.focus();
        }}
      >
        <AlertDialogHeader>
          <AlertDialogTitle>Delete LLM records for task “{state.confirmTask}”?</AlertDialogTitle>
          <AlertDialogDescription>
            This permanently removes its recorded request and response bodies. Chat conversations, graph evidence and
            token usage remain unchanged.
          </AlertDialogDescription>
        </AlertDialogHeader>
        <InferenceError text={state.deleteError} />
        <footer>
          <AlertDialogCancel disabled={busy}>Cancel</AlertDialogCancel>
          <AlertDialogAction
            disabled={busy}
            className="is-danger"
            onClick={(event) => {
              event.preventDefault();
              void journal.deleteTask();
            }}
          >
            {state.writing === "delete" ? "Deleting…" : "Confirm delete"}
          </AlertDialogAction>
        </footer>
      </AlertDialogContent>
    </AlertDialog>
  );
}
