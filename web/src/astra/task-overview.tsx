"use client";
import { useState, type ReactNode } from "react";
import { StatusBadge } from "@/components/status-badge";
import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import type { AssetInterceptKind, AssetInterceptRule, TaskConstraint, TaskGoal, TaskScopeRow } from "@/lib/types";
import {
  overviewCacheRate,
  overviewCount,
  overviewRuleKinds,
  overviewScopeKinds,
  overviewScopeText,
  overviewUsage,
  overviewWork,
} from "./overview-document";
import { OverviewNotice, OverviewPolicy, type OverviewField, type OverviewValues } from "./overview-policy";
import { useOverviewData } from "./use-overview-data";
import "./overview.css";

const goalFields: OverviewField[] = [
  { key: "text", label: "Goal text", required: true, placeholder: "A verifiable result for this task" },
  { key: "vulnclass", label: "Vulnerability class", placeholder: "Optional" },
];
const constraintFields: OverviewField[] = [
  {
    key: "kind",
    label: "Constraint action",
    options: [
      ["deny", "Deny"],
      ["allow", "Allow"],
    ],
  },
  { key: "text", label: "Constraint text", required: true, placeholder: "Which operations are allowed or denied?" },
];
const scopeFields: OverviewField[] = [
  { key: "kind", label: "Scope kind", options: overviewScopeKinds },
  {
    key: "value",
    label: "Scope value",
    required: true,
    placeholder: "Domain, address/range, company name or ID, ICP or keyword",
  },
];
const ruleFields: OverviewField[] = [
  {
    key: "action",
    label: "Rule action",
    options: [
      ["block", "Block"],
      ["allow", "Allow"],
    ],
  },
  { key: "kind", label: "Rule match", options: overviewRuleKinds },
  { key: "pattern", label: "Rule pattern", required: true, placeholder: "Domain, IP, URL, fragment or CIDR" },
  { key: "note", label: "Rule note", placeholder: "Optional" },
];
const ruleInput = (v: OverviewValues, row?: AssetInterceptRule) => ({
  action: v.action as "block" | "allow",
  kind: v.kind as AssetInterceptKind,
  pattern: v.pattern.trim(),
  note: v.note.trim(),
  enabled: row?.enabled ?? true,
});
const scopeName = (row: TaskScopeRow) => overviewScopeKinds.find(([kind]) => kind === row.kind)?.[1] || row.kind;

function Panel({
  title,
  icon,
  children,
  action,
  className = "",
}: {
  title: string;
  icon: UroIconName;
  children: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <section className={`astra-overview-panel ${className}`}>
      <header>
        <h2>
          <UroIcon name={icon} />
          {title}
        </h2>
        {action}
      </header>
      {children}
    </section>
  );
}

