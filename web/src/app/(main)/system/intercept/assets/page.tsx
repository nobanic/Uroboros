"use client";

import * as React from "react";

import {
  BanIcon,
  CheckCircle2Icon,
  Globe2Icon,
  NetworkIcon,
  PencilIcon,
  PlusIcon,
  RadarIcon,
  SearchIcon,
  ShieldXIcon,
  Trash2Icon,
} from "lucide-react";
import { toast } from "sonner";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Separator } from "@/components/ui/separator";
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle } from "@/components/ui/sheet";
import { Switch } from "@/components/ui/switch";
import { Textarea } from "@/components/ui/textarea";
import { api } from "@/lib/api";
import type { AssetInterceptKind, AssetInterceptRule } from "@/lib/types";

// ---- kind metadata ----

const KIND_OPTIONS: { value: AssetInterceptKind; label: string; group: string; placeholder: string }[] = [
  { value: "exact_domain", label: "Domain (exact)", group: "Exact match", placeholder: "example.gov" },
  { value: "exact_ip", label: "IP (exact)", group: "Exact match", placeholder: "203.0.113.10" },
  { value: "exact_url", label: "URL (exact)", group: "Exact match", placeholder: "https://example.gov/login" },
  { value: "fuzzy_domain", label: "Domain (fuzzy)", group: "Fuzzy match", placeholder: ".gov" },
  { value: "fuzzy_ip", label: "IP (fuzzy)", group: "Fuzzy match", placeholder: "203.0.113." },
  { value: "fuzzy_url", label: "URL (fuzzy)", group: "Fuzzy match", placeholder: "/admin" },
  { value: "cidr", label: "CIDR range", group: "Range", placeholder: "192.168.0.0/16" },
];

const KIND_LABEL: Record<AssetInterceptKind, string> = Object.fromEntries(
  KIND_OPTIONS.map((o) => [o.value, o.label]),
) as Record<AssetInterceptKind, string>;

const KIND_GROUPS = ["Exact match", "Fuzzy match", "Range"];

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label className="text-xs font-medium text-muted-foreground uppercase tracking-wide">{label}</Label>
      {children}
      {hint && <p className="text-[11px] text-muted-foreground">{hint}</p>}
    </div>
  );
}

// ---- form state ----

type RuleForm = {
  enabled: boolean;
  kind: AssetInterceptKind;
  pattern: string;
  note: string;
};

const defaultForm = (): RuleForm => ({ enabled: true, kind: "fuzzy_domain", pattern: "", note: "" });

// Light front-end validation (matches the backend: only exact_ip / cidr are format-checked here, the rest are left to the backend).
function frontValidate(form: RuleForm): string | null {
  const p = form.pattern.trim();
  if (!p) return "Match value cannot be empty";
  if (form.kind === "cidr" && !/^[0-9a-fA-F:.]+\/\d{1,3}$/.test(p)) {
    return "Invalid CIDR format, e.g. 192.168.0.0/16";
  }
  return null;
}

function ipv4Number(value: string): number | null {
  const parts = value.split(".").map(Number);
  if (parts.length !== 4 || parts.some((part) => !Number.isInteger(part) || part < 0 || part > 255)) return null;
  return parts.reduce((total, part) => total * 256 + part, 0) >>> 0;
}

function ruleMatches(rule: AssetInterceptRule, rawValue: string): boolean {
  const value = rawValue.trim().toLowerCase();
  if (!value || !rule.enabled) return false;
  let host = value;
  try {
    host = new URL(value.includes("://") ? value : `https://${value}`).hostname.toLowerCase();
  } catch {
    host = value.split("/")[0];
  }
  const pattern = rule.pattern.trim().toLowerCase();
  if (rule.kind === "exact_domain") return host === pattern;
  if (rule.kind === "exact_ip") return host === pattern;
  if (rule.kind === "exact_url") return value === pattern;
  if (rule.kind === "fuzzy_domain") return host.includes(pattern);
  if (rule.kind === "fuzzy_ip") return host.includes(pattern);
  if (rule.kind === "fuzzy_url") return value.includes(pattern);
  const [network, bitsText] = pattern.split("/");
  const address = ipv4Number(host);
  const base = ipv4Number(network);
  const bits = Number(bitsText);
  if (address === null || base === null || !Number.isInteger(bits) || bits < 0 || bits > 32) return false;
  const mask = bits === 0 ? 0 : (0xffffffff << (32 - bits)) >>> 0;
  return (address & mask) === (base & mask);
}

// ---- page ----

