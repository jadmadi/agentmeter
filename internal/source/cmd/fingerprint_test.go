// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFingerprintChangesWithSource(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "s.jsonl")
	if err := os.WriteFile(path, []byte("{\"type\":\"session\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	first := Fingerprint(root)
	if err := os.WriteFile(path, []byte("{\"type\":\"session\"}\n{\"type\":\"message\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := Fingerprint(root)
	if first == second {
		t.Fatal("fingerprint did not change after JSONL edit")
	}
}

func TestFingerprintIgnoresHiddenFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden", "s.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := Fingerprint(root)
	if err := os.WriteFile(filepath.Join(root, ".hidden", "s.jsonl"), []byte("{}\n{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	after := Fingerprint(root)
	if before != after {
		t.Fatal("hidden source should not affect the fingerprint")
	}
}
