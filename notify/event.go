package notify

// Snapshot is the contract for the notification_events.snapshot JSONB column.
// The writer is the finding-persistence transaction in the db layer; the readers
// are the delivery engine and filter matching in the server layer. It lives in
// this package because it is a "notification domain" payload: db only serializes
// it and does not understand the field meanings.
//
// Why vulnerability fields are stored redundantly rather than looked up at render
// time: a finding may later be renamed, re-graded, or have its status changed,
// yet the pushed content should reflect the conclusion **at the time of the
// event** — a lookup would produce the dangerously misleading "later downgraded
// to low". It also means fan-out and rendering need not JOIN the
// findings/tasks/assets tables.
type Snapshot struct {
	// Event type: finding_created / finding_status_changed
	Kind      string  `json:"kind"`
	FindingID int64   `json:"finding_id"`
	TaskID    int64   `json:"task_id"`
	VulnClass string  `json:"vulnclass"`
	Name      string  `json:"name"`
	Severity  string  `json:"severity"`
	Summary   string  `json:"summary"`
	AssetIDs  []int64 `json:"asset_ids"`
	// Non-empty only when kind=finding_status_changed.
	FromStatus string `json:"from_status,omitempty"`
	ToStatus   string `json:"to_status,omitempty"`
}

// Item is a single vulnerability to push, for channel rendering.
type Item struct {
	FindingID int64
	Name      string
	VulnClass string
	Severity  string
	Summary   string
	// Assets holds the resolved asset display names (e.g. domain/IP). Filled in by
	// the server layer — this package does not touch the database and cannot get
	// the names itself.
	Assets []string
	// DetailURL is the back-link to the finding detail; empty means public_base_url
	// is not configured, and the link is omitted at render time.
	DetailURL string
	// Status-change events only; when both are non-empty they render as
	// "Pending → Fixed".
	FromStatus string
	ToStatus   string
}

// IsStatusChange reports whether this item is a status-change event.
func (i Item) IsStatusChange() bool { return i.FromStatus != "" || i.ToStatus != "" }

// Title returns the item's display title: the manually assigned name first, then
// the vulnclass as a fallback, and a placeholder when both are empty — it never
// outputs an empty title.
func (i Item) Title() string {
	if i.Name != "" {
		return i.Name
	}
	if i.VulnClass != "" {
		return i.VulnClass
	}
	return "(unnamed vulnerability)"
}

// Message is the complete content of a single channel send.
type Message struct {
	// Length 1 for a single push; a whole batch for a digest push. An empty slice
	// is invalid; the caller must guarantee at least one item.
	Items []Item
	// When Batch=true, render as a digest message (different title, with the time
	// window and the count).
	Batch bool
	// WindowMinutes is the digest period (minutes); used only when Batch=true for
	// the "last N minutes" wording. Deliberately passed in explicitly from config
	// rather than computed via time.Since at render time, so rendering stays
	// deterministic and testable.
	WindowMinutes int
	// HomeURL is the platform dashboard address (the global public_base_url); empty
	// means no dashboard entry point is included.
	HomeURL string
}
