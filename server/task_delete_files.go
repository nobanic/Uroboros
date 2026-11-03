package server

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Selection precedes physical moves, with the original significant transcript dash.
type taskFilePlan struct {
	dataDir, taskID string
	explorationID   int64
}

func (plan taskFilePlan) prepare() (*taskFileDeleteStage, error) {
	stage := &taskFileDeleteStage{}
	targets, err := plan.targets()
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		stage.done = true
		return stage, nil
	}
	if err := plan.move(stage, targets); err != nil {
		return nil, err
	}
	return stage, nil
}
func (plan taskFilePlan) targets() ([]string, error) {
	var targets []string
	taskDir := filepath.Join(plan.dataDir, "tasks", plan.taskID)
	if _, err := os.Lstat(taskDir); err == nil {
		targets = append(targets, taskDir)
	} else if !os.IsNotExist(err) {
		return nil, err
	}

	transcriptDir := filepath.Join(plan.dataDir, "transcripts")
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		entries = nil
	}
	prefix := fmt.Sprintf("exp%d-", plan.explorationID)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) || (!entry.IsDir() && !strings.HasSuffix(name, ".jsonl")) {
			continue
		}
		targets = append(targets, filepath.Join(transcriptDir, name))
	}
	return targets, nil
}
func (plan taskFilePlan) move(stage *taskFileDeleteStage, targets []string) error {
	var err error
	parent := filepath.Join(plan.dataDir, ".delete-staging")
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	stage.stageDir, err = os.MkdirTemp(parent, "task-"+plan.taskID+"-")
	if err != nil {
		return err
	}
	for _, source := range targets {
		staged := filepath.Join(stage.stageDir, fmt.Sprintf("%d-%s", len(stage.moves), filepath.Base(source)))
		if err := os.Rename(source, staged); err != nil {
			cause := fmt.Errorf("stage task file %s: %w", source, err)
			if restoreErr := stage.rollback(); restoreErr != nil {
				return errors.Join(cause, fmt.Errorf("restore partially staged task files: %w", restoreErr))
			}
			return cause
		}
		stage.moves = append(stage.moves, stagedTaskPath{source: source, staged: staged})
	}
	stage.deleted = true
	return nil
}

func (plan taskFilePlan) remove() (bool, error) {
	stage, err := stageTaskFiles(plan.dataDir, plan.taskID, plan.explorationID)
	if err != nil {
		return false, err
	}
	deleted := stage.deleted
	if err := stage.commit(); err != nil {
		return deleted, err
	}
	return deleted, nil
}
