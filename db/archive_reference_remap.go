package db

import "encoding/json"

type archiveAssetReference struct {
	table         string
	list, payload bool
}

var archiveAssetReferences = []archiveAssetReference{
	{table: "exploration_anchors"}, {table: "task_asset_links"}, {table: "katana_discoveries"}, {table: "arsenal_observations"},
	{table: "exploration_nodes", list: true, payload: true}, {table: "findings", list: true},
}

func remapArchivedAssetRows(tables map[string]json.RawMessage, mapping map[int64]int64) (map[string]json.RawMessage, error) {
	out := make(map[string]json.RawMessage, len(tables))
	for table, raw := range tables {
		out[table] = raw
	}
	replace := func(value any) (int64, bool) {
		id, ok := jsonInt64(value)
		if !ok {
			return 0, false
		}
		replacement, found := mapping[id]
		return replacement, found
	}
	for _, reference := range archiveAssetReferences {
		rows, err := decodeArchiveRows(tables[reference.table])
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			container := row
			if reference.payload {
				payload, ok := row["payload"].(map[string]any)
				if !ok {
					continue
				}
				container = payload
			}
			if !reference.list {
				if value, ok := replace(container["asset_id"]); ok {
					container["asset_id"] = value
				}
				continue
			}
			if values, ok := container["asset_ids"].([]any); ok {
				for i, value := range values {
					if replacement, ok := replace(value); ok {
						values[i] = replacement
					}
				}
				container["asset_ids"] = values
			}
		}
		out[reference.table], _ = json.Marshal(rows)
	}
	return out, nil
}
