import type { WorkspaceEntry, WorkspaceFile } from "@/lib/types";

export function workspaceSize(bytes: number): string {
  const units = ["B", "KB", "MB", "GB"];
  let unit = 0;
  let amount = bytes;
  while (amount >= 1024 && unit < units.length - 1) {
    amount /= 1024;
    unit++;
  }
  return `${unit === 0 ? amount : amount.toFixed(1)} ${units[unit]}`;
}
export const workspaceTime = (milliseconds: number) =>
  new Date(milliseconds).toLocaleString("en-US", {
    year: "2-digit",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
  });

export function workspaceCrumbs(directory: string) {
  const crumbs = [{ name: "Workspace", path: "" }];
  for (const part of directory ? directory.split("/") : []) {
    const parent = crumbs[crumbs.length - 1].path;
    crumbs.push({ name: part, path: parent ? `${parent}/${part}` : part });
  }
  return crumbs;
}
export function workspaceFacts(entries: WorkspaceEntry[]) {
  return entries.reduce(
    (facts, entry) => {
      if (entry.dir) facts.folders++;
      else {
        facts.files++;
        facts.bytes += entry.size;
      }
      return facts;
    },
    { folders: 0, files: 0, bytes: 0 },
  );
}
export function workspaceVisible(entries: WorkspaceEntry[], filter: string) {
  const needle = filter.trim().toLocaleLowerCase();
  return needle ? entries.filter((entry) => entry.name.toLocaleLowerCase().includes(needle)) : entries;
}
export function workspaceLineCount(text: string) {
  let count = 1;
  for (let index = 0; index < text.length; index++) if (text.charCodeAt(index) === 10) count++;
  return count;
}
export const workspaceEditable = (file: WorkspaceFile) => !file.binary && !file.too_large;
export const workspaceContains = (parent: string, child: string) => child === parent || child.startsWith(`${parent}/`);
