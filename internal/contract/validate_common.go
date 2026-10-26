// SPDX-License-Identifier: AGPL-3.0-only

package contract

import (
	"math"
	"math/big"
)

// nonNegativeTotalsFields lists the totals fields validate_common requires to
// be finite non-negative numbers, in the Python validator's order.
var nonNegativeTotalsFields = []struct {
	field string
	rule  string
}{
	{"steps", RuleTotalsStepsNonNegative},
	{"generatedTokens", RuleTotalsGeneratedNonNegative},
	{"freshInputTokens", RuleTotalsFreshInputNonNegative},
	{"inputTotal", RuleTotalsInputTotalNonNegative},
	{"cacheReadTokens", RuleTotalsCacheReadNonNegative},
	{"cacheWriteTokens", RuleTotalsCacheWriteNonNegative},
	{"costUsd", RuleTotalsCostNonNegative},
	{"totalSpendUsd", RuleTotalsTotalSpendNonNegative},
}

// validateCommon is a line-by-line port of telemetry_common.validate_common
// (schema v3). Checks run in the Python order and rule ids are stable.
func validateCommon(c *checker, data map[string]any) {
	// Section access mirrors the Python `data.get(...) or {}` / `or []`.
	totals, totalsOK := ObjectSection(data["totals"])
	byModel, byModelOK := ListSection(data["byModel"])
	cost, costOK := ObjectSection(data["cost"])
	costByModel, costByModelOK := ListSection(cost["byModel"])
	cache, cacheOK := ObjectSection(data["cache"])
	timed, timedOK := ObjectSection(data["timed"])
	timedAll, timedAllOK := ObjectSection(timed["all"])
	sessions, sessionsOK := ListSection(data["sessions"])
	projects, projectsOK := ListSection(data["projects"])
	tools, toolsOK := ListSection(data["tools"])
	daily, dailyOK := ListSection(data["daily"])
	hourly, hourlyOK := ListSection(data["hourly"])
	meta, metaOK := ObjectSection(data["meta"])
	timing, _ := ObjectSection(meta["timing"])

	// schemaVersion must be 3.
	schemaVersion := numAt(data, "schemaVersion")
	c.check(RuleSchema, equalNumbers(schemaVersion, intNumber(SchemaVersion, SchemaVersion)),
		"schemaVersion must be %d", SchemaVersion)

	// Extension: malformed containers fail closed instead of panicking.
	for _, section := range []struct {
		name string
		ok   bool
		kind string
	}{
		{"meta", metaOK, "object"},
		{"totals", totalsOK, "object"},
		{"byModel", byModelOK, "array"},
		{"cost", costOK, "object"},
		{"cost.byModel", costByModelOK, "array"},
		{"cache", cacheOK, "object"},
		{"timed", timedOK, "object"},
		{"timed.all", timedAllOK, "object"},
		{"sessions", sessionsOK, "array"},
		{"projects", projectsOK, "array"},
		{"tools", toolsOK, "array"},
		{"daily", dailyOK, "array"},
		{"hourly", hourlyOK, "array"},
	} {
		c.check(RuleSections, section.ok, "%s must be a JSON %s", section.name, section.kind)
	}

	// Finite non-negative totals.
	for _, item := range nonNegativeTotalsFields {
		value := numAt(totals, item.field)
		c.check(item.rule, value.nonNegative(),
			"%s must be a finite non-negative number, got %s",
			"totals."+item.field, PyRepr(value.raw))
	}
	steps := numAt(totals, "steps")
	costUsd := numAt(totals, "costUsd")

	// totals.inputTotal == fresh + cacheRead + cacheWrite (exact).
	inputTotal := numAt(totals, "inputTotal")
	inputSum, inputSumOK := sumNumbers(
		numAt(totals, "freshInputTokens"),
		numAt(totals, "cacheReadTokens"),
		numAt(totals, "cacheWriteTokens"),
	)
	c.check(RuleTotalsInputSum, inputSumOK && exactSumMatch(inputTotal, inputSum),
		"totals.inputTotal != fresh + cacheRead + cacheWrite")

	// sum(byModel.steps) == totals.steps (exact).
	modelSteps, modelStepsOK := sumFieldDefaultZero(byModel, "steps")
	c.check(RuleTotalsStepsSum, modelStepsOK && exactSumMatch(steps, modelSteps),
		"sum(byModel.steps) != totals.steps")

	// close(totals.generatedTokens, sum(byModel.generatedTokens)).
	modelGenerated, modelGeneratedOK := sumFieldDefaultZero(byModel, "generatedTokens")
	c.check(RuleTotalsGeneratedSum,
		modelGeneratedOK && closeNumberSum(numAt(totals, "generatedTokens"), modelGenerated, 1e-6),
		"sum(byModel.generatedTokens) != totals.generatedTokens")

	// close(totals.costUsd, sum(cost.byModel.recordedCost or costUsd)).
	recordedSum, recordedSumOK := sumFieldChain(costByModel, "recordedCost", "costUsd")
	c.check(RuleTotalsCostSum, recordedSumOK && closeNumberSum(costUsd, recordedSum, 1e-6),
		"sum(cost.byModel.recordedCost) != totals.costUsd")

	// close(cost.recorded (default 0), totals.costUsd).
	recorded, recordedOK := getNum(cost, "recorded")
	c.check(RuleCostRecorded, recordedOK && closeNumbers(recorded, costUsd, 1e-6),
		"cost.recorded != totals.costUsd")

	// close(totals.totalSpendUsd, totals.costUsd + cost.errorCostUsd (default 0)).
	errorCost, errorCostOK := getNum(cost, "errorCostUsd")
	spendSum, spendSumOK := sumNumbers(costUsd, errorCost)
	c.check(RuleTotalsSpendSum,
		errorCostOK && spendSumOK && closeNumberSum(numAt(totals, "totalSpendUsd"), spendSum, 1e-6),
		"totalSpendUsd != costUsd + errorCostUsd")

	// 0 <= (cache.hitRate or 0) <= 1.
	hitRate := numAt(cache, "hitRate")
	if hitRate.set {
		c.check(RuleCacheHitRate, inUnitInterval(hitRate), "cache.hitRate outside [0, 1]")
	}

	// cache.cacheReadTokens (default 0) <= cache.inputTokens (default 0).
	cacheRead, cacheReadOK := getNum(cache, "cacheReadTokens")
	cacheInput, cacheInputOK := getNum(cache, "inputTokens")
	c.check(RuleCacheReadLEInput, cacheReadOK && cacheInputOK && lessOrEqual(cacheRead, cacheInput),
		"cache.cacheReadTokens > cache.inputTokens")

	// Per-model cross-checks between byModel and cost.byModel.
	costByModelIndex := make(map[string]map[string]any, len(costByModel))
	for _, row := range costByModel {
		costByModelIndex[keyOf(row["model"])] = row
	}
	for _, row := range byModel {
		model := row["model"]
		costRow, found := costByModelIndex[keyOf(model)]
		c.check(RuleByModelCostRow, found, "missing cost row for model %s", PyRepr(model))
		if !found {
			continue
		}
		rowInput := numAt(row, "inputTotal")
		rowInputSum, rowInputOK := sumNumbers(
			numAt(row, "freshInputTokens"),
			numAt(row, "cacheReadTokens"),
			numAt(row, "cacheWriteTokens"),
		)
		c.check(RuleByModelInputSum, rowInputOK && exactSumMatch(rowInput, rowInputSum),
			"byModel[%s].inputTotal != fresh + cacheRead + cacheWrite", PyStr(model))
		c.check(RuleByModelCacheHitRate,
			closeNumbers(numAt(row, "cacheHitRate"), numAt(costRow, "cacheHitRate"), 1e-6),
			"byModel[%s].cacheHitRate != cost.byModel cacheHitRate", PyStr(model))
		c.check(RuleByModelInputTotal,
			closeNumbers(rowInput, numAt(costRow, "inputTotal"), 1e-6),
			"byModel[%s].inputTotal != cost.byModel.inputTotal", PyStr(model))
		c.check(RuleByModelGeneratedTokens,
			closeNumbers(numAt(row, "generatedTokens"), numAt(costRow, "generatedTokens"), 1e-6),
			"byModel[%s].generatedTokens != cost.byModel.generatedTokens", PyStr(model))
	}

	// Timed coverage.
	timedN, timedNOK := getNum(timedAll, "n")
	c.check(RuleTimedNLE, timedNOK && lessOrEqual(timedN, steps),
		"timed.all.n > totals.steps")
	modelTimedN, modelTimedNOK := sumFieldDefaultZero(byModel, "timedN")
	c.check(RuleTimedNSum, timedNOK && modelTimedNOK && exactSumMatch(timedN, modelTimedN),
		"timed.all.n != sum(byModel.timedN)")
	if _, present := timing["plausible"]; present {
		c.check(RuleTimedPlausible, timedNOK && equalNumbers(timedN, numAt(timing, "plausible")),
			"timed.all.n != meta.timing.plausible")
	}
	if !timedNOK || timedN.truthy() {
		maxIncluded := numAt(timedAll, "maxIncluded")
		c.check(RuleTimedMaxIncludedNonNegative, maxIncluded.nonNegative(),
			"%s must be a finite non-negative number, got %s",
			"timed.all.maxIncluded", PyRepr(maxIncluded.raw))
		maxObserved := numAt(timedAll, "maxObserved")
		c.check(RuleTimedMaxObservedNonNegative, maxObserved.nonNegative(),
			"%s must be a finite non-negative number, got %s",
			"timed.all.maxObserved", PyRepr(maxObserved.raw))
	}

	// Session and project rollups.
	sessionSteps, sessionStepsOK := sumFieldDefaultZero(sessions, "steps")
	c.check(RuleSessionsStepsSum, sessionStepsOK && closeSumNumber(sessionSteps, steps, 1e-6),
		"sum(session.steps) != totals.steps")
	sessionGenerated, sessionGeneratedOK := sumFieldDefaultZero(sessions, "generatedTokens")
	c.check(RuleSessionsGeneratedSum,
		sessionGeneratedOK && closeSumNumber(sessionGenerated, numAt(totals, "generatedTokens"), 1e-6),
		"sum(session.generatedTokens) != totals.generatedTokens")
	sessionCost, sessionCostOK := sumFieldDefaultZero(sessions, "costUsd")
	c.check(RuleSessionsCostSum, sessionCostOK && closeSumNumber(sessionCost, costUsd, 1e-3),
		"sum(session.costUsd) != totals.costUsd")

	projectSteps, projectStepsOK := sumFieldDefaultZero(projects, "steps")
	c.check(RuleProjectsStepsSum, projectStepsOK && exactSumMatch(steps, projectSteps),
		"sum(project.steps) != totals.steps")
	projectGenerated, projectGeneratedOK := sumFieldDefaultZero(projects, "generatedTokens")
	c.check(RuleProjectsGeneratedSum,
		projectGeneratedOK && closeSumNumber(projectGenerated, numAt(totals, "generatedTokens"), 1e-6),
		"sum(project.generatedTokens) != totals.generatedTokens")
	projectCost, projectCostOK := sumFieldDefaultZero(projects, "costUsd")
	c.check(RuleProjectsCostSum, projectCostOK && closeSumNumber(projectCost, costUsd, 1e-3),
		"sum(project.costUsd) != totals.costUsd")
	labels := make(map[string]struct{}, len(projects))
	uniqueLabels := true
	for _, project := range projects {
		key := keyOf(project["name"])
		if _, duplicate := labels[key]; duplicate {
			uniqueLabels = false
		}
		labels[key] = struct{}{}
	}
	c.check(RuleProjectsUniqueLabels, uniqueLabels, "project labels are not unique")
	for _, project := range projects {
		name, isString := StringValue(project["name"])
		c.check(RuleProjectsName, isString && name != "", "project name is missing or empty")
	}

	// Tool attribution.
	if len(tools) > 0 {
		shareTotal, shareTotalOK := sumFieldDefaultZero(tools, "shareCalls")
		c.check(RuleToolsShareSum, shareTotalOK && closeSumFloat(shareTotal, 1.0, 1e-6),
			"sum(tool.shareCalls) != 1")
		for _, tool := range tools {
			share, shareOK := getNumOrZero(tool, "shareCalls")
			c.check(RuleToolsShareRange, shareOK && inUnitInterval(share),
				"tool %s shareCalls outside [0, 1]", PyRepr(tool["tool"]))
			timeShare := numAt(tool, "timeShare")
			if timeShare.set {
				c.check(RuleToolsTimeShareRange, inUnitInterval(timeShare),
					"tool %s timeShare outside [0, 1]", PyRepr(tool["tool"]))
			}
		}
		timeTotal, timeTotalOK := sumFieldNilAsZero(tools, "timeShare")
		timeSum, timeSumOK := rationalToFloat(timeTotal)
		c.check(RuleToolsTimeShareSum, timeTotalOK && timeSumOK && timeSum <= 1.0+1e-6,
			"sum(tool.timeShare) > 1")
	}

	// Daily and hourly series.
	dailyN, dailyNOK := sumFieldDefaultZero(daily, "nAll")
	c.check(RuleDailyNSum, dailyNOK && exactSumMatch(steps, dailyN),
		"sum(daily.nAll) != totals.steps")
	dailyTimedN, dailyTimedNOK := sumFieldDefaultZero(daily, "nTimed")
	c.check(RuleDailyTimedNSum, timedNOK && dailyTimedNOK && exactSumMatch(timedN, dailyTimedN),
		"sum(daily.nTimed) != timed.all.n")
	dailyGenerated, dailyGeneratedOK := sumFieldDefaultZero(daily, "generatedTokens")
	c.check(RuleDailyGeneratedSum,
		dailyGeneratedOK && closeSumNumber(dailyGenerated, numAt(totals, "generatedTokens"), 1e-6),
		"sum(daily.generatedTokens) != totals.generatedTokens")
	dailyCost, dailyCostOK := sumFieldDefaultZero(daily, "costUsd")
	c.check(RuleDailyCostSum, dailyCostOK && closeSumNumber(dailyCost, costUsd, 1e-4),
		"sum(daily.costUsd) != totals.costUsd")

	undatedErrorCost, undatedOK := getNum(cost, "undatedErrorCostUsd")
	dailySpend, dailySpendOK := sumFieldDefaultZero(daily, "totalSpendUsd")
	if dailySpendOK && undatedOK && undatedErrorCost.r != nil {
		dailySpend.Add(dailySpend, undatedErrorCost.r)
	} else {
		dailySpendOK = false
	}
	c.check(RuleDailySpendSum,
		dailySpendOK && closeSumNumber(dailySpend, numAt(totals, "totalSpendUsd"), 1e-3),
		"sum(daily.totalSpendUsd) + undated error cost != totals.totalSpendUsd")

	hourlySpend, hourlySpendOK := sumFieldDefaultZero(hourly, "totalSpendUsd")
	if hourlySpendOK && undatedOK && undatedErrorCost.r != nil {
		hourlySpend.Add(hourlySpend, undatedErrorCost.r)
	} else {
		hourlySpendOK = false
	}
	c.check(RuleHourlySpendSum,
		hourlySpendOK && closeSumNumber(hourlySpend, numAt(totals, "totalSpendUsd"), 1e-3),
		"sum(hourly.totalSpendUsd) + undated error cost != totals.totalSpendUsd")

	hourlyN, hourlyNOK := sumFieldDefaultZero(hourly, "nAll")
	c.check(RuleHourlyNSum, hourlyNOK && exactSumMatch(steps, hourlyN),
		"sum(hourly.nAll) != totals.steps")
	hourlyTimedN, hourlyTimedNOK := sumFieldDefaultZero(hourly, "nTimed")
	c.check(RuleHourlyTimedNSum, timedNOK && hourlyTimedNOK && exactSumMatch(timedN, hourlyTimedN),
		"sum(hourly.nTimed) != timed.all.n")
	hourlyGenerated, hourlyGeneratedOK := sumFieldDefaultZero(hourly, "generatedTokens")
	c.check(RuleHourlyGeneratedSum,
		hourlyGeneratedOK && closeSumNumber(hourlyGenerated, numAt(totals, "generatedTokens"), 1e-6),
		"sum(hourly.generatedTokens) != totals.generatedTokens")
	hourlyCost, hourlyCostOK := sumFieldDefaultZero(hourly, "costUsd")
	c.check(RuleHourlyCostSum, hourlyCostOK && closeSumNumber(hourlyCost, costUsd, 1e-4),
		"sum(hourly.costUsd) != totals.costUsd")

	// Daily/hourly decision metrics must be finite and non-negative when
	// present (Python: row.get(key) is not None).
	for _, section := range []struct {
		name string
		rows []map[string]any
	}{
		{"daily", daily},
		{"hourly", hourly},
	} {
		for _, row := range section.rows {
			for _, key := range []string{"ratio", "ratioPct", "index"} {
				value := numAt(row, key)
				if !value.set {
					continue
				}
				c.check(SeriesRuleFinite(section.name, key), value.finite(),
					"%s.%s is not finite", section.name, key)
				c.check(SeriesRuleNonNegative(section.name, key), value.nonNegative(),
					"%s.%s must be a finite non-negative number, got %s",
					section.name, key, PyRepr(value.raw))
			}
		}
	}
}

