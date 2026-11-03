"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { consoleRequest } from "./console-api";

type Snapshot<T> = { key: string | null; data?: T; busy: boolean; error?: Error };

// Changing a query invalidates its previous responses. Polling never overlaps
// another read; explicit reloads can supersede an older in-flight snapshot.
export function useConsoleResource<T>(path: string | null, revision = 0, poll = 0) {
  const [snapshot, setSnapshot] = useState<Snapshot<T>>({ key: null, busy: false });
  const control = useRef<() => Promise<boolean>>(async () => true);
  useEffect(() => {
    if (!path) {
      control.current = async () => true;
      return;
    }
    let active = true,
      generation = 0,
      inFlight = false;
    const load = async () => {
      const request = ++generation;
      inFlight = true;
      setSnapshot((previous) => ({ key: path, data: previous.key === path ? previous.data : undefined, busy: true }));
      try {
        const data = await consoleRequest<T>(path);
        if (active && request === generation) setSnapshot({ key: path, data, busy: false });
        // Superseded successful reads are ignored by the UI, but are not
        // transport failures when a retest or filter refresh changes epochs.
        return true;
      } catch (error) {
        if (active && request === generation)
          setSnapshot((previous) => ({
            ...previous,
            key: path,
            error: error instanceof Error ? error : new Error(String(error)),
            busy: false,
          }));
        return false;
      } finally {
        if (request === generation) inFlight = false;
      }
    };
    control.current = load;
    void load();
    const timer =
      poll > 0
        ? window.setInterval(() => {
            if (!inFlight) void load();
          }, poll)
        : undefined;
    return () => {
      active = false;
      generation++;
      if (timer !== undefined) window.clearInterval(timer);
      if (control.current === load) control.current = async () => true;
    };
  }, [path, revision, poll]);
  const reload = useCallback(() => control.current(), []);
  return { ...(snapshot.key === path ? snapshot : { key: path, busy: Boolean(path) }), reload };
}
