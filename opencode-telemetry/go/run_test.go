// SPDX-License-Identifier: AGPL-3.0-only

package octelemetry

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSchema = `
CREATE TABLE session_v2 (
    id TEXT PRIMARY KEY,
    title TEXT,
    slug TEXT,
    directory TEXT,
    parent_id TEXT,
    fork_session_id TEXT,
    version TEXT,
    time_created INTEGER
);
CREATE TABLE session_message (
    id TEXT PRIMARY KEY,
    session_id TEXT,
    type TEXT,
    time_created INTEGER,
    data TEXT
);
`

// writeTestStore creates a minimal but valid v2 store.
func writeTestStore(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO session_v2 (id, title, slug, directory, parent_id, fork_session_id, version, time_created) VALUES (?,?,?,?,?,?,?,?)",
		"ses1", "CLI fixture", "cli-fixture", "/tmp/cli-project", nil, nil, "1.15.0", int64(1000),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	created := int64(1_767_225_600_000)
	payloads := []struct {
		id      string
		model   string
		output  int64
		created int64
	}{
		{"m1", "model-a", 60, created},
		{"m2", "model-a", 120, created + 60_000},
		{"m3", "model-b", 90, created + 120_000},
	}
	for _, item := range payloads {
		payload := map[string]any{
			"model":   map[string]any{"providerID": "test", "id": item.model},
			"tokens":  map[string]any{"input": 200, "output": item.output, "reasoning": 10, "cache": map[string]any{"read": 0, "write": 0}},
			"time":    map[string]any{"created": item.created, "streamed": item.created + 1200, "completed": item.created + 1500},
			"content": []any{map[string]any{"type": "text"}},
			"finish":  "stop",
			"agent":   "build",
			"cost":    0.001,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		if _, err := db.Exec(
			"INSERT INTO session_message (id, session_id, type, time_created, data) VALUES (?,?,?,?,?)",
			item.id, "ses1", "assistant", item.created, string(raw),
		); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	if _, err := db.Exec(
		"INSERT INTO session_message (id, session_id, type, time_created, data) VALUES (?,?,?,?,?)",
		"c1", "ses1", "compaction", created+30_000, `{"tokensBefore": 1000}`,
	); err != nil {
		t.Fatalf("insert compaction: %v", err)
	}
}

// runCLI runs the command with captured streams.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRunMissingDatabase(t *testing.T) {
	code, _, stderr := runCLI(t, "--db", filepath.Join(t.TempDir(), "absent.db"), "--out", t.TempDir())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "OpenCode database not found at") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestRunRejectsV1Store(t *testing.T) {
	path := filepath.Join(t.TempDir(), "v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE message (id TEXT PRIMARY KEY); CREATE TABLE part (id TEXT PRIMARY KEY);"); err != nil {
		t.Fatalf("create v1 schema: %v", err)
	}
	db.Close()
	code, _, stderr := runCLI(t, "--db", path, "--out", t.TempDir())
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "schema not recognized") || !strings.Contains(stderr, "session_message") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestRunBuildsReport(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	writeTestStore(t, dbPath)
	outDir := filepath.Join(dir, "report")

	code, stdout, stderr := runCLI(t, "--db", dbPath, "--out", outDir)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr)
	}
	for _, name := range []string{"telemetry-data.json", "telemetry-data.js", "index.html"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(outDir, "telemetry-data.json"))
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if report["schemaVersion"].(float64) != 3 {
		t.Fatalf("schemaVersion: %v", report["schemaVersion"])
	}
	totals := report["totals"].(map[string]any)
	if totals["steps"].(float64) != 3 {
		t.Fatalf("steps: %v", totals["steps"])
	}

	js, err := os.ReadFile(filepath.Join(outDir, "telemetry-data.js"))
	if err != nil {
		t.Fatalf("read js: %v", err)
	}
	if !strings.HasPrefix(string(js), "window.OPENCODE_TELEMETRY = ") || !strings.HasSuffix(string(js), ";\n") {
		t.Fatalf("js bundle shape: %q", string(js[:min(60, len(js))]))
	}

	html, err := os.ReadFile(filepath.Join(outDir, "index.html"))
	if err != nil {
		t.Fatalf("read html: %v", err)
	}
	if !strings.Contains(string(html), `src="telemetry-data.js"`) {
		t.Fatal("html must point at the sibling bundle")
	}
	if strings.Contains(string(html), `src="data/telemetry-data.js"`) {
		t.Fatal("html must not keep the data/ prefix")
	}

	if !strings.Contains(stdout, "SOURCE") || !strings.Contains(stdout, "MODELS (top ") {
		t.Fatalf("summary output:\n%s", stdout)
	}
	if !strings.Contains(stdout, "Open "+filepath.Join(outDir, "index.html")) {
		t.Fatalf("missing open hint:\n%s", stdout)
	}
}

