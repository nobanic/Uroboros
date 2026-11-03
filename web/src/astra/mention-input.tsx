"use client";

import type { ComponentProps } from "react";

import { AtSign, ChevronRight, Loader2, Search, X } from "lucide-react";

import { InputGroupTextarea } from "@/components/ui/input-group";
import { Popover, PopoverAnchor, PopoverContent } from "@/components/ui/popover";
import { Textarea } from "@/components/ui/textarea";
import { mentionKinds } from "@/lib/chat-mentions";

import { useMentionInput } from "./use-mention-input";
import "./mention-input.css";

type Props = Omit<ComponentProps<"textarea">, "value" | "onChange" | "ref"> & {
  value: string;
  onValueChange: (value: string) => void;
  inputGroup?: boolean;
};

export function MentionTextarea({ value, onValueChange, onKeyDown, className, inputGroup, disabled, ...props }: Props) {
  const input = useMentionInput({ value, onValueChange, onKeyDown, disabled });
  const { categories } = input.search;
  const Control = inputGroup ? InputGroupTextarea : Textarea;
  const categoryName = mentionKinds.find((kind) => kind.kind === input.search.kind)?.label ?? "all records";
  return (
    <div className="astra-reference-field">
      <Popover
        open={input.open}
        onOpenChange={(next) => {
          if (!next) input.dismiss();
        }}
      >
        <PopoverAnchor asChild>
          <div>
            <Control
              {...props}
              ref={input.textarea}
              value={value}
              disabled={disabled}
              className={className}
              aria-label={props["aria-label"] ?? "Message, type @ to reference a record"}
              aria-autocomplete="list"
              aria-controls={input.open ? input.listId : undefined}
              aria-expanded={input.open}
              aria-activedescendant={
                input.open && input.count ? `${input.listId}-${Math.min(input.index, input.count - 1)}` : undefined
              }
              onChange={input.change}
              onSelect={(event) => input.setCursor(event.currentTarget.selectionStart)}
              onCompositionStart={() => input.composition(true)}
              onCompositionEnd={() => input.composition(false)}
              onKeyDown={input.keyDown}
            />
          </div>
        </PopoverAnchor>
        <PopoverContent
          side="top"
          align="start"
          className="astra-reference-menu"
          onOpenAutoFocus={(event) => event.preventDefault()}
          onCloseAutoFocus={(event) => event.preventDefault()}
          onInteractOutside={(event) => {
            if (event.target === input.textarea.current) event.preventDefault();
          }}
        >
          <header>
            <span className="astra-reference-emblem">
              <AtSign size={17} />
            </span>
            <div>
              <strong>{categories.length ? "Select a reference type" : `Search ${categoryName}`}</strong>
              <small>Link a source record to your message</small>
            </div>
            <kbd>Esc</kbd>
          </header>
          <div
            id={input.listId}
            role="listbox"
            aria-label="Reference candidates"
            className="astra-reference-options"
            onScroll={(event) => {
              const area = event.currentTarget;
              if (area.scrollHeight - area.scrollTop - area.clientHeight < 64) input.more();
            }}
          >
            {categories.map((kind, index) => (
              <button
                key={kind.kind}
                id={`${input.listId}-${index}`}
                type="button"
                role="option"
                aria-selected={input.index === index}
                data-selected={input.index === index}
                className="astra-reference-category"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => input.choose(index)}
              >
                <span className="astra-reference-kind">
                  <AtSign size={13} />
                </span>
                <span>
                  {kind.label}
                  <small>@{kind.alias}</small>
                </span>
                <ChevronRight size={14} />
              </button>
            ))}
            {!categories.length && input.loading && (
              <div role="status" className="astra-reference-message">
                <Loader2 size={15} className="astra-reference-spin" />
                Searching…
              </div>
            )}
            {!categories.length && !input.loading && input.error && (
              <div role="alert" className="astra-reference-message astra-reference-failure">
                Search failed: {input.error}. Re-enter to try again.
              </div>
            )}
            {!categories.length && !input.loading && !input.error && !input.items.length && (
              <div role="status" className="astra-reference-message">
                <Search size={16} />
                <span>No matching records; try a different name, address, or ID</span>
              </div>
            )}
            {input.items.map((item, index) => (
              <button
                key={`${item.kind}:${item.id}`}
                id={`${input.listId}-${index}`}
                type="button"
                role="option"
                aria-selected={input.index === index}
                data-selected={input.index === index}
                className="astra-reference-record"
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => input.choose(index)}
              >
                <span className="astra-reference-record-heading">
                  <span className="astra-reference-badge">
                    {mentionKinds.find((kind) => kind.kind === item.kind)?.label}
                  </span>
                  <strong>{item.label}</strong>
                  <code>#{item.id}</code>
                </span>
                <small>{item.description}</small>
              </button>
            ))}
          </div>
          {!categories.length && (
            <p className="astra-reference-page">
              {input.loadingMore && <span role="status">Loading more…</span>}
              {!input.loadingMore && input.nextCursor && (
                <button type="button" onMouseDown={(event) => event.preventDefault()} onClick={input.more}>
                  {input.pageError
                    ? "Load failed, click to retry"
                    : `Showing ${input.items.length}, scroll down to load more`}
                </button>
              )}
              {!input.loadingMore &&
                !input.nextCursor &&
                !input.loading &&
                !input.error &&
                input.items.length > 0 &&
                `Showing all ${input.items.length}`}
              {!input.loadingMore &&
                !input.nextCursor &&
                (input.loading || Boolean(input.error) || input.items.length === 0) &&
                "Type a name, address, or ID to search records"}
            </p>
          )}
          <footer>
            <span>↑↓ Navigate · Enter Confirm · Esc Close</span>
            <span>{categories.length ? "9 sources" : `${input.items.length} loaded`}</span>
          </footer>
        </PopoverContent>
      </Popover>
      {input.selected.length > 0 && (
        <div className="astra-reference-selection">
          {input.selected.map((item) => (
            <span className="astra-reference-chip" key={item.start}>
              <span title={item.label}>{item.label}</span>
              <button
                type="button"
                disabled={disabled}
                aria-label={`Remove reference ${item.label}`}
                onClick={() => input.remove(item)}
              >
                <X size={12} />
              </button>
            </span>
          ))}
          <span className="astra-reference-authority">Latest details are fetched on send · up to 10</span>
        </div>
      )}
    </div>
  );
}
