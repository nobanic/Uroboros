"use client";
import { useCallback, useEffect, useRef, useState } from "react";

import Link from "next/link";

import {
  ArrowRight,
  Bot,
  Braces,
  Check,
  CircleHelp,
  Filter,
  GitBranch,
  Pencil,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
  X,
} from "lucide-react";

import { PolicyEditor, PolicyError, PolicyJudge, PolicyScope, PolicySwitch } from "./interception-controls";
import { policyActions, policyCounts, policyRules } from "./interception-document";
import { useInterception } from "./use-interception";
import "./interception-workspace.css";

export function InterceptionWorkspace() {
  const { journal, state } = useInterception();
  const [tab, setTab] = useState<"rules" | "judge">("rules"),
    [query, setQuery] = useState(""),
    [action, setAction] = useState("all"),
    [scopeQuery, setScopeQuery] = useState(""),
    [helpOpen, setHelpOpen] = useState(false);
  const search = useRef<HTMLInputElement>(null),
    counts = policyCounts(state.rules),
    rows = policyRules(state.rules, query, action),
    ordered = policyRules(state.rules),
    busy = Boolean(state.writing);
  const toggleContext = useCallback(() => {
    setHelpOpen((previous) => {
      const next = !previous;
      localStorage.setItem("uroboros-module-rail", next ? "open" : "closed");
      return next;
    });
  }, []);
  useEffect(() => {
    setHelpOpen(localStorage.getItem("uroboros-module-rail") === "open");
  }, []);
  useEffect(() => {
    const listener = (event: KeyboardEvent) => {
      if (
        !["/", "]"].includes(event.key) ||
        event.metaKey ||
        event.ctrlKey ||
        event.altKey ||
        (event.target instanceof HTMLElement &&
          (event.target.matches("input,textarea,select") || event.target.isContentEditable)) ||
        state.editor ||
        state.scope.open
      )
        return;
      if (event.key === "]") {
        event.preventDefault();
        toggleContext();
      } else if (tab === "rules") {
        event.preventDefault();
        search.current?.focus();
      }
    };
    window.addEventListener("keydown", listener);
    return () => window.removeEventListener("keydown", listener);
  }, [tab, state.editor, state.scope.open, toggleContext]);
  function selectTab(next: "rules" | "judge") {
    setTab(next);
    if (next === "judge" && !state.judge.loaded && !state.judge.loading) void journal.loadJudge();
  }
  return (
    <main className="astra-policy-workspace" data-interception-workspace>
      <section className="astra-policy-command">
        <div className="astra-policy-command-icon">
          <ShieldCheck size={28} />
        </div>
        <div className="astra-policy-command-title">
          <span className="astra-policy-eyebrow">Uroboros / tool execution policy</span>
          <h1>Command decision engine</h1>
          <p>Protected tools. Ordered rules. An explicit decision before execution.</p>
        </div>
        <div className="astra-policy-command-actions">
          <Link href="/system/intercept/approvals">
            Approval queue <ArrowRight size={14} />
          </Link>
          <button
            type="button"
            className="is-primary"
            disabled={busy}
            onClick={() => {
              setScopeQuery("");
              void journal.openScope();
            }}
          >
            <Filter size={15} />
            Adjust scope
          </button>
        </div>
      </section>
      <div className="astra-policy-contextbar">
        <button
          type="button"
          onClick={() => {
            selectTab("rules");
            requestAnimationFrame(() => search.current?.focus());
          }}
        >
          Focus filter <kbd>/</kbd>
        </button>
        <button type="button" aria-expanded={helpOpen} aria-controls="policy-context" onClick={toggleContext}>
          Policy context <kbd>]</kbd>
        </button>
        <span>Scope → match → decide</span>
      </div>
      {helpOpen && (
        <section id="policy-context" className="astra-policy-context" aria-label="Command policy context">
          <p>The first matching rule wins. Priority and target field are part of the policy, not display metadata.</p>
          <ol>
            <li>
              <strong>01 · Scope</strong>Choose protected tools.
            </li>
            <li>
              <strong>02 · Match</strong>Order patterns and target fields.
            </li>
            <li>
              <strong>03 · Decide</strong>Ask, allow, or deny explicitly.
            </li>
          </ol>
          <nav aria-label="Related policy surfaces">
            <Link href="/system/intercept/assets">
              Asset guard <ArrowRight size={13} />
            </Link>
            <Link href="/system/intercept/approvals">
              Approval queue <ArrowRight size={13} />
            </Link>
            <Link href="/system/tools">
              Toolchain <ArrowRight size={13} />
            </Link>
          </nav>
        </section>
      )}
      <section className="astra-policy-metrics" aria-label="Active policy summary">
        <div>
          <span>Protected tools</span>
          <strong>{state.scopeKeys.length}</strong>
          <small>Explicit interception boundary</small>
        </div>
        <div>
          <span>Rules online</span>
          <strong>
            {counts.enabled}
            <em>/{counts.total}</em>
          </strong>
          <small>Ordered by priority</small>
        </div>
        {(["allow", "ask", "deny"] as const).map((key) => (
          <div key={key} data-verdict={key}>
            <span>{{ allow: "Allow gates", ask: "Human gates", deny: "Block gates" }[key]}</span>
            <strong>{counts[key]}</strong>
            <small>{policyActions[key]}</small>
          </div>
        ))}
      </section>
      <PolicyError
        text={state.scopeInfoError ? `Scope summary unavailable: ${state.scopeInfoError}` : ""}
        retry={() => void journal.refreshScopeInfo()}
      />
      <section className="astra-policy-surface">
        <header className="astra-policy-navigation">
          <nav aria-label="Command policy views">
            {(["rules", "judge"] as const).map((value) => (
              <button
                type="button"
                key={value}
                data-policy-tab={value}
                aria-pressed={tab === value}
                onClick={() => selectTab(value)}
              >
                {value === "rules" ? <GitBranch size={16} /> : <Bot size={16} />}
                {value === "rules" ? "Decision ladder" : "Model fallback"}
              </button>
            ))}
          </nav>
          <div className="astra-policy-boundary">
            <span>Active boundary</span>
            {state.scopeKeys.slice(0, 4).map((key) => (
              <code key={key}>{key}</code>
            ))}
            {state.scopeKeys.length > 4 && <small>+{state.scopeKeys.length - 4}</small>}
            {!state.scopeKeys.length && <small>{state.scopeInfoError ? "Unknown" : "No tools protected"}</small>}
          </div>
        </header>
        {tab === "judge" ? (
          <PolicyJudge journal={journal} state={state} />
        ) : (
          <section className="astra-policy-ladder">
            <header>
              <div>
                <span className="astra-policy-eyebrow">First match wins</span>
                <h2>Priority decision ladder</h2>
                <p>Enabled matchers evaluate from highest to lowest priority.</p>
              </div>
              <button type="button" className="is-primary" disabled={busy} onClick={() => journal.openEditor()}>
                <Plus size={15} />
                New rule
              </button>
            </header>
            <div className="astra-policy-flow" role="img" aria-label="Decision flow">
              <span>
                <ShieldCheck size={14} />
                Scoped call
              </span>
              <ArrowRight size={15} />
              <span>
                <Braces size={14} />
                Ordered matcher
              </span>
              <ArrowRight size={15} />
              <span>
                <Check size={14} />
                Final verdict
              </span>
            </div>
            <div className="astra-policy-filters">
              <input
                ref={search}
                type="search"
                aria-label="Search command rules"
                placeholder="Search rule names, patterns or messages…"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
              />
              <select aria-label="Filter command verdict" value={action} onChange={(e) => setAction(e.target.value)}>
                <option value="all">All verdicts</option>
                <option value="allow">Allow</option>
                <option value="ask">Human approval</option>
                <option value="deny">Block</option>
              </select>
              <span>
                {rows.length} / {state.rules.length}
              </span>
              <button
                type="button"
                aria-label="Refresh command rules"
                disabled={state.rulesLoading || busy}
                onClick={() => void journal.refreshRules()}
              >
                <RefreshCw size={15} />
              </button>
            </div>
            <PolicyError text={state.rulesError} retry={() => void journal.refreshRules()} />
            {state.rulesLoading && (
              <p role="status" className="astra-policy-read-status">
                Refreshing policy…
              </p>
            )}
            <div className="astra-policy-rule-list">
              {rows.map((rule) => (
                <article
                  key={rule.id}
                  data-policy-rule={rule.name}
                  data-enabled={rule.enabled}
                  data-verdict={rule.action}
                >
                  <div className="astra-policy-rule-rank">
                    <small>{String(ordered.findIndex((row) => row.id === rule.id) + 1).padStart(2, "0")}</small>
                    <strong>{rule.priority}</strong>
                    <span>priority</span>
                  </div>
                  <div className="astra-policy-rule-body">
                    <header>
                      <div>
                        <span className="astra-policy-eyebrow">
                          {rule.match_target === "tool_name" ? "Tool identity" : "Input payload"}
                        </span>
                        <h3>{rule.name}</h3>
                      </div>
                      <span className="astra-policy-state">{rule.enabled ? "Evaluating" : "Bypassed"}</span>
                    </header>
                    <div className="astra-policy-match">
                      <Braces size={16} />
                      <code title={rule.pattern}>{rule.pattern}</code>
                      <small>{rule.match_type === "regex" ? "RE2" : "contains"}</small>
                    </div>
                    {(rule.message || rule.action === "ask") && (
                      <details>
                        <summary>Message &amp; approval policy</summary>
                        {rule.message && <p>{rule.message}</p>}
                        {rule.action === "ask" && (
                          <p>
                            {rule.timeout_enabled
                              ? `${rule.timeout_seconds}s wait → auto-${rule.timeout_action}`
                              : "No approval timeout"}
                          </p>
                        )}
                      </details>
                    )}
                  </div>
                  <div className="astra-policy-verdict">
                    {{ allow: <Check size={17} />, ask: <CircleHelp size={17} />, deny: <X size={17} /> }[rule.action]}
                    <span>{policyActions[rule.action]}</span>
                  </div>
                  <div className="astra-policy-rule-actions">
                    <PolicySwitch
                      checked={rule.enabled}
                      disabled={busy}
                      aria-label={`Enable ${rule.name}`}
                      onCheckedChange={() => void journal.changeRule(rule)}
                    />
                    <button
                      type="button"
                      disabled={busy}
                      aria-label={`Edit ${rule.name}`}
                      onClick={() => journal.openEditor(rule)}
                    >
                      <Pencil size={15} />
                    </button>
                    <button
                      type="button"
                      disabled={busy}
                      aria-label={`Delete ${rule.name}`}
                      onClick={() => void journal.changeRule(rule, true)}
                    >
                      <Trash2 size={15} />
                    </button>
                  </div>
                </article>
              ))}
              {!rows.length && !state.rulesLoading && (
                <div className="astra-policy-empty">
                  <GitBranch size={30} />
                  <h3>{state.rules.length ? "No matching rules" : "Create the first decision gate"}</h3>
                  <p>
                    {state.rules.length
                      ? "Change your search or verdict filter."
                      : "Define a protected pattern and its explicit verdict."}
                  </p>
                  <button
                    type="button"
                    disabled={busy}
                    onClick={() => {
                      if (state.rules.length) {
                        setQuery("");
                        setAction("all");
                      } else journal.openEditor();
                    }}
                  >
                    {state.rules.length ? "Clear filters" : "New rule"}
                  </button>
                </div>
              )}
            </div>
            <footer className="astra-policy-fallback">
              <Bot size={22} />
              <div>
                <span className="astra-policy-eyebrow">No deterministic match</span>
                <p>Continue to the configured model fallback or pass through according to policy.</p>
              </div>
              <button type="button" onClick={() => selectTab("judge")}>
                View fallback <ArrowRight size={14} />
              </button>
            </footer>
          </section>
        )}
      </section>
      <p className="astra-policy-notice" role="status" aria-live="polite">
        {state.notice}
      </p>
      <PolicyEditor journal={journal} state={state} />
      <PolicyScope journal={journal} state={state} search={scopeQuery} onSearch={setScopeQuery} />
    </main>
  );
}
