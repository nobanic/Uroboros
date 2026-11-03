import type { NotificationChannel, NotificationDelivery, NotificationMeta } from "../types";
import { type DemoRequest, UNHANDLED } from "./demo-protocol";

type Seed = {
  kinds: NotificationMeta["kinds"];
  channels: NotificationChannel[];
  deliveries: NotificationDelivery[];
  enabled: boolean;
  baseURL: string;
  digestMinutes: string;
};

/** Notification state belongs to one runtime; reads retain original clone boundaries and sparse writes. */
export class NotificationRuntime {
  private readonly channels: NotificationChannel[];
  private readonly deliveries: NotificationDelivery[];
  private nextChannel = 3;

  constructor(private readonly seed: Seed) {
    this.channels = structuredClone(seed.channels);
    this.deliveries = structuredClone(seed.deliveries);
  }

  private metadata(): NotificationMeta {
    const count = (states: string[]) => this.deliveries.filter((item) => states.includes(item.state)).length;
    const pending = count(["pending", "sending"]);
    return {
      kinds: structuredClone(this.seed.kinds),
      enabled: this.seed.enabled,
      public_base_url: this.seed.baseURL,
      digest_interval_min: this.seed.digestMinutes,
      defaults: { digest_interval_min: 30 },
      stats: {
        channels: this.channels.length,
        channels_on: this.channels.filter((item) => item.enabled).length,
        pending,
        failed: count(["failed"]),
        sent_today: count(["sent"]),
        backlog_age_ms: pending > 0 ? 7 * 60_000 : 0,
      },
    };
  }

  private create(input: Partial<NotificationChannel>) {
    const kind = typeof input.kind === "string" ? input.kind : "dingtalk";
    const metadata = this.seed.kinds.find((item) => item.kind === kind);
    const now = new Date().toISOString();
    const channel: NotificationChannel = {
      id: this.nextChannel++,
      name: typeof input.name === "string" ? input.name : "Notification channel",
      kind,
      enabled: input.enabled ?? true,
      mode: input.mode === "digest" ? "digest" : "realtime",
      config: input.config ?? {},
      filter: input.filter ?? {},
      rate_per_min: input.rate_per_min ?? metadata?.default_rate_per_min ?? 0,
      created_at: now,
      updated_at: now,
      secret_keys: metadata?.secret_keys ?? [],
    };
    this.channels.push(channel);
    return { id: channel.id };
  }

  respond = ({ method, path, segments, query, body }: DemoRequest): unknown => {
    if (path === "/notify/meta" && method === "GET") return this.metadata();
    if (path === "/notify/channels") {
      if (method === "GET") return { channels: structuredClone(this.channels) };
      if (method === "POST") return this.create(body as Partial<NotificationChannel>);
    }
    // The prefix guard deliberately performs lookup before checking the action/method.
    if (segments[0] === "notify" && segments[1] === "channels" && segments.length >= 3) {
      const id = Number(segments[2]);
      const channel = this.channels.find((item) => item.id === id);
      if (!channel) throw new Error("Notification channel not found");
      if (segments[3] === "test" && method === "POST") return { ok: true, latency_ms: 84 };
      if (segments.length === 3 && method === "PATCH") {
        Object.assign(channel, body as Partial<NotificationChannel>, { updated_at: new Date().toISOString() });
        return { id };
      }
      if (segments.length === 3 && method === "DELETE") {
        this.channels.splice(this.channels.indexOf(channel), 1);
        return { ok: true };
      }
    }
    if (path === "/notify/deliveries" && method === "GET") {
      const channel = Number(query.get("channel_id")) || 0,
        state = query.get("state") ?? "";
      const page = Math.max(1, Number(query.get("page")) || 1),
        size = Math.min(100, Math.max(1, Number(query.get("page_size")) || 50));
      const selected = this.deliveries.filter(
        (item) => (!channel || item.channel_id === channel) && (!state || item.state === state),
      );
      const offset = (page - 1) * size;
      return {
        deliveries: structuredClone(selected.slice(offset, offset + size)),
        total: selected.length,
        page,
        page_size: size,
      };
    }
    if (segments[0] === "notify" && segments[1] === "deliveries" && segments[3] === "retry" && method === "POST") {
      const delivery = this.deliveries.find((item) => item.id === Number(segments[2]));
      if (!delivery) throw new Error("Notification delivery not found");
      Object.assign(delivery, { state: "pending", last_error: "", next_attempt_at: new Date().toISOString() });
      return { ok: true };
    }
    return UNHANDLED;
  };
}
