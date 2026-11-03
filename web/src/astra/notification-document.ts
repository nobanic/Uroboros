import type { NotificationChannel, NotificationFilter } from "@/lib/types";

export type NotificationDraft = {
  name: string;
  kind: string;
  mode: "realtime" | "digest";
  enabled: boolean;
  ratePerMin: string;
  config: Record<string, unknown>;
  minSeverity: string;
  includeText: string;
  excludeText: string;
  taskIDsText: string;
  assetIDsText: string;
  onStatusChange: boolean;
};
type Control = "text" | "password" | "number" | "select" | "textarea" | "switch" | "kv" | "list";
export type NotificationField = {
  key: string;
  label: string;
  control: Control;
  hint?: string;
  placeholder?: string;
  options?: readonly [string, string][];
};
type FieldSpec = Omit<NotificationField, "key">;
type ChannelSpec = { label: string; fields: Record<string, FieldSpec> };
const text = (label: string, hint?: string): FieldSpec => ({ label, control: "text", hint });
const password = (label: string, hint?: string): FieldSpec => ({ label, control: "password", hint });

/** UI controls and wire codecs share one closed channel description; backend metadata owns secrets/default rates. */
const channels: Record<string, ChannelSpec> = {
  dingtalk: {
    label: "DingTalk",
    fields: {
      webhook: text("Webhook URL"),
      secret: password(
        "Signing secret",
        "Required when the bot uses signed messages. Leave empty for custom keyword security.",
      ),
    },
  },
  feishu: {
    label: "Feishu",
    fields: {
      webhook: text("Webhook URL"),
      secret: password("Signature verification secret", "Required when signature verification is enabled on the bot."),
    },
  },
  wecom: { label: "WeCom", fields: { webhook: text("Webhook URL") } },
  webhook: {
    label: "Generic Webhook",
    fields: {
      url: text("Target URL"),
      method: {
        label: "Request method",
        control: "select",
        options: [
          ["POST", "POST (with body)"],
          ["PUT", "PUT (with body)"],
          ["PATCH", "PATCH (with body)"],
          ["GET", "GET (no body)"],
        ],
      },
      headers: {
        label: "Custom request headers",
        control: "kv",
        hint: "One KEY=VALUE per line. Saved headers are masked together; keep the saved value or explicitly replace the entire set.",
      },
      body_template: {
        label: "Request body template",
        control: "textarea",
        hint: "Empty uses the default Go template. Variables: .Title, .Batch, .Count, .HomeURL, .SentAt; range .Items provides .Name, .VulnClass, .Severity, .Summary, .Assets, .DetailURL and .StatusLabel. Use {{json .Xxx}} to quote strings safely.",
      },
    },
  },
  telegram: {
    label: "Telegram",
    fields: {
      bot_token: password("Bot Token"),
      chat_id: text("Chat ID"),
      base_url: text("API URL", "Empty uses https://api.telegram.org. A self-hosted Bot API may use another URL."),
    },
  },
  email: {
    label: "Email",
    fields: {
      host: text("SMTP server"),
      port: {
        label: "Port",
        control: "number",
        placeholder: "587",
        hint: "587 uses STARTTLS automatically; 465 uses implicit TLS.",
      },
      username: text("Account"),
      password: password("Password / app code"),
      from: text("Sender"),
      to: { label: "Recipients", control: "list", hint: "Separate addresses with commas or whitespace." },
      tls: {
        label: "Implicit TLS",
        control: "switch",
        hint: "Enable for port 465. Keep off for STARTTLS on port 587.",
      },
    },
  },
};
export const notificationKind = (kind: string) => channels[kind]?.label ?? kind;
export const notificationFields = (kind: string): NotificationField[] =>
  Object.entries(channels[kind]?.fields ?? {}).map(([key, spec]) => ({ key, ...spec }));
