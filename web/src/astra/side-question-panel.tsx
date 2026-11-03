"use client";

import { useEffect, useRef, useState } from "react";

import { ArrowUp, History, MessageCircleQuestion, Square, Trash2, X } from "lucide-react";

import { Markdown } from "@/components/markdown";
import type { SideQuestions } from "@/hooks/use-side-questions";
import type { SideExchange } from "@/lib/side-questions";

export type SideComposerLayout = "inline" | "stacked";
const phases = {
  preparing: "Preparing context…",
  summarizing_history: "Summarizing earlier side questions…",
  compressing_snapshot: "Compressing the side-question context copy…",
  retrying: "Model context exceeded; reducing and retrying…",
  answering: "Answering…",
};
const statuses = {
  running: "Answering",
  completed: "Completed",
  failed: "Failed",
  cancelled: "Stopped",
  interrupted: "Interrupted",
};

function SideAnswer({ item }: { item: SideExchange }) {
  const context = item.context;
  return (
    <article className="astra-side-exchange" data-state={item.status}>
      <header>
        <span className="astra-side-ordinal">Q{item.ordinal}</span>
        <span className="astra-side-state" data-state={item.status}>
          <i />
          {statuses[item.status]}
        </span>
      </header>
      <p className="astra-side-question">{item.question}</p>
      <div className="astra-side-answer">
        {item.answer && <Markdown text={item.answer} />}
        {!item.answer && item.status === "running" && (
          <p role="status" className="astra-side-preparing">
            {phases[context?.phase ?? "answering"]}
          </p>
        )}
        {item.error && (
          <p role="alert" className="astra-side-error">
            {item.error}
          </p>
        )}
      </div>
      <footer>
        <span title={item.model.name}>{item.model.model}</span>
        <time dateTime={item.snapshot_at} title={new Date(item.snapshot_at).toLocaleString()}>
          Context {new Date(item.snapshot_at).toLocaleTimeString()}
        </time>
      </footer>
      {context?.estimated_input_tokens != null && (
        <details className="astra-side-context">
          <summary>Context details</summary>
          <p>
            Last {context.recent_exchanges} Q&amp;A exchanges verbatim
            {context.history_summarized && " · includes earlier-exchange summary"}
            {context.snapshot_summarized && " · uses main-context summary"}
          </p>
          <dl>
            <div>
              <dt>Estimated input</dt>
              <dd>{context.estimated_input_tokens.toLocaleString()} tokens</dd>
            </div>
            {context.input_budget != null && (
              <div>
                <dt>Input budget</dt>
                <dd>{context.input_budget.toLocaleString()} tokens</dd>
              </div>
            )}
            {context.output_tokens != null && (
              <div>
                <dt>Output</dt>
                <dd>{context.output_tokens.toLocaleString()} tokens</dd>
              </div>
            )}
          </dl>
        </details>
      )}
    </article>
  );
}

