import type {
  ArchiveProviderArtifact,
  ArchiveProviderGrant,
  ArchiveProviderGrantView,
  AssetInterceptRule,
  AssetInterceptRuleInput,
  CoverageAssetRefs,
  CoverageGraphData,
  ResearchArtifact,
  ResearchDisclosureGrant,
  ResearchEgressGrant,
  ResearchEgressGrantView,
  ResearchGrantView,
  ResearchProvider,
  TaskConstraint,
  TaskGoal,
  TaskScopeRow,
} from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { Resource, slice } from "./resources";

type ArchiveGrant = {
  domain: string;
  capture_from: string;
  capture_to: string;
  expires_at: string;
  notice_version: string;
  acknowledged: true;
};
type DisclosureGrant = {
  provider?: ResearchProvider;
  expires_at?: string;
  max_requests?: number;
  notice_version: string;
  acknowledged: true;
};
type EgressGrant = {
  profile_id: "tor";
  seeds: string[];
  expires_at?: string;
  max_requests?: number;
  notice_version: "research-egress-v1";
  acknowledged: true;
};
type Coverage = {
  enabled: boolean;
  scope_rows: number;
  denominator: number;
  tested: number;
  pct: number | null;
  by_type: { type: string; total: number; tested: number }[];
};

/** Task-scoped policy paths never fall back to the active task or implicitly grant disclosure/egress. */
export function taskPolicyService(wire: ApplicationWire) {
  const tasks = new Resource(wire, "/tasks");
  const part = (task: string, ...segments: (string | number)[]) => tasks.at(String(task), ...segments);
  const grants = (task: string, kind: "archive-provider" | "research", collection = "grants") =>
    part(task, kind, collection);
  const goal = (task: string, goalId: string) => part(task, "goals", String(goalId));
  const constraint = (task: string, id: string) => part(task, "constraints", String(id));
  const rule = (task: string, id: number) => part(task, "intercept-rules", String(id));
  const revoke = (source: Resource, id: number) => source.at(String(id)).send<{ revoked: number }>("DELETE");
  return {
    archiveProviderGrants: (taskId: string) =>
      grants(taskId, "archive-provider").slices<ArchiveProviderGrantView>(["grants"]),
    createArchiveProviderGrant: (taskId: string, input: ArchiveGrant) =>
      grants(taskId, "archive-provider").send<ArchiveProviderGrant>("POST", input),
    revokeArchiveProviderGrant: (taskId: string, grantId: number) =>
      revoke(grants(taskId, "archive-provider"), grantId),
    archiveProviderArtifacts: (taskId: string, limit = 100, offset = 0) =>
      grants(taskId, "archive-provider", "artifacts")
        .query(
          [
            ["limit", limit],
            ["offset", offset],
          ],
          "component",
        )
        .slices<{ items: ArchiveProviderArtifact[]; raw_retained: false }>(["items"]),
    researchGrants: (taskId: string) =>
      grants(taskId, "research").slices<ResearchGrantView>(["grants", "disclosure_identities"]),
    createResearchGrant: (taskId: string, input: DisclosureGrant) =>
      grants(taskId, "research").send<ResearchDisclosureGrant>("POST", input),
    revokeResearchGrant: (taskId: string, grantId: number) => revoke(grants(taskId, "research"), grantId),
    researchEgressGrants: (taskId: string) =>
      grants(taskId, "research", "egress-grants").slices<ResearchEgressGrantView>(["grants"]),
    createResearchEgressGrant: (taskId: string, input: EgressGrant) =>
      grants(taskId, "research", "egress-grants").send<ResearchEgressGrant>("POST", input),
    revokeResearchEgressGrant: (taskId: string, grantId: number) =>
      revoke(grants(taskId, "research", "egress-grants"), grantId),
    researchArtifacts: (taskId: string, limit = 100) =>
      grants(taskId, "research", "artifacts")
        .query([["limit", limit]], "component")
        .slices<{ artifacts: ResearchArtifact[]; raw_retained: false; finding_authority: false }>(["artifacts"]),
    taskCoverage: (id: string) => part(id, "coverage").read<Coverage>(),
    taskCoverageGraph: (id: string) => part(id, "coverage-graph").read<CoverageGraphData>(),
    taskGoals: (id: string) =>
      part(id, "goals")
        .read<{ goals: TaskGoal[] | null }>()
        .then((value) => ({ goals: slice(value.goals) })),
    addGoal: (id: string, text: string, vulnclass?: string) =>
      part(id, "goals").send<TaskGoal>("POST", { text, vulnclass: vulnclass ?? "" }),
    updateGoal: (id: string, goalId: string, text: string, vulnclass?: string) =>
      goal(id, goalId).send<TaskGoal>("PATCH", { text, vulnclass: vulnclass ?? "" }),
    deleteGoal: (id: string, goalId: string) => goal(id, goalId).send<{ ok: boolean }>("DELETE"),
    taskConstraints: (id: string) =>
      part(id, "constraints")
        .read<{ constraints: TaskConstraint[] | null }>()
        .then((value) => ({ constraints: slice(value.constraints) })),
    addConstraint: (id: string, text: string, kind: TaskConstraint["kind"]) =>
      part(id, "constraints").send<TaskConstraint>("POST", { text, kind }),
    updateConstraint: (id: string, constraintId: string, text: string, kind: TaskConstraint["kind"]) =>
      constraint(id, constraintId).send<TaskConstraint>("PATCH", { text, kind }),
    deleteConstraint: (id: string, constraintId: string) =>
      constraint(id, constraintId).send<{ ok: boolean }>("DELETE"),
    taskInterceptRules: (id: string) => part(id, "intercept-rules").list<AssetInterceptRule>("rules"),
    createTaskInterceptRule: (id: string, input: AssetInterceptRuleInput) =>
      part(id, "intercept-rules").send<AssetInterceptRule>("POST", input),
    updateTaskInterceptRule: (id: string, ruleId: number, input: AssetInterceptRuleInput) =>
      rule(id, ruleId).send<AssetInterceptRule>("PUT", input),
    deleteTaskInterceptRule: (id: string, ruleId: number) => rule(id, ruleId).send<{ deleted: boolean }>("DELETE"),
    toggleTaskInterceptRule: (id: string, ruleId: number, enabled: boolean) =>
      rule(id, ruleId).at("toggle").send<{ ok: boolean; enabled: boolean }>("POST", { enabled }),
    taskScope: (id: string) => part(id, "scope").read<{ scope: TaskScopeRow[] }>(),
    addTaskScope: (id: string, kind: string, value: string, reason?: string) =>
      part(id, "scope").send<TaskScopeRow>("POST", { kind, value, reason }),
    deleteTaskScope: (id: string, scopeId: number) =>
      part(id, "scope", String(scopeId)).send<{ ok: boolean }>("DELETE"),
    taskAssetRefs: (id: string, assetId: number) =>
      part(id, "asset-refs")
        .query([["asset_id", String(assetId)]], "component")
        .read<CoverageAssetRefs>(),
  };
}
