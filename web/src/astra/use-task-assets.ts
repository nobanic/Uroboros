"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { Asset } from "@/lib/types";
import { taskAssetKey, taskAssetLastPage, type TaskAssetSelection } from "./task-asset-document";

type Snapshot = { assets: Asset[]; total: number; counts: Record<string, number>; key: string };
export function useTaskAssets(id: string) {
  const [selection, select] = useState<TaskAssetSelection>({ kind: "root_domain", page: 0, size: 50, query: "" });
  const [snapshot, render] = useState<Snapshot>(),
    [error, fail] = useState(""),
    [writeError, failWrite] = useState(""),
    [loading, showLoading] = useState(false),
    [busy, showBusy] = useState(false);
  const alive = useRef(false),
    ticket = useRef(0),
    pending = useRef<number | undefined>(undefined),
    writing = useRef(false),
    lock = useRef(false);
  const change = useCallback((next: TaskAssetSelection) => {
    if (lock.current) return;
    ticket.current++;
    pending.current = undefined;
    showLoading(true);
    fail("");
    select(next);
  }, []);
  const load = useCallback(
    async (force = false) => {
      if (writing.current || (pending.current !== undefined && !force)) return;
      const request = ++ticket.current;
      pending.current = request;
      showLoading(true);
      try {
        const dsl = selection.query.trim(),
          offset = selection.page * selection.size;
        const [page, counts] = await Promise.all([
          dsl
            ? api.searchTaskAssets(id, dsl, selection.kind, selection.size, offset)
            : api.taskAssets(id, selection.kind, selection.size, offset),
          api.assetCounts(id),
        ]);
        if (!alive.current || request !== ticket.current) return;
        const last = taskAssetLastPage(page.total, selection.size);
        if (selection.page > last) {
          select((previous) => ({ ...previous, page: last }));
          return;
        }
        render({ ...page, counts: counts ?? {}, key: taskAssetKey(selection) });
        fail("");
      } catch (error) {
        if (alive.current && request === ticket.current) fail(error instanceof Error ? error.message : String(error));
      } finally {
        if (pending.current === request) {
          pending.current = undefined;
          if (alive.current) showLoading(false);
        }
      }
    },
    [id, selection],
  );
  useEffect(() => {
    alive.current = true;
    const debounce = window.setTimeout(() => void load(), selection.query.trim() ? 400 : 0);
    const poll = window.setInterval(() => void load(), 10000);
    return () => {
      alive.current = false;
      ticket.current++;
      pending.current = undefined;
      window.clearTimeout(debounce);
      window.clearInterval(poll);
    };
  }, [load, selection.query]);
  const write = useCallback(
    async <T>(
      action: () => Promise<T>,
      accepted?: (snapshot: Snapshot, result: T) => Snapshot,
    ): Promise<{ ok: true; result: T } | { ok: false }> => {
      if (lock.current || !alive.current) return { ok: false };
      lock.current = true;
      writing.current = true;
      ticket.current++;
      pending.current = undefined;
      showBusy(true);
      failWrite("");
      let outcome: { ok: true; result: T } | { ok: false } = { ok: false };
      try {
        const result = await action();
        if (!alive.current) return { ok: false };
        if (accepted) render((previous) => (previous ? accepted(previous, result) : previous));
        outcome = { ok: true, result };
      } catch (error) {
        if (alive.current) failWrite(error instanceof Error ? error.message : String(error));
      } finally {
        writing.current = false;
        if (alive.current) {
          await load(true);
          if (alive.current) showBusy(false);
        }
        lock.current = false;
      }
      return alive.current ? outcome : { ok: false };
    },
    [load],
  );
  const stale = snapshot !== undefined && snapshot.key !== taskAssetKey(selection);
  return { selection, snapshot, error, writeError, loading, busy, stale, change, load, write };
}
