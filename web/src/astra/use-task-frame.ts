"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { LLMProfile, Task } from "@/lib/types";
import { taskSnapshot } from "./task-document";

export function useTaskFrame(id: string) {
  const [task, setTask] = useState<Task>(),
    [profiles, setProfiles] = useState<LLMProfile[]>([]),
    [pending, setPending] = useState(0),
    [error, fail] = useState(""),
    [notices, setNotices] = useState<string[]>([]);
  const active = useRef(false),
    revision = useRef(0),
    busy = useRef<number | undefined>(undefined);
  const load = useCallback(
    async (force = false) => {
      if (!id || (busy.current !== undefined && !force)) return;
      const request = ++revision.current;
      busy.current = request;
      const warnings: string[] = [];
      try {
        const [record, stats] = await Promise.all([
          api.task(id),
          api.stats(id).catch((error) => {
            warnings.push(`Live task metrics: ${(error as Error).message}`);
            return null;
          }),
        ]);
        if (active.current && request === revision.current) {
          setTask(taskSnapshot(record, stats));
          fail("");
          setNotices(warnings);
        }
      } catch (error) {
        if (active.current && request === revision.current) fail((error as Error).message);
      } finally {
        if (busy.current === request) busy.current = undefined;
      }
    },
    [id],
  );
  useEffect(() => {
    active.current = true;
    void load();
    let metadata = true;
    api
      .llmProfiles()
      .then((rows) => {
        if (metadata) setProfiles(rows);
      })
      .catch(() => {});
    let approvalBusy = false;
    async function approvals() {
      if (!id || approvalBusy) return;
      approvalBusy = true;
      try {
        const rows = await api.interceptTask(id);
        if (metadata) setPending(rows.filter((row) => row.status === "pending").length);
      } catch {
      } finally {
        approvalBusy = false;
      }
    }
    void approvals();
    const timer = window.setInterval(() => {
      void load();
      void approvals();
    }, 5000);
    return () => {
      active.current = false;
      metadata = false;
      revision.current++;
      busy.current = undefined;
      window.clearInterval(timer);
    };
  }, [id, load]);
  const updatePause = useCallback((paused: boolean) => {
    revision.current++;
    busy.current = undefined;
    setTask((previous) => (previous ? { ...previous, paused } : previous));
  }, []);
  return { task, profiles, pending, error, notices, load, updatePause };
}
