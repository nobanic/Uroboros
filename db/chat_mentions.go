package db

import (
	"context"
	"errors"
)

// ChatMention is a lightweight search result. Details are read again on send.
type ChatMention struct {
	Kind        string `json:"kind"`
	ID          int64  `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

type ChatMentionPage struct {
	Items      []ChatMention `json:"items"`
	NextCursor string        `json:"next_cursor,omitempty"`
}

var ErrInvalidChatMentionCursor = errors.New("invalid pagination position, please search again")

type chatMentionCursor struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Exact bool   `json:"exact"`
	Scope string `json:"scope"`
	Query string `json:"query"`
}

func ValidChatMentionKind(kind string) bool {
	switch kind {
	case "finding", "company", "asset", "endpoint", "ip", "app", "root_domain", "subdomain", "service":
		return true
	}
	return false
}

// SearchChatMentions searches the shared catalog, just like the asset/finding
// pages. Values remain SQL parameters; %, _ and backslash are literal search text.
func (d *DB) SearchChatMentions(ctx context.Context, kind, query string) ([]ChatMention, error) {
	page, err := d.SearchChatMentionsPage(ctx, kind, query, "")
	return page.Items, err
}

// SearchChatMentionsPage uses the last result's stable sort key rather than an
// offset, so loading later pages does not repeatedly skip all earlier results.
func (d *DB) SearchChatMentionsPage(ctx context.Context, kind, query, cursor string) (ChatMentionPage, error) {
	return (mentionLookup{database: d, kind: kind, query: query, cursor: cursor}).page(ctx)
}
