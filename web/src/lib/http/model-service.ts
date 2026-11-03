import type { LLMPoolStatus, LLMProfile, LLMRetryOverride, LLMRetryPolicy } from "@/lib/types";

import type { ApplicationWire } from "./application-wire";
import { Resource } from "./resources";

type Connection = {
  configured: boolean;
  provider: string;
  model: string;
  base_url: string;
  proxy?: string;
  key_set: boolean;
  rate_per_second?: number;
  rate_per_minute?: number;
  context_window_k?: number;
  thinking_type?: string;
  reasoning_effort?: string;
};
type ProfileInput = {
  id?: number;
  name: string;
  format: string;
  model: string;
  base_url?: string;
  proxy?: string;
  api_key?: string;
  rate_per_second?: number;
  rate_per_minute?: number;
  context_window_k?: number;
  thinking_type?: string;
  reasoning_effort?: string;
  priority?: number;
  pool_exclude?: boolean;
  streaming?: boolean;
  max_tokens?: number;
  max_tokens_field?: string;
  session_header_key?: string;
  retry?: LLMRetryOverride;
};
type Probe = { ok: boolean; error?: string; latency_ms?: number; model?: string; reply?: string };

/** Connection edits and profile probes retain exact credential, streaming and retry fields; server owns provider execution. */
export function modelService(wire: ApplicationWire) {
  const root = new Resource(wire, "/llm");
  const profiles = root.at("profiles");
  const pool = root.at("pool");
  const retry = root.at("retry-policy");
  const connection = (provider: string, model: string, base_url: string, proxy: string, api_key: string) => ({
    provider,
    model,
    base_url,
    proxy,
    api_key,
  });
  return {
    getLLM: () => root.read<Connection>(),
    setLLM: (
      provider: string,
      model: string,
      base_url: string,
      api_key: string,
      rate_per_second = 0,
      rate_per_minute = 0,
      proxy = "",
      context_window_k = 0,
    ) =>
      root.send<unknown>("POST", {
        ...connection(provider, model, base_url, proxy, api_key),
        rate_per_second,
        rate_per_minute,
        context_window_k,
      }),
    testLLM: (
      provider: string,
      model: string,
      base_url: string,
      api_key: string,
      proxy = "",
      thinking_type = "",
      reasoning_effort = "",
      profile_id?: number,
      streaming = true,
      session_header_key = "",
    ) =>
      root.at("test").send<Probe>("POST", {
        ...connection(provider, model, base_url, proxy, api_key),
        thinking_type,
        reasoning_effort,
        profile_id,
        streaming,
        session_header_key,
      }),
    llmProfiles: () => profiles.list<LLMProfile>("profiles"),
    saveLLMProfile: (input: ProfileInput) => profiles.send<{ id: number }>("POST", input),
    deleteLLMProfile: (id: string) => profiles.at(String(id)).send<{ deleted: number }>("DELETE"),
    activateLLMProfile: (id: string) => profiles.at("active").send<{ ok: boolean }>("POST", { id: Number(id) }),
    llmPool: () => pool.read<LLMPoolStatus>(),
    resetLLMPool: (id?: string) => pool.at("reset").send<LLMPoolStatus>("POST", { id: id ? Number(id) : 0 }),
    llmRetryPolicy: () => retry.read<LLMRetryPolicy>(),
    saveLLMRetryPolicy: (input: LLMRetryPolicy) => retry.send<LLMRetryPolicy>("POST", input),
    fetchLLMModels: (provider: string, base_url: string, api_key: string, proxy = "", profile_id?: number) =>
      root.at("models").send<{ ok: boolean; error?: string; models?: string[] }>("POST", {
        provider,
        base_url,
        api_key,
        proxy,
        profile_id,
      }),
  };
}
