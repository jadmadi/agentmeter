// SPDX-License-Identifier: AGPL-3.0-only

package contract_test

import (
	"encoding/json"
	"testing"

	"github.com/jadmadi/agentmeter/internal/contract"
)

// goodReport is the known-good fixture from
// tests/test_analytics_invariants.py::good_report, expressed with the Go
// integer/float mix a builder emits.
func goodReport() map[string]any {
	return map[string]any{
		"schemaVersion": int64(contract.SchemaVersion),
		"meta":          map[string]any{"timing": map[string]any{"plausible": int64(2)}},
		"totals": map[string]any{
			"steps":            int64(2),
			"generatedTokens":  int64(10),
			"freshInputTokens": int64(1),
			"inputTotal":       int64(3),
			"cacheReadTokens":  int64(2),
			"cacheWriteTokens": int64(0),
			"costUsd":          1.0,
			"totalSpendUsd":    1.5,
		},
		"byModel": []any{map[string]any{
			"model":            "m",
			"steps":            int64(2),
			"generatedTokens":  int64(10),
			"freshInputTokens": int64(1),
			"inputTotal":       int64(3),
			"cacheReadTokens":  int64(2),
			"cacheWriteTokens": int64(0),
			"cacheHitRate":     0.6667,
			"timedN":           int64(2),
		}},
		"cost": map[string]any{
			"recorded":      1.0,
			"errorCostUsd":  0.5,
			"totalSpendUsd": 1.5,
			"byModel": []any{map[string]any{
				"model":           "m",
				"recordedCost":    1.0,
				"generatedTokens": int64(10),
				"inputTotal":      int64(3),
				"cacheHitRate":    0.6667,
			}},
		},
		"cache": map[string]any{"inputTokens": int64(3), "cacheReadTokens": int64(2), "hitRate": 0.6667},
		"timed": map[string]any{"all": map[string]any{
			"n":           int64(2),
			"maxIncluded": int64(10),
			"maxObserved": int64(12),
		}},
		"sessions": []any{map[string]any{"steps": int64(2), "generatedTokens": int64(10), "costUsd": 1.0}},
		"projects": []any{map[string]any{"name": "p", "steps": int64(2), "generatedTokens": int64(10), "costUsd": 1.0}},
		"tools":    []any{map[string]any{"tool": "t", "shareCalls": 1.0, "timeShare": 0.5}},
		"daily": []any{map[string]any{
			"nAll":            int64(2),
			"nTimed":          int64(2),
			"generatedTokens": int64(10),
			"costUsd":         1.0,
			"totalSpendUsd":   1.5,
			"ratio":           1.0,
			"ratioPct":        100.0,
			"index":           100.0,
		}},
		"hourly": []any{map[string]any{
			"nAll":            int64(2),
			"nTimed":          int64(2),
			"generatedTokens": int64(10),
			"costUsd":         1.0,
			"totalSpendUsd":   1.5,
		}},
	}
}

// deepCopy clones the map/slice skeleton so mutations cannot leak between
// subtests.
func deepCopy(value any) any {
	switch node := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(node))
		for key, item := range node {
			out[key] = deepCopy(item)
		}
		return out
	case []any:
		out := make([]any, len(node))
		for i, item := range node {
			out[i] = deepCopy(item)
		}
		return out
	}
	return value
}

func report() map[string]any {
	return deepCopy(goodReport()).(map[string]any)
}

func totals(d map[string]any) map[string]any { return d["totals"].(map[string]any) }
func cost(d map[string]any) map[string]any   { return d["cost"].(map[string]any) }
func cache(d map[string]any) map[string]any  { return d["cache"].(map[string]any) }
func timedAll(d map[string]any) map[string]any {
	return d["timed"].(map[string]any)["all"].(map[string]any)
}
func byModelRow(d map[string]any) map[string]any { return d["byModel"].([]any)[0].(map[string]any) }
func costModelRow(d map[string]any) map[string]any {
	return cost(d)["byModel"].([]any)[0].(map[string]any)
}
func sessionRow(d map[string]any) map[string]any { return d["sessions"].([]any)[0].(map[string]any) }
func projectRow(d map[string]any) map[string]any { return d["projects"].([]any)[0].(map[string]any) }
func toolRow(d map[string]any) map[string]any    { return d["tools"].([]any)[0].(map[string]any) }
func dailyRow(d map[string]any) map[string]any   { return d["daily"].([]any)[0].(map[string]any) }
func hourlyRow(d map[string]any) map[string]any  { return d["hourly"].([]any)[0].(map[string]any) }
func timing(d map[string]any) map[string]any {
	return d["meta"].(map[string]any)["timing"].(map[string]any)
}

