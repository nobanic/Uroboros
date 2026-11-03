"use client";

import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ApiFailure, collection, type DataRow } from "./client";
import { consoleRequest as modelRequest } from "./console-api";
import { FormFields } from "./form-fields";
import { EditorDialog } from "./editor-dialog";
import { formDocument, type FormValues } from "./forms";
import { globalLayers, profileDocument, profileDraft, profileForm, profileProbe, retryForm } from "./model-profile";
import "./forms.css";

function ProfileEditor({
  profile,
  close,
  saved,
  failure,
}: {
  profile?: DataRow;
  close: () => void;
  saved: () => Promise<void>;
  failure: (error: unknown) => void;
}) {
  const [draft, setDraft] = useState(() => profileDraft(profile)),
    [busy, setBusy] = useState("");
  const [notice, setNotice] = useState(""),
    [models, setModels] = useState<string[]>([]);
  const execute = async (name: string, action: () => Promise<void>) => {
    setBusy(name);
    setNotice("");
    try {
      await action();
    } catch (error) {
      setNotice(error instanceof Error ? error.message : String(error));
      failure(error);
    } finally {
      setBusy("");
    }
  };
  const submit = (event: FormEvent) => {
    event.preventDefault();
    void execute("Saving", async () => {
      await modelRequest("/llm/profiles", "POST", profileDocument(draft));
      await saved();
      close();
    });
  };
  return (
    <EditorDialog
      title={profile ? String(profile.name) : "New profile"}
      eyebrow="Model profile"
      busy={Boolean(busy)}
      close={close}
    >
      <form onSubmit={submit}>
        {profile?.api_key_hint ? <p className="astra-form-note">Stored key: {String(profile.api_key_hint)}</p> : null}
        <FormFields groups={profileForm} value={draft} onChange={setDraft} disabled={Boolean(busy)} />
        <div className="astra-form-actions">
          <button
            type="button"
            disabled={Boolean(busy)}
            onClick={() =>
              void execute("Loading models", async () => {
                const result = await modelRequest<{ ok: boolean; error?: string; models?: string[] }>(
                  "/llm/models",
                  "POST",
                  profileProbe(draft),
                );
                if (!result.ok) throw new Error(result.error ?? "Model catalog unavailable");
                setModels(result.models ?? []);
                setNotice(`${result.models?.length ?? 0} models available`);
              })
            }
          >
            Load available models
          </button>
          <button
            type="button"
            disabled={Boolean(busy)}
            onClick={() =>
              void execute("Testing", async () => {
                const result = await modelRequest<{ ok: boolean; error?: string; latency_ms?: number; reply?: string }>(
                  "/llm/test",
                  "POST",
                  profileProbe(draft),
                );
                if (!result.ok) throw new Error(result.error ?? "Connection test failed");
                setNotice(`Connected · ${result.latency_ms ?? "?"} ms${result.reply ? ` · ${result.reply}` : ""}`);
              })
            }
          >
            Test connection
          </button>
          <button type="submit" disabled={Boolean(busy)} className="is-primary">
            {busy || "Save profile"}
          </button>
        </div>
        {models.length > 0 && (
          <label className="astra-model-picker">
            Available models
            <select
              aria-label="Available models"
              value=""
              onChange={(event) => setDraft((value) => ({ ...value, model: event.target.value }))}
            >
              <option value="">Choose a model…</option>
              {models.map((model) => (
                <option key={model}>{model}</option>
              ))}
            </select>
          </label>
        )}
        <p role="status">{notice}</p>
      </form>
    </EditorDialog>
  );
}

