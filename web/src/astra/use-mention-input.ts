"use client";

import {
  type ChangeEvent,
  type KeyboardEvent,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from "react";

import { api } from "@/lib/api";
import { activeMention, mentionSearch, mentionToken, selectedMentions } from "@/lib/chat-mentions";

import { MentionLookup } from "./mention-lookup";

export function useMentionInput({
  value,
  onValueChange,
  onKeyDown,
  disabled,
}: {
  value: string;
  onValueChange: (value: string) => void;
  onKeyDown?: (event: KeyboardEvent<HTMLTextAreaElement>) => void;
  disabled?: boolean;
}) {
  const textarea = useRef<HTMLTextAreaElement>(null),
    composing = useRef(false),
    pendingIndex = useRef<number | null>(null);
  const [cursor, setCursor] = useState<number | null>(null),
    [dismissed, setDismissed] = useState(false),
    [index, setIndex] = useState(0);
  const lookup = useMemo(() => new MentionLookup(api.chatMentions), []);
  const result = useSyncExternalStore(lookup.subscribe, lookup.snapshot, lookup.snapshot);
  const listId = useId(),
    active = cursor === null ? null : activeMention(value, cursor);
  const search = mentionSearch(active?.query ?? ""),
    open = Boolean(active && !dismissed && !disabled);
  const queryKey = open && !search.categories.length ? `${search.kind}:${search.query}` : "";
  const current = Boolean(queryKey && result.key === queryKey),
    items = current ? result.items : [];
  const count = search.categories.length || items.length,
    nextCursor = current ? result.nextCursor : undefined;
  const loading = Boolean(queryKey && (!current || result.phase === "loading"));
  useEffect(() => {
    if (!queryKey) {
      lookup.cancel();
      return;
    }
    lookup.begin(search.kind, search.query);
    return () => lookup.cancel();
  }, [lookup, queryKey, search.kind, search.query]);
  useEffect(() => {
    if (open) document.getElementById(`${listId}-${index}`)?.scrollIntoView({ block: "nearest" });
  }, [open, listId, index]);
  useEffect(() => {
    if (open && pendingIndex.current !== null && pendingIndex.current < count) {
      document.getElementById(`${listId}-${pendingIndex.current}`)?.scrollIntoView({ block: "nearest" });
      pendingIndex.current = null;
    }
  }, [count, listId, open]);
  function replace(text: string, close: boolean) {
    if (!active) return;
    const next = value.slice(0, active.start) + text + value.slice(active.end),
      caret = active.start + text.length;
    onValueChange(next);
    setCursor(caret);
    setDismissed(close);
    setIndex(0);
    requestAnimationFrame(() => {
      textarea.current?.focus();
      textarea.current?.setSelectionRange(caret, caret);
    });
  }
  function choose(target: number) {
    const category = search.categories[target];
    if (category) replace(`@${category.label} `, false);
    else if (items[target]) replace(`${mentionToken(items[target])} `, true);
  }
  function more() {
    lookup.more(queryKey);
  }
  function keyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (composing.current || event.nativeEvent.isComposing || event.keyCode === 229) return;
    if (open) {
      if (event.key === "Escape") {
        event.preventDefault();
        setDismissed(true);
        return;
      }
      if (event.key === "ArrowDown" || event.key === "ArrowUp") {
        event.preventDefault();
        if (event.key === "ArrowDown" && count && index >= count - 1 && nextCursor) {
          pendingIndex.current = count;
          setIndex(count);
          more();
          return;
        }
        if (count) setIndex((previous) => (previous + (event.key === "ArrowDown" ? 1 : count - 1)) % count);
        return;
      }
      if ((event.key === "Enter" || event.key === "Tab") && !event.shiftKey && !event.ctrlKey && !event.metaKey) {
        if (event.key === "Tab" && (loading || !count)) {
          setDismissed(true);
          return;
        }
        event.preventDefault();
        if (!loading && count) choose(Math.min(index, count - 1));
        return;
      }
    }
    onKeyDown?.(event);
  }
  function change(event: ChangeEvent<HTMLTextAreaElement>) {
    pendingIndex.current = null;
    onValueChange(event.target.value);
    setCursor(event.target.selectionStart);
    setDismissed(false);
    setIndex(0);
  }
  return {
    textarea,
    listId,
    open,
    search,
    items,
    index,
    count,
    choose,
    keyDown,
    change,
    setCursor,
    loading,
    error: current ? result.error : "",
    nextCursor,
    loadingMore: current && result.phase === "paging",
    pageError: current ? result.pageError : "",
    more,
    dismiss: () => setDismissed(true),
    composition: (value: boolean) => {
      composing.current = value;
    },
    selected: selectedMentions(value),
    remove: (item: ReturnType<typeof selectedMentions>[number]) => {
      onValueChange(value.slice(0, item.start) + value.slice(item.start + item.token.length));
      setCursor(null);
    },
  };
}
