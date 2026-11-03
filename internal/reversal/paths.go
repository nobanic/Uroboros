package reversal

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Paths restores the newest move first and retains a failed stage for recovery.
// The caller supplies its existing parent policy and joins errors after marking
// its own lifecycle state; the returned slice keeps the original error leaves.
func Paths[M any](stageDir string, moves []M, paths func(M) (string, string), parentMode fs.FileMode, stageName string) []error {
	var errs []error
	for i := len(moves) - 1; i >= 0; i-- {
		source, staged := paths(moves[i])
		if _, err := os.Lstat(source); err == nil {
			errs = append(errs, fmt.Errorf("restore destination already exists: %s", source))
			continue
		} else if !os.IsNotExist(err) {
			errs = append(errs, fmt.Errorf("inspect restore destination %s: %w", source, err))
			continue
		}
		if parentMode != 0 {
			if err := os.MkdirAll(filepath.Dir(source), parentMode); err != nil {
				errs = append(errs, fmt.Errorf("create restore parent for %s: %w", source, err))
				continue
			}
		}
		if err := os.Rename(staged, source); err != nil {
			errs = append(errs, fmt.Errorf("restore %s: %w", source, err))
		}
	}
	if len(errs) == 0 && stageDir != "" {
		if err := os.RemoveAll(stageDir); err != nil {
			errs = append(errs, fmt.Errorf("remove %s stage: %w", stageName, err))
		}
	}
	return errs
}
