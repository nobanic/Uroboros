package db

import (
	"database/sql"
	"time"
)

// ConvTokenSummary is one conversation's token total (sum of its kind='result'
// rows) with its profile + created_at, used to merge conversation usage into the
// dashboard's per-profile / daily token stats (which otherwise cover only tasks).
type ConvTokenSummary struct {
	LLMProfileID     *int64 `json:"llm_profile_id"`
	CreatedAt        string `json:"created_at"`
	InputTokens      int    `json:"input_tokens"`
	OutputTokens     int    `json:"output_tokens"`
	CacheReadTokens  int    `json:"cache_read_tokens"`
	CacheWriteTokens int    `json:"cache_write_tokens"`
}

// ConversationTokenSummaries returns one row per conversation with its summed
// result-row token usage (0 for conversations with no completed run yet).
func (d *DB) ConversationTokenSummaries() ([]ConvTokenSummary, error) {
	return conversationTokenTotals(d)

}

// Conversation is one ChatGPT-style chat thread bound to an agent key. It lives
// independent of the pentest exploration graph — see schema.sql §I.
type Conversation struct {
	ID           int64      `json:"id"`
	AgentKey     string     `json:"agent_key"`
	Title        string     `json:"title"`
	LLMProfileID *int64     `json:"llm_profile_id,omitempty"`
	Pinned       bool       `json:"pinned"`
	PinnedAt     *time.Time `json:"pinned_at,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// ConversationPatch updates only the fields whose pointers are non-nil.
type ConversationPatch struct {
	Title  *string
	Pinned *bool
}

const convCols = `id, agent_key, title, llm_profile_id, pinned_at, created_at, updated_at`

func scanConv(row interface{ Scan(...any) error }) (Conversation, error) {
	var c Conversation
	var pinnedAt sql.NullTime
	err := row.Scan(&c.ID, &c.AgentKey, &c.Title, &c.LLMProfileID, &pinnedAt, &c.CreatedAt, &c.UpdatedAt)
	if pinnedAt.Valid {
		c.Pinned = true
		c.PinnedAt = &pinnedAt.Time
	}
	return c, err
}

// CreateConversation opens a new chat thread for agentKey with an initial title.
// llmProfileID may be nil to use the globally active profile.
func (d *DB) CreateConversation(agentKey, title string, llmProfileID *int64) (*Conversation, error) {
	tx, err := d.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// Insert the child without a profile first. The new row is exclusively owned
	// by this transaction before it takes a profile lock, matching DeleteProfile's
	// child-row -> profile-row protocol.
	c, err := scanConv(tx.QueryRow(`
INSERT INTO conversations(agent_key, title, llm_profile_id) VALUES ($1, $2, NULL)
RETURNING `+convCols, agentKey, title))
	if err != nil {
		return nil, err
	}
	if err := lockLLMProfileForReference(tx, llmProfileID); err != nil {
		return nil, err
	}
	if llmProfileID != nil {
		c, err = scanConv(tx.QueryRow(`UPDATE conversations SET llm_profile_id=$2
WHERE id=$1 RETURNING `+convCols, c.ID, llmProfileID))
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateConversationProfile sets (or clears) the LLM profile override for a conversation.
func (d *DB) UpdateConversationProfile(id int64, llmProfileID *int64) error {
	tx, err := d.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var lockedID int64
	if err := tx.QueryRow(`SELECT id FROM conversations WHERE id=$1 FOR UPDATE`, id).Scan(&lockedID); err != nil {
		if err == sql.ErrNoRows {
			// Preserve the previous UPDATE semantics: an unknown id is a no-op.
			return tx.Commit()
		}
		return err
	}
	if err := lockLLMProfileForReference(tx, llmProfileID); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE conversations SET llm_profile_id=$2 WHERE id=$1`, id, llmProfileID); err != nil {
		return err
	}
	return tx.Commit()
}

