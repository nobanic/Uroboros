package db

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

type mentionLookup struct {
	database *DB
	kind     string
	query    string
	cursor   string
}

func (lookup mentionLookup) position() (chatMentionCursor, error) {
	var position chatMentionCursor
	if lookup.kind != "" && !ValidChatMentionKind(lookup.kind) {
		return position, fmt.Errorf("unsupported reference type")
	}
	if lookup.cursor == "" {
		return position, nil
	}
	if len(lookup.cursor) > 2048 {
		return position, ErrInvalidChatMentionCursor
	}
	encoded, err := base64.RawURLEncoding.DecodeString(lookup.cursor)
	if err != nil || json.Unmarshal(encoded, &position) != nil || position.ID <= 0 ||
		!ValidChatMentionKind(position.Kind) || position.Scope != lookup.kind || position.Query != lookup.query {
		return position, ErrInvalidChatMentionCursor
	}
	return position, nil
}

func (lookup mentionLookup) pattern() string {
	escape := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return "%" + escape.Replace(lookup.query) + "%"
}

func (lookup mentionLookup) continuation(last ChatMention) string {
	encoded, _ := json.Marshal(chatMentionCursor{last.ID, last.Kind, fmt.Sprint(last.ID) == lookup.query, lookup.kind, lookup.query})
	return base64.RawURLEncoding.EncodeToString(encoded)
}

func (lookup mentionLookup) page(ctx context.Context) (ChatMentionPage, error) {
	result := ChatMentionPage{Items: make([]ChatMention, 0)}
	position, err := lookup.position()
	if err != nil {
		return result, err
	}
	rows, err := lookup.database.QueryContext(ctx, mentionCatalogSQL(), lookup.kind, lookup.query, lookup.pattern(), position.ID, position.Kind, position.Exact)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		item := ChatMention{}
		if err := rows.Scan(&item.Kind, &item.ID, &item.Label, &item.Description); err != nil {
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Items) > 20 {
		result.Items = result.Items[:20]
		result.NextCursor = lookup.continuation(result.Items[19])
	}
	return result, nil
}