export function RetryPolicyEditor({ failure }: { failure: (error: unknown) => void }) {
  const [value, update] = useState<FormValues>(),
    [busy, setBusy] = useState(false),
    [notice, inform] = useState("");
  useEffect(() => {
    let current = true;
    modelRequest<FormValues>("/llm/retry-policy")
      .then((policy) => {
        if (current) update(policy);
      })
      .catch(failure);
    return () => {
      current = false;
    };
  }, [failure]);
  const groups = retryForm(globalLayers);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!value) return;
    setBusy(true);
    inform("");
    try {
      update(await modelRequest<FormValues>("/llm/retry-policy", "POST", formDocument(groups, value)));
      inform("Retry policy saved");
    } catch (error) {
      failure(error);
    } finally {
      setBusy(false);
    }
  };
  return (
    <section className="astra-model-section">
      <h2>Retry policy</h2>
      <p>
        Connection → empty response → uncommitted stream → circuit breaker → intent rerun. Outer retries can multiply
        the total request count.
      </p>
      {value ? (
        <form onSubmit={(event) => void submit(event)}>
          <FormFields groups={groups} value={value} onChange={update} disabled={busy} />
          <div className="astra-form-actions">
            <button disabled={busy}>{busy ? "Saving…" : "Save retry policy"}</button>
            <button
              type="button"
              disabled={busy}
              onClick={() =>
                update(Object.fromEntries(globalLayers.map((key) => [key, { attempts: 0, interval_ms: 0 }])))
              }
            >
              Reset all to default
            </button>
          </div>
          <p role="status">{notice}</p>
        </form>
      ) : (
        <p>Loading retry policy…</p>
      )}
    </section>
  );
}

