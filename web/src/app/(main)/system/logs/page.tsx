"use client";

import * as React from "react";

import {
  ActivityIcon,
  AlertTriangleIcon,
  ChevronRightIcon,
  CircleAlertIcon,
  Clock3Icon,
  CopyIcon,
  DatabaseIcon,
  HistoryIcon,
  PauseIcon,
  PlayIcon,
  RadioTowerIcon,
  SearchIcon,
  Trash2Icon,
} from "lucide-react";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { sseUrl } from "@/lib/api";
import { MOCK } from "@/lib/mock/enabled";
import type { LogLine } from "@/lib/types";
import { cn } from "@/lib/utils";

const MOCK_LOGS: LogLine[] = [
  {
    seq: 1,
    ts: "2026-07-26T03:55:00Z",
    level: "info",
    tag: "engine",
    text: "Uroboros v0.1.0 backend listening on :8787 (workers=3)",
  },
  {
    seq: 2,
    ts: "2026-07-26T03:55:01Z",
    level: "info",
    tag: "config",
    text: "LLM configured from DB: anthropic / claude-opus-4-8",
  },
  {
    seq: 3,
    ts: "2026-07-26T03:56:10Z",
    level: "info",
    tag: "planner",
    text: "task t-uroboros.lab-web: planning round 3, generated intent i-4",
  },
  {
    seq: 4,
    ts: "2026-07-26T03:57:00Z",
    level: "warn",
    tag: "guard",
    text: "block bash: target out of bounds, out.evil.example is not in scope",
  },
  {
    seq: 5,
    ts: "2026-07-26T03:57:30Z",
    level: "info",
    tag: "work#1",
    text: "report_finding: Default Credentials (high) persisted",
  },
  {
    seq: 6,
    ts: "2026-07-26T03:58:20Z",
    level: "error",
    tag: "work#3",
    text: "intercept: mysqldump matched a destructive rule, awaiting manual approval",
  },
];

function fmtTime(ts: string) {
  const date = new Date(ts);
  if (Number.isNaN(date.getTime())) return "";
  const pad = (value: number) => String(value).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
}

