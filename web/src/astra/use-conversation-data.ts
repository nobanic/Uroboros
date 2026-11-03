"use client";
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import type { Agent, Conversation, LLMProfile } from "@/lib/types";
import { ConversationJournal, type ConversationHistory } from "./conversation-journal";

export function useConversationCollection() {
  const [rows, setRows] = useState<Conversation[]>([]),
    [agents, setAgents] = useState<Agent[]>([]),
    [profiles, setProfiles] = useState<LLMProfile[]>([]),
    [loaded, setLoaded] = useState(false),
    [error, fail] = useState(""),
    [metadataError, failMetadata] = useState("");
  const active = useRef(false),
    sequence = useRef(0),
    metadataSequence = useRef(0),
    pending = useRef<number | undefined>(undefined);
  const reload = useCallback(async (force = false) => {
    if (pending.current !== undefined && !force) return;
    const request = ++sequence.current;
    pending.current = request;
    try {
      const items = await api.conversations();
      if (active.current && request === sequence.current) {
        setRows(items);
        setLoaded(true);
        fail("");
      }
    } catch (error) {
      if (active.current && request === sequence.current) fail((error as Error).message);
    } finally {
      if (request === pending.current) pending.current = undefined;
    }
  }, []);
  const metadata = useCallback(async () => {
    const request = ++metadataSequence.current;
    const problems: string[] = [];
    await Promise.all([
      api
        .agents()
        .then((value) => {
          if (active.current && request === metadataSequence.current) setAgents(value);
        })
        .catch((error) => problems.push(`Agents: ${error.message}`)),
      api
        .llmProfiles()
        .then((value) => {
          if (active.current && request === metadataSequence.current) setProfiles(value);
        })
        .catch((error) => problems.push(`Profiles: ${error.message}`)),
    ]);
    if (active.current && request === metadataSequence.current) failMetadata(problems.join(" · "));
  }, []);
  useEffect(() => {
    active.current = true;
    void metadata();
    let timer: ReturnType<typeof setTimeout>,
      alive = true;
    async function poll() {
      await reload();
      if (alive) timer = setTimeout(() => void poll(), 2000);
    }
    void poll();
    return () => {
      alive = false;
      active.current = false;
      sequence.current++;
      pending.current = undefined;
      clearTimeout(timer);
    };
  }, [reload, metadata]);
  const insert = useCallback((row: Conversation) => {
    sequence.current++;
    pending.current = undefined;
    setRows((previous) =>
      previous.some((item) => item.id === row.id)
        ? previous
        : [
            ...previous.filter((item) => item.pinned ?? !!item.pinned_at),
            row,
            ...previous.filter((item) => !(item.pinned ?? !!item.pinned_at)),
          ],
    );
  }, []);
  const remove = useCallback((ids: Set<number>) => {
    sequence.current++;
    pending.current = undefined;
    setRows((previous) => previous.filter((row) => !ids.has(row.id)));
  }, []);
  return { rows, agents, profiles, loaded, error, metadataError, reload, metadata, insert, remove };
}

export function useConversationJournal(id: number, serverRunning: boolean | undefined, settled: () => void) {
  const notify = useRef(settled);
  notify.current = settled;
  const controller = useRef<ConversationJournal | null>(null);
  const [state, update] = useState<ConversationHistory>({
    items: [],
    cursor: 0,
    before: 0,
    more: false,
    ready: false,
    running: false,
    loading: false,
    error: "",
    olderError: "",
    tailError: "",
  });
  useEffect(() => {
    const journal = new ConversationJournal({
      history: (before) => api.conversationHistory(id, before, 200),
      tail: (since) => api.conversationMessages(id, since),
      changed: update,
      settled: () => notify.current(),
    });
    controller.current = journal;
    void journal.recent();
    if (serverRunning) journal.run();
    return () => {
      journal.dispose();
      if (controller.current === journal) controller.current = null;
    };
  }, [id]);
  useEffect(() => {
    if (serverRunning) controller.current?.run();
  }, [serverRunning]);
  useEffect(() => {
    if (!state.running) return;
    let alive = true,
      timer: ReturnType<typeof setTimeout>;
    const journal = controller.current;
    async function tick() {
      await journal?.tail();
      if (alive && journal?.state.running) timer = setTimeout(() => void tick(), 1000);
    }
    void tick();
    return () => {
      alive = false;
      clearTimeout(timer);
    };
  }, [state.running, id]);
  const actions = useRef({
    recent: () => controller.current?.recent(),
    earlier: () => controller.current?.earlier() ?? Promise.resolve(false),
    run: () => controller.current?.run(),
    merge: (page: { items: ConversationHistory["items"]; hasMore?: boolean }) => controller.current?.mergeHistory(page),
  }).current;
  return { ...state, ...actions };
}
