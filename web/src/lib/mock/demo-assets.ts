import {
  classifyCompanyScopeLine,
  companyScopeRuleError,
  isCompanyScopeKind,
  normalizeCompanyScopeValue,
} from "../company-scope";
import type {
  Asset,
  Company,
  CompanyScopeRule,
  IntentAsset,
  ScopeRow,
  Task,
  TaskAssetMutation,
  TaskAssetScopeMutation,
  TaskScopeRow,
} from "../types";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";
import { DemoRows } from "./demo-records";

export type DemoAssetSource = Pick<Asset, "task_source" | "task_source_summary" | "task_source_node_id">;
const missing = () => new Error("uroboros.lab demonstration record.");
const sourceKey = (task: string, asset: number) => JSON.stringify([task, asset]);

function scopeValue(rule: CompanyScopeRule): {
  normalized: string;
  fields: { domain?: string; net?: string; value?: string };
} {
  const normalized = normalizeCompanyScopeValue(rule);
  const fields =
    rule.kind === "domain"
      ? { domain: normalized }
      : rule.kind === "ip"
        ? { net: `${normalized}/${normalized.includes(":") ? 128 : 32}` }
        : rule.kind === "cidr"
          ? { net: normalized }
          : { value: normalized };
  return { normalized, fields };
}

/** Asset membership, provenance and scope borrow the same mutable row collections. */
export class DemoAssets {
  readonly assets: DemoRows<Asset, number>;
  readonly companies: DemoRows<Company, number>;
  private readonly taskNumbers: Map<string, number>;
  private readonly scopes = new Map<string, TaskScopeRow[]>();
  private readonly sources = new Map<string, DemoAssetSource>();
  private nextTask: number;

  constructor(
    tasks: readonly Task[],
    companies: Company[],
    assets: Asset[],
    private readonly hasTask: (id: string) => boolean,
  ) {
    this.companies = new DemoRows(structuredClone(companies), (row) => row.id);
    this.assets = new DemoRows(structuredClone(assets), (row) => row.id);
    this.taskNumbers = new Map(tasks.map((task, index) => [task.id, index + 1]));
    this.nextTask = tasks.length + 1;
  }

  taskNumber(id: string): number | undefined {
    const value = Number(id);
    return Number.isInteger(value) && value > 0 ? value : this.taskNumbers.get(id);
  }

  private mark(task: string, asset: number, source: DemoAssetSource): void {
    this.sources.set(sourceKey(task, asset), source);
  }

  private clearSources(task: string, asset?: number): void {
    if (asset !== undefined) {
      this.sources.delete(sourceKey(task, asset));
      return;
    }
    for (const key of this.sources.keys())
      if ((JSON.parse(key) as [string, number])[0] === task) this.sources.delete(key);
  }

  forTask(task: string, asset: Asset): Asset {
    const source = this.sources.get(sourceKey(task, asset.id));
    return source ? { ...asset, ...source } : asset;
  }

  counts(task?: string | null): Record<string, number> {
    const number = task ? this.taskNumber(task) : undefined;
    const counts: Record<string, number> = {};
    this.assets.rows.forEach((asset) => {
      if (task && (number === undefined || !asset.task_ids.includes(number))) return;
      counts[asset.type] = (counts[asset.type] ?? 0) + 1;
    });
    return counts;
  }

  attachCompanies(task: string, companies: number[]): void {
    const number = this.nextTask++;
    this.taskNumbers.set(task, number);
    const selected = new Set(companies);
    for (const asset of this.assets.rows) {
      if (asset.company_id === undefined || !selected.has(asset.company_id)) continue;
      if (!asset.task_ids.includes(number)) asset.task_ids.push(number);
      this.mark(task, asset.id, {
        task_source: "company",
        task_source_summary: "uroboros.lab demonstration record. Context: company, name, asset.company_id.",
        task_source_node_id: undefined,
      });
    }
  }

  private unlink(number: number): void {
    for (const asset of this.assets.rows) asset.task_ids = asset.task_ids.filter((id) => id !== number);
  }

  detachTask(task: string): void {
    const number = this.taskNumber(task);
    this.taskNumbers.delete(task);
    this.clearSources(task);
    if (number !== undefined) this.unlink(number);
  }

