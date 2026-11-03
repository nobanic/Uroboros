import type { WorkspaceEntry, WorkspaceFile, WorkspaceListing } from "@/lib/types";

import { workspaceContains, workspaceEditable } from "./workspace-document";

export type WorkspacePorts = {
  list: (path: string) => Promise<WorkspaceListing>;
  read: (path: string) => Promise<WorkspaceFile>;
  write: (path: string, content: string) => Promise<unknown>;
  mkdir: (path: string) => Promise<unknown>;
  remove: (path: string) => Promise<unknown>;
  upload: (path: string, files: File[]) => Promise<{ uploaded: number }>;
  download: (path: string) => Promise<unknown>;
};
export type WorkspaceEdit = { file: WorkspaceFile; content: string; saved: string; identity: number };
type WorkspaceState = {
  path: string;
  entries: WorkspaceEntry[];
  filter: string;
  listing: boolean;
  opening: string;
  listError: string;
  readError: string;
  edit: WorkspaceEdit | null;
  writing: string;
  error: string;
  notice: string;
  folder: { parent: string; name: string; error: string } | null;
  deletion: { target: WorkspaceEntry; error: string } | null;
};
const message = (error: unknown) => (error instanceof Error ? error.message : String(error));
export const workspaceDirty = (edit: WorkspaceEdit | null) => Boolean(edit && edit.content !== edit.saved);

/** One editor session with independent directory/body leases and acknowledged mutations. */
export class WorkspaceJournal {
  private state: WorkspaceState = {
    path: "",
    entries: [],
    filter: "",
    listing: true,
    opening: "",
    listError: "",
    readError: "",
    edit: null,
    writing: "",
    error: "",
    notice: "",
    folder: null,
    deletion: null,
  };
  private active = false;
  private epoch = 0;
  private directoryRead = 0;
  private directoryTarget = "";
  private bodyRead = 0;
  private editSequence = 0;
  private listeners = new Set<() => void>();
  constructor(private ports: WorkspacePorts) {}
  snapshot = () => this.state;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private publish(patch: Partial<WorkspaceState>) {
    this.state = { ...this.state, ...patch };
    for (const listener of this.listeners) listener();
  }
  activate() {
    this.active = true;
    this.epoch++;
  }
  dispose() {
    this.active = false;
    this.epoch++;
  }
  private lease() {
    const epoch = this.epoch;
    return () => this.active && epoch === this.epoch;
  }
  filter(value: string) {
    this.publish({ filter: value });
  }
  draft(value: string) {
    if (this.state.edit) this.publish({ edit: { ...this.state.edit, content: value } });
  }
  async list(path = this.state.path) {
    this.directoryTarget = path;
    const generation = ++this.directoryRead,
      alive = this.lease();
    const owns = () => alive() && generation === this.directoryRead;
    this.publish({ listing: true, listError: "" });
    try {
      const result = await this.ports.list(path);
      if (!owns()) return;
      this.publish({
        path: result.path,
        entries: result.entries,
        filter: path === this.state.path ? this.state.filter : "",
      });
      if (!this.state.edit && !this.state.opening) {
        const first = result.entries.find((entry) => !entry.dir);
        if (first) void this.open(first.path);
      }
    } catch (error) {
      if (owns()) this.publish({ listError: `Failed to read directory: ${message(error)}` });
    } finally {
      if (owns()) this.publish({ listing: false });
    }
  }
  async open(path: string) {
    const generation = ++this.bodyRead,
      alive = this.lease();
    const owns = () => alive() && generation === this.bodyRead;
    const previous = this.state.edit;
    this.publish({ opening: path, readError: "" });
    try {
      const file = await this.ports.read(path);
      if (owns()) {
        if (this.state.edit?.identity === previous?.identity && this.state.edit?.content !== previous?.content) {
          this.publish({
            readError:
              "The current draft changed while opening a file. Open it again after saving or discarding changes.",
          });
        } else
          this.publish({
            edit: { file, content: file.content ?? "", saved: file.content ?? "", identity: ++this.editSequence },
          });
      }
    } catch (error) {
      if (owns()) this.publish({ readError: `Failed to open file: ${message(error)}` });
    } finally {
      if (owns()) this.publish({ opening: "" });
    }
  }
  private async mutate(
    label: string,
    action: () => Promise<unknown>,
    accept: () => void,
    reject?: (text: string) => void,
  ) {
    if (this.state.writing || !this.active) return false;
    const alive = this.lease();
    this.publish({ writing: label, error: "", notice: "" });
    try {
      await action();
      if (!alive()) return false;
      accept();
      void this.list(this.directoryTarget);
      return true;
    } catch (error) {
      if (alive()) {
        const text = `${label} failed: ${message(error)}`;
        this.publish({ error: text });
        reject?.(text);
      }
      return false;
    } finally {
      if (alive()) this.publish({ writing: "" });
    }
  }
  save() {
    const submitted = this.state.edit;
    if (!submitted || !workspaceDirty(submitted) || !workspaceEditable(submitted.file)) return Promise.resolve(false);
    return this.mutate(
      "Save",
      () => this.ports.write(submitted.file.path, submitted.content),
      () => {
        const current = this.state.edit;
        this.publish({
          notice: "Saved",
          ...(current?.identity === submitted.identity ? { edit: { ...current, saved: submitted.content } } : {}),
        });
      },
    );
  }
  showFolder() {
    this.publish({ folder: { parent: this.state.path, name: "", error: "" } });
  }
  folderName(name: string) {
    if (this.state.folder) this.publish({ folder: { ...this.state.folder, name } });
  }
  closeFolder() {
    if (!this.state.writing) this.publish({ folder: null });
  }
  createFolder() {
    const folder = this.state.folder,
      name = folder?.name.trim();
    if (!folder || !name) return Promise.resolve(false);
    const path = folder.parent ? `${folder.parent}/${name}` : name;
    return this.mutate(
      "Creation",
      () => this.ports.mkdir(path),
      () => this.publish({ folder: null, notice: "Directory created" }),
      (error) => this.publish({ folder: { ...folder, error } }),
    );
  }
  confirmDelete(target: WorkspaceEntry) {
    this.publish({ deletion: { target, error: "" } });
  }
  closeDelete() {
    if (!this.state.writing) this.publish({ deletion: null });
  }
  deleteConfirmed() {
    const deletion = this.state.deletion;
    if (!deletion) return Promise.resolve(false);
    return this.mutate(
      "Delete",
      () => this.ports.remove(deletion.target.path),
      () => {
        const removedOpening = this.state.opening && workspaceContains(deletion.target.path, this.state.opening);
        if (removedOpening) this.bodyRead++;
        const edit = this.state.edit;
        this.publish({
          deletion: null,
          ...(removedOpening ? { opening: "" } : {}),
          notice: "Deleted",
          edit: edit && workspaceContains(deletion.target.path, edit.file.path) ? null : edit,
        });
      },
      (error) => this.publish({ deletion: { ...deletion, error } }),
    );
  }
  async upload(files: File[]) {
    if (!files.length) return false;
    const path = this.state.path;
    let uploaded = 0;
    return this.mutate(
      "Upload",
      async () => {
        uploaded = (await this.ports.upload(path, files)).uploaded;
      },
      () => this.publish({ notice: `Uploaded ${uploaded} files` }),
      () => this.publish({ error: `${this.state.error}. Refresh to inspect files already accepted.` }),
    );
  }
  async download(path: string) {
    const alive = this.lease();
    try {
      await this.ports.download(path);
    } catch (error) {
      if (alive()) this.publish({ error: `Download failed: ${message(error)}` });
    }
  }
}
