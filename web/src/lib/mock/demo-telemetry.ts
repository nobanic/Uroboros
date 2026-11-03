import type { ConvTokenSummary, LLMProfile, LLMPoolMember, LLMPoolStatus, TokenTotal, TokenUsage } from '../types';

function usage(input: number, output: number, read: number, write: number): TokenTotal {
  return { input_tokens: input, output_tokens: output, cache_read_tokens: read, cache_write_tokens: write };
}

function workerUsage(worker: string, counters: TokenTotal): TokenUsage {
  return { worker, ...counters };
}

function conversationUsage(profile: number, at: string, counters: TokenTotal): ConvTokenSummary {
  return { llm_profile_id: profile, created_at: at, ...counters };
}

export function createUsageCatalog() {
  const tokenWorkers = [
    workerUsage("planner", usage(320000, 24000, 210000, 33000)),
    workerUsage("work#1", usage(288000, 21000, 190000, 28000)),
    workerUsage("work#2", usage(402000, 31000, 280000, 41000)),
    workerUsage("work#3", usage(274500, 20320, 210400, 30000)),
    workerUsage("workG", usage(88000, 5200, 60000, 8000)),
    workerUsage("work#6", usage(64000, 4100, 42000, 6000)),
    workerUsage("work#7", usage(132000, 8600, 90000, 12000)),
    workerUsage("work#8", usage(74000, 4700, 50000, 7000)),
    workerUsage("work#9", usage(82000, 5100, 56000, 8000)),
    workerUsage("work#10", usage(46000, 3200, 30000, 4500)),
    workerUsage("work#11", usage(96000, 6200, 64000, 9500)),
    workerUsage("work#12", usage(108000, 6800, 72000, 10000)),
    workerUsage("work#13", usage(144000, 9200, 96000, 14000)),
  ];
  const convTokens = [
    conversationUsage(1, "2026-07-25T10:00:00Z", usage(42000, 3800, 26000, 5200)),
    conversationUsage(2, "2026-07-24T16:00:00Z", usage(18000, 1600, 9000, 2100)),
  ];
  return { tokenWorkers, convTokens };
}

function commandRecord(id: number, worker: string, at: string, command: string, output: string, isError = false) {
  return {
    id, exploration_id: 1, worker, tool: 'bash', command: JSON.stringify({ command }),
    output, is_error: isError, created_at: at,
  };
}

interface InferenceOutcome { status: string; error?: string }

function inferenceRecord(
  model: string, id: number, at: string, session: string, worker: string,
  latency: number, counters: TokenTotal, outcome: InferenceOutcome = { status: 'ok' },
) {
  return {
    id, ts: at, model, profile_name: 'uroboros.lab demonstration record.', session_id: session,
    task_id: 't-001', worker, latency_ms: latency,
    input_tokens: counters.input_tokens, output_tokens: counters.output_tokens,
    cache_read: counters.cache_read_tokens, cache_write: counters.cache_write_tokens,
    status: outcome.status, ...(outcome.error === undefined ? {} : { error: outcome.error }),
  };
}

/** Read the profile once at seed time. Later profile edits cannot rewrite recorded history. */
export function createExecutionCatalog(profile: LLMProfile) {
  const commandRecords = [
    commandRecord(1, "worker-1", "2026-08-09T02:10:00Z", "curl -s 'https://www.acme.com/search?q=test'",
      "The mock tool completed successfully and returned demonstration evidence. Context: HTTP/1.1, 200, OK, Content-Length, 12034."),
    commandRecord(2, "worker-1", "2026-08-09T02:12:30Z", "sqlmap -u 'https://www.acme.com/search?q=1' --batch --dbs",
      "[*] available databases [2]:\n[*] acme_prod\n[*] information_schema\nback-end DBMS: MySQL >= 5.7"),
    commandRecord(3, "worker-2", "2026-08-09T02:15:00Z", "ffuf -u https://api.acme.com/FUZZ -w common.txt -mc 200,401,403",
      "v1       [Status: 200]\nhealth   [Status: 200]\norders   [Status: 401]\nadmin    [Status: 403]"),
    commandRecord(4, "worker-2", "2026-08-09T02:18:00Z", "nmap -sV -p- 203.0.113.10",
      "PORT    STATE SERVICE VERSION\n22/tcp  open  ssh     OpenSSH 8.9\n443/tcp open  https   nginx 1.24"),
    commandRecord(5, "worker-1", "2026-08-09T02:20:00Z", "curl -s https://admin.acme.com/login --data 'user=admin&pass=admin123'",
      "curl: (28) Operation timed out after 10000 ms", true),
  ];
  const llmRecords = [
    inferenceRecord(profile.model, 1, "2026-08-09T02:10:02Z", "exp1-worker-i12", "worker-1", 3210,
      usage(8421, 512, 6000, 1200)),
    inferenceRecord(profile.model, 2, "2026-08-09T02:11:40Z", "exp1-planner", "planner", 4180,
      usage(12044, 733, 9000, 1500)),
    inferenceRecord(profile.model, 3, "2026-08-09T02:15:20Z", "exp1-worker-i18", "worker-2", 2890,
      usage(7311, 421, 5200, 900)),
    inferenceRecord(profile.model, 4, "2026-08-09T02:16:05Z", "exp1-worker-i18", "worker-2", 900,
      usage(7600, 0, 5200, 0), { "status": "error", "error": "uroboros.lab demonstration record. Context: 429, Too, Many, Requests." }),
  ];
  return { commandRecords, llmRecords };
}

function poolMember(profile: LLMProfile, active: boolean, cooldown = 0, lastFailure?: { error: string; at: string }): LLMPoolMember {
  return {
    profile_id: profile.id, name: profile.name, model: profile.model, format: profile.format,
    priority: profile.priority ?? 0, active, excluded: profile.pool_exclude ?? false,
    state: lastFailure ? 'tripped' : 'ok', fails: 0, trips: lastFailure ? 1 : 0, cooldown_secs: cooldown,
    ...(lastFailure ? { last_error: lastFailure.error, last_at: lastFailure.at } : {}),
  };
}

export function createPoolCatalog(profiles: LLMProfile[]): LLMPoolStatus {
  return {
    enabled: true, bind_fallback: false, chain: [
      poolMember(profiles[0], true),
      poolMember(profiles[1], false, 42, { "error": "openai: status 402: insufficient balance", "at": "2026-07-26T00:10:00Z" }),
    ]
  };
}
