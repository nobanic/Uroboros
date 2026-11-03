"use client";

import { useEffect, useMemo, useState, useSyncExternalStore } from "react";

import { ListTodo, RefreshCw } from "lucide-react";

import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";

import { todoMarker, todoStatus, todoSummary } from "./todo-document";
import { TodoReader } from "./todo-reader";
import "./todo-inspector.css";

export function TodoPopover({
  seq,
  fetchDetail,
}: {
  seq: number | null;
  fetchDetail: (seq: number) => Promise<string>;
}) {
  const [open, setOpen] = useState(false);
  const reader = useMemo(() => new TodoReader(), []);
  const state = useSyncExternalStore(reader.subscribe, reader.snapshot, reader.snapshot);
  useEffect(() => {
    if (open && seq != null) void reader.read(seq, fetchDetail);
    return () => reader.cancel();
  }, [open, seq, fetchDetail, reader]);
  const counts = todoSummary(state.entries ?? []),
    disabled = seq == null;
  const refresh = () => {
    if (seq != null) void reader.read(seq, fetchDetail);
  };
  return (
    <Popover open={open} onOpenChange={disabled ? undefined : setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="astra-todo-trigger"
          disabled={disabled}
          title={disabled ? "No Todo in this session yet" : "View recent Todo"}
        >
          <ListTodo size={13} />
          Todo
        </button>
      </PopoverTrigger>
      <PopoverContent align="end" className="astra-todo-panel">
        <header>
          <span className="astra-todo-emblem">
            <ListTodo size={17} />
          </span>
          <div>
            <strong>Recent Todo{state.loading ? " · Loading…" : ""}</strong>
            <small>Reported by TodoWrite</small>
          </div>
          <button
            type="button"
            className="astra-todo-refresh"
            title="Refresh Todo"
            aria-label="Refresh Todo"
            disabled={state.loading || disabled}
            onClick={refresh}
          >
            <RefreshCw size={14} />
          </button>
        </header>
        {state.error && (
          <div className="astra-todo-failure" role="alert">
            <p>{state.error}</p>
            <button type="button" onClick={refresh} disabled={state.loading}>
              Retry Todo
            </button>
          </div>
        )}
        {state.entries && (
          <>
            <div className="astra-todo-counts">
              <span>
                {counts.completed}/{state.entries.length} completed
              </span>
              <span>{counts.running} in progress</span>
              {counts.other > 0 && <span>{counts.other} other</span>}
            </div>
            {!state.entries.length && !state.loading && <p className="astra-todo-empty">(empty)</p>}
            <ul className="astra-todo-rows">
              {state.entries.map((entry, index) => (
                <li
                  key={`${index}:${entry.content}`}
                  data-status={typeof entry.status === "string" ? entry.status : undefined}
                >
                  <span aria-hidden="true">{todoMarker(entry.status)}</span>
                  <div>
                    <p>{entry.content}</p>
                    <small>{todoStatus(entry.status)}</small>
                  </div>
                </li>
              ))}
            </ul>
          </>
        )}
        <footer>
          <span>
            {state.source != null
              ? `Activity #${state.source}`
              : state.selected != null
                ? `Activity #${state.selected}`
                : "No activity selected"}
          </span>
          <span>
            {state.loading && state.source != null && state.source !== state.selected
              ? `Loading #${state.selected}`
              : "Read only"}
          </span>
        </footer>
      </PopoverContent>
    </Popover>
  );
}
