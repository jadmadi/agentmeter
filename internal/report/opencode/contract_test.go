// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"math"
	"testing"
)

// stepIndexes resolves the lookup vocabularies the step tuples index into.
type stepIndexes struct {
	models      int
	tools       int
	agents      int
	variants    int
	projects    int
	entrypoints int
	sessions    int
	finishes    int
	timing      []any
	toolNames   []any
}

func resolveIndexes(t *testing.T, report map[string]any) stepIndexes {
	t.Helper()
	lookups := report["lookups"].(map[string]any)
	return stepIndexes{
		models:      len(lookups["models"].([]any)),
		tools:       len(lookups["tools"].([]any)),
		agents:      len(lookups["agents"].([]any)),
		variants:    len(lookups["variants"].([]any)),
		projects:    len(lookups["projects"].([]any)),
		entrypoints: len(lookups["entrypoints"].([]any)),
		sessions:    len(report["sessions"].([]any)),
		finishes:    len(lookups["finishes"].([]any)),
		timing:      lookups["timingReasons"].([]any),
		toolNames:   lookups["tools"].([]any),
	}
}

// intAt reads an int-like tuple slot.
func intAt(t *testing.T, tuple []any, slot int) int64 {
	t.Helper()
	switch value := tuple[slot].(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		if value != math.Trunc(value) {
			t.Fatalf("slot %d: %v is not an integer", slot, value)
		}
		return int64(value)
	default:
		t.Fatalf("slot %d: %T is not an integer", slot, tuple[slot])
		return 0
	}
}

// numberOrSentinel reads a duration slot: float or the -1 sentinel.
func numberOrSentinel(t *testing.T, tuple []any, slot int) (float64, bool) {
	t.Helper()
	switch value := tuple[slot].(type) {
	case int64:
		if value != -1 {
			t.Fatalf("slot %d: %d is neither a float nor the -1 sentinel", slot, value)
		}
		return 0, false
	case int:
		if value != -1 {
			t.Fatalf("slot %d: %d is neither a float nor the -1 sentinel", slot, value)
		}
		return 0, false
	case float64:
		if value == -1 {
			return 0, false
		}
		return value, true
	default:
		t.Fatalf("slot %d: %T is not numeric", slot, tuple[slot])
		return 0, false
	}
}

// TestStepTupleContract pins the 32-slot OpenCode step layout, its sentinels and
// every index range the renderer relies on.
func TestStepTupleContract(t *testing.T) {
	dbPath := fixtureDB(t)
	report, err := Build(Options{DBPath: dbPath, IncludeTitles: true, Now: fixtureNow})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	indexes := resolveIndexes(t, report)
	steps := report["steps"].([]any)
	if len(steps) == 0 {
		t.Fatal("fixture must produce steps")
	}
	bySession := map[[2]int64]bool{}
	lastEpoch := int64(-1)
	sawToolCall := false
	sawSentinel := false
	sawFinalAnswer := false
	for _, item := range steps {
		tuple, ok := item.([]any)
		if !ok {
			t.Fatalf("step is %T, not an array", item)
		}
		if len(tuple) != 32 {
			t.Fatalf("step tuple has %d slots, want 32", len(tuple))
		}

		epoch := intAt(t, tuple, 0)
		if epoch <= 0 || epoch > 1e11 {
			t.Fatalf("epoch %d is not in seconds", epoch)
		}
		if epoch < lastEpoch {
			t.Fatalf("steps are not sorted by epoch: %d after %d", epoch, lastEpoch)
		}
		lastEpoch = epoch

		model := intAt(t, tuple, 1)
		tool := intAt(t, tuple, 2)
		agent := intAt(t, tuple, 9)
		project := intAt(t, tuple, 11)
		entry := intAt(t, tuple, 12)
		session := intAt(t, tuple, 13)
		variant := intAt(t, tuple, 21)
		finish := intAt(t, tuple, 22)
		timing := intAt(t, tuple, 31)
		for name, pair := range map[string][2]int64{
			"model":      {model, int64(indexes.models)},
			"tool":       {tool, int64(indexes.tools)},
			"agent":      {agent, int64(indexes.agents)},
			"project":    {project, int64(indexes.projects)},
			"entrypoint": {entry, int64(indexes.entrypoints)},
			"session":    {session, int64(indexes.sessions)},
			"variant":    {variant, int64(indexes.variants)},
			"finish":     {finish, int64(indexes.finishes)},
			"timing":     {timing, int64(len(indexes.timing))},
		} {
			if pair[0] < 0 || pair[0] >= pair[1] {
				t.Fatalf("%s index %d out of range [0,%d)", name, pair[0], pair[1])
			}
		}

		// Token composition.
		out := intAt(t, tuple, 3)
		fresh := intAt(t, tuple, 5)
		cacheRead := intAt(t, tuple, 6)
		cacheWrite := intAt(t, tuple, 7)
		reasoning := intAt(t, tuple, 17)
		inputTotal := intAt(t, tuple, 20)
		if inputTotal != fresh+cacheRead+cacheWrite {
			t.Fatalf("input total %d != %d+%d+%d", inputTotal, fresh, cacheRead, cacheWrite)
		}
		if out < 0 || reasoning < 0 {
			t.Fatalf("negative tokens: out=%d reasoning=%d", out, reasoning)
		}

		// Durations: float or -1, and a missing LLM duration implies noTiming.
		dt, hasDT := numberOrSentinel(t, tuple, 4)
		for _, slot := range []int{18, 19, 23, 24, 25, 26, 27, 28} {
			if _, ok := numberOrSentinel(t, tuple, slot); !ok {
				sawSentinel = true
			}
		}
		if !hasDT {
			sawSentinel = true
		}
		if !hasDT && indexes.timing[timing] != "noTiming" {
			t.Fatalf("a step without dt must be noTiming, got %v", indexes.timing[timing])
		}
		if hasDT && dt <= 0 {
			t.Fatalf("dt must be positive, got %v", dt)
		}
		ratio, hasRatio := numberOrSentinel(t, tuple, 30)
		if hasRatio && ratio <= 0 {
			t.Fatalf("ratio must be positive, got %v", ratio)
		}
		if hasRatio && !hasDT {
			t.Fatal("ratio requires a duration")
		}

		// Tool-call rows: [tool_idx, runtime|-1, error, allocated].
		calls, ok := tuple[29].([]any)
		if !ok {
			t.Fatalf("tool calls are %T, not an array", tuple[29])
		}
		toolCount := intAt(t, tuple, 14)
		if int64(len(calls)) != toolCount {
			t.Fatalf("%d tool calls but nTools=%d", len(calls), toolCount)
		}
		if len(calls) == 0 {
			if indexes.toolNames[tool] != "(final answer)" {
				t.Fatalf("a text step must use the (final answer) tool, got %v", indexes.toolNames[tool])
			}
			sawFinalAnswer = true
		}
		for _, rawCall := range calls {
			call, ok := rawCall.([]any)
			if !ok || len(call) != 4 {
				t.Fatalf("tool call row: %T %v", rawCall, rawCall)
			}
			callTool := intAt(t, call, 0)
			if callTool < 0 || callTool >= int64(indexes.tools) {
				t.Fatalf("tool call index %d out of range", callTool)
			}
			numberOrSentinel(t, call, 1)
			errorFlag := intAt(t, call, 2)
			if errorFlag != 0 && errorFlag != 1 {
				t.Fatalf("error flag: %d", errorFlag)
			}
			allocated, ok := call[3].(float64)
			if !ok || allocated < 0 {
				t.Fatalf("allocated seconds: %v", call[3])
			}
			sawToolCall = true
		}

		key := [2]int64{epoch, session}
		if bySession[key] {
			t.Fatalf("duplicate (timestamp, session) step: %v", key)
		}
		bySession[key] = true
	}
	if !sawToolCall || !sawSentinel || !sawFinalAnswer {
		t.Fatalf("fixture should cover tool calls, sentinels and text steps: %v %v %v", sawToolCall, sawSentinel, sawFinalAnswer)
	}

	// Every compact index list aligns with its per-model companion list.
	models := report["lookups"].(map[string]any)["models"].([]any)
	for name, list := range map[string]any{
		"byModel":    report["byModel"],
		"fits":       report["fits"],
		"prices":     report["prices"],
		"regression": report["regression"],
	} {
		if got := len(list.([]any)); got != len(models) {
			t.Fatalf("%s has %d entries, want %d", name, got, len(models))
		}
	}
}

