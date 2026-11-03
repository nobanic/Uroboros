"use client";
import { useState, type ReactNode } from "react";
import { UroIcon } from "@/components/uroboros-icons";
import type { useOverviewResource } from "./use-overview-data";

export type OverviewField = {
  key: string;
  label: string;
  required?: boolean;
  options?: readonly (readonly [string, string])[];
  placeholder?: string;
};
export type OverviewValues = Record<string, string>;
type Resource<T> = ReturnType<typeof useOverviewResource<T[]>>;

export function OverviewNotice({ error, retry }: { error: string; retry: () => void }) {
  return error ? (
    <div className="astra-overview-notice" role="alert">
      <span>{error}</span>
      <button type="button" onClick={retry}>
        Refresh data
      </button>
    </div>
  ) : null;
}

function PolicyForm({
  title,
  fields,
  initial,
  busy,
  submit,
  cancel,
}: {
  title: string;
  fields: OverviewField[];
  initial: OverviewValues;
  busy: boolean;
  submit: (values: OverviewValues) => Promise<void>;
  cancel: () => void;
}) {
  const [values, change] = useState(initial);
  const valid = fields.every((field) => !field.required || values[field.key]?.trim());
  return (
    <form
      className="astra-overview-policy-form"
      aria-label={title}
      onSubmit={(event) => {
        event.preventDefault();
        if (valid && !busy) void submit(values);
      }}
      onKeyDown={(event) => {
        if (event.key === "Escape" && !busy) {
          event.preventDefault();
          cancel();
        }
      }}
    >
      <header>
        <strong>{title}</strong>
        <small>Enter to save · Escape to cancel</small>
      </header>
      {fields.map((field, index) => (
        <label key={field.key}>
          {field.label}
          {field.options ? (
            <select
              aria-label={field.label}
              value={values[field.key] || ""}
              disabled={busy}
              onChange={(event) => change((previous) => ({ ...previous, [field.key]: event.target.value }))}
            >
              {field.options.map(([value, label]) => (
                <option key={value} value={value}>
                  {label}
                </option>
              ))}
            </select>
          ) : (
            <input
              aria-label={field.label}
              value={values[field.key] || ""}
              required={field.required}
              placeholder={field.placeholder}
              autoFocus={index === fields.findIndex((item) => !item.options)}
              disabled={busy}
              onChange={(event) => change((previous) => ({ ...previous, [field.key]: event.target.value }))}
            />
          )}
        </label>
      ))}
      <footer>
        <button type="button" disabled={busy} onClick={cancel}>
          Cancel
        </button>
        <button type="submit" className="astra-overview-primary" disabled={busy || !valid}>
          {busy ? "Saving…" : "Save"}
        </button>
      </footer>
    </form>
  );
}

export function OverviewPolicy<T extends { id: string | number }>({
  title,
  singular,
  description,
  resource,
  fields,
  defaults,
  values,
  save,
  remove,
  render,
  removable = () => true,
  toggle,
}: {
  title: string;
  singular: string;
  description: string;
  resource: Resource<T>;
  fields: OverviewField[];
  defaults: OverviewValues;
  values: (row: T) => OverviewValues;
  save: (values: OverviewValues, row?: T) => Promise<unknown>;
  remove: (row: T) => Promise<unknown>;
  render: (row: T) => ReactNode;
  removable?: (row: T) => boolean;
  toggle?: { checked: (row: T) => boolean; write: (row: T) => Promise<unknown>; accepted: (row: T) => T };
}) {
  const [editor, edit] = useState<T | null>(),
    [search, find] = useState(""),
    [limit, show] = useState(20);
  const all = resource.data || [],
    needle = search.trim().toLocaleLowerCase();
  const rows = needle
    ? all.filter((row) => Object.values(values(row)).some((value) => value.toLocaleLowerCase().includes(needle)))
    : all;
  const close = () => edit(undefined);
  return (
    <section className="astra-overview-panel astra-overview-policy" aria-label={title}>
      <header>
        <div>
          <small>Task policy</small>
          <h2>
            {title} <span>{all.length}</span>
          </h2>
        </div>
        <button type="button" disabled={resource.busy} onClick={() => edit(null)}>
          <UroIcon name="plus" /> Add {singular}
        </button>
      </header>
      <p className="astra-overview-description">{description}</p>
      <OverviewNotice error={resource.error} retry={() => void resource.reload(true)} />
      {editor !== undefined && (
        <PolicyForm
          key={editor?.id ?? "new"}
          title={editor ? `Edit ${singular}` : `New ${singular}`}
          fields={fields}
          initial={editor ? values(editor) : defaults}
          busy={resource.busy}
          cancel={close}
          submit={async (input) => {
            const current = editor ? all.find((row) => row.id === editor.id) || editor : undefined;
            if (await resource.run(() => save(input, current))) close();
          }}
        />
      )}
      {all.length > 8 && (
        <label className="astra-overview-search">
          <UroIcon name="search" />
          <input
            aria-label={`Search ${title.toLowerCase()}`}
            placeholder={`Search ${title.toLowerCase()}…`}
            value={search}
            onChange={(event) => {
              find(event.target.value);
              show(20);
            }}
          />
        </label>
      )}
      <div className="astra-overview-policy-list">
        {rows.slice(0, limit).map((row) => (
          <article key={row.id} data-overview-policy={row.id}>
            <div className="astra-overview-policy-content">{render(row)}</div>
            <div className="astra-overview-row-actions">
              {toggle && (
                <label className="astra-overview-switch">
                  <input
                    type="checkbox"
                    aria-label={`Enable ${singular} ${row.id}`}
                    checked={toggle.checked(row)}
                    disabled={resource.busy || editor !== undefined}
                    onChange={() =>
                      void resource.run(
                        () => toggle.write(row),
                        (previous) => previous.map((item) => (item.id === row.id ? toggle.accepted(item) : item)),
                      )
                    }
                  />
                  <span>{toggle.checked(row) ? "Enabled" : "Disabled"}</span>
                </label>
              )}
              {removable(row) ? (
                <>
                  {singular !== "scope" && (
                    <button
                      type="button"
                      aria-label={`Edit ${singular} ${row.id}`}
                      disabled={resource.busy}
                      onClick={() => edit(row)}
                    >
                      <UroIcon name="settings" />
                    </button>
                  )}
                  <button
                    type="button"
                    aria-label={`Delete ${singular} ${row.id}`}
                    disabled={resource.busy}
                    onClick={async () => {
                      if (
                        (await resource.run(
                          () => remove(row),
                          (previous) => previous.filter((item) => item.id !== row.id),
                        )) &&
                        editor?.id === row.id
                      )
                        close();
                    }}
                  >
                    <UroIcon name="close" />
                  </button>
                </>
              ) : (
                <small>Inherited</small>
              )}
            </div>
          </article>
        ))}
        {!rows.length && (
          <div className="astra-overview-empty">
            {all.length
              ? "No matching entries."
              : resource.data
                ? `No ${title.toLowerCase()} yet.`
                : "Loading task policy…"}
          </div>
        )}
      </div>
      {rows.length > limit && (
        <button type="button" className="astra-overview-more" onClick={() => show((previous) => previous + 20)}>
          Show more · {rows.length - limit} remaining
        </button>
      )}
    </section>
  );
}