  capture(task: string, number: number): { assetIDs: number[]; assetSources: Array<[number, DemoAssetSource]> } {
    const assetIDs = this.assets.rows.filter((asset) => asset.task_ids.includes(number)).map((asset) => asset.id);
    const assetSources: Array<[number, DemoAssetSource]> = [];
    for (const id of assetIDs) {
      const source = this.sources.get(sourceKey(task, id));
      if (source) assetSources.push([id, structuredClone(source)]);
    }
    return { assetIDs, assetSources };
  }

  retire(task: string, number: number): void {
    this.unlink(number);
    this.clearSources(task);
  }

  restore(task: string, number: number, ids: number[], sources: Array<[number, DemoAssetSource]>): void {
    this.taskNumbers.set(task, number);
    for (const id of ids) {
      const asset = this.assets.get(id);
      if (asset && !asset.task_ids.includes(number)) asset.task_ids.push(number);
    }
    for (const [id, source] of sources) this.mark(task, id, source);
  }

  private companyScope(
    company: number,
    input: unknown,
    existing: ScopeRow[] = [],
  ): { rows: ScopeRow[]; invalid: number; skipped: number } {
    if (!Array.isArray(input)) return { rows: [], invalid: 0, skipped: 0 };
    let next =
      this.companies.rows.flatMap((row) => row.scope ?? []).reduce((maximum, row) => Math.max(maximum, row.id), 0) + 1;
    const keys = new Set(
      existing.map((row) => `${row.kind}|${row.domain ?? row.net ?? row.value ?? row.raw.trim().toLowerCase()}`),
    );
    const rows: ScopeRow[] = [];
    let invalid = 0,
      skipped = 0;
    for (const [index, candidate] of input.entries()) {
      let rule: CompanyScopeRule | undefined;
      if (typeof candidate === "string") rule = classifyCompanyScopeLine(candidate, index + 1).rule;
      else if (candidate && typeof candidate === "object") {
        const item = candidate as { kind?: unknown; value?: unknown },
          value = String(item.value ?? "").trim();
        if (item.kind === undefined || item.kind === "") rule = classifyCompanyScopeLine(value, index + 1).rule;
        else if (isCompanyScopeKind(item.kind)) rule = { kind: item.kind, value };
      }
      if (!rule || companyScopeRuleError(rule)) {
        invalid++;
        continue;
      }
      const value = scopeValue(rule);
      const row: ScopeRow = {
        id: next++,
        company_id: company,
        kind: rule.kind,
        raw: rule.value.trim(),
        ...value.fields,
      };
      const key = `${row.kind}|${row.domain ?? row.net ?? row.value}`;
      if (keys.has(key)) {
        skipped++;
        continue;
      }
      keys.add(key);
      rows.push(row);
    }
    return { rows, invalid, skipped };
  }

  private taskRules(input: unknown[]): CompanyScopeRule[] {
    return input.map((candidate, index) => {
      if (typeof candidate === "string") {
        const parsed = classifyCompanyScopeLine(candidate, index + 1);
        if (!parsed.rule || parsed.error)
          throw new Error("uroboros.lab demonstration record. Context: index, issue.error.");
        return parsed.rule;
      }
      const item = candidate as { kind?: unknown; value?: unknown },
        value = String(item?.value ?? "").trim();
      const rule =
        item?.kind && isCompanyScopeKind(item.kind)
          ? { kind: item.kind, value }
          : classifyCompanyScopeLine(value, index + 1).rule;
      const error = rule ? companyScopeRuleError(rule) : "uroboros.lab demonstration record.";
      if (!rule || error) throw new Error("uroboros.lab demonstration record. Context: index, error.");
      return rule;
    });
  }

