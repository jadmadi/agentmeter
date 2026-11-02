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

func TestRunDBDirectory(t *testing.T) {
	code, _, stderr := runCLI(t, "--db", t.TempDir(), "--out", t.TempDir())
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "--db is a directory") {
		t.Fatalf("stderr = %s", stderr)
	}
	if strings.Contains(stderr, "disk I/O error") {
		t.Fatalf("stderr kept the disk error: %s", stderr)
	}
}

func TestRunDBPermissionError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a chmod 000 directory")
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	if err := os.WriteFile(dbPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o755)
	code, _, stderr := runCLI(t, "--db", dbPath, "--out", t.TempDir())
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "permission denied") {
		t.Fatalf("stderr = %s", stderr)
	}
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

func coverageModel(short string, steps, timed int64, fixed any, low bool) map[string]any {
	return map[string]any{
		"short": short, "model": short, "steps": steps, "timedN": timed,
		"fixedSeconds": fixed, "lowSample": low,
		"weighted": 100.0, "median": 100.0, "p90": 100.0,
		"visibleMedian": 100.0, "pureRate": 100.0, "reasoningShare": 0.5,
	}
}

func TestPrintSummaryCoverage(t *testing.T) {
	data := map[string]any{
		"meta":    map[string]any{"stepsScanned": int64(200), "sessions": int64(1), "models": int64(3)},
		"totals":  map[string]any{"steps": int64(200)},
		"timed":   map[string]any{"all": map[string]any{"n": int64(115)}},
		"cache":   map[string]any{},
		"cost":    map[string]any{},
		"runtime": map[string]any{},
		"byModel": []any{
			coverageModel("high-cov", 100, 95, 0.5, false),
			coverageModel("low-cov", 100, 20, 2.0, false),
			coverageModel("zero-steps", 0, 0, nil, false),
		},
	}
	var out strings.Builder
	printSummary(&out, data, "json", "js", "html")
	output := out.String()
	if !strings.Contains(output, "95%") || !strings.Contains(output, "20%") {
		t.Errorf("missing coverage percentages:\n%s", output)
	}
	if !strings.Contains(output, "COVERAGE low coverage") {
		t.Errorf("missing coverage note:\n%s", output)
	}
	fixed := lineWithPrefix(output, "FIXED ")
	if !strings.Contains(fixed, "high-cov") || strings.Contains(fixed, "low-cov") {
		t.Errorf("fixed line = %q", fixed)
	}

	data["byModel"] = []any{
		coverageModel("low-a", 100, 60, 1.0, false),
		coverageModel("low-b", 100, 20, 2.0, false),
	}
	out.Reset()
	printSummary(&out, data, "json", "js", "html")
	if fixed := lineWithPrefix(out.String(), "FIXED "); fixed != "" {
		t.Errorf("all-low-coverage printed a fixed line: %q", fixed)
	}
}

func lineWithPrefix(output, prefix string) string {
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func TestPrintSummaryClockByTool(t *testing.T) {
	data := map[string]any{
		"meta":   map[string]any{"stepsScanned": int64(10), "sessions": int64(1), "models": int64(1)},
		"totals": map[string]any{"steps": int64(10)},
		"timed":  map[string]any{"all": map[string]any{"n": int64(10)}},
		"cache":  map[string]any{},
		"cost":   map[string]any{},
		"runtime": map[string]any{
			"steps": int64(1),
			"byTool": []any{
				map[string]any{"tool": "shell", "calls": int64(10), "allocatedSeconds": 100.0, "weightedShare": 0.3, "medianToolSeconds": 0.08, "p90ToolSeconds": 8.02},
				map[string]any{"tool": "subagent", "calls": int64(2), "allocatedSeconds": 200.0, "weightedShare": 0.4, "medianToolSeconds": 141.4, "p90ToolSeconds": 639.43},
				map[string]any{"tool": "read", "calls": int64(5), "allocatedSeconds": 50.0, "weightedShare": nil},
			},
		},
		"byModel": []any{},
	}
	var out strings.Builder
	printSummary(&out, data, "json", "js", "html")
	output := out.String()
	if !strings.Contains(output, "CLOCK BY TOOL") {
		t.Fatalf("missing CLOCK BY TOOL section:\n%s", output)
	}
	if strings.Index(output, "subagent") > strings.Index(output, "shell") {
		t.Errorf("allocated order should put subagent before shell:\n%s", output)
	}
	if !strings.Contains(output, "wait") {
		t.Errorf("missing wait kind:\n%s", output)
	}
	if strings.Contains(output, "read") {
		t.Errorf("null-share read row should be skipped:\n%s", output)
	}

	data["runtime"] = map[string]any{"steps": int64(1), "byTool": []any{}}
	out.Reset()
	printSummary(&out, data, "json", "js", "html")
	if strings.Contains(out.String(), "CLOCK BY TOOL") {
		t.Errorf("empty byTool printed the section:\n%s", out.String())
	}

	data["runtime"] = map[string]any{"steps": int64(1)}
	out.Reset()
	printSummary(&out, data, "json", "js", "html")
	if strings.Contains(out.String(), "CLOCK BY TOOL") {
		t.Errorf("missing byTool printed the section:\n%s", out.String())
	}
}

func TestPrintSummaryCostCaveats(t *testing.T) {
	data := map[string]any{
		"meta":    map[string]any{"stepsScanned": int64(10), "sessions": int64(1), "models": int64(1)},
		"totals":  map[string]any{"steps": int64(10), "generatedTokens": int64(1000)},
		"timed":   map[string]any{"all": map[string]any{"n": int64(10)}},
		"cache":   map[string]any{"savings": 1232.55831, "hitRate": 0.98, "writeShare": 0.0},
		"cost":    map[string]any{"recorded": 64.512617, "perStep": 0.0021},
		"runtime": map[string]any{},
		"byModel": []any{},
	}
	var out strings.Builder
	printSummary(&out, data, "json", "js", "html")
	output := out.String()
	for _, want := range []string{
		"recorded (floor)",
		"counterfactual cache savings",
		"free and subscription models report $0",
		"cache writes are not reported, not zero",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("summary missing %q:\n%s", want, output)
		}
	}

	data["cache"] = map[string]any{"hitRate": 0.98, "writeShare": 0.0}
	out.Reset()
	printSummary(&out, data, "json", "js", "html")
	output = out.String()
	if strings.Contains(output, "$null") || strings.Contains(output, "~$") {
		t.Errorf("missing savings printed a null value:\n%s", output)
	}
}

func TestRunJSON(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	writeTestStore(t, dbPath)
	outDir := filepath.Join(t.TempDir(), "out")
	code, stdout, stderr := runCLI(t, "--db", dbPath, "--out", outDir, "--json")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	if !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout is not valid JSON:\n%s", stdout)
	}
	if strings.Contains(stdout, "AgentMeter") || strings.Contains(stdout, "Open ") {
		t.Errorf("stdout contains human summary:\n%s", stdout)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(stdout), &data); err != nil {
		t.Fatal(err)
	}
	if data["schemaVersion"].(float64) != 3 {
		t.Errorf("schemaVersion = %v", data["schemaVersion"])
	}
	if _, err := os.Stat(filepath.Join(outDir, "telemetry-data.json")); err != nil {
		t.Errorf("report file missing: %v", err)
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
