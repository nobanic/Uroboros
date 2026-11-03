"use client";
import { Suspense, useEffect, useRef, useState, type ComponentType } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import { toast } from "sonner";
import { ThemeSwitcher } from "@/app/(main)/_components/sidebar/theme-switcher";
import { ArchiveIntelligenceTab } from "@/app/(main)/function/tasks/detail/_tabs/archive-intelligence-tab";
import { AssetsTab } from "@/app/(main)/function/tasks/detail/_tabs/assets-tab";
import { BroadcastTab } from "@/app/(main)/function/tasks/detail/_tabs/broadcast-tab";
import { CoverageGraphTab } from "@/app/(main)/function/tasks/detail/_tabs/coverage-graph-tab";
import { FindingsTab } from "@/app/(main)/function/tasks/detail/_tabs/findings-tab";
import { GraphTab } from "@/app/(main)/function/tasks/detail/_tabs/graph-tab";
import { InterceptTab } from "@/app/(main)/function/tasks/detail/_tabs/intercept-tab";
import { OverviewTab } from "@/app/(main)/function/tasks/detail/_tabs/overview-tab";
import { ReportTab } from "@/app/(main)/function/tasks/detail/_tabs/report-tab";
import { RetestsTab } from "@/app/(main)/function/tasks/detail/_tabs/retests-tab";
import { StatusBadge } from "@/components/status-badge";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { UroIcon, type UroIconName } from "@/components/uroboros-icons";
import { api } from "@/lib/api";
import { EditorDialog } from "./editor-dialog";
import { SessionWorkspace } from "./session-workspace";
import { taskControls } from "./task-document";
import { TaskProfileControl } from "./task-profile-control";
import { useTaskFrame } from "./use-task-frame";
import "./task.css";

const surfaces: { key: string; label: string; icon: UroIconName; view?: ComponentType<{ taskId: string }> }[] = [
  { key: "sessions", label: "Sessions", icon: "agents", view: SessionWorkspace },
  { key: "overview", label: "Overview", icon: "mission", view: OverviewTab },
  { key: "graph", label: "Exploration graph", icon: "topology", view: GraphTab },
  { key: "broadcast", label: "Broadcast", icon: "notifications", view: BroadcastTab },
  { key: "findings", label: "Findings", icon: "findings", view: FindingsTab },
  { key: "retests", label: "Retests", icon: "replay", view: RetestsTab },
  { key: "assets", label: "Test assets", icon: "assets", view: AssetsTab },
  { key: "archive-intelligence", label: "External intelligence", icon: "workspace", view: ArchiveIntelligenceTab },
  { key: "coverage", label: "Coverage graph", icon: "coverage" },
  { key: "intercept", label: "Interception approvals", icon: "approvals", view: InterceptTab },
  { key: "report", label: "Report", icon: "records", view: ReportTab },
];

