package server

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

func (command taskStateCommand) admit(expectedStatus, status string, queued bool, mode string, preservePosition bool) error {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	n, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil {
		return err
	}
	if queued {
		if mode != "bootstrap" && mode != "resume" {
			return fmt.Errorf("invalid queue mode %q", mode)
		}
	} else {
		mode = ""
	}

	var queuedAt, completedAt, firstRunAt, deadlineAt sql.NullTime
	var committedMode string
	err = command.manager.pg.QueryRow(`UPDATE tasks
	SET status=$2,
	    completed_at=CASE
	        WHEN $2 IN ('done','failed','timeout') THEN COALESCE(completed_at, now())
	        ELSE NULL
	    END,
	    paused=false,
	    queued=$3,
	    queued_at=CASE
	        WHEN NOT $3 THEN NULL
	        WHEN $5 AND queued THEN COALESCE(queued_at, now())
	        ELSE now()
	    END,
	    queue_mode=CASE
	        WHEN NOT $3 THEN ''
	        WHEN ($5 AND queued AND queue_mode='bootstrap') OR $4='bootstrap' THEN 'bootstrap'
	        ELSE 'resume'
	    END,
	    first_run_at=CASE
	        WHEN $6='timeout' AND $2 NOT IN ('done','failed','timeout') THEN NULL
	        ELSE first_run_at
	    END,
	    deadline_at=CASE
	        WHEN $6='timeout' AND $2 NOT IN ('done','failed','timeout') THEN NULL
	        ELSE deadline_at
	    END
	WHERE id=$1 AND deleted_at IS NULL AND status=$6
	RETURNING queued_at, queue_mode, completed_at, first_run_at, deadline_at`, n, status, queued, mode, preservePosition, expectedStatus).
		Scan(&queuedAt, &committedMode, &completedAt, &firstRunAt, &deadlineAt)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task %s lifecycle changed before admission (expected status %q)", command.id, expectedStatus)
	}
	if err != nil {
		return err
	}

	command.mirror(func(state *taskLifecycleState) {
		state.Status = status
		state.Paused = false
		state.Queued = queued
		state.QueueMode = committedMode
		state.QueuedAt = 0
		if queuedAt.Valid {
			state.QueuedAt = queuedAt.Time.UnixNano()
		}
		state.CompletedAt = 0
		if completedAt.Valid {
			state.CompletedAt = completedAt.Time.Unix()
		}
		state.FirstRunAt = 0
		if firstRunAt.Valid {
			state.FirstRunAt = firstRunAt.Time.Unix()
		}
		state.DeadlineAt = 0
		if deadlineAt.Valid {
			state.DeadlineAt = deadlineAt.Time.Unix()
		}
	})
	return nil
}

func (command taskStateCommand) pause() error {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	n, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil {
		return err
	}
	var mode string
	err = command.manager.pg.QueryRow(`UPDATE tasks
		SET paused=true, queued=false, queued_at=NULL
		WHERE id=$1 AND deleted_at IS NULL AND paused=false
		  AND status NOT IN ('done','failed','timeout')
		RETURNING COALESCE(queue_mode,'')`, n).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task %s is unavailable for pause", command.id)
	}
	if err != nil {
		return err
	}
	command.mirror(func(state *taskLifecycleState) {
		state.Paused = true
		state.Queued = false
		state.QueuedAt = 0
		state.QueueMode = mode
	})
	return nil
}

func (command taskStateCommand) enqueue(mode string) error {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	n, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil {
		return err
	}
	if mode != "bootstrap" && mode != "resume" {
		return fmt.Errorf("invalid queue mode %q", mode)
	}
	var queuedAt time.Time
	var committedMode string
	err = command.manager.pg.QueryRow(`UPDATE tasks
		SET queued=true,
		    queued_at=CASE WHEN queued THEN COALESCE(queued_at, now()) ELSE now() END,
		    queue_mode=CASE
		        WHEN queue_mode='bootstrap' OR $2='bootstrap' THEN 'bootstrap'
		        ELSE 'resume'
		    END
		WHERE id=$1 AND deleted_at IS NULL
		RETURNING queued_at, queue_mode`, n, mode).Scan(&queuedAt, &committedMode)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("task %s is unavailable for enqueue", command.id)
	}
	if err != nil {
		return err
	}
	command.mirror(func(state *taskLifecycleState) {
		state.QueuedAt = queuedAt.UnixNano()
		state.Queued = true
		state.QueueMode = committedMode
	})
	return nil
}
