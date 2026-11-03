import type { Settings } from "@/lib/types";

export const settingsDomains = [
  {
    id: "network",
    label: "Network & evidence",
    icon: "traffic",
    about: "Capture, evidence binding and target egress.",
    impact: "Network changes can rebuild agents immediately.",
  },
  {
    id: "runtime",
    label: "Agent runtime",
    icon: "agents",
    about: "Worker limits, constraints, scripts and session input.",
    impact: "Each control retains its original run or task boundary.",
  },
  {
    id: "intelligence",
    label: "Search intelligence",
    icon: "search",
    about: "Search providers, credentials and audited retrieval.",
    impact: "Research follows the existing host broker and task grants.",
  },
  {
    id: "lifecycle",
    label: "Lifecycle & labs",
    icon: "settings",
    about: "Software releases and experimental context handling.",
    impact: "Updates restart the program; compaction applies to new runs.",
  },
] as const;
export type SettingsDomain = (typeof settingsDomains)[number]["id"];
export const searchProviders = [
  ["ddgs", "DuckDuckGo · no key"],
  ["brave-free", "Brave · key required"],
  ["tavily", "Tavily · key required"],
] as const;
export function settingsDraftValues(settings: Settings | null) {
  return {
    globalProxy: settings?.global_proxy ?? "",
    searchProxy: settings?.web_search_proxy ?? "",
    python: settings?.python_interpreter ?? "",
    workers: String(settings?.workers ?? 3),
    fallback: settings?.web_search_fallback_backend ?? "",
    requestBudget: String(settings?.web_search_task_request_budget ?? 40),
    paidBudget: String(settings?.web_search_task_paid_request_budget ?? 20),
    rate: String(settings?.web_search_rate_per_minute ?? 10),
    cacheTTL: String(settings?.web_search_cache_ttl_seconds ?? 3600),
    braveKey: "",
    tavilyKey: "",
  };
}
export type SettingsDraft = ReturnType<typeof settingsDraftValues>;
export type SettingsField = keyof SettingsDraft;
export type SettingsGroup =
  | "globalProxy"
  | "searchProxy"
  | "python"
  | "workers"
  | "research"
  | "braveKey"
  | "tavilyKey";
export function settingsSnapshot(settings: Settings): Settings {
  const { brave_search_api_key: _brave, tavily_search_api_key: _tavily, ...publicSettings } = settings;
  return publicSettings;
}
export function settingsPatch(
  group: SettingsGroup,
  draft: SettingsDraft,
): { patch: Partial<Settings>; fields: SettingsField[] } {
  switch (group) {
    case "globalProxy":
      return { patch: { global_proxy: draft.globalProxy.trim() }, fields: ["globalProxy"] };
    case "searchProxy":
      return { patch: { web_search_proxy: draft.searchProxy.trim() }, fields: ["searchProxy"] };
    case "python":
      return { patch: { python_interpreter: draft.python.trim() }, fields: ["python"] };
    case "workers": {
      const workers = Number(draft.workers);
      if (!Number.isInteger(workers) || workers <= 0) throw new Error("Concurrency must be an integer greater than 0");
      return { patch: { workers }, fields: ["workers"] };
    }
    case "braveKey":
      return { patch: { brave_search_api_key: draft.braveKey }, fields: ["braveKey"] };
    case "tavilyKey":
      return { patch: { tavily_search_api_key: draft.tavilyKey }, fields: ["tavilyKey"] };
    case "research": {
      const request = Number(draft.requestBudget),
        paid = Number(draft.paidBudget),
        rate = Number(draft.rate),
        cache = Number(draft.cacheTTL);
      if (
        !Number.isInteger(request) ||
        request < 1 ||
        !Number.isInteger(paid) ||
        paid < 0 ||
        !Number.isInteger(rate) ||
        rate < 1 ||
        !Number.isInteger(cache) ||
        cache < 60
      ) {
        throw new Error("Use whole numbers: request/rate budgets ≥ 1, paid budget ≥ 0, cache TTL ≥ 60 seconds");
      }
      return {
        patch: {
          web_search_fallback_backend: draft.fallback,
          web_search_task_request_budget: request,
          web_search_task_paid_request_budget: paid,
          web_search_rate_per_minute: rate,
          web_search_cache_ttl_seconds: cache,
        },
        fields: ["fallback", "requestBudget", "paidBudget", "rate", "cacheTTL"],
      };
    }
  }
}
export function settingsSearchProbe(settings: Settings, draft: SettingsDraft) {
  return {
    web_search_backend: settings.web_search_backend || "ddgs",
    web_search_proxy: draft.searchProxy.trim(),
    brave_search_api_key: draft.braveKey,
    tavily_search_api_key: draft.tavilyKey,
  };
}
