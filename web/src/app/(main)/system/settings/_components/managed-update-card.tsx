"use client";

import * as React from "react";
import { ArrowUpCircleIcon, CheckCircle2Icon, ExternalLinkIcon, RefreshCwIcon, ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Progress } from "@/components/ui/progress";
import { Input } from "@/components/ui/input";
import { api } from "@/lib/api";
import type { UpdateCheck, UpdateStatus } from "@/lib/types";

const stages = [
  ["downloading", "Fetch source"], ["building", "Build"], ["backing-up", "Back up"],
  ["restarting", "Restart"], ["checking", "Verify"],
] as const;
const short = (revision?: string) => revision?.slice(0, 12) || "…";

export function ManagedUpdateCard({ info, checking, onCheck }: {
  info: UpdateCheck; checking: boolean; onCheck: () => void;
}) {
  const [status, setStatus] = React.useState<UpdateStatus | undefined>(info.status);
  const [starting, setStarting] = React.useState(false);
  const [reconnecting, setReconnecting] = React.useState(false);
  const [githubToken, setGithubToken] = React.useState("");
  const [credentialBusy, setCredentialBusy] = React.useState(false);
  const [tokenConfigured, setTokenConfigured] = React.useState(!!info.github_token_configured);
  React.useEffect(() => setTokenConfigured(!!info.github_token_configured), [info.github_token_configured]);
  const expected = React.useRef<string | null>(info.status?.running ? info.status.target_revision || null : null);
  const busy = starting || !!status?.running;

  React.useEffect(() => {
    let alive = true;
    const poll = async () => {
      try {
        const next = await api.updateStatus();
        if (!alive) return;
        setStatus(next);
        setReconnecting(false);
        if (next.running) { expected.current = next.target_revision || expected.current; sessionStorage.setItem("uroboros.update.section", "lifecycle"); }
        if (!next.running) setStarting(false);
        if (next.phase === "complete" && expected.current && next.current_revision === expected.current) {
          expected.current = null;
          toast.success("Update verified. Reloading the console.");
          sessionStorage.setItem("uroboros.update.section", "lifecycle");
          window.location.reload();
        }
      } catch {
        if (alive && expected.current) setReconnecting(true);
      }
    };
    void poll();
    const timer = window.setInterval(() => void poll(), 2000);
    return () => { alive = false; window.clearInterval(timer); };
  }, []);

  const apply = async () => {
    if (!info.latest_revision) return;
    if (!window.confirm(`Update Uroboros to commit ${short(info.latest_revision)} from main?\n\nThe new build is prepared first. The installer then creates a private backup, restarts the stack and checks the new console. Pause operations and finish agent conversations before continuing.`)) return;
    setStarting(true);
    expected.current = info.latest_revision;
    sessionStorage.setItem("uroboros.update.section", "lifecycle");
    try { await api.applyUpdate(info.latest_revision); }
    catch (error) {
      expected.current = null; setStarting(false);
      toast.error("Could not start update: " + (error as Error).message);
    }
  };

  const active = stages.findIndex(([phase]) => phase === status?.phase);
  const saveCredential = async (value: string) => {
    setCredentialBusy(true);
    try { const result = await api.saveUpdateCredential(value); setTokenConfigured(result.configured); setGithubToken(""); toast.success(result.configured ? "Private GitHub access saved locally." : "GitHub token cleared. Public access will be used."); onCheck(); }
    catch (error) { toast.error("Could not save GitHub access: " + (error as Error).message); }
    finally { setCredentialBusy(false); }
  };
  return (
    <Card className="mb-4 break-inside-avoid overflow-hidden md:mb-6">
      <CardHeader className="bg-gradient-to-br from-blue-500/10 via-sky-500/5 to-transparent">
        <CardTitle className="flex items-center gap-2 text-base"><ArrowUpCircleIcon className="size-4 text-blue-500" />Version & Updates</CardTitle>
        <CardDescription>Install updates directly from the main branch. Your operations, API profiles and settings stay with this installation.</CardDescription>
      </CardHeader>
      <CardContent className="space-y-4 pt-4">
        <div className="flex flex-wrap items-center gap-2 text-xs">
          <Badge variant="secondary">Uroboros {info.current}</Badge>
          <Badge variant="outline">Docker · main</Badge>
          <span className="text-muted-foreground">Installed</span>
          <Badge variant="outline" className="font-mono">{short(info.current_revision)}</Badge>
          {info.latest_revision && <><span className="text-muted-foreground">Available</span><Badge variant={info.has_update ? "default" : "secondary"} className="font-mono">{short(info.latest_revision)}</Badge></>}
        </div>
        {info.error && <p role="alert" className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs text-destructive">{info.error}</p>}
        <details className="rounded-md border border-border p-3 text-xs" open={!!info.error}>
          <summary className="cursor-pointer text-muted-foreground">GitHub access · optional {tokenConfigured ? "· configured" : ""}</summary>
          <div className="mt-3 space-y-3">
            <p className="text-muted-foreground">Public repositories need no token. For a private repository, select this repository and Contents: read-only in a fine-grained token. The credential stays in this installation and is never returned by the API.</p>
            <label htmlFor="github-update-token" className="block">Repository token</label>
            <Input id="github-update-token" type="password" autoComplete="new-password" spellCheck={false} value={githubToken} onChange={event => setGithubToken(event.target.value)} placeholder={tokenConfigured ? "A token is saved; enter a replacement" : "GitHub repository read token"} disabled={busy || credentialBusy} />
            <div className="flex flex-wrap items-center gap-2">
              <Button size="sm" variant="outline" onClick={() => void saveCredential(githubToken)} disabled={busy || credentialBusy || !githubToken.trim()}>Save GitHub access</Button>
              {tokenConfigured && <Button size="sm" variant="ghost" onClick={() => void saveCredential("")} disabled={busy || credentialBusy}>Clear token</Button>}
              <a href="https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens" target="_blank" rel="noreferrer" className="text-muted-foreground underline-offset-4 hover:underline">Token setup guide</a>
            </div>
          </div>
        </details>
        {info.blocked_reason && !busy && <p className="rounded-md border border-amber-500/30 bg-amber-500/5 p-3 text-xs text-amber-700 dark:text-amber-400">{info.blocked_reason}</p>}
        {!info.has_update && !info.error && !busy && <p className="flex items-center gap-2 text-xs text-muted-foreground"><CheckCircle2Icon className="size-4 text-emerald-500" />This installation matches main.</p>}
        {busy && <div className="space-y-3" aria-live="polite">
          <div className="flex flex-wrap gap-1.5">{stages.map(([phase, label], index) => <span key={phase} className={`rounded-full border px-2 py-1 text-[10px] ${index === active ? "border-blue-500/50 bg-blue-500/10 text-blue-500" : "border-border text-muted-foreground"}`}>{label}</span>)}</div>
          <Progress value={100} className="animate-pulse" />
          <p className="text-xs text-muted-foreground">{reconnecting ? "Reconnecting while the containers restart…" : status?.message || "Starting the update…"}</p>
        </div>}
        {status?.phase === "failed" && <div role="alert" className="space-y-1 rounded-md border border-destructive/30 bg-destructive/5 p-3 text-xs"><p>{status.message}</p><p className="break-words text-muted-foreground">{status.error}</p></div>}
        <div className="flex flex-wrap gap-2">
          <Button variant="outline" size="sm" onClick={onCheck} disabled={checking || busy || credentialBusy}><RefreshCwIcon className={checking ? "size-4 animate-spin" : "size-4"} />Check for updates</Button>
          <Button size="sm" onClick={() => void apply()} disabled={busy || credentialBusy || status?.recovery_required || !info.has_update || !info.latest_revision || !!info.error || info.can_update === false}><ArrowUpCircleIcon className="size-4" />{busy ? "Updating…" : "Update from main"}</Button>
          {info.html_url && <a href={info.html_url} target="_blank" rel="noreferrer" className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:underline">View commit <ExternalLinkIcon className="size-3" /></a>}
        </div>
        <p className="flex items-start gap-2 text-xs text-muted-foreground"><ShieldCheckIcon className="mt-0.5 size-3.5 shrink-0 text-blue-500" />The current version stays available during the build. Recovery backups are kept locally outside the source checkout.</p>
      </CardContent>
    </Card>
  );
}
