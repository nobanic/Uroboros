"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import type { Agent, AgentTrigger, Tool } from "@/lib/types";
import { consoleRequest } from "./console-api";
import { FormFields } from "./form-fields";
import { EditorDialog } from "./editor-dialog";
import { OrderedChoices } from "./operation-editor";
import {
  triggerConditions,
  triggerDocument,
  triggerDraft,
  triggerForm,
  triggerPolicyDocument,
  triggerPolicyDraft,
  triggerPolicyForm,
} from "./agent-document";

function TriggerEditor({
  agentKey,
  trigger,
  tools,
  close,
  saved,
}: {
  agentKey: string;
  trigger?: AgentTrigger;
  tools: Tool[];
  close: () => void;
  saved: () => Promise<void>;
}) {
  const [draft, edit] = useState(() => triggerDraft(trigger)),
    [busy, pending] = useState(false),
    [error, fail] = useState("");
  return (
    <EditorDialog
      title={trigger ? `Trigger ${trigger.id}` : "New agent trigger"}
      eyebrow="Trigger"
      busy={busy}
      close={close}
    >
      <form
        onSubmit={(event) => {
          event.preventDefault();
          pending(true);
          fail("");
          void (async () => {
            try {
              await consoleRequest(
                trigger ? `/triggers/${trigger.id}` : `/agents/${encodeURIComponent(agentKey)}/triggers`,
                trigger ? "PATCH" : "POST",
                triggerDocument(draft),
              );
              await saved();
              close();
            } catch (error) {
              fail(error instanceof Error ? error.message : String(error));
            } finally {
              pending(false);
            }
          })();
        }}
      >
        {error && (
          <p role="alert" className="astra-form-error">
            {error}
          </p>
        )}
        <FormFields groups={triggerForm} value={draft} onChange={edit} disabled={busy} />
        <OrderedChoices
          title="Trigger tools"
          choices={tools.map((tool) => ({ id: tool.key, label: tool.key }))}
          value={draft.tool_names as string[]}
          change={(names) => edit((current) => ({ ...current, tool_names: names }))}
          disabled={busy || !draft.on_tool_call}
        />
        <div className="astra-form-actions">
          <button disabled={busy}>Save trigger</button>
          <button type="button" disabled={busy} onClick={close}>
            Cancel
          </button>
        </div>
      </form>
    </EditorDialog>
  );
}

export function AgentTriggers({
  agent,
  tools,
  changed,
  failure,
}: {
  agent: Agent;
  tools: Tool[];
  changed: () => Promise<void>;
  failure: (error: unknown) => void;
}) {
  const [items, setItems] = useState<AgentTrigger[]>([]),
    [editor, open] = useState<AgentTrigger | null>(),
    [deleting, confirm] = useState<AgentTrigger>(),
    [busy, pending] = useState(false),
    [policy, configure] = useState(() => triggerPolicyDraft(agent));
  const sequence = useRef(0);
  const load = useCallback(async () => {
    const request = ++sequence.current;
    const result = await consoleRequest<{ triggers: AgentTrigger[] }>(
      `/agents/${encodeURIComponent(agent.key)}/triggers`,
    );
    if (request === sequence.current) setItems(result.triggers || []);
  }, [agent.key]);
  useEffect(() => {
    void load().catch(failure);
  }, [load, failure]);
  async function action(work: () => Promise<void>) {
    pending(true);
    try {
      await work();
      await load();
    } catch (error) {
      failure(error);
    } finally {
      pending(false);
    }
  }
  return (
    <section className="astra-agent-triggers">
      <form
        onSubmit={(event) => {
          event.preventDefault();
          void action(async () => {
            await consoleRequest(
              `/agents/${encodeURIComponent(agent.key)}/config`,
              "PUT",
              triggerPolicyDocument(policy),
            );
            await changed();
          });
        }}
      >
        <FormFields groups={triggerPolicyForm} value={policy} onChange={configure} disabled={busy} />
        <p>
          Serial merging applies to queued events. Parallel mode uses the session limit; 0 permits unlimited sessions.
        </p>
        <button disabled={busy}>Save trigger policy</button>
      </form>
      <header className="astra-agent-section-header">
        <h2>Trigger rules</h2>
        <button disabled={busy} onClick={() => open(null)}>
          New trigger
        </button>
        <button disabled={busy} onClick={() => void load().catch(failure)}>
          Refresh triggers
        </button>
      </header>
      {items.map((item) => (
        <article className="astra-agent-trigger" key={item.id}>
          <h3>
            Trigger {item.id} · {item.enabled ? "Enabled" : "Disabled"}
          </h3>
          <p>
            {[
              ...(item.interval_sec > 0 ? [`Every ${item.interval_sec} seconds`] : []),
              ...triggerConditions.filter(([flag]) => item[flag]).map(([, , label]) => label),
            ].join(" · ")}
          </p>
          <details>
            <summary>Messages and tool selection</summary>
            <pre>
              {JSON.stringify(
                {
                  interval_message: item.interval_message,
                  ...Object.fromEntries(triggerConditions.map(([, message]) => [message, item[message]])),
                  tool_names: item.tool_names,
                  last_fire: item.last_fire,
                },
                null,
                2,
              )}
            </pre>
          </details>
          <div className="astra-agent-section-header">
            <button disabled={busy} onClick={() => open(item)}>
              Edit trigger
            </button>
            <button
              disabled={busy}
              onClick={() =>
                void action(async () => {
                  const { id, agent_key, last_fire, ...document } = item;
                  await consoleRequest(`/triggers/${id}`, "PATCH", { ...document, enabled: !item.enabled });
                })
              }
            >
              {item.enabled ? "Disable trigger" : "Enable trigger"}
            </button>
            <button disabled={busy} onClick={() => confirm(item)}>
              Delete trigger
            </button>
          </div>
        </article>
      ))}
      {!items.length && <p>No trigger rules are configured.</p>}
      {editor !== undefined && (
        <TriggerEditor
          agentKey={agent.key}
          trigger={editor || undefined}
          tools={tools}
          close={() => open(undefined)}
          saved={load}
        />
      )}
      {deleting && (
        <EditorDialog
          title={`Delete trigger ${deleting.id}`}
          eyebrow="Trigger deletion"
          busy={busy}
          close={() => confirm(undefined)}
        >
          <p>Remove this trigger rule?</p>
          <div className="astra-form-actions">
            <button
              disabled={busy}
              onClick={() =>
                void action(async () => {
                  await consoleRequest(`/triggers/${deleting.id}`, "DELETE");
                  confirm(undefined);
                })
              }
            >
              Confirm trigger deletion
            </button>
            <button disabled={busy} onClick={() => confirm(undefined)}>
              Keep trigger
            </button>
          </div>
        </EditorDialog>
      )}
    </section>
  );
}