// exactSumMatch reports n == sum exactly.
func exactSumMatch(n number, sum *big.Rat) bool {
	if !n.valid() || n.r == nil || sum == nil {
		return false
	}
	return n.r.Cmp(sum) == 0
}

// closeNumberSum ports close(a, sum) where the tolerance bound is derived from
// the numeric right-hand side (the sum).
func closeNumberSum(a number, sum *big.Rat, tol float64) bool {
	if !a.valid() || !a.finite() || sum == nil {
		return false
	}
	sumFloat, sumFloatOK := rationalToFloat(sum)
	if !sumFloatOK {
		return false
	}
	bound := math.Max(math.Abs(tol), math.Abs(tol*sumFloat))
	if !finiteFloat(bound) {
		return false
	}
	if a.isPyInt && a.r != nil && sum.IsInt() {
		diff := new(big.Rat).Sub(a.r, sum)
		diff.Abs(diff)
		return diff.Cmp(new(big.Rat).SetFloat64(bound)) <= 0
	}
	return math.Abs(a.f-sumFloat) <= bound
}

// closeSumNumber ports close(sum, b) where the tolerance bound comes from b.
func closeSumNumber(sum *big.Rat, b number, tol float64) bool {
	if sum == nil || !b.valid() || !b.finite() {
		return false
	}
	bound := math.Max(math.Abs(tol), math.Abs(tol*b.f))
	if !finiteFloat(bound) {
		return false
	}
	if b.isPyInt && b.r != nil && sum.IsInt() {
		diff := new(big.Rat).Sub(sum, b.r)
		diff.Abs(diff)
		return diff.Cmp(new(big.Rat).SetFloat64(bound)) <= 0
	}
	sumFloat, sumFloatOK := rationalToFloat(sum)
	if !sumFloatOK {
		return false
	}
	return math.Abs(sumFloat-b.f) <= bound
}

