import type {
  AssetInterceptRule,
  InterceptApprovalFilter,
  InterceptApprovalRow,
  InterceptDetail,
  InterceptExecution,
  InterceptPending,
  InterceptRule,
  JudgeConfig,
  JudgeUsage,
} from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { Resource, slice } from "./resources";

type RuleInput = Omit<InterceptRule, "id" | "created_at" | "updated_at">;
type AssetRuleInput = Pick<AssetInterceptRule, "enabled" | "kind" | "pattern" | "note">;

/** Rule catalogs share CRUD policy, while decisions and execution reads retain separate endpoints and authority. */
function ruleCatalog<Row, Input>(resource: Resource) {
  return {
    list: () => resource.list<Row>("rules"),
    create: (input: Input) => resource.send<Row>("POST", input),
    update: (id: number, input: Input) => resource.at(String(id)).send<Row>("PUT", input),
    remove: (id: number) => resource.at(String(id)).send<{ deleted: number }>("DELETE"),
    toggle: (id: number, enabled: boolean) =>
      resource.at(String(id), "toggle").send<{ ok: boolean; enabled: boolean }>("POST", { enabled }),
  };
}

export function approvalService(wire: ApplicationWire, demo: () => boolean) {
  const root = new Resource(wire, "/intercept");
  const rules = ruleCatalog<InterceptRule, RuleInput>(root.at("rules"));
  const assets = ruleCatalog<AssetInterceptRule, AssetRuleInput>(new Resource(wire, "/asset-intercept/rules"));
  const pending = root.at("pending");
  const history = root.at("history");
  const config = root.at("tool-config");
  const judge = root.at("judge");
  const page = (source: Resource, page: number, size: number, filter: InterceptApprovalFilter) =>
    source
      .query([
        ["page", page],
        ["size", size],
        ["status", filter.status ? filter.status : undefined],
        ["decision_source", filter.decision_source ? filter.decision_source : undefined],
      ])
      .read<{ items: InterceptApprovalRow[]; total?: number }>()
      .then((value) => ({ items: slice(value.items), total: value.total ?? value.items?.length ?? 0 }));

  return {
    interceptRules: rules.list,
    createInterceptRule: rules.create,
    updateInterceptRule: rules.update,
    deleteInterceptRule: rules.remove,
    toggleInterceptRule: rules.toggle,
    assetInterceptRules: assets.list,
    createAssetInterceptRule: assets.create,
    updateAssetInterceptRule: assets.update,
    deleteAssetInterceptRule: assets.remove,
    toggleAssetInterceptRule: assets.toggle,
    interceptPending: () => pending.list<InterceptPending>("pending"),
    interceptGetOne: (id: number) => pending.at(String(id)).read<InterceptPending>(),
    interceptDecide: (id: number, decision: "allowed" | "denied") =>
      pending.at(String(id), "decide").send<{ ok: boolean }>("POST", { decision }),
    interceptExecution: (id: number, conversationId?: number) => {
      const execution = history.at(String(id), "execution");
      return (
        conversationId ? execution.query([["conversation", conversationId]], "component") : execution
      ).read<InterceptExecution>();
    },
    interceptDetail: (id: number) => history.at(String(id)).read<InterceptDetail>(),
    interceptHistory: () => history.list<InterceptApprovalRow>("items"),
    interceptHistoryPage: (pageNumber = 1, size = 20, filter: InterceptApprovalFilter = {}) =>
      page(history, pageNumber, size, filter),
    // Legacy unpaged task reads interpolate verbatim; paged task reads encode their ID.
    interceptTask: (taskId: string) => root.at("task", String(taskId)).list<InterceptApprovalRow>("items"),
    interceptTaskPage: (taskId: string, pageNumber = 1, size = 20, filter: InterceptApprovalFilter = {}) =>
      page(root.at("task", encodeURIComponent(taskId)), pageNumber, size, filter),
    interceptGetToolConfig: async (): Promise<{ enabled_tools: string[] }> => {
      if (demo()) return { enabled_tools: ["bash"] };
      const response = await config.raw();
      if (!response.ok) throw new Error(await response.text());
      return response.json();
    },
    interceptSetToolConfig: async (enabledTools: string[]): Promise<void> => {
      if (demo()) return;
      const response = await config.raw({
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ enabled_tools: enabledTools }),
      });
      if (!response.ok) throw new Error(await response.text());
    },
    interceptGetJudgeConfig: () => judge.read<JudgeConfig>(),
    interceptSetJudgeConfig: (cfg: JudgeConfig) => judge.send<{ ok: boolean }>("PUT", cfg),
    interceptJudgeUsage: (days = 30) =>
      judge
        .at("usage")
        .query([["days", days]], "component")
        .read<JudgeUsage>(),
  };
}
