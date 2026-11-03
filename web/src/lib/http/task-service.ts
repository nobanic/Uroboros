import type {
  Activity,
  ArchiveBatchItem,
  AssetInterceptRuleInput,
  BatchCategoryItem,
  BatchControlItem,
  DeleteTaskOptions,
  DeleteTaskResult,
  Task,
  TaskArchive,
  TaskArchivePage,
  TaskCategory,
  TaskLLMResolutions,
  TaskTemplate,
} from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { inputFields, Resource, slice } from "./resources";

type TaskInput = {
  name?: string;
  categoryId?: number;
  description: string;
  goal: string;
  llmProfileIds?: number[];
  sourceTaskIds?: string[];
  companyIds?: number[];
  timeoutSeconds?: number;
  seedFirstIntent?: boolean;
  planHeartbeatSeconds?: number;
  coverageEnabled?: boolean;
  strictIsolation?: boolean;
  skipInitialKatana?: boolean;
  initialScout?: boolean | null;
  interceptRules?: AssetInterceptRuleInput[];
};
type TemplateInput = Pick<TaskTemplate, "name" | "description" | "goal" | "category_id" | "intercept_rules">;
type Control = { id: string; paused: boolean; queued: boolean; status: string };

/** Defaults belong to creation only: patches, null category removal and profile switching keep their own policies. */
const creationFields = [
  ["name", "name", ""],
  ["category_id", "categoryId", null],
  ["description", "description"],
  ["goal", "goal"],
  ["llm_profile_ids", "llmProfileIds", []],
  ["source_task_ids", "sourceTaskIds", []],
  ["company_ids", "companyIds", []],
  ["timeout_seconds", "timeoutSeconds", 0],
  ["seed_first_intent", "seedFirstIntent", false],
  ["plan_heartbeat_seconds", "planHeartbeatSeconds", 0],
  ["coverage_enabled", "coverageEnabled", true],
  ["strict_isolation", "strictIsolation", false],
  ["skip_initial_katana", "skipInitialKatana", false],
  ["initial_scout", "initialScout", null],
  ["intercept_rules", "interceptRules", []],
] as const;

