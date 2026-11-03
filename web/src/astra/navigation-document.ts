import type { NavMainItem } from "@/navigation/sidebar/sidebar-items";

export function navigationPath(raw: string) {
  try {
    return decodeURIComponent(raw);
  } catch {
    return raw;
  }
}
export function navigationRoute(raw: string, item: NavMainItem) {
  const path = navigationPath(raw),
    children = item.subItems ?? [];
  return {
    active: children.length
      ? children.some((child) => path.startsWith(child.url))
      : path === ("url" in item ? item.url : undefined),
    expanded: children.some((child) => path.startsWith(child.url)),
    children: children.map((child) => ({ id: child.id, active: path === child.url })),
  };
}
export function navigationTarget(item: { newTab?: boolean }) {
  return item.newTab ? ({ target: "_blank", rel: "noreferrer" } as const) : {};
}
export function navigationCount(count: number) {
  return count > 99 ? "99+" : String(count);
}
