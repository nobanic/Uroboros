"use client";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { toast } from "sonner";
import { ApprovalExecutionFocus, useApprovalFocus, useApprovalHistory } from "@/components/approval-execution-focus";
import { MentionTextarea } from "@/components/mention-textarea";
import { SideQuestionButton, SideQuestionWorkspace } from "@/components/side-question-workspace";
import { TodoPopover } from "@/components/todo-popover";
import { Transcript } from "@/components/transcript";
import { ScrollArea } from "@/components/ui/scroll-area";
import { UroIcon } from "@/components/uroboros-icons";
import { useSideQuestions } from "@/hooks/use-side-questions";
import { api } from "@/lib/api";
import { shouldSubmitOnKey, useChatSendMode } from "@/lib/chat-send-mode";
import { isBtwCommand } from "@/lib/side-questions";
import type { Agent, ChatAttachment, Conversation, LLMProfile } from "@/lib/types";
import { conversationMeter } from "./conversation-document";
import { compactSessionTokens as compactTokens } from "./session-document";
import { useConversationJournal } from "./use-conversation-data";

export type ConversationDraft = { input?: string; attachments?: ChatAttachment[] };
export const conversationBriefs = [
  {
    title: "Command summary",
    hint: "Surface the next decision",
    icon: "mission",
    text: "Summarize the active operation and surface the next operator decision.",
  },
  {
    title: "Evidence review",
    hint: "Inspect verification gaps",
    icon: "findings",
    text: "Review current findings and identify the most important evidence gaps.",
  },
  {
    title: "Traffic correlation",
    hint: "Connect observations",
    icon: "traffic",
    text: "Correlate recent traffic with high-priority findings and propose the next validation step.",
  },
  {
    title: "Verification plan",
    hint: "Scope and stop conditions",
    icon: "shield",
    text: "Draft a scoped verification plan for uroboros.lab with clear stop conditions.",
  },
] as const;

