package server

import (
	pgdb "github.com/nobanic/UroborOS/db"
	"strconv"
	"time"
)

// taskStateCommand keeps one public command's original ID and manager.
type taskStateCommand struct {
	manager *Manager
	id      string
}

// mirror is the single locked handoff from a successful DB write to live state.
// The caller continues to own taskStateMu and any subsequent scanner effects.
func (command taskStateCommand) mirror(update func(*taskLifecycleState)) {
	command.manager.mu.Lock()
	if t := command.manager.tasks[command.id]; t != nil {
		t.updateLifecycle(update)
	}
	command.manager.mu.Unlock()
}
func (command taskStateCommand) paused(paused bool) error {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	n, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil {
		return err
	}
	if err := command.manager.pg.SetPaused(n, paused); err != nil {
		return err
	}
	command.mirror(func(state *taskLifecycleState) {
		state.Paused = paused
	})
	return nil
}

func (command taskStateCommand) dequeue(clearMode bool) error {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	n, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil {
		return err
	}
	if err := command.manager.pg.Dequeue(n, clearMode); err != nil {
		return err
	}
	command.mirror(func(state *taskLifecycleState) {
		state.Queued = false
		state.QueuedAt = 0
		if clearMode {
			state.QueueMode = ""
		}
	})
	return nil
}

func (command taskStateCommand) guarded(status string) (won bool, err error) {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	n, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil {
		return false, err
	}
	won, err = command.manager.pg.SetTerminalStatusGuarded(n, status)
	if err != nil || !won {
		return won, err
	}
	command.mirror(func(state *taskLifecycleState) {
		state.Status = status
		if state.CompletedAt == 0 {
			state.CompletedAt = time.Now().Unix()
		}
	})
	command.manager.cancelKatanaTask(n, "task entered terminal state "+status)
	command.manager.cancelArsenalTask(n, "task entered terminal state "+status)
	return true, nil
}

func (command taskStateCommand) status(status string) error {
	command.manager.taskStateMu.Lock()
	defer command.manager.taskStateMu.Unlock()
	n, err := strconv.ParseInt(command.id, 10, 64)
	if err != nil {
		return err
	}
	if err := command.manager.pg.SetStatus(n, status); err != nil {
		return err
	}
	command.mirror(func(state *taskLifecycleState) {
		state.Status = status
		// Mirror the DB's completed_at stamp on the live handle so the DTO shows
		// the finish time without a reload (terminal -> stamp once; else clear).
		if pgdb.IsTerminal(status) {
			if state.CompletedAt == 0 {
				state.CompletedAt = time.Now().Unix()
			}
		} else {
			state.CompletedAt = 0
		}
	})
	if pgdb.IsTerminal(status) {
		command.manager.cancelKatanaTask(n, "task entered terminal state "+status)
		command.manager.cancelArsenalTask(n, "task entered terminal state "+status)
	}
	return nil
}
