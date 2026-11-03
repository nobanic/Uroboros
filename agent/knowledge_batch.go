package agent

import (
	"fmt"
	"strconv"

	actool "github.com/nobanic/norma/tool"
)

// knowledgeWrites preserves ordered per-item outcomes; successful writes remain
// committed even when a later item fails. Domain callbacks stay at their callers.
type knowledgeWrites struct {
	ids    []int64
	errors map[string]string
}

func pickKnowledgeItems[T any](items []T, single T) ([]T, bool) {
	if len(items) > 0 {
		return items, true
	}
	return []T{single}, false
}

func writeKnowledgeItems[T any](items []T, write func(T) (int64, error), accepted func(T)) knowledgeWrites {
	writtenBatch := knowledgeWrites{ids: make([]int64, len(items)), errors: map[string]string{}}
	for i, it := range items {
		id, err := write(it)
		if err != nil {
			writtenBatch.errors[strconv.Itoa(i)] = err.Error()
			continue
		}
		writtenBatch.ids[i] = id
		if accepted != nil {
			accepted(it)
		}
	}
	return writtenBatch
}

func (writtenBatch knowledgeWrites) result(batch bool, prefix string) (actool.Result, error) {
	if !batch {
		if message, failed := writtenBatch.errors["0"]; failed {
			return actool.Errorf(message), nil
		}
		return actool.Text(fmt.Sprintf("%s: %d", prefix, writtenBatch.ids[0])), nil
	}
	out := map[string]any{"ids": writtenBatch.ids}
	if len(writtenBatch.errors) > 0 {
		out["errors"] = writtenBatch.errors
	}
	return jsonResult(out)
}
