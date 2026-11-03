package notify

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Filter is the contract for the notification_channels.filter JSONB column: the
// filtering conditions for a channel instance. Every field is optional, and the
// default means "no filtering" — which is exactly the fallback semantics for a
// malformed config, see ParseFilter.
type Filter struct {
	// MinSeverity is the minimum severity threshold (low/medium/high/critical);
	// empty = no threshold.
	MinSeverity string `json:"min_severity"`
	// TaskIDs / AssetIDs: an empty array means no restriction; a non-empty one
	// requires the event to intersect it.
	TaskIDs  []int64 `json:"task_ids"`
	AssetIDs []int64 `json:"asset_ids"`
	// VulnClassInclude empty means accept everything; non-empty requires the
	// vulnclass to match at least one keyword.
	// VulnClassExclude excludes on a match of any keyword (exclude takes priority
	// over include).
	// Matching is case-insensitive substring — safer than regex: a user's broken
	// regex won't silently disable the channel.
	VulnClassInclude []string `json:"vulnclass_include"`
	VulnClassExclude []string `json:"vulnclass_exclude"`
	// OnStatusChange decides whether this channel receives finding status-change
	// events (only meaningful in realtime mode).
	OnStatusChange bool `json:"on_status_change"`
}

// ParseFilter parses a channel's filter config.
//
// **Never returns an error.** This is a deliberate design choice: a malformed
// filter config always degrades to the zero-value Filter (= no filtering = match
// everything), because for a vulnerability notification system **pushing one
// extra is far better than silently dropping a high-severity one**. Turning a
// parse failure into "don't push" would hand the user a channel that looks
// configured but actually pushes nothing — the worst possible failure mode.
func ParseFilter(raw []byte) Filter {
	var f Filter
	if len(raw) == 0 {
		return f
	}
	// On a parse failure f stays the zero value, i.e. no filtering.
	_ = json.Unmarshal(raw, &f)
	return f
}

// ValidMinSeverity reports whether s is a valid severity threshold (an empty
// string means no threshold).
func ValidMinSeverity(s string) bool {
	if s == "" {
		return true
	}
	_, ok := severityRank[s]
	return ok
}

// Validate checks the **value-restricted** fields of a filter config, for use
// when saving a channel.
//
// Why it must be caught at write time: Match's verdict for an unknown threshold
// is `rank >= 0`, which is always true — meaning a one-character typo in
// min_severity ("hgih") **silently disables** the filter and turns it into "push
// everything". This aligns with this package's "push extra over missing one"
// trade-off (nothing is dropped), but the consequence is that the user thinks
// they are doing tiered pushing while actually flooding the group with every
// vulnerability, with no sign that anything is misconfigured. Exactly this kind
// of "silent degradation" should be caught at the entry point.
//
// Note that Validate is used only on the **write** path. The read path still uses
// ParseFilter's lenient semantics, so a bad value already in historical data
// won't make the whole channel unreadable.
func (f Filter) Validate() error {
	if !ValidMinSeverity(f.MinSeverity) {
		return fmt.Errorf("invalid minimum severity %q; choose one of: low / medium / high / critical, or leave empty for no limit", f.MinSeverity)
	}
	return nil
}

// Match decides whether an event should be delivered to a channel with the given
// filter.
//
// **Never returns an error**, for the same reason as ParseFilter: any internal
// anomaly is treated as a match. Order of evaluation: event type → severity
// threshold → task/asset scope → vulnerability-type keywords.
func Match(f Filter, s Snapshot) bool {
	// Status-change events are received only by channels that explicitly opt in.
	// Off by default, because the vast majority of users expect "push" to mean "a
	// new vulnerability was found", not following every status transition like a
	// ledger.
	if s.Kind == EventFindingStatusChanged && !f.OnStatusChange {
		return false
	}
	if !AtLeast(s.Severity, f.MinSeverity) {
		return false
	}
	if len(f.TaskIDs) > 0 && !slices.Contains(f.TaskIDs, s.TaskID) {
		return false
	}
	if len(f.AssetIDs) > 0 && !intersectsInt(f.AssetIDs, s.AssetIDs) {
		return false
	}
	// Exclude takes priority: a match of any exclude keyword drops the event, even
	// if it also matches the include list.
	if len(f.VulnClassExclude) > 0 && containsAnyFold(s.VulnClass, f.VulnClassExclude) {
		return false
	}
	if len(f.VulnClassInclude) > 0 && !containsAnyFold(s.VulnClass, f.VulnClassInclude) {
		return false
	}
	return true
}

func intersectsInt(a, b []int64) bool {
	// A linear scan is fine for small sets; both sides are "a few dozen
	// hand-picked" in magnitude, so building a map costs more than it saves.
	for _, v := range b {
		if slices.Contains(a, v) {
			return true
		}
	}
	return false
}

// containsAnyFold reports whether s contains any of the keywords
// (case-insensitive).
func containsAnyFold(s string, keywords []string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw != "" && strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}
