import type { ChatAttachment, WorkspaceFile, WorkspaceListing } from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { multipartFiles, Resource, saveDownload } from "./resources";

export function workspaceService(wire: ApplicationWire, demo: () => boolean) {
  const workspace = new Resource(wire, "/workspace");
  const file = (operation: string, path: string) =>
    workspace.at(operation).query([["path", String(path)]], "component");
  return {
    workspaceList: (path = "") => file("list", path).read<WorkspaceListing>(),
    workspaceRead: (path: string) => file("read", path).read<WorkspaceFile>(),
    workspaceWrite: (path: string, content: string) =>
      workspace.at("write").send<{ ok: boolean; path: string }>("POST", { path, content }),
    workspaceMkdir: (path: string) => workspace.at("mkdir").send<{ ok: boolean; path: string }>("POST", { path }),
    workspaceDelete: (path: string) => file("delete", path).send<{ ok: boolean }>("DELETE"),
    workspaceUpload: async (dir: string, files: File[]) => {
      if (demo()) return { uploaded: files.length };
      const response = await file("upload", dir).raw({ method: "POST", body: multipartFiles(files) });
      if (!response.ok) throw new Error(`Upload failed: ${response.status}`);
      return response.json() as Promise<{ uploaded: number }>;
    },
    workspaceDownload: async (path: string) => {
      let content: Blob;
      if (demo()) content = new Blob([`(demo) Sample download content for ${path}.`], { type: "text/plain" });
      else {
        const response = await file("download", path).raw();
        if (!response.ok) throw new Error(`Download failed: ${response.status}`);
        content = await response.blob();
      }
      saveDownload(content, () => path.split("/").pop() || "download");
    },
    chatUpload: async (scope: "task" | "session" | "staging", id: string, files: File[]) => {
      if (demo())
        return {
          attachments: files.map((file) => ({
            name: file.name,
            path: `uploads/${file.name}`,
            size: file.size,
            abs: `/mock/${file.name}`,
          })),
        };
      const upload = new Resource(wire, "/chat/upload").query(
        [
          ["scope", String(scope)],
          ["id", String(id)],
        ],
        "component",
      );
      const response = await upload.raw({ method: "POST", body: multipartFiles(files) });
      if (!response.ok) throw new Error(`Upload failed: ${response.status} ${await response.text()}`);
      return response.json() as Promise<{ attachments: ChatAttachment[] }>;
    },
    uploadSkill: async (file: File, overwrite = false): Promise<{ name: string; files: number }> => {
      if (demo()) return { name: file.name.replace(/\.zip$/i, ""), files: 1 };
      const upload = new Resource(wire, `/skills/upload${overwrite ? "?overwrite=true" : ""}`);
      const response = await upload.raw({ method: "POST", body: multipartFiles([file]) });
      const body = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(body?.error || `Upload failed(${response.status})`);
      return body;
    },
  };
}
