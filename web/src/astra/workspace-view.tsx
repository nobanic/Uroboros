"use client";
import { type KeyboardEvent as ReactKeyboardEvent, useCallback, useEffect, useMemo, useRef, useState } from "react";

import Link from "next/link";

import {
  Download,
  FileCode2,
  FileText,
  Folder,
  FolderOpen,
  FolderPlus,
  HardDrive,
  RefreshCw,
  Save,
  Search,
  Trash2,
  Upload,
} from "lucide-react";

import { consoleModuleForPath } from "@/components/uroboros-module-workspace";
import type { WorkspaceEntry } from "@/lib/types";

import { useWorkspace } from "./use-workspace";
import {
  workspaceCrumbs,
  workspaceEditable,
  workspaceFacts,
  workspaceLineCount,
  workspaceSize,
  workspaceTime,
  workspaceVisible,
} from "./workspace-document";
import { type WorkspaceEdit, workspaceDirty } from "./workspace-journal";
import "./workspace-view.css";

type WorkspaceView = ReturnType<typeof useWorkspace>;
const context = consoleModuleForPath("/function/workspace");
function navigateWorkspaceFiles(event: ReactKeyboardEvent<HTMLButtonElement>) {
  if (!["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
  const container = event.currentTarget.closest(".astra-files-entries");
  const buttons = Array.from(container?.querySelectorAll<HTMLButtonElement>("[data-workspace-open]") ?? []);
  const index = buttons.indexOf(event.currentTarget);
  let next = index;
  if (event.key === "Home") next = 0;
  else if (event.key === "End") next = buttons.length - 1;
  else next = Math.max(0, Math.min(buttons.length - 1, index + (event.key === "ArrowDown" ? 1 : -1)));
  event.preventDefault();
  buttons[next]?.focus();
}
function WorkspaceError({ text, retry }: { text: string; retry?: () => void }) {
  return text ? (
    <div className="astra-files-error" role="alert">
      <span>{text}</span>
      {retry && (
        <button type="button" onClick={retry}>
          Retry
        </button>
      )}
    </div>
  ) : null;
}
function WorkspaceModal({
  title,
  busy,
  close,
  children,
}: {
  title: string;
  busy: boolean;
  close: () => void;
  children: React.ReactNode;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  useEffect(() => {
    const element = dialog.current,
      trigger = document.activeElement;
    element?.showModal();
    element?.querySelector<HTMLElement>("[data-initial-focus]")?.focus();
    return () => {
      element?.close();
      if (trigger instanceof HTMLElement && trigger.isConnected) trigger.focus();
    };
  }, []);
  return (
    <dialog
      ref={dialog}
      className="astra-files-modal"
      aria-label={title}
      onCancel={(event) => {
        event.preventDefault();
        if (!busy) close();
      }}
    >
      <header>
        <small>Uroboros / operator files</small>
        <h2>{title}</h2>
      </header>
      {children}
    </dialog>
  );
}
function WorkspaceText({ edit, draft }: { edit: WorkspaceEdit; draft: (value: string) => void }) {
  const [top, setTop] = useState(0),
    [wrap, setWrap] = useState(false);
  const lineCount = useMemo(() => workspaceLineCount(edit.content), [edit.content]);
  const first = Math.max(0, Math.floor(top / 20) - 2),
    count = Math.min(96, lineCount - first);
  const lineNumbers = Array.from({ length: Math.max(0, count) }, (_, index) => first + index + 1);
  return (
    <>
      <div className="astra-files-codebar">
        <span>{lineCount.toLocaleString()} lines · UTF-8 text</span>
        <button type="button" aria-pressed={wrap} onClick={() => setWrap(!wrap)}>
          Wrap lines
        </button>
      </div>
      <div className="astra-files-code" data-wrap={wrap}>
        {!wrap && (
          <div className="astra-files-lines" aria-hidden="true">
            <div style={{ transform: `translateY(${first * 20 - top}px)` }}>
              {lineNumbers.map((number) => (
                <span key={number}>{number}</span>
              ))}
            </div>
          </div>
        )}
        <textarea
          aria-label={`Edit ${edit.file.path}`}
          value={edit.content}
          onChange={(event) => draft(event.target.value)}
          onScroll={(event) => setTop(event.currentTarget.scrollTop)}
          spellCheck={false}
          wrap={wrap ? "soft" : "off"}
        />
      </div>
    </>
  );
}
function WorkspaceInspector({ journal, state, dirty }: WorkspaceView) {
  const edit = state.edit,
    writing = Boolean(state.writing);
  return (
    <aside className="astra-files-inspector" aria-label="File inspector" aria-busy={Boolean(state.opening)}>
      {edit ? (
        <>
          <header>
            <div>
              <small>Evidence inspector</small>
              <span>{workspaceSize(edit.file.size)}</span>
            </div>
            <h2 title={edit.file.path}>{edit.file.path}</h2>
            <p>{workspaceEditable(edit.file) ? "Editable text evidence" : "Original artifact · download to inspect"}</p>
          </header>
          <div className="astra-files-state">
            <span data-dirty={dirty}>
              <i />
              {dirty ? "Local changes" : "Synced with workspace"}
            </span>
            <small>{state.opening ? `Opening ${state.opening}…` : "Ctrl / ⌘ + S to save"}</small>
          </div>
          <WorkspaceError text={state.readError} />
          {workspaceEditable(edit.file) ? (
            <WorkspaceText key={edit.identity} edit={edit} draft={(value) => journal.draft(value)} />
          ) : (
            <div className="astra-files-unavailable">
              <FileCode2 size={34} />
              <h3>{edit.file.too_large ? "Artifact exceeds the editor limit" : "Binary artifact detected"}</h3>
              <p>Download the original file to inspect it with a compatible local tool.</p>
              <button type="button" onClick={() => void journal.download(edit.file.path)}>
                <Download size={14} />
                Download artifact
              </button>
            </div>
          )}
          <footer>
            <button type="button" onClick={() => void journal.download(edit.file.path)}>
              <Download size={14} />
              Download
            </button>
            <button
              type="button"
              className="is-primary"
              disabled={!dirty || writing || !workspaceEditable(edit.file)}
              onClick={() => void journal.save()}
            >
              <Save size={14} />
              {state.writing === "Save" ? "Saving…" : "Commit changes"}
            </button>
          </footer>
        </>
      ) : (
        <div className="astra-files-unavailable">
          <HardDrive size={38} />
          <h2>{state.opening ? "Opening file…" : "Select a file to inspect"}</h2>
          <p>Keep the directory and your artifact in view while you work.</p>
          <WorkspaceError text={state.readError} />
        </div>
      )}
    </aside>
  );
}
function WorkspaceEntryRow({
  entry,
  active,
  busy,
  open,
  journal,
}: {
  entry: WorkspaceEntry;
  active: boolean;
  busy: boolean;
  open: () => void;
  journal: WorkspaceView["journal"];
}) {
  return (
    <article data-workspace-entry={entry.path} data-active={active} className="astra-files-entry">
      <button
        type="button"
        data-workspace-open
        onClick={open}
        onKeyDown={navigateWorkspaceFiles}
        aria-current={active ? "true" : undefined}
      >
        <span className="astra-files-fileicon">{entry.dir ? <Folder size={20} /> : <FileText size={20} />}</span>
        <span>
          <strong>{entry.name}</strong>
          <small>{entry.dir ? "Evidence directory" : workspaceSize(entry.size)}</small>
        </span>
        <time>{workspaceTime(entry.mtime)}</time>
      </button>
      <div>
        {!entry.dir && (
          <button
            type="button"
            aria-label={`Download ${entry.name}`}
            title="Download"
            onClick={() => void journal.download(entry.path)}
          >
            <Download size={14} />
          </button>
        )}
        <button
          type="button"
          disabled={busy}
          aria-label={`Delete ${entry.name}`}
          title="Delete"
          onClick={() => journal.confirmDelete(entry)}
        >
          <Trash2 size={14} />
        </button>
      </div>
    </article>
  );
}
export function WorkspaceView() {
  const view = useWorkspace(),
    { journal, state, dirty } = view;
  const upload = useRef<HTMLInputElement>(null),
    search = useRef<HTMLInputElement>(null);
  const [contextOpen, setContextOpen] = useState(false);
  const toggleContext = useCallback(
    () =>
      setContextOpen((open) => {
        localStorage.setItem("uroboros-module-rail", open ? "closed" : "open");
        return !open;
      }),
    [],
  );
  const visible = workspaceVisible(state.entries, state.filter),
    facts = workspaceFacts(state.entries),
    busy = Boolean(state.writing);
  const choose = (entry: WorkspaceEntry) => {
    if (entry.dir) {
      void journal.list(entry.path);
      return;
    }
    if (
      dirty &&
      state.edit?.file.path !== entry.path &&
      !window.confirm("Discard unsaved changes and open another file?")
    )
      return;
    if (
      dirty &&
      state.edit?.file.path === entry.path &&
      !window.confirm("Reload this file and discard unsaved changes?")
    )
      return;
    void journal.open(entry.path);
  };
  useEffect(() => {
    setContextOpen(localStorage.getItem("uroboros-module-rail") === "open");
  }, []);
  useEffect(() => {
    const key = (event: KeyboardEvent) => {
      if (
        state.folder ||
        state.deletion ||
        event.ctrlKey ||
        event.metaKey ||
        event.altKey ||
        (event.target instanceof HTMLElement &&
          (event.target.matches("input,textarea,select") || event.target.isContentEditable))
      )
        return;
      if (event.key === "/") {
        event.preventDefault();
        search.current?.focus();
      }
      if (event.key === "]") {
        event.preventDefault();
        toggleContext();
      }
    };
    window.addEventListener("keydown", key);
    return () => window.removeEventListener("keydown", key);
  }, [state.folder, state.deletion, toggleContext]);
  return (
    <main className="astra-files" data-workspace-view>
      <header className="astra-files-heading">
        <span>
          <HardDrive size={27} />
        </span>
        <div>
          <small>Uroboros / operator files</small>
          <h1>Evidence workspace</h1>
          <p>Browse, inspect and edit the artifacts produced during your work.</p>
        </div>
        <strong>
          {facts.files} files <span>in this directory</span>
        </strong>
      </header>
      <div className="astra-files-toolbar">
        <nav aria-label="Workspace path">
          {workspaceCrumbs(state.path).map((crumb, index) => (
            <span key={crumb.path}>
              {index > 0 && <span aria-hidden="true">/</span>}
              <button
                type="button"
                aria-current={index === workspaceCrumbs(state.path).length - 1 ? "page" : undefined}
                title={crumb.path || "Workspace root"}
                onClick={() => void journal.list(crumb.path)}
              >
                {crumb.name}
              </button>
            </span>
          ))}
        </nav>
        <div>
          <button type="button" disabled={busy} onClick={() => journal.showFolder()}>
            <FolderPlus size={14} />
            New folder
          </button>
          <button type="button" disabled={busy} onClick={() => upload.current?.click()}>
            <Upload size={14} />
            Upload
          </button>
          <button type="button" title="Refresh" aria-label="Refresh directory" onClick={() => void journal.list()}>
            <RefreshCw size={14} className={state.listing ? "is-spinning" : undefined} />
          </button>
        </div>
        <input
          ref={upload}
          type="file"
          multiple
          hidden
          onChange={async (event) => {
            const element = event.currentTarget,
              files = Array.from(element.files ?? []);
            try {
              await journal.upload(files);
            } finally {
              element.value = "";
            }
          }}
        />
      </div>
      <div className="astra-files-contextbar">
        <button type="button" aria-expanded={contextOpen} aria-controls="files-context" onClick={toggleContext}>
          Workspace context <kbd>]</kbd>
        </button>
        <nav aria-label="Related evidence views">
          {context?.related.map((link) => (
            <Link
              key={link.href}
              href={link.href}
              onClick={(event) => {
                if (
                  dirty &&
                  !event.ctrlKey &&
                  !event.metaKey &&
                  !window.confirm("Leave this editor with unsaved changes?")
                )
                  event.preventDefault();
              }}
            >
              {link.label}
            </Link>
          ))}
        </nav>
      </div>
      {contextOpen && context && (
        <section id="files-context" className="astra-files-context" aria-label="Workspace context">
          <p>{context.directive}</p>
          <ol>
            {context.workflow.map((step) => (
              <li key={step.label}>
                <strong>{step.label}</strong>
                {step.detail}
              </li>
            ))}
          </ol>
        </section>
      )}
      <WorkspaceError text={state.error} />
      {state.notice && (
        <p role="status" className="astra-files-notice">
          {state.notice}
        </p>
      )}
      <div className="astra-files-board">
        <section className="astra-files-browser" aria-label="Workspace files" aria-busy={state.listing}>
          <header>
            <div>
              <FolderOpen size={17} />
              <span>
                <small>Current evidence set</small>
                <strong>{state.path || "Workspace root"}</strong>
              </span>
            </div>
            <dl>
              <div>
                <dt>Folders</dt>
                <dd>{facts.folders}</dd>
              </div>
              <div>
                <dt>Files</dt>
                <dd>{facts.files}</dd>
              </div>
              <div>
                <dt>Visible size</dt>
                <dd>{workspaceSize(facts.bytes)}</dd>
              </div>
            </dl>
          </header>
          <label className="astra-files-search">
            <Search size={14} />
            <input
              ref={search}
              value={state.filter}
              onChange={(event) => journal.filter(event.target.value)}
              aria-label="Filter workspace files"
              placeholder="Filter files in this directory…"
            />
            <kbd>/</kbd>
          </label>
          <WorkspaceError text={state.listError} retry={() => void journal.list()} />
          <div className="astra-files-entries">
            {visible.map((entry) => (
              <WorkspaceEntryRow
                key={entry.path}
                entry={entry}
                active={state.edit?.file.path === entry.path}
                busy={busy}
                open={() => choose(entry)}
                journal={journal}
              />
            ))}
            {!visible.length && (
              <div className="astra-files-unavailable">
                <FolderOpen size={30} />
                <h3>
                  {state.listing ? "Reading workspace" : state.filter ? "No matching files" : "This directory is empty"}
                </h3>
                <p>
                  {state.filter
                    ? "Try another filename or clear the filter."
                    : "Upload an artifact or create a folder to begin."}
                </p>
              </div>
            )}
          </div>
          <footer>
            <span>
              {visible.length} of {state.entries.length} entries
            </span>
            <small>Files retain their workspace paths</small>
          </footer>
        </section>
        <WorkspaceInspector {...view} />
      </div>
      {state.folder && (
        <WorkspaceModal title="New folder" busy={busy} close={() => journal.closeFolder()}>
          <form
            onSubmit={(event) => {
              event.preventDefault();
              void journal.createFolder();
            }}
          >
            <p>
              Create inside <code>{state.folder.parent || "Workspace root"}</code>.
            </p>
            <label>
              Folder name
              <input
                data-initial-focus
                placeholder="Folder name"
                value={state.folder.name}
                onChange={(event) => journal.folderName(event.target.value)}
                disabled={busy}
              />
            </label>
            <WorkspaceError text={state.folder.error} />
            <footer>
              <button type="button" disabled={busy} onClick={() => journal.closeFolder()}>
                Cancel
              </button>
              <button type="submit" disabled={busy || !state.folder.name.trim()} className="is-primary">
                {busy ? "Creating…" : "Create"}
              </button>
            </footer>
          </form>
        </WorkspaceModal>
      )}
      {state.deletion && (
        <WorkspaceModal title="Delete workspace content?" busy={busy} close={() => journal.closeDelete()}>
          <p>
            Delete <strong>{state.deletion.target.path}</strong>
            {state.deletion.target.dir ? " and all its contents" : ""}? This removes the file content from the shared
            workspace.
          </p>
          {state.edit && workspaceDirty(state.edit) && (
            <p>Save any local draft you want to keep before deleting its file.</p>
          )}
          <WorkspaceError text={state.deletion.error} />
          <footer>
            <button type="button" data-initial-focus disabled={busy} onClick={() => journal.closeDelete()}>
              Cancel
            </button>
            <button
              type="button"
              data-workspace-confirm
              disabled={busy}
              className="is-destructive"
              onClick={() => void journal.deleteConfirmed()}
            >
              {busy ? "Deleting…" : "Delete content"}
            </button>
          </footer>
        </WorkspaceModal>
      )}
    </main>
  );
}
