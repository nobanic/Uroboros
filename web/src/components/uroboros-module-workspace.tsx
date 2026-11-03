"use client";

import type { ReactNode } from "react";
import * as React from "react";

import Link from "next/link";

import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { cn } from "@/lib/utils";

type WorkflowStep = {
  label: string;
  detail: string;
};

type RelatedSurface = {
  label: string;
  href: string;
  icon: UroIconName;
};

export type ConsoleModule = {
  id: string;
  match: string;
  title: string;
  eyebrow: string;
  icon: UroIconName;
  serial: string;
  brief: string;
  directive: string;
  workflow: [WorkflowStep, WorkflowStep, WorkflowStep];
  related: RelatedSurface[];
  framed?: boolean;
};

const modules: ConsoleModule[] = [
  {
    id: "finding-detail",
    match: "/function/findings/detail",
    title: "Finding Dossier",
    eyebrow: "Evidence decision",
    icon: "findings",
    serial: "TRIAGE / DOSSIER",
    brief: "Reconstruct the claim, follow its evidence lineage, and choose the next operator decision.",
    directive: "Treat every claim as provisional until the linked traffic, facts, and reproduction path agree.",
    workflow: [
      { label: "Reproduce", detail: "Read the claim and affected surface." },
      { label: "Trace", detail: "Follow evidence and lineage." },
      { label: "Decide", detail: "Retest, deepen, export, or close." },
    ],
    related: [
      { label: "Finding registry", href: "/function/findings", icon: "findings" },
      { label: "Observed traffic", href: "/function/traffic", icon: "traffic" },
      { label: "Operations", href: "/function/tasks", icon: "tasks" },
    ],
  },
  {
    id: "agent-detail",
    match: "/system/agents/detail",
    title: "Agent Workbench",
    eyebrow: "Capability composition",
    icon: "agents",
    serial: "FABRIC / AGENT",
    brief: "Shape one agent's instructions, model path, tools, skills, and external capabilities as a single unit.",
    directive: "Keep authority legible: prompt, model, tools, skills, and MCP visibility should tell the same story.",
    workflow: [
      { label: "Define", detail: "Review role and instructions." },
      { label: "Bind", detail: "Compose tools, skills, and MCP." },
      { label: "Verify", detail: "Check the effective capability set." },
    ],
    related: [
      { label: "Agent roster", href: "/system/agents", icon: "agents" },
      { label: "Toolchain", href: "/system/tools", icon: "tools" },
      { label: "MCP mesh", href: "/system/mcp", icon: "mcp" },
    ],
  },
  {
    id: "asset-guard",
    match: "/system/intercept/assets",
    title: "Asset Guard",
    eyebrow: "Scope authority",
    icon: "blocked",
    serial: "POLICY / SCOPE",
    brief: "Evaluate target authority and make allow or deny boundaries explicit before execution.",
    directive:
      "A visually quiet rule can still change target authority. Review precedence and scope before enabling it.",
    workflow: [
      { label: "Evaluate", detail: "Inspect the candidate asset." },
      { label: "Resolve", detail: "Apply interception and allow rules." },
      { label: "Audit", detail: "Confirm the effective boundary." },
    ],
    related: [
      { label: "Command rules", href: "/system/intercept", icon: "intercept" },
      { label: "Approval queue", href: "/system/intercept/approvals", icon: "approvals" },
      { label: "Asset graph", href: "/function/assets", icon: "assets" },
    ],
  },
  {
    id: "approvals",
    match: "/system/intercept/approvals",
    title: "Approval Station",
    eyebrow: "Human decision queue",
    icon: "approvals",
    serial: "POLICY / REVIEW",
    brief: "Review held actions with their command context and leave an attributable decision record.",
    directive: "Approve the specific action in context, never the general tool or target by implication.",
    workflow: [
      { label: "Queue", detail: "Identify waiting decisions." },
      { label: "Inspect", detail: "Read command and policy context." },
      { label: "Record", detail: "Allow or deny with rationale." },
    ],
    related: [
      { label: "Command rules", href: "/system/intercept", icon: "intercept" },
      { label: "Asset guard", href: "/system/intercept/assets", icon: "blocked" },
      { label: "System log", href: "/system/logs", icon: "logs" },
    ],
  },
  {
    id: "mission-control",
    match: "/dashboard",
    title: "Mission Control",
    eyebrow: "Global posture",
    icon: "mission",
    serial: "OPS / GLOBAL",
    brief: "Global operating posture.",
    directive: "",
    workflow: [
      { label: "Observe", detail: "" },
      { label: "Orient", detail: "" },
      { label: "Act", detail: "" },
    ],
    related: [],
    framed: false,
  },
  {
    id: "command-chat",
    match: "/chat",
    title: "Command Chat",
    eyebrow: "Agent channel",
    icon: "chat",
    serial: "OPS / CHANNEL",
    brief: "Direct agent channel.",
    directive: "",
    workflow: [
      { label: "Choose", detail: "" },
      { label: "Brief", detail: "" },
      { label: "Observe", detail: "" },
    ],
    related: [],
    framed: false,
  },
  {
    id: "operations",
    match: "/function/tasks",
    title: "Operations Queue",
    eyebrow: "Mission execution",
    icon: "tasks",
    serial: "OPS / QUEUE",
    brief: "Prioritize active missions, understand stalled work, and enter the operation that needs a decision.",
    directive: "Queue state should answer one question first: where does operator attention change the outcome now?",
    workflow: [
      { label: "Prioritize", detail: "Compare status, progress, and risk." },
      { label: "Dispatch", detail: "Create, resume, pause, or route work." },
      { label: "Verify", detail: "Enter the operation and inspect evidence." },
    ],
    related: [
      { label: "Mission control", href: "/dashboard", icon: "mission" },
      { label: "Finding registry", href: "/function/findings", icon: "findings" },
      { label: "Observed traffic", href: "/function/traffic", icon: "traffic" },
    ],
  },
  {
    id: "findings",
    match: "/function/findings",
    title: "Finding Registry",
    eyebrow: "Evidence triage",
    icon: "findings",
    serial: "OPS / TRIAGE",
    brief: "Turn candidate findings into an ordered review queue with visible evidence and next actions.",
    directive: "Severity drives urgency; evidence quality and reproducibility drive confidence.",
    workflow: [
      { label: "Triage", detail: "Rank severity and review state." },
      { label: "Correlate", detail: "Connect assets, traffic, and lineage." },
      { label: "Resolve", detail: "Retest, deepen, export, or close." },
    ],
    related: [
      { label: "Operations", href: "/function/tasks", icon: "tasks" },
      { label: "Observed traffic", href: "/function/traffic", icon: "traffic" },
      { label: "Asset graph", href: "/function/assets", icon: "assets" },
    ],
  },
  {
    id: "traffic",
    match: "/function/traffic",
    title: "Traffic Observatory",
    eyebrow: "Captured exchanges",
    icon: "traffic",
    serial: "EVIDENCE / TRAFFIC",
    brief: "Move from a live exchange stream to request inspection and evidence binding without losing target context.",
    directive:
      "Filter the stream before binding evidence; similar URLs can belong to different task and target contexts.",
    workflow: [
      { label: "Capture", detail: "Choose target and narrow the stream." },
      { label: "Inspect", detail: "Read request and response together." },
      { label: "Bind", detail: "Link the exchange to a finding." },
    ],
    related: [
      { label: "Finding registry", href: "/function/findings", icon: "findings" },
      { label: "Asset graph", href: "/function/assets", icon: "assets" },
      { label: "Workspace", href: "/function/workspace", icon: "workspace" },
    ],
  },
  {
    id: "tool-runs",
    framed: false,
    match: "/function/commands",
    title: "Execution Observatory",
    eyebrow: "Tool-call telemetry",
    icon: "commands",
    serial: "EVIDENCE / EXEC",
    brief: "Follow tool activity as an execution stream, isolate failures, and inspect exact input/output evidence.",
    directive:
      "A failed call is a diagnostic event, not just a red row—inspect its input and output in the same context.",
    workflow: [
      { label: "Observe", detail: "Scan the ordered execution stream." },
      { label: "Diagnose", detail: "Open exact input and output." },
      { label: "Compare", detail: "Use statistics to spot patterns." },
    ],
    related: [
      { label: "Model ledger", href: "/function/llm-records", icon: "records" },
      { label: "System log", href: "/system/logs", icon: "logs" },
      { label: "Toolchain", href: "/system/tools", icon: "tools" },
    ],
  },
  {
    id: "model-ledger",
    framed: false,
    match: "/function/llm-records",
    title: "Inference Observatory",
    eyebrow: "Model-call telemetry",
    icon: "records",
    serial: "EVIDENCE / MODEL",
    brief: "Trace model calls, compare latency and token shape, and inspect the actual request/response pair.",
    directive:
      "Use the raw view when transport behavior matters; parsed content is optimized for review, not wire fidelity.",
    workflow: [
      { label: "Trace", detail: "Filter by task, session, or model." },
      { label: "Compare", detail: "Read latency, tokens, and status." },
      { label: "Inspect", detail: "Open parsed or raw exchange data." },
    ],
    related: [
      { label: "Tool executions", href: "/function/commands", icon: "commands" },
      { label: "Model fabric", href: "/system/llm", icon: "llm" },
      { label: "System log", href: "/system/logs", icon: "logs" },
    ],
  },
  {
    id: "assets",
    match: "/function/assets",
    title: "Surface Registry",
    eyebrow: "Known attack surface",
    icon: "assets",
    serial: "SURFACE / REGISTRY",
    brief:
      "Navigate ownership, scope, and discovered assets as one operational surface rather than an inventory table.",
    directive: "Ownership and test authority are separate facts. Confirm both before using an asset in an operation.",
    workflow: [
      { label: "Map", detail: "Move through company and asset types." },
      { label: "Scope", detail: "Review and adjust authority." },
      { label: "Operate", detail: "Use the surface in a mission." },
    ],
    related: [
      { label: "Coverage lattice", href: "/function/tasks", icon: "coverage" },
      { label: "Asset guard", href: "/system/intercept/assets", icon: "blocked" },
    ],
  },
  {
    id: "workspace",
    framed: false,
    match: "/function/workspace",
    title: "Evidence Workspace",
    eyebrow: "Operator files",
    icon: "workspace",
    serial: "EVIDENCE / FILES",
    brief: "Browse task artifacts, inspect text evidence, and move files in or out of the operator workspace.",
    directive:
      "Workspace files are operator evidence. Preserve path context when downloading, editing, or deleting them.",
    workflow: [
      { label: "Browse", detail: "Navigate folders and evidence sets." },
      { label: "Inspect", detail: "Preview or edit supported text." },
      { label: "Export", detail: "Download or upload artifacts." },
    ],
    related: [
      { label: "Observed traffic", href: "/function/traffic", icon: "traffic" },
      { label: "Finding registry", href: "/function/findings", icon: "findings" },
      { label: "Operations", href: "/function/tasks", icon: "tasks" },
    ],
  },
  {
    id: "model-fabric",
    match: "/system/llm",
    title: "Model Fabric",
    eyebrow: "Inference control plane",
    icon: "llm",
    serial: "FABRIC / MODELS",
    brief: "See the active inference path, health, failover order, and rate behavior as one runtime topology.",
    directive: "An active profile is a routing decision. Review health and fallback order before changing it.",
    workflow: [
      { label: "Select", detail: "Choose the active profile path." },
      { label: "Health", detail: "Inspect limits and breaker state." },
      { label: "Fail over", detail: "Control retry and fallback behavior." },
    ],
    related: [
      { label: "Inference ledger", href: "/function/llm-records", icon: "records" },
      { label: "Agent roster", href: "/system/agents", icon: "agents" },
      { label: "Configuration", href: "/system/settings", icon: "settings" },
    ],
  },
  {
    id: "agents",
    match: "/system/agents",
    title: "Agent Roster",
    eyebrow: "Autonomous workforce",
    icon: "agents",
    serial: "FABRIC / CREW",
    brief: "Compare roles and effective capability bindings before opening an agent for composition.",
    directive: "Role labels describe intent; bindings determine effective capability. Review both.",
    workflow: [
      { label: "Survey", detail: "Compare built-in and custom roles." },
      { label: "Compose", detail: "Open one agent's capability set." },
      { label: "Verify", detail: "Confirm model and tool authority." },
    ],
    related: [
      { label: "Model fabric", href: "/system/llm", icon: "llm" },
      { label: "Toolchain", href: "/system/tools", icon: "tools" },
      { label: "Skills", href: "/system/skills", icon: "skills" },
    ],
  },
  {
    id: "mcp-mesh",
    match: "/system/mcp",
    title: "MCP Mesh",
    eyebrow: "External capability plane",
    icon: "mcp",
    serial: "FABRIC / MCP",
    brief: "Manage external tool servers as connections, authorization boundaries, and visible capability sets.",
    directive: "Enabled transport and per-agent visibility are independent controls. Validate both.",
    workflow: [
      { label: "Connect", detail: "Configure transport and endpoint." },
      { label: "Authorize", detail: "Choose agent visibility." },
      { label: "Observe", detail: "Confirm exposed tool inventory." },
    ],
    related: [
      { label: "Agent roster", href: "/system/agents", icon: "agents" },
      { label: "Toolchain", href: "/system/tools", icon: "tools" },
      { label: "System log", href: "/system/logs", icon: "logs" },
    ],
  },
  {
    id: "skills",
    match: "/system/skills",
    title: "Capability Library",
    eyebrow: "Reusable operating knowledge",
    icon: "skills",
    serial: "FABRIC / SKILLS",
    brief: "Curate reusable instructions, understand their source, and bind only the capabilities an agent needs.",
    directive: "A skill changes operating behavior even without adding a tool. Review its instructions before binding.",
    workflow: [
      { label: "Curate", detail: "Browse built-in and custom skills." },
      { label: "Inspect", detail: "Read source and instructions." },
      { label: "Bind", detail: "Assign skills to agents." },
    ],
    related: [
      { label: "Agent roster", href: "/system/agents", icon: "agents" },
      { label: "Toolchain", href: "/system/tools", icon: "tools" },
      { label: "MCP mesh", href: "/system/mcp", icon: "mcp" },
    ],
  },
  {
    id: "tools",
    match: "/system/tools",
    title: "Toolchain Registry",
    eyebrow: "Execution capabilities",
    icon: "tools",
    serial: "FABRIC / TOOLS",
    brief: "Understand what each tool does, which defaults it applies, and which agents can invoke it.",
    directive: "Descriptions and defaults enter the agent's operating context; keep them precise and reviewable.",
    workflow: [
      { label: "Describe", detail: "Review purpose and parameters." },
      { label: "Default", detail: "Set safe execution defaults." },
      { label: "Bind", detail: "Control agent availability." },
    ],
    related: [
      { label: "Agent roster", href: "/system/agents", icon: "agents" },
      { label: "Tool executions", href: "/function/commands", icon: "commands" },
      { label: "Command rules", href: "/system/intercept", icon: "intercept" },
    ],
  },
  {
    id: "signals",
    framed: false,
    match: "/system/notify",
    title: "Signal Router",
    eyebrow: "Outbound notifications",
    icon: "notifications",
    serial: "FABRIC / SIGNALS",
    brief: "Compose delivery channels, test them, and inspect recent delivery outcomes in one routing station.",
    directive: "Test saved destinations directly; review queued events and retry failures in the delivery ledger.",
    workflow: [
      { label: "Route", detail: "Configure channel and events." },
      { label: "Test", detail: "Send a controlled test signal." },
      { label: "Audit", detail: "Inspect delivery history." },
    ],
    related: [
      { label: "Finding registry", href: "/function/findings", icon: "findings" },
      { label: "System log", href: "/system/logs", icon: "logs" },
      { label: "Configuration", href: "/system/settings", icon: "settings" },
    ],
  },
  {
    id: "intercept",
    framed: false,
    match: "/system/intercept",
    title: "Command Policy",
    eyebrow: "Execution safety boundary",
    icon: "intercept",
    serial: "POLICY / COMMAND",
    brief: "Read command interception as an ordered decision system, not a flat collection of regex rows.",
    directive: "The first matching rule wins. Priority and target field are part of the policy, not display metadata.",
    workflow: [
      { label: "Scope", detail: "Choose protected tools." },
      { label: "Match", detail: "Order patterns and target fields." },
      { label: "Decide", detail: "Ask, allow, or deny explicitly." },
    ],
    related: [
      { label: "Asset guard", href: "/system/intercept/assets", icon: "blocked" },
      { label: "Approval queue", href: "/system/intercept/approvals", icon: "approvals" },
      { label: "Toolchain", href: "/system/tools", icon: "tools" },
    ],
  },
  {
    id: "logs",
    match: "/system/logs",
    title: "Runtime Console",
    eyebrow: "Live backend telemetry",
    icon: "logs",
    serial: "RUNTIME / STREAM",
    brief: "Tail the backend as a live signal stream, isolate anomalous components, and retain temporal context.",
    directive: "Pause before deep inspection so incoming lines do not move the event you are diagnosing.",
    workflow: [
      { label: "Tail", detail: "Watch the live ordered stream." },
      { label: "Filter", detail: "Narrow by level, tag, or text." },
      { label: "Diagnose", detail: "Correlate warnings and errors." },
    ],
    related: [
      { label: "Tool executions", href: "/function/commands", icon: "commands" },
      { label: "Inference ledger", href: "/function/llm-records", icon: "records" },
      { label: "Approval queue", href: "/system/intercept/approvals", icon: "approvals" },
    ],
  },
  {
    id: "settings",
    match: "/system/settings",
    title: "Control Matrix",
    eyebrow: "Runtime configuration",
    icon: "settings",
    serial: "CONTROL / CONFIG",
    brief:
      "Navigate runtime switches by operational domain, understand their blast radius, and test before relying on them.",
    directive: "Settings can rebuild agents or alter routing immediately. Read impact text before changing a switch.",
    workflow: [
      { label: "Navigate", detail: "Choose an operational domain." },
      { label: "Configure", detail: "Change one bounded control." },
      { label: "Verify", detail: "Use the available test action." },
    ],
    related: [
      { label: "Model fabric", href: "/system/llm", icon: "llm" },
      { label: "System log", href: "/system/logs", icon: "logs" },
      { label: "Signal router", href: "/system/notify", icon: "notifications" },
    ],
  },
];

