"use client";

import * as React from "react";

import Link from "next/link";

import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import type {
  Activity,
  Agent,
  Finding,
  InterceptPending,
  LLMProfile,
  MCPServer,
  SkillItem,
  Task,
  TokenTotal,
  Tool,
  TrafficExchange,
} from "@/lib/types";

function formatRelative(value?: string | number): string {
  if (!value) return "no signal";
  const timestamp = typeof value === "number" ? value * 1000 : Date.parse(value);
  const seconds = Math.max(0, Math.floor((Date.now() - timestamp) / 1000));
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function formatTokens(value: number): string {
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}k`;
  return String(value);
}

function taskLabel(task: Task): string {
  return task.name?.trim() || task.description.trim() || `Operation ${task.id}`;
}

function activityLabel(item: Activity): string {
  if (item.kind === "tool_use") return item.tool ?? "tool invocation";
  if (item.kind === "tool_result") return item.is_error ? "tool failure" : "tool result";
  return item.kind.replaceAll("_", " ");
}

function operationProgress(task: Task): number | null {
  if (task.status === "done") return 100;
  if (!task.goals_total) return null;
  return Math.max(0, Math.min(100, Math.round(((task.goals_met ?? 0) / task.goals_total) * 100)));
}

function PanelHeading({
  icon,
  eyebrow,
  title,
  action,
}: {
  icon: UroIconName;
  eyebrow: string;
  title: string;
  action?: React.ReactNode;
}) {
  return (
    <header className="uro-mc-panel-heading">
      <div className="uro-mc-panel-heading__identity">
        <span className="uro-mc-panel-heading__icon">
          <UroIcon name={icon} />
        </span>
        <span>
          <span className="uro-mc-panel-heading__eyebrow">{eyebrow}</span>
          <strong>{title}</strong>
        </span>
      </div>
      {action}
    </header>
  );
}

const severityOrder: Record<string, number> = { critical: 0, high: 1, medium: 2, low: 3 };
const operationOrder: Record<string, number> = {
  running: 0,
  paused: 1,
  queued: 2,
  created: 3,
  failed: 4,
  timeout: 5,
  done: 6,
};

export default function MissionControlPage() {
  const [tasks, setTasks] = React.useState<Task[]>([]);
  const [findings, setFindings] = React.useState<Finding[]>([]);
  const [pending, setPending] = React.useState<InterceptPending[]>([]);
  const [activity, setActivity] = React.useState<Activity[]>([]);
  const [tokens, setTokens] = React.useState<TokenTotal | null>(null);
  const [assetCounts, setAssetCounts] = React.useState<Record<string, number>>({});
  const [traffic, setTraffic] = React.useState<TrafficExchange[]>([]);
  const [agents, setAgents] = React.useState<Agent[]>([]);
  const [mcpServers, setMcpServers] = React.useState<MCPServer[]>([]);
  const [skills, setSkills] = React.useState<SkillItem[]>([]);
  const [tools, setTools] = React.useState<Tool[]>([]);
  const [profiles, setProfiles] = React.useState<LLMProfile[]>([]);
  const [connected, setConnected] = React.useState(false);

  React.useEffect(() => {
    let alive = true;
    let loading = false;
    const load = async () => {
      if (loading) return;
      loading = true;
      try {
        const response = await api.tasks();
        if (alive) {
          setTasks(response.tasks);
          setConnected(true);
        }
      } catch {
        if (alive) setConnected(false);
      } finally {
        loading = false;
      }
    };
    void load();
    const timer = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, []);

  React.useEffect(() => {
    let alive = true;
    let loading = false;
    const load = async () => {
      if (loading) return;
      loading = true;
      try {
        const [nextFindings, nextPending, nextActivity, nextTokens] = await Promise.all([
          api.findings(),
          api.interceptPending(),
          api.activity(undefined, { limit: 36 }),
          api.tokenStats(),
        ]);
        if (!alive) return;
        setFindings(nextFindings);
        setPending(nextPending);
        setActivity(nextActivity.items);
        setTokens(nextTokens.total ?? null);
      } catch {
        // Keep the last coherent telemetry frame and retry on the next pulse.
      } finally {
        loading = false;
      }
    };
    void load();
    const timer = setInterval(load, 5000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, []);

  React.useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const [nextTraffic, nextAssets, nextAgents, nextMcp, nextSkills, nextTools, nextProfiles] = await Promise.all([
          api.traffic(0, 80),
          api.assetCounts(),
          api.agents(),
          api.mcpServers(),
          api.skills(),
          api.tools(),
          api.llmProfiles(),
        ]);
        if (!alive) return;
        setTraffic(nextTraffic.exchanges ?? []);
        setAssetCounts(nextAssets ?? {});
        setAgents(nextAgents);
        setMcpServers(nextMcp);
        setSkills(nextSkills);
        setTools(nextTools);
        setProfiles(nextProfiles);
      } catch {
        // Slow-changing system inventory can remain stale until the next refresh.
      }
    };
    void load();
    const timer = setInterval(load, 15000);
    return () => {
      alive = false;
      clearInterval(timer);
    };
  }, []);

  const operations = React.useMemo(
    () =>
      [...tasks]
        .sort((a, b) => {
          const stateDelta = (operationOrder[a.status] ?? 99) - (operationOrder[b.status] ?? 99);
          if (stateDelta !== 0) return stateDelta;
          return (b.last_activity_unix ?? b.created_unix ?? 0) - (a.last_activity_unix ?? a.created_unix ?? 0);
        })
        .slice(0, 4),
    [tasks],
  );

  const recentFindings = React.useMemo(
    () =>
      [...findings]
        .sort((a, b) => {
          const severityDelta = (severityOrder[a.severity] ?? 9) - (severityOrder[b.severity] ?? 9);
          return severityDelta || Date.parse(b.ts) - Date.parse(a.ts);
        })
        .slice(0, 5),
    [findings],
  );

  const recentActivity = React.useMemo(
    () =>
      [...activity]
        .filter((item) => item.kind !== "usage")
        .sort((a, b) => b.seq - a.seq)
        .slice(0, 7),
    [activity],
  );

  const assetBreakdown = React.useMemo(
    () =>
      Object.entries(assetCounts)
        .filter(([, count]) => count > 0)
        .sort(([, a], [, b]) => b - a)
        .slice(0, 7),
    [assetCounts],
  );

  const trafficBreakdown = React.useMemo(() => {
    const buckets = [
      { code: "2xx", count: 0 },
      { code: "3xx", count: 0 },
      { code: "4xx", count: 0 },
      { code: "5xx", count: 0 },
    ];
    for (const exchange of traffic) {
      const index = Math.max(0, Math.min(3, Math.floor(exchange.status / 100) - 2));
      buckets[index].count++;
    }
    return buckets;
  }, [traffic]);

  const findingsBySeverity = React.useMemo(() => {
    const value = { critical: 0, high: 0, medium: 0, low: 0 };
    for (const finding of findings) {
      if (finding.severity in value) value[finding.severity as keyof typeof value]++;
    }
    return value;
  }, [findings]);

  const activeCount = tasks.filter((task) => task.status === "running" || task.active).length;
  const totalAssets = Object.values(assetCounts).reduce((sum, count) => sum + count, 0);
  const enabledAgents = agents.filter((agent) => agent.enabled).length;
  const enabledMcp = mcpServers.filter((server) => server.enabled).length;
  const enabledTools = tools.filter((tool) => tool.enabled).length;
  const activeProfile = profiles.find((profile) => profile.is_default);
  const findingTotal = findings.length;
  // This is a risk index, not an assumed security grade. A fresh instance has
  // no evaluated evidence, so it must start at 0 instead of implying a 98/100
  // posture before any operation has run.
  const postureValue = Math.min(
    100,
    findingsBySeverity.critical * 24 +
      findingsBySeverity.high * 12 +
      findingsBySeverity.medium * 5 +
      findingsBySeverity.low * 2 +
      pending.length * 4,
  );
  const severityScale = Math.max(1, findingTotal);
  const criticalEnd = (findingsBySeverity.critical / severityScale) * 100;
  const highEnd = criticalEnd + (findingsBySeverity.high / severityScale) * 100;
  const mediumEnd = highEnd + (findingsBySeverity.medium / severityScale) * 100;
  const lowEnd = mediumEnd + (findingsBySeverity.low / severityScale) * 100;
  const postureRingBackground = findingTotal
    ? `conic-gradient(#ff596b 0 ${criticalEnd}%, #e99062 ${criticalEnd}% ${highEnd}%, #d7af57 ${highEnd}% ${mediumEnd}%, #7093eb ${mediumEnd}% ${lowEnd}%, rgba(116, 140, 172, 0.12) ${lowEnd}% 100%)`
    : "conic-gradient(rgba(116, 140, 172, 0.12) 0 100%)";
  let postureLevel = "stable";
  if (findingsBySeverity.critical > 0 || postureValue >= 70) postureLevel = "critical";
  else if (postureValue > 0) postureLevel = "elevated";
  const totalTokenCount = tokens
    ? tokens.input_tokens + tokens.output_tokens + tokens.cache_read_tokens + tokens.cache_write_tokens
    : 0;
  const tokenSegments = [
    { key: "input", label: "Input", value: tokens?.input_tokens ?? 0 },
    { key: "output", label: "Output", value: tokens?.output_tokens ?? 0 },
    { key: "cache", label: "Cache", value: (tokens?.cache_read_tokens ?? 0) + (tokens?.cache_write_tokens ?? 0) },
  ];
  const assetMax = Math.max(1, ...assetBreakdown.map(([, count]) => count));
  const trafficTotal = Math.max(
    1,
    trafficBreakdown.reduce((sum, bucket) => sum + bucket.count, 0),
  );

  return (
    <main className="uro-mc" aria-label="Mission Control">
      <section className="uro-mc-hero">
        <div className="uro-mc-hero__scan" aria-hidden="true" />
        <div className="uro-mc-hero__copy">
          <span className="uro-kicker">Autonomous security operations</span>
          <h1>Mission Control</h1>
          <p>One operational frame for live missions, attack-surface movement, agent activity and evidence.</p>
        </div>
        <div className="uro-mc-hero__actions">
          <span className={`uro-mc-live-state ${connected ? "is-live" : "is-offline"}`}>
            <i /> {connected ? "Telemetry linked" : "Reconnecting"}
          </span>
          <Link className="uro-action-primary" href="/function/tasks">
            <UroIcon name="plus" />
            New operation
          </Link>
        </div>
        <section className="uro-mc-hero__signals" aria-label="Live mission signals">
          <div>
            <span>Active vectors</span>
            <strong>{activeCount.toString().padStart(2, "0")}</strong>
            <small>{tasks.length} operations in ledger</small>
          </div>
          <div className={findingsBySeverity.critical ? "is-danger" : undefined}>
            <span>Critical evidence</span>
            <strong>{findingsBySeverity.critical.toString().padStart(2, "0")}</strong>
            <small>{findingsBySeverity.high} high-priority signals</small>
          </div>
          <div className={pending.length ? "is-warning" : undefined}>
            <span>Human gates</span>
            <strong>{pending.length.toString().padStart(2, "0")}</strong>
            <small>{pending.length ? "review required" : "no blocked actions"}</small>
          </div>
          <div>
            <span>Mapped surface</span>
            <strong>{formatTokens(totalAssets)}</strong>
            <small>{assetBreakdown.length} active asset classes</small>
          </div>
        </section>
      </section>

      <div className="uro-mc-layout">
        <section className="uro-mc-panel uro-mc-operations">
          <PanelHeading
            icon="route"
            eyebrow="Command queue"
            title="Live operations"
            action={
              <Link className="uro-inline-link" href="/function/tasks">
                Open ledger <UroIcon name="arrow-up-right" />
              </Link>
            }
          />
          <div className="uro-mc-operation-list">
            {operations.length === 0 ? (
              <div className="uro-mc-empty">
                <UroIcon name="tasks" />
                <span>No operations have entered the queue.</span>
              </div>
            ) : (
              operations.map((task, index) => {
                const progress = operationProgress(task);
                const findingCount = Object.values(task.findings ?? {}).reduce((sum, count) => sum + count, 0);
                return (
                  <Link
                    className={`uro-mc-operation is-${task.status}`}
                    href={`/function/tasks/detail?id=${encodeURIComponent(task.id)}`}
                    key={task.id}
                  >
                    <span className="uro-mc-operation__index">{String(index + 1).padStart(2, "0")}</span>
                    <span className="uro-mc-operation__body">
                      <span className="uro-mc-operation__titleline">
                        <strong>{taskLabel(task)}</strong>
                        <em>{task.status}</em>
                      </span>
                      <span className="uro-mc-operation__goal">{task.goal || "Awaiting objective definition"}</span>
                      <span className="uro-mc-operation__track">
                        <i
                          className={progress === null && task.status === "running" ? "is-indeterminate" : undefined}
                          style={progress === null ? undefined : { width: `${progress}%` }}
                        />
                      </span>
                    </span>
                    <span className="uro-mc-operation__telemetry">
                      <span>
                        <UroIcon name="pulse" /> {task.in_flight ?? 0} in flight
                      </span>
                      <span>
                        <UroIcon name="findings" /> {findingCount} findings
                      </span>
                      <span>
                        <UroIcon name="clock" /> {formatRelative(task.last_activity_unix ?? task.created_unix)}
                      </span>
                    </span>
                    <UroIcon className="uro-mc-operation__arrow" name="arrow-up-right" />
                  </Link>
                );
              })
            )}
          </div>
        </section>

        <aside className={`uro-mc-panel uro-mc-posture is-${postureLevel}`}>
          <PanelHeading icon="shield" eyebrow="Risk synthesis" title="Operational posture" />
          <div className="uro-mc-posture__visual">
            <div className="uro-mc-posture__ring" style={{ background: postureRingBackground }}>
              <div className="uro-mc-posture__orbit">
                <i />
                <i />
                <i />
              </div>
              <div className="uro-mc-posture__score">
                <strong>{postureValue}</strong>
              </div>
            </div>
            <div className="uro-mc-posture__caption">
              <span>{postureLevel} posture · risk index</span>
              <p>Zero means no confirmed risk signal; findings and pending human gates raise the index.</p>
            </div>
          </div>
          <div className="uro-mc-posture__matrix">
            <span>
              <i className="critical" /> Critical <strong>{findingsBySeverity.critical}</strong>
            </span>
            <span>
              <i className="high" /> High <strong>{findingsBySeverity.high}</strong>
            </span>
            <span>
              <i className="medium" /> Medium <strong>{findingsBySeverity.medium}</strong>
            </span>
            <span>
              <i className="low" /> Low <strong>{findingsBySeverity.low}</strong>
            </span>
          </div>
        </aside>

        <section className="uro-mc-panel uro-mc-evidence">
          <PanelHeading
            icon="findings"
            eyebrow="Validated signals"
            title="Evidence pulse"
            action={
              <Link className="uro-inline-link" href="/function/findings">
                All findings <UroIcon name="arrow-up-right" />
              </Link>
            }
          />
          <div className="uro-mc-evidence-list">
            {recentFindings.length === 0 ? (
              <div className="uro-mc-empty">
                <UroIcon name="findings" />
                <span>No evidence recorded yet.</span>
              </div>
            ) : (
              recentFindings.map((finding) => (
                <Link className="uro-mc-finding" href="/function/findings" key={finding.id}>
                  <span className={`uro-mc-finding__severity is-${finding.severity}`}>
                    <UroIcon name="findings" />
                  </span>
                  <span className="uro-mc-finding__copy">
                    <strong>{finding.name?.trim() || finding.vulnclass}</strong>
                    <small>{finding.assets?.[0]?.label || finding.task_description || "Unbound evidence"}</small>
                  </span>
                  <span className={`uro-mc-finding__tag is-${finding.severity}`}>{finding.severity}</span>
                  <time>{formatRelative(finding.ts)}</time>
                </Link>
              ))
            )}
          </div>
        </section>

        <section className="uro-mc-panel uro-mc-activity">
          <PanelHeading icon="pulse" eyebrow="Execution stream" title="Agent activity" />
          <div className="uro-mc-activity-rail">
            {recentActivity.length === 0 ? (
              <div className="uro-mc-empty">
                <UroIcon name="pulse" />
                <span>Waiting for agent telemetry.</span>
              </div>
            ) : (
              recentActivity.map((item, index) => (
                <article className={item.is_error ? "is-error" : undefined} key={`${item.seq}-${item.worker}`}>
                  <span className="uro-mc-activity-rail__node">
                    <i />
                  </span>
                  <div>
                    <span className="uro-mc-activity-rail__meta">
                      <strong>{item.worker}</strong>
                      <em>{activityLabel(item)}</em>
                      <time>{formatRelative(item.ts)}</time>
                    </span>
                    <p>{item.summary || "Runtime event received"}</p>
                  </div>
                  {index === 0 && <span className="uro-mc-activity-rail__live">live</span>}
                </article>
              ))
            )}
          </div>
        </section>

        <section className="uro-mc-panel uro-mc-surface">
          <PanelHeading
            icon="topology"
            eyebrow="Discovery field"
            title="Attack surface"
            action={
              <Link className="uro-inline-link" href="/function/assets">
                Inspect assets <UroIcon name="arrow-up-right" />
              </Link>
            }
          />
          <div className="uro-mc-surface__body">
            <div className="uro-mc-asset-map">
              <div className="uro-mc-asset-map__axis">
                <span>Asset class</span>
                <span>Observed entities</span>
              </div>
              {assetBreakdown.length === 0 ? (
                <div className="uro-mc-empty">
                  <UroIcon name="assets" />
                  <span>No attack surface mapped.</span>
                </div>
              ) : (
                assetBreakdown.map(([type, count], index) => (
                  <div className="uro-mc-asset-row" key={type}>
                    <span className="uro-mc-asset-row__label">
                      <i>{String(index + 1).padStart(2, "0")}</i>
                      {type.replaceAll("_", " ")}
                    </span>
                    <span className="uro-mc-asset-row__track">
                      <i style={{ width: `${Math.max(4, (count / assetMax) * 100)}%` }} />
                    </span>
                    <strong>{count}</strong>
                  </div>
                ))
              )}
            </div>
            <div className="uro-mc-traffic-map">
              <span className="uro-mc-traffic-map__label">HTTP response field</span>
              <div className="uro-mc-traffic-map__bar">
                {trafficBreakdown.map((bucket) => (
                  <i
                    className={`is-${bucket.code[0]}`}
                    key={bucket.code}
                    style={{ width: `${(bucket.count / trafficTotal) * 100}%` }}
                  />
                ))}
              </div>
              <div className="uro-mc-traffic-map__legend">
                {trafficBreakdown.map((bucket) => (
                  <span key={bucket.code}>
                    <i className={`is-${bucket.code[0]}`} />
                    {bucket.code}
                    <strong>{bucket.count}</strong>
                  </span>
                ))}
              </div>
              <Link href="/function/traffic">
                Open traffic stream <UroIcon name="arrow-up-right" />
              </Link>
            </div>
          </div>
        </section>

        <section className="uro-mc-panel uro-mc-mesh">
          <PanelHeading icon="mcp" eyebrow="Runtime fabric" title="System mesh" />
          <div className="uro-mc-mesh__profile">
            <span>
              <UroIcon name="llm" />
            </span>
            <div>
              <small>Active inference profile</small>
              <strong>{activeProfile?.name || "Not configured"}</strong>
              <em>{activeProfile?.model || "No model route"}</em>
            </div>
            <i className={activeProfile ? "is-up" : "is-down"} />
          </div>
          <div className="uro-mc-mesh__rows">
            <div>
              <span>
                <UroIcon name="agents" /> Agents
              </span>
              <strong>
                {enabledAgents}
                <small> / {agents.length}</small>
              </strong>
              <i style={{ width: `${agents.length ? (enabledAgents / agents.length) * 100 : 0}%` }} />
            </div>
            <div>
              <span>
                <UroIcon name="mcp" /> MCP links
              </span>
              <strong>
                {enabledMcp}
                <small> / {mcpServers.length}</small>
              </strong>
              <i style={{ width: `${mcpServers.length ? (enabledMcp / mcpServers.length) * 100 : 0}%` }} />
            </div>
            <div>
              <span>
                <UroIcon name="tools" /> Toolchain
              </span>
              <strong>
                {enabledTools}
                <small> / {tools.length}</small>
              </strong>
              <i style={{ width: `${tools.length ? (enabledTools / tools.length) * 100 : 0}%` }} />
            </div>
            <div>
              <span>
                <UroIcon name="skills" /> Skills
              </span>
              <strong>{skills.length}</strong>
              <i style={{ width: `${Math.min(100, skills.length * 8)}%` }} />
            </div>
          </div>
        </section>

        <section className="uro-mc-panel uro-mc-resources">
          <PanelHeading
            icon="token"
            eyebrow="Consumption ledger"
            title="Resource telemetry"
            action={
              <Link className="uro-inline-link" href="/control/llm-profiles">
                Model ledger <UroIcon name="arrow-up-right" />
              </Link>
            }
          />
          <div className="uro-mc-resources__total">
            <span>Total processed</span>
            <strong>{formatTokens(totalTokenCount)}</strong>
            <small>tokens across the current operation ledger</small>
          </div>
          <div className="uro-mc-resources__segments">
            {tokenSegments.map((segment) => (
              <div className={`is-${segment.key}`} key={segment.key}>
                <span>
                  <i />
                  {segment.label}
                </span>
                <strong>{formatTokens(segment.value)}</strong>
                <em>
                  <i style={{ width: `${totalTokenCount ? (segment.value / totalTokenCount) * 100 : 0}%` }} />
                </em>
              </div>
            ))}
          </div>
          <div className="uro-mc-resources__context">
            <span>
              <UroIcon name="records" /> {profiles.length} model profiles
            </span>
            <span>
              <UroIcon name="tasks" /> {tasks.length} tracked operations
            </span>
            <span>
              <UroIcon name="pulse" /> 5s telemetry pulse
            </span>
          </div>
        </section>
      </div>
    </main>
  );
}
