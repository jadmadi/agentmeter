// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/jadmadi/agentmeter/internal/contract"
	sourcecmd "github.com/jadmadi/agentmeter/internal/source/cmd"
	"github.com/jadmadi/agentmeter/internal/testutil"
)

func TestMain(m *testing.M) {
	// Golden data is generated with TZ=UTC; pin the process zone so local
	// dates, hourly buckets and meta.timezone are deterministic.
	time.Local = time.UTC
	os.Exit(m.Run())
}

func repoPath(t *testing.T, parts ...string) string {
	t.Helper()
	base, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatalf("resolve repo: %v", err)
	}
	return filepath.Join(append([]string{base}, parts...)...)
}

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root := repoPath(t, "command-code-telemetry", "testdata", "fixture")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("fixture missing at %s: %v", root, err)
	}
	return root
}

// buildFixture builds the report from the committed fixture tree.
func buildFixture(t *testing.T, options Options) map[string]any {
	t.Helper()
	root := fixtureRoot(t)
	source := sourcecmd.Load(sourcecmd.Filters{
		Root:          root,
		ModelFilters:  options.ModelFilters,
		Since:         options.Since,
		Until:         options.Until,
		IncludeTitles: options.IncludeTitles,
	})
	data, err := Build(source, options)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return data
}

// roundTrip normalizes a built report to the shape the JSON writer produces.
// This is the correct comparison basis: the golden file is parsed JSON.
func roundTrip(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal report: %v", err)
	}
	return parsed
}

