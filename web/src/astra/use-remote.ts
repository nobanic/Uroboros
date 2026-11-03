"use client";

import { useEffect, useState } from "react";
import { request } from "./client";

type Load<T> = { key: string | null; data?: T; error?: Error; busy: boolean };

export function useRemote<T>(endpoint: string | null, revision = 0): Load<T> {
  const [state, setState] = useState<Load<T>>({ key: null, busy: false });
  useEffect(() => {
    if (!endpoint) return;
    const controller = new AbortController();
    setState((old) => ({ key: endpoint, data: old.key === endpoint ? old.data : undefined, busy: true }));
    request<T>(endpoint, { signal: controller.signal }).then(
      (data) => {
        if (!controller.signal.aborted) setState({ key: endpoint, data, busy: false });
      },
      (failure) => {
        if (!controller.signal.aborted)
          setState({
            key: endpoint,
            error: failure instanceof Error ? failure : new Error(String(failure)),
            busy: false,
          });
      },
    );
    return () => controller.abort();
  }, [endpoint, revision]);
  return state.key === endpoint ? state : { key: endpoint, busy: Boolean(endpoint) };
}