export const notificationSeverities = [
  ["", "Any"],
  ["low", "Low and above"],
  ["medium", "Medium and above"],
  ["high", "High and above"],
  ["critical", "Critical only"],
] as const;
export const notificationStates = ["pending", "sending", "sent", "failed", "skipped"] as const;
export function notificationDraft(kind: string, saved?: NotificationChannel): NotificationDraft {
  const filter = saved?.filter;
  return {
    name: saved?.name ?? "",
    kind: saved?.kind ?? kind,
    mode: saved?.mode ?? "realtime",
    enabled: saved?.enabled ?? true,
    ratePerMin: saved ? String(saved.rate_per_min) : "",
    config: { ...saved?.config },
    minSeverity: filter?.min_severity ?? "",
    includeText: filter?.vulnclass_include?.join("\n") ?? "",
    excludeText: filter?.vulnclass_exclude?.join("\n") ?? "",
    taskIDsText: filter?.task_ids?.join(",") ?? "",
    assetIDsText: filter?.asset_ids?.join(",") ?? "",
    onStatusChange: filter?.on_status_change ?? false,
  };
}
const tokens = (value: string, separator: RegExp) =>
  value
    .split(separator)
    .map((part) => part.trim())
    .filter(Boolean);
export const notificationIDs = (value: string) =>
  tokens(value, /[\s,]+/)
    .map(Number)
    .filter((id) => Number.isFinite(id) && id > 0);
export const notificationKeywords = (value: string) => tokens(value, /[\n,]+/);
export function notificationHeaders(value: string): Record<string, string> {
  const headers: Record<string, string> = {};
  for (const entry of tokens(value, /\n/)) {
    const separator = entry.indexOf("=");
    if (separator > 0) headers[entry.slice(0, separator).trim()] = entry.slice(separator + 1).trim();
  }
  return headers;
}
export const notificationMasked = (value: unknown): value is string =>
  typeof value === "string" && value.startsWith("__masked__");
const codecs: Record<Control, (value: unknown) => unknown> = {
  switch: (value) => value === true,
  number: (value) => (Number.isFinite(Number(value)) && Number(value) > 0 ? Number(value) : 0),
  kv: (value) => notificationHeaders(String(value ?? "")),
  list: (value) => tokens(String(value ?? ""), /[\s,]+/),
  text: (value) => String(value ?? "").trim(),
  password: (value) => String(value ?? "").trim(),
  select: (value) => String(value ?? "").trim(),
  textarea: (value) => String(value ?? "").trim(),
};
export function notificationConfig(draft: NotificationDraft) {
  return Object.fromEntries(
    notificationFields(draft.kind).map((field) => {
      const value = draft.config[field.key];
      return [
        field.key,
        field.control !== "switch" && notificationMasked(value) ? value : codecs[field.control](value),
      ];
    }),
  );
}
export function notificationFilter(draft: NotificationDraft): NotificationFilter {
  return {
    min_severity: draft.minSeverity || undefined,
    vulnclass_include: notificationKeywords(draft.includeText),
    vulnclass_exclude: notificationKeywords(draft.excludeText),
    task_ids: notificationIDs(draft.taskIDsText),
    asset_ids: notificationIDs(draft.assetIDsText),
    on_status_change: draft.onStatusChange,
  };
}
export const notificationPayload = (draft: NotificationDraft) => ({
  name: draft.name.trim(),
  kind: draft.kind,
  mode: draft.mode,
  enabled: draft.enabled,
  config: notificationConfig(draft),
  filter: notificationFilter(draft),
  rate_per_min: draft.ratePerMin.trim() ? Number(draft.ratePerMin) : undefined,
});
export function notificationDefaults(baseURL: string, interval: string): Record<string, string | number> {
  const patch: Record<string, string | number> = { notify_public_base_url: baseURL.trim() },
    minutes = Number(interval);
  if (Number.isFinite(minutes) && minutes > 0) patch.notify_digest_interval_min = minutes;
  return patch;
}
export function notificationBacklog(ms: number) {
  if (!ms) return "—";
  if (ms < 60_000) return `${Math.round(ms / 1_000)} sec`;
  if (ms < 3_600_000) return `${Math.round(ms / 60_000)} min`;
  return `${(ms / 3_600_000).toFixed(1)} hr`;
}
export function notificationConstraints(filter: NotificationFilter): string[] {
  const parts: string[] = [];
  if (filter.min_severity)
    parts.push(notificationSeverities.find(([value]) => value === filter.min_severity)?.[1] ?? filter.min_severity);
  for (const [values, label] of [
    [filter.vulnclass_include, "included types"],
    [filter.vulnclass_exclude, "excluded types"],
    [filter.task_ids, "tasks"],
    [filter.asset_ids, "assets"],
  ] as const)
    if (values?.length) parts.push(`${values.length} ${label}`);
  if (filter.on_status_change) parts.push("Status changes");
  return parts.length ? parts : ["All vulnerabilities"];
}
export const notificationCanRetry = (state: string) => state === "failed" || state === "skipped";
