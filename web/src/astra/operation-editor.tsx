"use client";

import { useEffect, useRef, useState, type FormEvent } from "react";
import type { AssetInterceptRuleInput, Task, TaskCategory, TaskTemplate } from "@/lib/types";
import { collection, type DataRow } from "./client";
import { consoleRequest, consoleUpload } from "./console-api";
import { EditorDialog } from "./editor-dialog";
import {
  applyOperationTemplate,
  emptyOperation,
  operationDocument,
  operationTitle,
  templateDocument,
  uploadManifest,
  type OperationDraft,
} from "./operations-domain";

export function OrderedChoices({
  title,
  choices,
  value,
  change,
  disabled,
  limit = Infinity,
}: {
  title: string;
  choices: { id: string; label: string }[];
  value: string[];
  change: (ids: string[]) => void;
  disabled?: boolean;
  limit?: number;
}) {
  const [next, choose] = useState("");
  function shift(index: number, offset: number) {
    const updated = [...value];
    [updated[index], updated[index + offset]] = [updated[index + offset], updated[index]];
    change(updated);
  }
  return (
    <fieldset disabled={disabled} className="astra-operation-choices">
      <legend>{title}</legend>
      <ol>
        {value.map((id, index) => (
          <li key={id}>
            <span>{choices.find((choice) => choice.id === id)?.label || `Unavailable record #${id}`}</span>
            <button
              type="button"
              aria-label={`Move ${title} ${index + 1} up`}
              disabled={index === 0}
              onClick={() => shift(index, -1)}
            >
              ↑
            </button>
            <button
              type="button"
              aria-label={`Move ${title} ${index + 1} down`}
              disabled={index === value.length - 1}
              onClick={() => shift(index, 1)}
            >
              ↓
            </button>
            <button
              type="button"
              aria-label={`Remove ${title} ${index + 1}`}
              onClick={() => change(value.filter((selected) => selected !== id))}
            >
              Remove
            </button>
          </li>
        ))}
      </ol>
      <div className="astra-operation-toolbar">
        <select aria-label={`Add ${title}`} value={next} onChange={(event) => choose(event.target.value)}>
          <option value="">Choose a record</option>
          {choices
            .filter((choice) => !value.includes(choice.id))
            .map((choice) => (
              <option key={choice.id} value={choice.id}>
                {choice.label}
              </option>
            ))}
        </select>
        <button
          type="button"
          disabled={!next || value.includes(next) || value.length >= limit}
          onClick={() => {
            change([...value, next]);
            choose("");
          }}
        >
          Add {title.toLowerCase()}
        </button>
      </div>
    </fieldset>
  );
}

const interceptKinds = {
  exact_domain: "Exact domain",
  exact_ip: "Exact IP",
  exact_url: "Exact URL",
  fuzzy_domain: "Domain contains",
  fuzzy_ip: "IP contains",
  fuzzy_url: "URL contains",
  cidr: "CIDR range",
};
export function ScopeRules({
  value,
  change,
  disabled,
}: {
  value: AssetInterceptRuleInput[];
  change: (rules: AssetInterceptRuleInput[]) => void;
  disabled?: boolean;
}) {
  function patch(index: number, fields: Partial<AssetInterceptRuleInput>) {
    change(value.map((rule, at) => (at === index ? { ...rule, ...fields } : rule)));
  }
  return (
    <fieldset disabled={disabled} className="astra-operation-rules">
      <legend>Asset allow and block rules</legend>
      <p>Rules travel with this operation. Their order and enabled state are preserved.</p>
      {value.map((rule, index) => (
        <div key={index} className="astra-operation-rule">
          <label>
            Action
            <select
              aria-label={`Rule ${index + 1} action`}
              value={rule.action}
              onChange={(event) => patch(index, { action: event.target.value as "block" | "allow" })}
            >
              <option value="block">Block</option>
              <option value="allow">Allow</option>
            </select>
          </label>
          <label>
            Match
            <select
              aria-label={`Rule ${index + 1} match`}
              value={rule.kind}
              onChange={(event) => patch(index, { kind: event.target.value as AssetInterceptRuleInput["kind"] })}
            >
              {Object.entries(interceptKinds).map(([kind, label]) => (
                <option key={kind} value={kind}>
                  {label}
                </option>
              ))}
            </select>
          </label>
          <label>
            Pattern
            <input
              aria-label={`Rule ${index + 1} pattern`}
              value={rule.pattern}
              onChange={(event) => patch(index, { pattern: event.target.value })}
            />
          </label>
          <label>
            Note
            <input
              aria-label={`Rule ${index + 1} note`}
              value={rule.note}
              onChange={(event) => patch(index, { note: event.target.value })}
            />
          </label>
          <label className="astra-operation-check">
            <input
              type="checkbox"
              checked={rule.enabled}
              onChange={(event) => patch(index, { enabled: event.target.checked })}
            />
            Enabled
          </label>
          <button
            type="button"
            aria-label={`Remove rule ${index + 1}`}
            onClick={() => change(value.filter((_, at) => at !== index))}
          >
            Remove
          </button>
        </div>
      ))}
      <button
        type="button"
        onClick={() =>
          change([...value, { action: "block", kind: "fuzzy_domain", pattern: "", note: "", enabled: true }])
        }
      >
        Add scope rule
      </button>
    </fieldset>
  );
}

