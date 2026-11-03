"use client";

import { useCallback, useReducer, useRef, type ChangeEvent, type KeyboardEvent } from "react";
import { applyAssetDslSuggestion, getAssetDslSuggestions, type AssetDslSuggestion } from "@/lib/asset-dsl/suggestions";

type DslSession = { suggestions: AssetDslSuggestion[]; selIdx: number; open: boolean };
type DslAction =
  | { type: "refresh"; suggestions: AssetDslSuggestion[] }
  | { type: "move"; direction: 1 | -1; limit: number }
  | { type: "select"; index: number }
  | { type: "visibility"; open: boolean };

function initialDslSession(): DslSession {
  return { suggestions: [], selIdx: 0, open: false };
}

function reduceDslSession(state: DslSession, action: DslAction): DslSession {
  switch (action.type) {
    case "refresh":
      return { suggestions: action.suggestions, selIdx: 0, open: action.suggestions.length > 0 };
    case "move":
      return {
        ...state,
        selIdx: action.direction === 1 ? Math.min(state.selIdx + 1, action.limit - 1) : Math.max(state.selIdx - 1, 0),
      };
    case "select":
      return { ...state, selIdx: action.index };
    case "visibility":
      return { ...state, open: action.open };
  }
}

// One input session owns completion/selection/visibility. The controlled query
// and all API debounce/scope/stale-response ownership remain with its parent.
export function useAssetDslSession(query: string, onChange: (value: string) => void) {
  const inputRef = useRef<HTMLInputElement>(null);
  const [state, dispatch] = useReducer(reduceDslSession, undefined, initialDslSession);
  const refresh = useCallback((value: string, cursor: number) => {
    dispatch({ type: "refresh", suggestions: getAssetDslSuggestions(value, cursor) });
  }, []);
  const setSelIdx = useCallback((index: number) => dispatch({ type: "select", index }), []);
  const setOpen = useCallback((open: boolean) => dispatch({ type: "visibility", open }), []);
  const apply = useCallback(
    (suggestion: AssetDslSuggestion) => {
      const cursor = inputRef.current?.selectionStart ?? query.length;
      const edit = applyAssetDslSuggestion(query, suggestion, cursor);
      onChange(edit.text);
      requestAnimationFrame(() => {
        if (!inputRef.current) return;
        inputRef.current.setSelectionRange(edit.cursor, edit.cursor);
        inputRef.current.focus();
        refresh(edit.text, edit.cursor);
      });
    },
    [query, onChange, refresh],
  );

  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    const value = event.target.value;
    onChange(value);
    refresh(value, event.target.selectionStart ?? value.length);
  };
  const handleKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (!state.open || state.suggestions.length === 0) return;
    switch (event.key) {
      case "ArrowDown":
      case "ArrowUp":
        event.preventDefault();
        // Capture the rendered list's bound, as the original event closure did.
        dispatch({ type: "move", direction: event.key === "ArrowDown" ? 1 : -1, limit: state.suggestions.length });
        break;
      case "Tab":
      case "Enter": {
        const selected = state.suggestions[state.selIdx];
        if (selected) {
          event.preventDefault();
          apply(selected);
        }
        break;
      }
      case "Escape":
        setOpen(false);
        break;
    }
  };

  return {
    ...state,
    inputRef,
    refresh,
    apply,
    handleChange,
    handleKeyDown,
    setSelIdx,
    setOpen,
    cursorPos: () => inputRef.current?.selectionStart ?? query.length,
  };
}
