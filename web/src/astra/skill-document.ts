import type { SkillItem } from "@/lib/types";
import type { FormGroup, FormValues } from "./forms";

export function validateSkillName(name: string): string {
  const letters = [...name];
  if (
    !letters.length ||
    letters.length > 64 ||
    !/^\p{L}/u.test(name) ||
    name.endsWith("-") ||
    name.includes("--") ||
    letters.some(
      (letter) => !(/^[a-z0-9-]$/.test(letter) || (letter.codePointAt(0)! > 127 && /^[\p{L}\p{Nd}]$/u.test(letter))),
    )
  ) {
    throw new Error(
      "Use 1–64 letters, digits or single hyphens; start with a letter. ASCII letters must be lowercase.",
    );
  }
  return name;
}

export function validateSkillPath(path: string): string {
  if (!path || new TextEncoder().encode(path).length > 512)
    throw new Error("A file path must contain 1–512 UTF-8 bytes.");
  if (
    /[\\%#?*:"<>|\u0000-\u001f\u007f\ufffd\p{Cf}\p{Co}\p{Cs}]/u.test(path) ||
    [...path].some((letter) => letter !== " " && /^\p{White_Space}$/u.test(letter))
  )
    throw new Error("The file path contains an unsupported character.");
  if (path.includes("..") || path.startsWith("/") || path.includes("//"))
    throw new Error("Use a relative path without '..' or empty segments.");
  const normalized = path
    .split("/")
    .filter((part) => part && part !== ".")
    .join("/");
  if (!normalized) throw new Error("Choose a file or directory below the skill root.");
  return normalized;
}

export function skillEndpoint(name: string, suffix = ""): string {
  return `/skills/${encodeURIComponent(name)}${suffix}`;
}

export function skillFileEndpoint(name: string, path: string): string {
  return skillEndpoint(name, `/files/${validateSkillPath(path).split("/").map(encodeURIComponent).join("/")}`);
}

export type SkillEntry = { path: string; name: string; directory: boolean; children: SkillEntry[] };

// Directory listings use native separators on Windows. Normalize only paths
// supplied by the server; operator-entered paths still use the strict validator.
export function skillListedPaths(entries: string[]): string[] {
  return [...new Set(entries.map((path) => path.replaceAll("\\", "/")))];
}

export function skillTree(entries: string[]): SkillEntry[] {
  const nodes = new Map<string, SkillEntry>();
  function ensure(path: string, directory: boolean): SkillEntry {
    const existing = nodes.get(path);
    if (existing) {
      existing.directory ||= directory;
      return existing;
    }
    const parent = path.lastIndexOf("/");
    const node: SkillEntry = { path, name: path.slice(parent + 1), directory, children: [] };
    nodes.set(path, node);
    if (parent >= 0) ensure(path.slice(0, parent), true).children.push(node);
    return node;
  }
  for (const entry of skillListedPaths(entries)) {
    const path = entry.replace(/\/$/, "");
    if (path) ensure(path, entry.endsWith("/"));
  }
  const roots = [...nodes.values()].filter((node) => !node.path.includes("/"));
  const sort = (list: SkillEntry[]) => {
    list.sort(
      (left, right) =>
        Number(right.directory) - Number(left.directory) ||
        left.name.localeCompare(right.name, undefined, { sensitivity: "base" }),
    );
    for (const node of list) sort(node.children);
  };
  sort(roots);
  return roots;
}

export function skillDraft(skill?: SkillItem): FormValues {
  return {
    name: skill?.name ?? "",
    description: skill?.description ?? "",
    license: skill?.license ?? "",
    compatibility: skill?.compatibility ?? "",
    instructions: "",
    mcps: [...(skill?.mcps ?? [])],
  };
}

export function skillForm(create: boolean): FormGroup[] {
  return [
    {
      title: "Skill metadata",
      fields: [
        ...(create ? [{ key: "name", label: "Skill name", required: true }] : []),
        { key: "description", label: "Description", type: "textarea", required: create },
        { key: "license", label: "License" },
        { key: "compatibility", label: "Compatibility" },
        ...(create
          ? [
              {
                key: "instructions",
                label: "Instructions",
                type: "textarea" as const,
                hint: "Leave empty to create the standard scaffold.",
              },
            ]
          : []),
      ],
    },
  ];
}

export function skillCreateDocument(draft: FormValues) {
  const name = validateSkillName(String(draft.name ?? "").trim());
  const description = String(draft.description ?? "").trim();
  if (!description) throw new Error("Description is required.");
  return {
    name,
    description,
    license: String(draft.license ?? "").trim(),
    compatibility: String(draft.compatibility ?? "").trim(),
    instructions: String(draft.instructions ?? "").trim(),
    mcps: Array.isArray(draft.mcps) ? draft.mcps.map(String) : [],
  };
}

// A partial metadata update must not overwrite a concurrently edited field just
// because it was displayed alongside the actual edit.
export function skillMetadataDocument(original: FormValues, draft: FormValues): FormValues {
  const document: FormValues = {};
  for (const key of ["description", "license", "compatibility", "mcps"]) {
    if (JSON.stringify(original[key]) !== JSON.stringify(draft[key])) document[key] = draft[key];
  }
  return document;
}

export function skillSummary(skills: SkillItem[]) {
  const ranked = [...skills].filter((skill) => skill.calls > 0).sort((a, b) => b.calls - a.calls);
  return {
    calls: skills.reduce((sum, skill) => sum + (skill.calls || 0), 0),
    unused: skills.filter((skill) => !skill.calls),
    ranked,
    recent: [...skills]
      .filter((skill) => skill.last_used)
      .sort((a, b) => (b.last_used ?? "").localeCompare(a.last_used ?? ""))
      .slice(0, 6),
  };
}