export function taskService(wire: ApplicationWire) {
  const tasks = new Resource(wire, "/tasks");
  const categories = new Resource(wire, "/task-categories");
  const templates = new Resource(wire, "/task-templates");
  const archives = new Resource(wire, "/task-archives");
  // Reading a task encodes its ID; historical command routes deliberately interpolate it verbatim.
  const task = (id: string, ...parts: (string | number)[]) => tasks.at(String(id), ...parts);
  const intent = (id: string, intentId: string, operation: string) => task(id, "intents", String(intentId), operation);
  const archiveBatch = (operation: string, archive_ids: number[]) =>
    archives.at(operation, "batch").send<{ items: ArchiveBatchItem[] }>("POST", { archive_ids });
  return {
    tasks: () =>
      tasks
        .read<{ tasks: Task[]; active: string }>()
        .then((value) => ({ tasks: slice(value.tasks), active: value.active ?? "" })),
    task: (id: string) => tasks.at(encodeURIComponent(id)).read<Task>(),
    createTask: (input: TaskInput) => tasks.send<Task>("POST", inputFields(input, creationFields)),
    taskCategories: () => categories.list<TaskCategory>("categories"),
    updateTask: (id: string, input: { name?: string; pinned?: boolean }) => task(id).send<Task>("PATCH", input),
    renameTask: (id: string, name: string) => task(id).send<Task>("PATCH", { name }),
    pinTask: (id: string, pinned: boolean) => task(id).send<Task>("PATCH", { pinned }),
    createTaskCategory: (name: string) => categories.send<TaskCategory>("POST", { name }),
    renameTaskCategory: (id: number, name: string) => categories.at(String(id)).send<TaskCategory>("PATCH", { name }),
    deleteTaskCategory: (id: number) => categories.at(String(id)).send<{ deleted: number }>("DELETE"),
    updateTaskCategory: (taskId: string, categoryId?: number) =>
      task(taskId, "category").send<Task>("PATCH", { category_id: categoryId ?? null }),
    updateTasksCategory: (taskIds: string[], categoryId?: number) =>
      tasks.at("category", "batch").send<{ items: BatchCategoryItem[]; category: TaskCategory | null }>("POST", {
        task_ids: taskIds,
        category_id: categoryId ?? null,
      }),
    taskTemplates: () => templates.list<TaskTemplate>("templates"),
    createTaskTemplate: (input: TemplateInput) => templates.send<TaskTemplate>("POST", input),
    updateTaskTemplate: (id: number, input: Partial<TemplateInput>) =>
      templates.at(String(id)).send<TaskTemplate>("PATCH", input),
    deleteTaskTemplate: (id: number) => templates.at(String(id)).send<{ deleted: number }>("DELETE"),
    updateTaskLLMProfiles: (id: string, llmProfileIds: number[], activeLLMProfileId?: number) =>
      task(id, "llm").send<{
        id: string;
        llm_profile_ids: number[];
        active_llm_profile_id?: number;
        llm_failover_state: string;
        reopened_intents: number;
        switch_event?: Activity;
      }>("PUT", { llm_profile_ids: llmProfileIds, active_llm_profile_id: activeLLMProfileId ?? null }),
    deleteTask: (id: string, options: DeleteTaskOptions) => task(id).send<DeleteTaskResult>("DELETE", options),
    controlTask: (id: string, action: "pause" | "resume") => task(id, "control").send<Control>("POST", { action }),
    controlTasksBatch: (taskIds: string[], action: "pause" | "resume") =>
      tasks.at("control", "batch").send<{ items: BatchControlItem[] }>("POST", { task_ids: taskIds, action }),
    taskArchives: (input?: { page?: number; size?: number; q?: string; state?: string }) =>
      archives
        .query([
          ["page", input?.page ?? 1],
          ["size", input?.size ?? 20],
          ["q", input?.q ? input.q : undefined],
          ["state", input?.state ? input.state : undefined],
        ])
        .read<TaskArchivePage>()
        .then((value) => ({ ...value, items: slice(value.items) })),
    taskArchive: (id: number) => archives.at(String(id)).read<TaskArchive>(),
    archiveTask: (id: string) => task(id, "archive").send<TaskArchive>("POST"),
    archiveTasks: (taskIds: string[]) =>
      tasks.at("archive", "batch").send<{ items: ArchiveBatchItem[] }>("POST", { task_ids: taskIds }),
    restoreTaskArchive: (id: number) => archives.at(String(id), "restore").send<TaskArchive>("POST"),
    restoreTaskArchives: (archiveIds: number[]) => archiveBatch("restore", archiveIds),
    deleteTaskArchive: (id: number) => archives.at(String(id)).send<TaskArchive>("DELETE"),
    deleteTaskArchives: (archiveIds: number[]) => archiveBatch("delete", archiveIds),
    controlIntent: (
      taskId: string,
      intentId: string,
      action: "pause" | "resume" | "cancel",
      reason?: string,
      mode?: "soft" | "hard",
    ) =>
      intent(taskId, intentId, "control").send<{
        id: number;
        state: "paused" | "open" | "deleted" | "";
        deleted?: { intents: number; facts: number; findings: number; activities: number };
      }>("POST", { action, reason: reason ?? "", mode: mode ?? "soft" }),
    sendWorkerMessage: (taskId: string, intentId: string, message: string, requestId: string) =>
      intent(taskId, intentId, "messages").send<{
        id: number;
        state: "running";
        accepted: true;
        request_id: string;
      }>("POST", { message, request_id: requestId }),
    taskLLMResolution: (id: string) => task(id, "llm", "resolution").read<TaskLLMResolutions>(),
    rerunIntent: (taskId: string, intentId: string) =>
      intent(taskId, intentId, "rerun").send<{ id: string; reopened: number }>("POST"),
    rerunBlocked: (taskId: string) =>
      task(taskId, "intents", "rerun-blocked").send<{ id: string; reopened: number }>("POST"),
    setActive: (id: string) => new Resource(wire, "/active").send<{ active: string }>("POST", { id }),
  };
}