function TaskWorkspace({ id }: { id: string }) {
  const frame = useTaskFrame(id),
    router = useRouter();
  const [surface, select] = useState("sessions"),
    [compact, collapse] = useState(false),
    [archive, confirm] = useState(false),
    [action, pending] = useState(""),
    [error, fail] = useState("");
  const alive = useRef(true),
    lock = useRef(false),
    navigation = useRef<HTMLDivElement>(null);
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  const task = frame.task,
    state = task ? taskControls(task) : undefined;
  useEffect(() => {
    const list = navigation.current;
    if (!list) return;
    const reveal = () => {
      if (!window.matchMedia("(max-width: 1100px)").matches) return;
      const selected = list.querySelector('[data-state="active"]');
      if (!selected) return;
      const bounds = list.getBoundingClientRect(),
        tab = selected.getBoundingClientRect();
      if (tab.left < bounds.left) list.scrollLeft += tab.left - bounds.left;
      else if (tab.right > bounds.right) list.scrollLeft += tab.right - bounds.right;
    };
    reveal();
    const observer = new ResizeObserver(reveal);
    observer.observe(list);
    return () => observer.disconnect();
  }, [surface, !!task]);
  async function control(kind: "pause" | "archive") {
    if (!task || !state || lock.current || (kind === "pause" ? state.terminal : !state.archive)) return;
    lock.current = true;
    pending(kind);
    fail("");
    try {
      if (kind === "archive") {
        await api.archiveTask(task.id);
        if (alive.current) {
          toast.success("Task added to the archive queue");
          router.push("/function/tasks");
        }
      } else {
        const next = !state.paused;
        await api.controlTask(task.id, next ? "pause" : "resume");
        if (alive.current) {
          frame.updatePause(next);
          toast.success(next ? "Exploration paused" : "Exploration resumed");
          void frame.load(true);
        }
      }
    } catch (error) {
      if (alive.current) fail(`${kind === "archive" ? "Archive" : "Operation"} failed: ${(error as Error).message}`);
    } finally {
      lock.current = false;
      if (alive.current) pending("");
    }
  }
  if (!task || !state)
    return (
      <section className="astra-task-empty">
        <UroIcon name="mission" />
        <h1>{id ? (frame.error ? "Unable to load this operation" : "Loading operation…") : "Choose an operation"}</h1>
        <p role={frame.error ? "alert" : undefined}>
          {frame.error || (id ? "Loading task state and live metrics." : "The task address does not include an ID.")}
        </p>
        <div>
          {id && <button onClick={() => void frame.load(true)}>Retry task</button>}
          <Link href="/function/tasks">Back to operations</Link>
        </div>
      </section>
    );
  const active = surfaces.find((s) => s.key === surface) || surfaces[0];
  const View = active.view;
  return (
    <Tabs
      value={surface}
      onValueChange={select}
      orientation="vertical"
      className="astra-task-workspace"
      data-task={id}
      data-nav-compact={compact}
    >
      <header className="astra-task-command">
        <div className="astra-task-identity">
          <SidebarTrigger />
          <Link href="/function/tasks" aria-label="Back to operations">
            <UroIcon name="chevron" className="astra-task-back" />
          </Link>
          <span className="astra-task-emblem">
            <UroIcon name="route" />
          </span>
          <div>
            <small>Uroboros · operation #{task.id}</small>
            <h1 title={task.name || task.description}>{task.name || task.description}</h1>
            <p title={task.goal}>{task.goal}</p>
          </div>
        </div>
        <div className="astra-task-actions">
          <ThemeSwitcher />
          <button aria-label="Refresh operation" onClick={() => void frame.load(true)}>
            <UroIcon name="replay" />
          </button>
          <TaskProfileControl task={task} profiles={frame.profiles} updated={() => void frame.load(true)} />
          <StatusBadge
            domain={state.terminal ? "task" : "engine"}
            value={state.terminal ? task.status : state.engine}
            dot
          />
          <button
            aria-label={state.archive ? "Archive task" : state.archiveReason}
            title={state.archive ? "Archive task" : state.archiveReason}
            disabled={!state.archive || !!action}
            onClick={() => {
              fail("");
              confirm(true);
            }}
          >
            <UroIcon name="workspace" />
          </button>
          <button
            className={state.paused ? "is-primary" : ""}
            disabled={state.terminal || !!action}
            onClick={() => void control("pause")}
          >
            <UroIcon name={state.terminal ? "shield" : state.paused ? "replay" : "blocked"} />
            {action === "pause" ? "Updating…" : state.label}
          </button>
        </div>
        <dl className="astra-task-metrics">
          {[
            ["Goals", `${task.goals_met || 0} / ${task.goals_total || 0}`],
            ["Progress", `${state.progress}%`],
            ["In flight", task.in_flight || 0],
            ["Findings", state.findings],
          ].map(([name, value]) => (
            <div key={name}>
              <dt>{name}</dt>
              <dd>{value}</dd>
            </div>
          ))}
          <div className="astra-task-progress">
            <span style={{ width: `${Math.min(100, Math.max(0, state.progress))}%` }} />
          </div>
        </dl>
      </header>
      {(frame.error || frame.notices.length > 0 || (error && !archive)) && (
        <div className="astra-task-read-error" role="alert">
          <span>{[frame.error, ...frame.notices, error && !archive ? error : ""].filter(Boolean).join(" · ")}</span>
          <button
            onClick={() => {
              fail("");
              void frame.load(true);
            }}
          >
            Retry task
          </button>
        </div>
      )}
      <div className="astra-task-body">
        <aside className="astra-task-navigation">
          <header>
            <span>Mission surfaces</span>
            <button
              aria-label="Toggle task navigation labels"
              aria-expanded={!compact}
              onClick={() => collapse(!compact)}
            >
              <UroIcon name="chevron" />
            </button>
          </header>
          <TabsList ref={navigation}>
            {surfaces.map((item, index) => (
              <TabsTrigger key={item.key} value={item.key} title={item.label} aria-label={item.label}>
                <UroIcon name={item.icon} />
                <span>{item.label}</span>
                {item.key === "intercept" && frame.pending > 0 ? (
                  <em>{frame.pending > 99 ? "99+" : frame.pending}</em>
                ) : (
                  <small>{String(index + 1).padStart(2, "0")}</small>
                )}
              </TabsTrigger>
            ))}
          </TabsList>
          <footer>
            <UroIcon name="shield" />
            <p>
              One operation.
              <br />
              Shared scope and evidence.
            </p>
          </footer>
        </aside>
        <main className="astra-task-stage">
          <header>
            <div>
              <UroIcon name={active.icon} />
              <span>{active.label}</span>
            </div>
            <button onClick={() => select(surface === "graph" ? "sessions" : "graph")}>
              <UroIcon name={surface === "graph" ? "agents" : "topology"} />
              {surface === "graph" ? "Return to sessions" : "Open exploration graph"}
            </button>
          </header>
          <TabsContent key={surface} value={surface}>
            {View ? (
              <View key={id} taskId={id} />
            ) : (
              <CoverageGraphTab key={id} taskId={id} coverageEnabled={task.coverage_enabled !== false} />
            )}
          </TabsContent>
        </main>
      </div>
      {archive && (
        <EditorDialog
          title={`Archive task #${task.id}?`}
          eyebrow="Operation archive"
          busy={!!action}
          close={() => {
            confirm(false);
            fail("");
          }}
        >
          <p>
            The task graph, related records, exclusive assets and traffic, working files, and LLM history will be
            compressed into cold storage. Once archiving completes, it can be restored from the "Archived" view of the
            task list.
          </p>
          {error && (
            <p role="alert" className="astra-task-warning">
              {error}
            </p>
          )}
          <footer className="astra-task-dialog-actions">
            <button
              disabled={!!action}
              onClick={() => {
                confirm(false);
                fail("");
              }}
            >
              Cancel
            </button>
            <button
              className="is-primary"
              disabled={!state.archive || !!action}
              onClick={() => void control("archive")}
            >
              {action === "archive" ? "Queuing…" : "Confirm archive"}
            </button>
          </footer>
        </EditorDialog>
      )}
    </Tabs>
  );
}
function TaskAddress() {
  const params = useSearchParams(),
    id = params.get("id") || "";
  return <TaskWorkspace key={id} id={id} />;
}
export function TaskDetailRoute() {
  return (
    <Suspense fallback={null}>
      <TaskAddress />
    </Suspense>
  );
}