export function OperationEditor({
  tasks,
  categories,
  close,
  created,
}: {
  tasks: Task[];
  categories: TaskCategory[];
  close: () => void;
  created: (id: string) => void;
}) {
  const [draft, setDraft] = useState(emptyOperation),
    [profiles, setProfiles] = useState<DataRow[]>([]),
    [companies, setCompanies] = useState<DataRow[]>([]),
    [templates, setTemplates] = useState<TaskTemplate[]>([]);
  const [template, selectTemplate] = useState(""),
    [templateName, nameTemplate] = useState(""),
    [confirmDelete, deleteTemplate] = useState(false);
  const [busy, pending] = useState(false),
    [loading, setLoading] = useState(true),
    [error, fail] = useState<string>(),
    [metadataError, failMetadata] = useState<string>();
  const uploadID = useRef<string>("");
  useEffect(() => {
    let active = true;
    Promise.all([
      consoleRequest<unknown>("/llm/profiles"),
      consoleRequest<unknown>("/companies"),
      consoleRequest<{ templates: TaskTemplate[] }>("/task-templates"),
    ])
      .then(([models, organizations, saved]) => {
        if (!active) return;
        setProfiles(collection(models, ["profiles"]));
        setCompanies(collection(organizations, ["companies"]));
        setTemplates(saved.templates || []);
      })
      .catch((error) => {
        if (active) failMetadata(String(error.message || error));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, []);
  function patch(fields: Partial<OperationDraft>) {
    setDraft((previous) => ({ ...previous, ...fields }));
  }
  async function action(work: () => Promise<void>) {
    pending(true);
    fail(undefined);
    try {
      await work();
    } catch (error) {
      fail(error instanceof Error ? error.message : String(error));
    } finally {
      pending(false);
    }
  }
  async function submit(event: FormEvent) {
    event.preventDefault();
    await action(async () => {
      const task = await consoleRequest<Task>("/tasks", "POST", operationDocument(draft));
      if (!task.id) throw new Error("The server did not return an operation ID.");
      created(String(task.id));
    });
  }
  async function storeTemplate(update: boolean) {
    await action(async () => {
      const document = templateDocument(draft, templateName),
        saved = await consoleRequest<TaskTemplate>(
          update ? `/task-templates/${template}` : "/task-templates",
          update ? "PATCH" : "POST",
          document,
        );
      setTemplates((current) => [...current.filter((item) => item.id !== saved.id), saved]);
      selectTemplate(String(saved.id));
      nameTemplate(saved.name);
    });
  }
  return (
    <EditorDialog title="New operation" eyebrow="Operation" busy={busy} close={close}>
      <form onSubmit={(event) => void submit(event)} className="astra-operation-form">
        {error && (
          <p role="alert" className="astra-form-error">
            {error}
          </p>
        )}
        {metadataError && (
          <p role="alert" className="astra-form-error">
            Some creation options could not be loaded: {metadataError}. Close and reopen to retry.
          </p>
        )}
        <fieldset disabled={busy || loading}>
          <legend>Assignment</legend>
          <label>
            Name
            <input name="name" value={draft.name} onChange={(event) => patch({ name: event.target.value })} />
          </label>
          <label>
            Category
            <select
              name="category"
              value={draft.category}
              onChange={(event) => patch({ category: event.target.value })}
            >
              <option value="">Uncategorized</option>
              {categories.map((category) => (
                <option key={category.id} value={category.id}>
                  {category.name}
                </option>
              ))}
            </select>
          </label>
          <label>
            Assignment
            <textarea
              name="description"
              rows={5}
              required
              value={draft.description}
              onChange={(event) => patch({ description: event.target.value })}
            />
          </label>
          <label>
            Goal
            <textarea
              name="goal"
              rows={4}
              required
              value={draft.goal}
              onChange={(event) => patch({ goal: event.target.value })}
            />
          </label>
          <label>
            Attach files
            <input
              type="file"
              multiple
              disabled={busy}
              onChange={(event) => {
                const input = event.currentTarget,
                  files = Array.from(input.files || []);
                if (!files.length) return;
                void action(async () => {
                  try {
                    uploadID.current ||= crypto.randomUUID();
                    const uploaded = await consoleUpload(uploadID.current, files);
                    setDraft((current) => ({
                      ...current,
                      description: uploadManifest(current.description, uploaded.attachments),
                    }));
                  } finally {
                    input.value = "";
                  }
                });
              }}
            />
          </label>
        </fieldset>
        <details className="astra-startup-menu">
          <summary>
            <span>Automatic startup</span>
            <small>Katana {draft.initialKatana ? "on" : "off"} · Scout {draft.initialScout}</small>
          </summary>
          <fieldset disabled={busy || loading}>
            <legend>Initial discovery</legend>
            <label className="astra-operation-check">
              <input
                name="initial_katana"
                type="checkbox"
                checked={draft.initialKatana}
                onChange={(event) => patch({ initialKatana: event.target.checked })}
                aria-describedby="initial-katana-help"
              />
              Initial Katana crawl
            </label>
            <p id="initial-katana-help">
              Crawl eligible web targets in this operation’s scope at startup. Turn off to leave discovery to the agent.
            </p>
            <label>
              Initial reconnaissance scout
              <select
                name="initial_scout"
                value={draft.initialScout}
                onChange={(event) => patch({ initialScout: event.target.value as OperationDraft["initialScout"] })}
                aria-describedby="initial-scout-help"
              >
                <option value="default">Use workspace default</option>
                <option value="on">On for this operation</option>
                <option value="off">Off for this operation</option>
              </select>
            </label>
            <p id="initial-scout-help">One bounded reconnaissance intent after goal decomposition.</p>
            <p className="astra-startup-note">
              These choices only control startup. Agents can still use the tools later, subject to scope and your rules.
            </p>
          </fieldset>
        </details>
        <details>
          <summary>Templates</summary>
          <fieldset disabled={busy || loading}>
            <legend>Saved assignment presets</legend>
            <p>
              Applying a template changes the assignment, goal, category and scope rules. Runtime settings and selected
              sources stay as entered.
            </p>
            <label>
              Template
              <select
                aria-label="Template"
                value={template}
                onChange={(event) => {
                  const id = event.target.value;
                  selectTemplate(id);
                  nameTemplate(templates.find((item) => String(item.id) === id)?.name || "");
                  deleteTemplate(false);
                }}
              >
                <option value="">Select a template</option>
                {templates.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              disabled={!template}
              onClick={() => {
                const saved = templates.find((item) => String(item.id) === template);
                if (saved) setDraft((current) => applyOperationTemplate(current, saved));
              }}
            >
              Apply template
            </button>
            <label>
              Template name
              <input name="template_name" value={templateName} onChange={(event) => nameTemplate(event.target.value)} />
            </label>
            <div className="astra-operation-toolbar">
              <button type="button" disabled={!templateName.trim()} onClick={() => void storeTemplate(false)}>
                Save new template
              </button>
              <button
                type="button"
                disabled={!template || !templateName.trim()}
                onClick={() => void storeTemplate(true)}
              >
                Update selected template
              </button>
              <button type="button" disabled={!template} onClick={() => deleteTemplate(true)}>
                Delete template
              </button>
            </div>
            {confirmDelete && (
              <div role="alert">
                Delete this saved template?
                <button
                  type="button"
                  onClick={() =>
                    void action(async () => {
                      await consoleRequest(`/task-templates/${template}`, "DELETE");
                      setTemplates((current) => current.filter((item) => String(item.id) !== template));
                      selectTemplate("");
                      nameTemplate("");
                      deleteTemplate(false);
                    })
                  }
                >
                  Confirm template deletion
                </button>
                <button type="button" onClick={() => deleteTemplate(false)}>
                  Keep template
                </button>
              </div>
            )}
          </fieldset>
        </details>
        <OrderedChoices
          title="Model profiles"
          choices={profiles.map((profile) => ({
            id: String(profile.id),
            label: `${profile.name} · ${profile.model}${profile.is_default ? " · workspace default" : ""}`,
          }))}
          value={draft.profiles}
          change={(profiles) => patch({ profiles })}
          disabled={busy || loading}
        />
        <p>The ordered model chain is used as entered. An empty chain uses the workspace default.</p>
        <details>
          <summary>Sources, runtime and scope</summary>
          <fieldset disabled={busy}>
            <legend>Runtime</legend>
            <label>
              Timeout (minutes; 0 is unlimited)
              <input
                name="timeout"
                type="number"
                min="0"
                step="1"
                value={draft.timeout}
                onChange={(event) => patch({ timeout: event.target.value })}
              />
            </label>
            <label>
              Planner heartbeat (minutes; minimum 10)
              <input
                name="heartbeat"
                type="number"
                min="0"
                step="1"
                value={draft.heartbeat}
                onChange={(event) => patch({ heartbeat: event.target.value })}
              />
            </label>
            <label className="astra-operation-check">
              <input
                name="seed"
                type="checkbox"
                checked={draft.seed}
                onChange={(event) => patch({ seed: event.target.checked })}
              />
              Seed the first intent
            </label>
            <label className="astra-operation-check">
              <input
                name="coverage"
                type="checkbox"
                checked={draft.coverage}
                onChange={(event) => patch({ coverage: event.target.checked })}
              />
              Enable asset coverage
            </label>
            <label className="astra-operation-check">
              <input
                name="isolated"
                type="checkbox"
                checked={draft.isolated}
                onChange={(event) => patch({ isolated: event.target.checked })}
              />
              Strict isolation
            </label>
            {draft.isolated && (
              <p>Isolation prevents inheritance. Remove selected source operations before creating this operation.</p>
            )}
          </fieldset>
          <OrderedChoices
            title="Source operations"
            choices={tasks.map((task) => ({ id: String(task.id), label: `#${task.id} ${operationTitle(task)}` }))}
            value={draft.sources}
            change={(sources) => patch({ sources })}
            disabled={busy || loading}
            limit={8}
          />
          <OrderedChoices
            title="Companies"
            choices={companies.map((company) => ({ id: String(company.id), label: String(company.name) }))}
            value={draft.companies}
            change={(companies) => patch({ companies })}
            disabled={busy || loading}
          />
          <ScopeRules value={draft.rules} change={(rules) => patch({ rules })} disabled={busy} />
        </details>
        <div className="astra-form-actions">
          <button type="submit" disabled={busy || loading || Boolean(metadataError)}>
            {busy ? "Working…" : "Create operation"}
          </button>
          <button type="button" disabled={busy} onClick={close}>
            Cancel
          </button>
        </div>
      </form>
    </EditorDialog>
  );
}
