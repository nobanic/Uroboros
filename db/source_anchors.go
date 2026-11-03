package db

import "sort"

// Lineage and asset anchors are read projections, not ownership or finding authority.
type sourceAnchorRead struct {
	store   *ExplorationStore
	assetID int64
}

func (anchors sourceAnchorRead) findings() (map[int64]int64, error) {
	out, err := anchors.store.FindingIntents()
	if err != nil {
		return nil, err
	}
	sources, err := anchors.store.DirectSourceStores()
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		items, err := source.Store.FindingIntentsTerminal()
		if err != nil {
			return nil, err
		}
		for findingID, intentID := range items {
			out[findingID] = intentID
		}
	}
	return out, nil
}
func (anchors sourceAnchorRead) refs() ([]AssetRef, error) {
	out, err := anchors.store.AssetRefs(anchors.assetID)
	if err != nil {
		return nil, err
	}
	sources, err := anchors.store.DirectSourceStores()
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		refs, err := source.Store.AssetRefs(anchors.assetID)
		if err != nil {
			return nil, err
		}
		for i := range refs {
			if refs[i].Kind == KindIntent && !inheritedIntentTerminal(refs[i].State) {
				continue
			}
			refs[i].SourceTaskID = source.Task.TaskID
			refs[i].Inherited = true
			out = append(out, refs[i])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}
