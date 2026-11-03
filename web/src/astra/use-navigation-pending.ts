"use client";
import { useEffect, useState } from "react";

import { api } from "@/lib/api";

export function useNavigationPending() {
  const [count, setCount] = useState(0);
  useEffect(() => {
    let mounted = true,
      latest = 0;
    const refresh = async () => {
      const ticket = ++latest;
      try {
        const rows = await api.interceptPending();
        if (mounted && ticket === latest) setCount(rows.length);
      } catch {
        /* Keep the last confirmed count; pending decisions remain server-authoritative. */
      }
    };
    void refresh();
    const timer = setInterval(() => void refresh(), 5000);
    return () => {
      mounted = false;
      latest++;
      clearInterval(timer);
    };
  }, []);
  return count;
}
