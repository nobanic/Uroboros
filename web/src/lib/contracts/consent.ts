import type { RecordTimestamps } from "./record";

// These are retained approval/projection records, not a new grant evaluator.
// Provider scope, notices and outcomes remain concrete on their public models.
export interface ApprovalLifecycle<Contract extends string = string, Notice extends string = string>
  extends RecordTimestamps {
  id: number;
  task_id: number;
  contract_version: Contract;
  notice_version: Notice;
  approved_by: string;
  approved_at: string;
  expires_at: string;
  revoked_at?: string;
  revoked_by?: string;
}

export interface CapturedAttempt<Outcome extends string> {
  id: number;
  attempt_id: string;
  request_sha256: string;
  response_status?: number;
  raw_body_sha256?: string;
  projection_sha256?: string;
  projection: Record<string, unknown>;
  outcome: Outcome;
  error_code?: string;
  started_at: string;
  finished_at: string;
  created_at: string;
}

export interface AssetRuleDefinition<Kind extends string> {
  kind: Kind;
  pattern: string;
  note: string;
  enabled: boolean;
}
