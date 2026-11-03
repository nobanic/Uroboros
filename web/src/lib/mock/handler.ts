import { DemoWorkers } from "./demo-workers";
import { DemoFindings } from "./demo-findings";
import { DemoAssets } from "./demo-assets";
import { DemoSessions } from "./demo-sessions";
import { FindingEvidence } from "./finding-evidence";
import { TaskArchives } from "./task-archives";
import { TaskRegistry } from "./task-registry";
import { TaskLabels } from "./task-labels";
import { surfaceCatalog } from "./surface-catalog";
import { AssetGuardRuntime } from "./asset-guard-runtime";
import { ApprovalRuntime } from "./approval-runtime";
import { DemoProtocol, UNHANDLED } from "./demo-protocol";
import { NotificationRuntime } from "./notification-runtime";
import { notificationSeed } from "./notification-seed";

import type { Task, TaskLLMResolution } from "../types";
import * as D from "./data";
import { showcaseEnglish } from "./showcase-english";
const mockTasks = structuredClone(D.tasks);
const mockFindings = structuredClone(D.findings);
const mockLLMRecords = structuredClone(D.llmRecords);
const mockTaskTemplates = structuredClone(D.taskTemplates);
const mockTaskCategories = structuredClone(D.taskCategories);
const sessions = new DemoSessions(D, (id) => mockFindings.find((finding) => finding.id === id));
const findingEvidence = new FindingEvidence(D.traffic.exchanges ?? [], (id) => mockFindings.some((finding) => finding.id === id));
const mockTrafficCount = (id: string) => findingEvidence.count(id);
const mockIntents = structuredClone(D.intents);
const assetModel = new DemoAssets(D.tasks, D.companies, D.assets, (id) => mockTasks.some((task) => task.id === id));
const mockCompanies = assetModel.companies.rows;
const mockAssets = assetModel.assets.rows;
const mockActivity = structuredClone(D.activity);
const workerModel = new DemoWorkers(mockIntents, mockActivity, D);
const findingModel = new DemoFindings(mockFindings, mockTasks, mockAssets, mockCompanies, mockTrafficCount);

function mockProfileResolution(profileID: number | undefined, source: TaskLLMResolution["source"]): TaskLLMResolution {
  const profile = D.llmProfiles.find((item) => Number(item.id) === profileID);
  if (!profile) {
    return { name: "", format: "", model: "", source, available: false, reason: "Recorded by the demonstration policy. Context: LLM." };
  }
  return {
    profile_id: Number(profile.id),
    name: profile.name,
    format: profile.format,
    model: profile.model,
    source,
    available: Boolean(profile.api_key_hint),
    reason: profile.api_key_hint ? undefined : "Recorded by the demonstration policy. Context: LLM, API, Key.",
  };
}

// Mirrors the backend precedence in server/task_resolution.go:
function mockRoleResolution(task: Task, agentKey: "mainagent" | "planner" | "worker"): TaskLLMResolution {
  const agent = D.agents.find((item) => item.key === agentKey);
  if (agent?.llm_profile_id) {
    const bound = mockProfileResolution(agent.llm_profile_id, "agent_binding");
    if (bound.available) return bound;
  }
  if (task.llm_profile_ids?.length) {
    if (task.active_llm_profile_id === undefined) {
      return {
        name: "",
        format: "",
        model: "",
        source: "task_chain",
        available: false,
        reason: "Recorded by the demonstration policy. Context: LLM.",
      };
    }
    return mockProfileResolution(task.active_llm_profile_id, "task_chain");
  }
  const globalProfile = D.llmProfiles.find((item) => item.is_default);
  if (globalProfile) return mockProfileResolution(Number(globalProfile.id), "global_profile");
  return {
    name: "uroboros.lab security record",
    format: D.llmConfig.provider,
    model: D.llmConfig.model,
    source: "environment",
    available: true,
  };
}

const taskLabels = new TaskLabels(mockTaskCategories, mockTaskTemplates, () => mockTasks);
const taskRegistry: TaskRegistry = new TaskRegistry(mockTasks, D.ACTIVE_TASK, taskLabels, {
  companies: mockCompanies,
  findings: mockFindings,
  records: mockLLMRecords,
  blocker: (id) => taskArchives.blocker(id),
  resolution: mockRoleResolution,
  attachCompanies: (id, companyIDs) => assetModel.attachCompanies(id, companyIDs),
  detach: (id) => assetModel.detachTask(id),
});

const taskArchives: TaskArchives = new TaskArchives(taskRegistry, {
  taskAssetID: (id) => assetModel.taskNumber(id),
  findings: mockFindings,
  records: mockLLMRecords,
  capture: (id, number) => assetModel.capture(id, number),
  retireAssets: (id, number) => assetModel.retire(id, number),
  restoreAssets: (snapshot) => assetModel.restore(snapshot.task.id, snapshot.numericTaskID, snapshot.assetIDs, snapshot.assetSources),
});

