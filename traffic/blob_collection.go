package traffic

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Collection borrows the caller's write exclusion. SQL references and legacy
// pointers are gathered before any filesystem removal; walk/remove errors retain
// their original best-effort policy. No body or reference record is rewritten.
type blobReferences map[string]struct{}

func collectTrafficBlobs(store *Traffic) error {
	live := make(blobReferences)
	rows, err := store.db.Query(`SELECT DISTINCT hash FROM blob_refs`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var hash string
		if err := rows.Scan(&hash); err != nil {
			rows.Close()
			return err
		}
		live[hash] = struct{}{}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if err := store.legacyBlobRefs(live); err != nil {
		return err
	}
	return live.sweep(filepath.Join(store.dir, "_blobs", "sha256"))
}

func (live blobReferences) addLegacy(store *Traffic) error {
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM exchanges WHERE path<>''`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	pointer := regexp.MustCompile(`@blob sha256:([0-9a-f]{64})`)
	return filepath.WalkDir(store.dir, func(filename string, entry fs.DirEntry, failure error) error {
		if failure != nil || entry == nil {
			return nil
		}
		if entry.IsDir() {
			if filename != store.dir && strings.HasPrefix(entry.Name(), "_") {
				return filepath.SkipDir
			}
			return nil
		}
		switch entry.Name() {
		case "request.http", "response.http":
		default:
			return nil
		}
		contents, err := os.ReadFile(filename)
		if err == nil {
			for _, match := range pointer.FindAllSubmatch(contents, -1) {
				live[string(match[1])] = struct{}{}
			}
		}
		return nil
	})
}

func (live blobReferences) sweep(root string) error {
	var buckets []string
	err := filepath.WalkDir(root, func(filename string, entry fs.DirEntry, failure error) error {
		if failure != nil || entry == nil {
			return nil
		}
		if entry.IsDir() {
			if filename != root {
				buckets = append(buckets, filename)
			}
			return nil
		}
		if _, retained := live[strings.TrimSuffix(entry.Name(), ".bin")]; !retained {
			os.Remove(filename)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(buckets, func(i, j int) bool { return len(buckets[i]) > len(buckets[j]) })
	for _, bucket := range buckets {
		os.Remove(bucket)
	}
	return nil
}
