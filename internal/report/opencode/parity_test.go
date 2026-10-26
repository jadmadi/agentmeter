// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jadmadi/agentmeter/internal/pyjson"
	"github.com/jadmadi/agentmeter/internal/stats"
	"github.com/jadmadi/agentmeter/internal/testutil"
)

// goldenDir is the committed reference directory.
func goldenPath(name string) string {
	return filepath.Join("..", "..", "..", "opencode-telemetry", "testdata", "golden", name)
}

// readJSONFile parses a JSON document into a generic map.
func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return data
}

// jsonRoundTrip converts a Go report (with int64/float64 values) into the shape
// a JSON parse produces, so it can be compared with the Python golden.
func jsonRoundTrip(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	return out
}

// normalizeGolden removes the fields a golden cannot pin: the wall clock and
// the absolute database path.
func normalizeGolden(data map[string]any) map[string]any {
	normalized := testutil.NormalizeReport(data)
	if meta, ok := normalized["meta"].(map[string]any); ok {
		delete(meta, "dbLabel")
	}
	return normalized
}

// diffValues reports the first differing path with both values.
func diffValues(path string, want, got any) string {
	if reflect.DeepEqual(want, got) {
		return ""
	}
	switch wantValue := want.(type) {
	case map[string]any:
		gotValue, ok := got.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: want object, got %T", path, got)
		}
		keys := make([]string, 0, len(wantValue)+len(gotValue))
		seen := map[string]bool{}
		for key := range wantValue {
			seen[key] = true
			keys = append(keys, key)
		}
		for key := range gotValue {
			if !seen[key] {
				keys = append(keys, key)
			}
		}
		for _, key := range keys {
			wantItem, wantOK := wantValue[key]
			gotItem, gotOK := gotValue[key]
			if !wantOK {
				return fmt.Sprintf("%s.%s: unexpected key in Go report", path, key)
			}
			if !gotOK {
				return fmt.Sprintf("%s.%s: missing key in Go report", path, key)
			}
			if diff := diffValues(path+"."+key, wantItem, gotItem); diff != "" {
				return diff
			}
		}
		return ""
	case []any:
		gotValue, ok := got.([]any)
		if !ok {
			return fmt.Sprintf("%s: want array, got %T", path, got)
		}
		if len(wantValue) != len(gotValue) {
			return fmt.Sprintf("%s: want %d items, got %d", path, len(wantValue), len(gotValue))
		}
		for i := range wantValue {
			if diff := diffValues(fmt.Sprintf("%s[%d]", path, i), wantValue[i], gotValue[i]); diff != "" {
				return diff
			}
		}
		return ""
	default:
		return fmt.Sprintf("%s: want %#v, got %#v", path, want, got)
	}
}

// compareWithGolden asserts that a Go report equals a Python golden after the
// volatile fields are removed.
func compareWithGolden(t *testing.T, built map[string]any, goldenName string) {
	t.Helper()
	want := normalizeGolden(readJSONFile(t, goldenPath(goldenName)))
	got := normalizeGolden(jsonRoundTrip(t, built))
	if target := os.Getenv("WRITE_GO_REPORT"); target != "" {
		raw, err := json.MarshalIndent(got, "", " ")
		if err != nil {
			t.Fatalf("marshal Go report: %v", err)
		}
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			t.Fatalf("write Go report: %v", err)
		}
	}
	if diff := diffValues("report", want, got); diff != "" {
		t.Fatalf("parity mismatch vs %s:\n%s", goldenName, diff)
	}
}

// writeGolden regenerates a golden from the reference Python builder.
func writeGolden(t *testing.T, dbPath, goldenName string, extraArgs ...string) {
	t.Helper()
	outDir := t.TempDir()
	report := runPythonBuilder(t, dbPath, outDir, extraArgs...)
	raw, err := json.MarshalIndent(report, "", " ")
	if err != nil {
		t.Fatalf("marshal golden: %v", err)
	}
	path := goldenPath(goldenName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create golden dir: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
	}
	t.Logf("wrote %s", path)
}

// fixtureDB builds the shared fixture store in a temp dir.
func fixtureDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	buildFixtureDB(t, path)
	if target := os.Getenv("WRITE_FIXTURE_DB"); target != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read fixture db: %v", err)
		}
		if err := os.WriteFile(target, raw, 0o644); err != nil {
			t.Fatalf("write fixture db: %v", err)
		}
		t.Logf("wrote fixture db %s", target)
	}
	return path
}

