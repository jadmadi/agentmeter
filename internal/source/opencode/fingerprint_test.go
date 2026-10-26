// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintChangesWithDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	if err := os.WriteFile(path, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := Fingerprint(path)
	if err := os.WriteFile(path, []byte("one two"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := Fingerprint(path)
	if first == second {
		t.Fatal("fingerprint did not change after DB edit")
	}
}
