"use client";

import type { ReactNode } from "react";

import { usePathname } from "next/navigation";

import { ConsoleConnectionBadge, useConsoleConnection } from "@/astra/console-connection";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { consoleModuleForPath, UroborosModuleWorkspace } from "@/components/uroboros-module-workspace";
import { useCurrentUser } from "@/hooks/use-current-user";
import { cn } from "@/lib/utils";

import { AccountSwitcher } from "./sidebar/account-switcher";
import { SearchDialog } from "./sidebar/search-dialog";
import { UpdateBadge } from "./update-badge";

const ROUTE_META: Array<{ match: string; title: string; eyebrow: string; icon: UroIconName }> = [
  { match: "/dashboard", title: "Mission Control", eyebrow: "Global posture", icon: "mission" },
  { match: "/chat", title: "Command Chat", eyebrow: "Agent channel", icon: "chat" },
  { match: "/function/tasks", title: "Operations", eyebrow: "Mission queue", icon: "tasks" },
  { match: "/function/findings", title: "Findings", eyebrow: "Evidence registry", icon: "findings" },
  { match: "/function/traffic", title: "Traffic", eyebrow: "Observed exchanges", icon: "traffic" },
  { match: "/function/commands", title: "Tool Runs", eyebrow: "Execution ledger", icon: "commands" },
  { match: "/function/llm-records", title: "Model Ledger", eyebrow: "Inference records", icon: "records" },
  { match: "/function/assets", title: "Asset Graph", eyebrow: "Known surface", icon: "assets" },
  { match: "/function/workspace", title: "Workspace", eyebrow: "Evidence files", icon: "workspace" },
  { match: "/system/llm", title: "Model Fabric", eyebrow: "System fabric", icon: "llm" },
  { match: "/system/agents", title: "Agents", eyebrow: "System fabric", icon: "agents" },
  { match: "/system/mcp", title: "MCP Mesh", eyebrow: "System fabric", icon: "mcp" },
  { match: "/system/skills", title: "Skills", eyebrow: "System fabric", icon: "skills" },
  { match: "/system/tools", title: "Toolchain", eyebrow: "System fabric", icon: "tools" },
  { match: "/system/notify", title: "Signals", eyebrow: "System fabric", icon: "notifications" },
  { match: "/system/intercept", title: "Intercept", eyebrow: "Safety boundary", icon: "intercept" },
  { match: "/system/logs", title: "System Log", eyebrow: "Runtime telemetry", icon: "logs" },
  { match: "/system/settings", title: "Configuration", eyebrow: "Control plane", icon: "settings" },
];

function routeMeta(pathname: string) {
  const module = consoleModuleForPath(pathname);
  if (module) return module;
  return (
    [...ROUTE_META].sort((a, b) => b.match.length - a.match.length).find((item) => pathname.startsWith(item.match)) ?? {
      title: "Control Plane",
      eyebrow: "Uroboros",
      icon: "mission" as const,
    }
  );
}

// Task detail owns its operation header and surface navigation; the global header would duplicate them.
function isFullBleed(pathname: string) {
  const p = (() => {
    try {
      return decodeURIComponent(pathname);
    } catch {
      return pathname;
    }
  })();
  // Static export enables trailingSlash, so the list page's own pathname is "/function/tasks/".
  // Strip the trailing slash before checking the prefix, otherwise the list page is mistaken for a detail page and loses the global header.
  const normalized = p.replace(/\/+$/, "");
  return normalized.startsWith("/function/tasks/");
}

export function MainContent({ children }: { children: ReactNode }) {
  const currentUser = useCurrentUser();
  const pathname = usePathname();
  const meta = routeMeta(pathname);
  const module = consoleModuleForPath(pathname);
  const {
    view: { version },
  } = useConsoleConnection();

  if (isFullBleed(pathname)) {
    return <>{children}</>;
  }

  return (
    <>
      <header
        data-slot="workspace-header"
        className={cn(
          "uro-command-bar flex h-16 shrink-0 items-center gap-2 transition-[width,height] ease-linear",
          "[html[data-navbar-style=sticky]_&]:sticky [html[data-navbar-style=sticky]_&]:top-0 [html[data-navbar-style=sticky]_&]:z-50",
        )}
      >
        <div className="flex w-full min-w-0 items-center gap-3 px-3 lg:px-5">
          <div className="uro-command-context">
            <SidebarTrigger className="uro-icon-button" />
            <span className="uro-route-glyph">
              <UroIcon name={meta.icon} />
            </span>
            <span className="uro-route-copy">
              <small>{meta.eyebrow}</small>
              <strong>{meta.title}</strong>
            </span>
          </div>
          <div className="min-w-0 flex-1 sm:max-w-md">
            <SearchDialog />
          </div>
          <div className="uro-command-actions">
            <ConsoleConnectionBadge />
            {version && <span className="uro-version-tag">v{version}</span>}
            <UpdateBadge />
            <AccountSwitcher users={[currentUser]} />
          </div>
        </div>
      </header>
      <div className="uro-workspace min-h-0 min-w-0 flex-1 overflow-x-hidden p-4 has-data-[content-padding=false]:p-0 md:p-5 md:has-data-[content-padding=false]:p-0 xl:p-6">
        {module && module.framed !== false ? (
          <UroborosModuleWorkspace module={module}>{children}</UroborosModuleWorkspace>
        ) : (
          children
        )}
      </div>
    </>
  );
}