// TestGoldenDefault compares the default build (titles, basename labels,
// default thresholds) against the Python builder.
func TestGoldenDefault(t *testing.T) {
	dbPath := fixtureDB(t)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		writeGolden(t, dbPath, "opencode-default.json")
	}
	built, err := Build(Options{DBPath: dbPath, IncludeTitles: true, Now: fixtureNow})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	compareWithGolden(t, built, "opencode-default.json")
}

// TestGoldenOptions compares a --no-titles --full-paths build with custom
// timing thresholds against the Python builder.
func TestGoldenOptions(t *testing.T) {
	dbPath := fixtureDB(t)
	thresholds := Thresholds{MinSeconds: 0.4, MaxSeconds: 240, MinTokens: 25, MinRate: 2.5, MaxRate: 350}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		writeGolden(t, dbPath, "opencode-options.json",
			"--no-titles", "--full-paths",
			"--min-seconds", "0.4", "--max-seconds", "240",
			"--min-tokens", "25", "--min-rate", "2.5", "--max-rate", "350")
	}
	built, err := Build(Options{
		DBPath:        dbPath,
		IncludeTitles: false,
		FullPaths:     true,
		Thresholds:    &thresholds,
		Now:           fixtureNow,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	compareWithGolden(t, built, "opencode-options.json")
}

// TestBaselineWindowSetsPaceBase verifies --baseline pins the pace base to the
// reference window and matches the Python reference builder.
func TestBaselineWindowSetsPaceBase(t *testing.T) {
	dbPath := fixtureDB(t)
	from, to := "2026-01-05", "2026-01-05"
	built, err := Build(Options{
		DBPath:        dbPath,
		IncludeTitles: true,
		Baseline:      from + ".." + to,
		Now:           fixtureNow,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	meta := built["meta"].(map[string]any)
	baseline, ok := meta["paceBaseline"].(map[string]any)
	if !ok {
		t.Fatalf("paceBaseline missing: %v", meta["paceBaseline"])
	}
	if baseline["from"] != from || baseline["to"] != to {
		t.Fatalf("baseline window: %v", baseline)
	}

	// Recompute the audit's expectation from the emitted step tuples: the
	// window's ratio sample and its median.
	stepRows := built["steps"].([]any)
	ratios := make([]float64, 0, len(stepRows))
	for _, item := range stepRows {
		tuple := item.([]any)
		epoch := tuple[0].(int64)
		ratio := tuple[30]
		ratioValue, isFloat := ratio.(float64)
		if !isFloat || ratioValue == -1 {
			continue
		}
		day := time.Unix(epoch, 0).Local().Format("2006-01-02")
		if from <= day && day <= to {
			ratios = append(ratios, ratioValue)
		}
	}
	if len(ratios) == 0 {
		t.Fatal("fixture must have ratio-bearing steps in the baseline window")
	}
	if got := baseline["n"]; got != len(ratios) {
		t.Fatalf("paceBaseline.n = %v, want %d (window sample size)", got, len(ratios))
	}
	// Step tuples round each ratio to 4 decimals, so compare the window median
	// within that rounding rather than bit-for-bit.
	wantBase := medianValue(t, ratios)
	gotBase, ok := meta["paceBase"].(float64)
	if !ok || math.Abs(gotBase-wantBase) > 1e-3 {
		t.Fatalf("meta.paceBase = %v, want the window median %v", meta["paceBase"], wantBase)
	}
}

// medianValue is statistics.median for the test's ratio sample.
func medianValue(t *testing.T, values []float64) float64 {
	t.Helper()
	value, ok := stats.Median(values)
	if !ok {
		t.Fatal("median of an empty sample")
	}
	return value
}

// TestBaselineRejectsMalformedWindow checks the CLI-shaped validation.
func TestBaselineRejectsMalformedWindow(t *testing.T) {
	dbPath := fixtureDB(t)
	if _, err := Build(Options{DBPath: dbPath, Baseline: "2026-01-05", Now: fixtureNow}); err == nil {
		t.Fatal("expected a malformed --baseline to fail")
	}
}

// TestPythonAuditAcceptsGoReport is the end-to-end oracle check: the Go report,
// written with the Python-compatible encoder, must pass the independent Python
// audit against the same store with zero exceptions across full-paths, default
// disambiguated labels, and model-filtered builds.
func TestPythonAuditAcceptsGoReport(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if err := exec.Command("python3", "-c", "import numpy").Run(); err != nil {
		t.Skip("the OpenCode audit requires numpy")
	}

	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repo root: %v", err)
	}
	auditScript := filepath.Join(repoRoot, "opencode-telemetry", "py", "audit.py")

	runAudit := func(t *testing.T, opts Options) (string, error) {
		t.Helper()
		dir := t.TempDir()
		dbPath := filepath.Join(dir, "opencode.db")
		buildFixtureDB(t, dbPath)
		opts.DBPath = dbPath
		opts.Now = fixtureNow
		built, err := Build(opts)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		jsonBytes, err := pyjson.JSONBytes(built)
		if err != nil {
			t.Fatalf("encode json: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "telemetry-data.json"), jsonBytes, 0o644); err != nil {
			t.Fatalf("write json: %v", err)
		}
		jsBytes, err := pyjson.JSBytes("OPENCODE_TELEMETRY", built)
		if err != nil {
			t.Fatalf("encode js: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "telemetry-data.js"), jsBytes, 0o644); err != nil {
			t.Fatalf("write js: %v", err)
		}
		audit := exec.Command("python3", auditScript,
			"--db", dbPath, "--data", filepath.Join(dir, "telemetry-data.json"))
		audit.Env = append(os.Environ(), "TZ=UTC")
		output, err := audit.CombinedOutput()
		return string(output), err
	}

	t.Run("full-paths", func(t *testing.T) {
		output, err := runAudit(t, Options{IncludeTitles: true, FullPaths: true})
		if err != nil {
			t.Fatalf("python audit rejected the Go report: %v\n%s", err, output)
		}
		if !strings.Contains(output, "ALL CHECKS PASSED") {
			t.Fatalf("python audit did not report success:\n%s", output)
		}
		if !strings.Contains(output, "0 failed") {
			t.Fatalf("expected no failed checks:\n%s", output)
		}
	})

	t.Run("default-labels", func(t *testing.T) {
		output, err := runAudit(t, Options{IncludeTitles: true, FullPaths: false})
		if err != nil {
			t.Fatalf("python audit rejected the Go report: %v\n%s", err, output)
		}
		if !strings.Contains(output, "ALL CHECKS PASSED") {
			t.Fatalf("python audit did not report success:\n%s", output)
		}
		if !strings.Contains(output, "0 failed") {
			t.Fatalf("expected no failed checks:\n%s", output)
		}
	})

	t.Run("model-filter", func(t *testing.T) {
		output, err := runAudit(t, Options{IncludeTitles: true, ModelFilters: []string{"model-a"}})
		if err != nil {
			t.Fatalf("python audit rejected the Go report: %v\n%s", err, output)
		}
		if !strings.Contains(output, "ALL CHECKS PASSED") {
			t.Fatalf("python audit did not report success:\n%s", output)
		}
		if !strings.Contains(output, "0 failed") {
			t.Fatalf("expected no failed checks:\n%s", output)
		}
	})

	t.Run("baseline", func(t *testing.T) {
		output, err := runAudit(t, Options{IncludeTitles: true, Baseline: "2026-01-05..2026-01-05"})
		if err != nil {
			t.Fatalf("python audit rejected the Go report: %v\n%s", err, output)
		}
		if !strings.Contains(output, "ALL CHECKS PASSED") {
			t.Fatalf("python audit did not report success:\n%s", output)
		}
		if !strings.Contains(output, "0 failed") {
			t.Fatalf("expected no failed checks:\n%s", output)
		}
	})
}

// TestOpenCode2CompactionAndLedgerLabelDisambiguation verifies that OpenCode 2
// compaction token objects are collapsed to integer input context and that
// duplicate project basenames across rows and ledger-only sessions disambiguate
// identically in Go, Python, and audit.py.
func TestOpenCode2CompactionAndLedgerLabelDisambiguation(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "opencode.db")
	buildFixtureDB(t, dbPath)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Add a ledger-only session whose directory shares basename "project-b"
	// with ses-root-b (/work/other/project-b).
	if _, err := db.Exec(
		"INSERT INTO session_v2 (id, title, slug, directory, parent_id, fork_session_id, version, time_created) VALUES (?,?,?,?,?,?,?,?)",
		"ses-ledger-dup", "Ledger dup", nil, "/home/user/.local/share/project-b", nil, nil, "2.1.4", fixtureTime(0, 9, 0, 0),
	); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	ledgerPayload, err := json.Marshal(fixturePayload{
		Provider: "alpha", Model: "model-a", Input: 120, Finish: "error",
		Created: fixtureTime(2, 17, 0, 0), Streamed: fixtureTime(2, 17, 0, 0) + 500, Completed: fixtureTime(2, 17, 0, 0) + 800,
		Cost: 0.0015,
	}.mapValue())
	if err != nil {
		t.Fatalf("marshal ledger payload: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO session_message (id, session_id, type, time_created, data) VALUES (?,?,?,?,?)",
		"err-dup-base", "ses-ledger-dup", "assistant", fixtureTime(2, 17, 0, 0), string(ledgerPayload),
	); err != nil {
		t.Fatalf("insert ledger msg: %v", err)
	}

	// Add an OpenCode 2 compaction event with nested tokens object.
	compactionPayload, err := json.Marshal(map[string]any{
		"tokens": map[string]any{
			"input":     652,
			"output":    1551,
			"reasoning": 2076,
			"cache":     map[string]any{"read": 916608, "write": 40},
		},
	})
	if err != nil {
		t.Fatalf("marshal compaction payload: %v", err)
	}
	if _, err := db.Exec(
		"INSERT INTO session_message (id, session_id, type, time_created, data) VALUES (?,?,?,?,?)",
		"compaction-oc2", "ses-root-a", "compaction", fixtureTime(2, 9, 15, 0), string(compactionPayload),
	); err != nil {
		t.Fatalf("insert compaction msg: %v", err)
	}
	db.Close()

	built, err := Build(Options{DBPath: dbPath, IncludeTitles: true, Now: fixtureNow})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	events := built["compactionEvents"].([]any)
	foundOC2 := false
	for _, item := range events {
		ev := item.(map[string]any)
		if ev["tokensBefore"] == int64(917300) {
			foundOC2 = true
		}
	}
	if !foundOC2 {
		t.Fatalf("expected OpenCode 2 compaction tokensBefore=917300 in %+v", events)
	}

	if _, err := exec.LookPath("python3"); err == nil {
		pyOut := runPythonBuilder(t, dbPath, filepath.Join(dir, "py"))
		if diff := diffValues("report", normalizeGolden(pyOut), normalizeGolden(jsonRoundTrip(t, built))); diff != "" {
			t.Fatalf("Go vs Python mismatch:\n%s", diff)
		}
	}
}

// TestEmptyDatabaseReport checks the empty-store report is complete and valid.
func TestEmptyDatabaseReport(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "empty.db")
	db, err := openAndSchema(dbPath)
	if err != nil {
		t.Fatalf("create empty store: %v", err)
	}
	db.Close()

	built, err := Build(Options{DBPath: dbPath, IncludeTitles: true, Now: fixtureNow})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	for _, key := range []string{
		"schemaVersion", "meta", "totals", "timed", "byModel", "cache", "cost",
		"reasoning", "runtime", "sessionHealth", "coverage", "tools", "thinking",
		"agents", "variants", "entrypoints", "projects", "projectsTotal",
		"errorLedger", "lookups", "fits", "fitsGlobal", "prices", "steps",
		"sessions", "regression", "histogram", "sizeBuckets", "daily",
		"dailyByModel", "hourly", "context", "contextText", "peaks", "slowest",
		"lowestRate", "rejectedExtremes", "anomalies", "fork", "compactionImpact",
		"compactionEvents", "modelChangeEvents",
	} {
		if _, ok := built[key]; !ok {
			t.Fatalf("empty report misses %q", key)
		}
	}
	totals := built["totals"].(map[string]any)
	if numeric(totals["steps"]) != 0 || numeric(totals["generatedTokens"]) != 0 {
		t.Fatalf("empty totals: %+v", totals)
	}
	if hourly, ok := built["hourly"].([]any); !ok || len(hourly) != 24 {
		t.Fatalf("hourly must keep 24 slots, got %v", built["hourly"])
	}
	steps, _ := built["steps"].([]any)
	if len(steps) != 0 {
		t.Fatalf("empty steps: %v", steps)
	}
}
