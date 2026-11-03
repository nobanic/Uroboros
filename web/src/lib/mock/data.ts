import { createPolicyCatalog } from "./demo-policy";
import { createUsageCatalog, createExecutionCatalog, createPoolCatalog } from "./demo-telemetry";
import { createToolCatalog, createConnectorCatalog } from "./demo-tools";
import { createCoverageCatalog } from "./demo-coverage";
import { ScenarioViews } from "./scenario-views";
import { ScenarioWorkspace } from "./scenario-workspace";
import { inferenceRecordDetail } from "./scenario-inference";
import { countRows, dailyUsage, sessionUsage, totalUsage } from "./scenario-usage";
import { ScenarioRecords } from "./scenario-records";
import { activityRecord } from "./demo-activity";
import { conversationRecord } from "./demo-sessions";
import { createInvestigationGraph } from "./investigation-graph";
import { createInvestigationHistory } from "./investigation-history";
// Rich mock dataset for the Vercel demo (NEXT_PUBLIC_MOCK=1). Shapes track the
// current backend contract in lib/api.ts + lib/types.ts. All data is static and
// self-consistent (one company "Acme", a few tasks, findings, exploration graph,
// sessions/activity, traffic, agents, etc.) so every page looks populated.

import type {
  Activity,
  Agent,
  AgentDetail,
  Asset,
  Company,
  Conversation,
  DailyTokenBucket,
  Edge,
  Finding,
  FindingAsset,
  InterceptAudit,
  LLMProfile,
  LLMTask,
  MissingSkill,
  PromptVar,
  PromptVersion,
  SessionTokenUsage,
  Settings,
  SkillCall,
  SkillItem,
  Stats,
  Task,
  TaskCategory,
  TaskNode,
  TaskTemplate,
  TokenTotal,
  TrafficDetail,
  TrafficHost,
  TrafficResp,
} from "@/lib/types";

const T = (iso: string) => iso; // readability helper for timestamps
const scenario = new ScenarioRecords(1);
const scenarioViews = new ScenarioViews({
  tasks: () => tasks,
  assets: () => assets,
  findings: () => findings,
  agents: () => agents,
  promptVars: () => promptVars,
  promptVersions: () => promptVersions,
  nodeAssets: () => NODE_ASSETS,
  activeTask: () => ACTIVE_TASK,
});

// ── Tasks ──────────────────────────────────────────────────────────────────
export const ACTIVE_TASK = "t-acme-web";

export const tasks: Task[] = [
  scenario.task({
    id: "t-acme-web",
    name: "Acme Perimeter Breach",
    category_id: 1,
    category_name: "External Assessment",
    description: "Acme public web and administration perimeter",
    goal: "Validate whether the acme.com administration boundary permits unauthorized access to sensitive customer data.",
    status: "running",
    created_at: T("2026-07-24T09:12:00Z"),
    created_unix: 1785489120,
    last_activity: T("2026-07-26T03:58:22Z"),
    last_activity_unix: 1785643102,
    active: true,
    in_flight: 3,
    findings: { critical: 1, high: 2, medium: 1, low: 0 },
    goals_total: 5,
    goals_met: 2,
    llm_profile_id: 1,
    llm_profile_ids: [1, 2],
    active_llm_profile_id: 1,
    source_task_ids: [],
    tokens: { input_tokens: 1284500, output_tokens: 96320, cache_read_tokens: 890400, cache_write_tokens: 132000 },
  }),
  scenario.task({
    id: "t-acme-api",
    name: "API Authorization Sweep",
    category_id: 2,
    category_name: "API Assurance",
    description: "api.acme.com authorization and injection assessment",
    goal: "Assess the order and customer APIs for object-level authorization failures and injection risk.",
    status: "running",
    created_at: T("2026-07-25T14:05:00Z"),
    created_unix: 1785592500,
    last_activity: T("2026-07-26T03:40:10Z"),
    last_activity_unix: 1785642010,
    in_flight: 2,
    findings: { critical: 0, high: 1, medium: 1, low: 0 },
    goals_total: 4,
    goals_met: 1,
    llm_profile_id: 1,
    llm_profile_ids: [1, 2],
    active_llm_profile_id: 1,
    source_task_ids: ["t-acme-web"],
    tokens: { input_tokens: 642300, output_tokens: 51200, cache_read_tokens: 401000, cache_write_tokens: 60000 },
  }),
  scenario.task({
    id: "t-shop-pay",
    name: "Storefront Transaction Review",
    category_id: 2,
    category_name: "API Assurance",
    description: "shop.acme.com payment and order workflow",
    goal: "Assess payment and order APIs for authorization bypass, price manipulation, and race conditions.",
    status: "paused",
    created_at: T("2026-07-22T08:30:00Z"),
    created_unix: 1785313800,
    last_activity: T("2026-07-23T18:40:10Z"),
    last_activity_unix: 1785436810,
    in_flight: 0,
    goals_total: 4,
    goals_met: 1,
    llm_profile_id: 2,
    llm_profile_ids: [2, 1],
    active_llm_profile_id: 2,
    source_task_ids: ["t-acme-web", "t-acme-api"],
    tokens: { input_tokens: 233000, output_tokens: 18700, cache_read_tokens: 120000, cache_write_tokens: 22000 },
  }),
  scenario.task({
    id: "t-vpn-edge",
    name: "VPN Edge Recon",
    category_id: 1,
    category_name: "External Assessment",
    description: "Internet-facing perimeter reconnaissance",
    goal: "Identify exploitable VPN and edge services exposed by the acme.com perimeter.",
    status: "done",
    created_at: T("2026-07-18T08:00:00Z"),
    created_unix: 1784966400,
    completed_at: T("2026-07-20T22:11:00Z"),
    completed_unix: 1785190260,
    last_activity: T("2026-07-20T22:11:00Z"),
    last_activity_unix: 1785190260,
    in_flight: 0,
    goals_total: 3,
    goals_met: 3,
    llm_profile_id: 1,
    llm_profile_ids: [1],
    active_llm_profile_id: 1,
    source_task_ids: [],
    tokens: { input_tokens: 512000, output_tokens: 40100, cache_read_tokens: 300000, cache_write_tokens: 41000 },
  }),
];