func TestValidateCommonAcceptsGoodReport(t *testing.T) {
	if issues := contract.ValidateCommonIssues(goodReport()); len(issues) != 0 {
		t.Fatalf("good report reported issues: %v", issues.Strings())
	}
}

// A report decoded from JSON carries float64 for every number. The validator
// must accept that representation too, because parity tests decode reports.
func TestValidateCommonAcceptsDecodedReport(t *testing.T) {
	encoded, err := json.Marshal(goodReport())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if issues := contract.ValidateCommonIssues(decoded); len(issues) != 0 {
		t.Fatalf("decoded report reported issues: %v", issues.Strings())
	}
}

func TestValidateCommonRejectsEmptyReport(t *testing.T) {
	issues := contract.ValidateCommonIssues(map[string]any{})
	if len(issues) == 0 {
		t.Fatal("empty report passed validation")
	}
	for _, rule := range []string{
		contract.RuleSchema,
		contract.RuleTotalsStepsNonNegative,
		contract.RuleTotalsInputSum,
		contract.RuleTotalsStepsSum,
	} {
		if !issues.Has(rule) {
			t.Errorf("empty report did not violate %s; got %v", rule, issues.Rules())
		}
	}
}

func TestValidateCommonContainerTypes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(d map[string]any)
	}{
		{"totals_is_array", func(d map[string]any) { d["totals"] = []any{int64(1)} }},
		{"byModel_is_object", func(d map[string]any) { d["byModel"] = map[string]any{"m": int64(1)} }},
		{"cost_is_array", func(d map[string]any) { d["cost"] = []any{"x"} }},
		{"cache_is_string", func(d map[string]any) { d["cache"] = "x" }},
		{"timed_is_array", func(d map[string]any) { d["timed"] = []any{int64(1)} }},
		{"daily_is_object", func(d map[string]any) { d["daily"] = map[string]any{"x": int64(1)} }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			data := report()
			test.mutate(data)
			issues := contract.ValidateCommonIssues(data)
			if !issues.Has(contract.RuleSections) {
				t.Fatalf("expected %s, got %v", contract.RuleSections, issues.Strings())
			}
		})
	}
}

// The tolerances must match the Python validator exactly.
func TestValidateCommonTolerances(t *testing.T) {
	cases := []struct {
		name      string
		rule      string
		tolerance float64
		base      func(d map[string]any)
		within    float64
		outside   float64
	}{
		{
			name: "session_cost_1e-3", rule: contract.RuleSessionsCostSum, tolerance: 1e-3,
			base:   func(d map[string]any) { sessionRow(d)["costUsd"] = 1.0 },
			within: 5e-4, outside: 2e-3,
		},
		{
			name: "daily_cost_1e-4", rule: contract.RuleDailyCostSum, tolerance: 1e-4,
			base:   func(d map[string]any) { dailyRow(d)["costUsd"] = 1.0 },
			within: 5e-5, outside: 2e-4,
		},
		{
			name: "daily_spend_1e-3", rule: contract.RuleDailySpendSum, tolerance: 1e-3,
			base:   func(d map[string]any) { dailyRow(d)["totalSpendUsd"] = 1.5 },
			within: 5e-4, outside: 2e-3,
		},
		{
			name: "hourly_spend_1e-3", rule: contract.RuleHourlySpendSum, tolerance: 1e-3,
			base:   func(d map[string]any) { hourlyRow(d)["totalSpendUsd"] = 1.5 },
			within: 5e-4, outside: 2e-3,
		},
		{
			name: "by_model_cache_hit_1e-6", rule: contract.RuleByModelCacheHitRate, tolerance: 1e-6,
			base:   func(d map[string]any) { byModelRow(d)["cacheHitRate"] = 0.6667 },
			within: 5e-7, outside: 2e-6,
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			// Control: the untouched base value must validate.
			control := report()
			test.base(control)
			if issues := contract.ValidateCommonIssues(control); len(issues) != 0 {
				t.Fatalf("control reported issues: %v", issues.Strings())
			}

			// Inside the tolerance: still clean.
			inside := report()
			test.base(inside)
			adjustNestedValue(inside, test.name, test.within)
			if issues := contract.ValidateCommonIssues(inside); issues.Has(test.rule) {
				t.Fatalf("value within tolerance %g violated %s: %v", test.within, test.rule, issues.Strings())
			}

			// Outside the tolerance: the rule fires.
			outside := report()
			test.base(outside)
			adjustNestedValue(outside, test.name, test.outside)
			if issues := contract.ValidateCommonIssues(outside); !issues.Has(test.rule) {
				t.Fatalf("value outside tolerance %g did not violate %s: %v", test.outside, test.rule, issues.Strings())
			}
		})
	}
}

