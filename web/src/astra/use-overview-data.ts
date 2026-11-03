"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { taskSnapshot } from "./task-document";

export function useOverviewResource<T>(id: string, read: () => Promise<T>, interval = 3000) {
  const port = useRef(read);
  port.current = read;
  const alive = useRef(false),
    revision = useRef(0),
    pending = useRef<number | undefined>(undefined),
    writing = useRef(false),
    mutation = useRef(false);
  const [data, setData] = useState<T>(),
    [readError, failRead] = useState(""),
    [writeError, failWrite] = useState(""),
    [busy, setBusy] = useState(false);
  const reload = useCallback(async (force = false) => {
    if (writing.current || (pending.current !== undefined && !force)) return;
    const request = ++revision.current;
    pending.current = request;
    try {
      const next = await port.current();
      if (alive.current && request === revision.current) {
        setData(next);
        failRead("");
      }
    } catch (error) {
      if (alive.current && request === revision.current)
        failRead(error instanceof Error ? error.message : String(error));
    } finally {
      if (pending.current === request) pending.current = undefined;
    }
  }, []);
  useEffect(() => {
    alive.current = true;
    void reload();
    const timer = interval ? window.setInterval(() => void reload(), interval) : undefined;
    return () => {
      alive.current = false;
      revision.current++;
      pending.current = undefined;
      window.clearInterval(timer);
    };
  }, [id, interval, reload]);
  const run = useCallback(
    async (write: () => Promise<unknown>, accepted?: (data: T) => T) => {
      if (mutation.current || !alive.current) return false;
      mutation.current = true;
      writing.current = true;
      revision.current++;
      pending.current = undefined;
      setBusy(true);
      failWrite("");
      let saved = false;
      try {
        await write();
        if (!alive.current) return false;
        saved = true;
        if (accepted) setData((previous) => (previous === undefined ? previous : accepted(previous)));
      } catch (error) {
        if (alive.current) failWrite(error instanceof Error ? error.message : String(error));
      } finally {
        writing.current = false;
        if (alive.current) {
          await reload(true);
          if (alive.current) setBusy(false);
        }
        mutation.current = false;
      }
      return saved && alive.current;
    },
    [reload],
  );
  return { data, error: writeError || readError, readError, writeError, reload, run, busy };
}

export function useOverviewData(id: string) {
  const main = useOverviewResource(id, async () => {
    const [task, stats, intents, findings] = await Promise.all([
      api.task(id),
      api.stats(id),
      api.intents(id),
      api.findings(id),
    ]);
    return { task: taskSnapshot(task, stats), stats, intents, findings };
  });
  const goals = useOverviewResource(id, async () => (await api.taskGoals(id)).goals);
  const constraints = useOverviewResource(id, async () => (await api.taskConstraints(id)).constraints);
  const scope = useOverviewResource(id, async () => (await api.taskScope(id)).scope ?? [], 0);
  const rules = useOverviewResource(id, () => api.taskInterceptRules(id), 0);
  const tokens = useOverviewResource(id, async () => (await api.tokensByModel(id)).models ?? []);
  const coverage = useOverviewResource(id, () => api.taskCoverage(id));
  return { main, goals, constraints, scope, rules, tokens, coverage };
}
