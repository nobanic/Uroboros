export type ConsoleConnectionObservation = { busy?: boolean; data?: unknown; error?: unknown };
export type ConsoleConnectionKind = "checking" | "connected" | "unverified" | "unavailable" | "demo";

const descriptions: Record<ConsoleConnectionKind, { label: string; footer: string; description: string }> = {
  checking: { label: "Checking…", footer: "Checking backend", description: "Checking the backend connection." },
  connected: {
    label: "Connected",
    footer: "Backend connected",
    description: "The backend confirmed its health status.",
  },
  unverified: {
    label: "Unverified",
    footer: "Status unverified",
    description: "A response arrived without a confirmed health status. Retry to verify the connection.",
  },
  unavailable: {
    label: "Unavailable",
    footer: "Backend unavailable",
    description: "The backend did not confirm availability. Check the connection and retry.",
  },
  demo: {
    label: "Demo",
    footer: "Demo workspace",
    description: "Demonstration data. This workspace does not use a backend or model connection.",
  },
};

// A successful transport or a truthy field is not an affirmative health check.
// The demo mode describes the local workspace, never backend/provider health.
export function consoleConnectionView(observation: ConsoleConnectionObservation, demo = false) {
  const payload =
    observation.data !== null && typeof observation.data === "object" && !Array.isArray(observation.data)
      ? (observation.data as Record<string, unknown>)
      : undefined;
  let kind: ConsoleConnectionKind = "unverified";
  if (demo) kind = "demo";
  else if (observation.busy) kind = "checking";
  else if (observation.error) kind = "unavailable";
  else if (payload?.ok === true) kind = "connected";
  else if (payload?.ok === false) kind = "unavailable";
  const version = typeof payload?.version === "string" ? payload.version.replace(/^v(?=\d)/, "") : "";
  return { kind, version, ...descriptions[kind] };
}
