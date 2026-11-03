//go:build embedui

package server

import (
	"io/fs"
	"strings"
)

// exportSegmentAliases indexes the immutable embedded export once per handler.
// A segment request is flattened only after its __next. component. Ambiguous
// names and incomplete indexes supply no aliases; normal file/page dispatch
// remains available through the original request policy.
func exportSegmentAliases(root fs.FS) map[string]string {
	aliases := make(map[string]string)
	ambiguous := make(map[string]bool)
	err := fs.WalkDir(root, ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(name, ".txt") {
			return nil
		}
		start := 0
		if !strings.HasPrefix(name, "__next.") {
			marker := strings.Index(name, "/__next.")
			if marker < 0 {
				return nil
			}
			start = marker + 1
		}
		alias := name[:start] + strings.ReplaceAll(name[start:], "/", ".")
		if alias == name || ambiguous[alias] {
			return nil
		}
		if previous, exists := aliases[alias]; exists && previous != name {
			delete(aliases, alias)
			ambiguous[alias] = true
			return nil
		}
		aliases[alias] = name
		return nil
	})
	if err != nil {
		return nil
	}
	return aliases
}
