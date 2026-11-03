import type {
  CommandRecord,
  LLMRecordDetail,
  LLMRecordItem,
  LLMTask,
  ModelTokenStat,
  NotificationChannel,
  NotificationDelivery,
  NotificationFilter,
  NotificationMeta,
  ToolStat,
  TrafficDetail,
  TrafficHost,
  TrafficResp,
} from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { type QueryEntry, Resource, slice } from "./resources";

type TrafficFilter = {
  body?: string;
  path?: string;
  status?: string;
  respMin?: string;
  respMax?: string;
  sort?: string;
  order?: string;
};
type ChannelInput = {
  name: string;
  kind: string;
  enabled?: boolean;
  mode?: string;
  config: Record<string, unknown>;
  filter?: NotificationFilter;
  rate_per_min?: number;
};
type ChannelPatch = Partial<ChannelInput>;
type CommandFilter = { task?: string; q?: string };
const present = <T>(value: T): T | undefined => (value ? value : undefined);
const commandFields = (filter?: CommandFilter): QueryEntry[] => [
  ["task", present(filter?.task)],
  ["q", present(filter?.q)],
];

/** Inspection queries preserve each reader's page origin, field order and omission semantics. No reader starts a run. */
export function inspectionService(wire: ApplicationWire) {
  const traffic = new Resource(wire, "/traffic");
  const notify = new Resource(wire, "/notify");
  const channels = notify.at("channels");
  const deliveries = notify.at("deliveries");
  const commands = new Resource(wire, "/commands");
  const records = new Resource(wire, "/llm/records");
  return {
    traffic: (page = 0, size = 100, host = "", method = "", q = "", opts: TrafficFilter = {}) =>
      traffic
        .query(
          [
            ["page", page],
            ["size", size],
            ["host", present(host)],
            ["method", method !== "all" ? present(method) : undefined],
            ["q", present(q)],
            ["body", present(opts.body)],
            ["path", present(opts.path)],
            ["status", opts.status !== "all" ? present(opts.status) : undefined],
            ["resp_min", present(opts.respMin)],
            ["resp_max", present(opts.respMax)],
            ["sort", present(opts.sort)],
            ["order", present(opts.order)],
          ],
          "component",
        )
        .read<TrafficResp>(),
    trafficExchange: (id: string) =>
      traffic
        .at("exchange")
        .query([["id", String(id)]], "component")
        .read<TrafficDetail>(),
    trafficHosts: () => traffic.at("hosts").read<{ hosts: TrafficHost[] }>(),
    trafficDeleteHost: (host: string) =>
      traffic.query([["host", String(host)]], "component").send<{ deleted: number }>("DELETE"),
    trafficDeleteHosts: (hosts: string[]) => traffic.at("hosts").send<{ deleted: number }>("DELETE", { hosts }),
    trafficDeleteAll: () => traffic.at("all").send<{ deleted: number; reclaimed: number }>("DELETE"),
    notifyMeta: () => notify.at("meta").read<NotificationMeta>(),
    notifyChannels: () => channels.list<NotificationChannel>("channels"),
    notifyCreateChannel: (payload: ChannelInput) => channels.send<{ id: number }>("POST", payload),
    notifyUpdateChannel: (id: number, payload: ChannelPatch) =>
      channels.at(String(id)).send<{ id: number }>("PATCH", payload),
    notifyDeleteChannel: (id: number) => channels.at(String(id)).send<{ ok: boolean }>("DELETE"),
    notifyTestChannel: (id: number) =>
      channels.at(String(id), "test").send<{ ok: boolean; latency_ms: number }>("POST"),
    notifyDeliveries: (q: { channelId?: number; state?: string; page?: number; pageSize?: number } = {}) =>
      deliveries
        .query([
          ["channel_id", present(q.channelId)],
          ["state", present(q.state)],
          ["page", q.page ?? 1],
          ["page_size", q.pageSize ?? 50],
        ])
        .read<{ deliveries: NotificationDelivery[]; total: number; page: number; page_size: number }>()
        .then((value) => ({ ...value, deliveries: slice(value.deliveries) })),
    notifyRetryDelivery: (id: number) => deliveries.at(String(id), "retry").send<{ ok: boolean }>("POST"),
    commands: (params?: CommandFilter & { page?: number; size?: number }) =>
      commands
        .query([...commandFields(params), ["page", params?.page ?? 0], ["size", params?.size ?? 50]])
        .read<{ commands: CommandRecord[]; total: number }>(),
    commandStats: (params?: CommandFilter) =>
      commands.at("stats").query(commandFields(params)).read<{ stats: ToolStat[] }>(),
    llmRecords: (params?: { model?: string; session?: string; task?: string; page?: number; size?: number }) =>
      records
        .query([
          ["model", present(params?.model)],
          ["session", present(params?.session)],
          ["task", present(params?.task)],
          ["page", params?.page !== undefined ? String(params.page) : undefined],
          ["size", params?.size ?? 50],
        ])
        .read<{ records: LLMRecordItem[]; total: number }>(),
    llmRecordDetail: (id: number) => records.at(String(id)).read<LLMRecordDetail>(),
    llmTasks: () => records.at("tasks").read<{ tasks: LLMTask[] }>(),
    llmRecordsDeleteTask: (task: string) =>
      records.query([["task", String(task)]], "component").send<{ deleted: number }>("DELETE"),
    tokensByModel: (task: string) =>
      records
        .at("by-model")
        .query([["task", String(task)]], "component")
        .read<{ models: ModelTokenStat[] }>(),
  };
}
