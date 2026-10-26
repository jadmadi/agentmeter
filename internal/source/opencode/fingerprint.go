// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import (
	"fmt"
	"os"
	"strings"
)

// Fingerprint returns a cheap change signature for the SQLite store and its
// WAL/SHM companions. A missing database is represented explicitly so the
// watcher can recover when it appears later.
func Fingerprint(dbPath string) string {
	var b strings.Builder
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := dbPath + suffix
		info, err := os.Stat(path)
		if err != nil {
			fmt.Fprintf(&b, "%s\x00missing\n", path)
			continue
		}
		fmt.Fprintf(&b, "%s\x00%d\x00%d\n", path, info.Size(), info.ModTime().UnixNano())
	}
	return b.String()
}
