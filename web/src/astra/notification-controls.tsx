"use client";
import { useState, type ComponentProps, type ReactNode } from "react";

import { Switch as SwitchPrimitive } from "radix-ui";

import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";

import {
  notificationFields,
  notificationKind,
  notificationMasked,
  notificationSeverities,
  type NotificationField,
} from "./notification-document";
import type { NotificationJournal } from "./notification-journal";

export type NotificationView = { journal: NotificationJournal; state: ReturnType<NotificationJournal["snapshot"]> };
export function NotificationSwitch(props: ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root {...props} data-slot="switch">
      <SwitchPrimitive.Thumb data-slot="switch-thumb" />
    </SwitchPrimitive.Root>
  );
}
export function NotificationError({ text, retry }: { text: string; retry?: () => void }) {
  return text ? (
    <div className="astra-notify-error" role="alert">
      <span>{text}</span>
      {retry && (
        <button type="button" onClick={retry}>
          Try again
        </button>
      )}
    </div>
  ) : null;
}
function Field({ id, label, hint, children }: { id: string; label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="astra-notify-field">
      <label htmlFor={id}>{label}</label>
      {children}
      {hint && <small id={`${id}-hint`}>{hint}</small>}
    </div>
  );
}
function ConfigInput({
  field,
  value,
  secret,
  disabled,
  change,
}: {
  field: NotificationField;
  value: unknown;
  secret: boolean;
  disabled: boolean;
  change: (value: unknown) => void;
}) {
  const [visible, setVisible] = useState(false),
    id = `n-cfg-${field.key}`,
    masked = secret && notificationMasked(value),
    hidden = secret || field.control === "password";
  const maskedTail = masked ? (String(value).split("…")[1] ?? "") : "";
  const display = masked ? "" : Array.isArray(value) ? value.join(", ") : String(value ?? "");
  const props = { id, disabled, "aria-describedby": field.hint ? `${id}-hint` : undefined };
  let input: ReactNode;
  if (field.control === "switch")
    input = <NotificationSwitch {...props} checked={value === true} onCheckedChange={change} />;
  else if (field.control === "select")
    input = (
      <select {...props} value={display || field.options?.[0]?.[0]} onChange={(event) => change(event.target.value)}>
        {field.options?.map(([value, label]) => (
          <option value={value} key={value}>
            {label}
          </option>
        ))}
      </select>
    );
  else if (["kv", "textarea"].includes(field.control))
    input = (
      <textarea
        {...props}
        value={display}
        placeholder={masked ? "Saved value retained" : field.placeholder}
        onChange={(event) => change(event.target.value)}
        rows={field.control === "kv" ? 3 : 5}
        className={hidden && !visible ? "astra-notify-concealed" : undefined}
        autoComplete="off"
        spellCheck={false}
      />
    );
  else
    input = (
      <input
        {...props}
        value={display}
        type={hidden && !visible ? "password" : field.control === "number" ? "number" : "text"}
        placeholder={masked ? "Saved value retained" : field.placeholder}
        onChange={(event) => change(event.target.value)}
        autoComplete="off"
      />
    );
  return (
    <Field id={id} label={field.label} hint={field.hint}>
      {input}
      {(masked || hidden) && (
        <div className="astra-notify-secret">
          <small>
            {masked
              ? `Saved credential${maskedTail ? ` · …${maskedTail}` : ""} kept until replaced or cleared`
              : "Changes apply when saved"}
          </small>
          {masked ? (
            <button
              type="button"
              disabled={disabled}
              onClick={() => change("")}
              aria-label={`Clear saved ${field.label}`}
            >
              Clear saved value
            </button>
          ) : (
            hidden && (
              <button
                type="button"
                onClick={() => setVisible(!visible)}
                aria-label={`${visible ? "Hide" : "Show"} ${field.label}`}
              >
                {visible ? "Hide" : "Show"}
              </button>
            )
          )}
        </div>
      )}
    </Field>
  );
}
export function NotificationEditor({ journal, state }: NotificationView) {
  const editor = state.editor,
    busy = Boolean(state.writing);
  if (!editor) return null;
  const { draft, id } = editor,
    metadata = state.meta?.kinds.find((kind) => kind.kind === draft.kind),
    fields = notificationFields(draft.kind),
    secrets = new Set(metadata?.secret_keys ?? []);
  const select = (
    label: string,
    key: "kind" | "mode" | "minSeverity",
    options: readonly (readonly [string, string])[],
    disabled = false,
  ) => (
    <Field id={`n-${key}`} label={label}>
      <select
        id={`n-${key}`}
        value={draft[key]}
        disabled={busy || disabled}
        onChange={(event) => journal.patchDraft({ [key]: event.target.value })}
      >
        {options.map(([value, label]) => (
          <option value={value} key={value}>
            {label}
          </option>
        ))}
      </select>
    </Field>
  );
  return (
    <Sheet
      open
      onOpenChange={(open) => {
        if (!open) journal.closeEditor();
      }}
    >
      <SheetContent
        className="astra-notify-editor"
        side="right"
        onEscapeKeyDown={(event) => {
          if (busy) event.preventDefault();
        }}
        onInteractOutside={(event) => {
          if (busy) event.preventDefault();
        }}
      >
        <SheetHeader>
          <SheetTitle>{id === null ? "Add notification channel" : notificationKind(draft.kind)}</SheetTitle>
          <SheetDescription>
            {id === null
              ? "Connect a destination and choose which events it receives."
              : "Edit a saved route. Keep masked values to retain stored credentials."}
          </SheetDescription>
        </SheetHeader>
        <div className="astra-notify-editor-body">
          <fieldset disabled={busy}>
            <legend>01 / Destination</legend>
            {select(
              "Channel type",
              "kind",
              (state.meta?.kinds ?? []).map((kind) => [kind.kind, notificationKind(kind.kind)]),
              id !== null,
            )}
            <Field id="n-name" label="Channel name">
              <input
                id="n-name"
                value={draft.name}
                onChange={(event) => journal.patchDraft({ name: event.target.value })}
              />
            </Field>
            {fields.length ? (
              fields.map((field) => (
                <ConfigInput
                  key={`${draft.kind}:${field.key}`}
                  field={field}
                  value={draft.config[field.key]}
                  secret={secrets.has(field.key)}
                  disabled={busy}
                  change={(value) => journal.config(field.key, value)}
                />
              ))
            ) : (
              <p>This channel type has no available editor yet. Its saved configuration remains on the server.</p>
            )}
            {id !== null && (
              <p className="astra-notify-note">
                To change channel type, create another route. Changing a destination may require re-entering its
                credentials.
              </p>
            )}
          </fieldset>
          <fieldset disabled={busy}>
            <legend>02 / Dispatch</legend>
            {select("Push timing", "mode", [
              ["realtime", "Realtime · one message per vulnerability"],
              ["digest", "Digest · combined into one message per interval"],
            ])}
            <Field
              id="n-rate"
              label="Rate limit (per minute)"
              hint={`Leave blank for the channel default (${metadata?.default_rate_per_min ?? 0}/min). Zero means unlimited; excess messages wait rather than being dropped.`}
            >
              <input
                id="n-rate"
                type="number"
                min={0}
                value={draft.ratePerMin}
                onChange={(event) => journal.patchDraft({ ratePerMin: event.target.value })}
              />
            </Field>
            <label className="astra-notify-switchline" htmlFor="n-enabled">
              <NotificationSwitch
                id="n-enabled"
                checked={draft.enabled}
                onCheckedChange={(enabled) => journal.patchDraft({ enabled })}
                aria-label="Enable"
              />
              Enable this channel
            </label>
          </fieldset>
          <fieldset disabled={busy}>
            <legend>03 / Event policy</legend>
            {select("Minimum severity", "minSeverity", notificationSeverities)}
            <Field
              id="n-inc"
              label="Only push these vulnerability types"
              hint="One keyword per line or comma. Case-insensitive substring match; empty includes all types."
            >
              <textarea
                id="n-inc"
                value={draft.includeText}
                onChange={(event) => journal.patchDraft({ includeText: event.target.value })}
                rows={3}
              />
            </Field>
            <Field id="n-exc" label="Exclude these vulnerability types" hint="Exclusion takes priority over inclusion.">
              <textarea
                id="n-exc"
                value={draft.excludeText}
                onChange={(event) => journal.patchDraft({ excludeText: event.target.value })}
                rows={2}
              />
            </Field>
            <div className="astra-notify-fieldpair">
              <Field id="n-tasks" label="Restrict to task IDs">
                <input
                  id="n-tasks"
                  value={draft.taskIDsText}
                  onChange={(event) => journal.patchDraft({ taskIDsText: event.target.value })}
                />
              </Field>
              <Field id="n-assets" label="Restrict to asset IDs">
                <input
                  id="n-assets"
                  value={draft.assetIDsText}
                  onChange={(event) => journal.patchDraft({ assetIDsText: event.target.value })}
                />
              </Field>
            </div>
            <p className="astra-notify-note">
              Blank task/asset lists are unrestricted. Set lists must intersect the vulnerability's tasks or assets.
            </p>
            <label className="astra-notify-switchline" htmlFor="n-status-change">
              <NotificationSwitch
                id="n-status-change"
                checked={draft.onStatusChange}
                onCheckedChange={(onStatusChange) => journal.patchDraft({ onStatusChange })}
                aria-label="Receive status changes"
              />
              Include triage status changes (realtime only)
            </label>
          </fieldset>
          {id !== null && (
            <p className="astra-notify-note">
              Send test message uses the saved channel, including saved credentials. Save edits before testing them.
              Direct tests bypass the queue and do not appear in the ledger.
            </p>
          )}
          <NotificationError text={editor.error} />
          {editor.testResult && (
            <p role="status" className="astra-notify-success">
              {editor.testResult}
            </p>
          )}
        </div>
        <footer className="astra-notify-editor-actions">
          <button
            type="button"
            className="astra-notify-primary"
            disabled={busy}
            onClick={() => void journal.saveChannel()}
          >
            {id === null ? "Add" : "Save"}
          </button>
          {id !== null && (
            <button type="button" disabled={busy} onClick={() => void journal.testChannel()}>
              Send test message
            </button>
          )}
          <span>{busy ? "Waiting for acknowledgement…" : ""}</span>
        </footer>
      </SheetContent>
    </Sheet>
  );
}