export function ModelWorkspace({ onUnauthorized }: { onUnauthorized?: () => void }) {
  const [profiles, setProfiles] = useState<DataRow[]>([]),
    [pool, setPool] = useState<DataRow>();
  const [error, setError] = useState<string>(),
    [busy, setBusy] = useState(false),
    [editor, openEditor] = useState<DataRow | null>();
  const [query, setQuery] = useState(""),
    [tab, setTab] = useState("profiles");
  const failure = useCallback(
    (cause: unknown) => {
      if (cause instanceof ApiFailure && cause.status === 401) onUnauthorized?.();
      setError(cause instanceof Error ? cause.message : String(cause));
    },
    [onUnauthorized],
  );
  const reload = useCallback(async () => {
    setBusy(true);
    setError(undefined);
    try {
      const [items, chain] = await Promise.all([
        modelRequest<unknown>("/llm/profiles"),
        modelRequest<DataRow>("/llm/pool"),
      ]);
      setProfiles(collection(items, ["profiles"]));
      setPool(chain);
    } catch (error) {
      failure(error);
    } finally {
      setBusy(false);
    }
  }, [failure]);
  useEffect(() => {
    void reload();
    const timer = setInterval(() => void reload(), 10000);
    return () => clearInterval(timer);
  }, [reload]);
  const act = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError(undefined);
    try {
      await action();
      await reload();
    } catch (error) {
      failure(error);
    } finally {
      setBusy(false);
    }
  };
  const chain = collection(pool, ["chain"]),
    health = new Map(chain.map((row) => [String(row.profile_id), row]));
  const visible = [...profiles]
    .filter((row) =>
      [row.name, row.model, row.format].join(" ").toLocaleLowerCase().includes(query.toLocaleLowerCase()),
    )
    .sort(
      (a, b) =>
        Number(Boolean(b.is_default)) - Number(Boolean(a.is_default)) ||
        Number(b.priority ?? 0) - Number(a.priority ?? 0) ||
        Number(a.id) - Number(b.id),
    );
  return (
    <div className="astra-model-workspace">
      <header className="astra-model-header">
        <div>
          <small>Configuration</small>
          <h1>Model profiles</h1>
          <p>Connection settings, active routing and recovery.</p>
        </div>
        <div>
          <button disabled={busy} onClick={() => void reload()}>
            Refresh
          </button>
          <button className="is-primary" onClick={() => openEditor(null)}>
            New profile
          </button>
        </div>
      </header>
      {error && (
        <div role="alert" className="astra-form-error">
          {error}
          <button onClick={() => setError(undefined)} aria-label="Dismiss error">
            ×
          </button>
        </div>
      )}
      <nav aria-label="Model configuration sections">
        {["profiles", "pool", "retry"].map((value) => (
          <button key={value} aria-pressed={tab === value} onClick={() => setTab(value)}>
            {value === "pool" ? "Pool settings" : value === "retry" ? "Retry policy" : "Profiles"}
          </button>
        ))}
      </nav>
      {tab === "profiles" && (
        <section className="astra-model-section">
          <label>
            Filter profiles
            <input type="search" value={query} onChange={(event) => setQuery(event.target.value)} />
          </label>
          <div className="astra-model-cards">
            {visible.map((profile) => {
              const member = health.get(String(profile.id));
              const status = !profile.api_key_hint
                ? "Key not configured"
                : member?.state === "tripped"
                  ? `Tripped · ${member.cooldown_secs ?? 0}s cooldown`
                  : member?.state === "degraded"
                    ? `Degraded · ${member.fails} failures`
                    : "Healthy";
              return (
                <article key={String(profile.id)}>
                  <header>
                    <button onClick={() => openEditor(profile)}>{String(profile.name)}</button>
                    {profile.is_default ? <span>Active</span> : null}
                  </header>
                  <p>
                    {String(profile.model)} · {String(profile.format)}
                  </p>
                  <p title={String(member?.last_error ?? "")}>{status}</p>
                  <dl>
                    <div>
                      <dt>Pool priority</dt>
                      <dd>{Number(profile.priority ?? 0)}</dd>
                    </div>
                    <div>
                      <dt>Output</dt>
                      <dd>{profile.streaming === false ? "Non-streaming" : "Streaming"}</dd>
                    </div>
                  </dl>
                  {member?.last_error ? <p>{String(member.last_error)}</p> : null}
                  <footer>
                    <button onClick={() => openEditor(profile)}>Edit</button>
                    <button
                      disabled={busy || Boolean(profile.is_default)}
                      onClick={() =>
                        void act(() => modelRequest("/llm/profiles/active", "POST", { id: Number(profile.id) }))
                      }
                    >
                      Set active
                    </button>
                    <button
                      disabled={busy || Boolean(profile.is_default)}
                      onClick={() =>
                        void act(() =>
                          modelRequest(`/llm/profiles/${encodeURIComponent(String(profile.id))}`, "DELETE"),
                        )
                      }
                    >
                      Delete
                    </button>
                  </footer>
                </article>
              );
            })}
            {!visible.length && <p>{busy ? "Loading profiles…" : "No matching model profiles."}</p>}
          </div>
        </section>
      )}
      {tab === "pool" && (
        <section className="astra-model-section">
          <h2>Fallback routing</h2>
          <label className="astra-form-check">
            <input
              type="checkbox"
              checked={Boolean(pool?.enabled)}
              disabled={busy}
              onChange={(event) =>
                void act(() => modelRequest("/settings", "PUT", { llm_pool_enabled: event.target.checked }))
              }
            />
            Enable pool
          </label>
          <label className="astra-form-check">
            <input
              type="checkbox"
              checked={Boolean(pool?.bind_fallback)}
              disabled={busy || !pool?.enabled}
              onChange={(event) =>
                void act(() => modelRequest("/settings", "PUT", { llm_pool_bind_fallback: event.target.checked }))
              }
            />
            Fall back when a profile is pinned
          </label>
          <p>
            The active profile leads the chain; eligible keyed profiles follow by priority. Explicitly pinned models
            retain their binding unless fallback is enabled.
          </p>
          <button disabled={busy} onClick={() => void act(() => modelRequest("/llm/pool/reset", "POST", { id: 0 }))}>
            Recover all
          </button>
          <ol className="astra-model-chain">
            {chain.map((member) => (
              <li key={String(member.profile_id)}>
                <strong>{String(member.name)}</strong> · {String(member.model)} · {String(member.state)}
                <p>
                  {member.active ? "Active" : member.excluded ? "Excluded" : `Priority ${member.priority}`} ·{" "}
                  {Number(member.cooldown_secs ?? 0)}s cooldown · {Number(member.fails ?? 0)} consecutive failures
                </p>
                {member.last_error ? <p>{String(member.last_error)}</p> : null}
                <button
                  disabled={busy}
                  onClick={() =>
                    void act(() => modelRequest("/llm/pool/reset", "POST", { id: Number(member.profile_id) }))
                  }
                >
                  Recover now
                </button>
              </li>
            ))}
          </ol>
        </section>
      )}
      {tab === "retry" && <RetryPolicyEditor failure={failure} />}
      {editor !== undefined && (
        <ProfileEditor
          key={String(editor?.id ?? "new")}
          profile={editor ?? undefined}
          close={() => openEditor(undefined)}
          saved={reload}
          failure={failure}
        />
      )}
    </div>
  );
}
