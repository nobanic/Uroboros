"use client";

import { useEffect, useRef, useSyncExternalStore } from "react";
import { api } from "@/lib/api";
import { ToolLedgerJournal } from "./tool-ledger-journal";

export function useToolLedger() {
  const instance = useRef<ToolLedgerJournal | null>(null);
  if (!instance.current)
    instance.current = new ToolLedgerJournal({
      list: (query) => api.commands(query),
      stats: (query) => api.commandStats(query),
    });
  const journal = instance.current;
  const state = useSyncExternalStore(journal.subscribe, journal.snapshot, journal.snapshot);
  useEffect(() => {
    journal.activate();
    void journal.refresh();
    return () => journal.dispose();
  }, [journal]);
  return { journal, state };
}
