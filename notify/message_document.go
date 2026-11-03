package notify

import (
	"fmt"
	"strings"
)

// Fields are selected once, in their original order. Each output keeps its own
// escaping and whitespace policy; this projection never interprets finding truth.
func noticeFields(it Item, text func(string, int) string, emit func(string, string)) {
	if it.IsStatusChange() {
		emit("Status change", text(StatusLabel(it.FromStatus), 0)+" → "+text(StatusLabel(it.ToStatus), 0))
	}
	if it.VulnClass != "" && it.VulnClass != it.Title() {
		emit("Type", text(it.VulnClass, 0))
	}
	if assets := assetLine(it.Assets, maxAssetsShown); assets != "" {
		emit("Assets", text(assets, 0))
	}
	if summary := text(it.Summary, maxSummaryRunes); summary != "" {
		emit("Summary", summary)
	}
}

func writeNoticeFields(out *strings.Builder, it Item, text func(string, int) string, pattern string) {
	noticeFields(it, text, func(label, value string) { fmt.Fprintf(out, pattern, label, value) })
}

func escapedNoticeText(escape func(string) string) func(string, int) string {
	return func(value string, limit int) string {
		if limit > 0 {
			value = OneLine(value, limit)
		}
		return escape(value)
	}
}

func literalNoticeText(value string, _ int) string { return value }

// Presence is tested before formatting. A whitespace-only raw value therefore
// retains the original empty digest fragment and its separators.
func noticeExtras(it Item, text func(string, int) string, summary bool) []string {
	var values []string
	if assets := assetLine(it.Assets, maxAssetsShown); assets != "" {
		values = append(values, text(assets, 0))
	}
	if summary && it.Summary != "" {
		values = append(values, text(it.Summary, 60))
	}
	return values
}

