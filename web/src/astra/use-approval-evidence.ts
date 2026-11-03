"use client";
import { useEffect, useRef, useState } from "react";

import { api } from "@/lib/api";
import type { InterceptApprovalRow, InterceptDetail } from "@/lib/types";

export function useApprovalEvidence(
  row: InterceptApprovalRow,
  revision: number,
  resolved?: (status: "allowed" | "denied" | "timeout") => void,
) {
  const [snapshot, setSnapshot] = useState<{ id: number; detail: InterceptDetail | null; error: string }>({
    id: row.id,
    detail: null,
    error: "",
  });
  const [retry, setRetry] = useState(0);
  const notify = useRef(resolved);
  notify.current = resolved;
  // biome-ignore lint/correctness/useExhaustiveDependencies: Status, revision and retry invalidate the same lazy evidence record.
  useEffect(() => {
    let alive = true,
      timer: ReturnType<typeof setTimeout> | undefined;
    async function read() {
      try {
        const detail = await api.interceptDetail(row.id);
        if (!alive) return;
        setSnapshot({ id: row.id, detail, error: "" });
        if (detail.status !== "pending") notify.current?.(detail.status);
        if (detail.status === "pending" || detail.audit?.execution_status === "awaiting_result")
          timer = setTimeout(read, 5000);
      } catch (error) {
        if (alive)
          setSnapshot((current) => ({
            id: row.id,
            detail: current.id === row.id ? current.detail : null,
            error: error instanceof Error ? error.message : "Failed to load details",
          }));
      }
    }
    void read();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [row.id, row.status, revision, retry]);
  return {
    detail: snapshot.id === row.id ? snapshot.detail : null,
    error: snapshot.id === row.id ? snapshot.error : "",
    retry: () => setRetry((value) => value + 1),
  };
}
