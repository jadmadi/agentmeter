// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Fingerprint returns a deterministic change signature for the DSH source tree:
// every discovered session JSONL(.zstd) file and projection cache JSON file
// plus its size and nanosecond mtime.
func Fingerprint(root string) string {
	files := discoverFiles(root)
	var b strings.Builder
	for _, path := range files {
		appendStat(&b, path)
	}
	for _, base := range projCacheCandidateRoots(expandHome(root)) {
		sessionsDir := filepath.Join(base, "storages", "session_projcache", "sessions")
		entries, err := os.ReadDir(sessionsDir)
		if err == nil {
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
					names = append(names, entry.Name())
				}
			}
			sort.Strings(names)
			for _, name := range names {
				appendStat(&b, filepath.Join(sessionsDir, name))
			}
		}
		indexPath := filepath.Join(base, "storages", "session_projcache.json")
		if _, err := os.Stat(indexPath); err == nil {
			appendStat(&b, indexPath)
		}
	}
	return b.String()
}

func appendStat(b *strings.Builder, path string) {
	info, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(b, "%s\x00missing\n", path)
		return
	}
	fmt.Fprintf(b, "%s\x00%d\x00%d\n", path, info.Size(), info.ModTime().UnixNano())
}