export function SideQuestionPanel({
  side,
  label,
  composerLayout,
}: {
  side: SideQuestions;
  label: string;
  composerLayout: SideComposerLayout;
}) {
  const [confirm, setConfirm] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null),
    viewport = useRef<HTMLDivElement>(null),
    pinned = useRef(true);
  const tail = side.items.at(-1);
  // biome-ignore lint/correctness/useExhaustiveDependencies: Only cumulative tail text moves a reader who remains pinned.
  useEffect(() => {
    if (pinned.current && viewport.current) viewport.current.scrollTop = viewport.current.scrollHeight;
  }, [tail?.answer, tail?.id]);
  useEffect(() => {
    if (confirm) {
      dialog.current?.showModal();
      dialog.current?.querySelector<HTMLButtonElement>("button")?.focus();
    } else dialog.current?.close();
  }, [confirm]);
  return (
    <section className="astra-side-panel" aria-label="Side question panel" data-composer={composerLayout}>
      <header className="astra-side-heading">
        <span className="astra-side-emblem">
          <MessageCircleQuestion size={19} />
        </span>
        <div>
          <small>
            Context conversation <kbd>/btw</kbd>
          </small>
          <h2>Side questions</h2>
          <p title={label}>{label}</p>
        </div>
        <button
          type="button"
          className="astra-side-icon"
          aria-label="Clear side-question history"
          title="Clear side-question history"
          disabled={!side.items.length || side.busy}
          onClick={() => setConfirm(true)}
        >
          <Trash2 size={15} />
        </button>
        <button
          type="button"
          className="astra-side-icon"
          aria-label="Close side-question panel"
          title="Close side-question panel"
          onClick={() => side.setOpen(false)}
        >
          <X size={17} />
        </button>
      </header>
      <div className="astra-side-snapshot">
        <i data-ready={side.snapshot?.available ?? false} />
        {side.snapshot ? (
          <div>
            <strong>{side.snapshot.model.model}</strong>
            <span>Context updated {new Date(side.snapshot.captured_at).toLocaleString()}</span>
          </div>
        ) : (
          <span>You can ask once the main Agent has run for the first time</span>
        )}
      </div>
      <div
        ref={viewport}
        className="astra-side-history"
        onScroll={(event) => {
          const area = event.currentTarget;
          pinned.current = area.scrollHeight - area.scrollTop - area.clientHeight < 80;
        }}
      >
        {side.nextCursor > 0 && (
          <button className="astra-side-earlier" type="button" onClick={() => void side.load(side.nextCursor)}>
            <History size={14} />
            Load earlier side questions
          </button>
        )}
        {side.loading && (
          <p className="astra-side-loading" role="status">
            Loading side questions…
          </p>
        )}
        {!side.loading && !side.items.length && (
          <div className="astra-side-empty">
            <MessageCircleQuestion size={28} />
            <h3>Ask a question anytime</h3>
            <p>Answered from the current Agent&apos;s context while the main task keeps running.</p>
          </div>
        )}
        {side.items.map((item) => (
          <SideAnswer key={item.id} item={item} />
        ))}
      </div>
      <div className="astra-side-compose">
        {(side.error || side.snapshot?.reason) && (
          <p role="alert" className="astra-side-error">
            {side.error || side.snapshot?.reason}
          </p>
        )}
        <div className="astra-side-input">
          <textarea
            rows={1}
            aria-label="Side question"
            placeholder="Ask about the current context…"
            value={side.draft}
            maxLength={4000}
            disabled={side.busy}
            onChange={(event) => side.setDraft(event.target.value)}
            onKeyDown={(event) => {
              if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) {
                event.preventDefault();
                void side.ask(side.draft);
              }
            }}
          />
          {side.running ? (
            <button
              type="button"
              className="astra-side-send astra-side-stop"
              aria-label="Stop side-question answer"
              onClick={() => void side.stop()}
            >
              <Square size={13} fill="currentColor" />
            </button>
          ) : (
            <button
              type="button"
              className="astra-side-send"
              aria-label="Send side question"
              disabled={side.busy || !side.draft.trim() || !side.snapshot?.available}
              onClick={() => void side.ask(side.draft)}
            >
              <ArrowUp size={18} />
            </button>
          )}
        </div>
        <div className="astra-side-compose-note">
          <span>Standalone Q&amp;A · no tool execution</span>
          <kbd>⇧ Enter</kbd>
        </div>
      </div>
      <dialog
        ref={dialog}
        role="alertdialog"
        className="astra-side-confirm"
        onCancel={() => setConfirm(false)}
        onClose={() => setConfirm(false)}
        aria-labelledby="astra-side-clear-title"
        aria-describedby="astra-side-clear-description"
      >
        <span className="astra-side-emblem">
          <Trash2 size={20} />
        </span>
        <h2 id="astra-side-clear-title">Clear side-question history?</h2>
        <p id="astra-side-clear-description">
          Delete the current Agent&apos;s side questions and stop any in-progress side-question answer. The main session
          and context snapshot are kept.
        </p>
        <div>
          <button type="button" onClick={() => setConfirm(false)}>
            Cancel
          </button>
          <button
            type="button"
            className="astra-side-confirm-delete"
            onClick={() => {
              setConfirm(false);
              void side.clear();
            }}
          >
            Clear history
          </button>
        </div>
      </dialog>
    </section>
  );
}
