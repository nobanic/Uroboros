import { MOCK } from "@/lib/mock/enabled";
import { mockHandle } from "@/lib/mock/handler";

import { ApiFailure, browserSession, request } from "./client";
import { demoErrorStatus } from "./demo-transport";

export async function consoleRequest<T>(
  path: string,
  method: "GET" | "POST" | "PUT" | "PATCH" | "DELETE" = "GET",
  body?: unknown,
): Promise<T> {
  if (!MOCK) return request<T>(path, { method, body });
  try {
    return await mockHandle<T>(method, path, body === undefined ? null : JSON.stringify(body));
  } catch (error) {
    const status = demoErrorStatus(error);
    if (status !== undefined) throw new ApiFailure(status, error instanceof Error ? error.message : String(error));
    throw error;
  }
}

export async function consoleDownload(path: string, fallback: string): Promise<void> {
  const token = browserSession.read();
  const response = await fetch(`/api${path}`, { headers: token ? { Authorization: `Bearer ${token}` } : {} });
  if (!response.ok) {
    if (response.status === 401) browserSession.clear();
    const message = await response.text();
    throw new ApiFailure(response.status, `Download failed (${response.status}): ${message}`);
  }
  const disposition = response.headers.get("Content-Disposition") || "";
  const filename =
    disposition.match(/filename="([^"]+)"/)?.[1] || disposition.match(/filename=([^;\s]+)/)?.[1] || fallback;
  const location = URL.createObjectURL(await response.blob());
  try {
    const link = document.createElement("a");
    link.href = location;
    link.download = filename;
    document.body.appendChild(link);
    link.click();
    link.remove();
  } finally {
    window.setTimeout(() => URL.revokeObjectURL(location), 1000);
  }
}

export async function consoleUpload(id: string, files: File[]) {
  if (MOCK)
    return {
      attachments: files.map((file) => ({
        name: file.name,
        path: `uploads/${file.name}`,
        abs: `/mock/${file.name}`,
        size: file.size,
      })),
    };
  const data = new FormData();
  for (const file of files) data.append("file", file);
  const token = browserSession.read();
  const response = await fetch(`/api/chat/upload?scope=staging&id=${encodeURIComponent(id)}`, {
    method: "POST",
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    body: data,
  });
  if (!response.ok) {
    if (response.status === 401) browserSession.clear();
    throw new ApiFailure(response.status, `Upload failed (${response.status}): ${await response.text()}`);
  }
  return response.json() as Promise<{ attachments: { name: string; path: string; abs?: string; size: number }[] }>;
}

export async function consoleSkillUpload(file: File, overwrite = false): Promise<{ name: string; files: number }> {
  if (MOCK) return { name: file.name.replace(/\.zip$/i, ""), files: 1 };
  const data = new FormData();
  data.append("file", file);
  const token = browserSession.read();
  const response = await fetch(`/api/skills/upload${overwrite ? "?overwrite=true" : ""}`, {
    method: "POST",
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    body: data,
  });
  const result = (await response.json().catch(() => ({}))) as { name?: string; files?: number; error?: string };
  if (!response.ok) {
    if (response.status === 401) browserSession.clear();
    throw new ApiFailure(response.status, result.error || `Skill upload failed (${response.status})`);
  }
  if (!result.name || typeof result.files !== "number")
    throw new Error("The server returned an incomplete skill upload result.");
  return { name: result.name, files: result.files };
}
