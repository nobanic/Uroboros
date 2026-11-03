"use client";

import Link from "next/link";

import { ConsoleConnectionFooter } from "@/astra/console-connection";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "@/components/ui/sidebar";
import { APP_CONFIG } from "@/config/app-config";
import { useCurrentUser } from "@/hooks/use-current-user";
import { sidebarItems } from "@/navigation/sidebar/sidebar-items";

import { NavMain } from "./nav-main";
import { NavUser } from "./nav-user";
import { SidebarSupportCard } from "./sidebar-support-card";

export function AppSidebar({ ...props }: React.ComponentProps<typeof Sidebar>) {
  const currentUser = useCurrentUser();

  return (
    <Sidebar {...props} className="uro-sidebar">
      <SidebarHeader className="uro-sidebar-header">
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton asChild className="uro-brand-link">
              <Link prefetch={false} href="/dashboard" aria-label={`${APP_CONFIG.name} Mission Control`}>
                <span className="uro-brand-mark" aria-hidden="true">
                  {/* biome-ignore lint/performance/noImgElement: Preserve the supplied logo in the static export. */}
                  <img src="/uroboros/dark-blue/logo-symbol.png" alt="" width={36} height={36} />
                </span>
                <span className="uro-brand-copy">
                  <strong>{APP_CONFIG.name}</strong>
                  <small>Control plane</small>
                </span>
              </Link>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent className="uro-sidebar-content">
        <NavMain items={sidebarItems} />
      </SidebarContent>
      <SidebarFooter className="uro-sidebar-footer">
        <SidebarSupportCard />
        <ConsoleConnectionFooter />
        <NavUser user={currentUser} />
      </SidebarFooter>
    </Sidebar>
  );
}
