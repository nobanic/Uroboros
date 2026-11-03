"use client";
import { useEffect, useMemo, useSyncExternalStore } from "react";

import { api } from "@/lib/api";

import { InterceptionJournal } from "./interception-journal";

export function useInterception() {
  const journal = useMemo(
    () =>
      new InterceptionJournal({
        rules: api.interceptRules,
        create: api.createInterceptRule,
        update: api.updateInterceptRule,
        remove: api.deleteInterceptRule,
        toggle: api.toggleInterceptRule,
        scope: api.interceptGetToolConfig,
        setScope: api.interceptSetToolConfig,
        tools: api.tools,
        judge: api.interceptGetJudgeConfig,
        setJudge: api.interceptSetJudgeConfig,
        profiles: api.llmProfiles,
        usage: api.interceptJudgeUsage,
      }),
    [],
  );
  const state = useSyncExternalStore(journal.subscribe, journal.snapshot, journal.snapshot);
  useEffect(() => {
    journal.activate();
    void journal.start();
    return () => journal.dispose();
  }, [journal]);
  return { journal, state };
}
