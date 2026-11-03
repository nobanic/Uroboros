"use client";

import { useEffect, useRef, useState } from "react";
import { api, sseUrl } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";
import type { Activity } from "@/lib/types";
import { type MainSegments, SessionJournal, type SessionPages, type SessionCache } from "./session-journal";

type Callbacks = {
  segments: (value: MainSegments) => void;
  audit: (value: Activity, active: () => boolean) => void;
  pinned: () => boolean;
};
export function useSessionJournal(taskId: string, callbacks: Callbacks) {
  const current = useRef(callbacks);
  current.current = callbacks;
  const journal = useRef<SessionJournal | null>(null);
  const [snapshot, update] = useState<{ task: string; pages: SessionPages; connected: boolean }>({
    task: taskId,
    pages: {},
    connected: false,
  });
  useEffect(() => {
    const controller = new SessionJournal({
      history: (key, before, limit) => api.activityHistory(taskId, key, before, limit),
      activity: (since, limit) => api.activity(taskId, { since, limit }),
      segments: () => api.mainSessions(taskId),
      stream: (since, receive, connected) => {
        const source = new EventSource(
          sseUrl(`/api/exploration/activity/stream?task=${encodeURIComponent(taskId)}&since=${since}`),
        );
        source.onopen = () => connected(true);
        source.onerror = () => connected(false);
        source.onmessage = (event) => {
          try {
            receive(JSON.parse(event.data));
          } catch {
            /* malformed frame */
          }
        };
        return source;
      },
      changed: (pages, connected) => update({ task: taskId, pages, connected }),
      resolved: (segments) => current.current.segments(segments),
      audit: (item) => current.current.audit(item, () => journal.current === controller),
      pinned: () => current.current.pinned(),
      mock: MOCK,
    });
    journal.current = controller;
    void controller.start();
    return () => {
      controller.dispose();
      if (journal.current === controller) journal.current = null;
    };
  }, [taskId]);
  const actions = useRef({
    activate: (key: string) => journal.current?.activate(key),
    load: (key: string) => journal.current?.load(key),
    earlier: (key: string) => journal.current?.earlier(key) ?? Promise.resolve(false),
    patch: (key: string, change: (cache: SessionCache) => SessionCache) => journal.current?.patch(key, change),
    seed: (key: string) => journal.current?.seed(key),
  }).current;
  return {
    ...actions,
    store: snapshot.task === taskId ? snapshot.pages : {},
    connected: snapshot.task === taskId && snapshot.connected,
  };
}