function ModelChoice({
  profiles,
  value,
  change,
  disabled,
}: {
  profiles: LLMProfile[];
  value: number | null;
  change: (value: number | null) => void;
  disabled: boolean;
}) {
  const selected = profiles.find((profile) => Number(profile.id) === value),
    global = profiles.find((profile) => profile.is_default);
  return (
    <label className="astra-conversation-profile">
      <UroIcon name="llm" />
      <span>Conversation profile</span>
      <select
        aria-label="Conversation model profile"
        disabled={disabled}
        value={value ?? ""}
        onChange={(event) => change(event.target.value ? Number(event.target.value) : null)}
      >
        <option value="">Default{global ? ` (${global.name} · ${global.model})` : ""}</option>
        {value !== null && !selected && (
          <option value={value} disabled>
            Unavailable profile #{value}
          </option>
        )}
        {profiles.map((profile) => (
          <option key={profile.id} value={profile.id}>
            {profile.name} · {profile.format} · {profile.model}
          </option>
        ))}
      </select>
    </label>
  );
}
function Composer({
  input,
  change,
  send,
  busy,
  running,
  stop,
  stopping,
  files,
  upload,
  uploading,
  remove,
  children,
  allowSide = false,
}: {
  input: string;
  change: (value: string) => void;
  send: () => void;
  busy: boolean;
  running?: boolean;
  stop?: () => void;
  stopping?: boolean;
  files: ChatAttachment[];
  upload: (files: File[]) => void;
  uploading: boolean;
  remove: (path: string) => void;
  children?: ReactNode;
  allowSide?: boolean;
}) {
  const picker = useRef<HTMLInputElement>(null),
    mode = useChatSendMode();
  return (
    <footer className="astra-conversation-composer">
      {files.length > 0 && (
        <div className="astra-conversation-files">
          {files.map((file) => (
            <span key={file.path} title={file.path}>
              <UroIcon name="workspace" />
              <strong>{file.name}</strong>
              <small>
                {file.size >= 1048576
                  ? `${(file.size / 1048576).toFixed(1)} MB`
                  : file.size >= 1024
                    ? `${(file.size / 1024).toFixed(1)} KB`
                    : `${file.size} B`}
              </small>
              <button aria-label={`Remove attachment ${file.name}`} onClick={() => remove(file.path)}>
                ×
              </button>
            </span>
          ))}
        </div>
      )}
      <MentionTextarea
        aria-label="Conversation message"
        value={input}
        onValueChange={change}
        rows={3}
        disabled={busy && !(running && allowSide)}
        placeholder={
          running ? "The agent is replying; type /btw to ask a question…" : "Type a message, @ to reference a record…"
        }
        onKeyDown={(event) => {
          if (shouldSubmitOnKey(event, mode)) {
            event.preventDefault();
            send();
          }
        }}
      />
      <div className="astra-conversation-compose-actions">
        <input
          ref={picker}
          type="file"
          multiple
          hidden
          onChange={(event) => {
            const files = Array.from(event.target.files || []);
            event.target.value = "";
            if (files.length) upload(files);
          }}
        />
        <button disabled={busy || uploading} onClick={() => picker.current?.click()}>
          <UroIcon name="workspace" />
          {uploading ? "Uploading…" : "Attach files"}
        </button>
        <small>@ Records{allowSide ? " · /btw Side question" : ""}</small>
        {running && allowSide && isBtwCommand(input) && (
          <button className="is-primary" aria-label="Send side question" onClick={send}>
            Ask /btw
          </button>
        )}
        {running ? (
          <button className="is-danger" disabled={stopping} onClick={stop} aria-label="Stop this run">
            {stopping ? "Stopping…" : "Stop run"}
          </button>
        ) : (
          <button
            className="is-primary"
            aria-label="Send message"
            disabled={busy || (!input.trim() && !files.length)}
            onClick={send}
          >
            Send message <span>↗</span>
          </button>
        )}
      </div>
      {children}
    </footer>
  );
}
export function DraftConversation({
  agents,
  profiles,
  started,
}: {
  agents: Agent[];
  profiles: LLMProfile[];
  started: (conversation: Conversation, draft?: ConversationDraft) => void;
}) {
  const [agent, choose] = useState(""),
    [profile, configure] = useState<number | null>(null),
    [input, write] = useState(""),
    [busy, pending] = useState(false),
    [uploading, setUploading] = useState(false);
  const lock = useRef(false),
    alive = useRef(true);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    if (agents.some((item) => item.key === "auto")) choose((current) => current || "auto");
  }, [agents]);
  async function create(files?: File[]) {
    const message = input.trim();
    if (lock.current || !agent || (!files?.length && !message)) return;
    lock.current = true;
    pending(true);
    setUploading(!!files?.length);
    let conversation: Conversation | undefined;
    try {
      conversation = await api.createConversation(agent, "", profile);
      if (files?.length) {
        const result = await api.chatUpload("session", `conv-${conversation.id}`, files);
        if (alive.current) started(conversation, { input, attachments: result.attachments });
      } else {
        await api.sendConversationMessage(conversation.id, message);
        if (alive.current) started({ ...conversation, running: true });
      }
    } catch (error) {
      if (alive.current) {
        toast.error(`${files?.length ? "Upload" : "Send"} failed: ${(error as Error).message}`);
        if (conversation) started(conversation, { input: files?.length ? input : message });
      }
    } finally {
      lock.current = false;
      if (alive.current) {
        pending(false);
        setUploading(false);
      }
    }
  }
  const selected = agents.find((row) => row.key === agent);
  return (
    <div className="astra-conversation-pane" data-conversation="draft">
      <header className="astra-conversation-heading">
        <span className="astra-conversation-emblem">
          <UroIcon name="agents" />
        </span>
        <div>
          <small>New transmission</small>
          <h2>Open an agent channel</h2>
        </div>
        <label>
          Agent
          <select
            aria-label="New conversation agent"
            value={agent}
            onChange={(e) => choose(e.target.value)}
            disabled={busy}
          >
            <option value="" disabled>
              Select an agent
            </option>
            {agents.map((row) => (
              <option key={row.key} value={row.key}>
                {row.name}
                {!row.builtin ? " · Custom" : ""}
              </option>
            ))}
          </select>
        </label>
      </header>
      <div className="astra-conversation-draft">
        <div className="astra-conversation-orbit" aria-hidden="true">
          <i />
          <i />
          <span>
            <UroIcon name="agents" />
          </span>
        </div>
        <small>Ready for your brief</small>
        <h2>{selected?.name || "Choose a specialist"}</h2>
        <p>{selected?.description || "Preserve context, inspect evidence, and give the agent a clear objective."}</p>
        <div className="astra-conversation-briefs">
          {conversationBriefs.map((brief) => (
            <button key={brief.title} aria-label={`Use ${brief.title} brief`} onClick={() => write(brief.text)}>
              <UroIcon name={brief.icon} />
              <strong>{brief.title}</strong>
              <span>{brief.hint}</span>
              <b>↗</b>
            </button>
          ))}
        </div>
      </div>
      <Composer
        input={input}
        change={write}
        send={() => void create()}
        busy={busy || !agent}
        files={[]}
        uploading={uploading}
        upload={(files) => void create(files)}
        remove={() => {}}
      >
        <ModelChoice profiles={profiles} value={profile} change={configure} disabled={busy} />
      </Composer>
    </div>
  );
}
export function ConversationView({
  conversation,
  agents,
  profiles,
  initial,
  refresh,
  consumed,
}: {
  conversation: Conversation;
  agents: Agent[];
  profiles: LLMProfile[];
  initial?: ConversationDraft;
  refresh: () => void;
  consumed: () => void;
}) {
  const journal = useConversationJournal(conversation.id, conversation.running, refresh),
    focus = useApprovalFocus({ conversationId: conversation.id }),
    side = useSideQuestions(`/api/conversations/${conversation.id}`);
  const [input, write] = useState(initial?.input || ""),
    [files, setFiles] = useState<ChatAttachment[]>(initial?.attachments || []),
    [sending, setSending] = useState(false),
    [uploading, setUploading] = useState(false),
    [stopping, setStopping] = useState(false),
    [profileBusy, setProfileBusy] = useState(false);
  const alive = useRef(true),
    locks = useRef({ send: false, upload: false, stop: false, profile: false });
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    consumed();
  }, []);
  const loadPage = useCallback(
    (before: number) => api.conversationHistory(conversation.id, before, 200),
    [conversation.id],
  );
  const located = useApprovalHistory(focus.state?.source, journal.ready, journal.items, loadPage, journal.merge);
  const detail = useCallback((seq: number) => api.conversationMsgDetail(conversation.id, seq), [conversation.id]);
  const meter = useMemo(() => conversationMeter(journal.items), [journal.items]),
    agent = agents.find((item) => item.key === conversation.agent_key);
  const todo = useMemo(() => {
    for (let index = journal.items.length - 1; index >= 0; index--) {
      const item = journal.items[index];
      if (item.kind === "tool_use" && item.tool === "TodoWrite") return item.seq;
    }
    return null;
  }, [journal.items]);
  const content = useRef<HTMLDivElement>(null),
    pinned = useRef(true),
    initialScroll = useRef(false);
  const viewport = () => content.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null;
  async function earlier() {
    const view = viewport();
    if (!view) return;
    const height = view.scrollHeight,
      top = view.scrollTop;
    if (await journal.earlier())
      requestAnimationFrame(() => {
        if (alive.current && view === viewport()) view.scrollTop = top + view.scrollHeight - height;
      });
  }
  const reads = useRef({ journal, earlier, focus: !!focus.state, ready: located.ready });
  reads.current = { journal, earlier, focus: !!focus.state, ready: located.ready };
  useEffect(() => {
    const view = viewport();
    if (!view) return;
    let previousTop = view.scrollTop;
    const scroll = () => {
      if (reads.current.focus && !reads.current.ready) return;
      const upward = view.scrollTop < previousTop;
      previousTop = view.scrollTop;
      pinned.current = view.scrollTop + view.clientHeight >= view.scrollHeight - 60;
      if (upward && view.scrollTop <= 80 && !reads.current.journal.loading && !reads.current.journal.olderError)
        void reads.current.earlier();
    };
    view.addEventListener("scroll", scroll, { passive: true });
    return () => view.removeEventListener("scroll", scroll);
  }, []);
  useLayoutEffect(() => {
    const view = viewport();
    if (!view || focus.state) return;
    if (journal.ready && !initialScroll.current) {
      initialScroll.current = true;
      pinned.current = true;
    }
    if (pinned.current) view.scrollTop = view.scrollHeight;
  }, [journal.items, journal.running, journal.ready, focus.state]);
  useEffect(() => {
    const node = content.current;
    if (!node) return;
    const observer = new ResizeObserver(() => {
      const view = viewport();
      if (view && pinned.current && !reads.current.focus) view.scrollTop = view.scrollHeight;
    });
    observer.observe(node);
    return () => observer.disconnect();
  }, []);
  async function send() {
    const message = input.trim(),
      attachments = files;
    if (side.handleCommand(message, () => write(""))) return;
    if (locks.current.send || journal.running || (!message && !attachments.length)) return;
    locks.current.send = true;
    setSending(true);
    write("");
    setFiles([]);
    try {
      await api.sendConversationMessage(conversation.id, message, attachments.length ? attachments : undefined);
      if (alive.current) journal.run();
    } catch (error) {
      if (alive.current) {
        write(message);
        setFiles(attachments);
        toast.error(`Send failed: ${(error as Error).message}`);
      }
    } finally {
      locks.current.send = false;
      if (alive.current) setSending(false);
    }
  }
  async function upload(picked: File[]) {
    if (locks.current.upload || !picked.length) return;
    locks.current.upload = true;
    setUploading(true);
    try {
      const result = await api.chatUpload("session", `conv-${conversation.id}`, picked);
      if (alive.current) setFiles((old) => [...old, ...result.attachments]);
    } catch (error) {
      if (alive.current) toast.error(`Upload failed: ${(error as Error).message}`);
    } finally {
      locks.current.upload = false;
      if (alive.current) setUploading(false);
    }
  }
  async function stop() {
    if (locks.current.stop) return;
    locks.current.stop = true;
    setStopping(true);
    try {
      await api.stopConversation(conversation.id);
    } catch (error) {
      if (alive.current) toast.error(`Stop failed: ${(error as Error).message}`);
    } finally {
      locks.current.stop = false;
      if (alive.current) setStopping(false);
    }
  }
  async function model(value: number | null) {
    if (locks.current.profile || journal.running || sending) return;
    locks.current.profile = true;
    setProfileBusy(true);
    try {
      await api.updateConversationProfile(conversation.id, value);
      if (alive.current) refresh();
    } catch (error) {
      if (alive.current) toast.error(`Failed to switch LLM: ${(error as Error).message}`);
    } finally {
      locks.current.profile = false;
      if (alive.current) setProfileBusy(false);
    }
  }
  return (
    <div className="astra-conversation-pane" data-conversation={conversation.id}>
      <SideQuestionWorkspace side={side} label={agent?.name || conversation.agent_key} composerLayout="inline">
        <header className="astra-conversation-heading">
          <span className="astra-conversation-emblem">
            <UroIcon name="agents" />
          </span>
          <div>
            <small>
              {agent?.name || conversation.agent_key}
              {agent && !agent.builtin ? " · Custom" : ""} · #{conversation.id}
            </small>
            <h2>{conversation.title || "New conversation"}</h2>
            <p>{agent?.description}</p>
          </div>
          {journal.running && (
            <span className="astra-conversation-live">
              <i />
              Live
            </span>
          )}
          <SideQuestionButton side={side} />
        </header>
        <div className="astra-conversation-meter">
          <span title="Number of model calls">{meter.turns} turns</span>
          <span>
            Input <b>{compactTokens(meter.input)}</b>
          </span>
          <span>
            Cache <b>{compactTokens(meter.cache)}</b>
          </span>
          <span>
            Output <b>{compactTokens(meter.output)}</b>
          </span>
          <span>
            {Math.floor(meter.duration / 60000)}m {Math.floor(meter.duration / 1000) % 60}s
          </span>
        </div>
        <ApprovalExecutionFocus focus={focus} history={located} />
        {(journal.error || journal.tailError) && (
          <div className="astra-conversation-error" role="alert">
            <span>{journal.error || journal.tailError}</span>
            <button onClick={() => void journal.recent()}>Retry conversation</button>
          </div>
        )}
        <ScrollArea type="auto" className="astra-conversation-transcript">
          <div ref={content} data-conversation-transcript={conversation.id}>
            {journal.more && (
              <button className="astra-conversation-earlier" disabled={journal.loading} onClick={() => void earlier()}>
                {journal.loading ? "Loading…" : "Load earlier messages"}
              </button>
            )}
            {journal.olderError && (
              <div className="astra-conversation-error" role="alert">
                {journal.olderError}
                <button onClick={() => void earlier()}>Retry earlier messages</button>
              </div>
            )}
            {!journal.ready && !journal.items.length ? (
              <p className="astra-conversation-placeholder">Loading recent history…</p>
            ) : !journal.items.length && !journal.running ? (
              <p className="astra-conversation-placeholder">
                Start a conversation with {agent?.name || conversation.agent_key}
              </p>
            ) : (
              <Transcript
                sourceKey={`conversation:${conversation.id}`}
                activity={journal.items}
                live={journal.running}
                chat
                fetchDetail={detail}
                focusedSeq={located.ready ? focus.state?.source?.seq : undefined}
              />
            )}
          </div>
        </ScrollArea>
        <Composer
          input={input}
          change={write}
          send={() => void send()}
          busy={journal.running || sending}
          running={journal.running}
          allowSide
          stop={() => void stop()}
          stopping={stopping}
          files={files}
          upload={(picked) => void upload(picked)}
          uploading={uploading}
          remove={(path) => setFiles((previous) => previous.filter((file) => file.path !== path))}
        >
          <div className="astra-conversation-compose-footer">
            <ModelChoice
              profiles={profiles}
              value={conversation.llm_profile_id ?? null}
              change={(value) => void model(value)}
              disabled={journal.running || sending || profileBusy}
            />
            <TodoPopover seq={todo} fetchDetail={detail} />
          </div>
        </Composer>
      </SideQuestionWorkspace>
    </div>
  );
}
