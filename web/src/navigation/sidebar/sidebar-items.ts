import type { UroIconName } from "@/components/uroboros-icons";

export type NavBadge = "new" | "soon";

export interface NavSubItem {
  id: string;
  title: string;
  url: string;
  icon?: UroIconName;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

interface NavItemBase {
  id: string;
  title: string;
  icon?: UroIconName;
  badge?: NavBadge;
  disabled?: boolean;
  newTab?: boolean;
}

export interface NavMainLinkItem extends NavItemBase {
  url: string;
  subItems?: never;
}

export interface NavMainParentItem extends NavItemBase {
  subItems: NavSubItem[];
}

export type NavMainItem = NavMainLinkItem | NavMainParentItem;

export interface NavGroup {
  id: number;
  label?: string;
  items: NavMainItem[];
}

export const sidebarItems: NavGroup[] = [
  {
    id: 1,
    label: "Operations",
    items: [
      { id: "dashboard", title: "Mission Control", url: "/dashboard", icon: "mission" },
      { id: "chat", title: "Command Chat", url: "/chat", icon: "chat" },
      { id: "tasks", title: "Operations", url: "/function/tasks", icon: "tasks" },
      { id: "findings", title: "Findings", url: "/function/findings", icon: "findings" },
      { id: "traffic", title: "Traffic", url: "/function/traffic", icon: "traffic" },
      { id: "commands", title: "Tool Runs", url: "/function/commands", icon: "commands" },
      { id: "llm-records", title: "Model Ledger", url: "/function/llm-records", icon: "records" },
      { id: "assets", title: "Asset Graph", url: "/function/assets", icon: "assets" },
      { id: "workspace", title: "Workspace", url: "/function/workspace", icon: "workspace" },
    ],
  },
  {
    id: 2,
    label: "System",
    items: [
      { id: "llm", title: "Model Fabric", url: "/system/llm", icon: "llm" },
      { id: "agents", title: "Agents", url: "/system/agents", icon: "agents" },
      { id: "mcp", title: "MCP Mesh", url: "/system/mcp", icon: "mcp" },
      { id: "skills", title: "Skills", url: "/system/skills", icon: "skills" },
      { id: "tools", title: "Toolchain", url: "/system/tools", icon: "tools" },
      { id: "notify", title: "Signals", url: "/system/notify", icon: "notifications" },
      { id: "intercept", title: "Intercept Rules", url: "/system/intercept", icon: "intercept" },
      { id: "asset-intercept", title: "Asset Guard", url: "/system/intercept/assets", icon: "blocked" },
      { id: "approvals", title: "Approvals", url: "/system/intercept/approvals", icon: "approvals" },
      { id: "logs", title: "System Log", url: "/system/logs", icon: "logs" },
      { id: "settings", title: "Configuration", url: "/system/settings", icon: "settings" },
    ],
  },
];
