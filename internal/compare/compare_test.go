// SPDX-License-Identifier: AGPL-3.0-only

package compare

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestNormalizeFamily(t *testing.T) {
	cases := []struct {
		id, short, want string
	}{
		{"agentrouter/deepseek-v4-flash", "deepseek-v4-flash", "deepseek-v4-flash"},
		{"MiniMaxAI/MiniMax-M3", "MiniMax-M3", "minimax-m3"},
		{"", "Gemini 3.8 flash", "gemini-3.8-flash"},
		{"provider/model_x", "", "model-x"},
		{"", "", "(unknown)"},
		{"a/DeepSeek_V4.1__Flash", "", "deepseek-v4.1-flash"},
		{"x/y:free", "y:free", "y-free"},
	}
	for _, test := range cases {
		if got := NormalizeFamily(test.id, test.short); got != test.want {
			t.Errorf("NormalizeFamily(%q, %q) = %q, want %q", test.id, test.short, got, test.want)
		}
	}
}

func TestNormalizeTool(t *testing.T) {
	cases := map[string]string{
		"shell_command":       "shell",
		"bash":                "shell",
		"run_command":         "shell",
		"read_file":           "read",
		"view":                "read",
		"write_file":          "write",
		"apply_patch":         "edit",
		"multiedit":           "edit",
		"grep":                "search",
		"glob":                "search",
		"list_dir":            "search",
		"web_search":          "web",
		"webfetch":            "web",
		"task":                "agent",
		"todowrite":           "todo",
		"(final answer)":      "final answer",
		"unknown_custom_tool": "other",
		"":                    "other",
	}
	for input, want := range cases {
		if got := NormalizeTool(input); got != want {
			t.Errorf("NormalizeTool(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestPercentDelta(t *testing.T) {
	a, b := 150.0, 100.0
	got := PercentDelta(&a, &b)
	if got == nil || math.Abs(*got-50) > 1e-9 {
		t.Fatalf("PercentDelta(150, 100) = %v, want 50", got)
	}
	zero := 0.0
	if PercentDelta(&a, &zero) != nil {
		t.Error("PercentDelta with zero denominator should be nil")
	}
	if PercentDelta(nil, &b) != nil {
		t.Error("PercentDelta with nil numerator should be nil")
	}
	negative, positive := -10.0, 20.0
	got = PercentDelta(&negative, &positive)
	if got == nil || math.Abs(*got-(-150)) > 1e-9 {
		t.Fatalf("PercentDelta(-10, 20) = %v, want -150", got)
	}
}

func TestPointDeltaAndWinner(t *testing.T) {
	a, b := 0.75, 0.50
	got := PointDelta(&a, &b)
	if got == nil || math.Abs(*got-25) > 1e-9 {
		t.Fatalf("PointDelta = %v, want 25", got)
	}
	if Winner(&a, &b, true, ToolOC, ToolCMD) != ToolOC {
		t.Error("higher value should win when higherBetter")
	}
	if Winner(&a, &b, false, ToolOC, ToolCMD) != ToolCMD {
		t.Error("lower value should win when !higherBetter")
	}
	if Winner(&a, &a, true, ToolOC, ToolCMD) != "tie" {
		t.Error("equal values should tie")
	}
	if Winner(nil, &b, true, ToolOC, ToolCMD) != "n/a" {
		t.Error("missing side should be n/a")
	}
}

func TestRatioAndTieHelpers(t *testing.T) {
	a, b := 104.0, 100.0
	ratio := RatioDelta(&a, &b)
	if ratio == nil || math.Abs(*ratio-1.04) > 1e-9 {
		t.Fatalf("RatioDelta = %v, want 1.04", ratio)
	}
	if WinnerWithTolerance(&a, &b, true, 0.05, ToolOC, ToolCMD) != "tie" {
		t.Error("a 4% speed gap should be a tie under a 5% band")
	}
	far := 120.0
	if WinnerWithTolerance(&far, &b, true, 0.05, ToolOC, ToolCMD) != ToolOC {
		t.Error("a 20% speed gap should favor OpenCode")
	}
	cacheA, cacheB := 0.983, 0.9814
	if WinnerCacheRate(&cacheA, &cacheB, 1.0, 0.05, ToolOC, ToolCMD) != "tie" {
		t.Error("a 0.16 pp cache gap should be a tie under a 1 pp band")
	}
	cacheFar := 0.90
	if WinnerCacheRate(&cacheA, &cacheFar, 1.0, 0.05, ToolOC, ToolCMD) != ToolOC {
		t.Error("an 8.3 pp cache gap should favor OpenCode")
	}
	if RatioDelta(nil, &b) != nil {
		t.Error("RatioDelta with nil numerator should be nil")
	}
}

func TestRatesAndBuckets(t *testing.T) {
	rate := Rate(25, 100)
	if rate == nil || *rate != 0.25 {
		t.Fatalf("Rate = %v", rate)
	}
	if Rate(1, 0) != nil {
		t.Error("Rate with zero denominator should be nil")
	}
	weighted := WeightedRate(100, 2)
	if weighted == nil || *weighted != 50 {
		t.Fatalf("WeightedRate = %v", weighted)
	}
	perK := PerKThousand(0.5, 1000)
	if perK == nil || *perK != 0.5 {
		t.Fatalf("PerKThousand = %v", perK)
	}
	cases := map[int64]string{19: "", 20: "20-100", 99: "20-100", 100: "100-300", 300: "300-1k", 999: "300-1k", 1000: "1k-5k", 4999: "1k-5k", 5000: "5k+", 1_000_000: "5k+"}
	for generated, want := range cases {
		got, ok := BucketFor(generated)
		if want == "" {
			if ok {
				t.Errorf("BucketFor(%d) = %q, want no bucket", generated, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("BucketFor(%d) = %q/%v, want %q", generated, got, ok, want)
		}
	}
}

func TestParseTimezone(t *testing.T) {
	cases := []struct {
		label  string
		offset int
	}{
		{"UTC", 0},
		{"UTC+00:00", 0},
		{"UTC+03:30", 3*3600 + 30*60},
		{"UTC-07:00", -7 * 3600},
		{"", 0},
		{"Mars/Olympus", 0},
	}
	for _, test := range cases {
		location := ParseTimezone(test.label)
		if location == nil {
			t.Fatalf("ParseTimezone(%q) returned nil", test.label)
		}
		_, offset := time.Date(2026, 1, 1, 0, 0, 0, 0, location).Zone()
		if offset != test.offset {
			t.Errorf("ParseTimezone(%q) offset = %d, want %d", test.label, offset, test.offset)
		}
	}
}

func TestParseRejectsInvalidReports(t *testing.T) {
	cases := []struct {
		name string
		data map[string]any
		want string
	}{
		{"empty", map[string]any{}, "schemaVersion"},
		{"wrong version", map[string]any{"schemaVersion": int64(2)}, "schemaVersion"},
		{"missing sections", map[string]any{"schemaVersion": int64(3)}, "missing"},
	}
	for _, test := range cases {
		_, err := Parse(ToolOC, Input{Kind: KindReport}, test.data)
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: error = %v, want substring %q", test.name, err, test.want)
		}
	}
}

func TestParseRejectsArithmeticMismatch(t *testing.T) {
	data := minimalReport()
	data["totals"].(map[string]any)["steps"] = int64(99)
	_, err := Parse(ToolCMD, Input{Kind: KindReport}, data)
	if err == nil || !strings.Contains(err.Error(), "totals.steps") {
		t.Fatalf("error = %v, want a totals.steps mismatch", err)
	}
}

func TestParseRejectsBadStepTuple(t *testing.T) {
	data := minimalReport()
	steps := data["steps"].([]any)
	steps[0] = []any{int64(1), int64(0)}
	_, err := Parse(ToolCMD, Input{Kind: KindReport}, data)
	if err == nil || !strings.Contains(err.Error(), "slots") {
		t.Fatalf("error = %v, want a tuple-length error", err)
	}
}

// minimalReport is the smallest valid cmd schema-v3 shape for parser tests:
// one model, one timed step, one final-answer call.
func minimalReport() map[string]any {
	return map[string]any{
		"schemaVersion": int64(3),
		"meta": map[string]any{
			"generatedAt": "2026-01-02T00:00:00+00:00",
			"timezone":    "UTC",
			"dateFrom":    "2026-01-02",
			"dateTo":      "2026-01-02",
			"models":      int64(1),
			"sessions":    int64(1),
		},
		"totals": map[string]any{
			"steps":            int64(1),
			"toolSteps":        int64(0),
			"toolCalls":        int64(1),
			"generatedTokens":  int64(100),
			"inputTotal":       int64(50),
			"cacheReadTokens":  int64(0),
			"cacheWriteTokens": int64(0),
			"costUsd":          0.01,
			"totalSpendUsd":    0.01,
			"models":           int64(1),
		},
		"timed": map[string]any{"all": map[string]any{
			"n": int64(1), "tokens": int64(100), "seconds": 2.0,
			"weighted": 50.0, "median": 50.0, "p10": 50.0, "p25": 50.0,
			"p75": 50.0, "p90": 50.0, "p99": 50.0, "max": 50.0,
		}},
		"cache": map[string]any{
			"inputTokens": int64(50), "cacheReadTokens": int64(0),
			"cacheWriteTokens": int64(0), "hitRate": 0.0, "writeShare": 0.0,
			"savings": 0.0, "fittedModels": int64(0),
		},
		"cost": map[string]any{
			"recorded": 0.01, "errorCostUsd": 0.0, "totalSpendUsd": 0.01,
			"perStep": 0.01, "perMillionGenerated": 100.0,
		},
		"byModel": []any{map[string]any{
			"model": "p/m", "steps": int64(1), "generatedTokens": int64(100),
			"inputTotal": int64(50), "cacheReadTokens": int64(0),
			"cacheWriteTokens": int64(0), "costUsd": 0.01, "timedN": int64(1),
			"weighted": 50.0, "median": 50.0, "p90": 50.0,
			"cacheHitRate": 0.0, "inputIncludesCache": false,
		}},
		"lookups": map[string]any{
			"models": []any{map[string]any{"id": "p/m", "short": "m", "provider": "p"}},
			"tools":  []any{"(final answer)"},
			"timingReasons": []any{
				"plausible", "noTiming", "tooShort", "tooShortDuration",
				"tooLong", "slowRate", "fastRate",
			},
		},
		"tools": []any{map[string]any{
			"tool": "(final answer)", "calls": int64(1), "stepsWithTool": int64(1),
			"shareCalls": 1.0, "generatedTokens": 100.0, "costUsd": 0.01,
			"timedCalls": int64(1), "weighted": 50.0, "medianTps": 50.0,
			"timeShare": nil, "runtime": "unavailable",
		}},
		"steps": []any{[]any{
			int64(1767312000), int64(0), int64(0), int64(100), 2.0,
			int64(50), int64(0), int64(0), 0.01, int64(0), int64(0),
			int64(0), int64(0), int64(0), int64(0), int64(0), int64(1),
			int64(-1), int64(0), []any{},
		}},
		"sessions": []any{map[string]any{
			"sessionId": "s1", "steps": int64(1), "generatedTokens": int64(100),
			"costUsd": 0.01, "spanMinutes": 0.0,
		}},
		"daily": []any{map[string]any{
			"date": "2026-01-02", "nAll": int64(1), "nTimed": int64(1),
			"generatedTokens": int64(100), "inputTotal": int64(50),
			"cacheReadTokens": int64(0), "costUsd": 0.01, "totalSpendUsd": 0.01,
			"cacheHitRate": 0.0, "weighted": 50.0, "median": 50.0,
		}},
	}
}
