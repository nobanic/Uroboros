"use client";
import { useEffect, useState, useSyncExternalStore } from "react";

import { api } from "@/lib/api";

import { SettingsJournal } from "./settings-journal";

export function useSettings() {
  const [journal] = useState(
    () =>
      new SettingsJournal({
        read: api.settings,
        write: api.setSettings,
        detect: api.detectPython,
        probe: api.testWebSearch,
      }),
  );
  const state = useSyncExternalStore(journal.subscribe, journal.snapshot, journal.snapshot);
  useEffect(() => {
    journal.activate();
    void journal.refresh();
    return () => journal.dispose();
  }, [journal]);
  return {
    journal,
    state,
    effective: state.server ? { ...state.server, ...state.optimistic } : null,
    disabled: !state.server || state.loading || !!state.busy,
  };
}
