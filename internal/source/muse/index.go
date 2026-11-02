// SPDX-License-Identifier: AGPL-3.0-only

package muse

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGO.
)

// indexFileName is the session metadata store. It lives next to the sessions
// root (<muse>/session-index.db); the loader also probes the root itself so
// a custom root containing both still resolves.
const indexFileName = "session-index.db"

// loadIndex reads session labels keyed by session id. The query selects an
// explicit label-only column list: the index also stores first_user_prompt
// and search_text, which must never be read. A missing or unreadable index
// is not an error; sessions simply carry no labels.
func loadIndex(root string) map[string]SessionMeta {
	meta := make(map[string]SessionMeta)
	path := findIndex(root)
	if path == "" {
		return meta
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return meta
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	rows, err := db.Query(`SELECT session_id, workspace_root, git_branch,
		COALESCE(NULLIF(effective_long_title, ''), NULLIF(title, ''), NULLIF(msp_title, ''))
		FROM sessions`)
	if err != nil {
		return meta
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var workspace, branch, title sql.NullString
		if err := rows.Scan(&id, &workspace, &branch, &title); err != nil {
			continue
		}
		if id == "" {
			continue
		}
		meta[id] = SessionMeta{
			Title:     nullText(title),
			Workspace: nullText(workspace),
			Branch:    nullText(branch),
		}
	}
	return meta
}

// findIndex locates the session index for a sessions root.
func findIndex(root string) string {
	expanded := expandHome(root)
	candidates := []string{
		filepath.Join(expanded, indexFileName),
		filepath.Join(filepath.Dir(expanded), indexFileName),
	}
	for _, candidate := range candidates {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		abs, err := filepath.Abs(candidate)
		if err != nil {
			continue
		}
		return abs
	}
	return ""
}

func nullText(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}
