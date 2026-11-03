"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";

import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuGroup,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuBadge,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarMenuSub,
  SidebarMenuSubButton,
  SidebarMenuSubItem,
  useSidebar,
} from "@/components/ui/sidebar";
import { UroIcon } from "@/components/uroboros-icons";
import type {
  NavBadge,
  NavGroup,
  NavMainItem,
  NavMainLinkItem,
  NavMainParentItem,
} from "@/navigation/sidebar/sidebar-items";

import { navigationCount, navigationRoute, navigationTarget } from "./navigation-document";
import { useNavigationPending } from "./use-navigation-pending";

function NavigationIcon({ item, fallback }: { item: NavMainItem; fallback: boolean }) {
  if (item.icon) return <UroIcon name={item.icon} />;
  return fallback ? (
    <span className="astra-navigation-initial" aria-hidden="true">
      {item.title.slice(0, 1)}
    </span>
  ) : null;
}
function PendingBadge() {
  const count = useNavigationPending();
  return count ? (
    <SidebarMenuBadge className="astra-navigation-pending" aria-label={`${count} pending approvals`}>
      {navigationCount(count)}
    </SidebarMenuBadge>
  ) : null;
}
function NavigationBadge({ badge, pending = false }: { badge?: NavBadge; pending?: boolean }) {
  if (pending) return <PendingBadge />;
  return badge ? (
    <SidebarMenuBadge className="astra-navigation-badge" data-badge={badge}>
      {badge}
    </SidebarMenuBadge>
  ) : null;
}
function NavigationLeaf({ item, active, compact }: { item: NavMainLinkItem; active: boolean; compact: boolean }) {
  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild isActive={active} aria-disabled={item.disabled} tooltip={item.title}>
        <Link prefetch={false} href={item.url} {...navigationTarget(item)} aria-current={active ? "page" : undefined}>
          <NavigationIcon item={item} fallback={compact} />
          <span className="astra-navigation-label">{item.title}</span>
        </Link>
      </SidebarMenuButton>
      <NavigationBadge badge={item.badge} pending={item.id === "approvals"} />
    </SidebarMenuItem>
  );
}
function NavigationBranch({ item, path, compact }: { item: NavMainParentItem; path: string; compact: boolean }) {
  const route = navigationRoute(path, item);
  const label = (
    <>
      <NavigationIcon item={item} fallback={compact} />
      <span className="astra-navigation-label">{item.title}</span>
    </>
  );
  if (compact)
    return (
      <SidebarMenuItem>
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <SidebarMenuButton isActive={route.active} disabled={item.disabled} tooltip={item.title}>
              {label}
            </SidebarMenuButton>
          </DropdownMenuTrigger>
          <DropdownMenuContent side="right" align="start" sideOffset={12} className="w-48">
            <DropdownMenuGroup>
              {item.subItems.map((child, index) => (
                <DropdownMenuItem asChild key={child.id} disabled={child.disabled}>
                  <Link
                    prefetch={false}
                    href={child.url}
                    {...navigationTarget(child)}
                    aria-current={route.children[index]?.active ? "page" : undefined}
                    className="astra-navigation-dropdown-link"
                  >
                    {child.icon && <UroIcon name={child.icon} />}
                    <span>{child.title}</span>
                  </Link>
                </DropdownMenuItem>
              ))}
            </DropdownMenuGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      </SidebarMenuItem>
    );
  return (
    <Collapsible asChild defaultOpen={route.expanded}>
      <SidebarMenuItem>
        <CollapsibleTrigger asChild>
          <SidebarMenuButton isActive={route.active} disabled={item.disabled} tooltip={item.title}>
            {label}
            <UroIcon name="chevron" className="astra-navigation-chevron" />
          </SidebarMenuButton>
        </CollapsibleTrigger>
        <NavigationBadge badge={item.badge} />
        <CollapsibleContent>
          <SidebarMenuSub>
            {item.subItems.map((child, index) => (
              <SidebarMenuSubItem key={child.id}>
                <SidebarMenuSubButton asChild isActive={route.children[index]?.active} aria-disabled={child.disabled}>
                  <Link
                    prefetch={false}
                    href={child.url}
                    {...navigationTarget(child)}
                    aria-current={route.children[index]?.active ? "page" : undefined}
                  >
                    {child.icon && <UroIcon name={child.icon} />}
                    <span>{child.title}</span>
                  </Link>
                </SidebarMenuSubButton>
              </SidebarMenuSubItem>
            ))}
          </SidebarMenuSub>
        </CollapsibleContent>
      </SidebarMenuItem>
    </Collapsible>
  );
}
export function NavigationTree({ items }: { readonly items: readonly NavGroup[] }) {
  const path = usePathname(),
    { state, isMobile } = useSidebar(),
    compact = state === "collapsed" && !isMobile;
  return (
    <>
      {items.map((group) => (
        <SidebarGroup key={group.id}>
          {group.label && <SidebarGroupLabel>{group.label}</SidebarGroupLabel>}
          <SidebarGroupContent>
            <SidebarMenu>
              {group.items.map((item) =>
                "url" in item && item.url !== undefined ? (
                  <NavigationLeaf
                    key={item.id}
                    item={item as NavMainLinkItem}
                    active={navigationRoute(path, item).active}
                    compact={compact}
                  />
                ) : (
                  <NavigationBranch key={item.id} item={item as NavMainParentItem} path={path} compact={compact} />
                ),
              )}
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
      ))}
    </>
  );
}
