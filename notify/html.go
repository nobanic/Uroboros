package notify

import (
	"strings"
)

// This file renders the HTML body of the email. It deliberately uses inline
// styles + a simple table layout rather than modern CSS: email clients
// (especially Outlook and Chinese enterprise mailboxes) vary wildly in their
// support for <style> blocks and flex/grid, and inline styles are the only form
// that displays correctly across all of them.

// htmlSeverityColor returns the accent color for a severity, used for the
// left-hand color bar and the title.
func htmlSeverityColor(severity string) string {
	switch severity {
	case "critical":
		return "#d32029"
	case "high":
		return "#e8830c"
	case "medium":
		return "#d4b106"
	case "low":
		return "#1677ff"
	default:
		return "#8c8c8c"
	}
}

// htmlTitle returns the email subject.
func htmlTitle(m Message) string {
	return markdownTitle(m)
}

// htmlBody renders the email body HTML. maxRunes<=0 means no truncation.
func htmlBody(m Message, maxRunes int) string {
	body, _ := renderNoticeText(m, maxRunes, emailNoticeFormat())
	return body
}

// htmlBatchIntro renders the start of a digest email: the count and severity
// distribution.
func htmlBatchIntro(m Message) string { return (noticeDigest{m, m.Items, len(m.Items)}).email() }

// htmlItem renders a single vulnerability. When full=true it includes the summary
// and back-link (single push); when false it is compressed to one line (digest
// list).
func htmlItem(it Item, full bool) string { return emailFinding(it, full) }

// htmlEscape escapes HTML text content. Vulnerability titles and summaries come
// from the target under test and from model output, and are untrusted content —
// not escaping would allow injecting arbitrary HTML (including remote images)
// into the email.
func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// htmlEscapeAttr escapes an HTML attribute value (beyond text escaping, it also
// handles quotes, to prevent a quote in the URL from closing the href attribute
// early).
func htmlEscapeAttr(s string) string {
	s = htmlEscape(s)
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