export const taskCategories: TaskCategory[] = [
  {
    id: 1,
    name: "External Assessment",
    task_count: 2,
    created_at: T("2026-07-18T08:00:00Z"),
    updated_at: T("2026-07-25T08:00:00Z"),
  },
  {
    id: 2,
    name: "API Assurance",
    task_count: 2,
    created_at: T("2026-07-19T08:00:00Z"),
    updated_at: T("2026-07-24T08:00:00Z"),
  },
];

export const taskTemplates: TaskTemplate[] = [
  {
    id: 1,
    name: "External Web Assessment",
    description:
      "Run a black-box assessment of the public attack surface, including web applications, APIs, and common administration entry points.",
    goal: "Identify and verify weaknesses that could enable unauthorized access, sensitive-data exposure, or server compromise.",
    created_at: T("2026-07-20T08:00:00Z"),
    updated_at: T("2026-07-25T08:00:00Z"),
  },
  {
    id: 2,
    name: "API Authorization Review",
    description: "Test authentication, object-level authorization, and role boundaries across the target API.",
    goal: "Confirm whether IDOR, horizontal or vertical privilege escalation, or bulk data access is possible.",
    created_at: T("2026-07-19T08:00:00Z"),
    updated_at: T("2026-07-24T08:00:00Z"),
  },
];

export const getTask = (id: string) => scenarioViews.task(id);

const grandTotal: TokenTotal = totalUsage(tasks.map((task) => task.tokens));

// ── Stats ──────────────────────────────────────────────────────────────────
export function stats(
  taskId?: string,
  state: { tasks?: readonly Task[]; findings?: readonly Finding[]; activeTask?: string } = {},
): Stats & { active_task?: unknown } {
  return scenarioViews.stats(taskId, state);
}

// ── Company + Assets (new unified model) ─────────────────────────────────────
export const companies: Company[] = [
  {
    id: 1,
    name: "Acme Corp",
    logo: "",
    asset_count: 18,
    scope: [
      { id: 1, company_id: 1, kind: "domain", domain: "acme.com", raw: "acme.com", reason: "Primary domain" },
      { id: 2, company_id: 1, kind: "cidr", net: "203.0.113.0/24", raw: "203.0.113.0/24" },
      { id: 3, company_id: 1, kind: "ip", net: "198.51.100.20", raw: "198.51.100.20" },
    ],
  },
];