// closeSumFloat ports close(sum, b) for a constant float bound such as 1.0.
func closeSumFloat(sum *big.Rat, b float64, tol float64) bool {
	if sum == nil || !finiteFloat(b) {
		return false
	}
	bound := math.Max(math.Abs(tol), math.Abs(tol*b))
	if !finiteFloat(bound) {
		return false
	}
	if sum.IsInt() {
		diff := new(big.Rat).Sub(sum, new(big.Rat).SetFloat64(b))
		diff.Abs(diff)
		return diff.Cmp(new(big.Rat).SetFloat64(bound)) <= 0
	}
	sumFloat, sumFloatOK := rationalToFloat(sum)
	if !sumFloatOK {
		return false
	}
	return math.Abs(sumFloat-b) <= bound
}

// rationalToFloat converts a rational to float64, reporting false for values
// that are not finite floats.
func rationalToFloat(r *big.Rat) (float64, bool) {
	if r == nil {
		return 0, false
	}
	f, _ := r.Float64()
	if !finiteFloat(f) {
		return 0, false
	}
	return f, true
}

// inUnitInterval reports 0 <= n <= 1.
func inUnitInterval(n number) bool {
	if !n.valid() || n.r == nil {
		return false
	}
	return n.r.Sign() >= 0 && n.r.Cmp(new(big.Rat).SetInt64(1)) <= 0
}
