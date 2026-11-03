package notify

import (
	"fmt"
	"strings"
)

// This file holds the message rendering shared by the "Markdown family" channels
// (DingTalk, WeCom). Feishu uses card JSON, Telegram uses HTML, and email uses
// HTML, each rendered in its own adapter.

// maxAssetsShown is the maximum number of assets listed in a message. A single
// vulnerability may anchor dozens of assets, and listing them all would overflow
// the message with no informational value — nobody reads the domains past the
// fourth in an IM.
const maxAssetsShown = 3

// maxSummaryRunes is how many characters the summary is compressed to. An IM
// message is "a prompt to go look at the detail", not the report itself; the full
// content is on the platform.
const maxSummaryRunes = 120

// markdownReservedBytes is set aside for the message header (digest line +
// severity distribution + a possible truncation notice) and footer (platform
// link). When packing whole items, this is deducted from the budget so the header
// and footer are never truncated — if they were, the reader couldn't even tell
// "which batch this is, and how many items aren't shown".
const markdownReservedBytes = 320

// markdownEscape escapes markdown metacharacters.
//
// Why it is required: vulnerability titles, summaries, types, and asset display
// names all come from **untrusted sources** — titles and summaries come from model
// output (the model reads the target-under-test's responses), and an asset's url
// is the full scanned URL (including a target-controllable query string). Without
// escaping, a vulnerability whose title is
//
//	Login SQL injection\n[Urgent: click here to verify account](http://attacker.tld)
//
// would render as a **clickable external link** in a security engineer's
// DingTalk/Feishu; and `![](http://attacker.tld/beacon)` would be fetched by the
// client at render time, effectively announcing "this vulnerability has been
// viewed" and leaking the reader's IP. Even benign content: an injected bold or
// blockquote can push the serious vulnerabilities below it past the fold.
//
// The escape set covers the title/link/emphasis/list/blockquote/strikethrough
// classes of characters that change structure or produce clickable elements. `\`
// must be handled first, or it would re-escape the backslashes added afterwards.
func markdownEscape(s string) string {
	replacer := strings.NewReplacer(
		`\`, `\\`,
		"`", "\\`",
		"*", `\*`,
		"_", `\_`,
		"[", `\[`,
		"]", `\]`,
		"(", `\(`,
		")", `\)`,
		"!", `\!`,
		"#", `\#`,
		">", `\>`,
		"|", `\|`,
		"~", `\~`,
	)
	return replacer.Replace(s)
}

// markdownText collapses untrusted text to a single line and escapes it, for use
// in markdown body. Single-lining is the other half beyond escaping: a newline on
// its own can forge a new list item or blockquote, and the escape characters
// can't stop it.
func markdownText(s string, maxRunes int) string {
	return markdownEscape(OneLine(s, maxRunes))
}

// markdownTitle returns the message title (the IM platform's title bar / card
// title), whose content is the **unescaped original**.
//
// It deliberately does not escape here: this title is shared by four rendering
// contexts — markdown body, Telegram's HTML, the Feishu card's plain_text, and
// the generic Webhook's JSON plus the email subject. Each context's escaping rules
// differ (markdown escaping dropped into HTML leaves visible backslashes, and
// dropped into JSON pollutes the data), so escaping must be the responsibility of
// each output end; see writeItem / feishuItemLines / telegramEscape. Markdown
// escaping was once added in the shared function, and the result was visible
// backslashes like `\(1\)` in Telegram messages.
func markdownTitle(m Message) string {
	if m.Batch {
		return fmt.Sprintf("Vulnerability digest · %d total", len(m.Items))
	}
	if len(m.Items) == 0 {
		return "Vulnerability notification"
	}
	it := m.Items[0]
	return fmt.Sprintf("[%s] %s", SeverityLabel(it.Severity), OneLine(it.Title(), 0))
}

// markdownBody renders the message body, returning the body and the **number of
// items actually written**.
//
// The returned kept is the number of items this delivery actually delivered; the
// caller marks only the first kept items as delivered — the items kept out by the
// channel's length limit must wait for the next batch rather than being marked
// successful along with the rest. This is exactly the source of "silent loss": the
// message was truncated, but the delivery record shows everything delivered, with
// nowhere to see that the second half was never sent.
//
// maxBytes<=0 means unlimited.
func markdownBody(m Message, maxBytes int) (string, int) {
	return renderNoticeText(m, maxBytes, markdownNoticeFormat())
}

// markdownBatchIntro renders the start of a digest message: the time window, the
// count, and the severity distribution. With these, whoever receives the digest
// can judge whether the batch needs immediate attention without clicking into the
// platform.
//
// items are the items **actually fit**, and total is the batch's intended total.
// When they differ it must state "how many more are in the next message" —
// otherwise the reader assumes the number written in the header is the whole
// thing, while those never-sent items simply don't exist in the UI.
func markdownBatchIntro(m Message, items []Item, total int) string {
	return (noticeDigest{m, items, total}).markdown()
}

// writeItem renders a single vulnerability item.
//
// prefix is the sequence number for the digest list; when single=true it renders
// the full version (with summary and back-link), while the digest list renders
// only a one-line summary — otherwise 50 digest items would become a long
// document.
//
// All externally sourced content (title/type/assets/summary) goes through
// markdownText: single-lining + escaping. The back-link is built from the
// admin-configured public_base_url, is not untrusted content, and must be a
// clickable link, so it is output as-is.
func writeItem(b *strings.Builder, it Item, prefix string, single bool) {
	markdownFinding(b, it, prefix, single)
}
