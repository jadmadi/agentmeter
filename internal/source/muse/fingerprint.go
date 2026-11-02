// SPDX-License-Identifier: AGPL-3.0-only

package muse

import (
	"fmt"
	"os"
	"strings"
)

// Fingerprint returns a cheap change signature for the source tree: every
// JSONL path plus its size and nanosecond mtime. It follows the same
// discovery rules as Load, so hidden files and non-JSONL paths do not trigger
// spurious rebuilds.
func Fingerprint(root string) string {
	files := discoverFiles(root)
	var b strings.Builder
	for _, path := range files {
		appendStat(&b, path)
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
