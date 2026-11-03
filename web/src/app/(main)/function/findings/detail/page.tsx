"use client";

import { FindingDisposition, FindingProperty, FindingSection } from "@/astra/finding-inspector";

import * as React from "react";

import Link from "next/link";
import { useSearchParams } from "next/navigation";

import { ArrowLeftIcon, ArrowUpRightIcon, ShieldAlertIcon } from "lucide-react";
import { toast } from "sonner";

import { CopyButton } from "@/components/copy-button";
import { FindingRetestPanel } from "@/components/finding-retest-panel";
import { FindingTrafficPanel } from "@/components/finding-traffic-panel";
import { Markdown } from "@/components/markdown";
import { StatusBadge } from "@/components/status-badge";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

import { Separator } from "@/components/ui/separator";
import { SidebarTrigger } from "@/components/ui/sidebar";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { statusMeta } from "@/lib/status";
import type { Finding, FindingStatus, Severity } from "@/lib/types";

import { FindingLineageView } from "./lineage";

const SEVERITIES: Severity[] = ["critical", "high", "medium", "low"];
const FINDING_STATUSES: FindingStatus[] = [
  "pending",
  "in_progress",
  "confirmed",
  "resolved",
  "fixed",
  "false_positive",
  "ignored",
  "duplicate",
  "risk_accepted",
];

function fmtTime(ts: string) {
  return new Date(ts).toLocaleString("en-US");
}

// FieldRow is one label/value line in the right-hand status panel.
function FieldRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <FindingProperty label={label}>{children}</FindingProperty>
  );
}

