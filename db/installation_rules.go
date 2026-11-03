package db

import "fmt"

type installationCommandRule struct {
	name, target, typ, pattern, action, message string
	priority                                    int
	enabled                                     bool
}

// All shipped command-rule families share the same input/RE2/deny policy.
// Enabled and priority remain explicit, especially the disabled exfil rule.
func deniedInputRule(priority int, enabled bool, name, pattern, message string) installationCommandRule {
	return installationCommandRule{
		name: name, target: "tool_input", typ: "regex", pattern: pattern,
		action: "deny", message: message, priority: priority, enabled: enabled,
	}
}

func (c installationCatalog) writeRules(statement string, rules []installationCommandRule, values func(installationCommandRule) []any) error {
	for _, rule := range rules {
		if err := c.exec(statement, values(rule)...); err != nil {
			return fmt.Errorf("rule %q: %w", rule.name, err)
		}
	}
	return nil
}

func (c installationCatalog) commandRulesV1() error {
	return c.once(installationMarker{key: "intercept_default_rules_v1", value: "done"}, func() error {
		rules := []installationCommandRule{
			deniedInputRule(
				100,
				true,
				"[built-in] recursive forced delete rm -rf",
				`(?i)\brm\b.{0,80}(?:-[a-z]*r[a-z]*f[a-z]*|-[a-z]*f[a-z]*r[a-z]*|--recursive|--no-preserve-root)`,
				"Recursive forced delete (rm -rf / rm --recursive) is forbidden; it could permanently damage the system or the target environment",
			),
			deniedInputRule(
				100,
				true,
				"[built-in] delete critical system directories",
				`\brm\b[^"'\n]{0,60}["'\s](/|/etc|/bin|/usr|/boot|/var|/lib|/sys|/proc|/dev|/sbin|/root)`,
				"Deleting critical system paths is forbidden",
			),
			deniedInputRule(
				100,
				true,
				"[built-in] disk format mkfs",
				`\bmkfs\b`,
				"Formatting disks (mkfs) is forbidden",
			),
			deniedInputRule(
				100,
				true,
				"[built-in] overwrite disk device dd",
				`\bdd\b[^|\n]{0,100}\bof=\s*/dev/[a-zA-Z]`,
				"Using dd to overwrite a disk device is forbidden",
			),
			deniedInputRule(
				100,
				true,
				"[built-in] fork bomb",
				`:\(\)\s*\{[^}]*:\|:`,
				"Executing a fork bomb is forbidden",
			),
			deniedInputRule(
				100,
				true,
				"[built-in] shutdown / reboot",
				`\b(?:shutdown|reboot|halt|poweroff|init\s+[06])\b`,
				"Executing shutdown or reboot commands is forbidden",
			),
			deniedInputRule(
				100,
				true,
				"[built-in] kill all processes",
				`\bkill\s+-9\s+-1\b|\bkillall\s+-9\b`,
				"kill -9 -1 or killall -9 (killing all processes) is forbidden",
			),
			deniedInputRule(
				100,
				true,
				"[built-in] disk erase shred / wipe",
				`\b(?:shred|wipe)\b[^|\n]{0,80}/dev/[a-zA-Z]`,
				"Running shred/wipe erase on a disk device is forbidden",
			),
			deniedInputRule(
				100,
				true,
				"[built-in] flush firewall rules",
				`\biptables\s+(?:-F|--flush)\b|\bnft\s+flush\s+ruleset\b`,
				"Flushing firewall rules (iptables -F / nft flush) is forbidden",
			),
			deniedInputRule(
				90,
				true,
				"[built-in] SQL DROP DATABASE / TABLE / SCHEMA",
				`(?i)\bDROP\s+(?:DATABASE|TABLE|SCHEMA|INDEX|VIEW|TABLESPACE|USER|ROLE)\b`,
				"DROP operations are forbidden; they could irreversibly destroy database objects",
			),
			deniedInputRule(
				90,
				true,
				"[built-in] SQL TRUNCATE",
				`(?i)\bTRUNCATE\s+(?:TABLE\s+)?\w`,
				"TRUNCATE is forbidden; it could wipe all data in a table",
			),
			deniedInputRule(
				90,
				true,
				"[built-in] MongoDB drop / dropDatabase",
				`(?i)\.(?:dropDatabase|dropCollection|drop)\s*\(`,
				"MongoDB drop operations are forbidden",
			),
			deniedInputRule(
				90,
				true,
				"[built-in] Redis FLUSHALL / FLUSHDB",
				`(?i)\b(?:FLUSHALL|FLUSHDB)\b`,
				"Redis FLUSHALL / FLUSHDB is forbidden; it could wipe all cached data",
			),
			deniedInputRule(
				80,
				true,
				"[built-in] curl / wget sending a DELETE request",
				`(?i)\bcurl\b[^|\n&;"]{0,300}(?:-X\s*DELETE|--request\s+DELETE|-XDELETE)|\bwget\b[^|\n&;"]{0,300}--method[=\s]+DELETE`,
				"Sending an HTTP DELETE request via curl/wget is forbidden; it could delete data on the target system",
			),
			deniedInputRule(
				80,
				true,
				"[built-in] Python HTTP client DELETE (requests/httpx/aiohttp)",
				`(?i)\b(?:requests|httpx|aiohttp|urllib\.request)\.delete\s*\(|session\.delete\s*\(|client\.delete\s*\(`,
				"Using a Python HTTP client to send a DELETE request is forbidden",
			),
			deniedInputRule(
				80,
				true,
				"[built-in] script declaring an HTTP DELETE method (JS/generic)",
				`(?i)axios\.delete\s*\(|method\s*[:=]\s*['"]DELETE['"]`,
				"Declaring and sending an HTTP DELETE request in a script is forbidden",
			),
			deniedInputRule(
				80,
				true,
				"[built-in] bulk clear / purge endpoint path",
				`(?i)/(?:clear|wipe|flush|purge|truncate|drop|destroy|factory[-_]reset|reset[-_]all)(?:[/?#"'\s]|$)`,
				"Calling bulk-clear or destroy endpoints (/clear /wipe /flush /purge, etc.) is forbidden",
			),
		}
		return c.writeRules(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, true, $2, $3, $4, $5, $6, $7, false, 60, 'deny')
ON CONFLICT DO NOTHING`, rules, func(rule installationCommandRule) []any {
			return []any{rule.name, rule.priority, rule.target, rule.typ, rule.pattern, rule.action, rule.message}
		})
	})
}

func (c installationCatalog) commandRulesV2() error {
	return c.once(installationMarker{key: "intercept_default_rules_v2", value: "done"}, func() error {
		// Ordinary operator-editable rules, including disabled-by-default exfil.
		rules := []installationCommandRule{
			deniedInputRule(
				100,
				true,
				"[built-in] destructive system command",
				`(?i)\b(rm\s+-rf\s+/|mkfs|dd\s+if=|:\(\)\s*\{|shutdown|reboot|>\s*/dev/sd)`,
				"Destructive command rejected (rm -rf / / mkfs / dd / fork bomb / shutdown-reboot / overwrite disk device)",
			),
			deniedInputRule(
				80,
				false,
				"[built-in] data-exfiltration pipe",
				`(?i)(curl|wget|nc|ncat)\b[^|]*\b(\|\s*(curl|wget|nc))`,
				"Suspected data-exfiltration pipe rejected (command output piped out via curl/wget/nc)",
			),
		}
		return c.writeRules(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
VALUES ($1, $2, $3, 'tool_input', 'regex', $4, $5, $6, false, 60, 'deny')
ON CONFLICT DO NOTHING`, rules, func(rule installationCommandRule) []any {
			return []any{rule.name, rule.enabled, rule.priority, rule.pattern, rule.action, rule.message}
		})
	})
}

