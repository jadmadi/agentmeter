// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ResolveRoot expands ~, verifies that root is a directory, and returns the
// absolute path the scan uses. On failure it returns the expanded path so the
// caller can name what it tried.
func ResolveRoot(root string) (string, error) {
	expanded := expandHome(root)
	info, err := os.Stat(expanded)
	if err != nil {
		return expanded, err
	}
	if !info.IsDir() {
		return expanded, fmt.Errorf("not a directory")
	}
	dir, err := os.Open(expanded)
	if err != nil {
		return expanded, err
	}
	_ = dir.Close()
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return expanded, err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return expanded, err
	}
	return resolved, nil
}

// discoverFiles finds all session .jsonl(.zstd) files under root (preferring
// <root>/sessions when present) and deduplicates multi-version session files
// within the same session directory so only the highest version (e.g.
// session.v4.jsonl.zstd over session.jsonl.zstd) is loaded. A missing root
// returns an empty slice.
func discoverFiles(root string) []string {
	expanded := expandHome(root)
	info, err := os.Stat(expanded)
	if err != nil || !info.IsDir() {
		return nil
	}

	walkRoot := expanded
	if sessionsDir := filepath.Join(expanded, "sessions"); isDir(sessionsDir) {
		walkRoot = sessionsDir
	}

	type versionedCandidate struct {
		path       string
		version    int
		compressed bool
	}
	bestByDir := make(map[string]versionedCandidate)
	var standalone []string

	_ = filepath.WalkDir(walkRoot, func(path string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		name := dirEntry.Name()
		if dirEntry.IsDir() {
			if (strings.HasPrefix(name, ".") && path != walkRoot) || name == "storages" || name == "logs" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(name, ".") {
			return nil
		}
		version, compressed, isStandardSession, ok := classifySessionFilename(name)
		if !ok {
			return nil
		}
		if !isStandardSession {
			standalone = append(standalone, path)
			return nil
		}
		dir := filepath.Dir(path)
		prev, exists := bestByDir[dir]
		if !exists ||
			version > prev.version ||
			(version == prev.version && compressed && !prev.compressed) ||
			(version == prev.version && compressed == prev.compressed && path < prev.path) {
			bestByDir[dir] = versionedCandidate{
				path:       path,
				version:    version,
				compressed: compressed,
			}
		}
		return nil
	})

	files := make([]string, 0, len(bestByDir)+len(standalone))
	for _, cand := range bestByDir {
		files = append(files, cand.path)
	}
	files = append(files, standalone...)
	sort.Strings(files)
	return files
}

func classifySessionFilename(name string) (version int, compressed bool, isStandardSession bool, ok bool) {
	lower := strings.ToLower(name)
	base := lower
	switch {
	case strings.HasSuffix(lower, ".jsonl.zstd"):
		compressed = true
		base = strings.TrimSuffix(lower, ".jsonl.zstd")
	case strings.HasSuffix(lower, ".jsonl.zst"):
		compressed = true
		base = strings.TrimSuffix(lower, ".jsonl.zst")
	case strings.HasSuffix(lower, ".jsonl"):
		compressed = false
		base = strings.TrimSuffix(lower, ".jsonl")
	default:
		return 0, false, false, false
	}

	if base == "session" {
		return 0, compressed, true, true
	}
	if strings.HasPrefix(base, "session.v") {
		verText := strings.TrimPrefix(base, "session.v")
		if v, err := strconv.Atoi(verText); err == nil && v >= 0 {
			return v, compressed, true, true
		}
	}
	return 0, compressed, false, true
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func defaultSessionIDForPath(path string) string {
	base := filepath.Base(path)
	lower := strings.ToLower(base)
	if strings.HasPrefix(lower, "session") {
		dir := filepath.Base(filepath.Dir(path))
		if dir != "" && dir != "." && dir != "/" {
			return dir
		}
	}
	for _, suffix := range []string{".jsonl.zstd", ".jsonl.zst", ".jsonl"} {
		if strings.HasSuffix(lower, suffix) {
			return base[:len(base)-len(suffix)]
		}
	}
	return base
}

func inferCWDFromPath(path string) string {
	dir := filepath.Dir(path)
	parent := filepath.Base(filepath.Dir(dir))
	if strings.HasPrefix(parent, "--") && strings.HasSuffix(parent, "--") {
		trimmed := strings.TrimSuffix(strings.TrimPrefix(parent, "--"), "--")
		if trimmed != "" {
			return "/" + strings.ReplaceAll(trimmed, "-", "/")
		}
	}
	return ""
}

func resolveCWD(cwd, sessionID string, projMeta map[string]projSessionMeta) string {
	if cwd != "" {
		return cwd
	}
	if meta, ok := projMeta[sessionID]; ok && meta.CWD != "" {
		return meta.CWD
	}
	return "(unknown)"
}

func inferProvider(model string) string {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "deepseek"):
		return "deepseek"
	case strings.Contains(m, "claude"):
		return "anthropic"
	case strings.Contains(m, "gpt") || strings.HasPrefix(m, "o1") || strings.HasPrefix(m, "o3") || strings.HasPrefix(m, "o4"):
		return "openai"
	case strings.Contains(m, "gemini"):
		return "google"
	case strings.Contains(m, "glm"):
		return "zhipu"
	case strings.Contains(m, "qwen"):
		return "alibaba"
	case strings.Contains(m, "kimi"):
		return "moonshot"
	default:
		return "deepseek"
	}
}
