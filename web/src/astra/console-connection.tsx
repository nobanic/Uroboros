"use client";

import { createContext, type ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";

import { UroIcon } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";

import { consoleConnectionView, type ConsoleConnectionObservation } from "./console-connection-view";

type ConnectionSession = { view: ReturnType<typeof consoleConnectionView>; retry: () => void };
const ConnectionSession = createContext<ConnectionSession | null>(null);

// Replaces the main frame's existing once-per-mount health read. Header and
// sidebar subscribe to the same result; only an explicit retry starts a new read.
export function ConsoleConnectionProvider({ children }: { children: ReactNode }) {
  const [observation, observe] = useState<ConsoleConnectionObservation>({ busy: true });
  const [revision, revise] = useState(0);
  const latest = useRef(0);
  useEffect(() => {
    let current = true;
    api.health().then(
      (data) => {
        if (current && revision === latest.current) observe({ busy: false, data });
      },
      (error: unknown) => {
        if (current && revision === latest.current) observe({ busy: false, error });
      },
    );
    return () => {
      current = false;
    };
  }, [revision]);
  const retry = useCallback(() => {
    if (observation.busy || MOCK) return;
    latest.current += 1;
    observe((previous) => ({ busy: true, data: previous.data }));
    revise(latest.current);
  }, [observation.busy]);
  const view = useMemo(() => consoleConnectionView(observation, MOCK), [observation]);
  const session = useMemo(() => ({ view, retry }), [view, retry]);
  return <ConnectionSession.Provider value={session}>{children}</ConnectionSession.Provider>;
}

export function useConsoleConnection() {
  const session = useContext(ConnectionSession);
  if (!session) throw new Error("ConsoleConnectionProvider is required for the console frame.");
  return session;
}

export function ConsoleConnectionBadge() {
  const { view, retry } = useConsoleConnection();
  const checking = view.kind === "checking";
  let action = "Retry backend connection";
  if (checking) action = "Checking backend connection";
  else if (view.kind === "connected") action = "Check backend connection";
  return (
    <button
      type="button"
      className="uro-live-state"
      data-connection={view.kind}
      onClick={retry}
      disabled={checking || MOCK}
      aria-label={MOCK ? "Demonstration workspace" : action}
      aria-busy={checking || undefined}
      title={view.description}
    >
      <UroIcon name="pulse" aria-hidden="true" />
      <span className="hidden xl:inline">{view.label}</span>
    </button>
  );
}

export function ConsoleConnectionFooter() {
  const { view } = useConsoleConnection();
  return (
    <div
      className="uro-system-state group-data-[collapsible=icon]:hidden"
      data-connection={view.kind}
      title={view.description}
    >
      <span className="uro-system-state-dot" aria-hidden="true" />
      <span>{view.footer}</span>
    </div>
  );
}