export const assets: Asset[] = [
  scenario.asset(
    1, "root_domain", [1], T("2026-07-26T02:00:00Z"),
    {
    domain: "acme.com",
    root_domain: "acme.com",
    icp: "",
    record_type: "A",
    record_value: ["203.0.113.10"],
  }
  ),
  scenario.asset(
    2, "subdomain", [1], T("2026-07-26T02:01:00Z"),
    {
    domain: "www.acme.com",
    root_domain: "acme.com",
    record_type: "CNAME",
    record_value: "acme.com",
  }
  ),
  scenario.asset(
    3, "subdomain", [2], T("2026-07-26T02:02:00Z"),
    {
    domain: "api.acme.com",
    root_domain: "acme.com",
    record_type: "A",
    record_value: ["203.0.113.11"],
  }
  ),
  scenario.asset(
    4, "subdomain", [3], T("2026-07-26T02:03:00Z"),
    {
    domain: "shop.acme.com",
    root_domain: "acme.com",
    record_type: "A",
    record_value: ["203.0.113.12"],
  }
  ),
  scenario.asset(
    5, "subdomain", [4], T("2026-07-20T02:00:00Z"),
    {
    domain: "vpn.acme.com",
    root_domain: "acme.com",
    record_type: "A",
    record_value: ["198.51.100.20"],
  }
  ),
  scenario.asset(
    6, "subdomain", [1], T("2026-07-26T02:04:00Z"),
    {
    domain: "admin.acme.com",
    root_domain: "acme.com",
    record_type: "A",
    record_value: ["203.0.113.10"],
  }
  ),
  scenario.asset(
    7, "ip", [1, 2], T("2026-07-26T02:05:00Z"),
    {
    ip: "203.0.113.10",
    c_segment: "203.0.113.0",
    open_ports: [
      { port: 80, service: "http" },
      { port: 443, service: "https" },
      { port: 22, service: "ssh" },
    ],
  }
  ),
  scenario.asset(
    8, "ip", [2], T("2026-07-26T02:06:00Z"),
    {
    ip: "203.0.113.11",
    c_segment: "203.0.113.0",
    open_ports: [{ port: 443, service: "https" }],
  }
  ),
  scenario.asset(
    9, "ip", [4], T("2026-07-20T02:00:00Z"),
    {
    ip: "198.51.100.20",
    c_segment: "198.51.100.0",
    open_ports: [
      { port: 443, service: "https" },
      { port: 500, service: " isakmp" },
    ],
  }
  ),
  scenario.asset(
    10, "service", [1], T("2026-07-26T02:07:00Z"),
    {
    ip: "203.0.113.10",
    port: 443,
    service_type: "https",
    service_name: "nginx/1.24.0",
  }
  ),
  scenario.asset(
    11, "service", [4], T("2026-07-20T02:00:00Z"),
    {
    ip: "198.51.100.20",
    port: 22,
    service_type: "ssh",
    service_name: "OpenSSH 8.9p1",
  }
  ),
  scenario.asset(
    12, "app", [1], T("2026-07-26T02:08:00Z"),
    {
    url: "https://www.acme.com",
    app_name: "Acme Public Site",
    category: "corp",
    status_code: 200,
    content_length: 48213,
    page_title: "Acme Corp — Public Site",
    technologies: ["Nginx", "React", "Cloudflare"],
    favicon_mmh3: "-1580860059",
  }
  ),
  scenario.asset(
    13, "app", [1], T("2026-07-26T02:09:00Z"),
    {
    url: "https://admin.acme.com",
    app_name: "Administration Console",
    category: "admin",
    status_code: 200,
    content_length: 12044,
    page_title: "Acme Admin Sign In",
    technologies: ["Nginx", "Vue", "Element-UI"],
  }
  ),
  scenario.asset(
    14, "app", [2], T("2026-07-26T02:10:00Z"),
    {
    url: "https://api.acme.com",
    app_name: "REST API",
    category: "api",
    status_code: 401,
    content_length: 45,
    page_title: "",
    technologies: ["Kong", "Node.js"],
  }
  ),
  scenario.asset(
    15, "endpoint", [2], T("2026-07-26T02:11:00Z"),
    {
    url: "https://api.acme.com/v1/orders",
    method: "GET",
    status_code: 200,
    params: [
      { name: "id", in: "query" },
      { name: "page", in: "query" },
    ],
  }
  ),
  scenario.asset(
    16, "endpoint", [2], T("2026-07-26T02:12:00Z"),
    {
    url: "https://api.acme.com/v1/users/me",
    method: "GET",
    status_code: 200,
  }
  ),
  scenario.asset(
    17, "endpoint", [1], T("2026-07-26T02:13:00Z"),
    {
    url: "https://admin.acme.com/login",
    method: "POST",
    status_code: 200,
    params: [{ name: "username" }, { name: "password" }],
  }
  ),
  scenario.asset(
    18, "endpoint", [1], T("2026-07-26T02:14:00Z"),
    {
    url: "https://www.acme.com/search",
    method: "GET",
    status_code: 200,
    params: [{ name: "q", in: "query" }],
  }
  ),
  scenario.asset(
    19, "ip", [1], T("2026-07-25T15:40:00Z"),
    {
    ip: "10.0.20.15",
    c_segment: "10.0.20.0",
    open_ports: [
      { port: 80, service: "http" },
      { port: 22, service: "ssh" },
    ],
  }
  ),
  scenario.asset(
    20, "ip", [1], T("2026-07-25T21:00:00Z"),
    {
    ip: "10.10.10.10",
    c_segment: "10.10.10.0",
    open_ports: [
      { port: 88, service: "kerberos" },
      { port: 389, service: "ldap" },
      { port: 445, service: "smb" },
    ],
  }
  ),
  scenario.asset(
    21, "ip", [1], T("2026-07-25T21:00:00Z"),
    {
    ip: "10.10.10.5",
    c_segment: "10.10.10.0",
    open_ports: [{ port: 445, service: "smb" }],
  }
  ),
  scenario.asset(
    22, "ip", [1], T("2026-07-25T21:00:00Z"),
    {
    ip: "10.10.10.20",
    c_segment: "10.10.10.0",
    open_ports: [{ port: 8080, service: "http-proxy" }],
  }
  ),
  scenario.asset(
    23, "service", [1], T("2026-07-25T21:35:00Z"),
    {
    ip: "10.10.10.20",
    port: 8080,
    service_type: "http",
    service_name: "Jenkins 2.289",
  }
  ),
  scenario.asset(
    24, "service", [1], T("2026-07-25T23:55:00Z"),
    {
    ip: "10.10.10.10",
    port: 445,
    service_type: "smb",
    service_name: "Windows Server 2019 DC",
  }
  ),
  scenario.asset(
    25, "app", [1], T("2026-07-25T21:35:00Z"),
    {
    url: "http://10.10.10.20:8080",
    app_name: "Jenkins (Internal)",
    category: "ci",
    status_code: 200,
    content_length: 14200,
    page_title: "Jenkins Dashboard",
    technologies: ["Jenkins", "Java"],
  }
  ),
  scenario.asset(
    26, "endpoint", [1], T("2026-07-25T21:35:00Z"),
    {
    url: "http://10.10.10.20:8080/script",
    method: "GET",
    status_code: 200,
  }
  ),
];

export const assetCounts: Record<string, number> = countRows(assets, (asset) => asset.type);

// Mock fixture data.
// Mock fixture data.
// Mock fixture data.
function assetRef(id: number): FindingAsset {
  return scenarioViews.assetRef(id);
}

