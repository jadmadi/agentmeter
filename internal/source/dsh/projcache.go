// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type projSessionMeta struct {
	SessionID string
	CWD       string
	Title     string
	CreatedAt *time.Time
	UpdatedAt *time.Time
}

// loadProjCache reads optional session metadata from
// <root>/storages/session_projcache/sessions/*.json (or a sibling storages/
// directory when root points at ~/.dsh/sessions) and
// <root>/storages/session_projcache.json.
func loadProjCache(root string, includeTitles bool) map[string]projSessionMeta {
	result := make(map[string]projSessionMeta)
	for _, base := range projCacheCandidateRoots(root) {
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
				if meta, ok := readProjSessionFile(filepath.Join(sessionsDir, name), includeTitles); ok {
					result[meta.SessionID] = meta
				}
			}
		}

		indexPath := filepath.Join(base, "storages", "session_projcache.json")
		readProjCacheIndex(indexPath, includeTitles, result)
	}
	return result
}

func projCacheCandidateRoots(root string) []string {
	cleaned := filepath.Clean(root)
	out := []string{cleaned}
	if filepath.Base(cleaned) == "sessions" {
		parent := filepath.Dir(cleaned)
		if parent != cleaned {
			out = append(out, parent)
		}
	}
	return out
}

func readProjSessionFile(path string, includeTitles bool) (projSessionMeta, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return projSessionMeta{}, false
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return projSessionMeta{}, false
	}
	return parseProjSessionEntry(doc, strings.TrimSuffix(filepath.Base(path), ".json"), includeTitles)
}

func readProjCacheIndex(path string, includeTitles bool, dst map[string]projSessionMeta) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return
	}
	sessions := asObject(doc["sessions"])
	if sessions == nil {
		return
	}
	keys := make([]string, 0, len(sessions))
	for k := range sessions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, exists := dst[key]; exists {
			continue
		}
		entry := asObject(sessions[key])
		if entry == nil {
			continue
		}
		if meta, ok := parseProjSessionEntry(entry, key, includeTitles); ok {
			dst[meta.SessionID] = meta
		}
	}
}

func parseProjSessionEntry(doc map[string]any, fallbackID string, includeTitles bool) (projSessionMeta, bool) {
	identity := asObject(doc["identity"])
	rows := asObject(doc["rows"])
	sid := strings.TrimSpace(asString(identity["id"]))
	if sid == "" {
		sid = strings.TrimSpace(fallbackID)
	}
	if sid == "" {
		return projSessionMeta{}, false
	}
	cwd := strings.TrimSpace(asString(identity["cwd"]))
	if cwd == "" {
		cwd = strings.TrimSpace(asString(asObject(rows["workingDirectory"])["val"]))
	}
	title := ""
	if includeTitles {
		title = strings.TrimSpace(asString(asObject(rows["title"])["val"]))
	}
	createdAt := parseTimestamp(identity["createdAt"])
	updatedAt := parseTimestamp(asObject(rows["updatedAt"])["val"])
	return projSessionMeta{
		SessionID: sid,
		CWD:       cwd,
		Title:     title,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}, true
}
