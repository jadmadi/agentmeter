// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, no CGO.
)

// BusyTimeoutMillis mirrors the Python builder's sqlite3 timeout=15.
const BusyTimeoutMillis = 15000

// DefaultDBPath mirrors telemetry.py:default_db_path so the CLI keeps the same
// platform autodetection and the same fallback candidate.
func DefaultDBPath() string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		filepath.Join(home, ".local", "share", "opencode", "opencode.db"),
		filepath.Join(home, "Library", "Application Support", "opencode", "opencode.db"),
		filepath.Join(os.Getenv("APPDATA"), "opencode", "opencode.db"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

// ExpandUser mirrors Path.expanduser for the leading "~" form.
func ExpandUser(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// fileURI builds the read-only SQLite URI for an absolute path. The Python
// builder uses Path.as_uri() + "?mode=ro"; keep the same shape.
func fileURI(abs string) string {
	slashed := filepath.ToSlash(abs)
	if !strings.HasPrefix(slashed, "/") {
		// Windows drive letters need the extra slash to stay an absolute URI.
		slashed = "/" + slashed
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

// OpenRO opens the store read-only and validates the v2 table set.
//
// Errors mirror telemetry.py:connect_ro messages so the CLI reads the same.
func OpenRO(path string) (*sql.DB, error) {
	expanded := ExpandUser(path)
	if _, err := os.Stat(expanded); err != nil {
		return nil, fmt.Errorf(
			"OpenCode database not found at %s. Pass --db <path> or set the correct OpenCode data directory",
			expanded)
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return nil, fmt.Errorf("Could not resolve OpenCode database at %s: %v. Pass --db <path> if it lives elsewhere", expanded, err)
	}
	dsn := fileURI(abs) + "?mode=ro"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("Could not open OpenCode database at %s: %v. Pass --db <path> if it lives elsewhere", expanded, err)
	}
	// One connection keeps every read on the same snapshot and avoids
	// checkpoint contention with a live OpenCode process.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(fmt.Sprintf("PRAGMA busy_timeout = %d", BusyTimeoutMillis)); err != nil {
		db.Close()
		return nil, fmt.Errorf("Could not open OpenCode database at %s: %v. Pass --db <path> if it lives elsewhere", expanded, err)
	}
	tables, err := tableNames(db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("Could not open OpenCode database at %s: %v. Pass --db <path> if it lives elsewhere", expanded, err)
	}
	missing := make([]string, 0, 2)
	for _, required := range []string{"session_v2", "session_message"} {
		if !tables[required] {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		db.Close()
		sort.Strings(missing)
		return nil, fmt.Errorf(
			"OpenCode database schema not recognized: missing %s. This report supports the session_v2/session_message store",
			strings.Join(missing, ", "))
	}
	return db, nil
}

func tableNames(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tables := map[string]bool{}
	for rows.Next() {
		var name sql.NullString
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if name.Valid {
			tables[name.String] = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return tables, nil
}
