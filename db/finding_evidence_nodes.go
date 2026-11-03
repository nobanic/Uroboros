package db

import (
	"fmt"
	"strings"
)

// The projection borrows only the first already-visible finding node for an ID.
type findingEvidenceNodes struct {
	database     *DB
	byID         map[int64]*Node
	args         []any
	placeholders []string
}

func (projection *findingEvidenceNodes) collect(nodes []*Node) {
	projection.byID = map[int64]*Node{}
	for _, node := range nodes {
		if node == nil || node.Kind != KindFinding || projection.byID[node.ID] != nil {
			continue
		}
		projection.byID[node.ID] = node
		projection.args = append(projection.args, node.ID)
		projection.placeholders = append(projection.placeholders, fmt.Sprintf("$%d", len(projection.args)))
	}
}
func (projection findingEvidenceNodes) read() error {
	if len(projection.args) == 0 {
		return nil
	}
	rows, err := projection.database.Query(`SELECT f.id,f.node_id,(SELECT count(*) FROM finding_traffic_bindings b WHERE b.finding_id=f.id) FROM findings f WHERE f.node_id IN (`+strings.Join(projection.placeholders, ",")+`)`, projection.args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var findingID, nodeID int64
		var count int
		if err := rows.Scan(&findingID, &nodeID, &count); err != nil {
			return err
		}
		node := projection.byID[nodeID]
		node.FindingID, node.FindingNodeID, node.TrafficCount = findingID, nodeID, count
	}
	return rows.Err()
}