function FindingDetailInner() {
  const searchParams = useSearchParams();
  const id = searchParams.get("id") ?? "";
  const contextTaskId = searchParams.get("context_task") ?? "";
  const [finding, setFinding] = React.useState<Finding | null>(null);
  const [loaded, setLoaded] = React.useState(false);
  const [tab, setTab] = React.useState("overview");

  const load = React.useCallback(() => {
    if (!id) {
      setLoaded(true);
      return;
    }
    api
      .getFinding(id, contextTaskId || undefined)
      .then((f) => setFinding(f))
      .catch(() => setFinding(null))
      .finally(() => setLoaded(true));
  }, [contextTaskId, id]);
  React.useEffect(() => {
    load();
  }, [load]);

  const changeSeverity = React.useCallback(
    async (next: Severity) => {
      if (!finding || finding.inherited || next === finding.severity) return;
      const prev = finding.severity;
      setFinding({ ...finding, severity: next });
      try {
        const updated = await api.setFindingSeverity(id, next);
        setFinding(updated);
        toast.success(`Severity changed to "${statusMeta("severity", next).label}"`);
      } catch (e) {
        setFinding((cur) => (cur ? { ...cur, severity: prev } : cur));
        toast.error("Update failed: " + (e as Error).message);
      }
    },
    [finding, id],
  );

  const changeStatus = React.useCallback(
    async (next: FindingStatus) => {
      if (!finding || finding.inherited || next === finding.status) return;
      const prev = finding.status;
      setFinding({ ...finding, status: next });
      try {
        const updated = await api.setFindingStatus(id, next);
        setFinding(updated);
        toast.success(`Status changed to "${statusMeta("finding", next).label}"`);
      } catch (e) {
        setFinding((cur) => (cur ? { ...cur, status: prev } : cur));
        toast.error("Update failed: " + (e as Error).message);
      }
    },
    [finding, id],
  );

  if (!finding) {
    return (
      <div className="flex flex-1 flex-col items-center justify-center gap-3 p-10 text-center">
        <p className="text-muted-foreground">{loaded ? `Finding ${id} not found` : "Loading…"}</p>
        {loaded && (
          <Button asChild variant="outline">
            <Link href="/function/findings">
              <ArrowLeftIcon /> Back to findings
            </Link>
          </Button>
        )}
      </div>
    );
  }

  const title = finding.name || finding.vulnclass || "Unclassified";

  return (
    <Tabs value={tab} onValueChange={setTab} className="finding-inspector flex flex-1 flex-col gap-0">
      {/* Sticky header */}
      <header className="sticky top-0 z-10 flex flex-col gap-2 border-b bg-background/95 px-4 py-2.5 backdrop-blur lg:px-6">
        <div className="flex flex-wrap items-center gap-2">
          <SidebarTrigger className="-ml-1" />
          <Button asChild variant="ghost" size="icon" className="size-7">
            <Link href="/function/findings">
              <ArrowLeftIcon />
            </Link>
          </Button>
          <ShieldAlertIcon className="size-4 text-muted-foreground" />
          <h1 className="finding-inspector-heading" title={title}>
            {title}
          </h1>
          <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">#{finding.id}</code>
          <Separator orientation="vertical" className="mx-1 h-4" />
          <StatusBadge domain="severity" value={finding.severity} dot />
          <StatusBadge domain="finding" value={finding.status} dot />
          {finding.inherited && finding.source_task_id && (
            <Badge variant="outline">From task #{finding.source_task_id} · Read-only</Badge>
          )}
        </div>
        <TabsList>
          <TabsTrigger value="overview">Overview</TabsTrigger>
          <TabsTrigger value="lineage">Lineage</TabsTrigger>
        </TabsList>
      </header>

      {/* Tab content */}
      <div className="flex-1 p-4 lg:p-6">
        {/* Overview: left (summary + evidence) / right (status panel) */}
        <TabsContent value="overview" className="mt-0">
          <div className="grid gap-4 lg:grid-cols-3">
            {/* Left column */}
            <div className="flex flex-col gap-4 lg:col-span-2">
              <FindingSection title={<>Summary</>}>
                <p className="text-sm leading-relaxed whitespace-pre-wrap">{finding.summary || "(No summary)"}</p>
              </FindingSection>
              <FindingRetestPanel key={`retest-${id}`} findingId={id} readOnly={finding.inherited} onCompleted={load} />
              <FindingSection title={<>Evidence / PoC</>}>
                {finding.evidence ? (
                  <pre className="max-h-[46vh] overflow-auto rounded-md bg-muted px-3 py-2 font-mono text-xs whitespace-pre-wrap">
                    {finding.evidence}
                  </pre>
                ) : (
                  <p className="text-sm text-muted-foreground">(No evidence)</p>
                )}
              </FindingSection>
              <FindingTrafficPanel
                key={`traffic-${id}`}
                findingId={id}
                contextTask={contextTaskId || undefined}
                readOnly={finding.inherited}
                onChanged={load}
              />
              {/* Below the evidence: detailed report (rendered Markdown) */}
              <FindingSection
                title={<>Detailed report</>}
                actions={
                  <>{finding.report && <CopyButton text={finding.report} successMessage="Detailed report copied" />}</>
                }
                headerClassName="flex-row items-center justify-between"
              >
                {finding.report_stale ? (
                  <Alert>
                    <AlertDescription>
                      Traffic evidence has changed; the detailed report is pending update.
                    </AlertDescription>
                  </Alert>
                ) : null}
                {finding.report ? (
                  <Markdown text={finding.report} />
                ) : (
                  <p className="text-sm text-muted-foreground">No detailed report yet.</p>
                )}
              </FindingSection>
            </div>

            {/* Right column: status panel */}
            <FindingSection className="h-fit self-start lg:sticky lg:top-0" title={<>Status</>} contentClassName="divide-y">
              {/* Vulnerability ID */}
              <FieldRow label="Vulnerability ID">
                <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-muted-foreground">
                  #{finding.id}
                </code>
              </FieldRow>

              {/* Severity */}
              <FieldRow label="Severity">
                {finding.inherited ? (
                  <StatusBadge domain="severity" value={finding.severity} dot />
                ) : (
                  <FindingDisposition
                    domain="severity"
                    value={finding.severity}
                    options={SEVERITIES}
                    onValueChange={(v) => changeSeverity(v as Severity)}
                    triggerClassName="h-7 w-auto border-none px-1 shadow-none focus-visible:ring-0"
                  />
                )}
              </FieldRow>

              {/* Triage status */}
              <FieldRow label="Status">
                {finding.inherited ? (
                  <StatusBadge domain="finding" value={finding.status} dot />
                ) : (
                  <FindingDisposition
                    domain="finding"
                    value={finding.status}
                    options={FINDING_STATUSES}
                    onValueChange={(v) => changeStatus(v as FindingStatus)}
                    triggerClassName="h-7 w-auto border-none px-1 shadow-none focus-visible:ring-0"
                  />
                )}
              </FieldRow>

              {/* Vulnerability class */}
              <FieldRow label="Vulnerability class">
                {finding.vulnclass ? (
                  <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs">{finding.vulnclass}</code>
                ) : (
                  <span className="text-muted-foreground">—</span>
                )}
              </FieldRow>

              {/* Affected assets */}
              <FieldRow label="Affected assets">
                {finding.assets && finding.assets.length > 0 ? (
                  <div className="flex flex-wrap justify-end gap-1">
                    {finding.assets.map((a) => (
                      <code
                        key={a.id}
                        className="max-w-[16rem] truncate rounded bg-muted px-1.5 py-0.5 font-mono text-xs"
                        title={`${a.type} · ${a.label}`}
                      >
                        {a.label}
                      </code>
                    ))}
                  </div>
                ) : (
                  <span className="text-muted-foreground">—</span>
                )}
              </FieldRow>

              {/* Task */}
              <FieldRow label="Task">
                {finding.task_id ? (
                  <Link
                    href={`/function/tasks/detail?id=${finding.task_id}`}
                    className="inline-flex max-w-[16rem] items-center gap-1 text-primary hover:underline"
                    title={finding.task_description}
                  >
                    <span className="truncate">{finding.task_description || `#${finding.task_id}`}</span>
                    <ArrowUpRightIcon className="size-3 shrink-0" />
                  </Link>
                ) : (
                  <span className="text-muted-foreground">—(Task deleted)</span>
                )}
              </FieldRow>

              {/* Found at */}
              <FieldRow label="Found at">
                <span className="tabular-nums">{fmtTime(finding.ts)}</span>
              </FieldRow>
            </FindingSection>
          </div>
        </TabsContent>

        {/* Lineage: the attack chain traced back from the task's initial node to this vulnerability's node */}
        <TabsContent value="lineage" className="mt-0">
          <FindingLineageView findingId={finding.id} />
        </TabsContent>
      </div>
    </Tabs>
  );
}

// useSearchParams must sit under a Suspense boundary for static export.
export default function FindingDetailPage() {
  return (
    <React.Suspense fallback={null}>
      <FindingDetailInner />
    </React.Suspense>
  );
}
