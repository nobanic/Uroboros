"use client";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { toast } from "sonner";
import { ApprovalExecutionFocus, useApprovalFocus, useApprovalHistory } from "@/components/approval-execution-focus";
import { MentionTextarea } from "@/components/mention-textarea";
import { SideQuestionButton, SideQuestionWorkspace } from "@/components/side-question-workspace";
import { TodoPopover } from "@/components/todo-popover";
import { Transcript } from "@/components/transcript";
import { ScrollArea } from "@/components/ui/scroll-area";
import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { useSideQuestions } from "@/hooks/use-side-questions";
import { api } from "@/lib/api";
import { shouldSubmitOnKey, useChatSendMode } from "@/lib/chat-send-mode";
import { MOCK } from "@/lib/mock/enabled";
import { isBtwCommand } from "@/lib/side-questions";
import { taskAssetSourceLabel, taskAssetTypeLabel } from "@/lib/task-assets";
import type { Activity, TokenTotal } from "@/lib/types";
import { EditorDialog } from "./editor-dialog";
import {
  compactSessionTokens,
  sessionFilter,
  locatedEntry,
  mainEntry,
  sessionActivity,
  sessionDuration,
  sessionEntries,
  sessionMatches,
  sessionReview,
  sessionRoles,
  sessionRoleNames,
  sessionTokens,
  type SessionEntry,
  workerMessageLength,
  workerMessageLimit,
} from "./session-document";
import { sessionHistorySize, sessionUnion, type MainSegments } from "./session-journal";
import { useSessionJournal } from "./use-session-journal";
import { useSessionData, useSessionWorkers } from "./use-session-data";
import { useSessionComposer } from "./use-session-composer";
import "./forms.css";
import "./session.css";

const roleIcon: Record<SessionEntry["role"], UroIconName> = {
  mainagent: "user",
  planner: "route",
  system: "logs",
  worker: "agents",
};
function Tokens({ value, label }: { value?: TokenTotal; label: string }) {
  if (!value) return null;
  return (
    <span
      className="astra-session-tokens"
      aria-label={label}
      title={`Input ${value.input_tokens.toLocaleString()} · Output ${value.output_tokens.toLocaleString()} · Cache read ${value.cache_read_tokens.toLocaleString()} · Cache write ${value.cache_write_tokens.toLocaleString()}`}
    >
      <span>{label}</span>
      {[
        ["In", value.input_tokens],
        ["Cache", value.cache_read_tokens],
        ["Out", value.output_tokens],
      ].map(([name, number]) => (
        <span key={name}>
          <small>{name}</small>
          <b>{compactSessionTokens(Number(number))}</b>
        </span>
      ))}
    </span>
  );
}

