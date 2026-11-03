"use client";
import { useEffect, useMemo, useSyncExternalStore } from "react";

import { api } from "@/lib/api";

import { WorkspaceJournal, workspaceDirty } from "./workspace-journal";

export function useWorkspace() {
  const journal = useMemo(
    () =>
      new WorkspaceJournal({
        list: api.workspaceList,
        read: api.workspaceRead,
        write: api.workspaceWrite,
        mkdir: api.workspaceMkdir,
        remove: api.workspaceDelete,
        upload: api.workspaceUpload,
        download: api.workspaceDownload,
      }),
    [],
  );
  const state = useSyncExternalStore(journal.subscribe, journal.snapshot, journal.snapshot);
  const dirty = workspaceDirty(state.edit);
  useEffect(() => {
    journal.activate();
    void journal.list("");
    return () => journal.dispose();
  }, [journal]);
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (
        !state.folder &&
        !state.deletion &&
        (event.ctrlKey || event.metaKey) &&
        event.key.toLowerCase() === "s" &&
        dirty
      ) {
        event.preventDefault();
        void journal.save();
      }
    };
    const unload = (event: BeforeUnloadEvent) => {
      event.preventDefault();
      event.returnValue = "";
    };
    window.addEventListener("keydown", key);
    if (dirty) window.addEventListener("beforeunload", unload);
    return () => {
      window.removeEventListener("keydown", key);
      window.removeEventListener("beforeunload", unload);
    };
  }, [journal, dirty, state.folder, state.deletion]);
  return { journal, state, dirty };
}