  private attach(task: string, body: Record<string, unknown>): TaskAssetMutation | TaskAssetScopeMutation {
    const number = this.taskNumber(task);
    if (!this.hasTask(task) || number === undefined) throw missing();
    if (Array.isArray(body.scope)) {
      if (!body.scope.length) throw missing();
      const rules = this.taskRules(body.scope);
      const mutation: TaskAssetScopeMutation = {
        requested: rules.length,
        assets_linked: 0,
        assets_existing: 0,
        scopes_added: 0,
        scopes_existing: 0,
      };
      const rows = this.scopes.get(task) ?? [];
      const keys = new Set(
        rows.map((row) => `${row.kind}|${row.domain ?? row.net ?? row.value ?? row.company_id ?? ""}`),
      );
      for (const rule of rules) {
        const { normalized, fields } = scopeValue(rule);
        const scope: TaskScopeRow = {
          id: Date.now() + rows.length,
          task_id: number,
          kind: rule.kind === "domain" ? "root_domain" : rule.kind,
          source: "manual",
          reason: "Recorded by the demonstration policy.",
          ...fields,
        };
        const key = `${scope.kind}|${scope.domain ?? scope.net ?? scope.value ?? ""}`;
        if (keys.has(key)) mutation.scopes_existing++;
        else {
          keys.add(key);
          rows.push(scope);
          mutation.scopes_added++;
        }
        if (rule.kind !== "domain" && rule.kind !== "ip") continue;
        const type = rule.kind === "domain" ? "root_domain" : "ip";
        let asset = this.assets.rows.find(
          (row) => row.type === type && (type === "root_domain" ? row.domain : row.ip) === normalized,
        );
        const linked = asset?.task_ids.includes(number) ?? false;
        if (!asset) {
          asset = {
            id: this.assets.nextNumber(),
            type,
            task_ids: [],
            ...(type === "root_domain" ? { domain: normalized, root_domain: normalized } : { ip: normalized }),
            last_seen: new Date().toISOString(),
          };
          this.assets.rows.push(asset);
        }
        if (linked) mutation.assets_existing++;
        else {
          asset.task_ids.push(number);
          mutation.assets_linked++;
        }
        this.mark(task, asset.id, {
          task_source: "manual",
          task_source_summary: "uroboros.lab demonstration record.",
          task_source_node_id: undefined,
        });
      }
      this.scopes.set(task, rows);
      return mutation;
    }
    const ids = [...new Set(Array.isArray(body.asset_ids) ? body.asset_ids.map(Number) : [])];
    const summary = String(body.source_summary ?? "").trim();
    if (!ids.length || ids.length > 100 || !summary) throw missing();
    const selected = ids.map((id) => this.assets.get(id));
    if (selected.some((asset) => !asset)) throw missing();
    const mutation: TaskAssetMutation = { requested: ids.length, attached: 0, existing: 0 };
    for (const asset of selected) {
      if (!asset) continue;
      if (asset.task_ids.includes(number)) mutation.existing++;
      else {
        asset.task_ids.push(number);
        mutation.attached++;
      }
      this.mark(task, asset.id, {
        task_source: "manual",
        task_source_summary: summary,
        task_source_node_id: undefined,
      });
    }
    return mutation;
  }

  private intentAssets(task: string): { assets: IntentAsset[] } {
    const api = {
      intentID: "i5",
      assetID: 3,
      summary: "Verified security evidence captured during the uroboros.lab demonstration assessment. Context: API.",
    };
    const mappings =
      task === "t-acme-web"
        ? [
            {
              intentID: "i3",
              assetID: 6,
              summary: "Verified security evidence captured during the uroboros.lab demonstration assessment.",
            },
            api,
          ]
        : task === "t-acme-api"
          ? [api]
          : [];
    const sourceTask = this.taskNumber(task) ?? 0;
    return {
      assets: mappings.flatMap((mapping) => {
        const stored = this.assets.get(mapping.assetID);
        if (!stored) return [];
        const asset = this.forTask(task, stored);
        return [
          {
            intent_id: mapping.intentID,
            asset_id: asset.id,
            type: asset.type,
            label: asset.domain ?? asset.ip ?? asset.app_name ?? asset.url ?? asset.service_name ?? `#${asset.id}`,
            source: asset.task_source ?? "agent",
            source_summary: asset.task_source_summary ?? mapping.summary,
            source_node_id: asset.task_source_node_id,
            source_task_id: sourceTask,
            inherited: false,
          },
        ];
      }),
    };
  }

