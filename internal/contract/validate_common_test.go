// SPDX-License-Identifier: AGPL-3.0-only

package contract_test

import (
	"math"
	"testing"

	"github.com/jadmadi/agentmeter/internal/contract"
)

// mutation breaks one invariant of a known-good report and names the rule that
// must catch it.
type mutation struct {
	name  string
	rule  string
	apply func(d map[string]any)
}

// commonMutations has at least one mutation for every shared rule id.
func commonMutations() []mutation {
	return []mutation{
		{"schema_version", contract.RuleSchema, func(d map[string]any) {
			d["schemaVersion"] = int64(4)
		}},
		{"schema_version_missing", contract.RuleSchema, func(d map[string]any) {
			delete(d, "schemaVersion")
		}},
		{"totals_container", contract.RuleSections, func(d map[string]any) {
			d["totals"] = []any{int64(1)}
		}},

		// Non-negative totals.
		{"totals_steps_negative", contract.RuleTotalsStepsNonNegative, func(d map[string]any) {
			totals(d)["steps"] = int64(-1)
		}},
		{"totals_generated_negative", contract.RuleTotalsGeneratedNonNegative, func(d map[string]any) {
			totals(d)["generatedTokens"] = int64(-1)
		}},
		{"totals_fresh_input_negative", contract.RuleTotalsFreshInputNonNegative, func(d map[string]any) {
			totals(d)["freshInputTokens"] = int64(-1)
		}},
		{"totals_input_total_negative", contract.RuleTotalsInputTotalNonNegative, func(d map[string]any) {
			totals(d)["inputTotal"] = int64(-1)
		}},
		{"totals_cache_read_negative", contract.RuleTotalsCacheReadNonNegative, func(d map[string]any) {
			totals(d)["cacheReadTokens"] = int64(-1)
		}},
		{"totals_cache_write_negative", contract.RuleTotalsCacheWriteNonNegative, func(d map[string]any) {
			totals(d)["cacheWriteTokens"] = int64(-1)
		}},
		{"totals_cost_negative", contract.RuleTotalsCostNonNegative, func(d map[string]any) {
			totals(d)["costUsd"] = int64(-1)
		}},
		{"totals_spend_negative", contract.RuleTotalsTotalSpendNonNegative, func(d map[string]any) {
			totals(d)["totalSpendUsd"] = int64(-1)
		}},
		{"totals_cost_nan", contract.RuleTotalsCostNonNegative, func(d map[string]any) {
			totals(d)["costUsd"] = math.NaN()
		}},
		{"totals_steps_string", contract.RuleTotalsStepsNonNegative, func(d map[string]any) {
			totals(d)["steps"] = "2"
		}},

		// Exact and close() sums.
		{"totals_input_sum", contract.RuleTotalsInputSum, func(d map[string]any) {
			totals(d)["inputTotal"] = int64(4)
		}},
		{"totals_steps_sum", contract.RuleTotalsStepsSum, func(d map[string]any) {
			byModelRow(d)["steps"] = int64(3)
		}},
		{"totals_generated_sum", contract.RuleTotalsGeneratedSum, func(d map[string]any) {
			byModelRow(d)["generatedTokens"] = int64(9)
		}},
		{"totals_cost_sum", contract.RuleTotalsCostSum, func(d map[string]any) {
			costModelRow(d)["recordedCost"] = 0.9
		}},
		{"cost_recorded", contract.RuleCostRecorded, func(d map[string]any) {
			cost(d)["recorded"] = 0.9
		}},
		{"totals_spend_sum", contract.RuleTotalsSpendSum, func(d map[string]any) {
			totals(d)["totalSpendUsd"] = 2.0
		}},
		{"cache_hit_rate", contract.RuleCacheHitRate, func(d map[string]any) {
			cache(d)["hitRate"] = 1.5
		}},
		{"cache_read_over_input", contract.RuleCacheReadLEInput, func(d map[string]any) {
			cache(d)["cacheReadTokens"] = int64(4)
		}},

		// Per-model cross-checks.
		{"by_model_cost_row", contract.RuleByModelCostRow, func(d map[string]any) {
			d["byModel"] = append(d["byModel"].([]any), map[string]any{
				"model":            "x",
				"steps":            int64(0),
				"generatedTokens":  int64(0),
				"freshInputTokens": int64(0),
				"inputTotal":       int64(0),
				"cacheReadTokens":  int64(0),
				"cacheWriteTokens": int64(0),
				"cacheHitRate":     0.0,
				"timedN":           int64(0),
			})
		}},
		{"by_model_input_sum", contract.RuleByModelInputSum, func(d map[string]any) {
			byModelRow(d)["inputTotal"] = int64(4)
		}},
		{"by_model_cache_hit_rate", contract.RuleByModelCacheHitRate, func(d map[string]any) {
			byModelRow(d)["cacheHitRate"] = 0.9
		}},
		{"by_model_input_total", contract.RuleByModelInputTotal, func(d map[string]any) {
			byModelRow(d)["inputTotal"] = 3.5
		}},
		{"by_model_generated_tokens", contract.RuleByModelGeneratedTokens, func(d map[string]any) {
			byModelRow(d)["generatedTokens"] = int64(900)
		}},

		// Timed coverage.
		{"timed_n_over_steps", contract.RuleTimedNLE, func(d map[string]any) {
			timedAll(d)["n"] = int64(3)
		}},
		{"timed_n_sum", contract.RuleTimedNSum, func(d map[string]any) {
			byModelRow(d)["timedN"] = int64(1)
		}},
		{"timed_plausible", contract.RuleTimedPlausible, func(d map[string]any) {
			timing(d)["plausible"] = int64(3)
		}},
		{"timed_max_included_negative", contract.RuleTimedMaxIncludedNonNegative, func(d map[string]any) {
			timedAll(d)["maxIncluded"] = int64(-1)
		}},
		{"timed_max_observed_negative", contract.RuleTimedMaxObservedNonNegative, func(d map[string]any) {
			timedAll(d)["maxObserved"] = int64(-1)
		}},

		// Session and project rollups.
		{"sessions_steps_sum", contract.RuleSessionsStepsSum, func(d map[string]any) {
			sessionRow(d)["steps"] = int64(1)
		}},
		{"sessions_cost_sum", contract.RuleSessionsCostSum, func(d map[string]any) {
			sessionRow(d)["costUsd"] = 0.5
		}},
		{"projects_steps_sum", contract.RuleProjectsStepsSum, func(d map[string]any) {
			projectRow(d)["steps"] = int64(1)
		}},
		{"projects_unique_labels", contract.RuleProjectsUniqueLabels, func(d map[string]any) {
			d["projects"] = append(d["projects"].([]any), map[string]any{"name": "p", "steps": int64(0)})
		}},
		{"projects_name_empty", contract.RuleProjectsName, func(d map[string]any) {
			projectRow(d)["name"] = ""
		}},
		{"projects_name_missing", contract.RuleProjectsName, func(d map[string]any) {
			delete(projectRow(d), "name")
		}},

		// Tool attribution.
		{"tools_share_sum", contract.RuleToolsShareSum, func(d map[string]any) {
			toolRow(d)["shareCalls"] = 0.5
		}},
		{"tools_share_range", contract.RuleToolsShareRange, func(d map[string]any) {
			toolRow(d)["shareCalls"] = 1.5
		}},
		{"tools_time_share_range", contract.RuleToolsTimeShareRange, func(d map[string]any) {
			toolRow(d)["timeShare"] = 1.5
		}},
		{"tools_time_share_sum", contract.RuleToolsTimeShareSum, func(d map[string]any) {
			d["tools"] = append(d["tools"].([]any), map[string]any{
				"tool": "u", "shareCalls": 0.0, "timeShare": 0.6,
			})
		}},

		// Daily and hourly rollups.
		{"daily_n_sum", contract.RuleDailyNSum, func(d map[string]any) {
			dailyRow(d)["nAll"] = int64(1)
		}},
		{"daily_generated_sum", contract.RuleDailyGeneratedSum, func(d map[string]any) {
			dailyRow(d)["generatedTokens"] = int64(9)
		}},
		{"daily_cost_sum", contract.RuleDailyCostSum, func(d map[string]any) {
			dailyRow(d)["costUsd"] = 0.9
		}},
		{"daily_spend_sum", contract.RuleDailySpendSum, func(d map[string]any) {
			dailyRow(d)["totalSpendUsd"] = 1.4
		}},
		{"hourly_spend_sum", contract.RuleHourlySpendSum, func(d map[string]any) {
			hourlyRow(d)["totalSpendUsd"] = 1.4
		}},
		{"hourly_n_sum", contract.RuleHourlyNSum, func(d map[string]any) {
			hourlyRow(d)["nAll"] = int64(1)
		}},
	}
}