function route(m: string, path: string, seg: string[], q: URLSearchParams, b: Record<string, unknown>): unknown {
  const labels = taskLabels.respond({ method: m, path, segments: seg, query: q, body: b });
  if (labels !== UNHANDLED) return labels;
  const taskResult = taskRegistry.respond({ method: m, path, segments: seg, query: q, body: b });
  if (taskResult !== UNHANDLED) return taskResult;
  const archiveResult = taskArchives.respond({ method: m, path, segments: seg, query: q, body: b });
  if (archiveResult !== UNHANDLED) return archiveResult;
  const task = q.get("task") ?? undefined;
  const notification = notificationRuntime.respond({ method: m, path, segments: seg, query: q, body: b });
  if (notification !== UNHANDLED) return notification;
  for (const domain of [assetGuardRuntime, approvalRuntime, catalogRuntime]) {
    const response = domain.respond({ method: m, path, segments: seg, query: q, body: b });
    if (response !== UNHANDLED) return response;
  }

  if (path === "/chat/mentions" && m === "GET") {
    const kind = q.get("kind") ?? "";
    const query = (q.get("q") ?? "").trim().toLowerCase();
    const candidates = [
      ...D.findings.map((finding, index) => ({
        kind: "finding",
        id: index + 1,
        label: finding.name || finding.vulnclass,
        description: `${finding.severity} · ${finding.summary}`,
      })),
      ...D.companies.map((company) => ({ kind: "company", id: company.id, label: company.name, description: "Security assessment record for the uroboros.lab demonstration environment." })),
      ...D.assets.map((asset) => ({
        kind: asset.type,
        id: asset.id,
        label:
          asset.type === "endpoint"
            ? `${asset.method || "GET"} ${asset.url}`
            : asset.app_name || asset.url || asset.domain || asset.ip || asset.bundle_id || "uroboros.lab demo record Context: asset.id.",
        description: [asset.type, asset.page_title, asset.service_name, asset.bundle_id, asset.ip]
          .filter(Boolean)
          .join(" · "),
      })),
    ];
    const filtered = candidates
      .filter(
        (item) =>
          (!kind || kind === item.kind || (kind === "asset" && item.kind !== "finding" && item.kind !== "company")) &&
          (!query || String(item.id) === query || `${item.label} ${item.description}`.toLowerCase().includes(query)),
      )
      .sort(
        (a, b) =>
          Number(String(b.id) === query) - Number(String(a.id) === query) ||
          b.id - a.id ||
          a.kind.localeCompare(b.kind),
      );
    const offset = Math.max(0, Number(q.get("cursor")) || 0);
    return {
      items: filtered.slice(offset, offset + 20),
      next_cursor: offset + 20 < filtered.length ? String(offset + 20) : undefined,
    };
  }

  // ── task cold archives ──

  // ── tasks ──

  const workerResponse = workerModel.respond({ method: m, path, segments: seg, query: q, body: b });
  if (workerResponse !== UNHANDLED) return workerResponse;

  const assetResponse = assetModel.respond({ method: m, path, segments: seg, query: q, body: b });
  if (assetResponse !== UNHANDLED) return assetResponse;

  if (path === "/tokens/usage")
    return {
      by_profile: [
        {
          profile_name: "default",
          calls: 60,
          tasks: 4,
          input_tokens: 1570000,
          output_tokens: 110000,
          cache_read_tokens: 1120000,
          cache_write_tokens: 140000,
        },
      ],
      daily: [
        {
          profile_name: "default",
          date: "2026-08-18",
          input_tokens: 520000,
          output_tokens: 38000,
          cache_read_tokens: 370000,
        },
        {
          profile_name: "default",
          date: "2026-08-19",
          input_tokens: 640000,
          output_tokens: 45000,
          cache_read_tokens: 460000,
        },
        {
          profile_name: "default",
          date: "2026-08-20",
          input_tokens: 410000,
          output_tokens: 27000,
          cache_read_tokens: 290000,
        },
      ],
    };

  // ── stats ──
  if (path === "/stats") {
    return D.stats(task, { tasks: mockTasks, findings: mockFindings, activeTask: taskRegistry.active });
  }

  // ── assets ──

  // ── companies ──

  const findingResponse = findingModel.respond({ method: m, path, segments: seg, query: q, body: b });
  if (findingResponse !== UNHANDLED) return findingResponse;

  const sessionResponse = sessions.respond({ method: m, path, segments: seg, query: q, body: b });
  if (sessionResponse !== UNHANDLED) return sessionResponse;

  // seg = ["exploration","findings",<id>,"traffic", ...]
  if (seg[0] === "exploration" && seg[1] === "findings" && seg[3] === "traffic") return findingEvidence.respond({ method: m, path, segments: seg, query: q, body: b });

  if (path === "/exploration/tokens") {
    const selectedTask = task ? mockTasks.find((item) => item.id === task) : undefined;
    return {
      workers: D.tokenWorkers,
      sessions: D.tokenSessions,
      total: selectedTask?.tokens ?? D.tokenTotal,
    };
  }

  if (seg.at(-1) === "side-questions") {
    if (m === "GET") return { items: [], current: null, next_cursor: 0, snapshot: null };
    if (m === "POST") throw new Error("uroboros.lab demonstration record.");
  }
  return UNHANDLED;
}

const notificationRuntime = new NotificationRuntime(notificationSeed);
const protocol = new DemoProtocol({
  beforeRequest: () => sessions.advance(),
  resolve: ({ method, path, segments, query, body }) => route(method, path, segments, query, body),
  present: showcaseEnglish,
});
export const mockHandle = protocol.handle;

const assetGuardRuntime = new AssetGuardRuntime(D.assetInterceptRules);
const approvalRuntime = new ApprovalRuntime({ history: D.interceptHistory, pending: D.interceptPending, details: D.interceptDetails, rules: D.interceptRules });

const catalogRuntime = surfaceCatalog(() => mockLLMRecords);
