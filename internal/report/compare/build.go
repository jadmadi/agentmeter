// SPDX-License-Identifier: AGPL-3.0-only

// Package compare builds the cross-tool comparison document from two schema
// v3 reports. It is a read-only aggregation: inputs are never modified, no
// network is used, and the output is validated before it is returned.
package compare

import (
	"fmt"
	"math"
	"sort"
	"time"

	shared "github.com/jadmadi/agentmeter/internal/compare"
	"github.com/jadmadi/agentmeter/internal/pyjson"
)

const (
	// tieRelative is the relative difference treated as a statistical tie.
	tieRelative = 0.05
	// cacheTiePp is the cache percentage-point tie band.
	cacheTiePp = 1.0
	// lowSampleN is the timed-step count below which a row is low-sample.
	lowSampleN = 30
)

func roundRatio(value *float64, digits int) any {
	if value == nil {
		return nil
	}
	return shared.Round(*value, digits)
}

func spendBasis(agg *toolAggregate) string {
	if !agg.spendComplete {
		return "recorded-only"
	}
	if agg.window.Mode == "overlap" {
		return "window-daily"
	}
	return "full-report"
}

func toAnyStrings(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

// Build aggregates one OpenCode and one Command Code report into the
// comparison document. The returned map is the exact JSON payload written to
// comparison-data.json, and mirrors the ordering the web dashboard expects.
func Build(oc, cmd *shared.Report, now time.Time) (map[string]any, error) {
	if oc == nil || cmd == nil {
		return nil, fmt.Errorf("compare: both reports are required")
	}
	window := resolveWindow(oc, cmd)
	ocAgg := aggregate(oc, window)
	cmdAgg := aggregate(cmd, window)
	families := summarizeFamilies(oc, cmd)
	ocShared := aggregateFamilies(oc, window, families.Set)
	cmdShared := aggregateFamilies(cmd, window, families.Set)
	defaultScope := "all"
	if likeForLikeAvailableFor(ocShared, cmdShared, families) {
		defaultScope = "shared"
	}

	doc := map[string]any{
		"schemaVersion":     shared.SchemaVersion,
		"comparisonVersion": shared.ComparisonVersion,
		"kind":              "comparison",
		"meta":              metaSection(oc, cmd, window, dataFamilyCounts(ocAgg, cmdAgg), families, defaultScope, now),
		"totals":            totalsSection(ocAgg, cmdAgg),
		"throughput":        throughputSection(ocAgg, cmdAgg),
		"cost":              costSection(ocAgg, cmdAgg),
		"cache":             cacheSection(ocAgg, cmdAgg),
		"tools":             toolsSection(ocAgg, cmdAgg),
		"models":            modelsSection(ocAgg, cmdAgg),
		"strata":            strataSection(ocAgg, cmdAgg),
		"sessions":          sessionsSection(ocAgg, cmdAgg),
		"timeline":          timelineSection(ocAgg, cmdAgg, window),
		"likeForLike":       likeForLikeSection(ocShared, cmdShared, families),
		"verdicts": map[string]any{
			"shared": verdictsSection(ocShared, cmdShared, "shared", families, ocAgg, cmdAgg),
			"all":    verdictsSection(ocAgg, cmdAgg, "all", families, ocAgg, cmdAgg),
		},
	}

	if issues := validateComparison(doc, ocAgg, cmdAgg, ocShared, cmdShared); len(issues) > 0 {
		return nil, fmt.Errorf("compare: comparison invariant failure:\n- %s", joinIssues(issues))
	}
	return doc, nil
}

// ---- meta

func metaSection(
	oc, cmd *shared.Report,
	window comparisonWindow,
	dataFamilies int,
	families familySummary,
	defaultScope string,
	now time.Time,
) map[string]any {
	partial := make([]any, 0, 4)
	if oc.Runtime == nil {
		partial = append(partial, "OpenCode runtime metrics are unavailable in this report")
	}
	if cmd.Runtime == nil {
		partial = append(partial, "Command Code records no per-step wall clock: active/idle minutes and runtime overhead are unavailable")
	}
	if hasModelWithoutVisible(cmd) {
		partial = append(partial, "Command Code outputTokens includes reasoning; visible-output splits are unavailable")
	}
	if window.Mode == "disjoint" {
		partial = append(partial, "The two sources have no overlapping dates; the comparison covers each tool's full range")
	}
	if window.Mode == "single" {
		partial = append(partial, "Only one source has dated telemetry; the comparison covers its range")
	}
	if oc.Timezone != "" && cmd.Timezone != "" && oc.Timezone != cmd.Timezone {
		partial = append(partial, fmt.Sprintf(
			"Sources use different local timezones (%s vs %s); window dates are compared as report-local dates",
			oc.Timezone, cmd.Timezone,
		))
	}
	if oc.Cost.UndatedErrorCostUsd != 0 || cmd.Cost.UndatedErrorCostUsd != 0 {
		scope := "excluded from overlap-window spend"
		if window.Mode != "overlap" {
			scope = "included in full-range spend"
		}
		partial = append(partial, fmt.Sprintf(
			"Undated ledger cost cannot be windowed (oc $%.6f, cmd $%.6f); %s",
			oc.Cost.UndatedErrorCostUsd, cmd.Cost.UndatedErrorCostUsd, scope,
		))
	}
	timezone := oc.Timezone
	if timezone == "" {
		timezone = cmd.Timezone
	}
	if oc.Timezone != cmd.Timezone {
		timezone = "mixed"
	}
	if timezone == "" {
		timezone = "UTC"
	}
	return map[string]any{
		"generatedAt":       NowUTC(now),
		"comparisonVersion": shared.ComparisonVersion,
		"schemaVersion":     shared.SchemaVersion,
		"kind":              "comparison",
		"timezone":          timezone,
		"tools": map[string]any{
			"oc":  toolInfo(oc),
			"cmd": toolInfo(cmd),
		},
		"window": map[string]any{
			"mode": window.Mode,
			"from": optionalString(window.From),
			"to":   optionalString(window.To),
			"days": window.Days,
			"oc":   map[string]any{"from": optionalString(window.OCFrom), "to": optionalString(window.OCTO)},
			"cmd":  map[string]any{"from": optionalString(window.CMDFrom), "to": optionalString(window.CMDTo)},
		},
		"canonical": map[string]any{
			"generatedTokens": "visible output plus reasoning where the source separates it",
			"inputTotal":      "freshInputTokens + cacheReadTokens + cacheWriteTokens",
			"speed":           "generatedTokens / plausible timed LLM seconds",
			"costEfficiency":  "recorded cost per 1,000 generated tokens",
			"toolShare":       "equal-share call attribution over every call in a step",
			"strata":          "turn type is pooled; output-size buckets 20-100, 100-300, 300-1k, 1k-5k, 5k+",
		},
		"families": map[string]any{
			"shared":        families.SharedFamilies,
			"ocOnly":        families.OCOnlyFamilies,
			"cmdOnly":       families.CMDOnlyFamilies,
			"withDataInWin": dataFamilies,
		},
		"models": map[string]any{
			"oc":        families.OCModels,
			"cmd":       families.CMDModels,
			"sharedOC":  families.SharedOCModels,
			"sharedCMD": families.SharedCMDModels,
		},
		"defaultScope":   defaultScope,
		"sharedFamilies": toAnyStrings(families.Shared),
		"notes": []any{
			"Speed and cost differences mostly reflect provider and model routing, not the agent harness.",
			"Like-for-like scope uses only model families present in both tools; the all-model aggregate is context, not a harness verdict.",
			"Spend per session compares different session counts and task mixes; it is not a per-task cost.",
			"Per-completed-task cost is unavailable: Command Code records no session outcome, so spend per session is the closest operational unit.",
			"Family counts and model counts are different units: a shared family can contain several models per tool. Windowed model counts can be smaller than source model counts.",
			"Ratios are OpenCode / Command Code. Speed ratios above 1 favor OpenCode; cost ratios below 1 favor OpenCode.",
			"Ties are <5% relative. Cache hit-rate ties are <1 percentage point or <5% relative.",
		},
		"partialMetrics": partial,
	}
}

func toolInfo(report *shared.Report) map[string]any {
	return map[string]any{
		"label":            report.Input.Label,
		"kind":             report.Input.Kind,
		"path":             report.Input.Path,
		"generatedAt":      optionalString(report.Generated),
		"timezone":         optionalString(report.Timezone),
		"dateFrom":         optionalString(report.DateFrom),
		"dateTo":           optionalString(report.DateTo),
		"models":           report.ModelsN,
		"sessions":         report.SessionsN,
		"steps":            report.Totals.Steps,
		"schemaVersion":    shared.SchemaVersion,
		"runtimeAvailable": report.Runtime != nil,
	}
}

func hasModelWithoutVisible(report *shared.Report) bool {
	for _, step := range report.Steps {
		if step.Visible < 0 {
			return true
		}
	}
	return false
}

func dataFamilyCounts(oc, cmd *toolAggregate) int {
	set := make(map[string]struct{})
	for family := range oc.families {
		set[family] = struct{}{}
	}
	for family := range cmd.families {
		set[family] = struct{}{}
	}
	return len(set)
}

// familySummary separates family counts from model counts. A family can hold
// several models per tool, which is why "shared + oc-only" does not equal the
// model total.
type familySummary struct {
	SharedFamilies  int
	OCOnlyFamilies  int
	CMDOnlyFamilies int
	OCModels        int
	CMDModels       int
	SharedOCModels  int
	SharedCMDModels int
	Shared          []string
	Set             map[string]struct{}
}

func summarizeFamilies(oc, cmd *shared.Report) familySummary {
	summary := familySummary{Set: make(map[string]struct{})}
	ocByFamily := make(map[string]int)
	cmdByFamily := make(map[string]int)
	for _, model := range oc.Models {
		ocByFamily[model.Family]++
	}
	for _, model := range cmd.Models {
		cmdByFamily[model.Family]++
	}
	summary.OCModels = len(oc.Models)
	summary.CMDModels = len(cmd.Models)
	for family, count := range ocByFamily {
		if cmdCount, ok := cmdByFamily[family]; ok {
			summary.SharedFamilies++
			summary.SharedOCModels += count
			summary.SharedCMDModels += cmdCount
			summary.Shared = append(summary.Shared, family)
			summary.Set[family] = struct{}{}
		} else {
			summary.OCOnlyFamilies++
		}
	}
	for family := range cmdByFamily {
		if _, ok := ocByFamily[family]; !ok {
			summary.CMDOnlyFamilies++
		}
	}
	sort.Strings(summary.Shared)
	return summary
}

// ---- totals

func totalsSection(oc, cmd *toolAggregate) map[string]any {
	ocTotals := totalsCell(oc)
	cmdTotals := totalsCell(cmd)
	ocHit := optFloat(ocTotals, "cacheHitRate")
	cmdHit := optFloat(cmdTotals, "cacheHitRate")
	return map[string]any{
		"oc":  ocTotals,
		"cmd": cmdTotals,
		"delta": map[string]any{
			"stepsPct":             roundDelta(shared.PercentDelta(floatPtr(oc.totals.Steps), floatPtr(cmd.totals.Steps)), 1),
			"stepsRatio":           roundRatio(shared.RatioDelta(floatPtr(oc.totals.Steps), floatPtr(cmd.totals.Steps)), 2),
			"sessionsRatio":        roundRatio(shared.RatioDelta(floatPtr(oc.totals.Sessions), floatPtr(cmd.totals.Sessions)), 2),
			"toolCallsPct":         roundDelta(shared.PercentDelta(floatPtr(oc.totals.ToolCalls), floatPtr(cmd.totals.ToolCalls)), 1),
			"toolCallsRatio":       roundRatio(shared.RatioDelta(floatPtr(oc.totals.ToolCalls), floatPtr(cmd.totals.ToolCalls)), 2),
			"generatedTokensPct":   roundDelta(shared.PercentDelta(floatPtr(oc.totals.GeneratedTokens), floatPtr(cmd.totals.GeneratedTokens)), 1),
			"generatedTokensRatio": roundRatio(shared.RatioDelta(floatPtr(oc.totals.GeneratedTokens), floatPtr(cmd.totals.GeneratedTokens)), 2),
			"inputTotalPct":        roundDelta(shared.PercentDelta(floatPtr(oc.totals.InputTotal), floatPtr(cmd.totals.InputTotal)), 1),
			"inputTotalRatio":      roundRatio(shared.RatioDelta(floatPtr(oc.totals.InputTotal), floatPtr(cmd.totals.InputTotal)), 2),
			"costUsdPct":           roundDelta(shared.PercentDelta(&oc.cost, &cmd.cost), 1),
			"costUsdRatio":         roundRatio(shared.RatioDelta(&oc.cost, &cmd.cost), 2),
			"totalSpendUsdPct":     roundDelta(shared.PercentDelta(&oc.totalSpend, &cmd.totalSpend), 1),
			"totalSpendUsdRatio":   roundRatio(shared.RatioDelta(&oc.totalSpend, &cmd.totalSpend), 2),
			"cacheHitRatePp":       roundDelta(shared.PointDelta(ocHit, cmdHit), 2),
			"cacheHitRateRatio":    roundRatio(shared.RatioDelta(ocHit, cmdHit), 4),
		},
	}
}

func totalsCell(agg *toolAggregate) map[string]any {
	hit := shared.R4(shared.Rate(agg.totals.CacheRead, agg.totals.InputTotal))
	spendBasis := "full-report"
	if !agg.spendComplete {
		spendBasis = "recorded-only"
	} else if agg.window.Mode == "overlap" {
		spendBasis = "window-daily"
	}
	return map[string]any{
		"steps":               agg.totals.Steps,
		"toolSteps":           agg.totals.ToolSteps,
		"toolCalls":           agg.totals.ToolCalls,
		"generatedTokens":     agg.totals.GeneratedTokens,
		"inputTotal":          agg.totals.InputTotal,
		"cacheReadTokens":     agg.totals.CacheRead,
		"cacheWriteTokens":    agg.totals.CacheWrite,
		"cacheHitRate":        anyOrNil(hit),
		"costUsd":             shared.Round(agg.cost, 6),
		"errorCostUsd":        shared.Round(maxFloat(agg.errorCost, 0), 6),
		"undatedErrorCostUsd": shared.Round(agg.report.Cost.UndatedErrorCostUsd, 6),
		"totalSpendUsd":       shared.Round(agg.totalSpend, 6),
		"spendBasis":          spendBasis,
		"models":              agg.totals.Models,
		"sessions":            agg.totals.Sessions,
	}
}

// ---- throughput

func throughputSection(oc, cmd *toolAggregate) map[string]any {
	ocSummary := oc.timed.summary()
	cmdSummary := cmd.timed.summary()
	return map[string]any{
		"oc":  timedCell(ocSummary),
		"cmd": timedCell(cmdSummary),
		"delta": map[string]any{
			"nPct":          roundDelta(shared.PercentDelta(floatPtr(ocSummary.N), floatPtr(cmdSummary.N)), 1),
			"nRatio":        roundRatio(shared.RatioDelta(floatPtr(ocSummary.N), floatPtr(cmdSummary.N)), 2),
			"tokensPct":     roundDelta(shared.PercentDelta(floatPtr(ocSummary.Tokens), floatPtr(cmdSummary.Tokens)), 1),
			"tokensRatio":   roundRatio(shared.RatioDelta(floatPtr(ocSummary.Tokens), floatPtr(cmdSummary.Tokens)), 2),
			"secondsPct":    roundDelta(shared.PercentDelta(&ocSummary.Seconds, &cmdSummary.Seconds), 1),
			"secondsRatio":  roundRatio(shared.RatioDelta(&ocSummary.Seconds, &cmdSummary.Seconds), 2),
			"weightedPct":   roundDelta(shared.PercentDelta(ocSummary.Weighted, cmdSummary.Weighted), 1),
			"weightedRatio": roundRatio(shared.RatioDelta(ocSummary.Weighted, cmdSummary.Weighted), 2),
			"medianPct":     roundDelta(shared.PercentDelta(ocSummary.Median, cmdSummary.Median), 1),
			"medianRatio":   roundRatio(shared.RatioDelta(ocSummary.Median, cmdSummary.Median), 2),
			"p90Pct":        roundDelta(shared.PercentDelta(ocSummary.P90, cmdSummary.P90), 1),
			"p90Ratio":      roundRatio(shared.RatioDelta(ocSummary.P90, cmdSummary.P90), 2),
		},
		"winner": map[string]any{
			"weighted": shared.WinnerWithTolerance(ocSummary.Weighted, cmdSummary.Weighted, true, tieRelative),
			"median":   shared.WinnerWithTolerance(ocSummary.Median, cmdSummary.Median, true, tieRelative),
		},
	}
}

func timedCell(summary shared.TimedSummary) map[string]any {
	return map[string]any{
		"n":        summary.N,
		"tokens":   summary.Tokens,
		"seconds":  summary.Seconds,
		"weighted": anyOrNil(summary.Weighted),
		"median":   anyOrNil(summary.Median),
		"p10":      anyOrNil(summary.P10),
		"p25":      anyOrNil(summary.P25),
		"p75":      anyOrNil(summary.P75),
		"p90":      anyOrNil(summary.P90),
		"p99":      anyOrNil(summary.P99),
		"max":      anyOrNil(summary.Max),
	}
}

// ---- cost

func costSection(oc, cmd *toolAggregate) map[string]any {
	ocCost := costCell(oc)
	cmdCost := costCell(cmd)
	ocPer1k := optFloat(ocCost, "per1kGenerated")
	cmdPer1k := optFloat(cmdCost, "per1kGenerated")
	ocPerStep := optFloat(ocCost, "perStep")
	cmdPerStep := optFloat(cmdCost, "perStep")
	ocPerSession := optFloat(ocCost, "costPerSession")
	cmdPerSession := optFloat(cmdCost, "costPerSession")
	ocPerInput := optFloat(ocCost, "perMillionInput")
	cmdPerInput := optFloat(cmdCost, "perMillionInput")
	ocSpendSession := optFloat(ocCost, "totalSpendPerSession")
	cmdSpendSession := optFloat(cmdCost, "totalSpendPerSession")
	return map[string]any{
		"oc":  ocCost,
		"cmd": cmdCost,
		"delta": map[string]any{
			"recordedPct":               roundDelta(shared.PercentDelta(floatPtrFloat(oc.cost), floatPtrFloat(cmd.cost)), 1),
			"recordedRatio":             roundRatio(shared.RatioDelta(floatPtrFloat(oc.cost), floatPtrFloat(cmd.cost)), 2),
			"totalSpendUsdPct":          roundDelta(shared.PercentDelta(&oc.totalSpend, &cmd.totalSpend), 1),
			"totalSpendUsdRatio":        roundRatio(shared.RatioDelta(&oc.totalSpend, &cmd.totalSpend), 2),
			"perStepPct":                roundDelta(shared.PercentDelta(ocPerStep, cmdPerStep), 1),
			"perStepRatio":              roundRatio(shared.RatioDelta(ocPerStep, cmdPerStep), 2),
			"perMillionGeneratedPct":    roundDelta(shared.PercentDelta(optFloat(ocCost, "perMillionGenerated"), optFloat(cmdCost, "perMillionGenerated")), 1),
			"perMillionGeneratedRatio":  roundRatio(shared.RatioDelta(optFloat(ocCost, "perMillionGenerated"), optFloat(cmdCost, "perMillionGenerated")), 2),
			"per1kGeneratedPct":         roundDelta(shared.PercentDelta(ocPer1k, cmdPer1k), 1),
			"per1kGeneratedRatio":       roundRatio(shared.RatioDelta(ocPer1k, cmdPer1k), 2),
			"perMillionInputPct":        roundDelta(shared.PercentDelta(ocPerInput, cmdPerInput), 1),
			"perMillionInputRatio":      roundRatio(shared.RatioDelta(ocPerInput, cmdPerInput), 2),
			"costPerSessionPct":         roundDelta(shared.PercentDelta(ocPerSession, cmdPerSession), 1),
			"costPerSessionRatio":       roundRatio(shared.RatioDelta(ocPerSession, cmdPerSession), 2),
			"totalSpendPerSessionPct":   roundDelta(shared.PercentDelta(ocSpendSession, cmdSpendSession), 1),
			"totalSpendPerSessionRatio": roundRatio(shared.RatioDelta(ocSpendSession, cmdSpendSession), 2),
			"perToolCallRatio":          roundRatio(shared.RatioDelta(optFloat(ocCost, "perToolCall"), optFloat(cmdCost, "perToolCall")), 2),
		},
		"winner": map[string]any{
			"per1kGenerated":       shared.WinnerWithTolerance(ocPer1k, cmdPer1k, false, tieRelative),
			"perMillionInput":      shared.WinnerWithTolerance(ocPerInput, cmdPerInput, false, tieRelative),
			"perStep":              shared.WinnerWithTolerance(ocPerStep, cmdPerStep, false, tieRelative),
			"costPerSession":       shared.WinnerWithTolerance(ocPerSession, cmdPerSession, false, tieRelative),
			"totalSpendPerSession": shared.WinnerWithTolerance(ocSpendSession, cmdSpendSession, false, tieRelative),
			"totalSpendUsd":        shared.WinnerWithTolerance(&oc.totalSpend, &cmd.totalSpend, false, tieRelative),
		},
	}
}

func costCell(agg *toolAggregate) map[string]any {
	perStep := shared.R4(shared.DivPtr(agg.cost, float64(agg.totals.Steps)))
	perMillion := shared.R2(shared.PerMillion(agg.cost, agg.totals.GeneratedTokens))
	per1k := shared.R4(shared.PerKThousand(agg.cost, agg.totals.GeneratedTokens))
	perSession := shared.R4(shared.DivPtr(agg.cost, float64(agg.totals.Sessions)))
	perMillionInput := shared.R2(shared.PerMillion(agg.cost, agg.totals.InputTotal))
	perToolCall := shared.R4(shared.DivPtr(agg.cost, float64(agg.totals.ToolCalls)))
	spendPerSession := shared.R4(shared.DivPtr(agg.totalSpend, float64(agg.totals.Sessions)))
	return map[string]any{
		"recorded":             shared.Round(agg.cost, 6),
		"errorCostUsd":         shared.Round(maxFloat(agg.errorCost, 0), 6),
		"totalSpendUsd":        shared.Round(agg.totalSpend, 6),
		"perStep":              anyOrNil(perStep),
		"perMillionGenerated":  anyOrNil(perMillion),
		"per1kGenerated":       anyOrNil(per1k),
		"perMillionInput":      anyOrNil(perMillionInput),
		"perToolCall":          anyOrNil(perToolCall),
		"costPerSession":       anyOrNil(perSession),
		"totalSpendPerSession": anyOrNil(spendPerSession),
		"spendBasis":           spendBasis(agg),
	}
}

// ---- cache

func cacheSection(oc, cmd *toolAggregate) map[string]any {
	ocCache := cacheCell(oc)
	cmdCache := cacheCell(cmd)
	ocHit := optFloat(ocCache, "hitRate")
	cmdHit := optFloat(cmdCache, "hitRate")
	ocWrite := optFloat(ocCache, "writeShare")
	cmdWrite := optFloat(cmdCache, "writeShare")
	return map[string]any{
		"oc":  ocCache,
		"cmd": cmdCache,
		"delta": map[string]any{
			"hitRatePp":       roundDelta(shared.PointDelta(ocHit, cmdHit), 2),
			"hitRateRatio":    roundRatio(shared.RatioDelta(ocHit, cmdHit), 4),
			"writeSharePp":    roundDelta(shared.PointDelta(ocWrite, cmdWrite), 2),
			"writeShareRatio": roundRatio(shared.RatioDelta(ocWrite, cmdWrite), 4),
			"savingsPct":      roundDelta(shared.PercentDelta(floatPtrFloat(oc.report.Cache.SavingsUsd), floatPtrFloat(cmd.report.Cache.SavingsUsd)), 1),
			"savingsRatio":    roundRatio(shared.RatioDelta(floatPtrFloat(oc.report.Cache.SavingsUsd), floatPtrFloat(cmd.report.Cache.SavingsUsd)), 2),
		},
		"winner": map[string]any{
			"hitRate": shared.WinnerCacheRate(ocHit, cmdHit, cacheTiePp, tieRelative),
		},
		"tieThresholdPp": cacheTiePp,
	}
}

func cacheCell(agg *toolAggregate) map[string]any {
	hit := shared.R4(shared.Rate(agg.totals.CacheRead, agg.totals.InputTotal))
	write := shared.R4(shared.Rate(agg.totals.CacheWrite, agg.totals.InputTotal))
	savings := agg.report.Cache.SavingsUsd
	savingsPerMillion := shared.R2(shared.PerMillion(savings, agg.totals.GeneratedTokens))
	return map[string]any{
		"inputTotal":                 agg.totals.InputTotal,
		"cacheReadTokens":            agg.totals.CacheRead,
		"cacheWriteTokens":           agg.totals.CacheWrite,
		"generatedTokens":            agg.totals.GeneratedTokens,
		"hitRate":                    anyOrNil(hit),
		"writeShare":                 anyOrNil(write),
		"savings":                    shared.Round(savings, 6),
		"savingsPerMillionGenerated": anyOrNil(savingsPerMillion),
		"sourceReportSavings":        true,
	}
}

// ---- tools

func toolsSection(oc, cmd *toolAggregate) map[string]any {
	ocRaw := toolCells(oc)
	cmdRaw := toolCells(cmd)
	ocTotal := oc.totals.ToolCalls
	cmdTotal := cmd.totals.ToolCalls

	categoryNames := make(map[string]struct{})
	for name := range oc.category {
		categoryNames[name] = struct{}{}
	}
	for name := range cmd.category {
		categoryNames[name] = struct{}{}
	}
	categories := make([]any, 0, len(categoryNames))
	ocShareSum, cmdShareSum := 0.0, 0.0
	for name := range categoryNames {
		ocCall := oc.category[name]
		cmdCall := cmd.category[name]
		ocShare := callShare(ocCall, ocTotal)
		cmdShare := callShare(cmdCall, cmdTotal)
		if ocShare != nil {
			ocShareSum += *ocShare
		}
		if cmdShare != nil {
			cmdShareSum += *cmdShare
		}
		categories = append(categories, map[string]any{
			"category": name,
			"oc":       callCell(ocCall, ocTotal),
			"cmd":      callCell(cmdCall, cmdTotal),
			"delta": map[string]any{
				"sharePp":      roundDelta(shared.PointDelta(ocShare, cmdShare), 2),
				"shareRatio":   roundRatio(shared.RatioDelta(ocShare, cmdShare), 2),
				"callsPct":     roundDelta(shared.PercentDelta(callCallsPtr(ocCall), callCallsPtr(cmdCall)), 1),
				"callsRatio":   roundRatio(shared.RatioDelta(callCallsPtr(ocCall), callCallsPtr(cmdCall)), 2),
				"generatedPct": roundDelta(shared.PercentDelta(callGeneratedPtr(ocCall), callGeneratedPtr(cmdCall)), 1),
			},
		})
	}
	sort.SliceStable(categories, func(i, j int) bool {
		iCalls := cellCalls(categories[i])
		jCalls := cellCalls(categories[j])
		if iCalls != jCalls {
			return iCalls > jCalls
		}
		return categoryName(categories[i]) < categoryName(categories[j])
	})

	return map[string]any{
		"oc":         ocRaw,
		"cmd":        cmdRaw,
		"normalized": categories,
		"totals": map[string]any{
			"oc":         ocTotal,
			"cmd":        cmdTotal,
			"deltaPct":   roundDelta(shared.PercentDelta(floatPtr(ocTotal), floatPtr(cmdTotal)), 1),
			"ratio":      roundRatio(shared.RatioDelta(floatPtr(ocTotal), floatPtr(cmdTotal)), 2),
			"categories": len(categories),
		},
		"coverage": map[string]any{
			"oc":  anyOrNil(shared.R4(&ocShareSum)),
			"cmd": anyOrNil(shared.R4(&cmdShareSum)),
		},
		"runtimeComparable": ocRuntimeComparable(oc) && cmdRuntimeComparable(cmd),
	}
}

func toolCells(agg *toolAggregate) []any {
	items := sortedCallAggregates(agg.tools)
	cells := make([]any, 0, len(items))
	for _, stat := range items {
		share := callShare(stat, agg.totals.ToolCalls)
		weighted := shared.R1(shared.WeightedRate(int64(stat.timedGenerated), stat.timedSeconds))
		medianTps := shared.R1(shared.MedianOf(stat.rates))
		costsPerCall := shared.R6(shared.DivPtr(stat.cost, float64(stat.calls)))
		cells = append(cells, map[string]any{
			"tool":            stat.name,
			"calls":           stat.calls,
			"stepsWithTool":   int64(len(stat.steps)),
			"shareCalls":      anyOrNil(share),
			"generatedTokens": shared.Round(stat.generated, 1),
			"costUsd":         shared.Round(stat.cost, 6),
			"timedCalls":      stat.timedCalls,
			"weighted":        anyOrNil(weighted),
			"medianTps":       anyOrNil(medianTps),
			"costPerCall":     anyOrNil(costsPerCall),
		})
	}
	return cells
}

func callCell(stat *callAggregate, total int64) map[string]any {
	if stat == nil {
		return map[string]any{
			"calls":           int64(0),
			"shareCalls":      nil,
			"generatedTokens": float64(0),
			"costUsd":         float64(0),
			"timedCalls":      int64(0),
			"weighted":        nil,
			"medianTps":       nil,
		}
	}
	return map[string]any{
		"calls":           stat.calls,
		"shareCalls":      anyOrNil(callShare(stat, total)),
		"generatedTokens": shared.Round(stat.generated, 1),
		"costUsd":         shared.Round(stat.cost, 6),
		"timedCalls":      stat.timedCalls,
		"weighted":        anyOrNil(shared.R1(shared.WeightedRate(int64(stat.timedGenerated), stat.timedSeconds))),
		"medianTps":       anyOrNil(shared.R1(shared.MedianOf(stat.rates))),
	}
}

func callShare(stat *callAggregate, total int64) *float64 {
	if stat == nil || total <= 0 {
		return nil
	}
	value := float64(stat.calls) / float64(total)
	return &value
}

func callCallsPtr(stat *callAggregate) *float64 {
	if stat == nil {
		return nil
	}
	return floatPtr(stat.calls)
}

func callGeneratedPtr(stat *callAggregate) *float64 {
	if stat == nil {
		return nil
	}
	value := stat.generated
	return &value
}

func cellCalls(entry any) int64 {
	cell := entry.(map[string]any)
	if side, ok := cell["oc"].(map[string]any); ok {
		if calls, ok := side["calls"].(int64); ok {
			return calls
		}
	}
	return 0
}

func categoryName(entry any) string {
	cell := entry.(map[string]any)
	name, _ := cell["category"].(string)
	return name
}

func ocRuntimeComparable(agg *toolAggregate) bool {
	for _, tool := range agg.report.Tools {
		if tool.TimeShare != nil {
			return true
		}
	}
	return false
}

func cmdRuntimeComparable(agg *toolAggregate) bool {
	return agg.report.Runtime != nil
}

// ---- models

func modelsSection(oc, cmd *toolAggregate) map[string]any {
	familyNames := sortedFamilies(oc, cmd)
	ocPer1kValues := make([]float64, 0, len(familyNames))
	cmdPer1kValues := make([]float64, 0, len(familyNames))
	ocSpeedValues := make([]float64, 0, len(familyNames))
	cmdSpeedValues := make([]float64, 0, len(familyNames))
	for _, family := range familyNames {
		if !familyShared(oc, cmd, family) {
			continue
		}
		ocCell := familyCell(oc.families[family], modelIDs(oc.report, family))
		cmdCell := familyCell(cmd.families[family], modelIDs(cmd.report, family))
		if value := optFloat(ocCell, "per1kGenerated"); value != nil {
			ocPer1kValues = append(ocPer1kValues, *value)
		}
		if value := optFloat(cmdCell, "per1kGenerated"); value != nil {
			cmdPer1kValues = append(cmdPer1kValues, *value)
		}
		if value := optFloat(ocCell, "weighted"); value != nil {
			ocSpeedValues = append(ocSpeedValues, *value)
		}
		if value := optFloat(cmdCell, "weighted"); value != nil {
			cmdSpeedValues = append(cmdSpeedValues, *value)
		}
	}
	ocPer1kMedian := shared.MedianOf(ocPer1kValues)
	cmdPer1kMedian := shared.MedianOf(cmdPer1kValues)
	ocSpeedMedian := shared.MedianOf(ocSpeedValues)
	cmdSpeedMedian := shared.MedianOf(cmdSpeedValues)

	families := make([]any, 0, len(familyNames))
	for _, family := range familyNames {
		ocFamily := oc.families[family]
		cmdFamily := cmd.families[family]
		ocModels := modelIDs(oc.report, family)
		cmdModels := modelIDs(cmd.report, family)
		sharedFamily := len(ocModels) > 0 && len(cmdModels) > 0
		ocCell := familyCell(ocFamily, ocModels)
		cmdCell := familyCell(cmdFamily, cmdModels)
		ocWeighted := optFloat(ocCell, "weighted")
		cmdWeighted := optFloat(cmdCell, "weighted")
		ocMedian := optFloat(ocCell, "median")
		cmdMedian := optFloat(cmdCell, "median")
		ocPer1k := optFloat(ocCell, "per1kGenerated")
		cmdPer1k := optFloat(cmdCell, "per1kGenerated")
		ocTimedN := int64Value(optFloat(ocCell, "timedN"))
		cmdTimedN := int64Value(optFloat(cmdCell, "timedN"))

		flags := make([]any, 0, 3)
		if !sharedFamily {
			flags = append(flags, "one-sided")
		}
		lowSample := sharedFamily && (ocTimedN < lowSampleN || cmdTimedN < lowSampleN)
		if lowSample {
			flags = append(flags, "low-sample")
		}
		if sharedFamily && (isOutlier(ocPer1k, ocPer1kMedian, 2.0) || isOutlier(cmdPer1k, cmdPer1kMedian, 2.0)) {
			flags = append(flags, "cost-outlier")
		}
		if sharedFamily && (isOutlier(ocWeighted, ocSpeedMedian, 3.0) || isOutlier(cmdWeighted, cmdSpeedMedian, 3.0)) {
			flags = append(flags, "speed-outlier")
		}

		families = append(families, map[string]any{
			"family": family,
			"shared": sharedFamily,
			"n": map[string]any{
				"oc":  ocTimedN,
				"cmd": cmdTimedN,
			},
			"flags":      flags,
			"confidence": familyConfidence(sharedFamily, ocWeighted, cmdWeighted, ocTimedN, cmdTimedN),
			"oc":         ocCell,
			"cmd":        cmdCell,
			"delta": map[string]any{
				"stepsPct":             roundDelta(shared.PercentDelta(optFloat(ocCell, "steps"), optFloat(cmdCell, "steps")), 1),
				"stepsRatio":           roundRatio(shared.RatioDelta(optFloat(ocCell, "steps"), optFloat(cmdCell, "steps")), 2),
				"generatedTokensPct":   roundDelta(shared.PercentDelta(optFloat(ocCell, "generatedTokens"), optFloat(cmdCell, "generatedTokens")), 1),
				"generatedTokensRatio": roundRatio(shared.RatioDelta(optFloat(ocCell, "generatedTokens"), optFloat(cmdCell, "generatedTokens")), 2),
				"weightedPct":          roundDelta(shared.PercentDelta(ocWeighted, cmdWeighted), 1),
				"weightedRatio":        roundRatio(shared.RatioDelta(ocWeighted, cmdWeighted), 2),
				"medianPct":            roundDelta(shared.PercentDelta(ocMedian, cmdMedian), 1),
				"medianRatio":          roundRatio(shared.RatioDelta(ocMedian, cmdMedian), 2),
				"p90Pct":               roundDelta(shared.PercentDelta(optFloat(ocCell, "p90"), optFloat(cmdCell, "p90")), 1),
				"p90Ratio":             roundRatio(shared.RatioDelta(optFloat(ocCell, "p90"), optFloat(cmdCell, "p90")), 2),
				"per1kGeneratedPct":    roundDelta(shared.PercentDelta(ocPer1k, cmdPer1k), 1),
				"per1kGeneratedRatio":  roundRatio(shared.RatioDelta(ocPer1k, cmdPer1k), 2),
				"cacheHitRatePp":       roundDelta(shared.PointDelta(optFloat(ocCell, "cacheHitRate"), optFloat(cmdCell, "cacheHitRate")), 2),
				"cacheHitRateRatio":    roundRatio(shared.RatioDelta(optFloat(ocCell, "cacheHitRate"), optFloat(cmdCell, "cacheHitRate")), 4),
			},
			"winner": map[string]any{
				"weighted":       shared.WinnerWithTolerance(ocWeighted, cmdWeighted, true, tieRelative),
				"median":         shared.WinnerWithTolerance(ocMedian, cmdMedian, true, tieRelative),
				"per1kGenerated": shared.WinnerWithTolerance(ocPer1k, cmdPer1k, false, tieRelative),
			},
		})
	}
	return map[string]any{
		"oc":       modelCells(oc),
		"cmd":      modelCells(cmd),
		"families": families,
	}
}

func int64Value(value *float64) int64 {
	if value == nil {
		return 0
	}
	return int64(*value)
}

func isOutlier(value, median *float64, factor float64) bool {
	if value == nil || median == nil || *median == 0 || *value == 0 {
		return false
	}
	ratio := *value / *median
	return ratio > factor || ratio < 1/factor
}

func familyConfidence(sharedFamily bool, ocWeighted, cmdWeighted *float64, ocN, cmdN int64) string {
	if !sharedFamily {
		return "one-sided"
	}
	ratio := shared.RatioDelta(ocWeighted, cmdWeighted)
	if ratio == nil {
		return "n/a"
	}
	relative := math.Abs(*ratio - 1)
	if relative < tieRelative {
		return "tie"
	}
	if ocN < lowSampleN || cmdN < lowSampleN {
		return "low-sample"
	}
	if relative < 0.20 {
		return "weak"
	}
	return "clear"
}

func modelCells(agg *toolAggregate) []any {
	models := make([]*modelAggregate, 0, len(agg.models))
	for _, model := range agg.models {
		models = append(models, model)
	}
	sort.SliceStable(models, func(i, j int) bool {
		if models[i].generated != models[j].generated {
			return models[i].generated > models[j].generated
		}
		return models[i].model.Model < models[j].model.Model
	})
	cells := make([]any, 0, len(models))
	for _, model := range models {
		hit := shared.R4(shared.Rate(model.cacheRead, model.inputTotal))
		per1k := shared.R4(shared.PerKThousand(model.cost, model.generated))
		perInput := shared.R2(shared.PerMillion(model.cost, model.inputTotal))
		perStep := shared.R4(shared.DivPtr(model.cost, float64(model.steps)))
		summary := model.timed.summary()
		cells = append(cells, map[string]any{
			"model":            model.model.Model,
			"short":            model.model.Short,
			"provider":         model.model.Provider,
			"family":           model.model.Family,
			"steps":            model.steps,
			"toolSteps":        model.toolSteps,
			"generatedTokens":  model.generated,
			"inputTotal":       model.inputTotal,
			"cacheReadTokens":  model.cacheRead,
			"cacheWriteTokens": model.cacheWrite,
			"cacheHitRate":     anyOrNil(hit),
			"costUsd":          shared.Round(model.cost, 6),
			"timedN":           summary.N,
			"weighted":         anyOrNil(summary.Weighted),
			"median":           anyOrNil(summary.Median),
			"p10":              anyOrNil(summary.P10),
			"p25":              anyOrNil(summary.P25),
			"p75":              anyOrNil(summary.P75),
			"p90":              anyOrNil(summary.P90),
			"per1kGenerated":   anyOrNil(per1k),
			"perMillionInput":  anyOrNil(perInput),
			"perStep":          anyOrNil(perStep),
		})
	}
	return cells
}

func familyCell(agg *familyAggregate, models []string) map[string]any {
	modelValues := make([]any, 0, len(models))
	for _, id := range models {
		modelValues = append(modelValues, id)
	}
	if agg == nil {
		return map[string]any{
			"models":          modelValues,
			"steps":           int64(0),
			"toolSteps":       int64(0),
			"generatedTokens": int64(0),
			"inputTotal":      int64(0),
			"cacheReadTokens": int64(0),
			"cacheHitRate":    nil,
			"costUsd":         float64(0),
			"per1kGenerated":  nil,
			"perMillionInput": nil,
			"perStep":         nil,
			"timedN":          int64(0),
			"weighted":        nil,
			"median":          nil,
			"p10":             nil,
			"p25":             nil,
			"p75":             nil,
			"p90":             nil,
		}
	}
	hit := shared.R4(shared.Rate(agg.cacheRead, agg.inputTotal))
	per1k := shared.R4(shared.PerKThousand(agg.cost, agg.generated))
	perInput := shared.R2(shared.PerMillion(agg.cost, agg.inputTotal))
	perStep := shared.R4(shared.DivPtr(agg.cost, float64(agg.steps)))
	summary := agg.timed.summary()
	return map[string]any{
		"models":          modelValues,
		"steps":           agg.steps,
		"toolSteps":       agg.toolSteps,
		"generatedTokens": agg.generated,
		"inputTotal":      agg.inputTotal,
		"cacheReadTokens": agg.cacheRead,
		"cacheHitRate":    anyOrNil(hit),
		"costUsd":         shared.Round(agg.cost, 6),
		"per1kGenerated":  anyOrNil(per1k),
		"perMillionInput": anyOrNil(perInput),
		"perStep":         anyOrNil(perStep),
		"timedN":          summary.N,
		"weighted":        anyOrNil(summary.Weighted),
		"median":          anyOrNil(summary.Median),
		"p10":             anyOrNil(summary.P10),
		"p25":             anyOrNil(summary.P25),
		"p75":             anyOrNil(summary.P75),
		"p90":             anyOrNil(summary.P90),
	}
}

// ---- strata

func strataSection(oc, cmd *toolAggregate) map[string]any {
	bucketLabels := make([]any, 0, len(shared.StrataBuckets))
	pooled := make([]any, 0, len(shared.StrataBuckets))
	for _, bucket := range shared.StrataBuckets {
		bucketLabels = append(bucketLabels, bucket.Label)
		ocBucket := oc.pooled[bucket.Label]
		cmdBucket := cmd.pooled[bucket.Label]
		ocCell := bucketCell(ocBucket)
		cmdCell := bucketCell(cmdBucket)
		pooled = append(pooled, map[string]any{
			"bucket": bucket.Label,
			"oc":     ocCell,
			"cmd":    cmdCell,
			"delta": map[string]any{
				"weightedPct":   roundDelta(shared.PercentDelta(optFloat(ocCell, "weighted"), optFloat(cmdCell, "weighted")), 1),
				"weightedRatio": roundRatio(shared.RatioDelta(optFloat(ocCell, "weighted"), optFloat(cmdCell, "weighted")), 2),
				"medianPct":     roundDelta(shared.PercentDelta(optFloat(ocCell, "median"), optFloat(cmdCell, "median")), 1),
				"medianRatio":   roundRatio(shared.RatioDelta(optFloat(ocCell, "median"), optFloat(cmdCell, "median")), 2),
			},
			"lowSample": bucketLowSample(ocCell, cmdCell),
		})
	}

	sharedFamilies := make([]any, 0, len(oc.families))
	for _, family := range sortedFamilies(oc, cmd) {
		if !familyShared(oc, cmd, family) {
			continue
		}
		buckets := make([]any, 0, len(shared.StrataBuckets))
		for _, bucket := range shared.StrataBuckets {
			ocBucket := familyBucket(oc, family, bucket.Label)
			cmdBucket := familyBucket(cmd, family, bucket.Label)
			ocCell := bucketCell(ocBucket)
			cmdCell := bucketCell(cmdBucket)
			deltaWeighted := roundDelta(shared.PercentDelta(optFloat(ocCell, "weighted"), optFloat(cmdCell, "weighted")), 1)
			buckets = append(buckets, map[string]any{
				"bucket": bucket.Label,
				"oc":     ocCell,
				"cmd":    cmdCell,
				"delta": map[string]any{
					"weightedPct":   deltaWeighted,
					"weightedRatio": roundRatio(shared.RatioDelta(optFloat(ocCell, "weighted"), optFloat(cmdCell, "weighted")), 2),
					"medianPct":     roundDelta(shared.PercentDelta(optFloat(ocCell, "median"), optFloat(cmdCell, "median")), 1),
					"medianRatio":   roundRatio(shared.RatioDelta(optFloat(ocCell, "median"), optFloat(cmdCell, "median")), 2),
				},
				"lowSample": bucketLowSample(ocCell, cmdCell),
			})
		}
		sharedFamilies = append(sharedFamilies, map[string]any{
			"family":  family,
			"buckets": buckets,
		})
	}
	return map[string]any{
		"buckets":  bucketLabels,
		"pooled":   pooled,
		"families": sharedFamilies,
	}
}

func familyBucket(agg *toolAggregate, family, label string) *bucketAggregate {
	result := &bucketAggregate{label: label}
	hasData := false
	for _, step := range agg.steps {
		if step.Family != family || !step.Timed || step.Duration == nil {
			continue
		}
		if bucket, ok := shared.BucketFor(step.Generated); !ok || bucket != label {
			continue
		}
		hasData = true
		result.add(step.Generated, *step.Duration)
	}
	if !hasData {
		return nil
	}
	return result
}

func bucketCell(bucket *bucketAggregate) map[string]any {
	if bucket == nil {
		return map[string]any{
			"n":        int64(0),
			"tokens":   int64(0),
			"seconds":  float64(0),
			"weighted": nil,
			"median":   nil,
			"p90":      nil,
		}
	}
	return bucket.summary()
}

func bucketLowSample(oc, cmd map[string]any) bool {
	ocN := oc["n"].(int64)
	cmdN := cmd["n"].(int64)
	return ocN < 5 || cmdN < 5
}

// ---- sessions

func sessionsSection(oc, cmd *toolAggregate) map[string]any {
	ocCell := sessionCell(oc)
	cmdCell := sessionCell(cmd)
	return map[string]any{
		"oc":  ocCell,
		"cmd": cmdCell,
		"delta": map[string]any{
			"spanMinutesPct":         roundDelta(shared.PercentDelta(optFloat(ocCell, "spanMinutesMedian"), optFloat(cmdCell, "spanMinutesMedian")), 1),
			"stepsPerSessionPct":     roundDelta(shared.PercentDelta(optFloat(ocCell, "stepsPerSession"), optFloat(cmdCell, "stepsPerSession")), 1),
			"generatedPerSessionPct": roundDelta(shared.PercentDelta(optFloat(ocCell, "generatedPerSession"), optFloat(cmdCell, "generatedPerSession")), 1),
			"outputPerMinutePct":     roundDelta(shared.PercentDelta(optFloat(ocCell, "outputPerMinuteMedian"), optFloat(cmdCell, "outputPerMinuteMedian")), 1),
			"costPerSessionPct":      roundDelta(shared.PercentDelta(optFloat(ocCell, "costPerSession"), optFloat(cmdCell, "costPerSession")), 1),
		},
		"overhead": map[string]any{
			"oc":  ocOverhead(oc),
			"cmd": cmdOverhead(cmd),
		},
		"winner": map[string]any{
			"outputPerMinute": shared.Winner(optFloat(ocCell, "outputPerMinuteMedian"), optFloat(cmdCell, "outputPerMinuteMedian"), true),
		},
	}
}

func sessionCell(agg *toolAggregate) map[string]any {
	sessions := make([]*sessionAggregate, 0, len(agg.sessions))
	for _, session := range agg.sessions {
		sessions = append(sessions, session)
	}
	spans := make([]float64, 0, len(sessions))
	actives := make([]float64, 0, len(sessions))
	idles := make([]float64, 0, len(sessions))
	outputs := make([]float64, 0, len(sessions))
	for _, session := range sessions {
		spans = append(spans, session.spanMinutes())
		if active := session.activeMinutes(); active != nil {
			actives = append(actives, *active)
		}
		if idle := session.idleMinutes(); idle != nil {
			idles = append(idles, *idle)
		}
		if perMinute, ok := shared.Ratio(float64(session.generated), session.spanMinutes()); ok {
			outputs = append(outputs, perMinute)
		}
	}
	stepsPerSession := shared.R4(shared.DivPtr(float64(agg.totals.Steps), float64(len(sessions))))
	generatedPerSession := shared.R1(shared.DivPtr(float64(agg.totals.GeneratedTokens), float64(len(sessions))))
	costPerSession := shared.R4(shared.DivPtr(agg.cost, float64(len(sessions))))
	return map[string]any{
		"sessions":              int64(len(sessions)),
		"stepsPerSession":       anyOrNil(stepsPerSession),
		"generatedPerSession":   anyOrNil(generatedPerSession),
		"costPerSession":        anyOrNil(costPerSession),
		"spanMinutesMedian":     anyOrNil(shared.R1(shared.MedianOf(spans))),
		"spanMinutesP90":        anyOrNil(shared.R1(shared.PercentileOf(spans, 90))),
		"activeMinutesMedian":   anyOrNil(shared.R1(shared.MedianOf(actives))),
		"idleMinutesMedian":     anyOrNil(shared.R1(shared.MedianOf(idles))),
		"outputPerMinuteMedian": anyOrNil(shared.R1(shared.MedianOf(outputs))),
		"runtimeAvailable":      len(actives) > 0,
	}
}

func ocOverhead(agg *toolAggregate) map[string]any {
	overheads := make([]float64, 0, len(agg.steps))
	totalDT := make([]float64, 0, len(agg.steps))
	medians := make([]float64, 0, len(agg.steps))
	for _, step := range agg.steps {
		if step.TotalDT == nil {
			continue
		}
		totalDT = append(totalDT, *step.TotalDT)
		medians = append(medians, *step.TotalDT)
		if step.Duration != nil {
			overhead := *step.TotalDT - *step.Duration
			if overhead < 0 {
				overhead = 0
			}
			overheads = append(overheads, overhead)
		}
	}
	share := (*float64)(nil)
	if sum := sumFloats(totalDT); sum > 0 {
		value := sumFloats(overheads) / sum
		share = &value
	}
	return map[string]any{
		"runtimeAvailable":      true,
		"medianOverheadSeconds": anyOrNil(shared.R2(shared.MedianOf(overheads))),
		"p90OverheadSeconds":    anyOrNil(shared.R2(shared.PercentileOf(overheads, 90))),
		"overheadShare":         anyOrNil(shared.R4(share)),
		"medianStepSeconds":     anyOrNil(shared.R2(shared.MedianOf(medians))),
	}
}

func cmdOverhead(agg *toolAggregate) map[string]any {
	return map[string]any{
		"runtimeAvailable":      false,
		"medianOverheadSeconds": nil,
		"p90OverheadSeconds":    nil,
		"overheadShare":         nil,
		"medianStepSeconds":     nil,
		"reason":                "Command Code transcripts do not record per-step wall clock",
	}
}

// ---- timeline

func timelineSection(oc, cmd *toolAggregate, window comparisonWindow) map[string]any {
	ocDays := dailyMap(oc, window)
	cmdDays := dailyMap(cmd, window)
	dates := timelineDates(window, ocDays, cmdDays)

	days := make([]any, 0, len(dates))
	cumulative := make([]any, 0, len(dates))
	var ocGenerated, cmdGenerated int64
	var ocCostF, ocSpendF, cmdCostF, cmdSpendF float64
	for _, date := range dates {
		ocDay := ocDays[date]
		cmdDay := cmdDays[date]
		days = append(days, map[string]any{
			"date": date,
			"oc":   dayCell(ocDay),
			"cmd":  dayCell(cmdDay),
		})
		if ocDay != nil {
			ocGenerated += ocDay.GeneratedTokens
			ocCostF += ocDay.CostUsd
			ocSpendF += ocDay.TotalSpendUsd
		}
		if cmdDay != nil {
			cmdGenerated += cmdDay.GeneratedTokens
			cmdCostF += cmdDay.CostUsd
			cmdSpendF += cmdDay.TotalSpendUsd
		}
		cumulative = append(cumulative, map[string]any{
			"date": date,
			"oc": map[string]any{
				"generatedTokens": ocGenerated,
				"costUsd":         shared.Round(ocCostF, 6),
				"totalSpendUsd":   shared.Round(ocSpendF, 6),
			},
			"cmd": map[string]any{
				"generatedTokens": cmdGenerated,
				"costUsd":         shared.Round(cmdCostF, 6),
				"totalSpendUsd":   shared.Round(cmdSpendF, 6),
			},
		})
	}
	return map[string]any{
		"from":       optionalString(window.From),
		"to":         optionalString(window.To),
		"days":       days,
		"cumulative": cumulative,
	}
}

func dailyMap(agg *toolAggregate, window comparisonWindow) map[string]*shared.DailyRow {
	days := make(map[string]*shared.DailyRow)
	for index := range agg.report.Daily {
		day := &agg.report.Daily[index]
		if window.Mode == "overlap" && (day.Date < window.From || day.Date > window.To) {
			continue
		}
		days[day.Date] = day
	}
	return days
}

// timelineDates enumerates every date in the comparison window so gaps are
// visible as gaps instead of disappearing from the x-axis. It falls back to
// the union of daily keys when the window is not a parseable range.
func timelineDates(window comparisonWindow, ocDays, cmdDays map[string]*shared.DailyRow) []string {
	if window.From != "" && window.To != "" && window.Days > 0 {
		start, err := time.Parse("2006-01-02", window.From)
		if err == nil {
			end, err := time.Parse("2006-01-02", window.To)
			if err == nil && !end.Before(start) {
				dates := make([]string, 0, window.Days)
				for day := start; !day.After(end); day = day.AddDate(0, 0, 1) {
					dates = append(dates, day.Format("2006-01-02"))
				}
				return dates
			}
		}
	}
	set := make(map[string]struct{}, len(ocDays)+len(cmdDays))
	for date := range ocDays {
		set[date] = struct{}{}
	}
	for date := range cmdDays {
		set[date] = struct{}{}
	}
	dates := make([]string, 0, len(set))
	for date := range set {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	return dates
}

func dayCell(day *shared.DailyRow) any {
	if day == nil {
		return nil
	}
	return map[string]any{
		"steps":           day.Steps,
		"timedN":          day.TimedN,
		"generatedTokens": day.GeneratedTokens,
		"inputTotal":      day.InputTotal,
		"costUsd":         shared.Round(day.CostUsd, 6),
		"totalSpendUsd":   shared.Round(day.TotalSpendUsd, 6),
		"cacheHitRate":    anyOrNil(day.CacheHitRate),
		"weighted":        anyOrNil(day.Weighted),
		"median":          anyOrNil(day.Median),
	}
}

// ---- like-for-like

func likeForLikeSection(oc, cmd *toolAggregate, families familySummary) map[string]any {
	return map[string]any{
		"available":  likeForLikeAvailableFor(oc, cmd, families),
		"basis":      "shared model families only",
		"families":   toAnyStrings(families.Shared),
		"ocModels":   families.SharedOCModels,
		"cmdModels":  families.SharedCMDModels,
		"totals":     totalsSection(oc, cmd),
		"throughput": throughputSection(oc, cmd),
		"cost":       costSection(oc, cmd),
		"cache":      cacheSection(oc, cmd),
		"tools":      toolsSection(oc, cmd),
	}
}

func likeForLikeAvailableFor(oc, cmd *toolAggregate, families familySummary) bool {
	return families.SharedFamilies > 0 && oc.totals.Steps > 0 && cmd.totals.Steps > 0
}

// ---- verdicts

type verdictInput struct {
	Metric       string
	Unit         string
	OC           any
	CMD          any
	Ratio        any
	DeltaPct     any
	DeltaPp      any
	Winner       string
	Scope        string
	Confidence   string
	N            int64
	HigherBetter bool
	Note         string
}

func verdictsSection(oc, cmd *toolAggregate, scope string, families familySummary, spendOC, spendCMD *toolAggregate) []any {
	ocTimed := oc.timed.summary()
	cmdTimed := cmd.timed.summary()
	ocCost := costCell(oc)
	cmdCost := costCell(cmd)
	ocPer1k := optFloat(ocCost, "per1kGenerated")
	cmdPer1k := optFloat(cmdCost, "per1kGenerated")
	ocPerStep := optFloat(ocCost, "perStep")
	cmdPerStep := optFloat(cmdCost, "perStep")
	ocSpendSession := optFloat(costCell(spendOC), "totalSpendPerSession")
	cmdSpendSession := optFloat(costCell(spendCMD), "totalSpendPerSession")
	ocHit := optFloat(cacheCell(oc), "hitRate")
	cmdHit := optFloat(cacheCell(cmd), "hitRate")

	minTimed := minInt64(ocTimed.N, cmdTimed.N)
	minSteps := minInt64(oc.totals.Steps, cmd.totals.Steps)
	minSessions := minInt64(spendOC.totals.Sessions, spendCMD.totals.Sessions)
	weightedWinner := shared.WinnerWithTolerance(ocTimed.Weighted, cmdTimed.Weighted, true, tieRelative)
	medianWinner := shared.WinnerWithTolerance(ocTimed.Median, cmdTimed.Median, true, tieRelative)
	per1kWinner := shared.WinnerWithTolerance(ocPer1k, cmdPer1k, false, tieRelative)
	perStepWinner := shared.WinnerWithTolerance(ocPerStep, cmdPerStep, false, tieRelative)
	spendWinner := shared.WinnerWithTolerance(ocSpendSession, cmdSpendSession, false, tieRelative)
	cacheWinner := shared.WinnerCacheRate(ocHit, cmdHit, cacheTiePp, tieRelative)

	scopeNote := "Shared model families only"
	if scope == "all" {
		scopeNote = "All models; confounded by model and workload mix"
	}
	spendBasisNote := spendBasis(spendOC)
	if spendBasis(spendCMD) != spendBasisNote {
		spendBasisNote = "mixed"
	}
	familyNote := ""
	if families.SharedFamilies == 0 {
		familyNote = " (no shared model families; like-for-like scope unavailable)"
	}

	return []any{
		makeVerdict(verdictInput{
			Metric: "generation speed (weighted)", Unit: "tok/s",
			OC: anyOrNil(ocTimed.Weighted), CMD: anyOrNil(cmdTimed.Weighted),
			Ratio:    roundRatio(shared.RatioDelta(ocTimed.Weighted, cmdTimed.Weighted), 2),
			DeltaPct: roundDelta(shared.PercentDelta(ocTimed.Weighted, cmdTimed.Weighted), 1),
			Winner:   weightedWinner, Scope: scope,
			Confidence:   confidenceFor(shared.RatioDelta(ocTimed.Weighted, cmdTimed.Weighted), minTimed, scope, weightedWinner),
			N:            minTimed,
			HigherBetter: true,
			Note:         scopeNote + familyNote + "; canonical generatedTokens / timed seconds",
		}),
		makeVerdict(verdictInput{
			Metric: "generation speed (median step)", Unit: "tok/s",
			OC: anyOrNil(ocTimed.Median), CMD: anyOrNil(cmdTimed.Median),
			Ratio:    roundRatio(shared.RatioDelta(ocTimed.Median, cmdTimed.Median), 2),
			DeltaPct: roundDelta(shared.PercentDelta(ocTimed.Median, cmdTimed.Median), 1),
			Winner:   medianWinner, Scope: scope,
			Confidence:   confidenceFor(shared.RatioDelta(ocTimed.Median, cmdTimed.Median), minTimed, scope, medianWinner),
			N:            minTimed,
			HigherBetter: true,
			Note:         scopeNote + familyNote + "; median per-step rate, resistant to long tails",
		}),
		makeVerdict(verdictInput{
			Metric: "cost per 1k generated", Unit: "$/1k generated",
			OC: anyOrNil(ocPer1k), CMD: anyOrNil(cmdPer1k),
			Ratio:    roundRatio(shared.RatioDelta(ocPer1k, cmdPer1k), 2),
			DeltaPct: roundDelta(shared.PercentDelta(ocPer1k, cmdPer1k), 1),
			Winner:   per1kWinner, Scope: scope,
			Confidence:   confidenceFor(shared.RatioDelta(ocPer1k, cmdPer1k), minSteps, scope, per1kWinner),
			N:            minSteps,
			HigherBetter: false,
			Note:         scopeNote + familyNote + "; recorded cost includes input and cache cost",
		}),
		makeVerdict(verdictInput{
			Metric: "cost per step", Unit: "$/step",
			OC: anyOrNil(ocPerStep), CMD: anyOrNil(cmdPerStep),
			Ratio:    roundRatio(shared.RatioDelta(ocPerStep, cmdPerStep), 2),
			DeltaPct: roundDelta(shared.PercentDelta(ocPerStep, cmdPerStep), 1),
			Winner:   perStepWinner, Scope: scope,
			Confidence:   confidenceFor(shared.RatioDelta(ocPerStep, cmdPerStep), minSteps, scope, perStepWinner),
			N:            minSteps,
			HigherBetter: false,
			Note:         scopeNote + familyNote + "; recorded cost / step",
		}),
		makeVerdict(verdictInput{
			Metric: "spend per session", Unit: "$/session",
			OC: anyOrNil(ocSpendSession), CMD: anyOrNil(cmdSpendSession),
			Ratio:    roundRatio(shared.RatioDelta(ocSpendSession, cmdSpendSession), 2),
			DeltaPct: roundDelta(shared.PercentDelta(ocSpendSession, cmdSpendSession), 1),
			Winner:   spendWinner, Scope: "all",
			Confidence:   confidenceFor(shared.RatioDelta(ocSpendSession, cmdSpendSession), minSessions, "all", spendWinner),
			N:            minSessions,
			HigherBetter: false,
			Note:         "All-model spend; session mixes differ, so this is not a per-task cost. Spend basis " + spendBasisNote,
		}),
		makeVerdict(verdictInput{
			Metric: "cache hit rate", Unit: "% of input",
			OC: anyOrNil(ocHit), CMD: anyOrNil(cmdHit),
			Ratio:   roundRatio(shared.RatioDelta(ocHit, cmdHit), 4),
			DeltaPp: roundDelta(shared.PointDelta(ocHit, cmdHit), 2),
			Winner:  cacheWinner, Scope: scope,
			Confidence:   confidenceFor(shared.RatioDelta(ocHit, cmdHit), minTimed, scope, cacheWinner),
			N:            minTimed,
			HigherBetter: true,
			Note:         scopeNote + "; <1 pp or <5% relative is a tie",
		}),
	}
}

func makeVerdict(input verdictInput) map[string]any {
	return map[string]any{
		"metric":       input.Metric,
		"unit":         input.Unit,
		"oc":           input.OC,
		"cmd":          input.CMD,
		"ratio":        input.Ratio,
		"deltaPct":     input.DeltaPct,
		"deltaPp":      input.DeltaPp,
		"winner":       input.Winner,
		"scope":        input.Scope,
		"confidence":   input.Confidence,
		"n":            input.N,
		"higherBetter": input.HigherBetter,
		"note":         input.Note,
	}
}

// confidenceFor separates statistical ties from clear differences and flags
// small samples and confounded all-model aggregates.
func confidenceFor(ratio *float64, n int64, scope, winner string) string {
	if ratio == nil {
		return "n/a"
	}
	if winner == "tie" {
		return "tie"
	}
	if n < lowSampleN {
		return "low-sample"
	}
	if scope == "all" {
		return "confounded"
	}
	if math.Abs(*ratio-1) < 0.20 {
		return "weak"
	}
	return "clear"
}

// ---- helpers

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func anyOrNil(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}

func optFloat(object map[string]any, key string) *float64 {
	value, ok := object[key]
	if !ok || value == nil {
		return nil
	}
	number, ok := pyjson.Float(value)
	if !ok {
		return nil
	}
	return &number
}

func floatPtr(value int64) *float64 {
	number := float64(value)
	return &number
}

func floatPtrFloat(value float64) *float64 {
	return &value
}

func roundDelta(value *float64, digits int) any {
	if value == nil {
		return nil
	}
	return shared.Round(*value, digits)
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func closeEnough(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance+1e-9*math.Abs(b)
}

func sumFloats(values []float64) float64 {
	var total float64
	for _, value := range values {
		total += value
	}
	return total
}

func joinIssues(issues []string) string {
	result := ""
	for index, issue := range issues {
		if index > 0 {
			result += "\n- "
		}
		result += issue
	}
	return result
}

// ---- validation

func validateComparison(doc map[string]any, oc, cmd, ocShared, cmdShared *toolAggregate) []string {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }

	validateSide := func(name string, agg *toolAggregate) {
		var familySteps, familyGenerated, familyInput int64
		for _, family := range agg.families {
			familySteps += family.steps
			familyGenerated += family.generated
			familyInput += family.inputTotal
		}
		if familySteps != agg.totals.Steps {
			add("%s: sum(family.steps) = %d, totals.steps = %d", name, familySteps, agg.totals.Steps)
		}
		if familyGenerated != agg.totals.GeneratedTokens {
			add("%s: sum(family.generatedTokens) = %d, totals.generatedTokens = %d", name, familyGenerated, agg.totals.GeneratedTokens)
		}
		if familyInput != agg.totals.InputTotal {
			add("%s: sum(family.inputTotal) = %d, totals.inputTotal = %d", name, familyInput, agg.totals.InputTotal)
		}
		var calls int64
		for _, stat := range agg.category {
			calls += stat.calls
		}
		if calls != agg.totals.ToolCalls {
			add("%s: sum(category.calls) = %d, totals.toolCalls = %d", name, calls, agg.totals.ToolCalls)
		}
		var strataN int64
		for _, bucket := range agg.pooled {
			strataN += bucket.n
		}
		if strataN > agg.timed.n {
			add("%s: sum(strata.n) = %d exceeds timed.n = %d", name, strataN, agg.timed.n)
		}
	}
	validateSide(shared.ToolOC, oc)
	validateSide(shared.ToolCMD, cmd)
	validateSide(shared.ToolOC+"-shared", ocShared)
	validateSide(shared.ToolCMD+"-shared", cmdShared)

	// Tool shares must cover every call exactly once.
	for _, side := range []struct {
		name string
		agg  *toolAggregate
	}{{shared.ToolOC, oc}, {shared.ToolCMD, cmd}, {shared.ToolOC + "-shared", ocShared}, {shared.ToolCMD + "-shared", cmdShared}} {
		total := side.agg.totals.ToolCalls
		if total == 0 {
			continue
		}
		var shareSum float64
		for _, stat := range side.agg.category {
			if share := callShare(stat, total); share != nil {
				shareSum += *share
			}
		}
		if math.Abs(shareSum-1) > 1e-4 {
			add("%s: normalized tool shares sum to %.6f, want 1", side.name, shareSum)
		}
	}

	// The like-for-like scope cannot exceed the all-model totals.
	if ocShared.totals.Steps > oc.totals.Steps {
		add("oc-shared steps %d exceed all-model steps %d", ocShared.totals.Steps, oc.totals.Steps)
	}
	if cmdShared.totals.Steps > cmd.totals.Steps {
		add("cmd-shared steps %d exceed all-model steps %d", cmdShared.totals.Steps, cmd.totals.Steps)
	}
	likeForLike := doc["likeForLike"].(map[string]any)
	families, _ := likeForLike["families"].([]any)
	available, _ := likeForLike["available"].(bool)
	expectedAvailable := len(families) > 0 && ocShared.totals.Steps > 0 && cmdShared.totals.Steps > 0
	if available != expectedAvailable {
		add("likeForLike.available = %v but %d shared families / %d oc / %d cmd shared steps",
			available, len(families), ocShared.totals.Steps, cmdShared.totals.Steps)
	}
	verdicts := doc["verdicts"].(map[string]any)
	for _, scope := range []string{"shared", "all"} {
		items, _ := verdicts[scope].([]any)
		if len(items) != 6 {
			add("verdicts.%s has %d entries, want 6", scope, len(items))
		}
	}

	// The emitted family list and timeline must carry the same totals.
	for _, side := range []struct {
		name string
		agg  *toolAggregate
	}{{shared.ToolOC, oc}, {shared.ToolCMD, cmd}} {
		models := doc["models"].(map[string]any)
		entries, _ := models[side.name].([]any)
		var emitted int64
		for _, entry := range entries {
			cell := entry.(map[string]any)
			if value, ok := cell["steps"].(int64); ok {
				emitted += value
			}
		}
		if emitted != side.agg.totals.Steps {
			add("%s: models block steps = %d, totals.steps = %d", side.name, emitted, side.agg.totals.Steps)
		}

		timeline := doc["timeline"].(map[string]any)
		days, _ := timeline["days"].([]any)
		var dayGenerated int64
		var dayCost float64
		for _, entry := range days {
			day := entry.(map[string]any)
			cell, ok := day[side.name].(map[string]any)
			if !ok {
				continue
			}
			if value, ok := cell["generatedTokens"].(int64); ok {
				dayGenerated += value
			}
			if value, ok := cell["costUsd"].(float64); ok {
				dayCost += value
			}
		}
		if dayGenerated != side.agg.totals.GeneratedTokens {
			add("%s: timeline generated = %d, totals.generatedTokens = %d", side.name, dayGenerated, side.agg.totals.GeneratedTokens)
		}
		if !closeEnough(dayCost, side.agg.cost, 1e-3) {
			add("%s: timeline cost = %.6f, totals.costUsd = %.6f", side.name, dayCost, side.agg.cost)
		}
	}

	if finiteIssues := scanFinite(doc, "comparison"); len(finiteIssues) > 0 {
		issues = append(issues, finiteIssues...)
	}
	return issues
}

func scanFinite(value any, path string) []string {
	var issues []string
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			issues = append(issues, scanFinite(item, path+"."+key)...)
		}
	case []any:
		for index, item := range typed {
			issues = append(issues, scanFinite(item, fmt.Sprintf("%s[%d]", path, index))...)
		}
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			issues = append(issues, fmt.Sprintf("%s is not finite (%v)", path, typed))
		}
	case *float64:
		issues = append(issues, fmt.Sprintf("%s is a raw pointer, not a JSON number", path))
	case int64:
		if typed < 0 {
			// Counts must never be negative; costs may be zero but not counts.
			if !isAllowedNegative(path) {
				issues = append(issues, fmt.Sprintf("%s is negative (%d)", path, typed))
			}
		}
	}
	return issues
}

func isAllowedNegative(path string) bool {
	for _, suffix := range []string{".errorCostUsd", ".totalSpendUsd", ".costUsd", ".recorded", ".savings"} {
		if len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix {
			return true
		}
	}
	return false
}
