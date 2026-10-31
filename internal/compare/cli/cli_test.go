// SPDX-License-Identifier: AGPL-3.0-only

package cli

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/jadmadi/agentmeter/internal/emit"
	reportcmd "github.com/jadmadi/agentmeter/internal/report/cmd"
	reportcompare "github.com/jadmadi/agentmeter/internal/report/compare"
	ocreport "github.com/jadmadi/agentmeter/internal/report/opencode"
	sourcecmd "github.com/jadmadi/agentmeter/internal/source/cmd"
)

func repoPath(t *testing.T, parts ...string) string {
	t.Helper()
	base, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo: %v", err)
	}
	return filepath.Join(append([]string{base}, parts...)...)
}

// writeJSON writes a report map as a schema v3 JSON file.
func writeJSON(t *testing.T, path string, data map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(data, "", " ")
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

// buildCMDFixtureReport builds the committed Command Code fixture into a
// report directory.
func buildCMDFixtureReport(t *testing.T, outDir string) {
	t.Helper()
	root := repoPath(t, "command-code-telemetry", "testdata", "fixture")
	source := sourcecmd.Load(sourcecmd.Filters{Root: root, IncludeTitles: true})
	data, err := reportcmd.Build(source, reportcmd.Options{Root: root, IncludeTitles: true})
	if err != nil {
		t.Fatalf("build cmd fixture: %v", err)
	}
	writeJSON(t, filepath.Join(outDir, "telemetry-data.json"), data)
}

const ocFixtureSchema = `
CREATE TABLE session_v2 (
    id TEXT PRIMARY KEY, title TEXT, slug TEXT, directory TEXT,
    parent_id TEXT, fork_session_id TEXT, version TEXT, time_created INTEGER
);
CREATE TABLE session_message (
    id TEXT PRIMARY KEY, session_id TEXT, type TEXT,
    time_created INTEGER, data TEXT
);
`

// buildOCFixtureDB writes a small OpenCode store whose Fast-1 family matches
// the Command Code fixture, so the shared-family path is exercised end to end.
func buildOCFixtureDB(t *testing.T, path string) {
	t.Helper()
	db := createOCStore(t, path)
	defer db.Close()
	if _, err := db.Exec(
		"INSERT INTO session_v2 VALUES (?,?,?,?,?,?,?,?)",
		"ses-oc", "OC fixture", "oc-fixture", "/work/oc", nil, nil, "2.1.4", 1_700_000_000_000,
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	base := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	for index := 0; index < 6; index++ {
		created := base.Add(time.Duration(index) * 20 * time.Minute)
		output := int64(40 + index*10)
		reasoning := int64(10 + index*2)
		payload := map[string]any{
			"model": map[string]any{"providerID": "TestCorp", "id": "Fast-1"},
			"tokens": map[string]any{
				"input": 100 + index*5, "output": output, "reasoning": reasoning,
				"cache": map[string]any{"read": 400 + index*10, "write": index % 2 * 50},
			},
			"time": map[string]any{
				"created":   created.UnixMilli(),
				"streamed":  created.Add(1500 * time.Millisecond).UnixMilli(),
				"completed": created.Add(2 * time.Second).UnixMilli(),
			},
			"content": []any{map[string]any{"type": "text"}},
			"finish":  "stop",
			"agent":   "build",
			"cost":    0.001 * float64(index+1),
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(
			"INSERT INTO session_message VALUES (?,?,?,?,?)",
			"msg-"+string(rune('a'+index)), "ses-oc", "assistant", created.UnixMilli(), string(raw),
		); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
}

// createOCStore opens a SQLite file and installs the OpenCode v2 schema.
func createOCStore(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(ocFixtureSchema); err != nil {
		db.Close()
		t.Fatalf("schema: %v", err)
	}
	return db
}

// buildOCFixtureReport builds the SQLite store into a report directory.
func buildOCFixtureReport(t *testing.T, dbPath, outDir string) {
	t.Helper()
	data, err := ocreport.Build(ocreport.Options{DBPath: dbPath, IncludeTitles: true})
	if err != nil {
		t.Fatalf("build oc fixture: %v", err)
	}
	writeJSON(t, filepath.Join(outDir, "telemetry-data.json"), data)
}

// buildReportPair builds the Command Code fixture report and a matching
// OpenCode report under root.
func buildReportPair(t *testing.T, root string) (ocDir, cmdDir string) {
	t.Helper()
	cmdDir = filepath.Join(root, "cmd-report")
	ocDir = filepath.Join(root, "oc-report")
	buildCMDFixtureReport(t, cmdDir)
	dbPath := filepath.Join(root, "opencode.db")
	buildOCFixtureDB(t, dbPath)
	buildOCFixtureReport(t, dbPath, ocDir)
	return ocDir, cmdDir
}

func TestRunCompareReportInputs(t *testing.T) {
	root := t.TempDir()
	ocDir, cmdDir := buildReportPair(t, root)
	outDir := filepath.Join(root, "out")

	var stdout, stderr strings.Builder
	code := run([]string{"--oc", ocDir, "--cmd", cmdDir, "--out", outDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	for _, name := range []string{"comparison-data.json", "comparison-data.js", "index.html"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("missing artifact %s: %v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "comparison-data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse comparison json: %v", err)
	}
	if doc["kind"] != "comparison" || doc["comparisonVersion"].(float64) != 1 {
		t.Errorf("header = kind %v version %v", doc["kind"], doc["comparisonVersion"])
	}
	meta := doc["meta"].(map[string]any)
	window := meta["window"].(map[string]any)
	if window["mode"] != "overlap" {
		t.Errorf("window mode = %v, want overlap", window["mode"])
	}
	families := doc["models"].(map[string]any)["families"].([]any)
	found := false
	for _, entry := range families {
		family := entry.(map[string]any)
		if family["family"] == "fast-1" {
			found = true
			if family["shared"] != true {
				t.Errorf("fast-1 shared = %v", family["shared"])
			}
		}
	}
	if !found {
		t.Errorf("shared family fast-1 not found in %v", families)
	}
	likeForLike := doc["likeForLike"].(map[string]any)
	if likeForLike["available"] != true {
		t.Error("like-for-like scope should be available for the fixture pair")
	}
	verdictsByScope := doc["verdicts"].(map[string]any)
	for _, scope := range []string{"shared", "all"} {
		verdicts, _ := verdictsByScope[scope].([]any)
		if len(verdicts) != 6 {
			t.Errorf("verdicts.%s = %d, want 6", scope, len(verdicts))
		}
	}
	if days := doc["timeline"].(map[string]any)["days"].([]any); len(days) == 0 {
		t.Error("timeline has no days")
	}
	if !strings.Contains(stdout.String(), "VERDICTS") || !strings.Contains(stdout.String(), "REPORT") {
		t.Errorf("terminal summary missing sections:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "SCOPE") || !strings.Contains(stdout.String(), "like-for-like") {
		t.Errorf("terminal summary should lead with the like-for-like scope:\n%s", stdout.String())
	}

	// The comparison handler serves the new artifact names and nothing else.
	handler := emit.NewHandlerFiles(outDir, emit.ComparisonFiles)
	for _, route := range []string{"/", "/index.html", "/comparison-data.js", "/comparison-data.json"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, route, nil))
		if recorder.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", route, recorder.Code)
		}
	}
	for _, route := range []string{"/telemetry-data.json", "/../comparison-data.json", "/missing"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, route, nil))
		if recorder.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", route, recorder.Code)
		}
	}
}

func TestRunCompareLiveInputs(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "out")
	dbPath := filepath.Join(root, "opencode.db")
	buildOCFixtureDB(t, dbPath)
	cmdRoot := repoPath(t, "command-code-telemetry", "testdata", "fixture")

	var stdout, stderr strings.Builder
	code := run([]string{"--oc", dbPath, "--cmd", cmdRoot, "--out", outDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(filepath.Join(outDir, "comparison-data.json")); err != nil {
		t.Fatalf("missing comparison json: %v", err)
	}
}

func TestRunCompareEmptySources(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "out")

	// Empty Command Code session root.
	cmdRoot := filepath.Join(root, "cmd-empty")
	if err := os.MkdirAll(cmdRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	cmdSource := sourcecmd.Load(sourcecmd.Filters{Root: cmdRoot, IncludeTitles: true})
	cmdData, err := reportcmd.Build(cmdSource, reportcmd.Options{Root: cmdRoot, IncludeTitles: true})
	if err != nil {
		t.Fatalf("build empty cmd report: %v", err)
	}
	cmdDir := filepath.Join(root, "cmd-report")
	writeJSON(t, filepath.Join(cmdDir, "telemetry-data.json"), cmdData)

	// Empty OpenCode store (schema only).
	dbPath := filepath.Join(root, "empty.db")
	db := createOCStore(t, dbPath)
	db.Close()
	ocDir := filepath.Join(root, "oc-report")
	buildOCFixtureReport(t, dbPath, ocDir)

	var stdout, stderr strings.Builder
	code := run([]string{"--oc", ocDir, "--cmd", cmdDir, "--out", outDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "comparison-data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse comparison json: %v", err)
	}
	window := doc["meta"].(map[string]any)["window"].(map[string]any)
	if window["mode"] != "empty" {
		t.Errorf("window mode = %v, want empty", window["mode"])
	}
	for _, side := range []string{"oc", "cmd"} {
		totals := doc["totals"].(map[string]any)[side].(map[string]any)
		if totals["steps"].(float64) != 0 || totals["generatedTokens"].(float64) != 0 {
			t.Errorf("%s empty totals = %v", side, totals)
		}
	}
	verdictsByScope := doc["verdicts"].(map[string]any)
	for _, scope := range []string{"shared", "all"} {
		verdicts, _ := verdictsByScope[scope].([]any)
		if len(verdicts) != 6 {
			t.Errorf("verdicts.%s = %d, want 6", scope, len(verdicts))
		}
	}
	likeForLike := doc["likeForLike"].(map[string]any)
	if likeForLike["available"] != false {
		t.Errorf("empty sources should have no like-for-like scope: %v", likeForLike["available"])
	}
	if days := doc["timeline"].(map[string]any)["days"].([]any); len(days) != 0 {
		t.Errorf("timeline days = %d, want 0", len(days))
	}
}

func TestNormalizeArgs(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
		err  bool
	}{
		{"optional pair", []string{"--oc", "--cmd"}, []string{"--oc=", "--cmd="}, false},
		{"oc value then omitted cmd", []string{"--oc", "data/oc", "--cmd"}, []string{"--oc", "data/oc", "--cmd="}, false},
		{"single dash", []string{"-oc", "-cmd", "--out", "x"}, []string{"-oc=", "-cmd=", "--out", "x"}, false},
		{"equals form", []string{"--oc=data/oc", "--cmd=data/cmd"}, []string{"--oc=data/oc", "--cmd=data/cmd"}, false},
		{"out value", []string{"--out", "x", "--port", "0"}, []string{"--out", "x", "--port", "0"}, false},
		{"out missing", []string{"--out", "--serve"}, nil, true},
		{"port missing", []string{"--port"}, nil, true},
		{"terminator", []string{"--", "--oc"}, []string{"--", "--oc"}, false},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeArgs(test.in)
			if test.err {
				if err == nil {
					t.Fatalf("normalizeArgs(%v) = %v, want error", test.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeArgs(%v): %v", test.in, err)
			}
			if strings.Join(got, "\x00") != strings.Join(test.want, "\x00") {
				t.Errorf("normalizeArgs(%v) = %v, want %v", test.in, got, test.want)
			}
		})
	}
}

func TestApplyPositionals(t *testing.T) {
	t.Run("pair alias", func(t *testing.T) {
		oc, cmd := "", reportcompare.DefaultCMDPath
		if err := applyPositionals([]string{"oc-cmd"}, &oc, &cmd, map[string]bool{}); err != nil {
			t.Fatal(err)
		}
		if oc != "" || cmd != reportcompare.DefaultCMDPath {
			t.Errorf("alias changed inputs: oc=%q cmd=%q", oc, cmd)
		}
	})
	t.Run("one positional is the OC source", func(t *testing.T) {
		oc, cmd := "", reportcompare.DefaultCMDPath
		if err := applyPositionals([]string{"data/oc"}, &oc, &cmd, map[string]bool{}); err != nil {
			t.Fatal(err)
		}
		if oc != "data/oc" || cmd != reportcompare.DefaultCMDPath {
			t.Errorf("oc=%q cmd=%q", oc, cmd)
		}
	})
	t.Run("two positionals", func(t *testing.T) {
		oc, cmd := "", reportcompare.DefaultCMDPath
		if err := applyPositionals([]string{"data/oc", "data/cmd"}, &oc, &cmd, map[string]bool{}); err != nil {
			t.Fatal(err)
		}
		if oc != "data/oc" || cmd != "data/cmd" {
			t.Errorf("oc=%q cmd=%q", oc, cmd)
		}
	})
	t.Run("too many", func(t *testing.T) {
		oc, cmd := "", ""
		if err := applyPositionals([]string{"a", "b", "c"}, &oc, &cmd, map[string]bool{}); err == nil {
			t.Fatal("three positionals should fail")
		}
	})
	t.Run("conflict", func(t *testing.T) {
		oc, cmd := "", reportcompare.DefaultCMDPath
		if err := applyPositionals([]string{"data/oc"}, &oc, &cmd, map[string]bool{"oc": true}); err == nil {
			t.Fatal("positional plus --oc should fail")
		}
	})
	t.Run("alias wins only when no path exists", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "oc-cmd"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		oc, cmd := "", reportcompare.DefaultCMDPath
		if err := applyPositionals([]string{"oc-cmd"}, &oc, &cmd, map[string]bool{}); err != nil {
			t.Fatal(err)
		}
		if oc != "oc-cmd" {
			t.Errorf("oc = %q, want the real directory path", oc)
		}
	})
}

func TestRunComparePositionalInputs(t *testing.T) {
	root := t.TempDir()
	ocDir, cmdDir := buildReportPair(t, root)
	outDir := filepath.Join(root, "out")

	var stdout, stderr strings.Builder
	code := run([]string{ocDir, cmdDir, "--out", outDir}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	for _, name := range []string{"comparison-data.json", "comparison-data.js", "index.html"} {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Errorf("missing artifact %s: %v", name, err)
		}
	}
}

func TestRunCompareJSON(t *testing.T) {
	root := t.TempDir()
	ocDir, cmdDir := buildReportPair(t, root)
	outDir := filepath.Join(root, "out")

	var stdout, stderr strings.Builder
	code := run([]string{"--oc", ocDir, "--cmd", cmdDir, "--out", outDir, "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d\nstderr:\n%s", code, stderr.String())
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(stdout.String()), &doc); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if doc["kind"] != "comparison" {
		t.Errorf("kind = %v", doc["kind"])
	}
	if strings.Contains(stdout.String(), "AgentMeter") {
		t.Error("--json should not print the human summary")
	}
	if _, err := os.Stat(filepath.Join(outDir, "comparison-data.json")); err != nil {
		t.Errorf("--json should still write artifacts: %v", err)
	}
}

func TestRunCompareAllModelsAndNoColor(t *testing.T) {
	root := t.TempDir()
	ocDir, cmdDir := buildReportPair(t, root)

	var stdout, stderr strings.Builder
	code := run([]string{"--oc", ocDir, "--cmd", cmdDir, "--out", filepath.Join(root, "all"), "--all-models"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("all-models exit %d\nstderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "all models") || !strings.Contains(stdout.String(), "confounded") {
		t.Errorf("all-models summary should label the confound:\n%s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"--oc", ocDir, "--cmd", cmdDir, "--out", filepath.Join(root, "color"), "--no-color"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("no-color exit %d\nstderr:\n%s", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "\x1b[") {
		t.Error("--no-color output contains ANSI escapes")
	}
}

func TestRunHelpAndErrors(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := run([]string{"--help"}, &stdout, &stderr); code != 0 {
		t.Errorf("--help exit = %d, want 0", code)
	}
	if !strings.Contains(stderr.String(), "agentmeter compare") {
		t.Errorf("--help output = %q", stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"--bogus"}, &stdout, &stderr); code != 2 {
		t.Errorf("bad flag exit = %d, want 2", code)
	}
	stderr.Reset()
	if code := run([]string{"a", "b", "c"}, &stdout, &stderr); code != 2 {
		t.Errorf("too many positionals exit = %d, want 2", code)
	}
	stderr.Reset()
	if code := run([]string{"extra"}, &stdout, &stderr); code != 1 {
		t.Errorf("missing positional input exit = %d, want 1", code)
	}
	stderr.Reset()
	if code := run([]string{"--oc", filepath.Join(t.TempDir(), "missing.db")}, &stdout, &stderr); code != 1 {
		t.Errorf("missing input exit = %d, want 1", code)
	}
}
