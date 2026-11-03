import type { Activity, ChatAttachment, Conversation } from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { Resource, slice } from "./resources";

type Tail = { items: Activity[]; cursor: number; running: boolean };
const tail = (value: Tail) => ({ items: slice(value.items), cursor: value.cursor ?? 0, running: !!value.running });

/** A conversation owns its profile, message tail and reverse-history cursor independently of task sessions. */
export function conversationService(wire: ApplicationWire) {
  const root = new Resource(wire, "/conversations");
  const conversation = (id: number) => root.at(String(id));
  const messages = (id: number) => conversation(id).at("messages");
  return {
    conversations: () => root.list<Conversation>("conversations"),
    createConversation: (agent_key: string, title = "", llm_profile_id?: number | null) =>
      root.send<Conversation>("POST", { agent_key, title, llm_profile_id: llm_profile_id ?? null }),
    updateConversation: (id: number, input: { title?: string; pinned?: boolean }) =>
      conversation(id).send<Conversation>("PATCH", input),
    renameConversation: (id: number, title: string) => conversation(id).send<Conversation>("PATCH", { title }),
    pinConversation: (id: number, pinned: boolean) => conversation(id).send<Conversation>("PATCH", { pinned }),
    updateConversationProfile: (id: number, llm_profile_id: number | null) =>
      conversation(id).at("profile").send<{ ok: boolean }>("PATCH", { llm_profile_id }),
    deleteConversation: (id: number) => conversation(id).send<{ deleted: number }>("DELETE"),
    deleteConversations: (ids: number[]) =>
      root.at("delete", "batch").send<{ items: { id: number; ok: boolean; error?: string }[] }>("POST", { ids }),
    conversationMessages: (id: number, since = 0) =>
      messages(id)
        .query([["since", since]], "component")
        .read<Tail>()
        .then(tail),
    conversationHistory: (id: number, before = 0, limit = 200) =>
      messages(id)
        .query([
          ["limit", limit],
          ["before", before > 0 ? before : undefined],
        ])
        .read<Tail & { hasMore: boolean }>()
        .then((value) => ({ ...tail(value), hasMore: !!value.hasMore })),
    conversationMsgDetail: (id: number, seq: number) =>
      messages(id)
        .at(String(seq))
        .read<{ detail: string }>()
        .then((value) => value.detail ?? ""),
    sendConversationMessage: (id: number, message: string, attachments?: ChatAttachment[]) =>
      messages(id).send<{ status: string }>("POST", { message, attachments }),
    stopConversation: (id: number) => conversation(id).at("stop").send<{ status: string }>("POST", {}),
  };
}
