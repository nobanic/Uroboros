"use client";
import { useEffect, useMemo, useSyncExternalStore } from "react";

import { api } from "@/lib/api";

import { InferenceJournal } from "./inference-journal";

export function useInference() {
  const journal = useMemo(
    () =>
      new InferenceJournal({
        list: api.llmRecords,
        tasks: api.llmTasks,
        detail: api.llmRecordDetail,
        settings: api.settings,
        recording: (enabled) => api.setSettings({ llm_record: enabled }),
        remove: api.llmRecordsDeleteTask,
      }),
    [],
  );
  const state = useSyncExternalStore(journal.subscribe, journal.snapshot, journal.snapshot);
  useEffect(() => {
    journal.activate();
    void journal.start();
    return () => journal.dispose();
  }, [journal]);
  useEffect(() => {
    const timer = setTimeout(() => journal.setFilter({ session: state.sessionDraft.trim() }), 300);
    return () => clearTimeout(timer);
  }, [journal, state.sessionDraft]);
  return { journal, state };
}
