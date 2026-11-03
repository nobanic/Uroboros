"use client";

import type { ReactNode } from "react";
import * as React from "react";

import { ConsoleConnectionProvider } from "@/astra/console-connection";
import { AppSidebar } from "@/app/(main)/_components/sidebar/app-sidebar";
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar";
import { AUTH_DISABLED, auth } from "@/lib/auth";
import { getClientCookie } from "@/lib/cookie.client";
import { cn } from "@/lib/utils";

import { MainContent } from "./_components/main-content";

export default function Layout({ children }: Readonly<{ children: ReactNode }>) {
  // Client-side auth gate — replaces the Next proxy/middleware that static export
  // disables. No token → bounce to /login; render nothing until confirmed so no
  // protected UI (or its API calls) flashes for a logged-out visitor.
  const [authed, setAuthed] = React.useState(AUTH_DISABLED);
  React.useEffect(() => {
    if (AUTH_DISABLED) return;
    if (auth.getToken()) {
      setAuthed(true);
    } else {
      // When the client-side guard decides we're not logged in, it must also clear the
      // cookie: otherwise proxy.ts, seeing only that "the cookie exists", redirects us
      // from /login back to the main UI, bouncing against this guard into an infinite
      // redirect → blank screen (triggered when cookie and localStorage disagree).
      auth.clearToken();
      window.location.href = "/login";
    }
  }, []);

  const defaultOpen = typeof document === "undefined" ? true : getClientCookie("sidebar_state") !== "false";
  if (!authed) return null;

  return (
    <ConsoleConnectionProvider>
      <SidebarProvider
        className="uro-console-shell"
        defaultOpen={defaultOpen}
        style={
          {
            "--sidebar-width": "15.75rem",
            "--sidebar-width-icon": "4.5rem",
          } as React.CSSProperties
        }
      >
        <AppSidebar variant="sidebar" collapsible="icon" />
        <SidebarInset
          className={cn(
            "uro-console-main",
            "[html[data-content-layout=centered]_&>*]:mx-auto",
            "[html[data-content-layout=centered]_&>*]:w-full",
            "[html[data-content-layout=centered]_&>*]:max-w-screen-2xl",
            "[--dashboard-header-height:--spacing(12)]",
            "min-w-0 overflow-x-hidden",
          )}
        >
          <MainContent>{children}</MainContent>
        </SidebarInset>
      </SidebarProvider>
    </ConsoleConnectionProvider>
  );
}
