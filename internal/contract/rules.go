// SPDX-License-Identifier: AGPL-3.0-only

package contract

// Rule ids for the shared schema v3 invariants. They are part of the package
// API: tests and builders assert on ids, not on message text, so an id must
// never change meaning.
const (
	// RuleSchema is the schemaVersion check.
	RuleSchema = "common.schema"
	// RuleSections reports a section with the wrong JSON container type. The
	// Python validator would raise AttributeError/TypeError here; Go reports
	// it instead.
	RuleSections = "common.sections"

	// Totals non-negativity, one id per field.
	RuleTotalsStepsNonNegative      = "common.totals.steps.nonnegative"
	RuleTotalsGeneratedNonNegative  = "common.totals.generatedTokens.nonnegative"
	RuleTotalsFreshInputNonNegative = "common.totals.freshInputTokens.nonnegative"
	RuleTotalsInputTotalNonNegative = "common.totals.inputTotal.nonnegative"
	RuleTotalsCacheReadNonNegative  = "common.totals.cacheReadTokens.nonnegative"
	RuleTotalsCacheWriteNonNegative = "common.totals.cacheWriteTokens.nonnegative"
	RuleTotalsCostNonNegative       = "common.totals.costUsd.nonnegative"
	RuleTotalsTotalSpendNonNegative = "common.totals.totalSpendUsd.nonnegative"

	RuleTotalsInputSum     = "common.totals.input_sum"
	RuleTotalsStepsSum     = "common.totals.steps_sum"
	RuleTotalsGeneratedSum = "common.totals.generated_sum"
	RuleTotalsCostSum      = "common.totals.cost_sum"
	RuleTotalsSpendSum     = "common.totals.spend_sum"

	RuleCostRecorded = "common.cost.recorded"

	RuleCacheHitRate     = "common.cache.hit_rate"
	RuleCacheReadLEInput = "common.cache.read_le_input"

	RuleByModelCostRow         = "common.byModel.cost_row"
	RuleByModelInputSum        = "common.byModel.input_sum"
	RuleByModelCacheHitRate    = "common.byModel.cache_hit_rate"
	RuleByModelInputTotal      = "common.byModel.input_total"
	RuleByModelGeneratedTokens = "common.byModel.generated_tokens"

	RuleTimedNLE                    = "common.timed.n_le_steps"
	RuleTimedNSum                   = "common.timed.n_sum"
	RuleTimedPlausible              = "common.timed.plausible"
	RuleTimedMaxIncludedNonNegative = "common.timed.maxIncluded.nonnegative"
	RuleTimedMaxObservedNonNegative = "common.timed.maxObserved.nonnegative"

	RuleSessionsStepsSum     = "common.sessions.steps_sum"
	RuleSessionsGeneratedSum = "common.sessions.generated_sum"
	RuleSessionsCostSum      = "common.sessions.cost_sum"

	RuleProjectsStepsSum     = "common.projects.steps_sum"
	RuleProjectsGeneratedSum = "common.projects.generated_sum"
	RuleProjectsCostSum      = "common.projects.cost_sum"
	RuleProjectsUniqueLabels = "common.projects.unique_labels"
	RuleProjectsName         = "common.projects.name"

	RuleToolsShareSum       = "common.tools.share_sum"
	RuleToolsShareRange     = "common.tools.share_range"
	RuleToolsTimeShareRange = "common.tools.time_share_range"
	RuleToolsTimeShareSum   = "common.tools.time_share_sum"

	RuleDailyNSum          = "common.daily.n_sum"
	RuleDailyTimedNSum     = "common.daily.timed_n_sum"
	RuleDailyGeneratedSum  = "common.daily.generated_sum"
	RuleDailyCostSum       = "common.daily.cost_sum"
	RuleDailySpendSum      = "common.daily.spend_sum"
	RuleHourlySpendSum     = "common.hourly.spend_sum"
	RuleHourlyNSum         = "common.hourly.n_sum"
	RuleHourlyTimedNSum    = "common.hourly.timed_n_sum"
	RuleHourlyGeneratedSum = "common.hourly.generated_sum"
	RuleHourlyCostSum      = "common.hourly.cost_sum"

	// Command Code step-tuple rules.
	RuleCmdStepsContainer   = "cmd.steps.container"
	RuleCmdStepTupleLength  = "cmd.steps.tuple_length"
	RuleCmdStepTimestamp    = "cmd.steps.timestamp"
	RuleCmdStepTimingIndex  = "cmd.steps.timing_index"
	RuleCmdStepLookupIndex  = "cmd.steps.lookup_index"
	RuleCmdStepTools        = "cmd.steps.tools"
	RuleCmdLookupsContainer = "cmd.lookups.container"
	RuleCmdTimingReasons    = "cmd.lookups.timing_reasons"

	// Muse step-tuple rules. Muse tuples share the 20-slot
	// Command Code layout; the rule ids stay tool-scoped.
	RuleMuseStepsContainer   = "muse.steps.container"
	RuleMuseStepTupleLength  = "muse.steps.tuple_length"
	RuleMuseStepTimestamp    = "muse.steps.timestamp"
	RuleMuseStepTimingIndex  = "muse.steps.timing_index"
	RuleMuseStepLookupIndex  = "muse.steps.lookup_index"
	RuleMuseStepTools        = "muse.steps.tools"
	RuleMuseLookupsContainer = "muse.lookups.container"
	RuleMuseTimingReasons    = "muse.lookups.timing_reasons"

	// OpenCode step-tuple rules.
	RuleOCStepsContainer   = "oc.steps.container"
	RuleOCStepTupleLength  = "oc.steps.tuple_length"
	RuleOCStepTimestamp    = "oc.steps.timestamp"
	RuleOCStepTimingIndex  = "oc.steps.timing_index"
	RuleOCStepLookupIndex  = "oc.steps.lookup_index"
	RuleOCStepToolCalls    = "oc.steps.tool_calls"
	RuleOCLookupsContainer = "oc.lookups.container"
	RuleOCTimingReasons    = "oc.lookups.timing_reasons"
)

// SeriesRuleFinite is the rule id for the finiteness of a daily/hourly
// ratio/ratioPct/index value.
func SeriesRuleFinite(section, key string) string {
	return "common." + section + "." + key + ".finite"
}

// SeriesRuleNonNegative is the rule id for the non-negativity of a
// daily/hourly ratio/ratioPct/index value.
func SeriesRuleNonNegative(section, key string) string {
	return "common." + section + "." + key + ".nonnegative"
}
