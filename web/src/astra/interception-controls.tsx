"use client";
import type { ComponentProps, ReactNode } from "react";

import { Switch as SwitchPrimitive } from "radix-ui";

import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import type { JudgeConfig } from "@/lib/types";

import { type PolicyDraft, policyRegexNotice, policyTokens, policyToolGroups } from "./interception-document";
import type { InterceptionJournal } from "./interception-journal";

export function PolicySwitch(props: ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root {...props} data-slot="switch">
      <SwitchPrimitive.Thumb data-slot="switch-thumb" />
    </SwitchPrimitive.Root>
  );
}

type JournalProps = { journal: InterceptionJournal; state: ReturnType<InterceptionJournal["snapshot"]> };
export function PolicyField({ label, children, id }: { label: string; id: string; children: ReactNode }) {
  return (
    <div className="astra-policy-field">
      <label htmlFor={id}>{label}</label>
      {children}
    </div>
  );
}
export function PolicyError({ text, retry }: { text: string; retry?: () => void }) {
  return text ? (
    <div className="astra-policy-error" role="alert">
      <span>{text}</span>
      {retry && (
        <button type="button" onClick={retry}>
          Retry
        </button>
      )}
    </div>
  ) : null;
}
export function PolicyEditor({ journal, state }: JournalProps) {
  const editor = state.editor,
    draft = editor?.draft;
  const disabled = Boolean(state.writing),
    patch = journal.patchRule;
  function text(key: keyof PolicyDraft, value: string) {
    patch({ [key]: value });
  }
  return (
    <Sheet
      open={Boolean(editor)}
      onOpenChange={(open) => {
        if (!open) journal.closeEditor();
      }}
    >
      <SheetContent className="astra-policy-dialog astra-policy-editor" side="right">
        <SheetHeader>
          <span className="astra-policy-eyebrow">Deterministic gate / {editor?.id ?? "new"}</span>
          <SheetTitle>{editor?.id ? "Edit rule" : "New rule"}</SheetTitle>
          <SheetDescription>
            Higher priority matches first; the first matching rule wins and the rest are skipped.
          </SheetDescription>
        </SheetHeader>
        {draft && (
          <>
            <div className="astra-policy-editor-body">
              <fieldset disabled={disabled}>
                <PolicyField id="policy-name" label="Name">
                  <input
                    id="policy-name"
                    value={draft.name}
                    placeholder="Give the rule a name"
                    onChange={(e) => text("name", e.target.value)}
                  />
                </PolicyField>
                <PolicyField id="policy-priority" label="Priority (higher number matches first)">
                  <input
                    id="policy-priority"
                    type="number"
                    value={draft.priority}
                    onChange={(e) => patch({ priority: parseInt(e.target.value, 10) || 0 })}
                  />
                </PolicyField>
                <div className="astra-policy-field-pair">
                  <PolicyField id="policy-target" label="Match target">
                    <select
                      id="policy-target"
                      value={draft.match_target}
                      onChange={(e) => patch({ match_target: e.target.value as PolicyDraft["match_target"] })}
                    >
                      <option value="tool_name">Tool name (tool_name)</option>
                      <option value="tool_input">Input content (tool_input JSON)</option>
                    </select>
                  </PolicyField>
                  <PolicyField id="policy-type" label="Match type">
                    <select
                      id="policy-type"
                      value={draft.match_type}
                      onChange={(e) => patch({ match_type: e.target.value as PolicyDraft["match_type"] })}
                    >
                      <option value="string">String contains</option>
                      <option value="regex">Regular expression</option>
                    </select>
                  </PolicyField>
                </div>
                <PolicyField id="policy-pattern" label="Pattern">
                  <input
                    id="policy-pattern"
                    className="astra-policy-code"
                    value={draft.pattern}
                    placeholder={draft.match_type === "regex" ? "^Bash$" : "rm -rf"}
                    onChange={(e) => text("pattern", e.target.value)}
                  />
                </PolicyField>
                {policyRegexNotice(draft) && (
                  <p className="astra-policy-warning">
                    The browser cannot preview this RE2 expression. Go syntax such as (?i) remains allowed; the server
                    validates it after submission.
                  </p>
                )}
                <PolicyField id="policy-action" label="Interception policy">
                  <select
                    id="policy-action"
                    value={draft.action}
                    onChange={(e) => patch({ action: e.target.value as PolicyDraft["action"] })}
                  >
                    <option value="allow">Allow — pass through directly, skip the remaining rules</option>
                    <option value="deny">Deny — block and return a rejection message to the model</option>
                    <option value="ask">Ask the user — wait for approval</option>
                  </select>
                </PolicyField>
                {draft.action !== "allow" && (
                  <PolicyField
                    id="policy-message"
                    label={
                      draft.action === "deny" ? "Rejection message (returned to the model)" : "Approval note (optional)"
                    }
                  >
                    <textarea
                      id="policy-message"
                      rows={3}
                      value={draft.message}
                      placeholder={draft.action === "deny" ? "Operation blocked by security policy" : ""}
                      onChange={(e) => text("message", e.target.value)}
                    />
                  </PolicyField>
                )}
                {draft.action === "ask" && (
                  <section className="astra-policy-timeout">
                    <label className="astra-policy-switchline" htmlFor="policy-timeout-enabled">
                      <span>
                        <strong>Enable approval timeout</strong>
                        <small>Apply the configured action after waiting.</small>
                      </span>
                      <PolicySwitch
                        id="policy-timeout-enabled"
                        checked={draft.timeout_enabled}
                        onCheckedChange={(timeout_enabled) => patch({ timeout_enabled })}
                      />
                    </label>
                    {draft.timeout_enabled && (
                      <div className="astra-policy-field-pair">
                        <PolicyField id="policy-timeout" label="Timeout (seconds)">
                          <input
                            id="policy-timeout"
                            type="number"
                            min={5}
                            value={draft.timeout_seconds}
                            onChange={(e) => {
                              const n = parseInt(e.target.value, 10);
                              if (n > 0) patch({ timeout_seconds: n });
                            }}
                          />
                        </PolicyField>
                        <PolicyField id="policy-timeout-action" label="Timeout action">
                          <select
                            id="policy-timeout-action"
                            value={draft.timeout_action}
                            onChange={(e) => patch({ timeout_action: e.target.value as "allow" | "deny" })}
                          >
                            <option value="deny">Auto-deny</option>
                            <option value="allow">Auto-allow</option>
                          </select>
                        </PolicyField>
                      </div>
                    )}
                  </section>
                )}
                <label className="astra-policy-switchline" htmlFor="rule-enabled">
                  <span>Enable this rule</span>
                  <PolicySwitch
                    id="rule-enabled"
                    checked={draft.enabled}
                    onCheckedChange={(enabled) => patch({ enabled })}
                  />
                </label>
              </fieldset>
              <PolicyError text={editor.error} />
            </div>
            <footer>
              <button type="button" onClick={journal.closeEditor}>
                Cancel
              </button>
              <button type="button" className="is-primary" disabled={disabled} onClick={() => void journal.saveRule()}>
                {disabled ? "Saving…" : "Save"}
              </button>
            </footer>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
export function PolicyScope({
  journal,
  state,
  search,
  onSearch,
}: JournalProps & { search: string; onSearch: (value: string) => void }) {
  const scope = state.scope,
    busy = Boolean(state.writing);
  const groups = policyToolGroups(scope.tools, scope.selected, search);
  return (
    <Dialog
      open={scope.open}
      onOpenChange={(open) => {
        if (!open) journal.closeScope();
      }}
    >
      <DialogContent className="astra-policy-dialog astra-policy-scope">
        <DialogHeader>
          <span className="astra-policy-eyebrow">Execution boundary / {scope.selected.size} selected</span>
          <DialogTitle>Interception scope</DialogTitle>
          <DialogDescription>
            Only tools with interception enabled enter rule matching; all other tools pass through directly.
          </DialogDescription>
        </DialogHeader>
        <div className="astra-policy-scope-body">
          <input
            aria-label="Search scope tools"
            placeholder="Search tool names and descriptions…"
            value={search}
            onChange={(e) => onSearch(e.target.value)}
          />
          <PolicyError text={scope.error} retry={() => void journal.openScope()} />
          {scope.loading ? (
            <p role="status">Loading…</p>
          ) : (
            scope.ready && (
              <>
                {groups.map((group) => (
                  <section key={group.label}>
                    <h3>{group.label}</h3>
                    {group.tools.map((tool) => (
                      <div className="astra-policy-tool" key={tool.key}>
                        <PolicySwitch
                          id={`scope-${tool.key}`}
                          disabled={busy}
                          checked={scope.selected.has(tool.key)}
                          onCheckedChange={(enabled) => journal.selectTool(tool.key, enabled)}
                        />
                        <label htmlFor={`scope-${tool.key}`}>
                          <strong>{tool.key}</strong>
                          {tool.kind !== "builtin" && <span>{tool.kind}</span>}
                          <small>{tool.description}</small>
                        </label>
                      </div>
                    ))}
                  </section>
                ))}
                {!groups.length && <p>No tools match this search.</p>}
              </>
            )
          )}
        </div>
        <footer>
          <button type="button" onClick={journal.closeScope}>
            Cancel
          </button>
          <button
            type="button"
            className="is-primary"
            disabled={!scope.ready || scope.loading || busy}
            onClick={() => void journal.saveScope()}
          >
            {busy ? "Saving…" : "Save"}
          </button>
        </footer>
      </DialogContent>
    </Dialog>
  );
}
export function PolicyJudge({ journal, state }: JournalProps) {
  const judge = state.judge,
    config = judge.config,
    usage = judge.usage,
    disabled = !judge.loaded || judge.loading || Boolean(state.writing);
  const patch = journal.patchJudge;
  const missing = config.profile_id && !judge.profiles.some((profile) => Number(profile.id) === config.profile_id);
  const meters = usage
    ? [
        ["Approval calls", usage.calls.toLocaleString()],
        ["Input tokens", policyTokens(usage.input_tokens)],
        ["Output tokens", policyTokens(usage.output_tokens)],
        ["Cache read", policyTokens(usage.cache_read_tokens)],
        ["Cache write", policyTokens(usage.cache_write_tokens)],
      ]
    : [];
  const peak = Math.max(1, ...(usage?.daily.map((day) => day.input_tokens + day.output_tokens) ?? []));
  return (
    <section className="astra-policy-judge" data-policy-judge>
      <div className="astra-policy-judge-heading">
        <div>
          <span className="astra-policy-eyebrow">Unmatched protected calls</span>
          <h2>Model fallback approval</h2>
          <p>
            Only commands within the interception scope that match no rule reach the model for allow, human approval or
            block.
          </p>
        </div>
        <label className="astra-policy-switchline" htmlFor="judge-enabled">
          <span>{config.enabled ? "Enabled" : "Disabled"}</span>
          <PolicySwitch
            id="judge-enabled"
            aria-label="Enable model fallback"
            disabled={disabled}
            checked={config.enabled}
            onCheckedChange={(enabled) => patch({ enabled })}
          />
        </label>
      </div>
      <PolicyError text={judge.error} retry={() => void journal.loadJudge()} />
      {judge.loading && <p role="status">Loading model policy…</p>}
      {config.enabled && (
        <>
          <section className="astra-policy-usage">
            <header>
              <div>
                <span className="astra-policy-eyebrow">Separate judge channel</span>
                <h3>Approval token usage</h3>
              </div>
              <button type="button" disabled={judge.usageLoading} onClick={() => void journal.loadUsage()}>
                Refresh
              </button>
            </header>
            <PolicyError text={judge.usageError} retry={() => void journal.loadUsage()} />
            {usage ? (
              <>
                <dl>
                  {meters.map(([label, value]) => (
                    <div key={label}>
                      <dt>{label}</dt>
                      <dd>{value}</dd>
                    </div>
                  ))}
                </dl>
                {usage.daily.length > 0 && (
                  <div
                    role="img"
                    className="astra-policy-sparkbars"
                    aria-label="Daily input and output tokens over the last 30 days"
                  >
                    {usage.daily.map((day) => (
                      <div
                        key={day.date}
                        title={`${day.date} · ${day.calls} calls · ${policyTokens(day.input_tokens + day.output_tokens)} tokens`}
                        style={{
                          height: `${Math.max(2, Math.round(((day.input_tokens + day.output_tokens) / peak) * 100))}%`,
                        }}
                      />
                    ))}
                  </div>
                )}
              </>
            ) : (
              <p>
                {judge.usageLoading ? "Loading metered usage…" : "Usage is unavailable. Configuration stays editable."}
              </p>
            )}
          </section>
          <fieldset disabled={disabled} className="astra-policy-judge-form">
            <div className="astra-policy-prompt">
              <header>
                <div>
                  <h3>Approval prompt</h3>
                  <p>ALLOW / ASK / DENY instructions sent to the model.</p>
                </div>
                <button type="button" onClick={() => void journal.saveJudge(true)}>
                  Restore default template
                </button>
              </header>
              <textarea
                aria-label="Approval prompt"
                value={config.prompt}
                onChange={(e) => patch({ prompt: e.target.value })}
                placeholder="Leave empty to use the built-in template"
                spellCheck={false}
              />
              <small>{config.prompt.length} chars</small>
            </div>
            <div className="astra-policy-parameters">
              <span className="astra-policy-eyebrow">Judgment model and policy</span>
              <PolicyField id="policy-profile" label="Approval model">
                <select
                  id="policy-profile"
                  value={String(config.profile_id || 0)}
                  onChange={(e) => patch({ profile_id: Number(e.target.value) })}
                >
                  <option value="0">Follow active profile</option>
                  {Boolean(missing) && (
                    <option value={String(config.profile_id)}>
                      Unavailable profile #{config.profile_id} · retained
                    </option>
                  )}
                  {judge.profiles.map((profile) => (
                    <option key={profile.id} value={profile.id}>
                      {profile.name} ({profile.model})
                    </option>
                  ))}
                </select>
              </PolicyField>
              <PolicyField id="judge-timeout" label="Model judgment timeout (seconds)">
                <input
                  id="judge-timeout"
                  type="number"
                  min={1}
                  value={config.timeout_seconds}
                  onChange={(e) => {
                    const n = parseInt(e.target.value, 10);
                    if (n > 0) patch({ timeout_seconds: n });
                  }}
                />
              </PolicyField>
              <PolicyField id="judge-failure" label="On model failure (error / timeout / unparseable)">
                <select
                  id="judge-failure"
                  value={config.fail_action}
                  onChange={(e) => patch({ fail_action: e.target.value as JudgeConfig["fail_action"] })}
                >
                  <option value="allow">Allow</option>
                  <option value="ask">Escalate to human approval</option>
                  <option value="deny">Block</option>
                </select>
              </PolicyField>
              <span className="astra-policy-eyebrow">Human escalation</span>
              <PolicyField id="judge-wait" label="Approval wait timeout (seconds)">
                <input
                  id="judge-wait"
                  type="number"
                  min={5}
                  value={config.ask_timeout_seconds}
                  onChange={(e) => {
                    const n = parseInt(e.target.value, 10);
                    if (n > 0) patch({ ask_timeout_seconds: n });
                  }}
                />
              </PolicyField>
              <PolicyField id="judge-after" label="Default action after timeout">
                <select
                  id="judge-after"
                  value={config.ask_timeout_action}
                  onChange={(e) => patch({ ask_timeout_action: e.target.value as JudgeConfig["ask_timeout_action"] })}
                >
                  <option value="deny">Block</option>
                  <option value="allow">Allow</option>
                </select>
              </PolicyField>
            </div>
          </fieldset>
        </>
      )}
      <footer>
        <span>Changes apply after saving. Recorded approvals retain their original context.</span>
        <button type="button" className="is-primary" disabled={disabled} onClick={() => void journal.saveJudge()}>
          {state.writing === "judge" ? "Saving…" : "Save config"}
        </button>
      </footer>
    </section>
  );
}