export default function AssetInterceptPage() {
  const [rules, setRules] = React.useState<AssetInterceptRule[]>([]);
  const [loading, setLoading] = React.useState(true);
  const [open, setOpen] = React.useState(false);
  const [editing, setEditing] = React.useState<AssetInterceptRule | null>(null);
  const [form, setForm] = React.useState<RuleForm>(defaultForm());
  const [saving, setSaving] = React.useState(false);
  const [probe, setProbe] = React.useState("payments.partner.test");

  const load = React.useCallback(async () => {
    try {
      setRules(await api.assetInterceptRules());
    } catch {
      toast.error("Failed to load asset interception rules");
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  function set(patch: Partial<RuleForm>) {
    setForm((f) => ({ ...f, ...patch }));
  }

  function openNew() {
    setEditing(null);
    setForm(defaultForm());
    setOpen(true);
  }

  function openEdit(rule: AssetInterceptRule) {
    setEditing(rule);
    setForm({ enabled: rule.enabled, kind: rule.kind, pattern: rule.pattern, note: rule.note });
    setOpen(true);
  }

  async function handleSave() {
    const err = frontValidate(form);
    if (err) {
      toast.error(err);
      return;
    }
    const payload = { ...form, pattern: form.pattern.trim() };
    setSaving(true);
    try {
      if (editing) {
        await api.updateAssetInterceptRule(editing.id, payload);
        toast.success("Rule updated");
      } else {
        await api.createAssetInterceptRule(payload);
        toast.success("Rule created");
      }
      setOpen(false);
      void load();
    } catch (e) {
      toast.error((e as Error).message);
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete(rule: AssetInterceptRule) {
    if (!window.confirm(`Delete asset interception rule "${rule.pattern}"?`)) return;
    try {
      await api.deleteAssetInterceptRule(rule.id);
      toast.success("Rule deleted");
      void load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  async function handleToggle(rule: AssetInterceptRule) {
    try {
      await api.toggleAssetInterceptRule(rule.id, !rule.enabled);
      void load();
    } catch (e) {
      toast.error((e as Error).message);
    }
  }

  const placeholder = KIND_OPTIONS.find((o) => o.value === form.kind)?.placeholder ?? "";
  const matches = rules.filter((rule) => ruleMatches(rule, probe));
  const enabledCount = rules.filter((rule) => rule.enabled).length;
  const exactCount = rules.filter((rule) => rule.enabled && rule.kind.startsWith("exact_")).length;
  const fuzzyCount = rules.filter((rule) => rule.enabled && rule.kind.startsWith("fuzzy_")).length;
  const rangeCount = rules.filter((rule) => rule.enabled && rule.kind === "cidr").length;

  return (
    <div className="uro-guard-console">
      <section className="uro-guard-commandbar">
        <div className="uro-guard-commandbar__identity">
          <span>
            <ShieldXIcon />
          </span>
          <div>
            <small>Target authority boundary</small>
            <strong>Asset guard perimeter</strong>
            <p>Stop protected destinations before any execution path can reach them.</p>
          </div>
        </div>
        <dl className="uro-guard-commandbar__metrics">
          <div>
            <dt>Rules online</dt>
            <dd>
              {enabledCount}
              <small>/{rules.length}</small>
            </dd>
          </div>
          <div>
            <dt>Exact fences</dt>
            <dd>{exactCount}</dd>
          </div>
          <div>
            <dt>Fuzzy fences</dt>
            <dd>{fuzzyCount}</dd>
          </div>
          <div>
            <dt>Network ranges</dt>
            <dd>{rangeCount}</dd>
          </div>
        </dl>
        <Button onClick={openNew}>
          <PlusIcon /> New boundary
        </Button>
      </section>

      <div className="uro-guard-workbench">
        <section className="uro-guard-rules">
          <header>
            <div>
              <BanIcon />
              <span>Protected boundary ledger</span>
            </div>
            <Badge variant="outline">deny before execution</Badge>
          </header>
          <div className="uro-guard-rules__labels">
            <span>Match surface</span>
            <span>Protected value</span>
            <span>Authority note</span>
            <span>State</span>
            <span />
          </div>
          <div className="uro-guard-rules__list">
            {loading ? (
              <div className="uro-guard-rules__empty">Loading authority boundaries...</div>
            ) : rules.length === 0 ? (
              <button type="button" className="uro-guard-rules__empty" onClick={openNew}>
                <PlusIcon />
                <strong>Create the first protected boundary</strong>
                <span>Exact, fuzzy, and CIDR matching are available.</span>
              </button>
            ) : (
              rules.map((rule) => (
                <article key={rule.id} className={`uro-guard-rule${rule.enabled ? " is-live" : ""}`}>
                  <span className="uro-guard-rule__kind">
                    {rule.kind === "cidr" ? <NetworkIcon /> : <Globe2Icon />}
                    <small>
                      {rule.kind.startsWith("exact_") ? "exact" : rule.kind.startsWith("fuzzy_") ? "fuzzy" : "range"}
                    </small>
                  </span>
                  <div className="uro-guard-rule__pattern">
                    <code>{rule.pattern}</code>
                    <small>{KIND_LABEL[rule.kind]}</small>
                  </div>
                  <div className="uro-guard-rule__note">
                    <span>{rule.builtin ? "built-in" : "operator"}</span>
                    <p>{rule.note || "No authority note recorded."}</p>
                  </div>
                  <span className={`uro-guard-rule__state${rule.enabled ? " is-live" : ""}`}>
                    <i />
                    {rule.enabled ? "blocking" : "bypassed"}
                  </span>
                  <div className="uro-guard-rule__controls">
                    <Switch
                      checked={rule.enabled}
                      onCheckedChange={() => handleToggle(rule)}
                      aria-label={`Enable ${rule.pattern}`}
                    />
                    <Button
                      size="icon"
                      variant="outline"
                      aria-label={`Edit ${rule.pattern}`}
                      onClick={() => openEdit(rule)}
                    >
                      <PencilIcon />
                    </Button>
                    <Button
                      size="icon"
                      variant="outline"
                      aria-label={`Delete ${rule.pattern}`}
                      onClick={() => handleDelete(rule)}
                    >
                      <Trash2Icon className="text-destructive" />
                    </Button>
                  </div>
                </article>
              ))
            )}
          </div>
          <footer>
            <ShieldXIcon />
            <span>Built-in public-sector and education boundaries remain visible in the same evaluation ledger.</span>
          </footer>
        </section>

        <aside className="uro-guard-simulator">
          <header>
            <RadarIcon />
            <span>Boundary simulator</span>
          </header>
          <label htmlFor="asset-boundary-probe">
            <span>Candidate destination</span>
            <div>
              <SearchIcon />
              <Input
                id="asset-boundary-probe"
                value={probe}
                onChange={(event) => setProbe(event.target.value)}
                placeholder="domain, IP, or URL"
              />
            </div>
          </label>
          <div className={`uro-guard-orbit${matches.length > 0 ? " is-blocked" : " is-clear"}`}>
            <i />
            <i />
            <i />
            <span>{matches.length > 0 ? <ShieldXIcon /> : <CheckCircle2Icon />}</span>
            <small>effective authority</small>
            <strong>{matches.length > 0 ? "Intercept" : "Pass through"}</strong>
            <em>
              {matches.length > 0
                ? `${matches.length} matching ${matches.length === 1 ? "boundary" : "boundaries"}`
                : "No active boundary matched"}
            </em>
          </div>
          <div className="uro-guard-simulator__matches">
            <span>Evaluation trace</span>
            {matches.map((rule) => (
              <button key={rule.id} type="button" onClick={() => openEdit(rule)}>
                <i />
                <div>
                  <code>{rule.pattern}</code>
                  <small>{KIND_LABEL[rule.kind]}</small>
                </div>
                <strong>match</strong>
              </button>
            ))}
            {matches.length === 0 && <p>The candidate remains eligible for task-level scope evaluation.</p>}
          </div>
          <p className="uro-guard-simulator__note">
            <ShieldXIcon /> This preview is local and sends no traffic to the candidate destination.
          </p>
        </aside>
      </div>

      {/* editor sheet */}
      <Sheet open={open} onOpenChange={setOpen}>
        <SheetContent side="right" className="flex flex-col gap-0 p-0 sm:max-w-md">
          <SheetHeader className="border-b px-6 py-4">
            <SheetTitle>{editing ? "Edit asset interception rule" : "New asset interception rule"}</SheetTitle>
            <SheetDescription className="text-xs">
              Target assets matching this rule are intercepted globally
            </SheetDescription>
          </SheetHeader>

          <div className="flex-1 min-h-0 overflow-y-auto px-6 py-5 space-y-5">
            <Field label="Match type">
              <Select value={form.kind} onValueChange={(v) => set({ kind: v as AssetInterceptKind })}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {KIND_GROUPS.map((g) => (
                    <React.Fragment key={g}>
                      <div className="px-2 py-1 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                        {g}
                      </div>
                      {KIND_OPTIONS.filter((o) => o.group === g).map((o) => (
                        <SelectItem key={o.value} value={o.value}>
                          {o.label}
                        </SelectItem>
                      ))}
                    </React.Fragment>
                  ))}
                </SelectContent>
              </Select>
            </Field>

            <Field
              label="Match value"
              hint={
                form.kind === "cidr"
                  ? "CIDR range, e.g. 192.168.0.0/16"
                  : form.kind.startsWith("fuzzy_")
                    ? "Fuzzy match: matches if the target contains this value"
                    : "Exact match: the target must exactly equal this value"
              }
            >
              <Input
                placeholder={placeholder}
                value={form.pattern}
                onChange={(e) => set({ pattern: e.target.value })}
              />
            </Field>

            <Field label="Note (optional)">
              <Textarea
                placeholder="Describe what this rule is for"
                value={form.note}
                onChange={(e) => set({ note: e.target.value })}
                rows={2}
                className="resize-none"
              />
            </Field>

            <Separator />

            <div className="flex items-center gap-3">
              <Switch id="asset-rule-enabled" checked={form.enabled} onCheckedChange={(v) => set({ enabled: v })} />
              <Label htmlFor="asset-rule-enabled" className="cursor-pointer">
                Enable this rule
              </Label>
            </div>
          </div>

          <SheetFooter className="border-t px-6 py-4 flex-row justify-end gap-2">
            <Button variant="outline" onClick={() => setOpen(false)}>
              Cancel
            </Button>
            <Button onClick={handleSave} disabled={saving}>
              {saving ? "Saving…" : "Save"}
            </Button>
          </SheetFooter>
        </SheetContent>
      </Sheet>
    </div>
  );
}
