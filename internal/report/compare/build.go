// SPDX-License-Identifier: AGPL-3.0-only

// Package compare builds the cross-tool comparison document from two schema
// v3 reports. It is a read-only aggregation: inputs are never modified, no
// network is used, and the output is validated before it is returned.
package compare

import (
	"fmt"
	"math"
	"sort"
	"strings"
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

// sideIDs returns the compared tool ids in canonical pair order. Aggregates
// built by unit tests may lack reports; those fall back to the oc-cmd pair
// so confidence helpers stay callable without fixtures.
func sideIDs(a, b *toolAggregate) (string, string) {
	aID, bID := shared.ToolOC, shared.ToolCMD
	if a != nil && a.report != nil && a.report.Tool != "" {
		aID = a.report.Tool
	}
	if b != nil && b.report != nil && b.report.Tool != "" {
		bID = b.report.Tool
	}
	return aID, bID
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

// Build aggregates two schema v3 reports into the comparison document. The
// reports must arrive in canonical tool order (side A first); ratios read
// A / B throughout. The returned map is the exact JSON payload written to
// comparison-data.json, and mirrors the ordering the web dashboard expects.
func Build(a, b *shared.Report, now time.Time) (map[string]any, error) {
	if a == nil || b == nil {
		return nil, fmt.Errorf("compare: both reports are required")
	}
	window := resolveWindow(a, b)
	aAgg := aggregate(a, window)
	bAgg := aggregate(b, window)
	families := summarizeFamilies(aAgg, bAgg)
	aShared := aggregateFamilies(a, window, families.Set)
	bShared := aggregateFamilies(b, window, families.Set)
	defaultScope := "all"
	if likeForLikeAvailableFor(aShared, bShared, families) {
		defaultScope = "shared"
	}

	doc := map[string]any{
		"schemaVersion":     shared.SchemaVersion,
		"comparisonVersion": shared.ComparisonVersion,
		"kind":              "comparison",
		"meta": metaSection(a, b, window, dataFamilyCounts(aAgg, bAgg), families, defaultScope, now,
			aAgg.cost > 0, bAgg.cost > 0),
		"totals":      totalsSection(aAgg, bAgg),
		"throughput":  throughputSection(aAgg, bAgg),
		"cost":        costSection(aAgg, bAgg),
		"cache":       cacheSection(aAgg, bAgg),
		"tools":       toolsSection(aAgg, bAgg),
		"models":      modelsSection(aAgg, bAgg),
		"strata":      strataSection(aAgg, bAgg),
		"sessions":    sessionsSection(aAgg, bAgg),
		"timeline":    timelineSection(aAgg, bAgg, window),
		"likeForLike": likeForLikeSection(aShared, bShared, families),
		"verdicts": map[string]any{
			"shared": verdictsSection(aShared, bShared, "shared", families, aAgg, bAgg),
			"all":    verdictsSection(aAgg, bAgg, "all", families, aAgg, bAgg),
		},
	}

	if issues := validateComparison(doc, aAgg, bAgg, aShared, bShared); len(issues) > 0 {
		return nil, fmt.Errorf("compare: comparison invariant failure:\n- %s", joinIssues(issues))
	}
	return doc, nil
}

// ---- meta

func metaSection(
	a, b *shared.Report,
	window comparisonWindow,
	dataFamilies int,
	families familySummary,
	defaultScope string,
	now time.Time,
	hasCostA, hasCostB bool,
) map[string]any {
	partial := make([]any, 0, 4)
	for _, side := range []*shared.Report{a, b} {
		if side.Runtime != nil {
			continue
		}
		switch side.Tool {
		case shared.ToolOC:
			partial = append(partial, "OpenCode runtime metrics are unavailable in this report")
		case shared.ToolMuse:
			partial = append(partial, "Muse records per-call durations but no active/idle split: active/idle minutes and runtime overhead are unavailable")
		default:
			partial = append(partial, fmt.Sprintf(
				"%s records no per-step wall clock: active/idle minutes and runtime overhead are unavailable",
				shared.DisplayName(side.Tool),
			))
		}
	}
	for _, side := range []*shared.Report{a, b} {
		if hasModelWithoutVisible(side) {
			partial = append(partial, fmt.Sprintf(
				"%s outputTokens includes reasoning; visible-output splits are unavailable",
				shared.DisplayName(side.Tool),
			))
		}
	}
	if window.Mode == "disjoint" {
		partial = append(partial, "The two sources have no overlapping dates; the comparison covers each tool's full range")
	}
	if window.Mode == "single" {
		partial = append(partial, "Only one source has dated telemetry; the comparison covers its range")
	}
	if a.Timezone != "" && b.Timezone != "" && a.Timezone != b.Timezone {
		partial = append(partial, fmt.Sprintf(
			"Sources use different local timezones (%s vs %s); window dates are compared as report-local dates",
			a.Timezone, b.Timezone,
		))
	}
	if a.Cost.UndatedErrorCostUsd != 0 || b.Cost.UndatedErrorCostUsd != 0 {
		scope := "excluded from overlap-window spend"
		if window.Mode != "overlap" {
			scope = "included in full-range spend"
		}
		partial = append(partial, fmt.Sprintf(
			"Undated ledger cost cannot be windowed (%s $%.6f, %s $%.6f); %s",
			a.Tool, a.Cost.UndatedErrorCostUsd, b.Tool, b.Cost.UndatedErrorCostUsd, scope,
		))
	}
	timezone := a.Timezone
	if timezone == "" {
		timezone = b.Timezone
	}
	if a.Timezone != b.Timezone {
		timezone = "mixed"
	}
	if timezone == "" {
		timezone = "UTC"
	}
	aKey, bKey := a.Tool, b.Tool
	dispA, dispB := shared.DisplayName(aKey), shared.DisplayName(bKey)
	notes := []any{
		"Speed and cost differences mostly reflect provider and model routing, not the agent harness.",
		"Like-for-like pools shared model families by volume; the family table and output-size strata are the matched views.",
		"All-model metrics are context, not a harness verdict.",
		"Spend per session compares different session counts and task mixes; it is not a per-task cost.",
		outcomeNote(aKey, bKey),
	}
	for _, side := range []struct {
		key     string
		hasCost bool
	}{{aKey, hasCostA}, {bKey, hasCostB}} {
		if !side.hasCost {
			notes = append(notes, fmt.Sprintf(
				"%s records no cost data; its cost views read zero, not free.",
				shared.DisplayName(side.key),
			))
		}
	}
	notes = append(notes,
		"Family counts and model counts are different units: a shared family can contain several models per tool. Windowed model counts can be smaller than source model counts.",
		fmt.Sprintf("Family rows show modelMatch (%s:%s model ids); 1:1 is a true model pair, while N:1 and 1:N pool provider variants.", dispA, dispB),
		fmt.Sprintf("Ratios are %s / %s. Speed ratios above 1 favor %s; cost ratios below 1 favor %s.", dispA, dispB, dispA, dispA),
		"Ties are <5% relative. Cache hit-rate ties are <1 percentage point or <5% relative.",
	)
	return map[string]any{
		"generatedAt":       NowUTC(now),
		"comparisonVersion": shared.ComparisonVersion,
		"schemaVersion":     shared.SchemaVersion,
		"kind":              "comparison",
		"timezone":          timezone,
		"pair":              []any{aKey, bKey},
		"tools": map[string]any{
			aKey: toolInfo(a),
			bKey: toolInfo(b),
		},
		"window": map[string]any{
			"mode": window.Mode,
			"from": optionalString(window.From),
			"to":   optionalString(window.To),
			"days": window.Days,
			aKey:   map[string]any{"from": optionalString(window.FromA), "to": optionalString(window.ToA)},
			bKey:   map[string]any{"from": optionalString(window.FromB), "to": optionalString(window.ToB)},
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
			aKey + "Only":   families.AOnlyFamilies,
			bKey + "Only":   families.BOnlyFamilies,
			"withDataInWin": dataFamilies,
		},
		"models": map[string]any{
			aKey:                     families.AModels,
			bKey:                     families.BModels,
			"shared" + upperID(aKey): families.SharedAModels,
			"shared" + upperID(bKey): families.SharedBModels,
		},
		"defaultScope":   defaultScope,
		"sharedFamilies": toAnyStrings(families.Shared),
		"notes":          notes,
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

func dataFamilyCounts(a, b *toolAggregate) int {
	set := make(map[string]struct{})
	for family := range a.families {
		set[family] = struct{}{}
	}
	for family := range b.families {
		set[family] = struct{}{}
	}
	return len(set)
}

// familySummary separates family counts from model counts. A family can hold
// several models per tool, which is why "shared + one-sided" does not equal the
// model total.
type familySummary struct {
	SharedFamilies int
	AOnlyFamilies  int
	BOnlyFamilies  int
	AModels        int
	BModels        int
	SharedAModels  int
	SharedBModels  int
	Shared         []string
	Set            map[string]struct{}
}

func summarizeFamilies(a, b *toolAggregate) familySummary {
	summary := familySummary{Set: make(map[string]struct{})}
	aByFamily := make(map[string]int)
	bByFamily := make(map[string]int)
	for family, item := range a.families {
		aByFamily[family] = len(item.models)
	}
	for family, item := range b.families {
		bByFamily[family] = len(item.models)
	}
	summary.AModels = len(a.models)
	summary.BModels = len(b.models)
	for family, count := range aByFamily {
		if bCount, ok := bByFamily[family]; ok {
			summary.SharedFamilies++
			summary.SharedAModels += count
			summary.SharedBModels += bCount
			summary.Shared = append(summary.Shared, family)
			summary.Set[family] = struct{}{}
		} else {
			summary.AOnlyFamilies++
		}
	}
	for family := range bByFamily {
		if _, ok := aByFamily[family]; !ok {
			summary.BOnlyFamilies++
		}
	}
	sort.Strings(summary.Shared)
	return summary
}

// ---- totals

func totalsSection(a, b *toolAggregate) map[string]any {
	aKey, bKey := sideIDs(a, b)
	aTotals := totalsCell(a)
	bTotals := totalsCell(b)
	aHit := optFloat(aTotals, "cacheHitRate")
	bHit := optFloat(bTotals, "cacheHitRate")
	return map[string]any{
		aKey: aTotals,
		bKey: bTotals,
		"delta": map[string]any{
			"stepsPct":             roundDelta(shared.PercentDelta(floatPtr(a.totals.Steps), floatPtr(b.totals.Steps)), 1),
			"stepsRatio":           roundRatio(shared.RatioDelta(floatPtr(a.totals.Steps), floatPtr(b.totals.Steps)), 2),
			"sessionsRatio":        roundRatio(shared.RatioDelta(floatPtr(a.totals.Sessions), floatPtr(b.totals.Sessions)), 2),
			"toolCallsPct":         roundDelta(shared.PercentDelta(floatPtr(a.totals.ToolCalls), floatPtr(b.totals.ToolCalls)), 1),
			"toolCallsRatio":       roundRatio(shared.RatioDelta(floatPtr(a.totals.ToolCalls), floatPtr(b.totals.ToolCalls)), 2),
			"generatedTokensPct":   roundDelta(shared.PercentDelta(floatPtr(a.totals.GeneratedTokens), floatPtr(b.totals.GeneratedTokens)), 1),
			"generatedTokensRatio": roundRatio(shared.RatioDelta(floatPtr(a.totals.GeneratedTokens), floatPtr(b.totals.GeneratedTokens)), 2),
			"inputTotalPct":        roundDelta(shared.PercentDelta(floatPtr(a.totals.InputTotal), floatPtr(b.totals.InputTotal)), 1),
			"inputTotalRatio":      roundRatio(shared.RatioDelta(floatPtr(a.totals.InputTotal), floatPtr(b.totals.InputTotal)), 2),
			"costUsdPct":           roundDelta(shared.PercentDelta(&a.cost, &b.cost), 1),
			"costUsdRatio":         roundRatio(shared.RatioDelta(&a.cost, &b.cost), 2),
			"totalSpendUsdPct":     roundDelta(shared.PercentDelta(&a.totalSpend, &b.totalSpend), 1),
			"totalSpendUsdRatio":   roundRatio(shared.RatioDelta(&a.totalSpend, &b.totalSpend), 2),
			"cacheHitRatePp":       roundDelta(shared.PointDelta(aHit, bHit), 2),
			"cacheHitRateRatio":    roundRatio(shared.RatioDelta(aHit, bHit), 4),
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

func throughputSection(a, b *toolAggregate) map[string]any {
	aKey, bKey := sideIDs(a, b)
	aSummary := a.timed.summary()
	bSummary := b.timed.summary()
	return map[string]any{
		aKey: timedCell(aSummary),
		bKey: timedCell(bSummary),
		"delta": map[string]any{
			"nPct":          roundDelta(shared.PercentDelta(floatPtr(aSummary.N), floatPtr(bSummary.N)), 1),
			"nRatio":        roundRatio(shared.RatioDelta(floatPtr(aSummary.N), floatPtr(bSummary.N)), 2),
			"tokensPct":     roundDelta(shared.PercentDelta(floatPtr(aSummary.Tokens), floatPtr(bSummary.Tokens)), 1),
			"tokensRatio":   roundRatio(shared.RatioDelta(floatPtr(aSummary.Tokens), floatPtr(bSummary.Tokens)), 2),
			"secondsPct":    roundDelta(shared.PercentDelta(&aSummary.Seconds, &bSummary.Seconds), 1),
			"secondsRatio":  roundRatio(shared.RatioDelta(&aSummary.Seconds, &bSummary.Seconds), 2),
			"weightedPct":   roundDelta(shared.PercentDelta(aSummary.Weighted, bSummary.Weighted), 1),
			"weightedRatio": roundRatio(shared.RatioDelta(aSummary.Weighted, bSummary.Weighted), 2),
			"medianPct":     roundDelta(shared.PercentDelta(aSummary.Median, bSummary.Median), 1),
			"medianRatio":   roundRatio(shared.RatioDelta(aSummary.Median, bSummary.Median), 2),
			"p90Pct":        roundDelta(shared.PercentDelta(aSummary.P90, bSummary.P90), 1),
			"p90Ratio":      roundRatio(shared.RatioDelta(aSummary.P90, bSummary.P90), 2),
		},
		"winner": map[string]any{
			"weighted": shared.WinnerWithTolerance(aSummary.Weighted, bSummary.Weighted, true, tieRelative, aKey, bKey),
			"median":   shared.WinnerWithTolerance(aSummary.Median, bSummary.Median, true, tieRelative, aKey, bKey),
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

func costSection(a, b *toolAggregate) map[string]any {
	aKey, bKey := sideIDs(a, b)
	aCost := costCell(a)
	bCost := costCell(b)
	aPer1k := optFloat(aCost, "per1kGenerated")
	bPer1k := optFloat(bCost, "per1kGenerated")
	aPerStep := optFloat(aCost, "perStep")
	bPerStep := optFloat(bCost, "perStep")
	aPerSession := optFloat(aCost, "costPerSession")
	bPerSession := optFloat(bCost, "costPerSession")
	aPerInput := optFloat(aCost, "perMillionInput")
	bPerInput := optFloat(bCost, "perMillionInput")
	aSpendSession := optFloat(aCost, "totalSpendPerSession")
	bSpendSession := optFloat(bCost, "totalSpendPerSession")
	return map[string]any{
		aKey: aCost,
		bKey: bCost,
		"delta": map[string]any{
			"recordedPct":               roundDelta(shared.PercentDelta(floatPtrFloat(a.cost), floatPtrFloat(b.cost)), 1),
			"recordedRatio":             roundRatio(shared.RatioDelta(floatPtrFloat(a.cost), floatPtrFloat(b.cost)), 2),
			"totalSpendUsdPct":          roundDelta(shared.PercentDelta(&a.totalSpend, &b.totalSpend), 1),
			"totalSpendUsdRatio":        roundRatio(shared.RatioDelta(&a.totalSpend, &b.totalSpend), 2),
			"perStepPct":                roundDelta(shared.PercentDelta(aPerStep, bPerStep), 1),
			"perStepRatio":              roundRatio(shared.RatioDelta(aPerStep, bPerStep), 2),
			"perMillionGeneratedPct":    roundDelta(shared.PercentDelta(optFloat(aCost, "perMillionGenerated"), optFloat(bCost, "perMillionGenerated")), 1),
			"perMillionGeneratedRatio":  roundRatio(shared.RatioDelta(optFloat(aCost, "perMillionGenerated"), optFloat(bCost, "perMillionGenerated")), 2),
			"per1kGeneratedPct":         roundDelta(shared.PercentDelta(aPer1k, bPer1k), 1),
			"per1kGeneratedRatio":       roundRatio(shared.RatioDelta(aPer1k, bPer1k), 2),
			"perMillionInputPct":        roundDelta(shared.PercentDelta(aPerInput, bPerInput), 1),
			"perMillionInputRatio":      roundRatio(shared.RatioDelta(aPerInput, bPerInput), 2),
			"costPerSessionPct":         roundDelta(shared.PercentDelta(aPerSession, bPerSession), 1),
			"costPerSessionRatio":       roundRatio(shared.RatioDelta(aPerSession, bPerSession), 2),
			"totalSpendPerSessionPct":   roundDelta(shared.PercentDelta(aSpendSession, bSpendSession), 1),
			"totalSpendPerSessionRatio": roundRatio(shared.RatioDelta(aSpendSession, bSpendSession), 2),
			"perToolCallRatio":          roundRatio(shared.RatioDelta(optFloat(aCost, "perToolCall"), optFloat(bCost, "perToolCall")), 2),
		},
		"winner": map[string]any{
			"per1kGenerated":       shared.WinnerWithTolerance(aPer1k, bPer1k, false, tieRelative, aKey, bKey),
			"perMillionInput":      shared.WinnerWithTolerance(aPerInput, bPerInput, false, tieRelative, aKey, bKey),
			"perStep":              shared.WinnerWithTolerance(aPerStep, bPerStep, false, tieRelative, aKey, bKey),
			"costPerSession":       shared.WinnerWithTolerance(aPerSession, bPerSession, false, tieRelative, aKey, bKey),
			"totalSpendPerSession": shared.WinnerWithTolerance(aSpendSession, bSpendSession, false, tieRelative, aKey, bKey),
			"totalSpendUsd":        shared.WinnerWithTolerance(&a.totalSpend, &b.totalSpend, false, tieRelative, aKey, bKey),
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

func cacheSection(a, b *toolAggregate) map[string]any {
	aKey, bKey := sideIDs(a, b)
	aCache := cacheCell(a)
	bCache := cacheCell(b)
	aHit := optFloat(aCache, "hitRate")
	bHit := optFloat(bCache, "hitRate")
	aWrite := optFloat(aCache, "writeShare")
	bWrite := optFloat(bCache, "writeShare")
	return map[string]any{
		aKey: aCache,
		bKey: bCache,
		"delta": map[string]any{
			"hitRatePp":       roundDelta(shared.PointDelta(aHit, bHit), 2),
			"hitRateRatio":    roundRatio(shared.RatioDelta(aHit, bHit), 4),
			"writeSharePp":    roundDelta(shared.PointDelta(aWrite, bWrite), 2),
			"writeShareRatio": roundRatio(shared.RatioDelta(aWrite, bWrite), 4),
			"savingsPct":      roundDelta(shared.PercentDelta(floatPtrFloat(a.report.Cache.SavingsUsd), floatPtrFloat(b.report.Cache.SavingsUsd)), 1),
			"savingsRatio":    roundRatio(shared.RatioDelta(floatPtrFloat(a.report.Cache.SavingsUsd), floatPtrFloat(b.report.Cache.SavingsUsd)), 2),
		},
		"winner": map[string]any{
			"hitRate": shared.WinnerCacheRate(aHit, bHit, cacheTiePp, tieRelative, aKey, bKey),
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

func toolsSection(a, b *toolAggregate) map[string]any {
	aKey, bKey := sideIDs(a, b)
	aRaw := toolCells(a)
	bRaw := toolCells(b)
	aTotal := a.totals.ToolCalls
	bTotal := b.totals.ToolCalls

	categoryNames := make(map[string]struct{})
	for name := range a.category {
		categoryNames[name] = struct{}{}
	}
	for name := range b.category {
		categoryNames[name] = struct{}{}
	}
	categories := make([]any, 0, len(categoryNames))
	aShareSum, bShareSum := 0.0, 0.0
	for name := range categoryNames {
		aCall := a.category[name]
		bCall := b.category[name]
		aShare := callShare(aCall, aTotal)
		bShare := callShare(bCall, bTotal)
		if aShare != nil {
			aShareSum += *aShare
		}
		if bShare != nil {
			bShareSum += *bShare
		}
		categories = append(categories, map[string]any{
			"category": name,
			aKey:       callCell(aCall, aTotal),
			bKey:       callCell(bCall, bTotal),
			"delta": map[string]any{
				"sharePp":      roundDelta(shared.PointDelta(aShare, bShare), 2),
				"shareRatio":   roundRatio(shared.RatioDelta(aShare, bShare), 2),
				"callsPct":     roundDelta(shared.PercentDelta(callCallsPtr(aCall), callCallsPtr(bCall)), 1),
				"callsRatio":   roundRatio(shared.RatioDelta(callCallsPtr(aCall), callCallsPtr(bCall)), 2),
				"generatedPct": roundDelta(shared.PercentDelta(callGeneratedPtr(aCall), callGeneratedPtr(bCall)), 1),
			},
		})
	}
	sort.SliceStable(categories, func(i, j int) bool {
		iCalls := cellCalls(categories[i], aKey)
		jCalls := cellCalls(categories[j], aKey)
		if iCalls != jCalls {
			return iCalls > jCalls
		}
		return categoryName(categories[i]) < categoryName(categories[j])
	})

	return map[string]any{
		aKey:         aRaw,
		bKey:         bRaw,
		"normalized": categories,
		"totals": map[string]any{
			aKey:         aTotal,
			bKey:         bTotal,
			"deltaPct":   roundDelta(shared.PercentDelta(floatPtr(aTotal), floatPtr(bTotal)), 1),
			"ratio":      roundRatio(shared.RatioDelta(floatPtr(aTotal), floatPtr(bTotal)), 2),
			"categories": len(categories),
		},
		"coverage": map[string]any{
			aKey: anyOrNil(shared.R4(&aShareSum)),
			bKey: anyOrNil(shared.R4(&bShareSum)),
		},
		"runtimeComparable": sideRuntimeComparable(a) && sideRuntimeComparable(b),
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

func cellCalls(entry any, aKey string) int64 {
	cell := entry.(map[string]any)
	if side, ok := cell[aKey].(map[string]any); ok {
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

// sideRuntimeComparable reports whether a side has comparable runtime data:
// per-tool time shares for OpenCode, a runtime block otherwise.
func sideRuntimeComparable(agg *toolAggregate) bool {
	if agg.report.Tool == shared.ToolOC {
		for _, tool := range agg.report.Tools {
			if tool.TimeShare != nil {
				return true
			}
		}
		return false
	}
	return agg.report.Runtime != nil
}

// ---- models

func modelsSection(a, b *toolAggregate) map[string]any {
	aKey, bKey := sideIDs(a, b)
	familyNames := sortedFamilies(a, b)
	aPer1kValues := make([]float64, 0, len(familyNames))
	bPer1kValues := make([]float64, 0, len(familyNames))
	aSpeedValues := make([]float64, 0, len(familyNames))
	bSpeedValues := make([]float64, 0, len(familyNames))
	for _, family := range familyNames {
		if !familyShared(a, b, family) {
			continue
		}
		aCell := familyCell(a.families[family], aggModelIDs(a, family))
		bCell := familyCell(b.families[family], aggModelIDs(b, family))
		if value := optFloat(aCell, "per1kGenerated"); value != nil {
			aPer1kValues = append(aPer1kValues, *value)
		}
		if value := optFloat(bCell, "per1kGenerated"); value != nil {
			bPer1kValues = append(bPer1kValues, *value)
		}
		if value := optFloat(aCell, "weighted"); value != nil {
			aSpeedValues = append(aSpeedValues, *value)
		}
		if value := optFloat(bCell, "weighted"); value != nil {
			bSpeedValues = append(bSpeedValues, *value)
		}
	}
	aPer1kMedian := shared.MedianOf(aPer1kValues)
	bPer1kMedian := shared.MedianOf(bPer1kValues)
	aSpeedMedian := shared.MedianOf(aSpeedValues)
	bSpeedMedian := shared.MedianOf(bSpeedValues)

	families := make([]any, 0, len(familyNames))
	for _, family := range familyNames {
		aFamily := a.families[family]
		bFamily := b.families[family]
		aModels := aggModelIDs(a, family)
		bModels := aggModelIDs(b, family)
		sharedFamily := len(aModels) > 0 && len(bModels) > 0
		if !sharedFamily {
			if len(aModels) == 0 && len(bModels) == 0 {
				aModels = modelIDs(a.report, family)
				bModels = modelIDs(b.report, family)
			}
		}
		aCell := familyCell(aFamily, aModels)
		bCell := familyCell(bFamily, bModels)
		aWeighted := optFloat(aCell, "weighted")
		bWeighted := optFloat(bCell, "weighted")
		aMedian := optFloat(aCell, "median")
		bMedian := optFloat(bCell, "median")
		aPer1k := optFloat(aCell, "per1kGenerated")
		bPer1k := optFloat(bCell, "per1kGenerated")
		aTimedN := int64Value(optFloat(aCell, "timedN"))
		bTimedN := int64Value(optFloat(bCell, "timedN"))

		flags := make([]any, 0, 3)
		if !sharedFamily {
			flags = append(flags, "one-sided")
		}
		lowSample := sharedFamily && (aTimedN < lowSampleN || bTimedN < lowSampleN)
		if lowSample {
			flags = append(flags, "low-sample")
		}
		if sharedFamily && (isOutlier(aPer1k, aPer1kMedian, 2.0) || isOutlier(bPer1k, bPer1kMedian, 2.0)) {
			flags = append(flags, "cost-outlier")
		}
		if sharedFamily && (isOutlier(aWeighted, aSpeedMedian, 3.0) || isOutlier(bWeighted, bSpeedMedian, 3.0)) {
			flags = append(flags, "speed-outlier")
		}

		families = append(families, map[string]any{
			"family":     family,
			"shared":     sharedFamily,
			"modelMatch": modelMatch(len(aModels), len(bModels)),
			"n": map[string]any{
				aKey: aTimedN,
				bKey: bTimedN,
			},
			"flags":      flags,
			"confidence": familyConfidence(sharedFamily, aWeighted, bWeighted, aTimedN, bTimedN),
			aKey:         aCell,
			bKey:         bCell,
			"delta": map[string]any{
				"stepsPct":             roundDelta(shared.PercentDelta(optFloat(aCell, "steps"), optFloat(bCell, "steps")), 1),
				"stepsRatio":           roundRatio(shared.RatioDelta(optFloat(aCell, "steps"), optFloat(bCell, "steps")), 2),
				"generatedTokensPct":   roundDelta(shared.PercentDelta(optFloat(aCell, "generatedTokens"), optFloat(bCell, "generatedTokens")), 1),
				"generatedTokensRatio": roundRatio(shared.RatioDelta(optFloat(aCell, "generatedTokens"), optFloat(bCell, "generatedTokens")), 2),
				"weightedPct":          roundDelta(shared.PercentDelta(aWeighted, bWeighted), 1),
				"weightedRatio":        roundRatio(shared.RatioDelta(aWeighted, bWeighted), 2),
				"medianPct":            roundDelta(shared.PercentDelta(aMedian, bMedian), 1),
				"medianRatio":          roundRatio(shared.RatioDelta(aMedian, bMedian), 2),
				"p90Pct":               roundDelta(shared.PercentDelta(optFloat(aCell, "p90"), optFloat(bCell, "p90")), 1),
				"p90Ratio":             roundRatio(shared.RatioDelta(optFloat(aCell, "p90"), optFloat(bCell, "p90")), 2),
				"per1kGeneratedPct":    roundDelta(shared.PercentDelta(aPer1k, bPer1k), 1),
				"per1kGeneratedRatio":  roundRatio(shared.RatioDelta(aPer1k, bPer1k), 2),
				"cacheHitRatePp":       roundDelta(shared.PointDelta(optFloat(aCell, "cacheHitRate"), optFloat(bCell, "cacheHitRate")), 2),
				"cacheHitRateRatio":    roundRatio(shared.RatioDelta(optFloat(aCell, "cacheHitRate"), optFloat(bCell, "cacheHitRate")), 4),
			},
			"winner": map[string]any{
				"weighted":       shared.WinnerWithTolerance(aWeighted, bWeighted, true, tieRelative, aKey, bKey),
				"median":         shared.WinnerWithTolerance(aMedian, bMedian, true, tieRelative, aKey, bKey),
				"per1kGenerated": shared.WinnerWithTolerance(aPer1k, bPer1k, false, tieRelative, aKey, bKey),
			},
		})
	}
	return map[string]any{
		aKey:       modelCells(a),
		bKey:       modelCells(b),
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

func modelMatch(aCount, bCount int) string {
	if aCount == 0 || bCount == 0 {
		return "one-sided"
	}
	return fmt.Sprintf("%d:%d", aCount, bCount)
}

func familyConfidence(sharedFamily bool, aWeighted, bWeighted *float64, aN, bN int64) string {
	if !sharedFamily {
		return "one-sided"
	}
	ratio := shared.RatioDelta(aWeighted, bWeighted)
	if ratio == nil {
		return "n/a"
	}
	relative := math.Abs(*ratio - 1)
	if relative < tieRelative {
		return "tie"
	}
	if aN < lowSampleN || bN < lowSampleN {
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
		perMillion := shared.R2(shared.PerMillion(model.cost, model.generated))
		perInput := shared.R2(shared.PerMillion(model.cost, model.inputTotal))
		perStep := shared.R4(shared.DivPtr(model.cost, float64(model.steps)))
		summary := model.timed.summary()
		cells = append(cells, map[string]any{
			"model":               model.model.Model,
			"short":               model.model.Short,
			"provider":            model.model.Provider,
			"family":              model.model.Family,
			"steps":               model.steps,
			"toolSteps":           model.toolSteps,
			"generatedTokens":     model.generated,
			"inputTotal":          model.inputTotal,
			"cacheReadTokens":     model.cacheRead,
			"cacheWriteTokens":    model.cacheWrite,
			"cacheHitRate":        anyOrNil(hit),
			"costUsd":             shared.Round(model.cost, 6),
			"timedN":              summary.N,
			"weighted":            anyOrNil(summary.Weighted),
			"median":              anyOrNil(summary.Median),
			"p10":                 anyOrNil(summary.P10),
			"p25":                 anyOrNil(summary.P25),
			"p75":                 anyOrNil(summary.P75),
			"p90":                 anyOrNil(summary.P90),
			"per1kGenerated":      anyOrNil(per1k),
			"perMillionGenerated": anyOrNil(perMillion),
			"perMillionInput":     anyOrNil(perInput),
			"perStep":             anyOrNil(perStep),
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
			"models":              modelValues,
			"steps":               int64(0),
			"toolSteps":           int64(0),
			"generatedTokens":     int64(0),
			"inputTotal":          int64(0),
			"cacheReadTokens":     int64(0),
			"cacheHitRate":        nil,
			"costUsd":             float64(0),
			"per1kGenerated":      nil,
			"perMillionGenerated": nil,
			"perMillionInput":     nil,
			"perStep":             nil,
			"timedN":              int64(0),
			"weighted":            nil,
			"median":              nil,
			"p10":                 nil,
			"p25":                 nil,
			"p75":                 nil,
			"p90":                 nil,
		}
	}
	hit := shared.R4(shared.Rate(agg.cacheRead, agg.inputTotal))
	per1k := shared.R4(shared.PerKThousand(agg.cost, agg.generated))
	perMillion := shared.R2(shared.PerMillion(agg.cost, agg.generated))
	perInput := shared.R2(shared.PerMillion(agg.cost, agg.inputTotal))
	perStep := shared.R4(shared.DivPtr(agg.cost, float64(agg.steps)))
	summary := agg.timed.summary()
	return map[string]any{
		"models":              modelValues,
		"steps":               agg.steps,
		"toolSteps":           agg.toolSteps,
		"generatedTokens":     agg.generated,
		"inputTotal":          agg.inputTotal,
		"cacheReadTokens":     agg.cacheRead,
		"cacheHitRate":        anyOrNil(hit),
		"costUsd":             shared.Round(agg.cost, 6),
		"per1kGenerated":      anyOrNil(per1k),
		"perMillionGenerated": anyOrNil(perMillion),
		"perMillionInput":     anyOrNil(perInput),
		"perStep":             anyOrNil(perStep),
		"timedN":              summary.N,
		"weighted":            anyOrNil(summary.Weighted),
		"median":              anyOrNil(summary.Median),
		"p10":                 anyOrNil(summary.P10),
		"p25":                 anyOrNil(summary.P25),
		"p75":                 anyOrNil(summary.P75),
		"p90":                 anyOrNil(summary.P90),
	}
}

// ---- strata

func strataSection(a, b *toolAggregate) map[string]any {
	aKey, bKey := sideIDs(a, b)
	bucketLabels := make([]any, 0, len(shared.StrataBuckets))
	pooled := make([]any, 0, len(shared.StrataBuckets))
	for _, bucket := range shared.StrataBuckets {
		bucketLabels = append(bucketLabels, bucket.Label)
		aBucket := a.pooled[bucket.Label]
		bBucket := b.pooled[bucket.Label]
		aCell := bucketCell(aBucket)
		bCell := bucketCell(bBucket)
		pooled = append(pooled, map[string]any{
			"bucket": bucket.Label,
			aKey:     aCell,
			bKey:     bCell,
			"delta": map[string]any{
				"weightedPct":   roundDelta(shared.PercentDelta(optFloat(aCell, "weighted"), optFloat(bCell, "weighted")), 1),
				"weightedRatio": roundRatio(shared.RatioDelta(optFloat(aCell, "weighted"), optFloat(bCell, "weighted")), 2),
				"medianPct":     roundDelta(shared.PercentDelta(optFloat(aCell, "median"), optFloat(bCell, "median")), 1),
				"medianRatio":   roundRatio(shared.RatioDelta(optFloat(aCell, "median"), optFloat(bCell, "median")), 2),
			},
			"lowSample": bucketLowSample(aCell, bCell),
		})
	}

	sharedFamilies := make([]any, 0, len(a.families))
	for _, family := range sortedFamilies(a, b) {
		if !familyShared(a, b, family) {
			continue
		}
		buckets := make([]any, 0, len(shared.StrataBuckets))
		for _, bucket := range shared.StrataBuckets {
			aBucket := familyBucket(a, family, bucket.Label)
			bBucket := familyBucket(b, family, bucket.Label)
			aCell := bucketCell(aBucket)
			bCell := bucketCell(bBucket)
			deltaWeighted := roundDelta(shared.PercentDelta(optFloat(aCell, "weighted"), optFloat(bCell, "weighted")), 1)
			buckets = append(buckets, map[string]any{
				"bucket": bucket.Label,
				aKey:     aCell,
				bKey:     bCell,
				"delta": map[string]any{
					"weightedPct":   deltaWeighted,
					"weightedRatio": roundRatio(shared.RatioDelta(optFloat(aCell, "weighted"), optFloat(bCell, "weighted")), 2),
					"medianPct":     roundDelta(shared.PercentDelta(optFloat(aCell, "median"), optFloat(bCell, "median")), 1),
					"medianRatio":   roundRatio(shared.RatioDelta(optFloat(aCell, "median"), optFloat(bCell, "median")), 2),
				},
				"lowSample": bucketLowSample(aCell, bCell),
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

func bucketLowSample(a, b map[string]any) bool {
	aN := a["n"].(int64)
	bN := b["n"].(int64)
	return aN < 5 || bN < 5
}

// ---- sessions

func sessionsSection(a, b *toolAggregate) map[string]any {
	aKey, bKey := sideIDs(a, b)
	aCell := sessionCell(a)
	bCell := sessionCell(b)
	return map[string]any{
		aKey: aCell,
		bKey: bCell,
		"delta": map[string]any{
			"spanMinutesPct":         roundDelta(shared.PercentDelta(optFloat(aCell, "spanMinutesMedian"), optFloat(bCell, "spanMinutesMedian")), 1),
			"stepsPerSessionPct":     roundDelta(shared.PercentDelta(optFloat(aCell, "stepsPerSession"), optFloat(bCell, "stepsPerSession")), 1),
			"generatedPerSessionPct": roundDelta(shared.PercentDelta(optFloat(aCell, "generatedPerSession"), optFloat(bCell, "generatedPerSession")), 1),
			"outputPerMinutePct":     roundDelta(shared.PercentDelta(optFloat(aCell, "outputPerMinuteMedian"), optFloat(bCell, "outputPerMinuteMedian")), 1),
			"costPerSessionPct":      roundDelta(shared.PercentDelta(optFloat(aCell, "costPerSession"), optFloat(bCell, "costPerSession")), 1),
		},
		"overhead": map[string]any{
			aKey: sideOverhead(a),
			bKey: sideOverhead(b),
		},
		"winner": map[string]any{
			"outputPerMinute": shared.Winner(optFloat(aCell, "outputPerMinuteMedian"), optFloat(bCell, "outputPerMinuteMedian"), true, aKey, bKey),
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

// sideOverhead computes step overhead for tools that record per-step wall
// clock (TotalDT); otherwise it reports unavailable without guessing.
func sideOverhead(agg *toolAggregate) map[string]any {
	if agg.report.Tool != shared.ToolOC {
		return map[string]any{
			"runtimeAvailable":      false,
			"medianOverheadSeconds": nil,
			"p90OverheadSeconds":    nil,
			"overheadShare":         nil,
			"medianStepSeconds":     nil,
			"reason":                fmt.Sprintf("%s transcripts do not record per-step wall clock", shared.DisplayName(agg.report.Tool)),
		}
	}
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

// ---- timeline

func timelineSection(a, b *toolAggregate, window comparisonWindow) map[string]any {
	aKey, bKey := sideIDs(a, b)
	aDays := dailyMap(a, window)
	bDays := dailyMap(b, window)
	dates := timelineDates(window, aDays, bDays)

	days := make([]any, 0, len(dates))
	cumulative := make([]any, 0, len(dates))
	var aGenerated, bGenerated int64
	var aCostF, aSpendF, bCostF, bSpendF float64
	for _, date := range dates {
		aDay := aDays[date]
		bDay := bDays[date]
		days = append(days, map[string]any{
			"date": date,
			aKey:   dayCell(aDay),
			bKey:   dayCell(bDay),
		})
		if aDay != nil {
			aGenerated += aDay.GeneratedTokens
			aCostF += aDay.CostUsd
			aSpendF += aDay.TotalSpendUsd
		}
		if bDay != nil {
			bGenerated += bDay.GeneratedTokens
			bCostF += bDay.CostUsd
			bSpendF += bDay.TotalSpendUsd
		}
		cumulative = append(cumulative, map[string]any{
			"date": date,
			aKey: map[string]any{
				"generatedTokens": aGenerated,
				"costUsd":         shared.Round(aCostF, 6),
				"totalSpendUsd":   shared.Round(aSpendF, 6),
			},
			bKey: map[string]any{
				"generatedTokens": bGenerated,
				"costUsd":         shared.Round(bCostF, 6),
				"totalSpendUsd":   shared.Round(bSpendF, 6),
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
func timelineDates(window comparisonWindow, aDays, bDays map[string]*shared.DailyRow) []string {
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
	set := make(map[string]struct{}, len(aDays)+len(bDays))
	for date := range aDays {
		set[date] = struct{}{}
	}
	for date := range bDays {
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

func likeForLikeSection(a, b *toolAggregate, families familySummary) map[string]any {
	aKey, bKey := sideIDs(a, b)
	return map[string]any{
		"available":     likeForLikeAvailableFor(a, b, families),
		"basis":         "shared model families pooled by volume",
		"families":      toAnyStrings(families.Shared),
		aKey + "Models": families.SharedAModels,
		bKey + "Models": families.SharedBModels,
		"totals":        totalsSection(a, b),
		"throughput":    throughputSection(a, b),
		"cost":          costSection(a, b),
		"cache":         cacheSection(a, b),
		"tools":         toolsSection(a, b),
	}
}

func likeForLikeAvailableFor(a, b *toolAggregate, families familySummary) bool {
	return families.SharedFamilies > 0 && a.totals.Steps > 0 && b.totals.Steps > 0
}

// ---- verdicts

type verdictInput struct {
	Metric       string
	Unit         string
	A            any
	B            any
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

func verdictsSection(a, b *toolAggregate, scope string, families familySummary, spendA, spendB *toolAggregate) []any {
	aKey, bKey := sideIDs(a, b)
	aTimed := a.timed.summary()
	bTimed := b.timed.summary()
	aCost := costCell(a)
	bCost := costCell(b)
	aPer1k := optFloat(aCost, "per1kGenerated")
	bPer1k := optFloat(bCost, "per1kGenerated")
	aPerStep := optFloat(aCost, "perStep")
	bPerStep := optFloat(bCost, "perStep")
	aSpendSession := optFloat(costCell(spendA), "totalSpendPerSession")
	bSpendSession := optFloat(costCell(spendB), "totalSpendPerSession")
	aHit := optFloat(cacheCell(a), "hitRate")
	bHit := optFloat(cacheCell(b), "hitRate")

	minTimed := minInt64(aTimed.N, bTimed.N)
	minSteps := minInt64(a.totals.Steps, b.totals.Steps)
	minSessions := minInt64(spendA.totals.Sessions, spendB.totals.Sessions)
	weightedWinner := shared.WinnerWithTolerance(aTimed.Weighted, bTimed.Weighted, true, tieRelative, aKey, bKey)
	medianWinner := shared.WinnerWithTolerance(aTimed.Median, bTimed.Median, true, tieRelative, aKey, bKey)
	per1kWinner := shared.WinnerWithTolerance(aPer1k, bPer1k, false, tieRelative, aKey, bKey)
	perStepWinner := shared.WinnerWithTolerance(aPerStep, bPerStep, false, tieRelative, aKey, bKey)
	spendWinner := shared.WinnerWithTolerance(aSpendSession, bSpendSession, false, tieRelative, aKey, bKey)
	cacheWinner := shared.WinnerCacheRate(aHit, bHit, cacheTiePp, tieRelative, aKey, bKey)

	scopeNote := fmt.Sprintf("Pooled across %d shared families", families.SharedFamilies)
	if scope == "all" {
		scopeNote = "All models; confounded by model and workload mix"
	}
	spendBasisNote := spendBasis(spendA)
	if spendBasis(spendB) != spendBasisNote {
		spendBasisNote = "mixed"
	}
	familyNote := ""
	if families.SharedFamilies == 0 {
		familyNote = " (no shared model families; like-for-like scope unavailable)"
	}

	weightedConf, weightedNote := sharedVerdictConfidence(
		a, b, families, shared.RatioDelta(aTimed.Weighted, bTimed.Weighted),
		minTimed, scope, weightedWinner, "weighted",
		scopeNote+familyNote+"; canonical generatedTokens / timed seconds")
	medianConf, medianNote := sharedVerdictConfidence(
		a, b, families, shared.RatioDelta(aTimed.Median, bTimed.Median),
		minTimed, scope, medianWinner, "median",
		scopeNote+familyNote+"; median per-step rate, resistant to long tails")
	per1kConf, per1kNote := sharedVerdictConfidence(
		a, b, families, shared.RatioDelta(aPer1k, bPer1k),
		minSteps, scope, per1kWinner, "per1kGenerated",
		scopeNote+familyNote+"; recorded cost includes input and cache cost")
	perStepConf, perStepNote := sharedVerdictConfidence(
		a, b, families, shared.RatioDelta(aPerStep, bPerStep),
		minSteps, scope, perStepWinner, "perStep",
		scopeNote+familyNote+"; recorded cost / step")

	return []any{
		makeVerdict(verdictInput{
			Metric: "generation speed (weighted)", Unit: "tok/s",
			A: anyOrNil(aTimed.Weighted), B: anyOrNil(bTimed.Weighted),
			Ratio:    roundRatio(shared.RatioDelta(aTimed.Weighted, bTimed.Weighted), 2),
			DeltaPct: roundDelta(shared.PercentDelta(aTimed.Weighted, bTimed.Weighted), 1),
			Winner:   weightedWinner, Scope: scope,
			Confidence:   weightedConf,
			N:            minTimed,
			HigherBetter: true,
			Note:         weightedNote,
		}, aKey, bKey),
		makeVerdict(verdictInput{
			Metric: "generation speed (median step)", Unit: "tok/s",
			A: anyOrNil(aTimed.Median), B: anyOrNil(bTimed.Median),
			Ratio:    roundRatio(shared.RatioDelta(aTimed.Median, bTimed.Median), 2),
			DeltaPct: roundDelta(shared.PercentDelta(aTimed.Median, bTimed.Median), 1),
			Winner:   medianWinner, Scope: scope,
			Confidence:   medianConf,
			N:            minTimed,
			HigherBetter: true,
			Note:         medianNote,
		}, aKey, bKey),
		makeVerdict(verdictInput{
			Metric: "cost per 1k generated", Unit: "$/1k generated",
			A: anyOrNil(aPer1k), B: anyOrNil(bPer1k),
			Ratio:    roundRatio(shared.RatioDelta(aPer1k, bPer1k), 2),
			DeltaPct: roundDelta(shared.PercentDelta(aPer1k, bPer1k), 1),
			Winner:   per1kWinner, Scope: scope,
			Confidence:   per1kConf,
			N:            minSteps,
			HigherBetter: false,
			Note:         per1kNote,
		}, aKey, bKey),
		makeVerdict(verdictInput{
			Metric: "cost per step", Unit: "$/step",
			A: anyOrNil(aPerStep), B: anyOrNil(bPerStep),
			Ratio:    roundRatio(shared.RatioDelta(aPerStep, bPerStep), 2),
			DeltaPct: roundDelta(shared.PercentDelta(aPerStep, bPerStep), 1),
			Winner:   perStepWinner, Scope: scope,
			Confidence:   perStepConf,
			N:            minSteps,
			HigherBetter: false,
			Note:         perStepNote,
		}, aKey, bKey),
		makeVerdict(verdictInput{
			Metric: "spend per session", Unit: "$/session",
			A: anyOrNil(aSpendSession), B: anyOrNil(bSpendSession),
			Ratio:    roundRatio(shared.RatioDelta(aSpendSession, bSpendSession), 2),
			DeltaPct: roundDelta(shared.PercentDelta(aSpendSession, bSpendSession), 1),
			Winner:   spendWinner, Scope: "all",
			Confidence:   confidenceFor(shared.RatioDelta(aSpendSession, bSpendSession), minSessions, "all", spendWinner),
			N:            minSessions,
			HigherBetter: false,
			Note:         "All-model spend; session mixes differ, so this is not a per-task cost. Spend basis " + spendBasisNote,
		}, aKey, bKey),
		makeVerdict(verdictInput{
			Metric: "cache hit rate", Unit: "% of input",
			A: anyOrNil(aHit), B: anyOrNil(bHit),
			Ratio:   roundRatio(shared.RatioDelta(aHit, bHit), 4),
			DeltaPp: roundDelta(shared.PointDelta(aHit, bHit), 2),
			Winner:  cacheWinner, Scope: scope,
			Confidence:   confidenceFor(shared.RatioDelta(aHit, bHit), minSteps, scope, cacheWinner),
			N:            minSteps,
			HigherBetter: true,
			Note:         scopeNote + "; <1 pp or <5% relative is a tie",
		}, aKey, bKey),
	}
}

func makeVerdict(input verdictInput, aKey, bKey string) map[string]any {
	return map[string]any{
		"metric":       input.Metric,
		"unit":         input.Unit,
		aKey:           input.A,
		bKey:           input.B,
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

func sharedVerdictConfidence(
	a, b *toolAggregate,
	families familySummary,
	ratio *float64,
	n int64,
	scope, winner, metric, baseNote string,
) (string, string) {
	aKey, bKey := sideIDs(a, b)
	conf := confidenceFor(ratio, n, scope, winner)
	if scope != "shared" || (conf != "clear" && conf != "weak") || len(families.Shared) <= 1 {
		return conf, baseNote
	}
	topFamily := ""
	var topTokens int64
	for _, name := range families.Shared {
		aFam := a.families[name]
		bFam := b.families[name]
		if aFam == nil || bFam == nil {
			continue
		}
		tokens := aFam.generated + bFam.generated
		if topFamily == "" || tokens > topTokens {
			topFamily = name
			topTokens = tokens
		}
	}
	if topFamily == "" {
		return conf, baseNote
	}
	aFam := a.families[topFamily]
	bFam := b.families[topFamily]
	if aFam.timed.n < lowSampleN || bFam.timed.n < lowSampleN {
		return conf, baseNote
	}
	var topWinner string
	switch metric {
	case "weighted":
		topWinner = shared.WinnerWithTolerance(aFam.timed.summary().Weighted, bFam.timed.summary().Weighted, true, tieRelative, aKey, bKey)
	case "median":
		topWinner = shared.WinnerWithTolerance(aFam.timed.summary().Median, bFam.timed.summary().Median, true, tieRelative, aKey, bKey)
	case "per1kGenerated":
		aVal := shared.R4(shared.PerKThousand(aFam.cost, aFam.generated))
		bVal := shared.R4(shared.PerKThousand(bFam.cost, bFam.generated))
		topWinner = shared.WinnerWithTolerance(aVal, bVal, false, tieRelative, aKey, bKey)
	case "perStep":
		aVal := shared.R4(shared.DivPtr(aFam.cost, float64(aFam.steps)))
		bVal := shared.R4(shared.DivPtr(bFam.cost, float64(bFam.steps)))
		topWinner = shared.WinnerWithTolerance(aVal, bVal, false, tieRelative, aKey, bKey)
	}
	if topWinner != "" && topWinner != "tie" && topWinner != "n/a" && topWinner != winner {
		return "confounded", baseNote + "; confounded by shared-family volume mix (reverses " + topFamily + ")"
	}
	return conf, baseNote
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

// upperID renders a tool id in shared-model key form: oc -> OC, cmd -> CMD,
// muse -> MUSE.
func upperID(id string) string {
	return strings.ToUpper(id)
}

// outcomeNote names the compared tools that record no session outcome.
// OpenCode and Muse derive outcomes; Command Code emits "unknown".
func outcomeNote(aKey, bKey string) string {
	var lacking []string
	for _, side := range []string{aKey, bKey} {
		if side != shared.ToolOC && side != shared.ToolMuse {
			lacking = append(lacking, shared.DisplayName(side))
		}
	}
	switch len(lacking) {
	case 1:
		return fmt.Sprintf("Per-completed-task cost is unavailable: %s records no session outcome, so spend per session is the closest operational unit.", lacking[0])
	case 2:
		return fmt.Sprintf("Per-completed-task cost is unavailable: %s and %s record no session outcomes, so spend per session is the closest operational unit.", lacking[0], lacking[1])
	default:
		return "Per-completed-task cost is unavailable, so spend per session is the closest operational unit."
	}
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

func validateComparison(doc map[string]any, a, b, aShared, bShared *toolAggregate) []string {
	aKey, bKey := sideIDs(a, b)
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
	validateSide(aKey, a)
	validateSide(bKey, b)
	validateSide(aKey+"-shared", aShared)
	validateSide(bKey+"-shared", bShared)

	// Tool shares must cover every call exactly once.
	for _, side := range []struct {
		name string
		agg  *toolAggregate
	}{{aKey, a}, {bKey, b}, {aKey + "-shared", aShared}, {bKey + "-shared", bShared}} {
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
	if aShared.totals.Steps > a.totals.Steps {
		add("%s-shared steps %d exceed all-model steps %d", aKey, aShared.totals.Steps, a.totals.Steps)
	}
	if bShared.totals.Steps > b.totals.Steps {
		add("%s-shared steps %d exceed all-model steps %d", bKey, bShared.totals.Steps, b.totals.Steps)
	}
	likeForLike := doc["likeForLike"].(map[string]any)
	families, _ := likeForLike["families"].([]any)
	available, _ := likeForLike["available"].(bool)
	expectedAvailable := len(families) > 0 && aShared.totals.Steps > 0 && bShared.totals.Steps > 0
	if available != expectedAvailable {
		add("likeForLike.available = %v but %d shared families / %d a / %d b shared steps",
			available, len(families), aShared.totals.Steps, bShared.totals.Steps)
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
	}{{aKey, a}, {bKey, b}} {
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