// ListConversations returns all threads, most-recently-updated first.
func (d *DB) ListConversations() ([]*Conversation, error) {
	rows, err := d.Query(`SELECT ` + convCols + ` FROM conversations
ORDER BY (pinned_at IS NOT NULL) DESC, pinned_at DESC NULLS LAST, updated_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Conversation{}
	return collectRowValues(rows, out, func() (*Conversation, error) {
		c, err := scanConv(rows)
		return &c, err
	})
}

// GetConversation returns one thread (nil, nil if absent).
func (d *DB) GetConversation(id int64) (*Conversation, error) {
	c, err := scanConv(d.QueryRow(`SELECT `+convCols+` FROM conversations WHERE id=$1`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpdateConversation applies a partial title/pin mutation and returns the updated
// row. Pinning an already-pinned conversation preserves its original pin order.
func (d *DB) UpdateConversation(id int64, patch ConversationPatch) (*Conversation, error) {
	c, err := scanConv(d.QueryRow(`UPDATE conversations SET
	title = CASE WHEN $2::boolean THEN $3 ELSE title END,
	pinned_at = CASE
		WHEN $4::boolean IS NULL THEN pinned_at
		WHEN $4::boolean THEN COALESCE(pinned_at, now())
		ELSE NULL
	END
WHERE id=$1
RETURNING `+convCols, id, patch.Title != nil, patch.Title, patch.Pinned))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// RenameConversation sets a thread's title. Kept for automatic first-message
// titles and compatibility with existing callers.
func (d *DB) RenameConversation(id int64, title string) error {
	_, err := d.UpdateConversation(id, ConversationPatch{Title: &title})
	return err
}

// TouchConversation bumps updated_at so the thread floats to the top of the list.
func (d *DB) TouchConversation(id int64) error {
	_, err := d.Exec(`UPDATE conversations SET updated_at=now() WHERE id=$1`, id)
	return err
}

// DeleteConversation removes a thread; its activities cascade via FK.
func (d *DB) DeleteConversation(id int64) error {
	_, err := d.Exec(`DELETE FROM conversations WHERE id=$1`, id)
	return err
}

// DeleteConversations removes existing threads in one statement and returns the
// ids that were actually present. Child activities and trigger runs cascade.
func (d *DB) DeleteConversations(ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return []int64{}, nil
	}
	rows, err := d.Query(`DELETE FROM conversations WHERE id=ANY($1::bigint[]) RETURNING id`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deleted := make([]int64, 0, len(ids))
	return collectRowValues(rows, deleted, func() (int64, error) {
		var id int64
		err := rows.Scan(&id)
		return id, err
	})
}

// AppendConvActivity records one step of a conversation (human message or an agent
// execution step) and returns its id. Mirrors ExplorationStore.AppendActivity but
// keyed by conversation_id. Reuses the Activity struct (NodeID is ignored here).
func (d *DB) AppendConvActivity(convID int64, a Activity) (int64, error) {
	return appendExecutionStep(d, convID, false, a)

}

// ConvActivityList returns a conversation's steps after sinceID (exclusive) with
// the summary-only column set (detail is lazy-loaded via ConvActivityDetail).
func (d *DB) ConvActivityList(convID, sinceID int64, limit int) ([]Activity, int64, error) {
	return conversationIncrement(d, convID, sinceID, limit)

}

// ConvActivityPage returns one page for reverse (newest-first) pagination: up to
// `limit` steps ending before id `before` (exclusive; before<=0 = the latest
// page), returned in ASCENDING id order. hasMore reports whether still-older steps
// exist before the returned window, so the client can stop loading earlier history
// on scroll-up. Summary-only columns (detail is lazy-loaded via ConvActivityDetail).
func (d *DB) ConvActivityPage(convID, before int64, limit int) ([]Activity, bool, error) {
	return conversationHistory(d, convID, before, limit)

}

// ConvActivityDetail lazily returns the full detail blob for one step.
func (d *DB) ConvActivityDetail(convID, id int64) (string, error) {
	return executionStepDetail(d, false, convID, id)

}