// TestThresholdsChangeCensoring checks that custom thresholds flow into the
// bucket counters, the timed summary and the compact timing index.
func TestThresholdsChangeCensoring(t *testing.T) {
	dbPath := fixtureDB(t)
	base, err := Build(Options{DBPath: dbPath, IncludeTitles: true, Now: fixtureNow})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	strictThresholds := Thresholds{MinSeconds: 2.0, MaxSeconds: 5.0, MinTokens: 100, MinRate: 50, MaxRate: 150}
	strict, err := Build(Options{
		DBPath: dbPath, IncludeTitles: true, Thresholds: &strictThresholds, Now: fixtureNow,
	})
	if err != nil {
		t.Fatalf("build strict: %v", err)
	}

	baseTiming := base["meta"].(map[string]any)["timing"].(map[string]any)
	strictTiming := strict["meta"].(map[string]any)["timing"].(map[string]any)

	thresholds := strictTiming["thresholds"].(map[string]any)
	if thresholds["minSeconds"] != 2.0 || thresholds["maxSeconds"] != 5.0 ||
		thresholds["minTokens"] != int64(100) || thresholds["minRate"] != 50.0 || thresholds["maxRate"] != 150.0 {
		t.Fatalf("thresholds echo: %v", thresholds)
	}
	basePlausible := baseTiming["plausible"].(int)
	strictPlausible := strictTiming["plausible"].(int)
	if strictPlausible >= basePlausible {
		t.Fatalf("stricter thresholds must reject more steps: %d vs %d", strictPlausible, basePlausible)
	}
	if strict["timed"].(map[string]any)["all"].(map[string]any)["n"] != strictPlausible {
		t.Fatalf("timed.all.n must equal meta.timing.plausible: %v vs %v",
			strict["timed"].(map[string]any)["all"].(map[string]any)["n"], strictPlausible)
	}

	// The compact timing index must follow the strict buckets.
	indexes := resolveIndexes(t, strict)
	counts := map[string]int{}
	for _, item := range strict["steps"].([]any) {
		tuple := item.([]any)
		counts[indexes.timing[intAt(t, tuple, 31)].(string)]++
	}
	for reason, count := range counts {
		if reason == "plausible" {
			if count != strictPlausible {
				t.Fatalf("plausible steps in tuples: %d, want %d", count, strictPlausible)
			}
			continue
		}
		if got := strictTiming[reason].(int); got != count {
			t.Fatalf("timing %q: meta=%d tuples=%d", reason, got, count)
		}
	}
}
