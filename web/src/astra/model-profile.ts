import type { FormGroup, FormValues } from "./forms";
import { formDocument } from "./forms";

export const retryDescriptions = {
  connect: [
    "Connection",
    "Retries connection failures before a response. Deterministic request rejections are returned immediately.",
  ],
  empty: ["Empty response", "Retries responses without content; model output limits are handled separately."],
  stream: [
    "Uncommitted stream",
    "Retries on the same provider before content is delivered. Committed output is never replayed by this layer.",
  ],
  breaker: [
    "Pool circuit breaker",
    "Sets failure threshold and cooldown. Authentication and quota failures can trip immediately.",
  ],
  intent: ["Intent rerun", "Controls reruns after a worker model error. These settings are global."],
} as const;
export type RetryLayer = keyof typeof retryDescriptions;
export const profileLayers: RetryLayer[] = ["connect", "empty", "stream"];
export const globalLayers: RetryLayer[] = [...profileLayers, "breaker", "intent"];

export function retryForm(layers: RetryLayer[], prefix = ""): FormGroup[] {
  return layers.map((key) => ({
    title: retryDescriptions[key][0],
    description: retryDescriptions[key][1],
    fields: [
      {
        key: `${prefix}${key}.attempts`,
        label: `${retryDescriptions[key][0]} attempts`,
        type: "number",
        min: -1,
        step: 1,
        hint: "0 inherits the default; -1 disables; positive values set the count.",
      },
      {
        key: `${prefix}${key}.interval_ms`,
        label: `${retryDescriptions[key][0]} interval (ms)`,
        type: "number",
        min: 0,
        step: 1,
        hint: "0 uses the default backoff; a positive value fixes the interval.",
      },
    ],
  }));
}

export const profileForm: FormGroup[] = [
  {
    title: "Connection",
    fields: [
      { key: "name", label: "Name", required: true },
      {
        key: "format",
        label: "Provider format",
        options: [
          ["anthropic", "Anthropic"],
          ["openai", "OpenAI Chat Completions"],
          ["openai-responses", "OpenAI Responses"],
        ],
      },
      { key: "model", label: "Model", required: true },
      {
        key: "api_key",
        label: "API key",
        type: "password",
        hint: "Leave blank when editing to retain the stored key.",
      },
      { key: "base_url", label: "Base URL", hint: "Optional provider or compatible gateway address." },
      { key: "proxy", label: "Provider proxy", hint: "Optional HTTP, HTTPS or SOCKS5 proxy for model requests." },
      {
        key: "session_header_key",
        label: "Session header",
        hint: "Optional header name; the runtime supplies its session ID.",
      },
      { key: "streaming", label: "Streaming output", type: "checkbox" },
    ],
  },
  {
    title: "Budgets and routing",
    fields: [
      {
        key: "rate_per_second",
        label: "Requests per second",
        type: "number",
        min: 0,
        step: "any",
        hint: "0 leaves this rate unlimited.",
      },
      {
        key: "rate_per_minute",
        label: "Requests per minute",
        type: "number",
        min: 0,
        step: "any",
        hint: "0 leaves this rate unlimited.",
      },
      {
        key: "context_window_k",
        label: "Context window (K tokens)",
        type: "number",
        min: 0,
        max: 1000,
        step: 1,
        hint: "0 uses the default context size.",
      },
      {
        key: "max_tokens",
        label: "Reply token limit",
        type: "number",
        min: 0,
        step: 1,
        hint: "0 sends no output cap.",
      },
      {
        key: "max_tokens_field",
        label: "Reply cap field",
        options: [
          ["", "max_tokens (default)"],
          ["max_completion_tokens", "max_completion_tokens"],
        ],
        when: (values) => values.format === "openai",
      },
      {
        key: "priority",
        label: "Pool priority",
        type: "number",
        step: 1,
        hint: "Higher priorities run first after the active profile.",
      },
      { key: "pool_exclude", label: "Exclude from fallback pool", type: "checkbox" },
    ],
  },
  {
    title: "Reasoning",
    fields: [
      {
        key: "thinking_type",
        label: "Thinking switch",
        options: [
          ["", "Provider default"],
          ["disabled", "Disabled"],
          ["enabled", "Enabled"],
        ],
      },
      {
        key: "reasoning_effort",
        label: "Reasoning effort",
        options: [
          ["", "Provider default"],
          ["low", "Low"],
          ["medium", "Medium"],
          ["high", "High"],
          ["xhigh", "Extra high"],
          ["max", "Maximum"],
        ],
      },
    ],
  },
  ...retryForm(profileLayers, "retry."),
];

export function profileDraft(profile?: FormValues): FormValues {
  const values: FormValues = {
    format: "anthropic",
    streaming: true,
    thinking_type: "",
    reasoning_effort: "",
    max_tokens_field: "",
  };
  for (const group of profileForm)
    for (const field of group.fields)
      if (!field.key.includes(".") && !(field.key in values))
        values[field.key] = field.type === "checkbox" ? false : field.type === "number" ? 0 : "";
  const retry = Object.fromEntries(profileLayers.map((key) => [key, { attempts: 0, interval_ms: 0 }]));
  if (profile?.retry && typeof profile.retry === "object") Object.assign(retry, structuredClone(profile.retry));
  // Credentials in a response are never copied into an edit form.
  return { ...values, ...profile, retry, api_key: "" };
}

export function profileDocument(values: FormValues): FormValues {
  const result = formDocument(profileForm, values);
  for (const key of ["name", "model", "base_url", "proxy", "session_header_key"])
    result[key] = String(result[key]).trim();
  result.max_tokens_field = result.format === "openai" ? result.max_tokens_field : "";
  if (values.id) result.id = Number(values.id);
  return result;
}

export function profileProbe(values: FormValues) {
  return {
    provider: values.format,
    model: values.model,
    base_url: values.base_url,
    proxy: values.proxy,
    api_key: values.api_key,
    profile_id: values.id ? Number(values.id) : undefined,
    thinking_type: values.thinking_type,
    reasoning_effort: values.reasoning_effort,
    streaming: values.streaming,
    session_header_key: String(values.session_header_key ?? "").trim(),
  };
}
