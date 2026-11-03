"use client";

import { useEffect, useMemo, useSyncExternalStore } from "react";

import { toast } from "sonner";

import { SideQuestionJournal } from "@/astra/side-question-journal";
import { sseUrl } from "@/lib/api";
import { isBtwCommand, sideAPI } from "@/lib/side-questions";

// Preserve request identity even in an insecure IP/http browser context.
function newSideRequestID(): string {
  return (
    globalThis.crypto?.randomUUID?.() ?? `btw-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
  );
}

export function useSideQuestions(parent: string | null) {
  const journal = useMemo(
    () =>
      new SideQuestionJournal({
        ...sideAPI,
        requestID: newSideRequestID,
        isCommand: isBtwCommand,
        notify: (message) => toast.error(message),
      }),
    [],
  );
  const state = useSyncExternalStore(journal.subscribe, journal.snapshot, journal.snapshot);
  const current = state.parent === parent;
  const running = current ? state.items.find((item) => item.status === "running") : undefined;
  const runningID = running?.id;
  useEffect(() => {
    journal.connect(parent);
    return () => journal.disconnect();
  }, [journal, parent]);
  useEffect(() => {
    if (!state.open || !parent) return;
    void journal.load();
    const timer = setInterval(() => void journal.load(), 2000);
    return () => clearInterval(timer);
  }, [journal, parent, state.open]);
  // A clear attempt invalidates old callbacks and reconnects even after failure.
  // biome-ignore lint/correctness/useExhaustiveDependencies: streamEpoch intentionally forces EventSource replacement.
  useEffect(() => {
    if (!runningID) return;
    const version = journal.version();
    const stream = new EventSource(sseUrl(`/api/side-questions/${runningID}/events`));
    stream.addEventListener("snapshot", (event) => {
      if (journal.receive(version, runningID, (event as MessageEvent).data)) stream.close();
    });
    stream.addEventListener("cleared", () => {
      stream.close();
      journal.cleared(version);
    });
    return () => stream.close();
  }, [journal, runningID, state.streamEpoch]);
  return {
    open: state.open,
    setOpen: journal.setOpen,
    items: current ? state.items : [],
    snapshot: current ? state.snapshot : null,
    draft: current ? state.draft : "",
    setDraft: journal.setDraft,
    busy: current && state.busy,
    loading: !current || state.loading,
    error: current ? state.error || state.loadError : "",
    running,
    nextCursor: current ? state.nextCursor : 0,
    load: journal.load,
    ask: journal.ask,
    handleCommand: journal.handleCommand,
    clear: journal.clear,
    stop: journal.stop,
    enabled: !!parent,
  };
}

export type SideQuestions = ReturnType<typeof useSideQuestions>;
