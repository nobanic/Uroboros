"use client";

import { useEffect, useRef, useState } from "react";
import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { resources, type Section } from "./resources";

export const consoleDestinations: Array<{ title: string; href: string; icon: UroIconName; hint: string }> = [
  { title: "Mission Control", href: "/dashboard", icon: "mission", hint: "Runtime overview and operator activity" },
  { title: "Command Chat", href: "/chat", icon: "chat", hint: "Interactive agent sessions" },
  { title: "Workspace", href: "/function/workspace", icon: "workspace", hint: "Files and investigation artifacts" },
  { title: "Tool Runs", href: "/function/commands", icon: "commands", hint: "Execution records and output" },
  { title: "Model Ledger", href: "/function/llm-records", icon: "records", hint: "Model requests and responses" },
  { title: "Signals", href: "/system/notify", icon: "notifications", hint: "Notification channels" },
  { title: "Intercept Rules", href: "/system/intercept", icon: "intercept", hint: "Network interception policy" },
  { title: "Asset Guard", href: "/system/intercept/assets", icon: "blocked", hint: "Asset admission rules" },
  { title: "Approvals", href: "/system/intercept/approvals", icon: "approvals", hint: "Pending operator decisions" },
  { title: "System Log", href: "/system/logs", icon: "logs", hint: "Runtime diagnostics" },
  { title: "Workspace settings", href: "/system/settings", icon: "settings", hint: "System configuration" },
];

export function ConsoleJump({ navigate }: { navigate: (section: Section) => void }) {
  const [open, show] = useState(false),
    [query, search] = useState(""),
    [active, select] = useState(0);
  const dialog = useRef<HTMLDialogElement>(null),
    input = useRef<HTMLInputElement>(null);
  const choices = [
    ...resources.map((resource) => ({
      title: resource.title,
      hint: resource.subtitle,
      section: resource.key,
      href: "",
      icon: "route" as UroIconName,
    })),
    ...consoleDestinations.map((destination) => ({ ...destination, section: undefined })),
  ].filter((choice) => `${choice.title} ${choice.hint}`.toLocaleLowerCase().includes(query.toLocaleLowerCase().trim()));
  useEffect(() => {
    const keyboard = (event: KeyboardEvent) => {
      if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "j") {
        if (document.querySelector("dialog[open]") && !dialog.current?.open) return;
        event.preventDefault();
        if (dialog.current?.open) {
          dialog.current.close();
          show(false);
        } else show(true);
      }
    };
    window.addEventListener("keydown", keyboard);
    return () => window.removeEventListener("keydown", keyboard);
  }, []);
  useEffect(() => {
    if (open) {
      search("");
      select(0);
      dialog.current?.showModal();
      input.current?.focus();
    } else dialog.current?.close();
  }, [open]);
  function go(index: number) {
    const choice = choices[index];
    if (!choice) return;
    dismiss();
    if (choice.section) navigate(choice.section);
    else window.location.assign(choice.href);
  }
  function dismiss() {
    dialog.current?.close();
    show(false);
  }
  return (
    <>
      <button className="astra-quick-jump" onClick={() => show(true)} aria-label="Jump to workspace section">
        <UroIcon name="search" aria-hidden="true" />
        <span>Jump to…</span>
        <kbd>⌘ / Ctrl J</kbd>
      </button>
      {open && (
        <dialog
          ref={dialog}
          className="astra-command-dialog"
          aria-labelledby="astra-jump-title"
          onCancel={() => show(false)}
          onClose={() => show(false)}
          onClick={(event) => {
            if (event.target !== event.currentTarget) return;
            const r = event.currentTarget.getBoundingClientRect();
            if (event.clientX < r.left || event.clientX > r.right || event.clientY < r.top || event.clientY > r.bottom)
              dismiss();
          }}
        >
          <h2 id="astra-jump-title">Navigate Uroboros</h2>
          <div className="astra-command-query">
            <UroIcon name="search" aria-hidden="true" />
            <input
              ref={input}
              aria-label="Find workspace section"
              role="combobox"
              aria-expanded="true"
              aria-controls="astra-jump-results"
              aria-activedescendant={choices[active] ? `astra-jump-${active}` : undefined}
              placeholder="Search tools, evidence, settings…"
              value={query}
              onChange={(event) => {
                search(event.target.value);
                select(0);
              }}
              onKeyDown={(event) => {
                if (event.key === "ArrowDown" || event.key === "ArrowUp") {
                  event.preventDefault();
                  const next = choices.length
                    ? (active + (event.key === "ArrowDown" ? 1 : -1) + choices.length) % choices.length
                    : 0;
                  select(next);
                  dialog.current?.querySelector(`[data-jump-index="${next}"]`)?.scrollIntoView({ block: "nearest" });
                }
                if (event.key === "Enter") {
                  event.preventDefault();
                  go(active);
                }
              }}
            />
            <button onClick={dismiss} aria-label="Close workspace jump">
              <UroIcon name="close" aria-hidden="true" />
            </button>
          </div>
          <div
            className="astra-command-results"
            id="astra-jump-results"
            role="listbox"
            aria-label="Matching workspace sections"
          >
            {choices.map((choice, index) => (
              <button
                key={choice.title}
                data-jump-index={index}
                id={`astra-jump-${index}`}
                role="option"
                aria-selected={active === index}
                data-active={active === index}
                onMouseEnter={() => select(index)}
                onClick={() => go(index)}
              >
                <UroIcon name={choice.icon} aria-hidden="true" />
                <span>
                  <strong>{choice.title}</strong>
                  <small>{choice.hint}</small>
                </span>
                <UroIcon name="arrow-up-right" aria-hidden="true" />
              </button>
            ))}
            {!choices.length && <p>No matching section.</p>}
          </div>
          <footer>
            <span>↑ ↓ choose · Enter open</span>
            <button onClick={dismiss}>Esc to close</button>
          </footer>
        </dialog>
      )}
    </>
  );
}
