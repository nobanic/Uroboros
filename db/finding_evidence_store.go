package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// A store borrows the caller's already-locked evidence transaction.
type findingEvidenceStore struct {
	tx        *sql.Tx
	findingID int64
}

func (store findingEvidenceStore) exec(statement string, args ...any) error {
	_, err := store.tx.Exec(statement, args...)
	return err
}
func (store findingEvidenceStore) affected(statement string, args ...any) (int64, error) {
	result, err := store.tx.Exec(statement, args...)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
func (store findingEvidenceStore) snapshot(s TrafficEvidenceSnapshot) error {
	if s.ID != TrafficSnapshotID(s) {
		return errors.New("evidence snapshot metadata hash mismatch")
	}
	id := s.ID
	s = s.Normalize()
	s.ID = id
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	return store.exec(`INSERT INTO traffic_evidence_snapshots
(id,source_traffic_id,captured_at,url,method,status,content_type,req_head,resp_head,req_hash,resp_hash,req_len,resp_len,created_at)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT(id) DO NOTHING`, s.ID, s.SourceTrafficID, s.CapturedAt, s.URL, s.Method, s.Status, s.ContentType, s.ReqHead, s.RespHead, s.ReqHash, s.RespHash, s.ReqLen, s.RespLen, s.CreatedAt)
}
func (store findingEvidenceStore) append(prepared []PreparedTrafficEvidence) error {
	var position int
	if err := store.tx.QueryRow(`SELECT COALESCE(MAX(position)+1,0) FROM finding_traffic_bindings WHERE finding_id=$1`, store.findingID).Scan(&position); err != nil {
		return err
	}
	changed := false
	for _, item := range prepared {
		if err := store.snapshot(item.Snapshot); err != nil {
			return err
		}
		if err := store.exec(`UPDATE traffic_evidence_snapshots SET unreferenced_at=NULL WHERE id=$1`, item.Snapshot.ID); err != nil {
			return err
		}
		n, err := store.affected(`INSERT INTO finding_traffic_bindings(finding_id,snapshot_id,role,note,position)
VALUES($1,$2,$3,$4,$5) ON CONFLICT(finding_id,snapshot_id) DO NOTHING`, store.findingID, item.Snapshot.ID, item.Ref.Role, item.Ref.Note, position)
		if err != nil {
			return err
		}
		if n > 0 {
			changed = true
			position++
		}
	}
	if changed {
		return bumpEvidenceVersionTx(store.tx, store.findingID)
	}
	return nil
}
func (store findingEvidenceStore) read() (*FindingTraffic, error) {
	out := &FindingTraffic{FindingID: store.findingID, Bindings: []FindingTrafficBinding{}}
	if err := store.tx.QueryRow(`SELECT evidence_version,report_evidence_version FROM findings WHERE id=$1`, store.findingID).Scan(&out.Version, &out.ReportVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrFindingNotFound
		}
		return nil, err
	}
	rows, err := store.tx.Query(`SELECT b.id,b.finding_id,b.snapshot_id,b.role,b.note,b.position,b.created_at,to_jsonb(s)
FROM finding_traffic_bindings b JOIN traffic_evidence_snapshots s ON s.id=b.snapshot_id WHERE b.finding_id=$1 ORDER BY b.position,b.id`, store.findingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var binding FindingTrafficBinding
		var raw []byte
		if err := rows.Scan(&binding.ID, &binding.FindingID, &binding.SnapshotID, &binding.Role, &binding.Note, &binding.Position, &binding.CreatedAt, &raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &binding.Snapshot); err != nil {
			return nil, err
		}
		out.Bindings = append(out.Bindings, binding)
	}
	return out, rows.Err()
}
func (store findingEvidenceStore) reorder(order []int64) error {
	current, err := store.read()
	if err != nil {
		return err
	}
	if len(order) != len(current.Bindings) {
		return ErrEvidenceConflict
	}
	ids := map[int64]bool{}
	for _, binding := range current.Bindings {
		ids[binding.ID] = true
	}
	for position, id := range order {
		if !ids[id] {
			return ErrEvidenceConflict
		}
		delete(ids, id)
		if err := store.exec(`UPDATE finding_traffic_bindings SET position=$1 WHERE finding_id=$2 AND id=$3`, position, store.findingID, id); err != nil {
			return err
		}
	}
	return nil
}
func (store findingEvidenceStore) edit(bindingID, version int64, role, note *string, remove bool, order []int64) error {
	if err := LockFindingEvidenceTx(store.tx, store.findingID, &version); err != nil {
		return err
	}
	if order != nil {
		if err := store.reorder(order); err != nil {
			return err
		}
	} else {
		var n int64
		var err error
		if remove {
			n, err = store.affected(`DELETE FROM finding_traffic_bindings WHERE finding_id=$1 AND id=$2`, store.findingID, bindingID)
		} else {
			n, err = store.affected(`UPDATE finding_traffic_bindings SET role=COALESCE($3,role),note=COALESCE($4,note) WHERE finding_id=$1 AND id=$2`, store.findingID, bindingID, role, note)
		}
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrEvidenceNotFound
		}
	}
	return bumpEvidenceVersionTx(store.tx, store.findingID)
}
func (store findingEvidenceStore) report(nodeID int64, report string, version *int64) (int64, error) {
	var id int64
	if err := store.tx.QueryRow(`SELECT id FROM findings WHERE node_id=$1`, nodeID).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	if err := LockFindingEvidenceTx(store.tx, id, version); err != nil {
		return 0, err
	}
	return store.affected(`UPDATE findings SET report=$2,report_evidence_version=COALESCE($3::bigint,CASE WHEN evidence_version=0 THEN 0 ELSE -1 END) WHERE id=$1`, id, report, version)
}