// ── Findings ─────────────────────────────────────────────────────────────────
export const findings: Finding[] = [
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-1",
    assets: [assetRef(18)],
    vulnclass: "SQL Injection",
    name: "Error-based SQL injection in public search",
    severity: "high",
    report:
      "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
    summary: "The q parameter on www.acme.com/search is vulnerable to error-based SQL injection.",
    evidence:
      "GET /search?q=1' AND 1=CONVERT(int,@@version)-- returned an MSSQL conversion error that disclosed database structure.",
    intent_id: "i-2",
    ts: T("2026-07-26T01:20:00Z"),
  }),
  scenario.finding(getTask("t-acme-api")!, {
    id: "f-2",
    assets: [assetRef(15)],
    vulnclass: "IDOR",
    severity: "high",
    summary: "api.acme.com/v1/orders exposes other customers' orders through an IDOR.",
    evidence:
      "Changing id=1001 to id=1002 returned another customer's order, shipping address, and phone number without an ownership check.",
    intent_id: "i-5",
    ts: T("2026-07-26T02:44:00Z"),
  }),
  scenario.finding(getTask("t-acme-api")!, {
    id: "f-3",
    assets: [assetRef(3)],
    vulnclass: "Weak JWT",
    severity: "high",
    summary: "API JWTs use a weak signing secret and can be forged offline.",
    evidence:
      "The HS256 secret was recovered with John in five seconds, allowing arbitrary subject claims to be signed.",
    ts: T("2026-07-26T03:02:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-4",
    assets: [assetRef(18)],
    vulnclass: "Reflected XSS",
    severity: "medium",
    summary: "The search page reflects the q parameter without output encoding.",
    evidence:
      "q=<script>alert(document.domain)</script> was returned verbatim in the result heading and executed in the browser.",
    ts: T("2026-07-25T22:10:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-5",
    assets: [assetRef(2)],
    vulnclass: "Exposed .git",
    severity: "medium",
    summary: "www.acme.com exposes its .git directory and recoverable source history.",
    evidence: "GET /.git/HEAD returned 200; git-dumper recovered backend source and database connection notes.",
    ts: T("2026-07-25T20:30:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-6",
    assets: [assetRef(17)],
    vulnclass: "Default Credentials",
    severity: "high",
    summary: "admin.acme.com accepts the default credential pair admin/admin123.",
    evidence: "The credentials authenticated successfully and exposed user and order administration functions.",
    intent_id: "i-3",
    ts: T("2026-07-26T03:50:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-7",
    assets: [assetRef(17)],
    vulnclass: "Open Redirect",
    severity: "low",
    summary: "The post-authentication next parameter permits an arbitrary redirect.",
    evidence: "/login?next=https://evil.example returned a 302 redirect to the external origin after authentication.",
    ts: T("2026-07-25T19:12:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-8",
    assets: [assetRef(17)],
    vulnclass: "Missing Rate Limit",
    severity: "medium",
    summary: "The login endpoint has no effective rate limit or lockout.",
    evidence: "One thousand attempts per minute were accepted without account lockout or a challenge.",
    ts: T("2026-07-25T18:40:00Z"),
  }),
  scenario.finding(getTask("t-acme-api")!, {
    id: "f-9",
    assets: [assetRef(16)],
    vulnclass: "Verbose Error",
    severity: "low",
    summary: "API error responses disclose stack traces, filesystem paths, and framework versions.",
    evidence: "A controlled 500 response returned a Node.js stack trace with absolute paths and dependency versions.",
    ts: T("2026-07-25T23:05:00Z"),
  }),
  scenario.finding(getTask("t-shop-pay")!, {
    id: "f-10",
    assets: [assetRef(4)],
    vulnclass: "Outdated Component",
    severity: "medium",
    summary: "The storefront uses an outdated component with a known remote-code-execution path.",
    evidence:
      "Fingerprinting identified component v2.3.1, which maps to the demonstrated deserialization vulnerability.",
    ts: T("2026-07-23T15:00:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-11",
    vulnclass: "Hardcoded Credentials",
    severity: "high",
    summary: "Recovered source contains the hard-coded database credential sa/Acme@2021.",
    evidence:
      "git-dumper recovered config.php from www.acme.com/.git; the plaintext database credential was later confirmed to work internally.",
    intent_id: "ig",
    ts: T("2026-07-25T20:36:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-12",
    assets: [assetRef(4)],
    vulnclass: "Deserialization RCE",
    name: "Fastjson deserialization remote code execution",
    severity: "critical",
    status: "confirmed",
    report:
      "Fastjson deserialization triggered the controlled callback and confirmed remote code execution on the assessed host.",
    summary: "shop.acme.com permits remote code execution through Fastjson 1.2.24 deserialization.",
    evidence:
      "POST /api/import accepted a JNDI payload using @type:JdbcRowSetImpl, triggered the callback, and established a shell as www-data on dmz-web01.",
    intent_id: "i7",
    ts: T("2026-07-25T15:40:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-13",
    assets: [assetRef(19)],
    vulnclass: "Privilege Escalation",
    severity: "high",
    summary: "A sudo NOPASSWD misconfiguration on the DMZ foothold permits local privilege escalation to root.",
    evidence:
      "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
    intent_id: "i8",
    ts: T("2026-07-25T16:20:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-14",
    assets: [assetRef(19), assetRef(21)],
    vulnclass: "Network Segmentation",
    severity: "medium",
    summary: "A dual-homed DMZ host bypasses the intended internal network boundary.",
    evidence:
      "dmz-web01 has a second interface on 10.10.10.0/24 and can directly reach the domain controller, file server, and Jenkins.",
    intent_id: "i9",
    ts: T("2026-07-25T21:00:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-15",
    assets: [assetRef(26)],
    vulnclass: "Unauthenticated RCE",
    severity: "high",
    summary: "The internal Jenkins Script Console permits unauthenticated remote code execution.",
    evidence:
      "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
    intent_id: "i11",
    ts: T("2026-07-25T21:50:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-16",
    assets: [assetRef(21)],
    vulnclass: "Kerberoasting",
    severity: "high",
    summary: "The svc_sql domain service account is Kerberoastable and uses a weak password.",
    evidence:
      "GetUserSPNs obtained the svc_sql TGS and hashcat mode 13100 recovered Sql@2020 offline; the account belongs to the SQL administrators group.",
    intent_id: "i12",
    ts: T("2026-07-25T23:20:00Z"),
  }),
  scenario.finding(getTask("t-acme-web")!, {
    id: "f-17",
    assets: [assetRef(20)],
    vulnclass: "Domain Compromise",
    name: "Full compromise of domain controller DC01",
    severity: "critical",
    status: "confirmed",
    report:
      "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
    summary: "Domain Admin access provided full control of DC01 and completed the internal objective.",
    evidence:
      "svc_deploy was a Domain Admin; psexec.py obtained SYSTEM on DC01 and secretsdump exported domain NTLM hashes, including krbtgt.",
    intent_id: "i13",
    ts: T("2026-07-26T00:20:00Z"),
  }),
];

// ── Exploration graph (active task) ──────────────────────────────────────────
// Mock fixture data.
// Mock fixture data.
// Mock fixture data.

export const explorationGraph: { nodes: TaskNode[]; edges: Edge[] } = createInvestigationGraph();

export const intents: TaskNode[] = explorationGraph.nodes.filter((n) => n.type === "intent");
export const frontier: TaskNode[] = explorationGraph.nodes.filter(
  (n) => n.type === "intent" && (n.state === "open" || n.state === "running"),
);

// Mock fixture data.
// Mock fixture data.
// Mock fixture data.
// Mock fixture data.
export const activity: Activity[] = createInvestigationHistory();

export function activityForTask(): Activity[] {
  return activity;
}

// ── Token stats ──────────────────────────────────────────────────────────────
const usageCatalog = createUsageCatalog();
export const tokenWorkers = usageCatalog.tokenWorkers;

// Mirrors TokenStatsBySession: completed usage is keyed by the stable UI session
// (main | plan | intent:<id>) instead of the reusable work#N executor name.
export const tokenSessions: SessionTokenUsage[] = sessionUsage(activity);

export const tokenTotal: TokenTotal = grandTotal;

export const dailyTokens: DailyTokenBucket[] = dailyUsage("2026-07-26T00:00:00Z", 30);
export const convTokens = usageCatalog.convTokens;

// ── Traffic ──────────────────────────────────────────────────────────────────
const exchanges = [
  ["x-1", "www.acme.com", "GET", "https://www.acme.com/", 200, "text/html", 48213],
  ["x-2", "www.acme.com", "GET", "https://www.acme.com/search?q=test", 200, "text/html", 5120],
  ["x-3", "www.acme.com", "GET", "https://www.acme.com/.git/HEAD", 200, "text/plain", 23],
  ["x-4", "admin.acme.com", "GET", "https://admin.acme.com/login", 200, "text/html", 12044],
  ["x-5", "admin.acme.com", "POST", "https://admin.acme.com/login", 302, "text/html", 0],
  ["x-6", "admin.acme.com", "GET", "https://admin.acme.com/dashboard", 200, "text/html", 33120],
  ["x-7", "admin.acme.com", "GET", "https://admin.acme.com/api/users?page=1", 200, "application/json", 8842],
  ["x-8", "api.acme.com", "GET", "https://api.acme.com/v1/orders?id=1001", 200, "application/json", 640],
  ["x-9", "api.acme.com", "GET", "https://api.acme.com/v1/orders?id=1002", 200, "application/json", 655],
  ["x-10", "api.acme.com", "GET", "https://api.acme.com/v1/users/me", 200, "application/json", 312],
  ["x-11", "api.acme.com", "GET", "https://api.acme.com/v1/admin", 401, "application/json", 45],
  ["x-12", "shop.acme.com", "GET", "https://shop.acme.com/checkout", 200, "text/html", 21990],
  ["x-13", "shop.acme.com", "POST", "https://shop.acme.com/pay", 200, "application/json", 128],
  ["x-14", "www.acme.com", "GET", "https://www.acme.com/robots.txt", 200, "text/plain", 88],
  ["x-15", "shop.acme.com", "POST", "https://shop.acme.com/api/import", 500, "application/json", 2048],
  ["x-16", "shop.acme.com", "POST", "https://shop.acme.com/api/import", 200, "application/json", 0],
  ["x-17", "10.10.10.20", "GET", "http://10.10.10.20:8080/script", 200, "text/html", 14200],
  ["x-18", "10.10.10.20", "POST", "http://10.10.10.20:8080/scriptText", 200, "text/plain", 86],
  ["x-19", "10.10.10.10", "POST", "https://10.10.10.10:445/psexec", 200, "application/octet-stream", 0],
] as const;

export const traffic: TrafficResp = {
  enabled: true,
  proxy: ":8788",
  count: exchanges.length,
  total: exchanges.length,
  page: 0,
  size: 100,
  exchanges: exchanges.map(([id, host, method, url, status, ct, len], i) => ({
    id,
    host,
    method,
    url,
    status,
    content_type: ct,
    resp_len: len,
    ts: T(`2026-07-26T0${1 + (i % 3)}:${String(10 + i).padStart(2, "0")}:00Z`),
  })),
};

// Distinct hosts with counts, derived from the exchanges above (target picker).
const hostCounts: Record<string, number> = countRows(exchanges, (exchange) => exchange[1]);

export const trafficHosts: TrafficHost[] = Object.entries(hostCounts).map(([host, count]) => ({ host, count }));

export const trafficDetail: TrafficDetail = {
  req: `GET /v1/orders?id=1002 HTTP/1.1
Host: api.acme.com
Authorization: Bearer eyJhbGciOiJIUzI1NiJ9...
User-Agent: Uroboros-worker/1.0
Accept: application/json

`,
  resp: "uroboros.lab demonstration record. Context: HTTP/1.1, 200, OK, Content-Type, application/json.",
};

// ── Security / Audit ─────────────────────────────────────────────────────────
const policyCatalog = createPolicyCatalog();
export const audit = policyCatalog.audit;

// ── LLM profiles ─────────────────────────────────────────────────────────────
export const llmProfiles: LLMProfile[] = [
  scenario.profile({
    id: "1",
    name: "Claude Opus 4.8",
    format: "anthropic",
    model: "claude-opus-4-8",
    api_key_hint: "…a3f2",
    rate_per_minute: 0,
    context_window_k: 1000,
    thinking_type: "enabled",
    reasoning_effort: "high",
    is_default: true,
    priority: 0,
    max_tokens: 0,
    max_tokens_field: "",
  }),
  scenario.profile({
    id: "2",
    name: "DeepSeek V4",
    format: "openai",
    base_url: "https://api.deepseek.com",
    model: "deepseek-v4-flash",
    api_key_hint: "…9c11",
    rate_per_minute: 60,
    context_window_k: 128,
    thinking_type: "",
    reasoning_effort: "",
    is_default: false,
    priority: 10,
    max_tokens: 8192,
    max_tokens_field: "max_completion_tokens",
  }),
];

export const llmConfig = scenario.llmConfig(llmProfiles[0]);

// ── Agents ───────────────────────────────────────────────────────────────────
export const agents: Agent[] = [
  scenario.agent({
    id: "1001",
    key: "retester",
    name: "Finding Verification",
    role: "assistant",
    builtin: false,
    description: "Launches from a finding dossier and records an independent verification result.",
    mcp_count: 0,
    skill_count: 0,
    tool_count: 2,
  }),
  scenario.agent({
    id: "1",
    key: "goals",
    name: "Goal Decomposer",
    role: "goals",
    builtin: true,
    description: "Turns a mission objective into bounded, explorable sub-goals.",
    max_turns: 8,
    mcp_count: 0,
    skill_count: 1,
    tool_count: 3,
  }),
  scenario.agent({
    id: "2",
    key: "planner",
    name: "Attack Planner",
    role: "planner",
    builtin: true,
    description: "Reads the exploration graph, judges goal completion, and dispatches new intents.",
    mcp_count: 1,
    skill_count: 2,
    tool_count: 9,
  }),
  scenario.agent({
    id: "3",
    key: "mainagent",
    name: "Operator Liaison",
    role: "mainagent",
    builtin: true,
    description: "Maintains the human-in-the-loop conversation and injects hints or priority intents.",
    web_search: true,
    mcp_count: 2,
    skill_count: 3,
    tool_count: 11,
  }),
  scenario.agent({
    id: "4",
    key: "worker",
    name: "Execution Worker",
    role: "worker",
    builtin: true,
    description: "Claims an intent, runs scoped security tools, and writes verified facts back to the graph.",
    max_turns: 40,
    run_seconds: 1800,
    web_search: false,
    interactive_shell: true,
    mcp_count: 2,
    skill_count: 3,
    tool_count: 14,
  }),
  scenario.agent({
    id: "5",
    key: "recon-bot",
    name: "Recon Sentinel",
    role: "custom",
    builtin: false,
    description: "Runs scheduled passive reconnaissance against newly observed assets.",
    max_turns: 12,
    mcp_count: 1,
    skill_count: 1,
    tool_count: 4,
  }),
];

const promptVars: PromptVar[] = [
  {
    name: "Goal",
    description: "Current mission objective",
    example: "Validate administrative access controls on acme.com",
    source: "runtime",
  },
  {
    name: "AssetSummary",
    description: "Distilled asset graph overview",
    example: "6 subdomains / 3 IPs / 4 applications / 4 endpoints",
    source: "distilled",
  },
  {
    name: "RouteHint",
    description: "Exploration route hint",
    example: "Prioritize the administrative portal and API authorization surface",
    source: "exploration",
  },
];

const promptVersions: PromptVersion[] = [
  {
    version: 3,
    ts: T("2026-07-25T10:00:00Z"),
    note: "Strengthened authorization-testing guidance",
    template_text: "You are the Uroboros attack planner. Keep every action inside the authorized scope.",
  },
  {
    version: 2,
    ts: T("2026-07-20T10:00:00Z"),
    note: "Initial prompt refinement",
    template_text: "You are the Uroboros attack planner (v2).",
  },
];

export function agentDetail(key: string): AgentDetail {
  return scenarioViews.agentDetail(key);
}

// -- MCP ----------------------------------------------------------------------
const connectorCatalog = createConnectorCatalog();
export const mcpServers = connectorCatalog.mcpServers;
export const mcpToolsById = connectorCatalog.mcpToolsById;

// -- Skills -------------------------------------------------------------------
export const skills: SkillItem[] = [
  {
    name: "api-recon",
    description:
      "Map REST and GraphQL APIs, enumerate authorization boundaries, and triage newly discovered endpoints.",
    license: "MIT",
    mcps: [],
    files: ["SKILL.md", "scripts/enum.py"],
    calls: 24,
    tasks: 6,
    usage_agents: ["worker", "planner"],
    last_used: "2026-08-20T09:12:00Z",
  },
  {
    name: "playwright-cli",
    description: "Drive Playwright for dynamic exploration, rendered-page inspection, and reproducible screenshots.",
    mcps: ["playwright"],
    files: ["SKILL.md"],
    calls: 7,
    tasks: 3,
    usage_agents: ["worker"],
    last_used: "2026-08-19T21:40:00Z",
  },
];

export const skillCalls: SkillCall[] = [
  { ts: "2026-08-20T09:12:00Z", agent_key: "worker", task_id: 42, session_id: "", args_len: 128 },
  { ts: "2026-08-20T08:03:00Z", agent_key: "planner", task_id: 42, session_id: "", args_len: 0 },
  { ts: "2026-08-19T17:55:00Z", agent_key: "worker", task_id: 37, session_id: "", args_len: 64 },
];

export const missingSkills: MissingSkill[] = [
  { skill: "jwt-forge", calls: 3, agents: ["worker"], last_used: "2026-08-20T07:20:00Z" },
];

// -- Tools --------------------------------------------------------------------
export const tools = createToolCatalog(scenario);

// -- Settings -----------------------------------------------------------------
export const settings: Settings = {
  traffic_capture: true,
  agent_traffic_binding: false,
  llm_record: false,
  web_search_enabled: true,
  web_search_backend: "ddgs",
  web_search_fallback_backend: "",
  web_search_task_request_budget: 40,
  web_search_task_paid_request_budget: 20,
  web_search_rate_per_minute: 10,
  web_search_cache_ttl_seconds: 3600,
  web_search_audit_mode: "host_broker_v1",
  tor_research_enabled: false,
  tor_egress_profile_id: "tor",
  tor_socks_configured: false,
  tor_proxy_coordinates_exposed: false,
  brave_key_set: false,
  tavily_key_set: true,
  web_search_proxy: "",
  global_proxy: "",
  python_interpreter: "/usr/bin/python3",
  workers: 3,
  llm_pool_enabled: true,
  llm_pool_bind_fallback: false,
  noa_compaction: false,
};

// -- LLM pool (failover) -------------------------------------------------------
// Showcase state: the active route is healthy while one reserve route cools down.
export const llmPool = createPoolCatalog(llmProfiles);

// -- Intercept ----------------------------------------------------------------
export const interceptRules = policyCatalog.interceptRules;
export const assetInterceptRules = policyCatalog.assetInterceptRules;
export const interceptPending = policyCatalog.interceptPending;
export const interceptHistory = policyCatalog.interceptHistory;
// Approval detail fixtures use harmless report-writing examples.
export const interceptDetails: Record<number, InterceptAudit> = {
  94: {
    run_id: "demo-run-report",
    tool_use_id: "call-write-report",
    correlation: "exact",
    input_digest: "a3b458eca3b458eca3b458eca3b458eca3b458eca3b458eca3b458eca3b458ec1234",
    user_message: "uroboros.lab demonstration record. Context: reports/summary.md.",
    context: [
      { kind: "user", text: "Demo assessment update." },
      {
        kind: "tool_use",
        tool: "Read",
        tool_use_id: "call-read-evidence",
        text: '{"path":"reports/evidence-index.json"}',
      },
      {
        kind: "tool_result",
        tool: "Read",
        tool_use_id: "call-read-evidence",
        text: "Demo assessment update.",
      },
    ],
    captured_at: T("2026-07-26T08:00:00Z"),
    initial_action: "allow",
    initial_reason: "uroboros.lab demonstration record.",
    effective_action: "allow",
    config_digest: "b4c569fdb4c569fdb4c569fdb4c569fdb4c569fdb4c569fdb4c569fdb4c569fd1234",
    profile_id: 1,
    execution_status: "succeeded",
    output: "The mock tool completed successfully and returned demonstration evidence. Context: Successfully, wrote, reports/summary.md, 68.",
    execution_ended_at: T("2026-07-26T08:00:03Z"),
  },
  93: {
    run_id: "demo-run-review",
    tool_use_id: "call-pending-review",
    correlation: "exact",
    input_digest: "c5d670aec5d670aec5d670aec5d670aec5d670aec5d670aec5d670aec5d670ae1234",
    user_message: "uroboros.lab demonstration record.",
    context: [],
    captured_at: T("2026-07-25T15:14:00Z"),
    initial_action: "ask",
    initial_reason: "uroboros.lab demonstration record.",
    profile_id: 1,
    execution_status: "not_started",
  },
};

// ── Conversations (chat) ─────────────────────────────────────────────────────
export const conversations: Conversation[] = [
  conversationRecord(1, "mainagent", "uroboros.lab security record Context: acme.", T("2026-07-25T10:00:00Z"), T("2026-07-26T03:40:00Z"), {llm_profile_id: 1, pinned: true, pinned_at: T("2026-07-26T04:00:00Z")}),
  conversationRecord(2, "mainagent", "uroboros.lab security record Context: API.", T("2026-07-24T16:00:00Z"), T("2026-07-24T17:20:00Z"), {llm_profile_id: 2}),
  conversationRecord(3, "mainagent", "uroboros.lab security record Context: DMZ.", T("2026-07-25T15:45:00Z"), T("2026-07-26T00:25:00Z"), {llm_profile_id: 1}),
];

export const conversationMessages: Record<number, Activity[]> = {
  1: [
    activityRecord(1, "user", T("2026-07-25T10:00:00Z"), "user", {
      summary: "Verified security evidence captured during the uroboros.lab demonstration assessment.",
    }),
    activityRecord(2, "mainagent", T("2026-07-25T10:00:04Z"), "thinking", {
      summary: "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: admin.acme.com.",
    }),
    activityRecord(3, "mainagent", T("2026-07-25T10:00:06Z"), "tool_use", {
      tool: "bash",
      tool_use_id: "c11",
      summary: "curl -s https://admin.acme.com/login | grep -Eo 'element-ui|captcha|csrf'",
    }),
    activityRecord(4, "mainagent", T("2026-07-25T10:00:08Z"), "tool_result", {
      tool: "bash",
      tool_use_id: "c11",
      summary: "element-ui\ncsrf",
      detail: "Reproducible evidence was captured and attached to this demonstration record. Context: Element-UI, CSRF, token.",
    }),
    activityRecord(5, "mainagent", T("2026-07-25T10:00:20Z"), "tool_use", {
      tool: "bash",
      tool_use_id: "c12",
      summary:
        "for i in 1 2 3 4 5; do curl -s -o /dev/null -w '%{http_code}\\n' -d 'username=x&password=y' https://admin.acme.com/login; done",
    }),
    activityRecord(6, "mainagent", T("2026-07-25T10:00:24Z"), "tool_result", {
      tool: "bash",
      tool_use_id: "c12",
      summary: "401\\n401\\n401\\n401\\n401",
      detail: "Reproducible evidence was captured and attached to this demonstration record.",
    }),
    activityRecord(7, "mainagent", T("2026-07-25T10:00:30Z"), "text", {
      summary:
        "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: Element-UI, admin/admin, admin/admin123, admin/123456, top1000.",
    }),
    activityRecord(8, "user", T("2026-07-26T03:40:00Z"), "user", {
      summary: "Verified security evidence captured during the uroboros.lab demonstration assessment.",
    }),
    activityRecord(9, "mainagent", T("2026-07-26T03:40:05Z"), "text", {
      summary:
        "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: admin/admin123.",
    }),
  ],
  2: [
    activityRecord(1, "user", T("2026-07-24T16:00:00Z"), "user", {
      summary: "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: api.acme.com.",
    }),
    activityRecord(2, "mainagent", T("2026-07-24T16:00:05Z"), "thinking", {
      summary: "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check.",
    }),
    activityRecord(3, "mainagent", T("2026-07-24T16:00:10Z"), "tool_use", {
      tool: "bash",
      tool_use_id: "c21",
      summary: "curl -s https://api.acme.com/v1/users/me -H 'Authorization: Bearer <self>' | jq '{id,role}'",
    }),
    activityRecord(4, "mainagent", T("2026-07-24T16:00:12Z"), "tool_result", {
      tool: "bash",
      tool_use_id: "c21",
      summary: '{ "id": 42, "role": "user" }',
      detail: "Reproducible evidence was captured and attached to this demonstration record. Context: token.",
    }),
    activityRecord(5, "mainagent", T("2026-07-24T16:00:30Z"), "tool_use", {
      tool: "bash",
      tool_use_id: "c22",
      summary: "Offline verification recovered the weak signing secret and confirmed that arbitrary identity claims could be forged.",
    }),
    activityRecord(6, "mainagent", T("2026-07-24T16:00:31Z"), "tool_result", {
      tool: "bash",
      tool_use_id: "c22",
      summary: '{"alg":"HS256","typ":"JWT"}',
      detail: "Offline verification recovered the weak signing secret and confirmed that arbitrary identity claims could be forged.",
    }),
    activityRecord(7, "mainagent", T("2026-07-24T16:01:00Z"), "text", {
      summary:
        "Authorization testing confirmed that an order record could be accessed across account boundaries without an ownership check.",
    }),
  ],
  3: [
    activityRecord(1, "user", T("2026-07-25T15:45:00Z"), "user", {
      summary: "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
    }),
    activityRecord(2, "mainagent", T("2026-07-25T15:45:05Z"), "thinking", {
      summary: "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
    }),
    activityRecord(3, "mainagent", T("2026-07-25T15:45:10Z"), "text", {
      summary:
        "The worker validated the local privilege boundary and preserved the controlled shell evidence for review.",
    }),
    activityRecord(4, "user", T("2026-07-25T21:05:00Z"), "user", {
      summary: "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
    }),
    activityRecord(5, "mainagent", T("2026-07-25T21:05:08Z"), "tool_use", {
      tool: "bash",
      tool_use_id: "c31",
      summary: "proxychains curl -s http://10.10.10.20:8080/script -o /dev/null -w '%{http_code}'",
    }),
    activityRecord(6, "mainagent", T("2026-07-25T21:05:12Z"), "tool_result", {
      tool: "bash",
      tool_use_id: "c31",
      summary: "200",
      detail: "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
    }),
    activityRecord(7, "mainagent", T("2026-07-25T21:05:20Z"), "text", {
      summary:
        "The Jenkins script surface accepted the controlled Groovy probe and produced reproducible execution evidence.",
    }),
    activityRecord(8, "user", T("2026-07-25T23:25:00Z"), "user", {
      summary: "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
    }),
    activityRecord(9, "mainagent", T("2026-07-25T23:25:10Z"), "text", {
      summary:
        "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
    }),
    activityRecord(10, "user", T("2026-07-26T00:22:00Z"), "user", {
      summary: "Verified security evidence captured during the uroboros.lab demonstration assessment.",
    }),
    activityRecord(11, "mainagent", T("2026-07-26T00:22:12Z"), "text", {
      summary:
        "Domain controller access was validated and the resulting privilege evidence was preserved for review.",
    }),
  ],
};

// Mock fixture data.
const coverageCatalog = createCoverageCatalog();
export const coverage = coverageCatalog.coverage;
export const coverageGraph = coverageCatalog.coverageGraph;
// Mock fixture data.
// Mock fixture data.
const NODE_ASSETS: Record<string, number[]> = {
  fi1: [3],
  fi2: [2, 4],
  fi3: [5],
  fiRce: [4],
  fiPriv: [19],
  fiJenkins: [21],
  i1: [2],
  i7: [4],
};

export function nodeAssetsFor(nodeId: string): FindingAsset[] {
  return scenarioViews.nodeAssetsFor(nodeId);
}

export function assetRefsFor(_assetId: number) {
  return scenarioViews.assetReferences();
}

// Mock fixture data.
const WS_TREE: Record<string, { name: string; dir: boolean; size: number; content?: string }[]> = {
  "": [
    { name: "t-001", dir: true, size: 0 },
    { name: "transcripts", dir: true, size: 0 },
    {
      name: "notes.md",
      dir: false,
      size: 96,
      content: "Demo operator note: review the linked evidence and continue the verified assessment workflow. Context: demo.",
    },
  ],
  "t-001": [
    { name: "i12", dir: true, size: 0 },
    { name: "recon.txt", dir: false, size: 64, content: "acme.com\nwww.acme.com\napi.acme.com\nadmin.acme.com\n" },
  ],
  "t-001/i12": [
    {
      name: "exploit.py",
      dir: false,
      size: 220,
      content:
        "#!/usr/bin/env python3\n# (demo) SQLi PoC — www.acme.com/search?q=\nimport requests\n\nr = requests.get('https://www.acme.com/search', params={'q': \"1' OR '1'='1\"})\nprint(r.status_code, len(r.text))\n",
    },
    {
      name: "response.html",
      dir: false,
      size: 180,
      content: "The controlled SQL probe produced a database conversion error and confirmed the injectable request boundary.",
    },
  ],
  transcripts: [
    { name: "exp1-worker-i12.jsonl", dir: false, size: 512, content: "Demo operator note: review the linked evidence and continue the verified assessment workflow. Context: demo, LLM." },
  ],
};

const workspace = new ScenarioWorkspace(WS_TREE);

export function workspaceList(path: string) {
  return workspace.list(path);
}

export function workspaceRead(path: string) {
  return workspace.read(path);
}

// Mock fixture data.
const executionCatalog = createExecutionCatalog(llmProfiles[0]);
export const commandRecords = executionCatalog.commandRecords;
export const llmRecords = executionCatalog.llmRecords;
// Distinct tasks with counts, derived from llmRecords above (task picker).
const llmTaskCounts: Record<string, number> = countRows(llmRecords.filter((record) => record.task_id), (record) => record.task_id);

export const llmTasks: LLMTask[] = Object.entries(llmTaskCounts).map(([task_id, count]) => ({ task_id, count }));

export function llmRecordDetail(id: number, records = llmRecords) {
  return inferenceRecordDetail(id, records);
}