// adjustNestedValue applies a delta to the field each tolerance case starts
// from.
func adjustNestedValue(d map[string]any, name string, delta float64) {
	switch name {
	case "session_cost_1e-3":
		sessionRow(d)["costUsd"] = 1.0 + delta
	case "daily_cost_1e-4":
		dailyRow(d)["costUsd"] = 1.0 + delta
	case "daily_spend_1e-3":
		dailyRow(d)["totalSpendUsd"] = 1.5 + delta
	case "hourly_spend_1e-3":
		hourlyRow(d)["totalSpendUsd"] = 1.5 + delta
	case "by_model_cache_hit_1e-6":
		byModelRow(d)["cacheHitRate"] = 0.6667 + delta
	}
}

// Integer identity checks must be exact, unlike the close() family.
func TestValidateCommonExactChecks(t *testing.T) {
	cases := []struct {
		name   string
		rule   string
		mutate func(d map[string]any)
	}{
		{
			name:   "input_sum_exact",
			rule:   contract.RuleTotalsInputSum,
			mutate: func(d map[string]any) { totals(d)["inputTotal"] = 3.0000001 },
		},
		{
			name:   "steps_sum_exact",
			rule:   contract.RuleTotalsStepsSum,
			mutate: func(d map[string]any) { byModelRow(d)["steps"] = 3 },
		},
		{
			name:   "daily_n_sum_exact",
			rule:   contract.RuleDailyNSum,
			mutate: func(d map[string]any) { dailyRow(d)["nAll"] = 1 },
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			data := report()
			test.mutate(data)
			issues := contract.ValidateCommonIssues(data)
			if !issues.Has(test.rule) {
				t.Fatalf("expected %s, got %v", test.rule, issues.Strings())
			}
		})
	}
}

func TestIssuesHelpers(t *testing.T) {
	issues := contract.Issues{
		{Rule: "a.b", Message: "first"},
		{Rule: "c.d", Message: ""},
	}
	strings := issues.Strings()
	if len(strings) != 2 || strings[0] != "a.b: first" || strings[1] != "c.d" {
		t.Fatalf("Strings() = %v", strings)
	}
	if got := issues.Rules(); len(got) != 2 || got[0] != "a.b" || got[1] != "c.d" {
		t.Fatalf("Rules() = %v", got)
	}
	if !issues.Has("a.b") || issues.Has("missing") {
		t.Fatalf("Has() misbehaved")
	}
	if issues[0].Error() != "a.b: first" {
		t.Fatalf("Error() = %q", issues[0].Error())
	}
	if len(contract.Issues(nil).Strings()) != 0 {
		t.Fatal("nil Issues must render no strings")
	}
}