func TestRunDefaultOutputIsCwdRelative(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	writeTestStore(t, dbPath)
	t.Chdir(t.TempDir())

	code, _, stderr := runCLI(t, "--db", dbPath)
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join("data", "oc", "telemetry-data.json")); err != nil {
		t.Fatalf("default output must be ./data/oc: %v", err)
	}
}

func TestRunFiltersAndNoTitles(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	writeTestStore(t, dbPath)
	outDir := filepath.Join(dir, "report")

	code, _, stderr := runCLI(t, "--db", dbPath, "--out", outDir,
		"--models", " model-b , ", "--no-titles", "--since", "2026-01-01", "--until", "2026-01-02")
	if code != 0 {
		t.Fatalf("exit code = %d, stderr=%s", code, stderr)
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "telemetry-data.json"))
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		t.Fatalf("parse json: %v", err)
	}
	meta := report["meta"].(map[string]any)
	if meta["includeTitles"].(bool) {
		t.Fatal("--no-titles must be recorded")
	}
	filters := meta["modelFilters"].([]any)
	if len(filters) != 1 || filters[0] != "model-b" {
		t.Fatalf("modelFilters: %v", filters)
	}
	if meta["since"] != "2026-01-01" || meta["until"] != "2026-01-02" {
		t.Fatalf("date filters: %v / %v", meta["since"], meta["until"])
	}
	byModel := report["byModel"].([]any)
	if len(byModel) != 1 {
		t.Fatalf("filtered models: %v", byModel)
	}
	if byModel[0].(map[string]any)["model"] != "test/model-b" {
		t.Fatalf("filtered model: %v", byModel[0])
	}
}

func TestRunFlagErrors(t *testing.T) {
	code, _, stderr := runCLI(t, "--nope")
	if code != 2 {
		t.Fatalf("unknown flag exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "flag provided but not defined") {
		t.Fatalf("stderr: %s", stderr)
	}

	code, _, _ = runCLI(t, "--port", "not-a-number")
	if code != 2 {
		t.Fatalf("bad value exit code = %d, want 2", code)
	}

	code, _, _ = runCLI(t, "--since", "2026-13-45")
	if code != 2 {
		t.Fatalf("bad --since exit code = %d, want 2", code)
	}
	code, _, _ = runCLI(t, "--since", "2026-05-01", "--until", "2026-01-01")
	if code != 2 {
		t.Fatalf("inverted window exit code = %d, want 2", code)
	}

	code, _, stderr = runCLI(t, "extra")
	if code != 2 {
		t.Fatalf("extra argument exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr, "unexpected argument") {
		t.Fatalf("stderr: %s", stderr)
	}
}

func TestRunHelp(t *testing.T) {
	code, stdout, stderr := runCLI(t, "--help")
	if code != 0 {
		t.Fatalf("--help exit code = %d, want 0", code)
	}
	if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "-db") {
		t.Fatalf("help output:\n%s", stdout)
	}
	if stderr != "" {
		t.Fatalf("--help stderr = %q, want empty", stderr)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
