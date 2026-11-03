// Package notify implements the IM / email delivery channel adapters for
// vulnerability findings.
//
// Layering: this package is a **leaf package** that depends only on the standard
// library. It knows nothing about the database or the server. Channel config is
// passed in as map[string]any (matching the notification_channels.config JSONB
// column), and the content to push is passed in as a Message. The benefit of
// splitting it this way is that the genuinely error-prone parts — signature
// computation, UTF-8 truncation, filter matching — can be unit-tested without
// PostgreSQL; the host only has to orchestrate on the server side.
//
// Concurrency contract: Channel implementations must be **stateless**. The same
// Channel instance is reused concurrently by multiple channel configs (even
// multiple bot instances of the same channel), so all credentials are passed in
// via the cfg argument; caching things like the webhook URL into the
// implementation's own fields is not allowed.
package notify

// Channel type identifiers. These values are also the valid set for
// notification_channels.kind, allow-list-validated on the server side (same
// rationale as findings.status: no DB CHECK, so adding channels later is easy).
const (
	KindDingTalk = "dingtalk" // DingTalk custom bot
	KindFeishu   = "feishu"   // Feishu (including Lark) custom bot
	KindWeCom    = "wecom"    // WeCom group bot
	KindWebhook  = "webhook"  // generic Webhook: custom method/headers/JSON template
	KindTelegram = "telegram" // Telegram Bot API
	KindEmail    = "email"    // SMTP email
)

// Event types, matching notification_events.kind.
const (
	EventFindingCreated       = "finding_created"
	EventFindingStatusChanged = "finding_status_changed"
)

// InitKind is the fallback value for an empty kind in config.
const InitKind = KindDingTalk

// severityRank maps a vulnerability severity to a comparable ordinal. Unknown
// severities return 0, so any min_severity setting keeps unknown severities out
// — when in doubt, don't push, to avoid flooding with false positives.
var severityRank = map[string]int{
	"low":      1,
	"medium":   2,
	"high":     3,
	"critical": 4,
}

// SeverityRank returns the severity's ordinal; unknown severities return 0.
func SeverityRank(severity string) int { return severityRank[severity] }

// SeverityLabel returns the severity name with an emoji, used in message titles
// and card coloring. Unknown severities are echoed back as-is, not invented.
func SeverityLabel(severity string) string {
	switch severity {
	case "critical":
		return "🔴 Critical"
	case "high":
		return "🟠 High"
	case "medium":
		return "🟡 Medium"
	case "low":
		return "🔵 Low"
	default:
		return severity
	}
}

// StatusLabel maps a disposition status to a human-readable label, used in
// status-change messages.
func StatusLabel(status string) string {
	switch status {
	case "pending":
		return "Pending"
	case "in_progress":
		return "In progress"
	case "confirmed":
		return "Confirmed"
	case "resolved":
		return "Resolved"
	case "fixed":
		return "Fixed"
	case "false_positive":
		return "False positive"
	case "ignored":
		return "Ignored"
	case "duplicate":
		return "Duplicate"
	case "risk_accepted":
		return "Risk accepted"
	default:
		return status
	}
}

// AtLeast reports whether severity meets the min threshold. An empty min means
// no threshold, so everything passes. Note that an unknown severity has ordinal
// 0 and is rejected by any non-empty min (see the severityRank comment).
func AtLeast(severity, min string) bool {
	if min == "" {
		return true
	}
	return SeverityRank(severity) >= SeverityRank(min)
}
