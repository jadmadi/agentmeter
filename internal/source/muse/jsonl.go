// SPDX-License-Identifier: AGPL-3.0-only

package muse

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// sessionFileName is the only file the loader reads. Session directories
// also hold SQLite stores, lock files, CLI logs and tool-output dumps, all
// of which are ignored.
const sessionFileName = "session.jsonl"

// discoverFiles returns every session.jsonl under root in sorted order:
// <root>/<date>/<session-id>/session.jsonl plus
// <session>/subagent/<id>/session.jsonl child logs. Hidden directories are
// skipped; a missing root is an empty source.
func discoverFiles(root string) []string {
	expanded := expandHome(root)
	var files []string
	_ = filepath.WalkDir(expanded, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := entry.Name()
		if entry.IsDir() {
			if strings.HasPrefix(name, ".") && path != expanded {
				return filepath.SkipDir
			}
			return nil
		}
		if name != sessionFileName {
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	sort.Strings(files)
	return files
}

// readLines reads a JSONL file the way Python's text mode does:
// universal newlines, a lone trailing line without "\n" still counts.
func readLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\n"))
	raw = bytes.ReplaceAll(raw, []byte("\r"), []byte("\n"))
	text := stripInvalidUTF8(raw)
	lines := strings.Split(string(text), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	kept := lines[:0]
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, line)
	}
	return kept, nil
}

// stripInvalidUTF8 drops undecodable bytes, matching CPython's UTF-8 decoder
// in errors="ignore" mode closely enough for record scanning.
func stripInvalidUTF8(raw []byte) []byte {
	return []byte(strings.ToValidUTF8(string(raw), ""))
}

// decodeLine parses one JSONL line into a record. Blank lines, invalid JSON
// and values that are not objects are skipped.
func decodeLine(line string) map[string]any {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	record, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return record
}