export default function LogsPage() {
  const [lines, setLines] = React.useState<LogLine[]>([]);
  const [query, setQuery] = React.useState("");
  const [level, setLevel] = React.useState<"all" | LogLine["level"]>("all");
  const [source, setSource] = React.useState("all");
  const [selectedSeq, setSelectedSeq] = React.useState<number | null>(MOCK ? 6 : null);
  const [paused, setPaused] = React.useState(false);
  const [loadingHistory, setLoadingHistory] = React.useState(false);
  const [hasMore, setHasMore] = React.useState(false);
  const bottom = React.useRef<HTMLDivElement>(null);
  const stick = React.useRef(true);
  const pausedRef = React.useRef(false);
  pausedRef.current = paused;

  const minDbId = React.useMemo(() => {
    let min = 0;
    for (const line of lines) if (line.db_id && (min === 0 || line.db_id < min)) min = line.db_id;
    return min;
  }, [lines]);

  React.useEffect(() => {
    if (MOCK) {
      setLines(MOCK_LOGS);
      return;
    }
    const stream = new EventSource(sseUrl("/api/logs/stream?since=0"));
    stream.onmessage = (event) => {
      if (pausedRef.current) return;
      try {
        const line = JSON.parse(event.data) as LogLine;
        setLines((previous) =>
          previous.some((item) => item.seq === line.seq) ? previous : [...previous, line].slice(-3000),
        );
      } catch {
        // Ignore malformed frames while keeping the stream alive.
      }
    };
    return () => stream.close();
  }, []);

  React.useEffect(() => {
    if (minDbId > 1) setHasMore(true);
  }, [minDbId]);

  async function loadOlderHistory() {
    if (loadingHistory) return;
    setLoadingHistory(true);
    try {
      const params = minDbId > 0 ? `?before=${minDbId}&limit=200` : "?limit=200";
      const response = await fetch(`/api/logs/history${params}`);
      if (!response.ok) return;
      const data = (await response.json()) as { items: LogLine[]; has_more: boolean };
      if (data.items?.length) {
        setLines((previous) => {
          const minSeq = previous.reduce((minimum, line) => Math.min(minimum, line.seq ?? 0), 0);
          const older = data.items.map((line, index) => ({ ...line, seq: minSeq - data.items.length + index }));
          const seen = new Set<number>();
          return [...older, ...previous]
            .filter((line) => {
              if (!line.db_id) return true;
              if (seen.has(line.db_id)) return false;
              seen.add(line.db_id);
              return true;
            })
            .slice(-5000);
        });
      }
      setHasMore(data.has_more ?? false);
    } finally {
      setLoadingHistory(false);
    }
  }

  const filtered = React.useMemo(() => {
    const needle = query.trim().toLowerCase();
    return lines.filter(
      (line) =>
        (level === "all" || line.level === level) &&
        (source === "all" || line.tag === source) &&
        (!needle || line.text.toLowerCase().includes(needle) || line.tag.toLowerCase().includes(needle)),
    );
  }, [level, lines, query, source]);

  // biome-ignore lint/correctness/useExhaustiveDependencies: A changed filtered stream should follow the tail when enabled.
  React.useEffect(() => {
    if (stick.current && !paused) bottom.current?.scrollIntoView();
  }, [filtered, paused]);

  const counts = React.useMemo(() => {
    let warn = 0;
    let error = 0;
    for (const line of lines) {
      if (line.level === "warn") warn += 1;
      else if (line.level === "error") error += 1;
    }
    return { warn, error, total: lines.length };
  }, [lines]);

  const sources = React.useMemo(() => {
    const sourceCounts = new Map<string, number>();
    for (const line of lines) {
      const tag = line.tag || "untagged";
      sourceCounts.set(tag, (sourceCounts.get(tag) ?? 0) + 1);
    }
    return [...sourceCounts.entries()].sort((a, b) => b[1] - a[1]);
  }, [lines]);

  const selected = React.useMemo(
    () => filtered.find((line) => line.seq === selectedSeq) ?? filtered.at(-1) ?? null,
    [filtered, selectedSeq],
  );

  return (
    <div className="uro-logs-console">
      <section className="uro-logs-commandbar">
        <div className="uro-logs-commandbar__identity">
          <span className={paused ? "is-paused" : "is-live"}>
            <RadioTowerIcon />
          </span>
          <div>
            <small>{paused ? "Stream held" : "Live signal"}</small>
            <strong>Backend telemetry uplink</strong>
            <p>
              {paused
                ? "New events are held while you inspect the buffer."
                : "Following planner, worker, database, and policy events."}
            </p>
          </div>
        </div>
        <dl className="uro-logs-commandbar__metrics">
          <div>
            <dt>Buffer</dt>
            <dd>
              {counts.total}
              <small>events</small>
            </dd>
          </div>
          <div>
            <dt>Sources</dt>
            <dd>
              {sources.length}
              <small>active</small>
            </dd>
          </div>
          <div className="is-warn">
            <dt>Warnings</dt>
            <dd>{counts.warn}</dd>
          </div>
          <div className="is-error">
            <dt>Errors</dt>
            <dd>{counts.error}</dd>
          </div>
        </dl>
        <div className="uro-logs-commandbar__actions">
          <Button size="sm" variant={paused ? "default" : "outline"} onClick={() => setPaused((value) => !value)}>
            {paused ? <PlayIcon /> : <PauseIcon />}
            {paused ? "Resume" : "Pause"}
          </Button>
          <Button
            size="icon-sm"
            variant="outline"
            aria-label="Clear local log buffer"
            onClick={() => {
              setLines([]);
              setSelectedSeq(null);
            }}
          >
            <Trash2Icon />
          </Button>
        </div>
      </section>

      <section className="uro-logs-workbench">
        <aside className="uro-logs-sources">
          <header>
            <DatabaseIcon />
            <span>Signal sources</span>
          </header>
          <nav>
            <button type="button" className={cn(source === "all" && "is-active")} onClick={() => setSource("all")}>
              <span>
                <ActivityIcon />
                All signals
              </span>
              <strong>{lines.length}</strong>
            </button>
            {sources.map(([tag, count]) => (
              <button
                key={tag}
                type="button"
                className={cn(source === tag && "is-active")}
                onClick={() => setSource(tag)}
              >
                <span>
                  <i />
                  {tag}
                </span>
                <strong>{count}</strong>
              </button>
            ))}
          </nav>
          <footer>
            <span>Severity gate</span>
            {(["all", "info", "warn", "error"] as const).map((value) => (
              <button
                key={value}
                type="button"
                className={cn(level === value && "is-active", `is-${value}`)}
                onClick={() => setLevel(value)}
              >
                <i />
                {value === "all" ? "Any level" : value}
              </button>
            ))}
          </footer>
        </aside>

        <div className="uro-logs-stream">
          <header>
            <div>
              <ActivityIcon />
              <span>Ordered event stream</span>
              <small>{filtered.length} visible</small>
            </div>
            {hasMore && (
              <Button size="sm" variant="ghost" disabled={loadingHistory} onClick={loadOlderHistory}>
                <HistoryIcon />
                {loadingHistory ? "Loading" : "Earlier"}
              </Button>
            )}
          </header>
          <div className="uro-logs-search">
            <SearchIcon />
            <Input
              placeholder="Filter message text or component tag"
              aria-label="Filter runtime events"
              value={query}
              onChange={(event) => setQuery(event.target.value)}
            />
            <kbd>/</kbd>
          </div>
          <div
            className="uro-logs-stream__list"
            onScroll={(event) => {
              const element = event.currentTarget;
              stick.current = element.scrollHeight - element.scrollTop - element.clientHeight < 40;
            }}
          >
            {filtered.length === 0 ? (
              <div className="uro-logs-stream__empty">
                <SearchIcon />
                <strong>No matching events</strong>
                <span>Broaden the source, severity, or text filter.</span>
              </div>
            ) : (
              filtered.map((line) => {
                const body =
                  line.tag && line.text.startsWith(`[${line.tag}]`)
                    ? line.text.slice(line.tag.length + 2).trimStart()
                    : line.text;
                return (
                  <button
                    key={line.seq}
                    type="button"
                    className={cn("uro-log-event", `is-${line.level}`, selected?.seq === line.seq && "is-active")}
                    onClick={() => setSelectedSeq(line.seq)}
                  >
                    <time>{new Date(line.ts).toLocaleTimeString([], { hour12: false })}</time>
                    <span className="uro-log-event__level">
                      <i />
                      {line.level}
                    </span>
                    <code>{line.tag || "system"}</code>
                    <p>{body}</p>
                    <ChevronRightIcon />
                  </button>
                );
              })
            )}
            <div ref={bottom} />
          </div>
        </div>

        <aside className="uro-logs-inspector">
          <header>
            <CircleAlertIcon />
            <span>Event inspector</span>
          </header>
          {selected ? (
            <div className="uro-logs-inspector__body">
              <div className={cn("uro-logs-inspector__signal", `is-${selected.level}`)}>
                {selected.level === "error" ? (
                  <CircleAlertIcon />
                ) : selected.level === "warn" ? (
                  <AlertTriangleIcon />
                ) : (
                  <ActivityIcon />
                )}
                <small>Selected event</small>
                <strong>
                  {selected.level === "error"
                    ? "Action may be required"
                    : selected.level === "warn"
                      ? "Review boundary signal"
                      : "Informational telemetry"}
                </strong>
              </div>
              <dl>
                <div>
                  <dt>Sequence</dt>
                  <dd>#{selected.seq}</dd>
                </div>
                <div>
                  <dt>Source</dt>
                  <dd>{selected.tag || "system"}</dd>
                </div>
                <div>
                  <dt>Level</dt>
                  <dd>{selected.level}</dd>
                </div>
                <div>
                  <dt>Recorded</dt>
                  <dd>{fmtTime(selected.ts)}</dd>
                </div>
              </dl>
              <section>
                <span>Raw event</span>
                <pre>{selected.text}</pre>
                <Button size="sm" variant="outline" onClick={() => void navigator.clipboard?.writeText(selected.text)}>
                  <CopyIcon />
                  Copy event
                </Button>
              </section>
              <footer>
                <Clock3Icon />
                <p>Pause the stream before correlating a moving incident window.</p>
              </footer>
            </div>
          ) : (
            <div className="uro-logs-inspector__empty">
              <ActivityIcon />
              <strong>Select an event</strong>
              <p>Event metadata and raw payload will stay pinned here.</p>
            </div>
          )}
        </aside>
      </section>
    </div>
  );
}
