package notify

import (
	"strings"
	"unicode/utf8"
)

const ellipsis = "…"

// TruncateBytes truncates s to at most max bytes, guaranteeing the result is
// valid UTF-8 and does not cut a character in half.
//
// Why it must cut on character boundaries: the WeCom group bot's markdown has a
// hard 4096-**byte** limit (not a character count), and a Chinese character is 3
// bytes. Slicing by byte directly would cut a character in half and produce
// invalid UTF-8 — the platform either rejects the whole message or shows garbled
// boxes. The approach here is to step back from the budget position to the
// nearest rune start byte (utf8.RuneStart detects continuation bytes
// 0b10xxxxxx).
//
// max<=0 means unlimited. An ellipsis is appended after truncation, unless max is
// too small to fit the ellipsis.
func TruncateBytes(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	budget := max - len(ellipsis)
	suffix := ellipsis
	if budget < 0 {
		// max is shorter than the ellipsis itself: drop the ellipsis and truncate
		// plainly, so the result does not end up exceeding max.
		budget = max
		suffix = ""
	}
	cut := budget
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + suffix
}

// OneLine collapses multi-line text into a single line: fold all whitespace, then
// truncate by character count. Used for the title line of IM messages — summaries
// often contain newlines, and dropping them straight into a table/title breaks
// the layout.
// max<=0 means no length limit.
func OneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	return TruncateRunes(s, max)
}

// TruncateRunes truncates s to at most max characters (not bytes), appending an
// ellipsis when it overflows.
// max<=0 means unlimited.
//
// The difference from TruncateBytes is the platform's unit: WeCom limits by bytes,
// Telegram limits by character count. Using the wrong unit does not error out, it
// just cuts the message far shorter than expected (a Chinese character = 3 bytes,
// so a 4096-byte cut leaves only about 1365 characters), which is why both
// functions must be kept and chosen per channel.
func TruncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + ellipsis
}

// TruncateHTML truncates an HTML fragment by character count while guaranteeing
// it does not produce a half-cut tag.
//
// Truncating HTML by character directly can cut out a broken tag like
// `<a href="htt`, which the platform parser either rejects the whole message over
// or swallows the following body as an attribute value. The approach here is:
// truncate by character first, then check whether the tail has an unclosed `<`,
// and if so back up to before it.
//
// It does not balance tags (completing a </b> and so on): Telegram's HTML parser
// auto-closes unclosed tags, and implementing balancing ourselves would have to
// handle quotes inside attributes, comments, and self-closing tags — complexity
// out of proportion to the benefit.
func TruncateHTML(s string, max int) string {
	if max <= 0 || len([]rune(s)) <= max {
		return s
	}
	cut := TruncateRunes(s, max)
	// If the tail is a fragment starting with `<` (the last `<` has no following
	// `>`), back up to before the `<`.
	if lt := strings.LastIndex(cut, "<"); lt >= 0 && !strings.Contains(cut[lt:], ">") {
		cut = cut[:lt]
	}
	// If the tail is a cut-off HTML entity (e.g. `&amp;` cut to `&amp`), back up
	// as well. An entity fragment can get the **whole message** rejected by a
	// parser that only accepts entities — and a digest message over the length
	// limit is common enough that losing the whole notification over it is not
	// worth it.
	if amp := strings.LastIndex(cut, "&"); amp >= 0 && !strings.Contains(cut[amp:], ";") {
		cut = cut[:amp]
	}
	return cut
}

// packItemCount computes how many items fit **completely** within the budget, for
// packing a digest message whole-item by whole-item.
//
// Why pack whole items rather than render the full thing and then truncate:
// truncation makes the later items vanish into thin air while their delivery
// records are still marked as delivered — invisible in the message and invisible
// in the delivery history, so the vulnerability is simply lost. With whole-item
// packing, the items that don't fit stay in the database as the next batch, and
// the kept count the caller receives is the number of items this message actually
// delivered.
//
// Parameters: maxSize<=0 means unlimited; reserve is the amount set aside for the
// message header/footer; size does the measuring (the unit differs per platform:
// WeCom/DingTalk by bytes, Telegram by character count — using the wrong unit does
// not error out, it just squeezes a Chinese message far below the limit); render
// renders item idx into its actual text — its length varies with content and
// cannot be estimated.
//
// Returns at least 1 (as long as items remain). Even a single extremely long item
// must be emitted, backstopped by the caller's final truncation; otherwise one
// oversized vulnerability would jam the whole batch in place forever.
func packItemCount(items []Item, maxSize, reserve int, footer string, size func(string) int, render func(Item, int) string) int {
	if maxSize <= 0 {
		return len(items)
	}
	budget := maxSize - reserve - size(footer)
	if budget < 0 {
		budget = 0
	}
	used := 0
	for i, it := range items {
		used += size(render(it, i))
		if used > budget && i > 0 {
			return i
		}
	}
	return len(items)
}

// byteSize / runeSize are packItemCount's two measuring units, named so that the
// call sites don't carry a bare func(s string) int closure, which would make it
// hard to tell at a glance which unit is in use.
func byteSize(s string) int { return len(s) }
func runeSize(s string) int { return utf8.RuneCountInString(s) }

// assetLine renders an asset list into a single line of display text, omitting the
// rest and noting the total when there are more than limit. A single vulnerability
// may anchor dozens of assets, and listing them all would overflow the message.
func assetLine(assets []string, limit int) string {
	if len(assets) == 0 {
		return ""
	}
	if limit <= 0 || len(assets) <= limit {
		return strings.Join(assets, ", ")
	}
	return strings.Join(assets[:limit], ", ") + " (" + itoa(len(assets)) + " total)"
}

// itoa is a short alias for strconv.Itoa, used only to build display text, to
// avoid importing strconv everywhere.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