func markdownFinding(out *strings.Builder, it Item, prefix string, full bool) {
	fmt.Fprintf(out, "%s**%s · %s**", prefix, SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	if !full {
		if extras := noticeExtras(it, markdownText, true); len(extras) > 0 {
			out.WriteString(" — " + strings.Join(extras, " · "))
		}
		out.WriteString("\n")
		return
	}
	out.WriteString("\n")
	writeNoticeFields(out, it, markdownText, "**%s**: %s\n")
	if it.DetailURL != "" {
		fmt.Fprintf(out, "[View details](%s)\n", it.DetailURL)
	}
}

func emailFinding(it Item, full bool) string {
	var out strings.Builder
	if full {
		fmt.Fprintf(&out, `<div style="border-left:4px solid %s;padding:8px 0 8px 12px;margin-bottom:12px;">`, htmlSeverityColor(it.Severity))
	} else {
		fmt.Fprintf(&out, `<div style="border-left:3px solid %s;padding:4px 0 4px 10px;margin-bottom:8px;">`, htmlSeverityColor(it.Severity))
	}
	fmt.Fprintf(&out, `<div style="font-weight:600;">%s &middot; %s</div>`, htmlEscape(SeverityLabel(it.Severity)), htmlEscape(it.Title()))
	if full {
		writeNoticeFields(&out, it, escapedNoticeText(htmlEscape), `<div><b>%s</b>: %s</div>`)
		if it.DetailURL != "" {
			fmt.Fprintf(&out, `<div style="margin-top:6px;"><a href="%s" style="color:#1677ff;">View details</a></div>`, htmlEscapeAttr(it.DetailURL))
		}
	} else if extras := noticeExtras(it, escapedNoticeText(htmlEscape), true); len(extras) > 0 {
		fmt.Fprintf(&out, `<div style="color:#595959;font-size:13px;">%s</div>`, strings.Join(extras, " &middot; "))
	}
	out.WriteString(`</div>`)
	return out.String()
}

func telegramFinding(it Item) string {
	var out strings.Builder
	writeNoticeFields(&out, it, escapedNoticeText(telegramEscape), "\n<b>%s</b>: %s")
	if it.DetailURL != "" {
		fmt.Fprintf(&out, "\n\n<a href=\"%s\">View details</a>", telegramEscapeAttr(it.DetailURL))
	}
	return out.String()
}

func cardFinding(it Item) string {
	var out strings.Builder
	fmt.Fprintf(&out, "**%s · %s**", SeverityLabel(it.Severity), markdownText(it.Title(), 0))
	writeNoticeFields(&out, it, markdownText, "\n**%s**: %s")
	return out.String()
}

func noticeFindingLine(it Item, index int, card bool) string {
	text := literalNoticeText
	pattern := "%d. %s · %s"
	if card {
		text = markdownText
		pattern = "**%d. %s · %s**"
	}
	line := fmt.Sprintf(pattern, index, SeverityLabel(it.Severity), text(it.Title(), 0))
	if extras := noticeExtras(it, text, false); len(extras) > 0 {
		line += " — " + extras[0]
	}
	return line
}

// Digest statistics count only the supplied slice and render known severities in
// the original fixed order. Unknown labels remain absent from the distribution.
func noticeSeverities(items []Item, render func(string, int) string) []string {
	var counts [4]int
	for _, it := range items {
		switch it.Severity {
		case "critical":
			counts[0]++
		case "high":
			counts[1]++
		case "medium":
			counts[2]++
		case "low":
			counts[3]++
		}
	}
	var parts []string
	for index, severity := range []string{"critical", "high", "medium", "low"} {
		if counts[index] > 0 {
			parts = append(parts, render(severity, counts[index]))
		}
	}
	return parts
}

type noticeDigest struct {
	message Message
	items   []Item
	total   int
}

func (digest noticeDigest) markdown() string {
	var out strings.Builder
	if digest.message.WindowMinutes > 0 {
		fmt.Fprintf(&out, "**Last %d min: %d new vulnerabilities**", digest.message.WindowMinutes, digest.total)
	} else {
		fmt.Fprintf(&out, "**%d new vulnerabilities**", digest.total)
	}
	if extra := digest.total - len(digest.items); extra > 0 {
		fmt.Fprintf(&out, " (this message shows the first %d; the remaining %d will continue in the next message)", len(digest.items), extra)
	}
	parts := noticeSeverities(digest.items, func(severity string, count int) string { return fmt.Sprintf("%s %d", SeverityLabel(severity), count) })
	if len(parts) > 0 {
		out.WriteString("\n" + strings.Join(parts, " · "))
	}
	out.WriteString("\n\n")
	return out.String()
}

func (digest noticeDigest) email() string {
	var out strings.Builder
	if digest.message.WindowMinutes > 0 {
		fmt.Fprintf(&out, `<h2 style="font-size:16px;margin:0 0 4px;">Last %d min: %d new vulnerabilities</h2>`, digest.message.WindowMinutes, digest.total)
	} else {
		fmt.Fprintf(&out, `<h2 style="font-size:16px;margin:0 0 4px;">%d new vulnerabilities</h2>`, digest.total)
	}
	parts := noticeSeverities(digest.items, func(severity string, count int) string {
		return fmt.Sprintf(`<span style="color:%s;font-weight:600;">%s %d</span>`, htmlSeverityColor(severity), htmlEscape(SeverityLabel(severity)), count)
	})
	if len(parts) > 0 {
		fmt.Fprintf(&out, `<p style="margin:0 0 12px;">%s</p>`, strings.Join(parts, " &middot; "))
	}
	return out.String()
}

func (digest noticeDigest) telegram() string {
	title := fmt.Sprintf("Vulnerability digest · %d total", digest.total)
	if extra := digest.total - len(digest.items); extra > 0 {
		title += fmt.Sprintf(" (showing the first %d; the remaining %d continue in the next message)", len(digest.items), extra)
	}
	if digest.message.WindowMinutes > 0 {
		title = fmt.Sprintf("Last %d min · %s", digest.message.WindowMinutes, title)
	}
	return title
}

type noticePage struct {
	items []Item
	kept  int
}

func selectNoticePage(message Message, limit, reserve int, footer string, size func(string) int, line func(Item, int) string) noticePage {
	kept := packItemCount(message.Items, limit, reserve, footer, size, func(item Item, index int) string { return line(item, index+1) })
	return noticePage{message.Items[:kept], kept}
}

func (page noticePage) each(visit func(Item, int)) {
	for index, item := range page.items {
		visit(item, index+1)
	}
}

type noticeTextFormat struct {
	header                            func(Message, []Item, int) string
	item                              func(Item, int, bool) string
	display                           func(string) string
	home                              func(Message) string
	truncate                          func(string, int) string
	size                              func(string) int
	end, separator                    string
	reserve                           int
	allItems, omitEmpty, homeOnSingle bool
}

func renderNoticeText(message Message, limit int, format noticeTextFormat) (string, int) {
	var out strings.Builder
	if !message.Batch {
		if format.omitEmpty && len(message.Items) == 0 {
			return "", 0
		}
		out.WriteString(format.header(message, message.Items, len(message.Items)))
		kept := 0
		if len(message.Items) > 0 {
			out.WriteString(format.item(message.Items[0], 1, true))
			kept = 1
		}
		if format.homeOnSingle {
			out.WriteString(format.home(message))
		}
		out.WriteString(format.end)
		return format.truncate(out.String(), limit), kept
	}
	footer := format.home(message)
	budget := limit
	if format.allItems {
		budget = 0
	}
	page := selectNoticePage(message, budget, format.reserve, footer, format.size, func(it Item, index int) string { return format.item(it, index, false) })
	out.WriteString(format.header(message, page.items, len(message.Items)))
	page.each(func(it Item, index int) {
		out.WriteString(format.separator)
		out.WriteString(format.display(format.item(it, index, false)))
	})
	out.WriteString(footer)
	out.WriteString(format.end)
	return format.truncate(out.String(), limit), page.kept
}

func noticeIdentity(value string) string { return value }

func markdownNoticeFormat() noticeTextFormat {
	return noticeTextFormat{
		header: func(m Message, items []Item, total int) string {
			if !m.Batch {
				return ""
			}
			return (noticeDigest{m, items, total}).markdown()
		},
		item: func(it Item, index int, full bool) string {
			var out strings.Builder
			prefix := ""
			if !full {
				prefix = fmt.Sprintf("%d. ", index)
			}
			markdownFinding(&out, it, prefix, full)
			return out.String()
		},
		display: noticeIdentity,
		home: func(m Message) string {
			if m.HomeURL == "" {
				return ""
			}
			return fmt.Sprintf("\n[View all on the platform](%s)\n", m.HomeURL)
		},
		truncate: TruncateBytes, size: byteSize, reserve: markdownReservedBytes, omitEmpty: true,
	}
}

const noticeEmailFrame = `<div style="font-family:-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif;font-size:14px;color:#262626;line-height:1.6;">`

func emailNoticeFormat() noticeTextFormat {
	return noticeTextFormat{
		header: func(m Message, items []Item, total int) string {
			if !m.Batch {
				return noticeEmailFrame
			}
			return noticeEmailFrame + (noticeDigest{m, items, total}).email()
		},
		item: func(it Item, _ int, full bool) string { return emailFinding(it, full) }, display: noticeIdentity,
		home: func(m Message) string {
			if m.HomeURL == "" {
				return ""
			}
			return fmt.Sprintf(`<p style="margin:16px 0 0;"><a href="%s" style="color:#1677ff;">View all on the platform</a></p>`, htmlEscapeAttr(m.HomeURL))
		},
		truncate: TruncateHTML, size: runeSize, end: `</div>`, allItems: true, homeOnSingle: true,
	}
}

func telegramNoticeFormat() noticeTextFormat {
	return noticeTextFormat{
		header: func(m Message, items []Item, total int) string {
			if !m.Batch {
				return "<b>" + telegramEscape(markdownTitle(m)) + "</b>\n"
			}
			return "<b>" + telegramEscape((noticeDigest{m, items, total}).telegram()) + "</b>"
		},
		item: func(it Item, index int, full bool) string {
			if full {
				return telegramFinding(it)
			}
			return noticeFindingLine(it, index, false)
		}, display: telegramEscape,
		home: func(m Message) string {
			if m.HomeURL == "" {
				return ""
			}
			return fmt.Sprintf("\n\n<a href=\"%s\">View all on the platform</a>", telegramEscapeAttr(m.HomeURL))
		},
		truncate: TruncateHTML, size: runeSize, reserve: telegramReservedRunes, separator: "\n",
	}
}

func renderNoticeCard(message Message) (map[string]any, int) {
	elements := []any{}
	kept := 0
	if message.Batch {
		page := selectNoticePage(message, feishuMaxCardBytes, markdownReservedBytes, "", byteSize, func(it Item, index int) string { return noticeFindingLine(it, index, true) })
		kept = page.kept
		elements = append(elements, feishuMarkdownDiv((noticeDigest{message, page.items, len(message.Items)}).markdown()))
		page.each(func(it Item, index int) {
			elements = append(elements, feishuMarkdownDiv(noticeFindingLine(it, index, true)))
		})
		if message.HomeURL != "" {
			elements = append(elements, feishuButton("View all on the platform", message.HomeURL))
		}
	} else if len(message.Items) > 0 {
		kept = 1
		item := message.Items[0]
		elements = append(elements, feishuMarkdownDiv(cardFinding(item)))
		if item.DetailURL != "" {
			elements = append(elements, feishuButton("View details", item.DetailURL))
		}
	}
	card := map[string]any{"config": map[string]any{"wide_screen_mode": true}, "header": map[string]any{"title": map[string]any{"tag": "plain_text", "content": markdownTitle(message)}}, "elements": elements}
	if len(message.Items) > 0 {
		card["header"].(map[string]any)["template"] = feishuSeverityTemplate(message.Items[0].Severity)
	}
	return card, kept
}
