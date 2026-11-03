"use client";
import {
  type ComponentProps,
  type CSSProperties,
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useReducer,
} from "react";

import { Button } from "@/components/ui/button";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { UroIcon } from "@/components/uroboros-icons";
import { useIsMobile } from "@/hooks/use-mobile";
import { cn } from "@/lib/utils";

import { sidebarPolicy, sidebarPreference, sidebarRequest, sidebarVisibility } from "./sidebar-policy";
import "./sidebar-frame.css";

type SidebarSession = {
  state: "expanded" | "collapsed";
  open: boolean;
  setOpen: (value: boolean | ((current: boolean) => boolean)) => void;
  openMobile: boolean;
  setOpenMobile: (value: boolean) => void;
  isMobile: boolean;
  toggleSidebar: () => void;
};
const SidebarSession = createContext<SidebarSession | null>(null);
export function useSidebar() {
  const session = useContext(SidebarSession);
  if (!session) throw new Error("useSidebar must be used within a SidebarProvider.");
  return session;
}
export function SidebarProvider({
  defaultOpen = true,
  open: controlled,
  onOpenChange,
  className,
  style,
  children,
  ...props
}: ComponentProps<"div"> & { defaultOpen?: boolean; open?: boolean; onOpenChange?: (open: boolean) => void }) {
  const [visibility, dispatch] = useReducer(sidebarVisibility, { desktop: defaultOpen, mobile: false });
  const isMobile = useIsMobile(),
    open = controlled ?? visibility.desktop;
  const setOpen = useCallback(
    (request: boolean | ((current: boolean) => boolean)) => {
      const next = sidebarRequest(request, open);
      if (onOpenChange) onOpenChange(next);
      else dispatch({ channel: "desktop", value: next });
      document.cookie = sidebarPreference(next);
    },
    [onOpenChange, open],
  );
  const setOpenMobile = useCallback((value: boolean) => dispatch({ channel: "mobile", value }), []);
  const toggleSidebar = useCallback(() => {
    if (isMobile) dispatch({ channel: "mobile", value: "toggle" });
    else setOpen((current) => !current);
  }, [isMobile, setOpen]);
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if (event.key === "b" && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        toggleSidebar();
      }
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, [toggleSidebar]);
  const session = useMemo<SidebarSession>(
    () => ({
      state: open ? "expanded" : "collapsed",
      open,
      setOpen,
      openMobile: visibility.mobile,
      setOpenMobile,
      isMobile,
      toggleSidebar,
    }),
    [open, setOpen, visibility.mobile, setOpenMobile, isMobile, toggleSidebar],
  );
  return (
    <SidebarSession.Provider value={session}>
      <div
        data-slot="sidebar-wrapper"
        data-sidebar-engine="astra"
        className={cn("astra-sidebar-layout group/sidebar-wrapper", className)}
        style={
          {
            "--sidebar-width": sidebarPolicy.width,
            "--sidebar-width-icon": sidebarPolicy.iconWidth,
            ...style,
          } as CSSProperties
        }
        {...props}
      >
        {children}
      </div>
    </SidebarSession.Provider>
  );
}

export function Sidebar({
  side = "left",
  variant = "sidebar",
  collapsible = "offcanvas",
  className,
  children,
  dir,
  ...props
}: ComponentProps<"div"> & {
  side?: "left" | "right";
  variant?: "sidebar" | "floating" | "inset";
  collapsible?: "offcanvas" | "icon" | "none";
}) {
  const session = useSidebar();
  if (collapsible === "none")
    return (
      <div data-slot="sidebar" data-sidebar="sidebar" className={cn("astra-sidebar-static", className)} {...props}>
        {children}
      </div>
    );
  if (session.isMobile)
    return (
      <Sheet open={session.openMobile} onOpenChange={session.setOpenMobile} {...props}>
        <SheetContent
          dir={dir}
          side={side}
          data-sidebar="sidebar"
          data-slot="sidebar"
          data-mobile="true"
          className="astra-sidebar-mobile"
          style={{ "--sidebar-width": sidebarPolicy.mobileWidth } as CSSProperties}
        >
          <SheetHeader className="sr-only">
            <SheetTitle>Sidebar</SheetTitle>
            <SheetDescription>Displays the mobile sidebar.</SheetDescription>
          </SheetHeader>
          <div className="astra-sidebar-mobile-body">{children}</div>
        </SheetContent>
      </Sheet>
    );
  return (
    <div
      className="astra-sidebar-frame group peer"
      data-slot="sidebar"
      data-state={session.state}
      data-collapsible={session.state === "collapsed" ? collapsible : ""}
      data-variant={variant}
      data-side={side}
    >
      <div className="astra-sidebar-gap" data-slot="sidebar-gap" />
      <div data-slot="sidebar-container" data-side={side} className={cn("astra-sidebar-panel", className)} {...props}>
        <div data-slot="sidebar-inner" data-sidebar="sidebar" className="astra-sidebar-inner">
          {children}
        </div>
      </div>
    </div>
  );
}
export function SidebarTrigger({ className, onClick, ...props }: ComponentProps<typeof Button>) {
  const { toggleSidebar } = useSidebar();
  return (
    <Button
      data-sidebar="trigger"
      data-slot="sidebar-trigger"
      variant="ghost"
      size="icon-sm"
      className={className}
      onClick={(event) => {
        onClick?.(event);
        toggleSidebar();
      }}
      {...props}
    >
      <UroIcon name="panel" />
      <span className="sr-only">Toggle Sidebar</span>
    </Button>
  );
}
export function SidebarRail({ className, ...props }: ComponentProps<"button">) {
  const { toggleSidebar } = useSidebar();
  return (
    <button
      type="button"
      data-sidebar="rail"
      data-slot="sidebar-rail"
      aria-label="Toggle Sidebar"
      title="Toggle Sidebar"
      tabIndex={-1}
      onClick={toggleSidebar}
      className={cn("astra-sidebar-rail", className)}
      {...props}
    />
  );
}
export function SidebarInset({ className, ...props }: ComponentProps<"main">) {
  return <main data-slot="sidebar-inset" className={cn("astra-sidebar-main", className)} {...props} />;
}