func (c installationCatalog) commandRulesV3() error {
	return c.once(installationMarker{key: "intercept_default_rules_v3", value: "done"}, func() error {
		const name = "[built-in] delete-type endpoint path"
		rules := []installationCommandRule{
			deniedInputRule(
				80,
				true,
				name,
				deleteEndpointPathPattern,
				"Calling delete-type endpoints (/delete /remove /unlink /erase, etc.) is forbidden regardless of HTTP method — most apps' delete endpoints fire on GET/POST and will really delete target data",
			),
		}
		// Unlike v1/v2, this version retains its name-based NOT EXISTS condition.
		return c.writeRules(`
INSERT INTO intercept_rules(name, enabled, priority, match_target, match_type, pattern, action, message, timeout_enabled, timeout_seconds, timeout_action)
SELECT $1, true, 80, 'tool_input', 'regex', $2, 'deny', $3, false, 60, 'deny'
WHERE NOT EXISTS (SELECT 1 FROM intercept_rules WHERE name = $1)`, rules, func(rule installationCommandRule) []any {
			return []any{rule.name, rule.pattern, rule.message}
		})
	})
}

func (c installationCatalog) assetRules() error {
	return c.once(installationMarker{key: "asset_intercept_default_rules_v1", value: "done"}, func() error {
		for _, rule := range defaultAssetInterceptRules {
			if err := c.exec(`
INSERT INTO asset_intercept_rules(enabled, kind, pattern, note, builtin)
VALUES (true, $1, $2, $3, true)
ON CONFLICT DO NOTHING`, rule.kind, rule.pattern, rule.note); err != nil {
				return fmt.Errorf("asset rule %q: %w", rule.pattern, err)
			}
		}
		return nil
	})
}

func (c installationCatalog) reconcileAssetRules() error {
	return c.once(installationMarker{key: "asset_intercept_default_rules_v2", value: "done"}, func() error {
		// Remove only obsolete built-in country entries; retain operator rows.
		return c.exec(`
DELETE FROM asset_intercept_rules
WHERE builtin = true
  AND kind = 'fuzzy_domain'
  AND pattern IN ('.gov.cn', '.edu.cn')`)
	})
}