function TaskOverview({ id }: { id: string }) {
  const data = useOverviewData(id),
    [blockedLimit, showBlocked] = useState(20),
    [runningLimit, showRunning] = useState(6),
    [findingLimit, showFindings] = useState(6);
  const snapshot = data.main.data,
    task = snapshot?.task;
  const work = overviewWork(id, snapshot?.intents || [], snapshot?.findings || []);
  const models = data.tokens.data || [],
    usage = overviewUsage(models),
    coverage = data.coverage.data;
  const progress = task?.goals_total ? Math.round(((task.goals_met || 0) / task.goals_total) * 100) : 0;
  const refresh = () => {
    for (const resource of Object.values(data)) void resource.reload(true);
  };
  const rerun = (intent?: string) =>
    data.main.run(
      () => (intent ? api.rerunIntent(id, intent) : api.rerunBlocked(id)),
      (previous) => ({
        ...previous,
        intents: previous.intents.map((row) =>
          (intent ? row.id === intent : row.state === "blocked") ? { ...row, state: "open" } : row,
        ),
      }),
    );
  return (
    <div className="astra-overview" data-overview-task={id}>
      <section className="astra-overview-brief">
        <header>
          <div>
            <small>Operation brief</small>
            <h2>Task description and goal</h2>
          </div>
          <button type="button" onClick={refresh}>
            <UroIcon name="sync" /> Refresh overview
          </button>
        </header>
        <div>
          <article>
            <small>Description</small>
            <p>{task?.description?.trim() || (snapshot ? "—" : "Loading task…")}</p>
          </article>
          <article>
            <small>Desired outcome</small>
            <p>{task?.goal?.trim() || "—"}</p>
          </article>
        </div>
        <OverviewNotice error={data.main.error} retry={() => void data.main.reload(true)} />
      </section>
      <div className="astra-overview-metrics">
        {(
          [
            ["Running workers", work.running.length, "pulse"],
            ["Unclaimed intents", work.open.length, "route"],
            ["Blocked intents", work.blocked.length, "blocked"],
            ["Findings", work.findings.length, "findings"],
            ["Total intents", work.total, "topology"],
          ] as [string, number, UroIconName][]
        ).map(([label, value, icon]) => (
          <article key={label}>
            <UroIcon name={icon} />
            <strong>{snapshot ? value : "—"}</strong>
            <span>{label}</span>
          </article>
        ))}
      </div>
      <Panel title="Heartbeat" icon="pulse" className="astra-overview-heartbeat">
        <div>
          <span>Engine state</span>
          <StatusBadge domain="engine" value={snapshot?.stats.engine_mode ?? task?.engine_mode ?? "idle"} dot />
        </div>
        <div>
          <span>Running workers</span>
          <strong>{snapshot ? work.running.length : "—"}</strong>
        </div>
        <div>
          <span>Last activity</span>
          <strong>{task?.last_activity ? new Date(task.last_activity).toLocaleTimeString("en-US") : "—"}</strong>
        </div>
        <div>
          <span>
            Goals {task?.goals_met ?? 0}/{task?.goals_total ?? 0}
          </span>
          <progress aria-label="Goal completion" value={progress} max={100} />
          <small>{progress}%</small>
        </div>
        {!!task?.completed_unix && task.completed_unix > 0 && (
          <div>
            <span>Completed at</span>
            <strong>{new Date(task.completed_unix * 1000).toLocaleString("en-US")}</strong>
          </div>
        )}
      </Panel>
      <div className="astra-overview-work">
        <Panel title="In-progress intents" icon="agents">
          {work.running.slice(0, runningLimit).map((row) => (
            <article key={row.id} className="astra-overview-work-row">
              <StatusBadge domain="intent" value={row.state} />
              <p>{row.payload}</p>
            </article>
          ))}
          {!work.running.length && <p className="astra-overview-empty">No in-progress intents</p>}
          {work.running.length > runningLimit && (
            <button type="button" className="astra-overview-more" onClick={() => showRunning((n) => n + 20)}>
              Show more intents
            </button>
          )}
        </Panel>
        <Panel title="Recent findings" icon="findings">
          {work.findings.slice(0, findingLimit).map((row) => (
            <article key={row.id} className="astra-overview-work-row">
              <StatusBadge domain="severity" value={row.severity} dot />
              <p>{row.summary}</p>
            </article>
          ))}
          {!work.findings.length && <p className="astra-overview-empty">No findings yet</p>}
          {work.findings.length > findingLimit && (
            <button type="button" className="astra-overview-more" onClick={() => showFindings((n) => n + 20)}>
              Show more findings
            </button>
          )}
        </Panel>
      </div>
      <div className="astra-overview-policies">
        <OverviewPolicy<TaskGoal>
          title="Goal management"
          singular="goal"
          description="Final, verifiable goals. Adding or editing notifies the planner and revives the task; deleting notifies it without reviving."
          resource={data.goals}
          fields={goalFields}
          defaults={{ text: "", vulnclass: "" }}
          values={(row) => ({ text: row.text, vulnclass: row.vulnclass || "" })}
          save={(v, row) =>
            row
              ? api.updateGoal(id, row.id, v.text.trim(), v.vulnclass.trim() || undefined)
              : api.addGoal(id, v.text.trim(), v.vulnclass.trim() || undefined)
          }
          remove={(row) => api.deleteGoal(id, row.id)}
          render={(row) => (
            <>
              <StatusBadge domain="goal" value={row.state} />
              <p>{row.text}</p>
              {row.vulnclass && <code>{row.vulnclass}</code>}
            </>
          )}
        />
        <OverviewPolicy<TaskConstraint>
          title="Operation constraints"
          singular="constraint"
          description="Allow or deny operations in the next planning round. Changes retain the planner/worker injection settings and do not interrupt current work."
          resource={data.constraints}
          fields={constraintFields}
          defaults={{ kind: "deny", text: "" }}
          values={(row) => ({ kind: row.kind, text: row.text })}
          save={(v, row) =>
            row
              ? api.updateConstraint(id, row.id, v.text.trim(), v.kind as TaskConstraint["kind"])
              : api.addConstraint(id, v.text.trim(), v.kind as TaskConstraint["kind"])
          }
          remove={(row) => api.deleteConstraint(id, row.id)}
          render={(row) => (
            <>
              <span className="astra-overview-tag" data-policy={row.kind}>
                {row.kind === "allow" ? "Allow" : "Deny"}
              </span>
              <p>{row.text}</p>
            </>
          )}
        />
        <OverviewPolicy<TaskScopeRow>
          title="Test scope"
          singular="scope"
          description="Testing authority and the coverage denominator. Inherited scope stays with its source task. Asset discovery alone does not extend this boundary."
          resource={data.scope}
          fields={scopeFields}
          defaults={{ kind: "root_domain", value: "" }}
          values={(row) => ({ kind: row.kind, value: overviewScopeText(row) })}
          save={(v) => api.addTaskScope(id, v.kind, v.value.trim())}
          remove={(row) => api.deleteTaskScope(id, row.id)}
          removable={(row) => String(row.task_id) === id}
          render={(row) => (
            <>
              <span className="astra-overview-tag">{scopeName(row)}</span>
              <p className="astra-overview-mono">{overviewScopeText(row)}</p>
              <small>{{ auto: "Auto", agent: "Agent", manual: "Manual" }[row.source]}</small>
            </>
          )}
        />
        <OverviewPolicy<AssetInterceptRule>
          title="Task-level asset block / allow"
          singular="rule"
          description="Applies to this task only: block rules first, then allow rules. These entries remain separate from the global rules."
          resource={data.rules}
          fields={ruleFields}
          defaults={{ action: "block", kind: "fuzzy_domain", pattern: "", note: "" }}
          values={(row) => ({ action: row.action ?? "block", kind: row.kind, pattern: row.pattern, note: row.note })}
          save={(v, row) =>
            row
              ? api.updateTaskInterceptRule(id, row.id, ruleInput(v, row))
              : api.createTaskInterceptRule(id, ruleInput(v))
          }
          remove={(row) => api.deleteTaskInterceptRule(id, row.id)}
          toggle={{
            checked: (row) => row.enabled,
            write: (row) => api.toggleTaskInterceptRule(id, row.id, !row.enabled),
            accepted: (row) => ({ ...row, enabled: !row.enabled }),
          }}
          render={(row) => (
            <>
              <span className="astra-overview-tag" data-policy={row.action || "block"}>
                {row.action === "allow" ? "Allow" : "Block"}
              </span>
              <small>{overviewRuleKinds.find(([kind]) => kind === row.kind)?.[1] || row.kind}</small>
              <p className="astra-overview-mono">{row.pattern}</p>
              {row.note && <small>{row.note}</small>}
            </>
          )}
        />
      </div>
      <div className="astra-overview-work">
        <Panel title="Asset test coverage" icon="coverage">
          <OverviewNotice error={data.coverage.error} retry={() => void data.coverage.reload(true)} />
          {coverage?.enabled && coverage.scope_rows > 0 ? (
            <>
              <div className="astra-overview-coverage">
                <strong>{coverage.pct == null ? "—" : `${Math.round(coverage.pct * 100)}%`}</strong>
                <span>
                  Tested {coverage.tested} / In scope {coverage.denominator}
                </span>
              </div>
              {coverage.pct != null && (
                <progress aria-label="Asset test coverage" max={100} value={Math.round(coverage.pct * 100)} />
              )}
              <div className="astra-overview-type-counts">
                {coverage.by_type.map((row) => (
                  <span key={row.type}>
                    {row.type}
                    <strong>
                      {row.tested}/{row.total}
                    </strong>
                  </span>
                ))}
              </div>
              <p className="astra-overview-description">
                Rough estimate for reference. Testing status remains evidence based.
              </p>
            </>
          ) : (
            <p className="astra-overview-empty">
              {coverage
                ? coverage.enabled
                  ? "Add test scope to establish a coverage denominator."
                  : "Asset coverage is disabled for this task."
                : "Coverage data has not loaded."}
            </p>
          )}
        </Panel>
        <Panel title="LLM Token Usage" icon="token">
          <OverviewNotice error={data.tokens.error} retry={() => void data.tokens.reload(true)} />
          <div className="astra-overview-token-totals">
            {(
              [
                ["Input", usage.input],
                ["Output", usage.output],
                ["Cache read", usage.cacheRead],
                ["Cache write", usage.cacheWrite],
                ["Calls", usage.calls],
              ] as [string, number][]
            ).map(([name, count]) => (
              <div key={name}>
                <span>{name}</span>
                <strong title={String(count)}>{overviewCount(count)}</strong>
              </div>
            ))}
            <div>
              <span>Cache hit rate</span>
              <strong>{overviewCacheRate(usage.cacheRead, usage.input)}</strong>
            </div>
          </div>
          {!models.length && <p className="astra-overview-empty">No LLM usage yet. Records may still be arriving.</p>}
        </Panel>
      </div>
      {!!models.length && (
        <Panel title="Usage by model" icon="llm">
          <div className="astra-overview-table">
            <table>
              <thead>
                <tr>
                  {["Model", "Calls", "Input", "Output", "Cache read", "Cache write", "Hit rate"].map((label) => (
                    <th key={label}>{label}</th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {models.map((row) => (
                  <tr key={row.model}>
                    <th scope="row">{row.model}</th>
                    {[
                      row.calls,
                      row.input_tokens,
                      row.output_tokens,
                      row.cache_read_tokens,
                      row.cache_write_tokens,
                    ].map((count, i) => (
                      <td key={i} title={String(count)}>
                        {overviewCount(count)}
                      </td>
                    ))}
                    <td>{overviewCacheRate(row.cache_read_tokens, row.input_tokens)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Panel>
      )}
      {!!work.blocked.length && (
        <Panel
          title="Blocked / errored intents"
          icon="blocked"
          className="astra-overview-blocked"
          action={
            <button type="button" disabled={data.main.busy} onClick={() => void rerun()}>
              <UroIcon name="replay" />
              Rerun all blocked
            </button>
          }
        >
          <p className="astra-overview-description">
            Reruns preserve graph evidence. A paused or terminal task can revive when the server accepts the request.
          </p>
          <OverviewNotice error={data.main.writeError} retry={() => void data.main.reload(true)} />
          {work.blocked.slice(0, blockedLimit).map((row) => (
            <article key={row.id} className="astra-overview-work-row">
              <StatusBadge domain="intent" value={row.state} />
              <p>{row.payload}</p>
              <button
                type="button"
                disabled={data.main.busy}
                aria-label={`Rerun intent ${row.id}`}
                onClick={() => void rerun(row.id)}
              >
                <UroIcon name="replay" />
                Rerun
              </button>
            </article>
          ))}
          {work.blocked.length > blockedLimit && (
            <button type="button" className="astra-overview-more" onClick={() => showBlocked((n) => n + 20)}>
              Show {Math.min(20, work.blocked.length - blockedLimit)} more · {work.blocked.length - blockedLimit}{" "}
              remaining
            </button>
          )}
        </Panel>
      )}
    </div>
  );
}

export function TaskOverviewRoute({ taskId }: { taskId: string }) {
  return <TaskOverview key={taskId} id={taskId} />;
}