func TestTimingReasons(t *testing.T) {
	want := []string{"plausible", "noTiming", "tooShort", "tooShortDuration", "tooLong", "slowRate", "fastRate"}
	if len(contract.TimingReasons) != len(want) {
		t.Fatalf("TimingReasons = %v", contract.TimingReasons)
	}
	for i, reason := range want {
		if contract.TimingReasons[i] != reason {
			t.Fatalf("TimingReasons[%d] = %q, want %q", i, contract.TimingReasons[i], reason)
		}
		if got := contract.TimingReasonIndex(reason); got != i {
			t.Fatalf("TimingReasonIndex(%q) = %d, want %d", reason, got, i)
		}
		if !contract.IsTimingReason(reason) {
			t.Fatalf("IsTimingReason(%q) = false", reason)
		}
	}
	if contract.TimingReasonIndex("bogus") != -1 || contract.IsTimingReason("bogus") {
		t.Fatal("unknown timing reason accepted")
	}
	copied := contract.TimingReasonsCopy()
	copied[0] = "mutated"
	if contract.TimingReasons[0] != "plausible" {
		t.Fatal("TimingReasonsCopy shares the package slice")
	}
}

func TestStepLayoutConstants(t *testing.T) {
	if contract.CmdStepSlots != 20 || contract.OCStepSlots != 32 {
		t.Fatalf("step widths = %d/%d", contract.CmdStepSlots, contract.OCStepSlots)
	}
	if contract.CmdStepTimingSlot != 18 || contract.CmdStepToolsSlot != 19 {
		t.Fatalf("cmd slots timing=%d tools=%d", contract.CmdStepTimingSlot, contract.CmdStepToolsSlot)
	}
	if contract.OCStepToolCallsSlot != 29 || contract.OCStepTimingSlot != 31 {
		t.Fatalf("oc slots calls=%d timing=%d", contract.OCStepToolCallsSlot, contract.OCStepTimingSlot)
	}
	if contract.OCToolCallSlots != 4 {
		t.Fatalf("oc tool call width = %d", contract.OCToolCallSlots)
	}
}

// Python treats 3.0 == 3, True == 1 and int/float interchangeably in
// arithmetic. The validator must accept float-encoded integers so a report
// decoded with encoding/json still validates.
func TestValidateCommonNumericRepresentations(t *testing.T) {
	data := report()
	data["schemaVersion"] = 3.0
	totals(data)["steps"] = 2.0
	byModelRow(data)["steps"] = 2.0
	byModelRow(data)["timedN"] = 2.0
	timedAll(data)["n"] = 2.0
	sessionRow(data)["steps"] = 2.0
	projectRow(data)["steps"] = 2.0
	dailyRow(data)["nAll"] = 2.0
	hourlyRow(data)["nAll"] = 2.0
	if issues := contract.ValidateCommonIssues(data); len(issues) != 0 {
		t.Fatalf("float-encoded integers rejected:\n%v", issues.Strings())
	}

	for _, invalid := range []any{4.0, "3", nil, true} {
		data := report()
		data["schemaVersion"] = invalid
		if issues := contract.ValidateCommonIssues(data); !issues.Has(contract.RuleSchema) {
			t.Errorf("schemaVersion %v accepted: %v", invalid, issues.Strings())
		}
	}
}

// close() uses a relative tolerance: abs(a-b) <= max(tol, |tol*b|). At b=1000
// the accepted drift is ~1e-3, not 1e-6.
func TestValidateCommonRelativeTolerance(t *testing.T) {
	base := func() map[string]any {
		data := report()
		byModelRow(data)["freshInputTokens"] = int64(998)
		byModelRow(data)["cacheReadTokens"] = int64(2)
		byModelRow(data)["inputTotal"] = int64(1000)
		costModelRow(data)["inputTotal"] = 1000.0
		return data
	}
	if issues := contract.ValidateCommonIssues(base()); len(issues) != 0 {
		t.Fatalf("control rejected:\n%v", issues.Strings())
	}
	within := base()
	costModelRow(within)["inputTotal"] = 1000.0005
	if issues := contract.ValidateCommonIssues(within); issues.Has(contract.RuleByModelInputTotal) {
		t.Fatalf("relative tolerance rejected 5e-4 at 1e3: %v", issues.Strings())
	}
	outside := base()
	costModelRow(outside)["inputTotal"] = 1000.002
	if issues := contract.ValidateCommonIssues(outside); !issues.Has(contract.RuleByModelInputTotal) {
		t.Fatalf("relative tolerance accepted 2e-3 at 1e3: %v", issues.Strings())
	}
}
