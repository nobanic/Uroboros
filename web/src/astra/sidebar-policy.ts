export const sidebarPolicy = {
  cookie: "sidebar_state",
  lifetime: 604800,
  width: "16rem",
  mobileWidth: "18rem",
  iconWidth: "3rem",
} as const;
export type SidebarVisibility = { desktop: boolean; mobile: boolean };
export type SidebarVisibilityEvent =
  | { channel: "desktop"; value: boolean }
  | { channel: "mobile"; value: boolean | "toggle" };
export function sidebarVisibility(state: SidebarVisibility, event: SidebarVisibilityEvent): SidebarVisibility {
  if (event.channel === "desktop") return { ...state, desktop: event.value };
  return { ...state, mobile: event.value === "toggle" ? !state.mobile : event.value };
}
export function sidebarPreference(open: boolean) {
  return `${sidebarPolicy.cookie}=${open}; path=/; max-age=${sidebarPolicy.lifetime}`;
}
export function sidebarRequest(value: boolean | ((open: boolean) => boolean), open: boolean) {
  return typeof value === "function" ? value(open) : value;
}
