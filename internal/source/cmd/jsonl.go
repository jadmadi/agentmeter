// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const jsonlSuffix = ".jsonl"

// discoverFiles reproduces
// `sorted(glob.glob(os.path.join(root, "**", "*.jsonl"), recursive=True))`:
// recursive, lexicographic path order, hidden files and hidden directories
// skipped, symlinked directories not followed (files linked into the tree are
// still listed).
func discoverFiles(root string) []string {
	resolved, ok := resolveRoot(expandHome(root))
	if !ok {
		return nil
	}
	files := make([]string, 0, 64)
	_ = filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if path == resolved {
			return nil
		}
		name := entry.Name()
		if strings.HasPrefix(name, ".") {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if strings.HasSuffix(name, jsonlSuffix) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files
}

// resolveRoot requires a readable directory, like the Python glob. A missing
// root, or a root that is a regular file, yields no matches.
func resolveRoot(root string) (string, bool) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return "", false
	}
	// WalkDir does not follow a symlinked root, while Python's glob opens the
	// directory directly. Resolve only the root in that case.
	if link, err := os.Lstat(root); err == nil && link.Mode()&fs.ModeSymlink != 0 {
		if target, err := filepath.EvalSymlinks(root); err == nil {
			return target, true
		}
		return "", false
	}
	return root, true
}

// readLines reads a JSONL file the way Python's text mode does:
//   - universal newlines: "\n", "\r\n" and a lone "\r" all end a line;
//   - invalid UTF-8 bytes are dropped, like errors="ignore";
//   - a trailing newline does not produce a final empty line, but any other
//     trailing fragment does.
func readLines(path string) ([][]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !utf8.Valid(raw) {
		raw = stripInvalidUTF8(raw)
	}
	return splitLines(raw), nil
}

// stripInvalidUTF8 drops undecodable bytes, matching CPython's UTF-8 decoder
// with errors="ignore".
func stripInvalidUTF8(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	for index := 0; index < len(raw); {
		byteValue := raw[index]
		if byteValue < utf8.RuneSelf {
			out = append(out, byteValue)
			index++
			continue
		}
		runeValue, size := utf8.DecodeRune(raw[index:])
		if runeValue == utf8.RuneError && size == 1 {
			index++
			continue
		}
		out = append(out, raw[index:index+size]...)
		index += size
	}
	return out
}

func splitLines(raw []byte) [][]byte {
	lines := make([][]byte, 0, 128)
	start := 0
	for index := 0; index < len(raw); {
		switch raw[index] {
		case '\n':
			lines = append(lines, raw[start:index])
			index++
			start = index
		case '\r':
			lines = append(lines, raw[start:index])
			index++
			if index < len(raw) && raw[index] == '\n' {
				index++
			}
			start = index
		default:
			index++
		}
	}
	if start < len(raw) {
		lines = append(lines, raw[start:])
	}
	return lines
}

// orderedEntries preserves first-insertion order while a repeated message id
// overwrites in place, matching Python's dict semantics.
type orderedEntries struct {
	order []string
	byID  map[string]map[string]any
}

func newOrderedEntries() *orderedEntries {
	return &orderedEntries{byID: make(map[string]map[string]any)}
}

func (entries *orderedEntries) set(id string, record map[string]any) {
	if _, exists := entries.byID[id]; !exists {
		entries.order = append(entries.order, id)
	}
	entries.byID[id] = record
}

func (entries *orderedEntries) get(id string) map[string]any {
	return entries.byID[id]
}

// decodeLine parses one JSONL record. Lines that are not valid JSON, and JSON
// values that are not objects, are skipped: the Python loader would raise on
// the latter, so those files cannot produce a report at all.
func decodeLine(line []byte) map[string]any {
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" {
		return nil
	}
	var value any
	if err := json.Unmarshal([]byte(trimmed), &value); err != nil {
		return nil
	}
	return asObject(value)
}
