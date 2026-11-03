import { browserSession } from "@/astra/client";
import { agentService } from "@/lib/http/agent-service";
import { ApplicationWire } from "@/lib/http/application-wire";
import { approvalService } from "@/lib/http/approval-service";
import { assetService } from "@/lib/http/asset-service";
import { capabilityService } from "@/lib/http/capability-service";
import { conversationService } from "@/lib/http/conversation-service";
import { findingService } from "@/lib/http/finding-service";
import { historyService } from "@/lib/http/history-service";
import { inspectionService } from "@/lib/http/inspection-service";
import { modelService } from "@/lib/http/model-service";
import { systemService } from "@/lib/http/system-service";
import { taskPolicyService } from "@/lib/http/task-policy-service";
import { taskService } from "@/lib/http/task-service";
import { workspaceService } from "@/lib/http/workspace-service";
import { MOCK } from "@/lib/mock/enabled";
import { mockHandle } from "@/lib/mock/handler";

// Public workspace API. Domain services own routes, input and response policies; the wire owns auth and JSON errors.
function getToken(): string | null {
  return typeof window === "undefined" ? null : browserSession.read();
}
const wire = new ApplicationWire({
  fetch: (...args) => fetch(...args),
  token: getToken,
  expired: () => {
    if (typeof window !== "undefined") {
      browserSession.clear();
      window.location.href = "/login";
    }
  },
  mock: { enabled: () => MOCK, reply: (method, path, body) => mockHandle(method, path, body) },
});
export const http = wire.json;
// Dev SSE connects directly to Go to avoid proxy buffering; production defaults to same-origin.
export function sseUrl(path: string): string {
  const base =
    process.env.NEXT_PUBLIC_SSE_BASE ??
    (process.env.NODE_ENV !== "production" && typeof window !== "undefined"
      ? `${window.location.protocol}//${window.location.hostname}:8787`
      : "");
  const token = getToken();
  const sep = path.includes("?") ? "&" : "?";
  return token ? `${base}${path}${sep}token=${encodeURIComponent(token)}` : `${base}${path}`;
}
function mockReport(_task?: string): string {
  return `# Uroboros Penetration Test Report — uroboros.lab

## Overview
- Scope: uroboros.lab (incl. www / admin / api / shop / vpn subdomains)
- Confirmed findings: 6 (high 3 · medium 3 · low 2)
- Engine mode: exploring

## Key findings
1. **[High] Default admin-panel password** admin.uroboros.lab admin/admin123 → full takeover of the admin panel.
2. **[High] SQL injection** www.uroboros.lab/search?q= → the uroboros_lab_prod database is readable.
3. **[High] IDOR** api.uroboros.lab/v1/orders?id= → other users' orders can be read without authorization (incl. phone numbers/addresses).
4. **[Medium] Reflected XSS**, **exposed .git source**, **no rate limiting on login**.

## Recommendations
- Force an admin-panel password change + enable MFA, and ban default passwords.
- Use parameterized queries and output encoding on the search endpoint.
- Add object-level authorization checks to the API (IDOR), and rotate to a strong JWT key.

> (demo) This report is generated from mock data, for UI demonstration only.`;
}
export const api = {
  ...modelService(wire),
  ...capabilityService(wire),
  ...agentService(wire),
  ...taskPolicyService(wire),
  ...assetService(wire),
  ...inspectionService(wire),
  ...approvalService(wire, () => MOCK),
  ...conversationService(wire),
  ...historyService(wire),
  ...taskService(wire),
  ...findingService(wire),
  ...workspaceService(wire, () => MOCK),
  ...systemService(wire, { enabled: () => MOCK, report: mockReport }),
};