export function consoleModuleForPath(pathname: string) {
  const normalized = (() => {
    try {
      return decodeURIComponent(pathname).replace(/\/+$/, "") || "/";
    } catch {
      return pathname.replace(/\/+$/, "") || "/";
    }
  })();

  return [...modules]
    .sort((a, b) => b.match.length - a.match.length)
    .find((module) => normalized === module.match || normalized.startsWith(`${module.match}/`));
}

function focusPrimaryControl() {
  const root = document.querySelector<HTMLElement>(".uro-module-stage");
  const control = root?.querySelector<HTMLElement>(
    'input[type="search"], input[placeholder*="Search"], input[placeholder*="search"], input[placeholder*="Filter"], input[placeholder*="filter"]',
  );
  control?.focus();
}

export function UroborosModuleWorkspace({ module, children }: { module: ConsoleModule; children: ReactNode }) {
  const [railOpen, setRailOpen] = React.useState(true);

  React.useEffect(() => {
    const stored = window.localStorage.getItem("uroboros-module-rail");
    if (stored === "closed") setRailOpen(false);

    const onKeyDown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null;
      const typing = target?.matches("input, textarea, select, [contenteditable=true]");
      if (!typing && event.key === "/") {
        event.preventDefault();
        focusPrimaryControl();
      }
      if (!typing && event.key === "]") {
        event.preventDefault();
        setRailOpen((current) => !current);
      }
    };
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, []);

  const toggleRail = () => {
    setRailOpen((current) => {
      const next = !current;
      window.localStorage.setItem("uroboros-module-rail", next ? "open" : "closed");
      return next;
    });
  };

  return (
    <section className={cn("uro-module-workspace", !railOpen && "is-rail-collapsed")} data-module={module.id}>
      <header className="uro-module-briefing">
        <div className="uro-module-ident">
          <span className="uro-module-icon">
            <UroIcon name={module.icon} />
          </span>
          <span className="uro-module-serial">{module.serial}</span>
        </div>
        <div className="uro-module-titleblock">
          <span>{module.eyebrow}</span>
          <h1>{module.title}</h1>
          <p>{module.brief}</p>
        </div>
        <div className="uro-module-briefing-actions">
          <button type="button" onClick={focusPrimaryControl} title="Focus this workspace's primary filter (/)">
            <UroIcon name="search" />
            <span>Focus</span>
            <kbd>/</kbd>
          </button>
          <button type="button" onClick={toggleRail} title="Toggle decision rail (])">
            <UroIcon name="panel" />
            <span>{railOpen ? "Close rail" : "Open rail"}</span>
            <kbd>]</kbd>
          </button>
        </div>
      </header>

      <ol className="uro-module-workflow" aria-label={`${module.title} workflow`}>
        {module.workflow.map((step, index) => (
          <li key={step.label}>
            <span>{String(index + 1).padStart(2, "0")}</span>
            <div>
              <strong>{step.label}</strong>
              <small>{step.detail}</small>
            </div>
          </li>
        ))}
      </ol>

      <div className="uro-module-body">
        <main className="uro-module-stage">{children}</main>
        <aside className="uro-module-rail" aria-label={`${module.title} decision rail`}>
          <div className="uro-module-rail-head">
            <span>Decision rail</span>
            <button type="button" onClick={toggleRail} aria-label="Close decision rail">
              <UroIcon name="close" />
            </button>
          </div>
          <section>
            <span className="uro-module-rail-label">Operating directive</span>
            <p>{module.directive}</p>
          </section>
          <section>
            <span className="uro-module-rail-label">Workflow lens</span>
            <ol className="uro-module-rail-steps">
              {module.workflow.map((step, index) => (
                <li key={step.label}>
                  <span>{index + 1}</span>
                  <div>
                    <strong>{step.label}</strong>
                    <small>{step.detail}</small>
                  </div>
                </li>
              ))}
            </ol>
          </section>
          <section>
            <span className="uro-module-rail-label">Related surfaces</span>
            <nav className="uro-module-crosslinks">
              {module.related.map((item) => (
                <Link key={item.href} href={item.href}>
                  <UroIcon name={item.icon} />
                  <span>{item.label}</span>
                  <UroIcon name="arrow-up-right" />
                </Link>
              ))}
            </nav>
          </section>
          <footer>
            <UroIcon name="info" />
            <span>
              <kbd>/</kbd> focuses the primary filter. <kbd>]</kbd> toggles this rail.
            </span>
          </footer>
        </aside>
      </div>
    </section>
  );
}
