"use client";
import { useEffect, useMemo, useSyncExternalStore } from "react";

import { api } from "@/lib/api";

import { NotificationJournal } from "./notification-journal";

export function useNotifications() {
  const journal = useMemo(
    () =>
      new NotificationJournal({
        meta: api.notifyMeta,
        channels: api.notifyChannels,
        deliveries: api.notifyDeliveries,
        create: api.notifyCreateChannel,
        update: api.notifyUpdateChannel,
        remove: api.notifyDeleteChannel,
        test: api.notifyTestChannel,
        retry: api.notifyRetryDelivery,
        settings: api.setSettings,
      }),
    [],
  );
  const state = useSyncExternalStore(journal.subscribe, journal.snapshot, journal.snapshot);
  useEffect(() => {
    journal.activate();
    void journal.refresh();
    return () => journal.dispose();
  }, [journal]);
  return { journal, state };
}
