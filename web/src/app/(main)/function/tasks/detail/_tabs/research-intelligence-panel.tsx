"use client";

import * as React from "react";

import { RefreshCwIcon, SearchIcon, ShieldCheckIcon } from "lucide-react";
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Spinner } from "@/components/ui/spinner";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { api } from "@/lib/api";
import type {
  ResearchArtifact,
  ResearchDisclosureGrant,
  ResearchEgressGrant,
  ResearchEgressGrantView,
  ResearchGrantView,
  ResearchProvider,
} from "@/lib/types";

function researchDefaultExpiry() {
  const date = new Date(Date.now() + 24 * 60 * 60 * 1000);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function formatResearchTimestamp(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function researchGrantState(grant: Pick<ResearchDisclosureGrant | ResearchEgressGrant, "revoked_at" | "expires_at">) {
  if (grant.revoked_at) return "revoked";
  if (new Date(grant.expires_at).getTime() <= Date.now()) return "expired";
  return "active";
}

function researchShortHash(value?: string) {
  return value ? `${value.slice(0, 12)}…` : "—";
}

export function ResearchIntelligencePanel({ taskId }: { taskId: string }) {
  const [view, setView] = React.useState<ResearchGrantView | null>(null);
  const [egressView, setEgressView] = React.useState<ResearchEgressGrantView | null>(null);
  const [artifacts, setArtifacts] = React.useState<ResearchArtifact[]>([]);
  const [provider, setProvider] = React.useState<ResearchProvider | "">("");
  const [expiresAt, setExpiresAt] = React.useState(researchDefaultExpiry);
  const [maxRequests, setMaxRequests] = React.useState("20");
  const [acknowledged, setAcknowledged] = React.useState(false);
  const [torSeed, setTorSeed] = React.useState("");
  const [torExpiresAt, setTorExpiresAt] = React.useState(researchDefaultExpiry);
  const [torMaxRequests, setTorMaxRequests] = React.useState("20");
  const [torAcknowledged, setTorAcknowledged] = React.useState(false);
  const [savingTor, setSavingTor] = React.useState(false);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);
  const [revoking, setRevoking] = React.useState<number | null>(null);
  const [error, setError] = React.useState("");

  const load = React.useCallback(async () => {
    try {
      const [grantView, torView, artifactView] = await Promise.all([
        api.researchGrants(taskId),
        api.researchEgressGrants(taskId),
        api.researchArtifacts(taskId),
      ]);
      setView(grantView);
      setEgressView(torView);
      setArtifacts(artifactView.artifacts);
      setProvider((current) => current || grantView.current_provider);
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

  async function createGrant() {
    if (!view || saving) return;
    const expires = new Date(expiresAt);
    const requests = Number(maxRequests);
    if (Number.isNaN(expires.getTime()) || !Number.isInteger(requests) || requests < 1) {
      toast.error("Choose a valid future expiry and a positive request limit");
      return;
    }
    setSaving(true);
    try {
      await api.createResearchGrant(taskId, {
        provider: provider || view.current_provider,
        expires_at: expires.toISOString(),
        max_requests: requests,
        notice_version: view.notice_version,
        acknowledged: true,
      });
      toast.success("Research disclosure grant created");
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
      await api.revokeResearchGrant(taskId, grantId);
      toast.success("Research disclosure grant revoked");
      await load();
    } catch (e) {
      toast.error(`Revocation failed: ${(e as Error).message}`);
    } finally {
      setRevoking(null);
    }
  }

  async function createTorGrant() {
    if (!egressView || savingTor) return;
    const expires = new Date(torExpiresAt);
    const requests = Number(torMaxRequests);
    if (!torSeed.trim() || Number.isNaN(expires.getTime()) || !Number.isInteger(requests) || requests < 1) {
      toast.error("Enter an absolute seed URL, future expiry, and positive request limit");
      return;
    }
    setSavingTor(true);
    try {
      await api.createResearchEgressGrant(taskId, {
        profile_id: "tor",
        seeds: [torSeed.trim()],
        expires_at: expires.toISOString(),
        max_requests: requests,
        notice_version: egressView.notice_version,
        acknowledged: true,
      });
      toast.success("Tor research egress grant created");
      setTorAcknowledged(false);
      await load();
    } catch (e) {
      toast.error(`Tor grant failed: ${(e as Error).message}`);
    } finally {
      setSavingTor(false);
    }
  }

  async function revokeTorGrant(grantId: number) {
    setRevoking(grantId);
    try {
      await api.revokeResearchEgressGrant(taskId, grantId);
      toast.success("Tor research egress grant revoked");
      await load();
    } catch (e) {
      toast.error(`Revocation failed: ${(e as Error).message}`);
    } finally {
      setRevoking(null);
    }
  }

  const effectiveProvider = provider || view?.current_provider || "ddgs";
  const notice = (view?.notice_template ?? "Allow target-identifying queries to {provider}.").replace(
    "{provider}",
    effectiveProvider,
  );

  if (loading && !view) {
    return (
      <div className="flex items-center gap-2 text-muted-foreground text-sm">
        <Spinner /> Loading audited research…
      </div>
    );
  }

  return (
    <section className="flex flex-col gap-4">
      {error ? (
        <Alert variant="destructive">
          <AlertTitle>Audited research is unavailable</AlertTitle>
          <AlertDescription className="flex items-center justify-between gap-3">
            <span>{error}</span>
            <Button variant="outline" size="sm" onClick={() => void load()}>
              Retry
            </Button>
          </AlertDescription>
        </Alert>
      ) : null}

      <Alert>
        <SearchIcon />
        <AlertTitle>Host-owned research boundary</AlertTitle>
        <AlertDescription>
          Generic queries are audited automatically. A query that names this task&apos;s explicit scope runs only when
          the selected provider has an active grant below. Scope added later is not inherited by an older grant.
        </AlertDescription>
      </Alert>

      <div className="grid items-start gap-4 xl:grid-cols-[minmax(22rem,0.9fr)_minmax(0,1.1fr)]">
        <Card>
          <CardHeader>
            <CardTitle>Create research grant</CardTitle>
            <CardDescription>
              Identities are inferred and frozen from task scope, so the operator does not re-enter targets.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-2">
                <Label>Provider</Label>
                <Select
                  value={effectiveProvider}
                  onValueChange={(value) => {
                    setProvider(value as ResearchProvider);
                    setAcknowledged(false);
                  }}
                >
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="brave-free">Brave</SelectItem>
                    <SelectItem value="tavily">Tavily</SelectItem>
                    <SelectItem value="ddgs">DuckDuckGo (best-effort)</SelectItem>
                    <SelectItem value="osv">OSV package/version matching</SelectItem>
                    <SelectItem value="nvd">NVD CPE matching</SelectItem>
                    <SelectItem value="cve-list-v5">CVE List V5</SelectItem>
                    <SelectItem value="ghsa">GitHub advisories</SelectItem>
                    <SelectItem value="cisa-kev">CISA KEV</SelectItem>
                    <SelectItem value="first-epss">FIRST EPSS</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              <div className="grid gap-2">
                <Label htmlFor="research-expiry">Expires</Label>
                <Input
                  id="research-expiry"
                  type="datetime-local"
                  value={expiresAt}
                  onChange={(event) => {
                    setExpiresAt(event.target.value);
                    setAcknowledged(false);
                  }}
                />
              </div>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="research-max">Maximum disclosed requests</Label>
              <Input
                id="research-max"
                inputMode="numeric"
                value={maxRequests}
                onChange={(event) => {
                  setMaxRequests(event.target.value);
                  setAcknowledged(false);
                }}
              />
            </div>
            <div className="rounded-lg border bg-muted/40 p-3 text-sm leading-relaxed">
              <p>{notice}</p>
              <p className="mt-2 text-muted-foreground">
                Raw provider bytes are hashed then discarded. Sanitized projections are untrusted and cannot create
                scope, assets, facts or findings by themselves.
              </p>
            </div>
            <div className="flex flex-wrap gap-1">
              {view?.disclosure_identities.map((identity) => (
                <Badge key={identity} variant="secondary">
                  {identity}
                </Badge>
              ))}
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="research-ack"
                checked={acknowledged}
                onCheckedChange={(value) => setAcknowledged(value === true)}
              />
              <Label htmlFor="research-ack" className="font-normal leading-snug">
                I acknowledge disclosure of the frozen identities above to {effectiveProvider} until this grant expires.
              </Label>
            </div>
            <Button
              onClick={() => void createGrant()}
              disabled={!view || !acknowledged || saving || !view.disclosure_identities.length}
            >
              {saving ? <Spinner data-icon="inline-start" /> : <ShieldCheckIcon data-icon="inline-start" />}
              Create grant
            </Button>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-start justify-between gap-3">
              <div>
                <CardTitle>Research disclosure grants</CardTitle>
                <CardDescription>One provider and one frozen identity set per grant.</CardDescription>
              </div>
              <Button variant="ghost" size="icon-sm" aria-label="Refresh research grants" onClick={() => void load()}>
                <RefreshCwIcon />
              </Button>
            </div>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {view?.grants.length ? (
              view.grants.map((grant) => {
                const state = researchGrantState(grant);
                return (
                  <div key={grant.id} className="flex flex-col gap-2 rounded-lg border p-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <strong>{grant.provider}</strong>
                        <Badge variant={state === "active" ? "default" : "secondary"}>{state}</Badge>
                      </div>
                      {state === "active" ? (
                        <AlertDialog>
                          <AlertDialogTrigger asChild>
                            <Button variant="destructive" size="sm" disabled={revoking === grant.id}>
                              Revoke
                            </Button>
                          </AlertDialogTrigger>
                          <AlertDialogContent>
                            <AlertDialogHeader>
                              <AlertDialogTitle>Revoke research disclosure?</AlertDialogTitle>
                              <AlertDialogDescription>
                                Future target-identifying queries to {grant.provider} will fail closed.
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
                      <span>{grant.identities.length} frozen identities</span>
                      <span>Budget: {grant.max_requests}</span>
                      <span>Expires: {formatResearchTimestamp(grant.expires_at)}</span>
                      <span>Grant #{grant.id}</span>
                    </div>
                  </div>
                );
              })
            ) : (
              <p className="text-muted-foreground text-sm">No research disclosure grant exists for this task.</p>
            )}
          </CardContent>
        </Card>
      </div>

      <div className="grid items-start gap-4 xl:grid-cols-[minmax(22rem,0.9fr)_minmax(0,1.1fr)]">
        <Card>
          <CardHeader>
            <CardTitle>Create Tor research grant</CardTitle>
            <CardDescription>
              One seed authorizes read-only navigation only on its canonical origin through profile <b>tor</b>.
            </CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-4">
            <div className="flex flex-wrap gap-2 text-xs">
              <Badge variant={egressView?.enabled ? "default" : "secondary"}>
                profile {egressView?.enabled ? "enabled" : "disabled"}
              </Badge>
              <Badge variant={egressView?.endpoint_configured ? "default" : "secondary"}>
                SOCKS {egressView?.endpoint_configured ? "configured" : "unavailable"}
              </Badge>
              <Badge variant="outline">proxy hidden</Badge>
            </div>
            <div className="grid gap-2">
              <Label htmlFor="tor-research-seed">Seed URL</Label>
              <Input
                id="tor-research-seed"
                placeholder="https://example.org/ or http://…onion/"
                value={torSeed}
                onChange={(event) => {
                  setTorSeed(event.target.value);
                  setTorAcknowledged(false);
                }}
              />
            </div>
            <div className="grid gap-3 sm:grid-cols-2">
              <div className="grid gap-2">
                <Label htmlFor="tor-research-expiry">Expires</Label>
                <Input
                  id="tor-research-expiry"
                  type="datetime-local"
                  value={torExpiresAt}
                  onChange={(event) => {
                    setTorExpiresAt(event.target.value);
                    setTorAcknowledged(false);
                  }}
                />
              </div>
              <div className="grid gap-2">
                <Label htmlFor="tor-research-max">Maximum requests</Label>
                <Input
                  id="tor-research-max"
                  inputMode="numeric"
                  value={torMaxRequests}
                  onChange={(event) => {
                    setTorMaxRequests(event.target.value);
                    setTorAcknowledged(false);
                  }}
                />
              </div>
            </div>
            <div className="rounded-lg border bg-muted/40 p-3 text-sm leading-relaxed">
              Only GET/HEAD is available. DNS stays inside SOCKS, redirects cannot leave the approved origin, and an
              outage never falls back to direct internet. No forms, login, upload, JavaScript browser, target mutation,
              active scanning, LLM traffic, ControlPort or NEWNYM is included.
            </div>
            <div className="flex items-start gap-2">
              <Checkbox
                id="tor-research-ack"
                checked={torAcknowledged}
                onCheckedChange={(value) => setTorAcknowledged(value === true)}
              />
              <Label htmlFor="tor-research-ack" className="font-normal leading-snug">
                I approve bounded Tor retrieval on the origin derived from this seed until the grant expires.
              </Label>
            </div>
            <Button
              onClick={() => void createTorGrant()}
              disabled={!egressView || !torAcknowledged || savingTor || !torSeed.trim()}
            >
              {savingTor ? <Spinner data-icon="inline-start" /> : <ShieldCheckIcon data-icon="inline-start" />}
              Create Tor grant
            </Button>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Tor egress grants</CardTitle>
            <CardDescription>Frozen seeds and origins; credentials and proxy coordinates are never exposed.</CardDescription>
          </CardHeader>
          <CardContent className="flex flex-col gap-3">
            {egressView?.grants.length ? (
              egressView.grants.map((grant) => {
                const state = researchGrantState(grant);
                return (
                  <div key={grant.id} className="flex flex-col gap-2 rounded-lg border p-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <div className="flex items-center gap-2">
                        <strong>{grant.profile_id}</strong>
                        <Badge variant={state === "active" ? "default" : "secondary"}>{state}</Badge>
                      </div>
                      {state === "active" ? (
                        <Button
                          variant="destructive"
                          size="sm"
                          disabled={revoking === grant.id}
                          onClick={() => void revokeTorGrant(grant.id)}
                        >
                          Revoke
                        </Button>
                      ) : null}
                    </div>
                    <div className="truncate text-xs" title={grant.seeds.join(", ")}>
                      {grant.seeds.join(", ")}
                    </div>
                    <div className="grid gap-1 text-muted-foreground text-xs sm:grid-cols-2">
                      <span>{grant.origins.length} approved origin(s)</span>
                      <span>Budget: {grant.max_requests}</span>
                      <span>Expires: {formatResearchTimestamp(grant.expires_at)}</span>
                      <span>Grant #{grant.id}</span>
                    </div>
                  </div>
                );
              })
            ) : (
              <p className="text-muted-foreground text-sm">No Tor research egress grant exists for this task.</p>
            )}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle>Research receipts</CardTitle>
          <CardDescription>
            Search, typed vulnerability and Tor retrieval attempts are task-local and included in task archive/restore.
          </CardDescription>
        </CardHeader>
        <CardContent className="overflow-x-auto">
          {artifacts.length ? (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Tool / provider</TableHead>
                  <TableHead>Outcome</TableHead>
                  <TableHead>Query</TableHead>
                  <TableHead>HTTP</TableHead>
                  <TableHead>Raw hash</TableHead>
                  <TableHead>Finished</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {artifacts.map((artifact) => (
                  <TableRow key={artifact.id}>
                    <TableCell>
                      <div>
                        {artifact.tool_name} · {artifact.provider}
                        {artifact.retry_index ? ` · retry ${artifact.retry_index}` : ""}
                      </div>
                      <div className="text-muted-foreground text-xs">{artifact.agent_key}</div>
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={
                          artifact.outcome === "success" ||
                          artifact.outcome === "cache_hit" ||
                          artifact.outcome === "stale_cache"
                            ? "default"
                            : "secondary"
                        }
                      >
                        {artifact.outcome}
                      </Badge>
                      {artifact.target_identifying ? (
                        <Badge className="ml-1" variant="outline">
                          target
                        </Badge>
                      ) : null}
                    </TableCell>
                    <TableCell className="max-w-72 truncate text-xs" title={artifact.query_projection}>
                      {artifact.query_projection}
                    </TableCell>
                    <TableCell>{artifact.response_status ?? "—"}</TableCell>
                    <TableCell className="font-mono text-xs" title={artifact.raw_body_sha256}>
                      {researchShortHash(artifact.raw_body_sha256)}
                    </TableCell>
                    <TableCell className="whitespace-nowrap text-xs">
                      <div>{formatResearchTimestamp(artifact.finished_at)}</div>
                      {artifact.retrieved_at ? (
                        <div className="text-muted-foreground">source {formatResearchTimestamp(artifact.retrieved_at)}</div>
                      ) : null}
                      {artifact.schema_version ? (
                        <div className="text-muted-foreground">{artifact.schema_version}</div>
                      ) : null}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : (
            <p className="text-muted-foreground text-sm">
              No research provider attempt has been recorded for this task.
            </p>
          )}
        </CardContent>
      </Card>
    </section>
  );
}
