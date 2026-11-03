package report

import (
	"fmt"
	"strings"
	"time"
)

const reportTimeLayout = "2006-01-02 15:04:05"

// markdownDocument shares text framing; domain callers retain their own source,
// ordering, empty-value and triage policies.
type markdownDocument struct{ strings.Builder }
type markdownBlock struct{ before, after string }

func (d *markdownDocument) heading(level int, title string) {
	d.WriteString(strings.Repeat("#", level) + " " + title + "\n\n")
}

func (d *markdownDocument) field(label, value string) {
	d.WriteString("- **" + label + "**: " + value + "\n")
}

func (d *markdownDocument) count(label string, value int) {
	fmt.Fprintf(&d.Builder, "- %s: **%d**\n", label, value)
}

func (d *markdownDocument) paragraph(value string) {
	d.WriteString(value + "\n\n")
}

func (d *markdownDocument) block(value string, style markdownBlock) {
	if value != "" {
		d.WriteString(style.before)
		d.WriteString(value)
		d.WriteString(style.after)
	}
}

func (d *markdownDocument) trimmedBlock(value string, style markdownBlock) {
	d.block(strings.TrimSpace(value), style)
}

func reportTime(value time.Time) string { return value.Format(reportTimeLayout) }
