package db

// intentViews reads only the original exploration. origins' two fixed callers
// select either local topology or the original terminal-only inherited view.
type intentViews struct{ store *ExplorationStore }

func (command intentViews) facts(intentID int64) ([]int64, error) {
	rows, err := command.store.db.Query(`SELECT n.id
		FROM exploration_edges e
		JOIN exploration_nodes n ON n.id=e.dst_id AND n.exploration_id=e.exploration_id
		WHERE e.exploration_id=$1 AND e.src_id=$2 AND e.rel=$3 AND n.kind=$4
		ORDER BY n.id`, command.store.expID, intentID, RelYields, KindFact)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	return collectRowValues(rows, ids, func() (int64, error) {
		var id int64
		err := rows.Scan(&id)
		return id, err
	})
}

func (command intentViews) lineage(nodeID int64) ([]*Node, []Edge, error) {
	// anc = the node + everything that can reach it via edges (walk src<-dst).
	nodeRows, err := command.store.db.Query(`
WITH RECURSIVE anc(id) AS (
    SELECT $2::bigint
  UNION
    SELECT e.src_id
    FROM exploration_edges e
    JOIN anc ON e.dst_id = anc.id
    WHERE e.exploration_id = $1
)
SELECT `+nodeCols+`
FROM exploration_nodes
WHERE exploration_id = $1 AND id IN (SELECT id FROM anc)
ORDER BY id`, command.store.expID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	nodes, err := scanNodes(nodeRows)
	if err != nil {
		return nil, nil, err
	}
	if len(nodes) == 0 {
		return nil, nil, nil
	}
	// edges among the ancestor set only (the lineage's internal relations).
	edgeRows, err := command.store.db.Query(`
WITH RECURSIVE anc(id) AS (
    SELECT $2::bigint
  UNION
    SELECT e.src_id
    FROM exploration_edges e
    JOIN anc ON e.dst_id = anc.id
    WHERE e.exploration_id = $1
)
SELECT src_id, rel, dst_id
FROM exploration_edges
WHERE exploration_id = $1
  AND src_id IN (SELECT id FROM anc)
  AND dst_id IN (SELECT id FROM anc)`, command.store.expID, nodeID)
	if err != nil {
		return nil, nil, err
	}
	defer edgeRows.Close()
	var edges []Edge
	for edgeRows.Next() {
		var e Edge
		if err := edgeRows.Scan(&e.From, &e.Rel, &e.To); err != nil {
			return nil, nil, err
		}
		edges = append(edges, e)
	}
	return nodes, edges, edgeRows.Err()
}

func (command intentViews) origins(terminal bool) (map[int64]int64, error) {
	query := `SELECT e.dst_id, e.src_id
FROM exploration_edges e
JOIN exploration_nodes n ON n.id = e.dst_id AND n.exploration_id = e.exploration_id
WHERE e.exploration_id=$1 AND e.rel=$2 AND n.kind='finding'`
	if terminal {
		query = `SELECT e.dst_id, e.src_id
	FROM exploration_edges e
	JOIN exploration_nodes finding ON finding.id=e.dst_id AND finding.exploration_id=e.exploration_id
	JOIN exploration_nodes intent ON intent.id=e.src_id AND intent.exploration_id=e.exploration_id
	WHERE e.exploration_id=$1 AND e.rel=$2 AND finding.kind='finding'
	  AND intent.kind='intent' AND intent.state IN ('done','blocked','exhausted','stopped')`
	}
	return collectQueryState(queriedRows(command.store.db.Query(query, command.store.expID, RelYields)), func() map[int64]int64 {
		return map[int64]int64{}
	}, func(queryRow rowDecoder, queryState *map[int64]int64) error {
		var findingID, intentID int64
		if err := queryRow.Scan(&findingID, &intentID); err != nil {
			return err
		}
		(*queryState)[findingID] = intentID
		return nil
	})
}
