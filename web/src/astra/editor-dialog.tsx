"use client";

import { useEffect, useRef, type ReactNode } from "react";

export function EditorDialog({
  title,
  eyebrow,
  busy,
  close,
  children,
}: {
  title: string;
  eyebrow: string;
  busy?: boolean;
  close: () => void;
  children: ReactNode;
}) {
  const element = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const dialog = element.current;
    dialog?.showModal();
    return () => dialog?.close();
  }, []);
  return (
    <dialog
      ref={element}
      className="astra-editor-panel"
      aria-label={title}
      onCancel={(event) => {
        event.preventDefault();
        if (!busy) close();
      }}
    >
      <header>
        <div>
          <small>{eyebrow}</small>
          <h2>{title}</h2>
        </div>
        <button type="button" disabled={busy} aria-label={`Close ${eyebrow.toLowerCase()} editor`} onClick={close}>
          ×
        </button>
      </header>
      <div className="astra-editor-body">{children}</div>
    </dialog>
  );
}
