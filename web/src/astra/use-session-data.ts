"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { TaskNode } from "@/lib/types";

function useSessionRead<T>(taskId: string, read: () => Promise<T>, interval: number) {
  const port = useRef(read);
  port.current = read;
  const ticket = useRef(0),
    pending = useRef<number | undefined>(undefined),
    alive = useRef(false);
  const [data, setData] = useState<T>(),
    [error, fail] = useState("");
  const reload = useCallback(async (force = false) => {
    if (pending.current !== undefined && !force) return;
    const request = ++ticket.current;
    pending.current = request;
    try {
      const value = await port.current();
      if (alive.current && request === ticket.current) {
        setData(value);
        fail("");
      }
    } catch (error) {
      if (alive.current && request === ticket.current) fail(error instanceof Error ? error.message : String(error));
    } finally {
      if (pending.current === request) pending.current = undefined;
    }
  }, []);
  useEffect(() => {
    alive.current = true;
    void reload();
    const timer = window.setInterval(() => void reload(true), interval);
    return () => {
      alive.current = false;
      ticket.current++;
      pending.current = undefined;
      window.clearInterval(timer);
    };
  }, [taskId, interval, reload]);
  const replace = useCallback((value: T) => {
    ticket.current++;
    setData(value);
    fail("");
  }, []);
  const invalidate = useCallback(() => {
    ticket.current++;
  }, []);
  return { data, error, reload, replace, invalidate };
}

export function useSessionData(taskId: string) {
  const tokens = useSessionRead(taskId, () => api.tokenStats(taskId), 5000);
  const running = useSessionRead(taskId, () => api.chatStatus(taskId), 2000);
  const profiles = useSessionRead(taskId, () => api.taskLLMResolution(taskId), 10000);
  const approvals = useSessionRead(taskId, () => api.interceptTask(taskId), 5000);
  const assets = useSessionRead(taskId, () => api.taskIntentAssets(taskId), 5000);
  return { tokens, running, profiles, approvals, assets };
}

type WorkerPages = { head: TaskNode[]; tail: TaskNode[]; headMore: boolean; tailMore?: boolean };
export function useSessionWorkers(taskId: string) {
  const [pages, render] = useState<WorkerPages>({ head: [], tail: [], headMore: false });
  const current = useRef(pages),
    alive = useRef(false),
    revision = useRef(0),
    mutation = useRef(0),
    firstBusy = useRef<number | undefined>(undefined),
    pastBusy = useRef(false);
  const [olderBusy, setOlderBusy] = useState(false),
    [error, fail] = useState("");
  function commit(value: WorkerPages) {
    current.current = value;
    render(value);
  }
  const reload = useCallback(
    async (force = false) => {
      if (firstBusy.current !== undefined && !force) return;
      const request = ++revision.current;
      firstBusy.current = request;
      try {
        const page = await api.intentsPage(taskId, 0, 300);
        if (!alive.current || request !== revision.current) return;
        const previous = current.current,
          ids = new Set(page.items.map((row) => row.id)),
          floor = Math.min(...page.items.map((row) => Number(row.id)));
        const displaced = page.hasMore ? previous.head.filter((row) => !ids.has(row.id) && Number(row.id) < floor) : [];
        const tail = new Map(previous.tail.map((row) => [row.id, row]));
        for (const row of displaced) tail.set(row.id, row);
        commit({ ...previous, head: page.items, headMore: page.hasMore, tail: [...tail.values()] });
        fail("");
      } catch (error) {
        if (alive.current && request === revision.current) fail(error instanceof Error ? error.message : String(error));
      } finally {
        if (firstBusy.current === request) firstBusy.current = undefined;
      }
    },
    [taskId],
  );
  useEffect(() => {
    alive.current = true;
    void reload();
    const timer = window.setInterval(() => void reload(true), 5000);
    return () => {
      alive.current = false;
      revision.current++;
      mutation.current++;
      firstBusy.current = undefined;
      window.clearInterval(timer);
    };
  }, [reload]);
  const earlier = useCallback(async () => {
    if (pastBusy.current) return;
    const before = Math.min(...[...current.current.head, ...current.current.tail].map((row) => Number(row.id)));
    if (!Number.isFinite(before)) return;
    pastBusy.current = true;
    setOlderBusy(true);
    const transaction = mutation.current;
    try {
      const result = await api.intentsPage(taskId, before, 300);
      if (!alive.current) return;
      const next = current.current,
        seen = new Set([...next.head, ...next.tail].map((row) => row.id));
      // An accepted control invalidates stale snapshots; retry the older page.
      if (transaction !== mutation.current) return;
      commit({
        ...next,
        tail: [...next.tail, ...result.items.filter((row) => !seen.has(row.id))],
        tailMore: result.hasMore,
      });
      fail("");
    } catch (error) {
      if (alive.current) fail(error instanceof Error ? error.message : String(error));
    } finally {
      pastBusy.current = false;
      if (alive.current) setOlderBusy(false);
    }
  }, [taskId]);
  const patch = useCallback((id: string, state?: string, reason?: string) => {
    revision.current++;
    mutation.current++;
    const change = (rows: TaskNode[]) =>
      state
        ? rows.map((row) => (row.id === id ? { ...row, state, ...(reason ? { delete_reason: reason } : {}) } : row))
        : rows.filter((row) => row.id !== id);
    commit({ ...current.current, head: change(current.current.head), tail: change(current.current.tail) });
  }, []);
  const byId = new Map(pages.tail.map((row) => [row.id, row]));
  for (const row of pages.head) byId.set(row.id, row);
  return {
    nodes: [...byId.values()].sort((a, b) => Number(b.id) - Number(a.id)),
    hasMore: pages.tailMore ?? pages.headMore,
    earlier,
    olderBusy,
    error,
    reload,
    patch,
  };
}