export function SessionWorkspace({ taskId }: { taskId: string }) {
  const data = useSessionData(taskId),
    workers = useSessionWorkers(taskId),
    focus = useApprovalFocus({ taskId });
  const [segments, setSegments] = useState<MainSegments>({ current: 0, sessions: [{ seq: 0, created_at: "" }] });
  const [selected, select] = useState("main:0"),
    [located, retain] = useState<SessionEntry>(),
    [query, search] = useState(""),
    [filter, setFilter] = useState("all"),
    [listOpen, expandList] = useState(false),
    [inspecting, inspect] = useState(false);
  const [deleting, chooseDeletion] = useState<SessionEntry>(),
    [reason, setReason] = useState(""),
    [mode, setMode] = useState<"soft" | "hard">("soft"),
    [newMain, confirmNewMain] = useState(false);
  const [action, setAction] = useState(""),
    [failure, fail] = useState(""),
    [notice, notify] = useState("");
  const actionLock = useRef(false),
    mounted = useRef(true),
    pinned = useRef(true),
    keyRef = useRef("main:0"),
    content = useRef<HTMLDivElement>(null),
    fileInput = useRef<HTMLInputElement>(null);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const journal = useSessionJournal(taskId, {
    pinned: () => pinned.current,
    segments: (value) => {
      setSegments(value);
      select(`main:${value.current}`);
    },
    audit: (item, active) => {
      const kind =
        item.is_error || item.metadata?.llm_transition?.mode === "exhausted"
          ? "error"
          : item.metadata?.llm_transition?.mode === "automatic"
            ? "success"
            : "info";
      toast[kind](item.summary, { id: `task-${taskId}-llm-${item.seq}` });
      if (active()) void data.profiles.reload(true);
    },
  });
  const focused = useMemo(() => locatedEntry(focus.state?.source), [focus.state?.source]);
  useEffect(() => {
    if (focused) retain(focused);
  }, [focused]);
  const running = data.running.data?.running ?? null;
  const base = sessionEntries(segments, workers.nodes, journal.store, false, running, focused || located);
  const entry = base.find((row) => row.key === (focused?.key || selected)) || mainEntry(0);
  const side = useSideQuestions(
    entry.role === "mainagent"
      ? `/api/tasks/${taskId}/chat`
      : entry.role === "worker" && !entry.inherited && entry.intent_id
        ? `/api/tasks/${taskId}/intents/${entry.intent_id}`
        : null,
  );
  const composer = useSessionComposer({
    taskId,
    entry,
    pages: journal.store,
    running,
    side,
    patch: workers.patch,
    invalidateStatus: data.running.invalidate,
    updateStatus: data.running.replace,
  });
  const [, tick] = useState(0);
  useEffect(() => {
    const timer = window.setInterval(() => tick((value) => value + 1), 1500);
    return () => window.clearInterval(timer);
  }, []);
  const entries = sessionEntries(segments, workers.nodes, journal.store, composer.sending, running, focused || located),
    active = entries.find((row) => row.key === entry.key) || entry;
  const cache = journal.store[active.key],
    activity = useMemo(
      () => sessionActivity(active, cache?.items || []),
      [active.key, active.title, active.last_activity, active.inherited, active.source_task_id, cache?.items],
    );
  const tokens = sessionTokens(
    data.tokens.data?.sessions.find((row) => row.session === active.key),
    cache?.items || [],
  );
  const approvals = data.approvals.data || [],
    assets = (data.assets.data || []).filter((asset) => String(asset.intent_id) === active.intent_id);
  const profile = active.inherited || active.role === "system" ? undefined : data.profiles.data?.[active.role];
  const profileName = profile?.name || profile?.model || "Unnamed profile";
  const latestTodo =
    [...activity].reverse().find((item) => item.kind === "tool_use" && item.tool === "TodoWrite")?.seq ?? null;
  const unread = Object.entries(journal.store).reduce(
    (sum, [key, state]) => sum + (key === active.key ? 0 : state.unread),
    0,
  );
  const viewport = useCallback(
    () => content.current?.closest('[data-slot="scroll-area-viewport"]') as HTMLElement | null,
    [],
  );
  const earlier = useCallback(
    async (key: string) => {
      const view = viewport(),
        top = view?.scrollTop || 0,
        height = view?.scrollHeight || 0;
      if (!(await journal.earlier(key))) return;
      requestAnimationFrame(() => {
        if (keyRef.current === key && view && viewport() === view) view.scrollTop = top + view.scrollHeight - height;
      });
    },
    [journal.earlier, viewport],
  );
  useEffect(() => {
    keyRef.current = active.key;
    journal.activate(active.key);
  }, [active.key, journal.activate]);
  const focusKey = focus.state?.source?.session;
  const focusLoad = useCallback(
    (before: number) => api.activityHistory(taskId, focusKey || "main", before, sessionHistorySize),
    [taskId, focusKey],
  );
  const focusMerge = useCallback(
    (page: { items: Activity[]; hasMore: boolean }) => {
      if (focusKey)
        journal.patch(focusKey, (value) => {
          const items = sessionUnion(page.items, value.items);
          return { ...value, items, hasMore: page.hasMore, earliestSeq: items[0]?.seq || value.earliestSeq };
        });
    },
    [focusKey, journal.patch],
  );
  const focusHistory = useApprovalHistory(
    focus.state?.source,
    !!focusKey && !!journal.store[focusKey]?.loaded,
    focusKey ? journal.store[focusKey]?.items || [] : [],
    focusLoad,
    focusMerge,
  );
  useEffect(() => {
    if (focus.state) pinned.current = false;
  }, [focus.state]);
  useEffect(() => {
    const view = viewport();
    if (!view) return;
    const scroll = () => {
      if (focus.state && !focusHistory.ready) return;
      pinned.current = view.scrollTop + view.clientHeight >= view.scrollHeight - 60;
      if (view.scrollTop <= 80) void earlier(keyRef.current);
    };
    view.addEventListener("scroll", scroll, { passive: true });
    return () => view.removeEventListener("scroll", scroll);
  }, [active.key, viewport, earlier, focus.state, focusHistory.ready]);
  useLayoutEffect(() => {
    if (!focus.state) {
      const view = viewport();
      if (view) view.scrollTop = view.scrollHeight;
      pinned.current = true;
    }
  }, [active.key, focus.state, viewport]);
  useLayoutEffect(() => {
    const view = viewport();
    if (!focus.state && pinned.current && view) view.scrollTop = view.scrollHeight;
  }, [activity, focus.state, viewport]);
  useEffect(() => {
    if (!content.current) return;
    const observer = new ResizeObserver(() => {
      const view = viewport();
      if (!focus.state && pinned.current && view) view.scrollTop = view.scrollHeight;
    });
    observer.observe(content.current);
    return () => observer.disconnect();
  }, [active.key, viewport, focus.state]);
  function choose(row: SessionEntry) {
    focus.close();
    select(row.key);
    expandList(false);
  }
  async function control(row: SessionEntry, operation: "pause" | "resume" | "cancel") {
    if (actionLock.current || row.inherited || !row.intent_id || (operation === "cancel" && !reason.trim())) return;
    actionLock.current = true;
    setAction(row.key);
    fail("");
    try {
      const result = await api.controlIntent(
        taskId,
        row.intent_id,
        operation,
        operation === "cancel" ? reason : undefined,
        operation === "cancel" ? mode : undefined,
      );
      if (!mounted.current) return;
      workers.patch(
        row.intent_id,
        operation === "pause" ? "paused" : operation === "resume" ? "open" : mode === "hard" ? undefined : "deleted",
        operation === "cancel" ? reason : undefined,
      );
      const counts = result.deleted;
      notify(
        operation === "pause"
          ? `Worker #${row.intent_id} paused.`
          : operation === "resume"
            ? `Worker #${row.intent_id} resumed, waiting to be reclaimed.`
            : mode === "hard"
              ? `Worker #${row.intent_id} permanently deleted${counts ? ` · ${counts.intents} intents / ${counts.facts} facts / ${counts.findings} vulnerabilities` : ""}.`
              : `Worker #${row.intent_id} deleted; its history and outputs are retained.`,
      );
      if (operation === "cancel") {
        chooseDeletion(undefined);
        setReason("");
      }
    } catch (error) {
      if (mounted.current) fail(`Worker operation failed: ${(error as Error).message}`);
    } finally {
      actionLock.current = false;
      if (mounted.current) setAction("");
    }
  }
  async function createMain() {
    if (actionLock.current) return;
    actionLock.current = true;
    setAction("new-main");
    fail("");
    try {
      const result = await api.newMainSession(taskId);
      if (!mounted.current) return;
      setSegments((current) => ({
        current: result.current ?? result.seq,
        sessions: [
          { seq: result.seq, created_at: result.created_at },
          ...current.sessions.filter((row) => row.seq !== result.seq),
        ],
      }));
      journal.seed(`main:${result.seq}`);
      focus.close();
      select(`main:${result.seq}`);
      expandList(false);
      composer.setMain("");
      confirmNewMain(false);
      notify("New Main Agent session started. The task graph, scope, assets and goals are retained.");
    } catch (error) {
      if (mounted.current) fail(`Failed to create session: ${(error as Error).message}`);
    } finally {
      actionLock.current = false;
      if (mounted.current) setAction("");
    }
  }
  const sendMode = useChatSendMode();
  const matches = (row: SessionEntry) => sessionMatches(row, query) && sessionFilter(row, filter, approvals);
  const errors = [
    ["Workers", workers.error, workers.reload],
    ["Tokens", data.tokens.error, data.tokens.reload],
    ["Running status", data.running.error, data.running.reload],
    ["Profiles", data.profiles.error, data.profiles.reload],
    ["Approvals", data.approvals.error, data.approvals.reload],
    ["Assets", data.assets.error, data.assets.reload],
  ] as const;
  const writableWorker = active.role === "worker" && !active.inherited && ["running", "paused"].includes(active.status);
  return (
    <section className="astra-sessions-workspace" data-session-workspace>
      <aside className="astra-session-roster" data-expanded={listOpen}>
        <header>
          <button
            className="astra-session-roster-toggle"
            aria-expanded={listOpen}
            aria-controls="astra-session-list"
            onClick={() => expandList((value) => !value)}
          >
            <UroIcon name="agents" />
            <span>
              Sessions<small>{active.title}</small>
            </span>
            {unread > 0 && <b>{unread > 99 ? "99+" : unread}</b>}
          </button>
          <span className="astra-session-connection" data-connected={journal.connected}>
            {MOCK ? "Demo snapshot" : journal.connected ? "Live" : "Reconnecting"}
          </span>
        </header>
        <Tokens value={data.tokens.data?.total} label="Task total" />
        <div className="astra-session-list" id="astra-session-list">
          <label className="astra-session-search">
            <UroIcon name="search" />
            <input
              aria-label="Search loaded sessions"
              value={query}
              onChange={(event) => search(event.target.value)}
              placeholder="Search loaded sessions…"
            />
          </label>
          <select aria-label="Session status filter" value={filter} onChange={(event) => setFilter(event.target.value)}>
            <option value="all">All sessions</option>
            {[
              "running",
              "paused",
              "pending",
              "done",
              "blocked",
              "exhausted",
              "stopped",
              "deleted",
              "review",
              "inherited",
            ].map((value) => (
              <option key={value} value={value}>
                {value === "review" ? "Needs approval" : value[0].toUpperCase() + value.slice(1)}
              </option>
            ))}
          </select>
          <ScrollArea className="astra-session-list-scroll">
            <nav aria-label="Task session list">
              {sessionRoles.map((role) => (
                <div key={role} className="astra-session-group">
                  <h3>
                    <UroIcon name={roleIcon[role]} />
                    {sessionRoleNames[role]}
                    <span>{entries.filter((row) => row.role === role).length}</span>
                    {role === "mainagent" && (
                      <button
                        aria-label="New Main Agent session"
                        title="Start a clean context over the same task"
                        disabled={!!action}
                        onClick={() => {
                          fail("");
                          confirmNewMain(true);
                        }}
                      >
                        <UroIcon name="plus" />
                      </button>
                    )}
                  </h3>
                  {entries
                    .filter((row) => row.role === role && matches(row))
                    .map((row) => (
                      <div
                        className="astra-session-item"
                        data-session={row.key}
                        data-status={row.role === "worker" ? row.status : row.live ? "running" : "idle"}
                        data-active={row.key === active.key}
                        key={row.key}
                      >
                        <button onClick={() => choose(row)} aria-current={row.key === active.key ? "true" : undefined}>
                          <span
                            className="astra-session-status"
                            aria-label={row.role === "worker" ? row.status : row.live ? "active" : "idle"}
                          />
                          <span>
                            <strong>{row.title}</strong>
                            <small>
                              {row.intent_id
                                ? `#${row.intent_id} · ${row.status}`
                                : row.role === "mainagent"
                                  ? "Interactive"
                                  : row.role === "planner"
                                    ? "Planning activity"
                                    : "Provider transitions"}
                              {row.inherited ? ` · From #${row.source_task_id}` : ""}
                            </small>
                          </span>
                          {sessionReview(row, approvals) && <UroIcon name="approvals" aria-label="Approval pending" />}
                          {row.live && <em>Live</em>}
                          {row.key !== active.key && !!journal.store[row.key]?.unread && (
                            <b>{journal.store[row.key].unread > 99 ? "99+" : journal.store[row.key].unread}</b>
                          )}
                        </button>
                        {row.role === "worker" &&
                          !row.inherited &&
                          ["running", "paused", "pending"].includes(row.status) && (
                            <button
                              className="astra-session-delete"
                              aria-label="Delete this intent (reason required; soft-delete or hard-delete optional)"
                              disabled={!!action}
                              onClick={() => {
                                chooseDeletion(row);
                                setMode("soft");
                                setReason("");
                                fail("");
                              }}
                            >
                              <UroIcon name="close" />
                            </button>
                          )}
                      </div>
                    ))}
                </div>
              ))}
            </nav>
          </ScrollArea>
          {workers.hasMore && (
            <button className="astra-button" disabled={workers.olderBusy} onClick={() => void workers.earlier()}>
              {workers.olderBusy ? "Loading Workers…" : "Load earlier Workers"}
            </button>
          )}
          {!entries.some(matches) && (
            <p className="astra-session-muted">No loaded sessions match. Earlier Workers remain available below.</p>
          )}
          <footer>{entries.length} loaded sessions · History loads on demand</footer>
        </div>
      </aside>
      <div className="astra-session-stage">
        {errors.some(([, error]) => !!error) && (
          <details className="astra-session-read-errors">
            <summary>Some live reads are unavailable; last successful data is retained.</summary>
            {errors
              .filter(([, error]) => !!error)
              .map(([name, error, retry]) => (
                <p key={name}>
                  {name}: {error}
                  <button className="astra-button" onClick={() => void retry(true)}>
                    Retry {name.toLowerCase()}
                  </button>
                </p>
              ))}
          </details>
        )}
        {notice && (
          <p className="astra-session-notice" role="status">
            {notice}
            <button aria-label="Dismiss session notice" onClick={() => notify("")}>
              ×
            </button>
          </p>
        )}
        {failure && !deleting && !newMain && (
          <p className="astra-form-error" role="alert">
            {failure}
          </p>
        )}
        <SideQuestionWorkspace
          side={side}
          label={active.role === "worker" ? `Worker #${active.intent_id} · ${active.title}` : active.title}
        >
          <div className="astra-session-execution">
            <header className="astra-session-header">
              <span className="astra-session-emblem">
                <UroIcon name={roleIcon[active.role]} />
              </span>
              <div>
                <p>
                  {active.role === "worker"
                    ? `Worker #${active.intent_id} · ${active.status}`
                    : active.role === "mainagent"
                      ? "Interactive console"
                      : active.role === "planner"
                        ? "Planning record"
                        : "Task-level audit"}
                </p>
                <h2>{active.role === "worker" ? "Worker execution session" : active.title}</h2>
              </div>
              {active.live && <span className="astra-session-live">Live</span>}
              <SideQuestionButton side={side} />
              <button className="astra-button" onClick={() => inspect(true)} aria-label="Inspect session source">
                <UroIcon name="info" />
                Source
              </button>
            </header>
            <div className="astra-session-facts">
              {profile && (
                <span
                  title={profile.available ? `${profileName} / ${profile.model}` : profile.reason}
                  aria-label={profile.available ? `Current profile: ${profileName}` : "Model unavailable"}
                >
                  <UroIcon name="llm" />
                  {profile.available ? profileName : "Model unavailable"}
                </span>
              )}
              {active.inherited && <span>Source task #{active.source_task_id} · Read-only history</span>}
              {assets.length > 0 && (
                <button onClick={() => inspect(true)} title={assets.map((asset) => asset.label).join("\n")}>
                  Current asset: {assets[0].label || `#${assets[0].asset_id}`}
                  {assets.length > 1 ? ` +${assets.length - 1}` : ""}
                </button>
              )}
              <Tokens value={tokens} label="Session" />
              {sessionDuration(activity, active.live) && (
                <span>
                  <UroIcon name="clock" />
                  {sessionDuration(activity, active.live)}
                </span>
              )}
            </div>
            {active.status === "deleted" && (
              <p className="astra-session-deleted">
                This intent was deleted. Execution stopped; its history and outputs remain available.
                {active.reason && <strong>Deletion reason: {active.reason}</strong>}
              </p>
            )}
            <ApprovalExecutionFocus
              focus={{
                ...focus,
                close: () => {
                  select(active.key);
                  focus.close();
                },
              }}
              history={focusHistory}
            />
            <ScrollArea className="astra-session-transcript">
              <div ref={content} data-session-transcript={active.key}>
                {cache?.error && (
                  <p role="alert" className="astra-form-error">
                    Failed to load: {cache.error}
                    <button
                      className="astra-button"
                      onClick={() =>
                        cache.loaded && cache.hasMore ? void earlier(active.key) : void journal.load(active.key)
                      }
                    >
                      Retry
                    </button>
                  </p>
                )}
                {cache?.hasMore && (
                  <button
                    className="astra-session-history-button"
                    disabled={cache.loadingMore}
                    onClick={() => void earlier(active.key)}
                  >
                    {cache.loadingMore ? "Loading earlier history…" : "Load earlier history"}
                  </button>
                )}
                {!cache || (cache.loading && !cache.loaded) ? (
                  <p className="astra-session-placeholder" role="status">
                    <UroIcon name="sync" />
                    Loading activity stream…
                  </p>
                ) : activity.length ? (
                  <Transcript
                    sourceKey={`task:${taskId}:${active.key}`}
                    activity={activity}
                    live={active.live}
                    taskId={taskId}
                    chat={active.role === "mainagent"}
                    focusedSeq={focusHistory.ready ? focus.state?.source?.seq : undefined}
                  />
                ) : (
                  !cache.error && (
                    <div className="astra-session-placeholder">
                      <UroIcon name={roleIcon[active.role]} />
                      <h3>
                        {active.role === "mainagent"
                          ? "A clean conversation, the same operation"
                          : "Waiting for recorded activity"}
                      </h3>
                      <p>
                        {active.role === "mainagent"
                          ? "Message the Main Agent below to steer the exploration. The graph, assets and goals remain connected to this task."
                          : "New execution records will appear here through the task's live stream."}
                      </p>
                    </div>
                  )
                )}
              </div>
            </ScrollArea>
            <footer className="astra-session-composer">
              {active.role === "mainagent" && (
                <>
                  <div className="astra-session-attachments">
                    {composer.files.map((file) => (
                      <span key={file.path} title={file.path}>
                        <UroIcon name="workspace" />
                        {file.name}
                        <small>{file.size.toLocaleString()} B</small>
                        <button
                          aria-label={`Remove attachment ${file.name}`}
                          onClick={() => composer.removeFile(file.path)}
                        >
                          ×
                        </button>
                      </span>
                    ))}
                  </div>
                  <input
                    ref={fileInput}
                    type="file"
                    multiple
                    hidden
                    onChange={(event) => {
                      void composer.upload(event.target.files);
                      event.currentTarget.value = "";
                    }}
                  />
                  <MentionTextarea
                    aria-label="Message the Main Agent"
                    value={composer.main}
                    onValueChange={composer.setMain}
                    disabled={composer.sending}
                    placeholder={
                      composer.busy
                        ? "The Main Agent is running; type /btw to ask a question…"
                        : "Message the Main Agent; use @ to reference records…"
                    }
                    rows={2}
                    onKeyDown={(event) => {
                      if (shouldSubmitOnKey(event, sendMode)) {
                        event.preventDefault();
                        void composer.sendMain();
                      }
                    }}
                  />
                  <div className="astra-session-compose-actions">
                    <button
                      className="astra-button"
                      aria-label="Upload file"
                      disabled={composer.busy || composer.uploading}
                      onClick={() => fileInput.current?.click()}
                    >
                      <UroIcon name="workspace" />
                      {composer.uploading ? "Uploading…" : "Attach files"}
                    </button>
                    <span>@ Records · /btw Side question</span>
                    {composer.busy && isBtwCommand(composer.main) && (
                      <button
                        className="astra-button"
                        aria-label="Send side question"
                        onClick={() => void composer.sendMain()}
                      >
                        Ask
                      </button>
                    )}
                    {composer.busy ? (
                      <button
                        className="astra-button is-danger"
                        aria-label="Stop the current run"
                        disabled={composer.stopping}
                        onClick={() => void composer.stop()}
                      >
                        Stop run
                      </button>
                    ) : (
                      <button
                        className="astra-button is-primary"
                        aria-label="Send message"
                        disabled={composer.sending || (!composer.main.trim() && !composer.files.length)}
                        onClick={() => void composer.sendMain()}
                      >
                        {composer.sending ? "Sending…" : "Send message"}
                        <UroIcon name="arrow-up-right" />
                      </button>
                    )}
                  </div>
                </>
              )}
              {writableWorker && (
                <>
                  <MentionTextarea
                    aria-label={`Message Worker #${active.intent_id}`}
                    value={composer.worker}
                    onValueChange={composer.setWorker}
                    disabled={composer.workerSending}
                    rows={2}
                    placeholder={`Message Worker #${active.intent_id}; pause before sending, or use /btw…`}
                    onKeyDown={(event) => {
                      if (shouldSubmitOnKey(event, sendMode)) {
                        event.preventDefault();
                        void composer.sendWorker();
                      }
                    }}
                  />
                  <div className="astra-session-compose-actions">
                    <span>
                      {workerMessageLength(composer.worker)} / {workerMessageLimit} characters
                    </span>
                    {active.status === "running" && isBtwCommand(composer.worker) && (
                      <button
                        className="astra-button"
                        aria-label="Send side question"
                        onClick={() => void composer.sendWorker()}
                      >
                        Ask
                      </button>
                    )}
                    {active.status === "running" ? (
                      <button
                        className="astra-button"
                        aria-label="Pause the current Worker"
                        disabled={!!action}
                        onClick={() => void control(active, "pause")}
                      >
                        Pause Worker
                      </button>
                    ) : (
                      <>
                        <button
                          className="astra-button"
                          aria-label="Continue immediately"
                          disabled={!!action || composer.workerSending}
                          onClick={() => void control(active, "resume")}
                        >
                          Continue
                        </button>
                        <button
                          className="astra-button is-primary"
                          aria-label="Send message"
                          disabled={
                            composer.workerSending ||
                            !composer.worker.trim() ||
                            workerMessageLength(composer.worker) > workerMessageLimit
                          }
                          onClick={() => void composer.sendWorker()}
                        >
                          {composer.workerSending ? "Sending…" : "Send message"}
                        </button>
                      </>
                    )}
                  </div>
                </>
              )}
              <div className="astra-session-footer-tools">
                <TodoPopover
                  seq={latestTodo}
                  fetchDetail={(seq) => api.activityDetail(seq, taskId).then((row) => row.detail || "")}
                />
                {active.inherited ? (
                  <span>Inherited execution is read only.</span>
                ) : active.role === "planner" || active.role === "system" ? (
                  <span>Recorded activity · task source remains authoritative</span>
                ) : (
                  <span>Messages appear after the server persists them.</span>
                )}
              </div>
            </footer>
          </div>
        </SideQuestionWorkspace>
      </div>
      {inspecting && (
        <EditorDialog title="Session source" eyebrow="Session source" close={() => inspect(false)}>
          <p>{active.title}</p>
          <dl className="astra-session-source-facts">
            <dt>Session key</dt>
            <dd>{active.key}</dd>
            <dt>Task</dt>
            <dd>{taskId}</dd>
            <dt>State</dt>
            <dd>{active.role === "worker" ? active.status : active.live ? "Active" : "Idle"}</dd>
            <dt>Provenance</dt>
            <dd>{active.inherited ? `Inherited from task #${active.source_task_id}` : "Current task"}</dd>
          </dl>
          <h3>Bound assets</h3>
          {assets.length ? (
            assets.map((asset) => (
              <article className="astra-session-source-asset" key={`${asset.intent_id}:${asset.asset_id}`}>
                <strong>{asset.label || `#${asset.asset_id}`}</strong>
                <p>
                  {taskAssetTypeLabel(asset.type)} · {taskAssetSourceLabel(asset.source)}
                  {asset.inherited ? ` · source task #${asset.source_task_id}` : ""}
                </p>
                <p>{asset.source_summary}</p>
              </article>
            ))
          ) : (
            <p>No bound assets reported for this session.</p>
          )}
          <h3>{active.role === "worker" ? "Raw intent record" : "Session view metadata"}</h3>
          <pre className="astra-session-raw">{JSON.stringify(active.raw || active, null, 2)}</pre>
          <h3>Token totals</h3>
          <pre className="astra-session-raw">{JSON.stringify(tokens, null, 2)}</pre>
        </EditorDialog>
      )}
      {deleting && (
        <EditorDialog
          title={`Delete Worker #${deleting.intent_id}?`}
          eyebrow="Worker deletion"
          close={() => {
            chooseDeletion(undefined);
            fail("");
          }}
          busy={!!action}
        >
          <p>
            {mode === "hard"
              ? "Hard delete physically removes this intent and downstream nodes supported only by it. Shared nodes, goals and the task root are kept. This action cannot be undone."
              : "Soft delete marks the intent as deleted and records the reason. Its execution records, facts and vulnerabilities are kept. The planner is notified and can re-plan."}
          </p>
          <div className="astra-session-delete-modes">
            {(["soft", "hard"] as const).map((value) => (
              <button
                key={value}
                className="astra-button"
                aria-pressed={mode === value}
                disabled={!!action}
                onClick={() => setMode(value)}
              >
                <div>{value === "hard" ? "Hard delete" : "Soft delete"}</div>
                <small>{value === "hard" ? "Exclusive descendants are removed" : "Keep data and history"}</small>
              </button>
            ))}
          </div>
          <label htmlFor="cancel-reason">Deletion reason (required)</label>
          <textarea
            id="cancel-reason"
            value={reason}
            disabled={!!action}
            onChange={(event) => setReason(event.target.value)}
            rows={3}
            autoFocus
          />
          {failure && (
            <p role="alert" className="astra-form-error">
              {failure}
            </p>
          )}
          <footer className="astra-editor-actions">
            <button
              className="astra-button"
              disabled={!!action}
              onClick={() => {
                chooseDeletion(undefined);
                fail("");
              }}
            >
              Back
            </button>
            <button
              className="astra-button is-danger"
              disabled={!!action || !reason.trim()}
              onClick={() => void control(deleting, "cancel")}
            >
              {mode === "hard" ? "Permanently delete" : "Confirm deletion"}
            </button>
          </footer>
        </EditorDialog>
      )}
      {newMain && (
        <EditorDialog
          title="Start a new session?"
          eyebrow="Main Agent session"
          close={() => {
            confirmNewMain(false);
            fail("");
          }}
          busy={!!action}
        >
          <p>
            The Main Agent starts with a clean context. Existing sessions remain selectable and writable; the task
            graph, assets, scope and goals are retained.
          </p>
          {failure && (
            <p role="alert" className="astra-form-error">
              {failure}
            </p>
          )}
          <footer className="astra-editor-actions">
            <button
              className="astra-button"
              disabled={!!action}
              onClick={() => {
                confirmNewMain(false);
                fail("");
              }}
            >
              Cancel
            </button>
            <button className="astra-button is-primary" disabled={!!action} onClick={() => void createMain()}>
              {action ? "Starting…" : "Start new session"}
            </button>
          </footer>
        </EditorDialog>
      )}
    </section>
  );
}
