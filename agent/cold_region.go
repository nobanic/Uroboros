package agent

import (
	"slices"
	"sort"

	"github.com/nobanic/UroborOS/db"
)

// Cold membership is key-based: a present false value still names a candidate.
// Only the destination test during causal joining uses the stored boolean.
func selectColdKeys[V any](source map[int64]V, keep func(int64, V) bool) map[int64]bool {
	selection := make(map[int64]bool)
	for id, value := range source {
		if keep(id, value) {
			selection[id] = true
		}
	}
	return selection
}

func reconcileColdStamps(wanted map[int64]bool, stamps map[int64]*int64, round int64) []stampOp {
	var changes []stampOp
	for id := range wanted {
		if stamps[id] == nil {
			changes = append(changes, stampOp{ID: id, Set: true, Round: round})
		}
	}
	for id, stamp := range stamps {
		_, cold := wanted[id]
		if stamp != nil && !cold {
			changes = append(changes, stampOp{ID: id})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	return changes
}

// A partition retains the original union-find algorithm and its two admission
// passes. Parent buckets are also the source of external context anchors.
type coldPartition struct {
	graph *coldGraph
	links *unionFind
}

func (g *coldGraph) eachColdParent(ids []int64, accept func(parent, member int64)) {
	for _, id := range ids {
		for _, parent := range g.parents[id] {
			if g.nodes[parent].Kind != db.KindDigest {
				accept(parent, id)
			}
		}
	}
}

func (p coldPartition) connectCausal(candidates map[int64]bool) {
	for source := range candidates {
		for _, destination := range p.graph.children[source] {
			if candidates[destination] {
				p.links.union(source, destination)
			}
		}
	}
}

func (p coldPartition) rescueSingletons() {
	var singletons []int64
	for _, component := range p.links.components() {
		if len(component) == 1 {
			singletons = append(singletons, component[0])
		}
	}
	buckets := make(map[int64][]int64)
	p.graph.eachColdParent(singletons, func(parent, member int64) {
		buckets[parent] = append(buckets[parent], member)
	})
	for _, siblings := range buckets {
		for i := 1; i < len(siblings); i++ {
			p.links.union(siblings[0], siblings[i])
		}
	}
}

func (p coldPartition) blocks(minimum int) []block {
	var result []block
	for _, component := range p.links.components() {
		if len(component) < minimum {
			continue
		}
		slices.Sort(component)
		members := make(map[int64]bool, len(component))
		for _, id := range component {
			members[id] = true
		}
		parents := make(map[int64]bool)
		p.graph.eachColdParent(component, func(parent, _ int64) {
			_, exists := p.graph.nodes[parent]
			if exists && !members[parent] {
				parents[parent] = true
			}
		})
		anchors := make([]int64, 0, len(parents))
		for parent := range parents {
			anchors = append(anchors, parent)
		}
		slices.Sort(anchors)
		result = append(result, block{Members: component, Anchors: anchors})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Members[0] < result[j].Members[0] })
	return result
}
