import * as React from "react";

/**
 * Uroboros' interface glyphs deliberately avoid a third-party icon silhouette.
 * Every mark is drawn on the same 24 × 24 circuit grid and uses a squared,
 * technical stroke language shared with the product symbol.
 */
export type UroIconName =
  | "mission"
  | "chat"
  | "tasks"
  | "findings"
  | "traffic"
  | "commands"
  | "records"
  | "assets"
  | "sync"
  | "workspace"
  | "llm"
  | "agents"
  | "mcp"
  | "skills"
  | "tools"
  | "notifications"
  | "intercept"
  | "blocked"
  | "approvals"
  | "logs"
  | "settings"
  | "search"
  | "pulse"
  | "route"
  | "topology"
  | "coverage"
  | "asset-company"
  | "asset-domain"
  | "asset-subdomain"
  | "asset-ip"
  | "asset-service"
  | "asset-app"
  | "asset-endpoint"
  | "replay"
  | "fit"
  | "arrange"
  | "zoom-in"
  | "zoom-out"
  | "chevron"
  | "arrow-up-right"
  | "clock"
  | "shield"
  | "token"
  | "user"
  | "panel"
  | "filter"
  | "download"
  | "close"
  | "plus"
  | "eye"
  | "info";

const drawings: Record<UroIconName, React.ReactNode> = {
  mission: (
    <>
      <path d="M4 13.5V5.8L9.2 3h5.6L20 5.8v7.7l-5.2 2.9H9.2L4 13.5Z" />
      <path d="M8.2 13.1V8.4l3.8-2.1 3.8 2.1v4.7L12 15.2l-3.8-2.1Z" />
      <path d="M12 15.4V21M8.8 21h6.4" />
    </>
  ),
  chat: (
    <>
      <path d="M4 5.5h16v10H9l-5 4v-14Z" />
      <path d="M8 9h8M8 12h5" />
    </>
  ),
  tasks: (
    <>
      <circle cx="12" cy="12" r="7.5" />
      <circle cx="12" cy="12" r="3" />
      <path d="M12 2v3M22 12h-3M12 22v-3M2 12h3M14.2 9.8 20 4" />
    </>
  ),
  findings: (
    <>
      <path d="M8.2 8.2 5.5 5.5M15.8 8.2l2.7-2.7M7 12H3M21 12h-4M8.1 16.2l-2.6 2.6M15.9 16.2l2.6 2.6" />
      <path d="M8 10.2V8.5l4-2.3 4 2.3v7L12 18l-4-2.5v-5.3Z" />
      <path d="M12 6.2V3.5M9.5 12h5M12 9.5v5" />
    </>
  ),
  traffic: (
    <>
      <path d="M3 8h13M13 5l3 3-3 3M21 16H8M11 13l-3 3 3 3" />
      <circle cx="4" cy="16" r="1.2" />
      <circle cx="20" cy="8" r="1.2" />
    </>
  ),
  commands: (
    <>
      <path d="M4 5h16v14H4z" />
      <path d="m7 9 3 3-3 3M12.5 15H17" />
    </>
  ),
  records: (
    <>
      <path d="M5 4h14v16H5z" />
      <path d="M8 8h8M8 12h5M8 16h8" />
      <circle cx="17" cy="12" r="1" />
    </>
  ),
  assets: (
    <>
      <circle cx="5" cy="12" r="2" />
      <circle cx="12" cy="5" r="2" />
      <circle cx="19" cy="12" r="2" />
      <circle cx="12" cy="19" r="2" />
      <path d="m6.5 10.5 4-4M13.5 6.5l4 4M17.5 13.5l-4 4M10.5 17.5l-4-4" />
    </>
  ),
  sync: (
    <>
      <path d="M5.2 9A7 7 0 0 1 17 6.2L19.5 9M18.8 15A7 7 0 0 1 7 17.8L4.5 15" />
      <path d="M19.5 4.5V9H15M4.5 19.5V15H9" />
    </>
  ),
  workspace: (
    <>
      <path d="M3.5 6.5h6l1.5 2h9.5v10h-17z" />
      <path d="M3.5 9h17M8 13h8M8 16h5" />
    </>
  ),
  llm: (
    <>
      <path d="M7 6.5 12 3l5 3.5v5L12 15l-5-3.5z" />
      <path d="M9 17.5h6M12 15v6M5 9H2M22 9h-3" />
      <circle cx="10" cy="9" r=".8" fill="currentColor" stroke="none" />
      <circle cx="14" cy="9" r=".8" fill="currentColor" stroke="none" />
    </>
  ),
  agents: (
    <>
      <path d="M6 8.5 12 5l6 3.5v7L12 19l-6-3.5z" />
      <path d="M9 11h6M12 8v6M12 5V2.5" />
      <circle cx="12" cy="2.5" r="1" />
    </>
  ),
  mcp: (
    <>
      <path d="M8 7V4M16 7V4M7 7h10v5a5 5 0 0 1-10 0V7Z" />
      <path d="M12 17v4M9 21h6" />
    </>
  ),
  skills: (
    <>
      <path d="m12 3 1.6 5.1L19 9.5l-4.2 3.2.3 5.5-3.1-1.8-3.1 1.8.3-5.5L5 9.5l5.4-1.4L12 3Z" />
      <circle cx="12" cy="11.5" r="1.4" />
    </>
  ),
  tools: (
    <>
      <path d="m13.5 5.2 5.3 5.3M11.2 7.5l5.3 5.3" />
      <path d="M14 4.7 16.7 2 22 7.3 19.3 10M10 11l-7 7v3h3l7-7" />
    </>
  ),
  notifications: (
    <>
      <path d="M6 16h12l-1.5-2V9a4.5 4.5 0 0 0-9 0v5L6 16Z" />
      <path d="M10 19h4M12 3V1.5" />
    </>
  ),
  intercept: (
    <>
      <path d="M12 3 19 6v5c0 4.7-2.7 7.8-7 10-4.3-2.2-7-5.3-7-10V6l7-3Z" />
      <path d="M8 12h8M12 8v8" />
    </>
  ),
  blocked: (
    <>
      <circle cx="12" cy="12" r="8" />
      <path d="m6.4 6.4 11.2 11.2" />
    </>
  ),
  approvals: (
    <>
      <path d="M7 3h10v18H7zM10 3V1.5h4V3" />
      <path d="m9.5 12 1.7 1.7 3.8-4" />
    </>
  ),
  logs: (
    <>
      <path d="M5 3h14v18H5z" />
      <path d="M8 8h8M8 12h8M8 16h5" />
      <path d="M2.5 7H5M2.5 12H5M2.5 17H5" />
    </>
  ),
  settings: (
    <>
      <circle cx="12" cy="12" r="3" />
      <path d="M12 2.5v3M12 18.5v3M2.5 12h3M18.5 12h3M5.3 5.3l2.1 2.1M16.6 16.6l2.1 2.1M18.7 5.3l-2.1 2.1M7.4 16.6l-2.1 2.1" />
    </>
  ),
  search: (
    <>
      <circle cx="10.5" cy="10.5" r="6.5" />
      <path d="m15.2 15.2 5.3 5.3M8 10.5h5M10.5 8v5" />
    </>
  ),
  pulse: <path d="M2 12h4l2-5 4 10 2.2-5H22" />,
  route: (
    <>
      <circle cx="5" cy="18" r="2" />
      <circle cx="19" cy="6" r="2" />
      <path d="M7 18h3a3 3 0 0 0 3-3V9a3 3 0 0 1 3-3h1" />
      <path d="m9 11 4 4 4-4" />
    </>
  ),
  topology: (
    <>
      <circle cx="5" cy="12" r="2" />
      <circle cx="12" cy="5" r="2" />
      <circle cx="19" cy="9" r="2" />
      <circle cx="16" cy="18" r="2" />
      <path d="m6.5 10.5 4-4M13.8 5.7l3.4 2M18.5 11l-1.8 5M14 17.5 6.8 13" />
    </>
  ),
  coverage: (
    <>
      <path d="M12 3a9 9 0 1 0 9 9h-9V3Z" />
      <path d="M15 3.5A8 8 0 0 1 20.5 9H15V3.5Z" />
    </>
  ),
  "asset-company": (
    <>
      <path d="M4 20V8l8-4 8 4v12M8 20v-4h8v4" />
      <path d="M8 10h1M12 10h1M16 10h1M8 13h1M12 13h1M16 13h1" />
    </>
  ),
  "asset-domain": (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M3.5 12h17M12 3.5c2.4 2.3 3.6 5.1 3.6 8.5S14.4 18.2 12 20.5M12 3.5C9.6 5.8 8.4 8.6 8.4 12s1.2 6.2 3.6 8.5" />
    </>
  ),
  "asset-subdomain": (
    <>
      <circle cx="5" cy="12" r="2" /><circle cx="12" cy="6" r="2" /><circle cx="19" cy="12" r="2" />
      <path d="m6.6 10.7 3.8-3.3M13.6 7.3l3.8 3.4M7 12h10M12 14v6M9 20h6" />
    </>
  ),
  "asset-ip": (
    <>
      <path d="M4 4h16v6H4zM4 14h16v6H4z" />
      <path d="M7 7h.1M10 7h.1M7 17h.1M10 17h.1M14 7h3M14 17h3" />
    </>
  ),
  "asset-service": (
    <>
      <circle cx="12" cy="12" r="2" />
      <path d="M8.5 8.5a5 5 0 0 0 0 7M15.5 8.5a5 5 0 0 1 0 7M5.7 5.7a9 9 0 0 0 0 12.6M18.3 5.7a9 9 0 0 1 0 12.6" />
    </>
  ),
  "asset-app": (
    <>
      <path d="M3.5 5h17v14h-17zM3.5 9h17" />
      <path d="M7 7h.1M10 7h.1M8 13h3v3H8zM14 13h2M14 16h2" />
    </>
  ),
  "asset-endpoint": (
    <>
      <path d="M4 7h7v4H4zM13 13h7v4h-7z" />
      <path d="M11 9h2a3 3 0 0 1 3 3v1M8 11v5a3 3 0 0 0 3 3h2" />
      <circle cx="8" cy="16" r="1" /><circle cx="16" cy="12" r="1" />
    </>
  ),
  replay: (
    <>
      <path d="M5.5 7.5A8 8 0 1 1 4 14" />
      <path d="M5.5 3.5v4h4M10 9l5 3-5 3V9Z" />
    </>
  ),
  fit: (
    <>
      <path d="M9 4H4v5M15 4h5v5M9 20H4v-5M15 20h5v-5" />
      <path d="M9 9h6v6H9z" />
    </>
  ),
  arrange: (
    <>
      <path d="M4 5h6v5H4zM14 14h6v5h-6z" />
      <path d="M10 7.5h3a3 3 0 0 1 3 3V14M13 11l3 3 3-3" />
    </>
  ),
  "zoom-in": (
    <>
      <circle cx="10.5" cy="10.5" r="6.5" />
      <path d="M8 10.5h5M10.5 8v5m4.7 2.2 5.3 5.3" />
    </>
  ),
  "zoom-out": (
    <>
      <circle cx="10.5" cy="10.5" r="6.5" />
      <path d="M8 10.5h5m2.2 4.7 5.3 5.3" />
    </>
  ),
  chevron: <path d="m9 5 7 7-7 7" />,
  "arrow-up-right": <path d="M6 18 18 6M9 6h9v9" />,
  clock: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 7v5l3.5 2" />
    </>
  ),
  shield: (
    <>
      <path d="M12 3 19 6v5c0 4.7-2.7 7.8-7 10-4.3-2.2-7-5.3-7-10V6l7-3Z" />
      <path d="m8.5 12 2.2 2.2 4.8-5" />
    </>
  ),
  token: (
    <>
      <path d="m12 3 7 4v10l-7 4-7-4V7l7-4Z" />
      <path d="m8.5 9 3.5-2 3.5 2v6L12 17l-3.5-2V9Z" />
    </>
  ),
  user: (
    <>
      <circle cx="12" cy="8" r="3.5" />
      <path d="M5.5 20c.5-4 2.8-6 6.5-6s6 2 6.5 6" />
    </>
  ),
  panel: (
    <>
      <path d="M3 4h18v16H3zM8 4v16" />
      <path d="M5 8h1M5 12h1M5 16h1" />
    </>
  ),
  filter: (
    <>
      <path d="M3 5h18l-7 8v5l-4 2v-7L3 5Z" />
    </>
  ),
  download: (
    <>
      <path d="M12 3v12M7.5 10.5 12 15l4.5-4.5" />
      <path d="M4 17v3h16v-3" />
    </>
  ),
  close: <path d="m5 5 14 14M19 5 5 19" />,
  plus: <path d="M12 4v16M4 12h16" />,
  eye: (
    <>
      <path d="M2.5 12S6 6.5 12 6.5 21.5 12 21.5 12 18 17.5 12 17.5 2.5 12 2.5 12Z" />
      <circle cx="12" cy="12" r="2.5" />
    </>
  ),
  info: (
    <>
      <circle cx="12" cy="12" r="8.5" />
      <path d="M12 10v6M12 7.2v.2" />
    </>
  ),
};

export interface UroIconProps extends Omit<React.SVGProps<SVGSVGElement>, "name"> {
  name: UroIconName;
}

export const UroIcon = React.forwardRef<SVGSVGElement, UroIconProps>(function UroIcon(
  { name, className, ...props },
  ref,
) {
  return (
    <svg
      ref={ref}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.55"
      strokeLinecap="round"
      strokeLinejoin="round"
      vectorEffect="non-scaling-stroke"
      aria-hidden="true"
      className={className}
      {...props}
    >
      {drawings[name]}
    </svg>
  );
});
