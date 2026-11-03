"use client";
import { useEffect, useMemo, useState, useSyncExternalStore } from "react";

import { api } from "@/lib/api";
import type { ExploreKind } from "@/lib/types";

import {
  type BroadcastSelection,
  broadcastAtLatest,
  broadcastDocument,
  broadcastLastPage,
  broadcastWindow,
} from "./broadcast-document";
import { BroadcastJournal } from "./broadcast-journal";

export function useBroadcast(task: string) {
  const [selection, select] = useState<BroadcastSelection>({
    task,
    page: 1,
    size: 20,
    kinds: [],
    query: "",
    order: "desc",
    live: true,
  });
  const [queryInput, setQueryInput] = useState("");
  const [now, setNow] = useState(0);
  const [journal] = useState(
    () =>
      new BroadcastJournal((current) =>
        api.explorationNodes(current.task, {
          page: current.page,
          size: current.size,
          kinds: current.kinds,
          q: current.query,
          order: current.order,
        }),
      ),
  );
  const snapshot = useSyncExternalStore(journal.subscribe, journal.snapshot, journal.snapshot);
  const key = broadcastWindow(selection);
  const document = useMemo(
    () => (snapshot.document && snapshot.documentKey === key ? broadcastDocument(snapshot.document) : null),
    [snapshot.document, snapshot.documentKey, key],
  );

  useEffect(() => {
    const timer = setTimeout(
      () => select((current) => (current.query === queryInput ? current : { ...current, query: queryInput, page: 1 })),
      300,
    );
    return () => clearTimeout(timer);
  }, [queryInput]);
  useEffect(() => {
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), 30_000);
    return () => clearInterval(timer);
  }, []);
  useEffect(() => {
    journal.configure(selection);
    void journal.refresh();
    // Pausing still performs one immediate read, as the original timeline did.
    const timer = selection.live ? setInterval(journal.refresh, 8000) : null;
    return () => {
      if (timer !== null) clearInterval(timer);
      journal.suspend();
    };
  }, [journal, selection]);
  useEffect(() => {
    if (snapshot.readKey !== key) return;
    const last = broadcastLastPage(snapshot.total, selection.size);
    if (selection.page > last) select((current) => ({ ...current, page: last }));
  }, [snapshot.readKey, snapshot.total, key, selection.page, selection.size]);

  function toggleKind(kind: ExploreKind) {
    select((current) => ({
      ...current,
      page: 1,
      kinds: current.kinds.includes(kind) ? current.kinds.filter((item) => item !== kind) : [...current.kinds, kind],
    }));
  }
  const total = snapshot.readKey === key ? snapshot.total : 0;
  return {
    selection,
    select,
    snapshot,
    document,
    total,
    now,
    queryInput,
    setQueryInput,
    toggleKind,
    atLatest: broadcastAtLatest(selection),
    pageCount: broadcastLastPage(total, selection.size),
    retry: journal.refresh,
    latest: () => select((current) => ({ ...current, page: 1, order: "desc" })),
  };
}