// seriesMutations covers the 12 daily/hourly decision-metric rules.
func seriesMutations() []mutation {
	var out []mutation
	sections := []struct {
		name string
		row  func(map[string]any) map[string]any
	}{
		{"daily", dailyRow},
		{"hourly", hourlyRow},
	}
	keys := []string{"ratio", "ratioPct", "index"}
	for _, section := range sections {
		for _, key := range keys {
			section, key := section, key
			out = append(out,
				mutation{
					name: section.name + "_" + key + "_finite",
					rule: contract.SeriesRuleFinite(section.name, key),
					apply: func(d map[string]any) {
						section.row(d)[key] = math.Inf(1)
					},
				},
				mutation{
					name: section.name + "_" + key + "_nonnegative",
					rule: contract.SeriesRuleNonNegative(section.name, key),
					apply: func(d map[string]any) {
						section.row(d)[key] = -1.0
					},
				},
			)
		}
	}
	return out
}

func TestCommonRuleMutations(t *testing.T) {
	for _, test := range append(commonMutations(), seriesMutations()...) {
		test := test
		t.Run(test.name, func(t *testing.T) {
			data := report()
			test.apply(data)
			issues := contract.ValidateCommonIssues(data)
			if !issues.Has(test.rule) {
				t.Fatalf("expected rule %s, got\n%v", test.rule, issues.Strings())
			}
		})
	}
}

// Mutations must not be so broad that they break the fixture before the rule
// under test can be observed; the good report itself is the control.
func TestMutationsOnlyBreakTheirRule(t *testing.T) {
	for _, test := range commonMutations() {
		test := test
		t.Run(test.name, func(t *testing.T) {
			data := report()
			test.apply(data)
			issues := contract.ValidateCommonIssues(data)
			if len(issues) == 0 {
				t.Fatalf("mutation %s broke nothing", test.name)
			}
		})
	}
}
