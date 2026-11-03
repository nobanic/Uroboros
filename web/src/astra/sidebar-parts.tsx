"use client";
import { type ComponentProps, createElement, useState } from "react";

import { Slot } from "radix-ui";

import { Input } from "@/components/ui/input";
import { Separator } from "@/components/ui/separator";
import { Skeleton } from "@/components/ui/skeleton";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

import { useSidebar } from "./sidebar-frame";

function part<Tag extends "div" | "ul" | "li">(tag: Tag, kind: string) {
  return function SidebarPart({ className, ...props }: ComponentProps<Tag>) {
    return createElement(tag, {
      "data-slot": `sidebar-${kind}`,
      "data-sidebar": kind,
      ...props,
      className: cn(`astra-sidebar-${kind}`, className),
    });
  };
}
export const SidebarHeader = part("div", "header");
export const SidebarFooter = part("div", "footer");
export const SidebarContent = part("div", "content");
export const SidebarGroup = part("div", "group");
export const SidebarGroupContent = part("div", "group-content");
export const SidebarMenu = part("ul", "menu");
export const SidebarMenuItem = part("li", "menu-item");
export const SidebarMenuBadge = part("div", "menu-badge");
export const SidebarMenuSub = part("ul", "menu-sub");
export const SidebarMenuSubItem = part("li", "menu-sub-item");
export function SidebarInput({ className, ...props }: ComponentProps<typeof Input>) {
  return (
    <Input data-slot="sidebar-input" data-sidebar="input" className={cn("astra-sidebar-input", className)} {...props} />
  );
}
export function SidebarSeparator({ className, ...props }: ComponentProps<typeof Separator>) {
  return (
    <Separator
      data-slot="sidebar-separator"
      data-sidebar="separator"
      className={cn("astra-sidebar-separator", className)}
      {...props}
    />
  );
}
export function SidebarGroupLabel({
  asChild = false,
  className,
  ...props
}: ComponentProps<"div"> & { asChild?: boolean }) {
  const Host = asChild ? Slot.Root : "div";
  return (
    <Host
      data-slot="sidebar-group-label"
      data-sidebar="group-label"
      className={cn("astra-sidebar-group-label", className)}
      {...props}
    />
  );
}
export function SidebarGroupAction({
  asChild = false,
  className,
  ...props
}: ComponentProps<"button"> & { asChild?: boolean }) {
  const Host = asChild ? Slot.Root : "button";
  return (
    <Host
      data-slot="sidebar-group-action"
      data-sidebar="group-action"
      className={cn("astra-sidebar-group-action", className)}
      {...props}
    />
  );
}
export function SidebarMenuAction({
  asChild = false,
  showOnHover = false,
  className,
  ...props
}: ComponentProps<"button"> & { asChild?: boolean; showOnHover?: boolean }) {
  const Host = asChild ? Slot.Root : "button";
  return (
    <Host
      data-slot="sidebar-menu-action"
      data-sidebar="menu-action"
      data-hover-only={showOnHover}
      className={cn("astra-sidebar-menu-action", className)}
      {...props}
    />
  );
}
export function SidebarMenuButton({
  asChild = false,
  isActive = false,
  variant = "default",
  size = "default",
  tooltip,
  className,
  ...props
}: ComponentProps<"button"> & {
  asChild?: boolean;
  isActive?: boolean;
  variant?: "default" | "outline" | null;
  size?: "default" | "sm" | "lg" | null;
  tooltip?: string | ComponentProps<typeof TooltipContent>;
}) {
  const Host = asChild ? Slot.Root : "button",
    session = useSidebar();
  const content = (
    <Host
      data-slot="sidebar-menu-button"
      data-sidebar="menu-button"
      data-size={size}
      data-active={isActive}
      data-variant={variant}
      className={cn("astra-sidebar-menu-button peer/menu-button group/menu-button", className)}
      {...props}
    />
  );
  const hint = typeof tooltip === "string" ? { children: tooltip } : tooltip;
  const hidden = hint?.hidden ?? (session.state !== "collapsed" || session.isMobile);
  if (!hint || hidden) return content;
  return (
    <Tooltip>
      <TooltipTrigger asChild>{content}</TooltipTrigger>
      <TooltipContent side="right" align="center" {...hint} />
    </Tooltip>
  );
}
export function SidebarMenuSubButton({
  asChild = false,
  size = "md",
  isActive = false,
  className,
  ...props
}: ComponentProps<"a"> & { asChild?: boolean; size?: "sm" | "md"; isActive?: boolean }) {
  const Host = asChild ? Slot.Root : "a";
  return (
    <Host
      data-slot="sidebar-menu-sub-button"
      data-sidebar="menu-sub-button"
      data-size={size}
      data-active={isActive}
      className={cn("astra-sidebar-menu-sub-button", className)}
      {...props}
    />
  );
}
export function SidebarMenuSkeleton({
  showIcon = false,
  className,
  ...props
}: ComponentProps<"div"> & { showIcon?: boolean }) {
  const [width] = useState(() => `${50 + Math.floor(Math.random() * 40)}%`);
  return (
    <div
      data-slot="sidebar-menu-skeleton"
      data-sidebar="menu-skeleton"
      className={cn("astra-sidebar-menu-skeleton", className)}
      {...props}
    >
      {showIcon && <Skeleton data-sidebar="menu-skeleton-icon" className="astra-sidebar-skeleton-icon" />}
      <Skeleton data-sidebar="menu-skeleton-text" className="astra-sidebar-skeleton-text" style={{ maxWidth: width }} />
    </div>
  );
}
