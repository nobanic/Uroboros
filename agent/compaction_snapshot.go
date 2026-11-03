package agent

import (
	"context"

	"github.com/nobanic/UroborOS/db"
)

// Each phase borrows a fresh source snapshot. It is never reused for the
// commit-time liveness check in foldBlock, which must read again after Complete.
type compactionSnapshot struct {
	round    int64
	graph    *coldGraph
	records  map[int64]*db.Node
	stamps   map[int64]*int64
	versions map[int64]int
	cold     map[int64]bool
}

func readColdSnapshot(store *db.ExplorationStore, round int64) (*compactionSnapshot, error) {
	graph, records, err := loadColdGraph(store)
	if err != nil {
		return nil, err
	}
	stamps, err := store.ColdStamps()
	if err != nil {
		return nil, err
	}
	return &compactionSnapshot{round: round, graph: graph, records: records, stamps: stamps}, nil
}

func readCompactionSnapshot(store *db.ExplorationStore) (*compactionSnapshot, error) {
	round, err := store.RoundNo()
	if err != nil {
		return nil, err
	}
	snapshot, err := readColdSnapshot(store, round)
	if err != nil {
		return nil, err
	}
	snapshot.versions, err = store.ContentVersions()
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

func (s *compactionSnapshot) structural() map[int64]bool {
	if s.cold == nil {
		s.cold = s.graph.structuralCold(s.graph.hotSet())
	}
	return s.cold
}

func (s *compactionSnapshot) eligible(params coldParams) map[int64]bool {
	return s.graph.eligibleCold(s.structural(), s.stamps, s.round, params)
}

func (s *compactionSnapshot) stamp(store *db.ExplorationStore) error {
	changes := computeStampOps(s.structural(), s.stamps, s.round)
	if err := store.ApplyStampOps(toDBStampOps(changes)); err != nil {
		return err
	}
	applyStampsInPlace(s.stamps, changes)
	return nil
}

// The count and the minor batch share the same uncovered membership rule
// without allocating a second set merely to count it during maintenance.
type coldRemainder struct {
	eligible map[int64]bool
	covered  map[int64]int64
}

func (r coldRemainder) each(accept func(int64)) {
	for id := range r.eligible {
		if _, covered := r.covered[id]; !covered {
			accept(id)
		}
	}
}

func (r coldRemainder) count() int {
	count := 0
	r.each(func(int64) { count++ })
	return count
}

func (r coldRemainder) members() map[int64]bool {
	result := make(map[int64]bool)
	r.each(func(id int64) { result[id] = true })
	return result
}

func (s *compactionSnapshot) fold(ctx context.Context, c *Compactor, store *db.ExplorationStore, blocks []block, active []*db.Node) {
	for _, b := range blocks {
		c.foldBlock(ctx, store, s.graph, b, s.records, s.versions, c.generationFor(b, active))
	}
}

// Planning is pure. The caller must retire stale covers before any replacement
// completion, including when that completion subsequently fails.
func digestReplacements(blocks []block, versions map[int64]int, active []*db.Node) (create []block, retire []int64) {
	existing := make(map[string]bool, len(active))
	for _, digest := range active {
		signature, _ := digestSigGen(digest)
		existing[signature] = true
	}
	wanted := make(map[string]bool, len(blocks))
	for _, candidate := range blocks {
		signature := blockSignature(candidate, versions)
		wanted[signature] = true
		if !existing[signature] {
			create = append(create, candidate)
		}
	}
	for _, digest := range active {
		signature, _ := digestSigGen(digest)
		if !wanted[signature] {
			retire = append(retire, digest.ID)
		}
	}
	return
}
