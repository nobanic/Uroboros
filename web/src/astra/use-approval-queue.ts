"use client";
import { useEffect, useMemo, useSyncExternalStore } from "react";

import { api } from "@/lib/api";

import { ApprovalJournal } from "./approval-journal";

export function useApprovalQueue(taskId?: string) {
  const journal = useMemo(
    () =>
      new ApprovalJournal(
        {
          history: (task, page, size, filter) =>
            task ? api.interceptTaskPage(task, page, size, filter) : api.interceptHistoryPage(page, size, filter),
          pending: () => api.interceptPending(),
          decide: (id, decision) => api.interceptDecide(id, decision),
        },
        taskId,
      ),
    [taskId],
  );
  const state = useSyncExternalStore(journal.subscribe, journal.snapshot, journal.snapshot);
  useEffect(() => {
    journal.activate();
    void journal.refresh();
    const timer = setInterval(() => void journal.refresh(), 5000);
    return () => {
      clearInterval(timer);
      journal.dispose();
    };
  }, [journal]);
  return { journal, state };
}