// TestGoldenReport compares the Go builder against the Python builder's
// committed output on the fixture tree. Only meta.generatedAt is normalized.
func TestGoldenReport(t *testing.T) {
	expectedPath := repoPath(t, "command-code-telemetry", "testdata", "expected", "telemetry-data.json")
	expected, err := testutil.ReadReport(expectedPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	actual := testutil.NormalizeReport(roundTrip(t, buildFixture(t, Options{
		Root:          fixtureRoot(t),
		IncludeTitles: true,
	})))
	// The fixture path depends on the checkout location; the golden stores the
	// literal "<fixture>" for meta.rootLabel (see testdata/generate.py).
	actual["meta"].(map[string]any)["rootLabel"] = "<fixture>"
	var diffs []string
	compareValues("report", expected, actual, &diffs)
	if len(diffs) > 0 {
		limit := len(diffs)
		if limit > 25 {
			limit = 25
		}
		for _, diff := range diffs[:limit] {
			t.Error(diff)
		}
		t.Fatalf("%d differences against the Python golden report", len(diffs))
	}
}

// compareValues collects the first differences between parsed JSON values.
func compareValues(path string, expected, actual any, diffs *[]string) {
	if len(*diffs) > 200 {
		return
	}
	switch expectedValue := expected.(type) {
	case map[string]any:
		actualValue, ok := actual.(map[string]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: type %T != %T", path, expected, actual))
			return
		}
		for key, value := range expectedValue {
			other, exists := actualValue[key]
			if !exists {
				*diffs = append(*diffs, fmt.Sprintf("%s.%s: missing from Go report", path, key))
				continue
			}
			compareValues(path+"."+key, value, other, diffs)
		}
		for key := range actualValue {
			if _, exists := expectedValue[key]; !exists {
				*diffs = append(*diffs, fmt.Sprintf("%s.%s: extra key in Go report", path, key))
			}
		}
	case []any:
		actualValue, ok := actual.([]any)
		if !ok {
			*diffs = append(*diffs, fmt.Sprintf("%s: type %T != %T", path, expected, actual))
			return
		}
		if len(expectedValue) != len(actualValue) {
			*diffs = append(*diffs, fmt.Sprintf("%s: length %d != %d", path, len(expectedValue), len(actualValue)))
		}
		for index := 0; index < len(expectedValue) && index < len(actualValue); index++ {
			compareValues(fmt.Sprintf("%s[%d]", path, index), expectedValue[index], actualValue[index], diffs)
		}
	default:
		if !reflect.DeepEqual(expected, actual) {
			*diffs = append(*diffs, fmt.Sprintf("%s: python=%v go=%v", path, expected, actual))
		}
	}
}

var topLevelKeys = []string{
	"schemaVersion", "meta", "totals", "timed", "byModel", "cache", "cost",
	"sessionHealth", "coverage", "tools", "thinking", "efforts", "entrypoints",
	"projects", "projectsTotal", "lookups", "fits", "prices", "steps", "sessions",
	"regression", "histogram", "sizeBuckets", "daily", "dailyByModel", "hourly",
	"context", "contextText", "peaks", "slowest", "lowestRate", "rejectedExtremes",
	"anomalies", "skipLedger", "fork", "compactionImpact", "compactionEvents",
	"modelChangeEvents",
}

func TestReportKeyCoverage(t *testing.T) {
	report := roundTrip(t, buildFixture(t, Options{Root: fixtureRoot(t), IncludeTitles: true}))
	for _, key := range topLevelKeys {
		if _, ok := report[key]; !ok {
			t.Errorf("missing top-level key %q", key)
		}
	}
	if len(report) != len(topLevelKeys) {
		t.Errorf("top-level keys = %d, want %d", len(report), len(topLevelKeys))
	}

	cases := map[string][]string{
		"meta": {"generatedAt", "rootLabel", "modelFilters", "models", "filesScanned",
			"sessions", "duplicatesSkipped", "skippedSteps", "emptySteps",
			"includeTitles", "fullPaths", "since", "until", "timezone", "paceBase",
			"paceBasePct", "timing", "counts", "sourceWatermark", "dateFrom", "dateTo"},
		"meta.timing": {"files", "scanned", "considered", "rows", "plausible",
			"noTiming", "tooShort", "tooShortDuration", "tooLong", "slowRate",
			"fastRate", "duplicates", "noOutput", "noTimestamp", "filteredModel",
			"filteredDate", "thresholds"},
		"meta.timing.thresholds": {"minSeconds", "maxSeconds", "minTokens", "minRate", "maxRate"},
		"meta.counts": {"files", "scanned", "assistant", "considered", "rows",
			"duplicates", "noOutput", "noTimestamp", "filteredModel", "filteredDate"},
		"meta.sourceWatermark": {"maxTs", "files", "scanned", "rows"},
		"totals": {"steps", "toolSteps", "textSteps", "toolCalls", "outputTokens",
			"generatedTokens", "visibleOutputTokens", "inputTokens", "inputRawTokens",
			"freshInputTokens", "inputTotal", "cacheReadTokens", "cacheWriteTokens",
			"costUsd", "errorCostUsd", "totalSpendUsd", "models"},
		"timed.all": {"n", "tokens", "seconds", "weighted", "median", "p10", "p25",
			"p75", "p90", "p99", "max", "maxIncluded", "maxObserved", "maxRejected",
			"maxRejectedReason", "minIncluded", "maxDurationIncluded",
			"maxDurationObserved", "maxDurationAny", "upperCensored", "lowerCensored",
			"durationCensoredHigh", "durationCensoredLow"},
		"timed.text": {"n", "tokens", "seconds", "weighted", "median", "p10", "p25",
			"p75", "p90", "p99", "max"},
		"cache": {"inputTokens", "inputRawTokens", "freshInputTokens",
			"cacheReadTokens", "cacheWriteTokens", "generatedTokens", "hitRate",
			"writeShare", "savings", "writeCost", "fittedModels", "unfittedModels",
			"savingsCoverage"},
		"cost": {"recorded", "errorCostUsd", "undatedErrorCostUsd", "totalSpendUsd",
			"tokenCost", "freshCost", "cacheReadCost", "cacheWriteCost", "outputCost",
			"perStep", "perMillionGenerated", "perMillionOutput", "byModel"},
		"sessionHealth": {"hooks", "hookEvents", "toolWindows", "compactions",
			"modelChanges", "fileSnapshots", "branches", "timing", "toolCalls",
			"skippedSteps", "skippedCostUsd", "emptySteps", "noTimestampSteps",
			"duplicateSteps", "duplicateCostUsd", "duplicateTokens"},
		"sessionHealth.toolWindows":   {"count", "medianSeconds", "p90Seconds"},
		"sessionHealth.compactions":   {"count", "tokensBefore"},
		"sessionHealth.modelChanges":  {"count"},
		"sessionHealth.fileSnapshots": {"count"},
		"coverage": {"cacheRead", "cacheWrite", "thinking", "hooks", "toolWindows",
			"compactions", "modelChanges", "fileSnapshots", "branches", "titles",
			"entrypoints", "timing", "timingBreakdown", "toolCalls", "skipLedger",
			"projectPaths", "inputSemantics"},
		"thinking.with": {"steps", "share", "outputTokens", "medianOut", "timedN",
			"weighted", "medianTps"},
		"lookups": {"models", "tools", "efforts", "projects", "projectLabels",
			"entrypoints", "timingReasons"},
		"lookups.models[0]": {"id", "short", "provider"},
		"histogram":         {"labels", "all", "text"},
		"fork":              {"duplicateSteps", "duplicateTokens", "duplicateCostUsd", "sessionsWithDuplicates"},
		"compactionImpact": {"events", "paired", "medianContextBefore",
			"medianContextAfter", "medianContextDelta", "medianRateBefore",
			"medianRateAfter", "medianRateDeltaPct", "medianCostBefore", "medianCostAfter"},
	}
	for path, keys := range cases {
		value, ok := lookupPath(report, path)
		if !ok {
			t.Errorf("missing %s", path)
			continue
		}
		switch typed := value.(type) {
		case map[string]any:
			for _, key := range keys {
				if _, exists := typed[key]; !exists {
					t.Errorf("%s: missing key %q", path, key)
				}
			}
			if len(typed) != len(keys) {
				t.Errorf("%s: %d keys, want %d", path, len(typed), len(keys))
			}
		case []any:
			if len(typed) == 0 {
				t.Errorf("%s: empty list", path)
				continue
			}
			first, ok := typed[0].(map[string]any)
			if !ok {
				t.Errorf("%s[0]: not an object", path)
				continue
			}
			for _, key := range keys {
				if _, exists := first[key]; !exists {
					t.Errorf("%s[0]: missing key %q", path, key)
				}
			}
			if len(first) != len(keys) {
				t.Errorf("%s[0]: %d keys, want %d", path, len(first), len(keys))
			}
		default:
			t.Errorf("%s: unexpected type %T", path, value)
		}
	}

	// List-element shapes that have more than one element.
	listCases := map[string][]string{
		"byModel": {"model", "short", "provider", "steps", "toolSteps", "textSteps",
			"outputTokens", "generatedTokens", "visibleOutputTokens", "inputTokens",
			"inputRawTokens", "freshInputTokens", "inputTotal", "cacheReadTokens",
			"cacheWriteTokens", "cacheHitRate", "inputIncludesCache",
			"inputSemanticsSource", "costUsd", "timedN", "rated", "lowSample",
			"medianCI", "weighted", "median", "p90", "pureRate", "fixedSeconds",
			"r2", "fitUsable", "timing", "rejected", "standardizedRate",
			"standardizedCoverage", "standardizedN"},
		"cost.byModel": {"model", "short", "steps", "outputTokens", "generatedTokens",
			"visibleOutputTokens", "inputTokens", "inputRawTokens", "freshInputTokens",
			"inputTotal", "cacheReadTokens", "cacheWriteTokens", "cacheHitRate",
			"cacheWriteShare", "inputIncludesCache", "inputSemanticsSource",
			"recordedCost", "tokenCost", "freshCost", "cacheReadCost", "cacheWriteCost",
			"outputCost", "cacheSavings", "effectiveInputPrice", "freshInputRate",
			"cacheRate", "cacheWriteRate", "outputRate", "priceR2", "costPerStep",
			"costPerMillionGenerated", "costPerMillionOutput"},
		"tools": {"tool", "calls", "stepsWithTool", "shareCalls", "generatedTokens",
			"visibleOutputTokens", "medianTokensPerCall", "timedCalls", "weighted",
			"medianTps", "errors", "errRate", "costUsd", "costPerCall", "timeShare",
			"attribution", "runtime"},
		"efforts": {"steps", "share", "outputTokens", "medianOut", "timedN",
			"weighted", "medianTps", "effort"},
		"entrypoints": {"steps", "share", "outputTokens", "medianOut", "timedN",
			"weighted", "medianTps", "entrypoint", "sessions"},
		"projects": {"name", "path", "steps", "sessions", "generatedTokens",
			"outputTokens", "inputTotal", "cacheReadTokens", "cacheHitRate", "costUsd"},
		"sessions": {"id", "sessionId", "title", "project", "projectPath",
			"entrypoint", "branch", "models", "steps", "toolSteps", "textSteps",
			"outputTokens", "generatedTokens", "visibleOutputTokens", "inputTokens",
			"inputRawTokens", "freshInputTokens", "inputTotal", "cacheReadTokens",
			"cacheWriteTokens", "cacheHitRate", "costUsd", "skippedSteps",
			"skippedCostUsd", "totalSpendUsd", "duplicateSteps", "timedN",
			"weighted", "firstTs", "lastTs", "spanMinutes", "activeMinutes",
			"idleMinutes", "outputPerMinute", "wallOutputPerMinute", "outcome"},
		"regression":  {"model", "short", "all", "text", "tool"},
		"fits":        {"all", "text", "tool"},
		"prices":      {"fresh", "cache", "cacheWrite", "output", "inputIncludesCache", "inputSemanticsSource"},
		"sizeBuckets": {"label", "n", "tokens", "weighted", "median", "p90"},
		"daily": {"date", "nAll", "nTimed", "n", "tokens", "generatedTokens",
			"timedTokens", "timedSeconds", "inputTotal", "cacheReadTokens",
			"cacheHitRate", "costUsd", "errorCostUsd", "totalSpendUsd", "errorSteps",
			"errorRate", "sessions", "weighted", "median", "p90", "ratio", "ratioPct",
			"ratioN", "index", "textN", "textMedian", "textIndex", "weighted7d",
			"cost7d", "spend7dTotal", "index7d"},
		"hourly": {"hour", "nAll", "nTimed", "n", "tokens", "generatedTokens",
			"inputTotal", "cacheReadTokens", "cacheHitRate", "costUsd", "errorCostUsd",
			"totalSpendUsd", "errorSteps", "sessions", "weighted", "median", "p90",
			"ratio", "ratioPct", "ratioN", "index", "textN", "textMedian",
			"textRatio", "textIndex"},
		"context": {"label", "n", "median", "weighted", "ratio", "ratioPct", "ratioN", "index"},
		"peaks":   {"tps", "out", "dt", "model", "at", "tool", "inputTotal", "reason", "sessionId", "project"},
		"skipLedger": {"id", "reason", "sessionId", "projectPath", "model", "ts",
			"outputTokens", "inputTokens", "cacheReadTokens", "cacheWriteTokens", "costUsd"},
		"anomalies":         {"date", "metric", "value", "z", "direction"},
		"compactionEvents":  {"ts", "sessionId", "tokensBefore"},
		"modelChangeEvents": {"ts", "sessionId", "model"},
	}
	for path, keys := range listCases {
		value, ok := lookupPath(report, path)
		if !ok {
			t.Errorf("missing %s", path)
			continue
		}
		items, ok := value.([]any)
		if !ok || len(items) == 0 {
			// Empty lists are legitimate for some sections; only verify when
			// the fixture produced at least one element.
			continue
		}
		first, ok := items[0].(map[string]any)
		if !ok {
			t.Errorf("%s[0]: not an object", path)
			continue
		}
		for _, key := range keys {
			if _, exists := first[key]; !exists {
				t.Errorf("%s[0]: missing key %q", path, key)
			}
		}
		if len(first) != len(keys) {
			got := make([]string, 0, len(first))
			for key := range first {
				got = append(got, key)
			}
			sort.Strings(got)
			t.Errorf("%s[0]: %d keys, want %d (got %v)", path, len(first), len(keys), got)
		}
	}

	// dailyByModel wraps a per-model series.
	dailyByModel := report["dailyByModel"].([]any)
	for _, entry := range dailyByModel {
		item := entry.(map[string]any)
		if _, ok := item["model"]; !ok {
			t.Errorf("dailyByModel entry missing model")
		}
		series, ok := item["series"].([]any)
		if !ok || len(series) == 0 {
			continue
		}
		first := series[0].(map[string]any)
		for _, key := range []string{"date", "nAll", "nTimed", "generatedTokens",
			"costUsd", "errorCostUsd", "cacheHitRate", "weighted", "median", "ratio",
			"ratioPct", "ratioN", "index"} {
			if _, exists := first[key]; !exists {
				t.Errorf("dailyByModel series missing %q", key)
			}
		}
		if len(first) != 13 {
			t.Errorf("dailyByModel series has %d keys, want 13", len(first))
		}
	}
}

func lookupPath(data map[string]any, path string) (any, bool) {
	var current any = data
	start := 0
	for index := 0; index <= len(path); index++ {
		if index != len(path) && path[index] != '.' {
			continue
		}
		segment := path[start:index]
		start = index + 1
		if segment == "" {
			continue
		}
		if bracket := lastIndex(segment, '['); bracket >= 0 {
			segment = segment[:bracket]
		}
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[segment]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func lastIndex(text string, target byte) int {
	for index := len(text) - 1; index >= 0; index-- {
		if text[index] == target {
			return index
		}
	}
	return -1
}

// TestStepTupleContract asserts the positional step layout the renderer
// depends on: exactly 20 slots, in-range indexes and -1 sentinels.
func TestStepTupleContract(t *testing.T) {
	report := roundTrip(t, buildFixture(t, Options{Root: fixtureRoot(t), IncludeTitles: true}))
	steps := report["steps"].([]any)
	if len(steps) == 0 {
		t.Fatal("fixture produced no steps")
	}
	lookups := report["lookups"].(map[string]any)
	models := lookups["models"].([]any)
	tools := lookups["tools"].([]any)
	efforts := lookups["efforts"].([]any)
	projects := lookups["projects"].([]any)
	entrypoints := lookups["entrypoints"].([]any)
	timingReasons := lookups["timingReasons"].([]any)
	sessions := report["sessions"].([]any)
	if got, want := len(timingReasons), len(sourcecmd.TimingReasons); got != want {
		t.Errorf("timingReasons = %d, want %d", got, want)
	}
	if timingReasons[0] != "plausible" {
		t.Errorf("timingReasons[0] = %v, want plausible", timingReasons[0])
	}
	foundFinalAnswer := false
	for _, tool := range tools {
		if tool == "(final answer)" {
			foundFinalAnswer = true
		}
	}
	if !foundFinalAnswer {
		t.Error("lookups.tools is missing \"(final answer)\"")
	}

	previousTS := -1.0
	for index, entry := range steps {
		tuple, ok := entry.([]any)
		if !ok {
			t.Fatalf("step %d is not an array", index)
		}
		if len(tuple) != 20 {
			t.Fatalf("step %d has %d slots, want 20", index, len(tuple))
		}
		slot := func(position int) float64 {
			value, ok := tuple[position].(float64)
			if !ok {
				t.Fatalf("step %d slot %d is %T", index, position, tuple[position])
			}
			return value
		}
		timestamp := slot(0)
		if timestamp < previousTS {
			t.Errorf("steps are not sorted by timestamp at %d", index)
		}
		previousTS = timestamp
		if timestamp <= 0 {
			t.Errorf("step %d timestamp %v is not epoch seconds", index, timestamp)
		}
		rate := func(position int) {
			if index > 0 || true {
				if slot(position) < 0 {
					t.Errorf("step %d slot %d is negative", index, position)
				}
			}
		}
		rate(3) // output tokens
		rate(5) // input tokens
		rate(6) // cache read
		rate(7) // cache write
		if slot(4) != -1 && slot(4) <= 0 {
			t.Errorf("step %d duration %v is neither positive nor -1", index, slot(4))
		}
		if slot(17) != -1 && slot(17) < 0 {
			t.Errorf("step %d ratio %v is neither non-negative nor -1", index, slot(17))
		}
		if value := slot(10); value != 0 && value != 1 {
			t.Errorf("step %d thinking flag = %v", index, value)
		}
		indexes := []struct {
			position int
			limit    int
		}{
			{1, len(models)},
			{2, len(tools)},
			{9, len(efforts)},
			{11, len(projects)},
			{12, len(entrypoints)},
			{13, len(sessions)},
			{18, len(timingReasons)},
		}
		for _, item := range indexes {
			value := int(slot(item.position))
			if value < 0 || value >= item.limit {
				t.Errorf("step %d slot %d index %d out of range (%d)", index, item.position, value, item.limit)
			}
		}
		rate(14)
		rate(15)
		rate(16)
		toolList, ok := tuple[19].([]any)
		if !ok {
			t.Fatalf("step %d tool list is %T", index, tuple[19])
		}
		for _, toolIndex := range toolList {
			value, ok := toolIndex.(float64)
			if !ok || value < 0 || int(value) >= len(tools) {
				t.Errorf("step %d tool index %v out of range", index, toolIndex)
			}
		}
	}

	// Alignment: one fits/prices/regression/byModel/dailyByModel entry per model,
	// one session row per session index.
	for _, key := range []string{"byModel", "fits", "prices", "regression", "dailyByModel"} {
		list := report[key].([]any)
		if len(list) != len(models) {
			t.Errorf("%s has %d entries, want %d models", key, len(list), len(models))
		}
	}
	sessionIndexes := map[int]struct{}{}
	for _, entry := range steps {
		tuple := entry.([]any)
		sessionIndexes[int(tuple[13].(float64))] = struct{}{}
	}
	if len(sessionIndexes) != len(sessions) {
		t.Errorf("steps reference %d sessions, sessions has %d", len(sessionIndexes), len(sessions))
	}
}

// TestEmptySourceReport proves an empty tree produces a valid, complete
// report rather than an error or a partial schema.
func TestEmptySourceReport(t *testing.T) {
	root := t.TempDir()
	source := sourcecmd.Load(sourcecmd.Filters{Root: root, IncludeTitles: true})
	data, err := Build(source, Options{Root: root, IncludeTitles: true})
	if err != nil {
		t.Fatalf("Build(empty): %v", err)
	}
	report := roundTrip(t, data)
	for _, key := range topLevelKeys {
		if _, ok := report[key]; !ok {
			t.Errorf("empty report missing %q", key)
		}
	}
	totals := report["totals"].(map[string]any)
	if totals["steps"].(float64) != 0 {
		t.Errorf("steps = %v", totals["steps"])
	}
	if steps := report["steps"].([]any); len(steps) != 0 {
		t.Errorf("steps = %d", len(steps))
	}
	if hourly := report["hourly"].([]any); len(hourly) != 24 {
		t.Errorf("hourly = %d, want 24", len(hourly))
	}
	labels := report["histogram"].(map[string]any)["labels"].([]any)
	counts := report["histogram"].(map[string]any)["all"].([]any)
	if len(labels) != 12 || len(counts) != 12 {
		t.Errorf("histogram = %d/%d", len(labels), len(counts))
	}
	if errors := contract.ValidateCmd(report); len(errors) > 0 {
		t.Errorf("empty report failed validation: %v", errors)
	}
	// modelFilters must be a JSON array even when empty.
	if filters, ok := report["meta"].(map[string]any)["modelFilters"].([]any); !ok || len(filters) != 0 {
		t.Errorf("modelFilters = %v", report["meta"].(map[string]any)["modelFilters"])
	}
}

// TestFilteredReport validates a filtered build (the audit does not cover
// filters, so the builder must still enforce the contract).
func TestFilteredReport(t *testing.T) {
	raw := buildFixture(t, Options{
		Root:          fixtureRoot(t),
		IncludeTitles: true,
		ModelFilters:  []string{"fast"},
		Since:         "2026-09-01",
		Until:         "2026-09-04",
	})
	if errors := contract.ValidateCmd(raw); len(errors) > 0 {
		t.Errorf("filtered report failed validation: %v", errors)
	}
	report := roundTrip(t, raw)
	timing := report["meta"].(map[string]any)["timing"].(map[string]any)
	if timing["filteredModel"].(float64) == 0 {
		t.Error("filteredModel not counted")
	}
	if timing["filteredDate"].(float64) == 0 {
		t.Error("filteredDate not counted")
	}
}

// TestNoTitlesAndFullPaths checks the privacy flags.
func TestNoTitlesAndFullPaths(t *testing.T) {
	noTitles := roundTrip(t, buildFixture(t, Options{Root: fixtureRoot(t), IncludeTitles: false}))
	for _, entry := range noTitles["sessions"].([]any) {
		item := entry.(map[string]any)
		sessionID := item["sessionId"].(string)
		title := item["title"].(string)
		if len(title) > 8 {
			t.Errorf("title %q was kept with --no-titles", title)
		}
		if title != sessionID[:minInt(len(sessionID), 8)] {
			t.Errorf("title %q is not the id prefix", title)
		}
	}
	if coverage := noTitles["coverage"].(map[string]any); coverage["titles"].(bool) {
		t.Error("coverage.titles true without titles")
	}

	fullPaths := roundTrip(t, buildFixture(t, Options{Root: fixtureRoot(t), IncludeTitles: true, FullPaths: true}))
	for _, entry := range fullPaths["projects"].([]any) {
		item := entry.(map[string]any)
		if item["name"] != item["path"] {
			t.Errorf("--full-paths name %v != path %v", item["name"], item["path"])
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
