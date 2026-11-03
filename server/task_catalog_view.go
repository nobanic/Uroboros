package server

import (
	"sort"
	"strconv"
)

// taskCatalogView retains the read lock through live snapshot sorting.
type taskCatalogView struct{ manager *Manager }

func (command taskCatalogView) list() []*Task {
	command.manager.mu.RLock()
	defer command.manager.mu.RUnlock()
	out := make([]*Task, 0, len(command.manager.tasks))
	for _, t := range command.manager.tasks {
		out = append(out, t)
	}
	// Pinned tasks first, within the group by pin time descending; normal tasks by id descending. m.tasks is a map,
	// so each poll must re-sort, and id gives a stable, unique fallback order for tasks created at the same instant.
	sort.Slice(out, func(i, j int) bool {
		iState := out[i].lifecycleSnapshot()
		jState := out[j].lifecycleSnapshot()
		if (iState.PinnedAt > 0) != (jState.PinnedAt > 0) {
			return iState.PinnedAt > 0
		}
		if iState.PinnedAt != jState.PinnedAt {
			return iState.PinnedAt > jState.PinnedAt
		}
		ai, _ := strconv.ParseInt(out[i].ID, 10, 64)
		aj, _ := strconv.ParseInt(out[j].ID, 10, 64)
		return ai > aj
	})
	return out
}
