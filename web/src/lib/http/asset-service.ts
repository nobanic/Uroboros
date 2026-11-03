import type {
  Asset,
  Company,
  CompanyScopeMutation,
  CompanyScopeRule,
  IntentAsset,
  TaskAssetMutation,
  TaskAssetScopeMutation,
} from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { type QueryEntry, Resource } from "./resources";

type AssetPage = { count: number; total: number; assets: Asset[] };
const projectPage = (value: AssetPage) => ({ assets: value?.assets ?? [], total: value?.total ?? value?.count ?? 0 });

/** Asset searches share one scope-aware query plan and page projection; legacy unscoped type interpolation is retained. */
export function assetService(wire: ApplicationWire) {
  const root = new Resource(wire, "/assets");
  const companies = new Resource(wire, "/companies");
  const taskAssets = (task: string) => new Resource(wire, `/tasks/${task}/assets`);
  const search = (scope: QueryEntry[], type: string, limit: number, offset: number) =>
    root
      .query([...scope, ["type", type], ["limit", limit], ["offset", offset]], "component")
      .read<AssetPage>()
      .then(projectPage);
  const companyScope = (id: number, scope: CompanyScopeRule[], reason: string, reset = false) =>
    companies
      .at(String(id), "scope")
      .send<CompanyScopeMutation>("POST", { scope, reason, ...(reset ? { reset: true } : {}) });
  return {
    assets: (type = "", limit = 50, offset = 0) =>
      new Resource(wire, `/assets?type=${type}&limit=${limit}&offset=${offset}`).read<AssetPage>().then(projectPage),
    searchAssets: (dsl: string, type = "", limit = 50, offset = 0) =>
      root
        .query(
          [
            ["dsl", String(dsl)],
            ["type", type ? type : undefined],
            ["limit", limit],
            ["offset", offset],
          ],
          "component",
        )
        .read<AssetPage>()
        .then(projectPage),
    assetCounts: (taskId = "") =>
      (taskId ? root.at("counts").query([["task_id", taskId]], "component") : root.at("counts")).read<
        Record<string, number>
      >(),
    deleteAssets: (ids: number[]) => root.send<{ deleted: number }>("DELETE", { ids }),
    taskAssets: (taskId: string, type = "", limit = 50, offset = 0) =>
      search([["task_id", String(taskId)]], type, limit, offset),
    searchTaskAssets: (taskId: string, dsl: string, type = "", limit = 50, offset = 0) =>
      search(
        [
          ["task_id", String(taskId)],
          ["dsl", String(dsl)],
        ],
        type,
        limit,
        offset,
      ),
    attachTaskAssets: (taskId: string, assetIds: number[], sourceSummary: string) =>
      taskAssets(taskId).send<TaskAssetMutation>("POST", { asset_ids: assetIds, source_summary: sourceSummary }),
    registerTaskAssetScopes: (taskId: string, scope: CompanyScopeRule[]) =>
      taskAssets(taskId).send<TaskAssetScopeMutation>("POST", { scope }),
    detachTaskAsset: (taskId: string, assetId: number) =>
      taskAssets(taskId).at(String(assetId)).send<{ detached: number }>("DELETE"),
    taskIntentAssets: (taskId: string) =>
      new Resource(wire, `/tasks/${taskId}/intent-assets`).list<IntentAsset>("assets"),
    companies: () => companies.list<Company>(),
    createCompany: (name: string, scope: CompanyScopeRule[]) =>
      companies.send<{
        id: number;
        created: boolean;
        scope_added?: number;
        scope_invalid?: number;
        scope_errors?: string[];
      }>("POST", { name, scope }),
    addCompanyScope: (id: number, scope: CompanyScopeRule[], reason = "") => companyScope(id, scope, reason),
    updateCompanyScope: (id: number, scope: CompanyScopeRule[], reason = "") => companyScope(id, scope, reason, true),
    deleteCompany: (id: number, deleteAssets = false) =>
      companies
        .at(String(id))
        .send<{ deleted: number; assets_deleted: number }>("DELETE", { delete_assets: deleteAssets }),
  };
}