  respond({ method, path, segments, query, body }: DemoRequest): unknown {
    if (segments[0] === "tasks" && segments[2] === "scope") {
      const task = segments[1];
      if (segments.length === 3 && method === "GET") return { scope: this.scopes.get(task) ?? [] };
      if (segments.length === 3 && method === "POST") {
        const rows = this.scopes.get(task) ?? [],
          kind = String(body.kind ?? "") as TaskScopeRow["kind"],
          value = String(body.value ?? "").trim();
        const scope: TaskScopeRow = {
          id: Date.now(),
          task_id: this.taskNumber(task) ?? Number(task),
          kind,
          source: "manual",
        };
        if (kind === "root_domain" || kind === "subdomain") scope.domain = value;
        else if (kind === "ip" || kind === "cidr") scope.net = value;
        else scope.value = value;
        this.scopes.set(task, [...rows, scope]);
        return scope;
      }
      if (segments.length === 4 && method === "DELETE") {
        this.scopes.set(
          task,
          (this.scopes.get(task) ?? []).filter((row) => row.id !== Number(segments[3])),
        );
        return { ok: true };
      }
    }
    if (path === "/assets/counts") return this.counts(query.get("task_id"));
    if (path === "/assets" && method === "GET") {
      const type = query.get("type") ?? "",
        task = query.get("task_id"),
        number = task ? this.taskNumber(task) : undefined;
      const dsl = query.get("dsl") ?? "";
      const words = dsl
        .replaceAll(/[()"]/g, " ")
        .replaceAll(/\b(?:AND|OR)\b/gi, " ")
        .replaceAll(/\b[a-z_][a-z0-9_]*(?:==|!=|>=|<=|=|>|<)/gi, " ")
        .trim()
        .toLowerCase();
      const rows = this.assets.rows.filter((asset) => {
        if (type && asset.type !== type) return false;
        if (task && (number === undefined || !asset.task_ids.includes(number))) return false;
        if (!dsl || !words) return true;
        const haystack = JSON.stringify(asset).toLowerCase();
        return words.split(/\s+/).every((word) => haystack.includes(word));
      });
      const limit = Number(query.get("limit") ?? 50),
        start = Number(query.get("offset") ?? 0);
      const assets = rows.slice(start, start + limit).map((asset) => (task ? this.forTask(task, asset) : asset));
      return { count: assets.length, total: rows.length, assets };
    }
    if (path === "/assets" && method === "DELETE") {
      const ids = new Set(Array.isArray(body.ids) ? body.ids.map(Number) : []);
      return { deleted: this.assets.removeWhere((asset) => ids.has(asset.id)) };
    }
    if (segments[0] === "tasks" && segments[2] === "assets") {
      if (segments.length === 3 && method === "POST") return this.attach(segments[1], body);
      if (segments.length === 4 && method === "DELETE") {
        const number = this.taskNumber(segments[1]),
          asset = this.assets.get(Number(segments[3]));
        if (number === undefined || !asset || !asset.task_ids.includes(number)) throw missing();
        asset.task_ids = asset.task_ids.filter((id) => id !== number);
        this.clearSources(segments[1], asset.id);
        return { detached: asset.id };
      }
    }
    if (segments[0] === "tasks" && segments[2] === "intent-assets" && segments.length === 3 && method === "GET")
      return this.intentAssets(segments[1]);
    if (path === "/companies" && method === "GET") return structuredClone(this.companies.rows);
    if (path === "/companies" && method === "POST") {
      const name = String(body.name ?? "").trim();
      if (!name || this.companies.rows.some((row) => row.name.toLowerCase() === name.toLowerCase())) throw missing();
      const id = this.companies.nextNumber(),
        scope = this.companyScope(id, body.scope);
      this.companies.rows.push({ id, name, asset_count: 0, scope: scope.rows });
      return {
        id,
        created: true,
        scope_added: scope.rows.length,
        scope_skipped: scope.skipped,
        scope_invalid: scope.invalid,
      };
    }
    if (segments[0] === "companies" && segments[2] === "scope" && method === "POST") {
      const company = this.companies.get(Number(segments[1]));
      if (!company) throw missing();
      const reset = body.reset === true,
        scope = this.companyScope(company.id, body.scope, reset ? [] : (company.scope ?? []));
      if (reset && scope.invalid) throw missing();
      company.scope = reset ? scope.rows : [...(company.scope ?? []), ...scope.rows];
      return { added: scope.rows.length, skipped: scope.skipped, invalid: scope.invalid };
    }
    if (segments[0] === "companies" && segments.length === 2 && method === "DELETE") {
      const id = Number(segments[1]);
      if (!this.companies.remove(id)) throw missing();
      const deleted = this.assets.removeWhere((asset) => {
        if (asset.company_id !== id) return false;
        if (body.delete_assets === true) return true;
        delete asset.company_id;
        return false;
      });
      return { deleted: 1, assets_deleted: deleted };
    }
    return UNHANDLED;
  }
}
