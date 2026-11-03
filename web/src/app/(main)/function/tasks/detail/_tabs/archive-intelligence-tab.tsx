"use client";

import * as React from "react";

import { ArchiveIcon, RefreshCwIcon, ShieldCheckIcon } from "lucide-react";
import { toast } from "sonner";

import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type { ArchiveProviderArtifact, ArchiveProviderGrant, ArchiveProviderGrantView } from "@/lib/types";

import { ResearchIntelligencePanel } from "./research-intelligence-panel";

function defaultExpiry() {
  const date = new Date(Date.now() + 24 * 60 * 60 * 1000);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function formatTimestamp(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function shortHash(value?: string) {
  return value ? `${value.slice(0, 12)}…` : "—";
}

function grantState(grant: ArchiveProviderGrant) {
  if (grant.revoked_at) return "revoked";
  if (new Date(grant.expires_at).getTime() <= Date.now()) return "expired";
  return "active";
}

function projectionRows(artifact: ArchiveProviderArtifact) {
  const rows = artifact.projection.rows;
  return Array.isArray(rows) ? rows.length : 0;
}

export function ArchiveIntelligenceTab({ taskId }: { taskId: string }) {
  const [view, setView] = React.useState<ArchiveProviderGrantView | null>(null);
  const [artifacts, setArtifacts] = React.useState<ArchiveProviderArtifact[]>([]);
  const [domain, setDomain] = React.useState("");
  const [captureFrom, setCaptureFrom] = React.useState("");
  const [captureTo, setCaptureTo] = React.useState("");
  const [expiresAt, setExpiresAt] = React.useState(defaultExpiry);
  const [acknowledged, setAcknowledged] = React.useState(false);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [revoking, setRevoking] = React.useState<number | null>(null);
  const [error, setError] = React.useState("");

  const load = React.useCallback(async () => {
    try {
      const [grants, artifactPage] = await Promise.all([
        api.archiveProviderGrants(taskId),
        api.archiveProviderArtifacts(taskId),
      ]);
      setView(grants);
      setArtifacts(artifactPage.items);
      setError("");
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  }, [taskId]);

  React.useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  const notice = (
    view?.notice_template ?? "The query sends the domain {domain} to Internet Archive (web.archive.org)."
  ).replace("{domain}", domain.trim() || "{domain}");

  async function createGrant() {
    if (!view || saving) return;
    const expires = new Date(expiresAt);
    if (Number.isNaN(expires.getTime())) {
      toast.error("Choose a valid expiry time");
      return;
    }
    setSaving(true);
    try {
      await api.createArchiveProviderGrant(taskId, {
        domain: domain.trim(),
        capture_from: captureFrom.trim(),
        capture_to: captureTo.trim(),
        expires_at: expires.toISOString(),
        notice_version: view.notice_version,
        acknowledged: true,
      });
      toast.success("Archive provider disclosure grant created");
      setAcknowledged(false);
      await load();
    } catch (e) {
      toast.error(`Grant failed: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  async function revokeGrant(grantId: number) {
    setRevoking(grantId);
    try {
      await api.revokeArchiveProviderGrant(taskId, grantId);
      toast.success("Archive provider disclosure grant revoked");
      await load();
    } catch (e) {
      toast.error(`Revocation failed: ${(e as Error).message}`);
    } finally {
      setRevoking(null);
    }
  }

  if (loading && !view) {
    return (
      <div className="flex items-center gap-2 text-muted-foreground text-sm">
        <Spinner /> Loading archive intelligence…
      </div>
    );
  }

  return (
    <div className="flex flex-col gap-4">
      <ResearchIntelligencePanel taskId={taskId} />

      <div className="my-2 border-t" />

      {error ? (
        <Alert variant="destructive">
          <AlertTitle>Archive intelligence is unavailable</AlertTitle>
          <AlertDescription className="flex items-center justify-between gap-3">
            <span>{error}</span>
            <Button variant="outline" size="sm" onClick={() => void load()}>
              Retry
            </Button>
          </AlertDescription>
        </Alert>
      ) : null}

      <Alert>
        <ShieldCheckIcon />
        <AlertTitle>Provider access is off by default</AlertTitle>
        <AlertDescription>
          A grant permits only matching Internet Archive CDX metadata queries. Creating a grant sends no request. The
          task agent must explicitly invoke archive discovery, and every result remains an unverified candidate.
        </AlertDescription>
      </Alert>

      <div className="grid items-start gap-4 xl:grid-cols-[minmax(22rem,0.9fr)_minmax(0,1.1fr)]">
        <Card>
          <CardHeader>
            <CardTitle>Create disclosure grant</CardTitle>
            <CardDescription>
              Only a registrable root domain already present in this task's frozen root-domain scope is accepted.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="grid gap-2">
              <Label htmlFor="archive-domain">Root domain</Label>
              <Input
                id="archive-domain"
                value={domain}
                onChange={(event) => {
                  setDomain(event.target.value);
                  setAcknowledged(false);
                }}
                placeholder="example.com"
                autoCapitalize="none"
                autoCorrect="off"
              />
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="archive-from">Capture from</Label>
                <Input
                  id="archive-from"
                  value={captureFrom}
                  onChange={(event) => {
                    setCaptureFrom(event.target.value);
                    setAcknowledged(false);
                  }}
                  placeholder="YYYY, YYYYMM, YYYYMMDD…"
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="archive-to">Capture to</Label>
                <Input
                  id="archive-to"
                  value={captureTo}
                  onChange={(event) => {
                    setCaptureTo(event.target.value);
                    setAcknowledged(false);
                  }}
                  placeholder="YYYY, YYYYMM, YYYYMMDD…"
                />
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="archive-expiry">Grant expires</Label>
              <Input
                id="archive-expiry"
                type="datetime-local"
                value={expiresAt}
                onChange={(event) => {
                  setExpiresAt(event.target.value);
                  setAcknowledged(false);
                }}
              />
            </div>
            <div className="rounded-lg border bg-muted/40 p-3 text-sm leading-relaxed">
              <p>{notice}</p>
              <p className="mt-2 text-muted-foreground">
                Raw provider response bytes are discarded after bounded parsing. Only separate body and sanitized
                projection hashes are retained in private operator artifacts.
              </p>
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="archive-acknowledge"
                checked={acknowledged}
                onCheckedChange={(value) => setAcknowledged(value === true)}
              />
              <Label htmlFor="archive-acknowledge" className="font-normal leading-snug">
                I acknowledge this provider disclosure for the domain, capture range and expiry shown above.
              </Label>
            </div>
            <Button onClick={() => void createGrant()} disabled={!view || !domain.trim() || !acknowledged || saving}>
              {saving ? <Spinner data-icon="inline-start" /> : <ShieldCheckIcon data-icon="inline-start" />}
              Create grant
            </Button>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div>
                <CardTitle>Disclosure grants</CardTitle>
                <CardDescription>Revoking an active grant also cancels the archive job bound to it.</CardDescription>
              </div>
              <Button variant="ghost" size="icon-sm" aria-label="Refresh grants" onClick={() => void load()}>
                <RefreshCwIcon />
              </Button>
            </div>
          </CardHeader>
          <CardContent>
            {view?.grants.length ? (
              <div className="flex flex-col gap-3">
                {view.grants.map((grant) => {
                  const state = grantState(grant);
                  return (
                    <div key={grant.id} className="flex flex-col gap-2 rounded-lg border p-3">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <div className="flex items-center gap-2">
                          <span className="font-medium">{grant.domain}</span>
                          <Badge variant={state === "active" ? "default" : "secondary"}>{state}</Badge>
                        </div>
                        {state === "active" ? (
                          <AlertDialog>
                            <AlertDialogTrigger asChild>
                              <Button variant="destructive" size="sm" disabled={revoking === grant.id}>
                                {revoking === grant.id ? <Spinner data-icon="inline-start" /> : null}
                                Revoke
                              </Button>
                            </AlertDialogTrigger>
                            <AlertDialogContent>
                              <AlertDialogHeader>
                                <AlertDialogTitle>Revoke disclosure for {grant.domain}?</AlertDialogTitle>
                                <AlertDialogDescription>
                                  The grant cannot be restored. Any archive job using it will be canceled and joined.
                                </AlertDialogDescription>
                              </AlertDialogHeader>
                              <AlertDialogFooter>
                                <AlertDialogCancel>Keep grant</AlertDialogCancel>
                                <AlertDialogAction onClick={() => void revokeGrant(grant.id)}>
                                  Revoke grant
                                </AlertDialogAction>
                              </AlertDialogFooter>
                            </AlertDialogContent>
                          </AlertDialog>
                        ) : null}
                      </div>
                      <div className="grid gap-1 text-muted-foreground text-xs sm:grid-cols-2">
                        <span>
                          Captures: {grant.capture_from || "unbounded"} → {grant.capture_to || "unbounded"}
                        </span>
                        <span>Expires: {formatTimestamp(grant.expires_at)}</span>
                        <span>Approved: {formatTimestamp(grant.approved_at)}</span>
                        <span>Grant #{grant.id}</span>
                      </div>
                    </div>
                  );
                })}
              </div>
            ) : (
              <p className="text-muted-foreground text-sm">No archive provider disclosure has been granted.</p>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <ArchiveIcon className="size-4" /> Private provider artifacts
          </CardTitle>
          <CardDescription>
            One row per provider attempt. These hashes and sanitized projections are separate from target Traffic and
            never create assets or findings.
          </CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          {artifacts.length ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Attempt</TableHead>
                  <TableHead>Outcome</TableHead>
                  <TableHead>HTTP</TableHead>
                  <TableHead>Rows</TableHead>
                  <TableHead>Raw body hash</TableHead>
                  <TableHead>Projection hash</TableHead>
                  <TableHead>Finished</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {artifacts.map((artifact) => (
                  <TableRow key={artifact.id}>
                    <TableCell className="font-mono text-xs">{artifact.attempt_id}</TableCell>
                    <TableCell>
                      <Badge variant={artifact.outcome === "page" ? "default" : "secondary"}>{artifact.outcome}</Badge>
                      {artifact.error_code ? (
                        <span className="ml-2 text-muted-foreground text-xs">{artifact.error_code}</span>
                      ) : null}
                    </TableCell>
                    <TableCell>{artifact.response_status ?? "—"}</TableCell>
                    <TableCell>{projectionRows(artifact)}</TableCell>
                    <TableCell className="font-mono text-xs" title={artifact.raw_body_sha256}>
                      {shortHash(artifact.raw_body_sha256)}
                    </TableCell>
                    <TableCell className="font-mono text-xs" title={artifact.projection_sha256}>
                      {shortHash(artifact.projection_sha256)}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs">{formatTimestamp(artifact.finished_at)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : (
            <p className="text-muted-foreground text-sm">No provider request has been recorded for this task.</p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
