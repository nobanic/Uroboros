import type { ChatMention } from "@/lib/chat-mentions";
import type { Audit, ChatAttachment, Settings, Stats, UpdateCheck, UpdateStatus, UsageStats } from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { Resource } from "./resources";

type SearchProbe = {
  web_search_backend?: string;
  web_search_proxy?: string;
  brave_search_api_key?: string;
  tavily_search_api_key?: string;
};
type Demo = { enabled: () => boolean; report: (task?: string) => string };

/** System operations remain explicit commands. Reading health, configuration or update status never applies an update. */
export function systemService(wire: ApplicationWire, demo: Demo) {
  const auth = new Resource(wire, "/auth");
  const settings = new Resource(wire, "/settings");
  const update = new Resource(wire, "/update");
  const scoped = (path: string, task?: string) => {
    const resource = new Resource(wire, path);
    return task ? resource.query([["task", task]], "component") : resource;
  };
  const chat = (taskId: string) => new Resource(wire, `/tasks/${taskId}/chat`);
  return {
    health: () => new Resource(wire, "/health").read<{ ok: boolean; service: string; version: string }>(),
    authStatus: () => auth.at("status").read<{ initialized: boolean }>(),
    login: (username: string, password: string) =>
      auth.at("login").send<{ token: string }>("POST", { username, password }),
    initPassword: (password: string) => auth.at("init").send<{ token: string }>("POST", { password }),
    changePassword: (oldPassword: string, newPassword: string) =>
      auth
        .at("change-password")
        .send<{ ok: boolean }>("POST", { old_password: oldPassword, new_password: newPassword }),
    stats: (task?: string) => scoped("/stats", task).read<Stats>(),
    usageStats: (days = 365) =>
      new Resource(wire, "/tokens/usage").query([["days", days]], "component").read<UsageStats>(),
    audit: (task?: string) => scoped("/audit", task).read<Audit>(),
    settings: () => settings.read<Settings>(),
    setSettings: (patch: Partial<Settings>) => settings.send<Settings>("PUT", patch),
    testWebSearch: (patch: SearchProbe) =>
      settings
        .at("web-search", "test")
        .send<{ ok: boolean; error?: string; count?: number; backend?: string }>("POST", patch),
    detectPython: () => settings.at("python", "detect").send<{ python_interpreter: string }>("POST", {}),
    report: async (task?: string) => {
      if (demo.enabled()) return demo.report(task);
      const response = await scoped("/report", task).raw();
      if (!response.ok) throw new Error(`report: ${response.status}`);
      return response.text();
    },
    chatMentions: (kind: string, query: string, signal?: AbortSignal, cursor = "") =>
      new Resource(wire, "/chat/mentions")
        .query([
          ["kind", String(kind)],
          ["q", String(query)],
          ["cursor", cursor],
        ])
        .read<{ items: ChatMention[]; next_cursor?: string }>({ signal }),
    chat: (message: string, task?: string, attachments?: ChatAttachment[], seg?: number) =>
      scoped("/chat", task).send<{ reply: string; mode: string }>("POST", { message, attachments, seg }),
    chatStatus: (taskId: string) => chat(taskId).at("status").read<{ running: boolean }>(),
    stopChat: (taskId: string) => chat(taskId).at("stop").send<{ status: string }>("POST", {}),
    gc: (ttl = 86400) =>
      new Resource(wire, "/gc").query([["ttl", ttl]], "component").send<{ removed: number }>("POST", {}),
    checkUpdate: (force = false) =>
      (force ? update.at("check").query([["force", 1]]) : update.at("check")).read<UpdateCheck>(),
    applyUpdate: (target?: string) => update.at("apply").send<{ ok: boolean; target: string }>("POST", target ? { target } : undefined),
    updateStatus: () => update.at("status").read<UpdateStatus>(),
    saveUpdateCredential: (token: string) => update.at("credentials").send<{ configured: boolean }>("POST", { token }),
    rollbackUpdate: () => update.at("rollback").send<{ ok: boolean }>("POST"),
  };
}
