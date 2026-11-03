package traffic

import (
	"errors"
	"fmt"
	"path/filepath"
)

type hostDeleteRequest struct {
	traffic           *Traffic
	hosts             []string
	archiveID, taskID int64
}

func (request hostDeleteRequest) prepare() (*HostDeleteStage, error) {
	request.traffic.wmu.Lock()
	stage := &HostDeleteStage{traffic: request.traffic}
	fail := func(cause error) (*HostDeleteStage, error) {
		if rollbackErr := stage.rollbackLocked(); rollbackErr != nil {
			return nil, errors.Join(cause, fmt.Errorf("roll back traffic deletion: %w", rollbackErr))
		}
		return nil, cause
	}
	abort := func(cause error) (*HostDeleteStage, error) {
		request.traffic.wmu.Unlock()
		stage.done = true
		return nil, cause
	}

	unique := make([]string, 0, len(request.hosts))
	seen := make(map[string]struct{}, len(request.hosts))
	for _, host := range request.hosts {
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		unique = append(unique, host)
	}

	// Exact hosts are known up front, so their directories are derived directly
	// rather than looked up: a host whose index rows are already gone can still
	// own an orphaned directory that this deletion should take with it.
	legacy := make([]string, 0, len(unique))
	for _, h := range unique {
		legacy = append(legacy, filepath.Join(request.traffic.dir, sanitize(h)))
	}

	tx, err := request.traffic.db.Begin()
	if err != nil {
		return abort(err)
	}
	stage.tx = tx
	for _, h := range unique {
		n, err := request.traffic.deleteWhere(tx, `host=?`, h)
		if err != nil {
			return fail(err)
		}
		stage.deleted += n
	}

	stage.stageDir, stage.moves, err = request.traffic.stageTreesForArchive(legacy, unique, request.archiveID, request.taskID)
	if err != nil {
		return fail(err)
	}
	return stage, nil
}

func (request hostDeleteRequest) remove() (int64, error) {
	stage, err := request.traffic.StageDeleteHostsExact(request.hosts)
	if err != nil {
		return 0, err
	}
	deleted := stage.Deleted()
	if err := stage.Commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}
