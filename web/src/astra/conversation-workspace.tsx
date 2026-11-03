"use client";
import { Suspense, useEffect, useRef, useState } from "react";
import { useSearchParams } from "next/navigation";
import { ThemeSwitcher } from "@/app/(main)/_components/sidebar/theme-switcher";
import { toast } from "sonner";
import { UroIcon } from "@/components/uroboros-icons";
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu";
import { getLocalStorageValue, setLocalStorageValue } from "@/lib/local-storage.client";
import { api } from "@/lib/api";
import type { Conversation } from "@/lib/types";
import { EditorDialog } from "./editor-dialog";
import {
  collapsedConversationGroupsKey,
  conversationAddress,
  conversationFilters,
  conversationGroups,
  conversationPageSize,
  conversationPinned,
  conversationalAgents,
  matchingConversations,
  restoreConversationGroups,
} from "./conversation-document";
import { ConversationView, DraftConversation, type ConversationDraft } from "./conversation-view";
import { useConversationCollection } from "./use-conversation-data";
import "./conversation.css";

function RenameConversation({ row, close, updated }: { row: Conversation; close: () => void; updated: () => void }) {
  const [text, write] = useState(row.title || ""),
    [busy, pending] = useState(false),
    [error, fail] = useState("");
  const ignore = useRef(false),
    lock = useRef(false),
    alive = useRef(true),
    input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    alive.current = true;
    const frame = requestAnimationFrame(() => {
      input.current?.focus();
      input.current?.select();
    });
    return () => {
      alive.current = false;
      cancelAnimationFrame(frame);
    };
  }, []);
  async function save() {
    if (ignore.current || lock.current) return;
    const title = text.trim();
    if (!title) {
      close();
      return;
    }
    lock.current = true;
    pending(true);
    try {
      await api.renameConversation(row.id, title);
      if (alive.current) {
        updated();
        close();
      }
    } catch (error) {
      if (alive.current) fail(`Rename failed: ${(error as Error).message}`);
    } finally {
      lock.current = false;
      if (alive.current) pending(false);
    }
  }
  return (
    <div className="astra-conversation-rename">
      <input
        aria-label={`Rename conversation ${row.id}`}
        ref={input}
        value={text}
        disabled={busy}
        onFocus={(event) => event.target.select()}
        onChange={(event) => write(event.target.value)}
        onBlur={() => {
          if (!error) void save();
        }}
        onKeyDown={(event) => {
          if (event.key === "Enter") {
            event.preventDefault();
            void save();
          }
          if (event.key === "Escape") {
            event.preventDefault();
            ignore.current = true;
            close();
          }
        }}
      />
      <button
        aria-label="Save conversation name"
        disabled={busy}
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => void save()}
      >
        ✓
      </button>
      <button
        aria-label="Cancel conversation rename"
        disabled={busy}
        onMouseDown={(event) => event.preventDefault()}
        onClick={() => {
          ignore.current = true;
          close();
        }}
      >
        ×
      </button>
      {error && <p role="alert">{error}</p>}
    </div>
  );
}
function ConversationWorkspace() {
  const params = useSearchParams(),
    selectedId = conversationAddress(params.get("c")),
    sourceRequested = params.has("approval"),
    data = useConversationCollection();
  const [agent, filterAgent] = useState(""),
    [search, find] = useState(""),
    [visible, showMore] = useState(conversationPageSize),
    [collapsed, setCollapsed] = useState(new Set<string>()),
    [selection, selectMode] = useState(false),
    [checked, mark] = useState(new Set<number>()),
    [editing, rename] = useState<number>(),
    [deleting, chooseDelete] = useState<{ ids: number[]; batch: boolean }>(),
    [deletingBusy, deleteBusy] = useState(false),
    [deleteError, failDelete] = useState(""),
    [outcomes, setOutcomes] = useState<{ id: number; ok: boolean; error?: string }[]>([]),
    [pinning, setPinning] = useState<number>(),
    [indexOpen, showIndex] = useState(false),
    [contextOpen, inspect] = useState(false),
    [handoffs, setHandoffs] = useState<Record<number, ConversationDraft>>({});
  const alive = useRef(true),
    deleteLock = useRef(false),
    pinLock = useRef(false);
  useEffect(() => {
    alive.current = true;
    setCollapsed(restoreConversationGroups(getLocalStorageValue(collapsedConversationGroupsKey)));
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    const ids = new Set(data.rows.map((row) => row.id));
    mark((previous) => {
      const next = new Set([...previous].filter((id) => ids.has(id)));
      return next.size === previous.size ? previous : next;
    });
  }, [data.rows]);
  useEffect(() => {
    showMore(conversationPageSize);
    mark(new Set());
    chooseDelete(undefined);
    rename(undefined);
  }, [agent, search]);
  const selected = data.rows.find((row) => row.id === selectedId),
    selectedAgent = selected ? data.agents.find((row) => row.key === selected.agent_key) : undefined;
  const matching = matchingConversations(data.rows, agent, search),
    visibleRows = matching.slice(0, visible),
    groups = conversationGroups(visibleRows, data.agents),
    pinned = visibleRows.filter(conversationPinned),
    agents = conversationalAgents(data.agents);
  function open(id: number | null) {
    const url = new URL(window.location.href);
    const previous = conversationAddress(url.searchParams.get("c"));
    if (id === null) url.searchParams.delete("c");
    else url.searchParams.set("c", String(id));
    if (id !== previous) url.searchParams.delete("approval");
    window.history.replaceState(null, "", url);
    showIndex(false);
  }
  function toggle(key: string) {
    const next = new Set(collapsed);
    if (!next.delete(key)) next.add(key);
    setCollapsed(next);
    setLocalStorageValue(collapsedConversationGroupsKey, JSON.stringify([...next]));
  }
  function requestDeletion(ids: number[], batch: boolean) {
    failDelete("");
    setOutcomes([]);
    chooseDelete({ ids, batch });
  }
  async function remove() {
    if (!deleting || deleteLock.current) return;
    deleteLock.current = true;
    deleteBusy(true);
    failDelete("");
    const removed = new Set<number>(),
      results: { id: number; ok: boolean; error?: string }[] = [];
    const apply = () => {
      if (!alive.current) return;
      data.remove(removed);
      mark((current) => new Set([...current].filter((id) => !removed.has(id))));
      const current = conversationAddress(new URLSearchParams(window.location.search).get("c"));
      if (current && removed.has(current)) open(null);
      setOutcomes([...results]);
    };
    try {
      if (deleting.batch) {
        for (let offset = 0; offset < deleting.ids.length; offset += 100) {
          const ids = deleting.ids.slice(offset, offset + 100),
            result = await api.deleteConversations(ids);
          for (const id of ids) {
            const item = result.items.find((item) => item.id === id) || { id, ok: false, error: "No outcome returned" };
            results.push(item);
            if (item.ok) removed.add(id);
          }
          apply();
        }
      } else {
        await api.deleteConversation(deleting.ids[0]);
        removed.add(deleting.ids[0]);
        results.push({ id: deleting.ids[0], ok: true });
        apply();
      }
      if (!alive.current) return;
      const failed = results.filter((item) => !item.ok);
      if (removed.size) toast.success(`Deleted ${removed.size} conversations`);
      if (failed.length) {
        failDelete(`Failed to delete ${failed.length} conversations. Successful deletions are saved.`);
        chooseDelete({ ...deleting, ids: failed.map((item) => item.id) });
      } else {
        chooseDelete(undefined);
        selectMode(false);
      }
      void data.reload(true);
    } catch (error) {
      apply();
      if (alive.current) {
        const remaining = deleting.ids.filter((id) => !removed.has(id));
        chooseDelete({ ...deleting, ids: remaining });
        failDelete(`Delete failed: ${(error as Error).message}. Confirmed deletions are saved.`);
        void data.reload(true);
      }
    } finally {
      deleteLock.current = false;
      if (alive.current) deleteBusy(false);
    }
  }
  async function pin(row: Conversation) {
    if (pinLock.current) return;
    pinLock.current = true;
    setPinning(row.id);
    try {
      await api.pinConversation(row.id, !conversationPinned(row));
      if (alive.current) void data.reload(true);
    } catch (error) {
      if (alive.current)
        toast.error(`Failed to ${conversationPinned(row) ? "unpin" : "pin"}: ${(error as Error).message}`);
    } finally {
      pinLock.current = false;
      if (alive.current) setPinning(undefined);
    }
  }
  function row(item: Conversation) {
    const current = item.id === selectedId;
    return (
      <article key={item.id} className="astra-conversation-row" data-thread={item.id} data-active={current}>
        {selection && (
          <input
            type="checkbox"
            aria-label={`Select conversation ${item.id}`}
            checked={checked.has(item.id)}
            disabled={deletingBusy}
            onChange={(event) =>
              mark((previous) => {
                const next = new Set(previous);
                if (event.target.checked) next.add(item.id);
                else next.delete(item.id);
                return next;
              })
            }
          />
        )}
        {editing === item.id ? (
          <RenameConversation row={item} close={() => rename(undefined)} updated={() => void data.reload(true)} />
        ) : (
          <button
            className="astra-conversation-select"
            aria-current={current ? "true" : undefined}
            onClick={() => open(item.id)}
            onDoubleClick={() => rename(item.id)}
            title="Double-click to rename"
          >
            <strong>
              {conversationPinned(item) && <span aria-label="Pinned">⌖</span>}
              {item.title || "New conversation"}
              {item.running && <i aria-label="Running" />}
            </strong>
            <small>
              {data.agents.find((agent) => agent.key === item.agent_key)?.name || item.agent_key} · #{item.id}
            </small>
            <time dateTime={item.created_at}>
              {new Date(item.created_at).toLocaleDateString("en-US", {
                month: "numeric",
                day: "numeric",
                hour: "2-digit",
                minute: "2-digit",
              })}
            </time>
          </button>
        )}
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <button className="astra-conversation-row-menu" aria-label={`Manage conversation ${item.id}`}>
              ···
            </button>
          </DropdownMenuTrigger>
          <DropdownMenuContent
            className="astra-conversation-menu"
            align="end"
            onCloseAutoFocus={(event) => {
              if (editing === item.id) event.preventDefault();
            }}
          >
            <DropdownMenuItem onSelect={() => rename(item.id)}>Rename</DropdownMenuItem>
            <DropdownMenuItem disabled={pinning === item.id} onSelect={() => void pin(item)}>
              {conversationPinned(item) ? "Unpin" : "Pin"}
            </DropdownMenuItem>
            <DropdownMenuItem variant="destructive" onSelect={() => requestDeletion([item.id], false)}>
              Delete
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </article>
    );
  }
  const context = (
    <>
      <div className="astra-conversation-context-signal">
        <UroIcon name={selected?.running ? "pulse" : "agents"} />
        <small>{selected ? "Active transmission" : "Channel staging"}</small>
        <h3>{selected?.title || selectedAgent?.name || "New agent channel"}</h3>
        <p>
          {selected
            ? "Context remains bound to this conversation."
            : "A conversation is created when you send a message or attach a file."}
        </p>
      </div>
      <dl>
        {[
          ["Operator", selectedAgent?.name || (selected ? selected.agent_key : "Auto routing")],
          ["Channel", selected ? `#${selected.id}` : "Draft"],
          ["State", selected?.running ? "Running" : "Ready"],
          ["Model profiles", `${data.profiles.length} available`],
        ].map(([name, value]) => (
          <div key={name}>
            <dt>{name}</dt>
            <dd>{value}</dd>
          </div>
        ))}
      </dl>
      <section className="astra-conversation-protocol">
        <small>Briefing protocol</small>
        {[
          ["01", "Objective", "State the decision or outcome."],
          ["02", "Evidence", "Reference records with @ mentions."],
          ["03", "Boundary", "Declare scope and stop conditions."],
        ].map(([number, title, hint]) => (
          <div key={number}>
            <b>{number}</b>
            <p>
              <strong>{title}</strong>
              <span>{hint}</span>
            </p>
          </div>
        ))}
      </section>
      <footer>
        <UroIcon name="shield" />
        Attachments remain scoped to this conversation session.
      </footer>
    </>
  );
  return (
    <section className="astra-conversations" data-index-open={indexOpen}>
      <header className="astra-conversations-command">
        <div>
          <small>Uroboros · Agent communications</small>
          <h1>Command Chat</h1>
          <p>Brief specialists. Follow execution. Keep the evidence in context.</p>
        </div>
        <dl>
          <div>
            <dt>Threads</dt>
            <dd>{data.rows.length}</dd>
          </div>
          <div>
            <dt>Running</dt>
            <dd>{data.rows.filter((row) => row.running).length}</dd>
          </div>
          <div>
            <dt>Agents</dt>
            <dd>{agents.length}</dd>
          </div>
        </dl>
        <button
          className="astra-conversation-index-toggle"
          aria-expanded={indexOpen}
          onClick={() => showIndex(!indexOpen)}
        >
          Conversations
        </button>
        <ThemeSwitcher />
        <button aria-label="Inspect channel context" onClick={() => inspect(true)}>
          <UroIcon name="info" />
        </button>
        <button className="is-primary" onClick={() => open(null)}>
          + New conversation
        </button>
      </header>
      {(data.error || data.metadataError) && (
        <div role="alert" className="astra-conversation-error">
          <span>{[data.error, data.metadataError].filter(Boolean).join(" · ")}</span>
          <button
            onClick={() => {
              void data.reload(true);
              void data.metadata();
            }}
          >
            Retry chat data
          </button>
        </div>
      )}
      <div className="astra-conversations-body">
        <aside className="astra-conversation-index">
          <header>
            <div>
              <UroIcon name="agents" />
              <strong>Conversations</strong>
            </div>
            <button aria-label="Refresh conversations" onClick={() => void data.reload(true)}>
              <UroIcon name="replay" />
            </button>
          </header>
          <div className="astra-conversation-filter">
            <input
              type="search"
              aria-label="Search conversations"
              placeholder="Search threads…"
              value={search}
              onChange={(event) => find(event.target.value)}
            />
            <select
              aria-label="Filter conversation agent"
              value={agent}
              onChange={(event) => filterAgent(event.target.value)}
            >
              <option value="">All agents</option>
              {conversationFilters(data.rows, data.agents, agent).map((choice) => (
                <option key={choice.key} value={choice.key}>
                  {choice.name} ({choice.count})
                </option>
              ))}
            </select>
            <div>
              <span>
                {checked.size ? `${checked.size} selected` : `${matching.length} / ${data.rows.length} threads`}
              </span>
              <button
                onClick={() => {
                  selectMode(!selection);
                  mark(new Set());
                }}
              >
                {selection ? "Done" : "Multi-select"}
              </button>
            </div>
            {selection && (
              <div>
                <label>
                  <input
                    type="checkbox"
                    aria-label="Select all conversations in the current filter"
                    checked={matching.length > 0 && matching.every((row) => checked.has(row.id))}
                    ref={(node) => {
                      if (node) node.indeterminate = checked.size > 0 && !matching.every((row) => checked.has(row.id));
                    }}
                    onChange={(event) =>
                      mark(event.target.checked ? new Set(matching.map((row) => row.id)) : new Set())
                    }
                  />
                  Select matching
                </label>
                <button
                  className="is-danger"
                  disabled={!checked.size || deletingBusy}
                  onClick={() => requestDeletion([...checked], true)}
                >
                  Delete selected
                </button>
              </div>
            )}
          </div>
          <div className="astra-conversation-roster">
            {pinned.length > 0 && (
              <section>
                <h3>
                  <span aria-label="Pinned">⌖</span>Pinned<span>{pinned.length}</span>
                </h3>
                {pinned.map(row)}
              </section>
            )}
            {groups.map((group) => (
              <section key={group.key}>
                <button
                  className="astra-conversation-group"
                  aria-expanded={!collapsed.has(group.key)}
                  onClick={() => toggle(group.key)}
                >
                  <UroIcon name="chevron" />
                  <strong>{group.name}</strong>
                  {group.running > 0 && <i title={`${group.running} running`} />}
                  <span>{group.items.length}</span>
                </button>
                {!collapsed.has(group.key) && group.items.map(row)}
              </section>
            ))}
            {!matching.length && (
              <p className="astra-conversation-placeholder">
                {data.loaded ? "No matching conversations" : "Loading conversations…"}
              </p>
            )}
            {visible < matching.length && (
              <button
                className="astra-conversation-load"
                onClick={() => showMore((value) => value + conversationPageSize)}
              >
                Load more conversations
              </button>
            )}
          </div>
        </aside>
        <main className="astra-conversation-channel">
          {selected ? (
            <ConversationView
              key={selected.id}
              conversation={selected}
              agents={data.agents}
              profiles={data.profiles}
              initial={handoffs[selected.id]}
              refresh={() => void data.reload(true)}
              consumed={() =>
                setHandoffs((previous) => {
                  if (!previous[selected.id]) return previous;
                  const next = { ...previous };
                  delete next[selected.id];
                  return next;
                })
              }
            />
          ) : selectedId !== null && !data.loaded ? (
            <div className="astra-conversation-placeholder" role="status">
              {data.error ? "Conversation data is unavailable. Retry the read above." : "Loading the conversation…"}
            </div>
          ) : sourceRequested ? (
            <div className="astra-conversation-placeholder" role="status">
              {data.loaded ? "This conversation has been deleted" : "Loading the conversation…"}
            </div>
          ) : (
            <DraftConversation
              agents={agents}
              profiles={data.profiles}
              started={(conversation, draft) => {
                if (agent && agent !== conversation.agent_key) filterAgent("");
                if (draft) setHandoffs((previous) => ({ ...previous, [conversation.id]: draft }));
                data.insert(conversation);
                open(conversation.id);
                void data.reload(true);
              }}
            />
          )}
        </main>
        <aside className="astra-conversation-context">{context}</aside>
      </div>
      {contextOpen && (
        <EditorDialog title="Channel context" eyebrow="Channel context" close={() => inspect(false)}>
          <div className="astra-conversation-context is-dialog">{context}</div>
        </EditorDialog>
      )}
      {deleting && (
        <EditorDialog
          title={`Delete ${deleting.ids.length} conversation${deleting.ids.length === 1 ? "" : "s"}?`}
          eyebrow="Conversation deletion"
          close={() => {
            chooseDelete(undefined);
            failDelete("");
          }}
          busy={deletingBusy}
        >
          <p>
            The conversation messages and execution records will be deleted along with them. This action cannot be
            undone.
          </p>
          <p className="astra-conversation-delete-ids">{deleting.ids.map((id) => `#${id}`).join(", ")}</p>
          {deleteError && (
            <p role="alert" className="astra-conversation-error">
              {deleteError}
            </p>
          )}
          {outcomes.length > 0 && (
            <ul className="astra-conversation-outcomes">
              {outcomes.map((result) => (
                <li key={result.id} data-ok={result.ok}>
                  #{result.id}: {result.ok ? "Deleted" : result.error || "Failed"}
                </li>
              ))}
            </ul>
          )}
          <footer className="astra-conversation-dialog-actions">
            <button disabled={deletingBusy} onClick={() => chooseDelete(undefined)}>
              Cancel
            </button>
            <button className="is-danger" disabled={deletingBusy || !deleting.ids.length} onClick={() => void remove()}>
              {deletingBusy ? "Deleting…" : "Confirm delete"}
            </button>
          </footer>
        </EditorDialog>
      )}
    </section>
  );
}
export function ConversationRoute() {
  return (
    <Suspense fallback={null}>
      <ConversationWorkspace />
    </Suspense>
  );
}
