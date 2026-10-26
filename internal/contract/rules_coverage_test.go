// SPDX-License-Identifier: AGPL-3.0-only

package contract_test

import (
	"testing"

	"github.com/jadmadi/agentmeter/internal/contract"
)

// allRuleIDs lists every rule id the package can report. It must stay in sync
// with the exported constants; the coverage test fails if a mutation table
// misses one.
func allRuleIDs() []string {
	rules := []string{
		contract.RuleSchema,
		contract.RuleSections,
		contract.RuleTotalsStepsNonNegative,
		contract.RuleTotalsGeneratedNonNegative,
		contract.RuleTotalsFreshInputNonNegative,
		contract.RuleTotalsInputTotalNonNegative,
		contract.RuleTotalsCacheReadNonNegative,
		contract.RuleTotalsCacheWriteNonNegative,
		contract.RuleTotalsCostNonNegative,
		contract.RuleTotalsTotalSpendNonNegative,
		contract.RuleTotalsInputSum,
		contract.RuleTotalsStepsSum,
		contract.RuleTotalsGeneratedSum,
		contract.RuleTotalsCostSum,
		contract.RuleTotalsSpendSum,
		contract.RuleCostRecorded,
		contract.RuleCacheHitRate,
		contract.RuleCacheReadLEInput,
		contract.RuleByModelCostRow,
		contract.RuleByModelInputSum,
		contract.RuleByModelCacheHitRate,
		contract.RuleByModelInputTotal,
		contract.RuleByModelGeneratedTokens,
		contract.RuleTimedNLE,
		contract.RuleTimedNSum,
		contract.RuleTimedPlausible,
		contract.RuleTimedMaxIncludedNonNegative,
		contract.RuleTimedMaxObservedNonNegative,
		contract.RuleSessionsStepsSum,
		contract.RuleSessionsGeneratedSum,
		contract.RuleSessionsCostSum,
		contract.RuleProjectsStepsSum,
		contract.RuleProjectsGeneratedSum,
		contract.RuleProjectsCostSum,
		contract.RuleProjectsUniqueLabels,
		contract.RuleProjectsName,
		contract.RuleToolsShareSum,
		contract.RuleToolsShareRange,
		contract.RuleToolsTimeShareRange,
		contract.RuleToolsTimeShareSum,
		contract.RuleDailyNSum,
		contract.RuleDailyTimedNSum,
		contract.RuleDailyGeneratedSum,
		contract.RuleDailyCostSum,
		contract.RuleDailySpendSum,
		contract.RuleHourlySpendSum,
		contract.RuleHourlyNSum,
		contract.RuleHourlyTimedNSum,
		contract.RuleHourlyGeneratedSum,
		contract.RuleHourlyCostSum,
		contract.RuleCmdStepsContainer,
		contract.RuleCmdStepTupleLength,
		contract.RuleCmdStepTimestamp,
		contract.RuleCmdStepTimingIndex,
		contract.RuleCmdStepLookupIndex,
		contract.RuleCmdStepTools,
		contract.RuleCmdLookupsContainer,
		contract.RuleCmdTimingReasons,
		contract.RuleOCStepsContainer,
		contract.RuleOCStepTupleLength,
		contract.RuleOCStepTimestamp,
		contract.RuleOCStepTimingIndex,
		contract.RuleOCStepLookupIndex,
		contract.RuleOCStepToolCalls,
		contract.RuleOCLookupsContainer,
		contract.RuleOCTimingReasons,
	}
	for _, section := range []string{"daily", "hourly"} {
		for _, key := range []string{"ratio", "ratioPct", "index"} {
			rules = append(rules,
				contract.SeriesRuleFinite(section, key),
				contract.SeriesRuleNonNegative(section, key),
			)
		}
	}
	return rules
}

func TestEveryRuleHasAMutation(t *testing.T) {
	covered := make(map[string]bool)
	add := func(test mutation) {
		if test.rule == "" {
			t.Errorf("mutation %q has no rule", test.name)
			return
		}
		covered[test.rule] = true
	}
	for _, test := range commonMutations() {
		add(test)
	}
	for _, test := range seriesMutations() {
		add(test)
	}
	for _, test := range cmdRuleMutations() {
		add(test)
	}
	for _, test := range ocRuleMutations() {
		add(test)
	}

	seen := make(map[string]bool)
	for _, rule := range allRuleIDs() {
		if seen[rule] {
			t.Errorf("duplicate rule id in allRuleIDs: %s", rule)
		}
		seen[rule] = true
		if !covered[rule] {
			t.Errorf("no mutation covers rule %s", rule)
		}
	}
	for rule := range covered {
		if !seen[rule] {
			t.Errorf("mutation targets unknown rule %s (add it to allRuleIDs)", rule)
		}
	}
}
