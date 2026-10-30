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

	sharedFamilies, ocOnly, cmdOnly := familyCounts(oc, cmd)
	doc := map[string]any{
		"schemaVersion":     shared.SchemaVersion,
		"comparisonVersion": shared.ComparisonVersion,
		"kind":              "comparison",
		"meta":              metaSection(oc, cmd, window, dataFamilyCounts(ocAgg, cmdAgg), sharedFamilies, ocOnly, cmdOnly, now),
		"totals":            totalsSection(ocAgg, cmdAgg),
		"throughput":        throughputSection(ocAgg, cmdAgg),
		"cost":              costSection(ocAgg, cmdAgg),
		"cache":             cacheSection(ocAgg, cmdAgg),
		"tools":             toolsSection(ocAgg, cmdAgg),
		"models":            modelsSection(ocAgg, cmdAgg),
		"strata":            strataSection(ocAgg, cmdAgg),
		"sessions":          sessionsSection(ocAgg, cmdAgg),
		"timeline":          timelineSection(ocAgg, cmdAgg, window),
		"verdicts":          verdictsSection(ocAgg, cmdAgg),
	}

	if issues := validateComparison(doc, ocAgg, cmdAgg); len(issues) > 0 {
		return nil, fmt.Errorf("compare: comparison invariant failure:\n- %s", joinIssues(issues))
	}
	return doc, nil
}

// ---- meta

func metaSection(
	oc, cmd *shared.Report,
	window comparisonWindow,
	dataFamilies, sharedFamilies, ocOnly, cmdOnly int,
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
			"shared":        sharedFamilies,
			"ocOnly":        ocOnly,
			"cmdOnly":       cmdOnly,
			"withDataInWin": dataFamilies,
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

func familyCounts(oc, cmd *shared.Report) (sharedN, ocOnly, cmdOnly int) {
	ocFamilies := make(map[string]struct{})
	cmdFamilies := make(map[string]struct{})
	for _, model := range oc.Models {
		ocFamilies[model.Family] = struct{}{}
	}
	for _, model := range cmd.Models {
		cmdFamilies[model.Family] = struct{}{}
	}
	for family := range ocFamilies {
		if _, ok := cmdFamilies[family]; ok {
			sharedN++
		} else {
			ocOnly++
		}
	}
	for family := range cmdFamilies {
		if _, ok := ocFamilies[family]; !ok {
			cmdOnly++
		}
	}
	return sharedN, ocOnly, cmdOnly
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
			"stepsPct":           roundDelta(shared.PercentDelta(floatPtr(oc.totals.Steps), floatPtr(cmd.totals.Steps)), 1),
			"toolCallsPct":       roundDelta(shared.PercentDelta(floatPtr(oc.totals.ToolCalls), floatPtr(cmd.totals.ToolCalls)), 1),
			"generatedTokensPct": roundDelta(shared.PercentDelta(floatPtr(oc.totals.GeneratedTokens), floatPtr(cmd.totals.GeneratedTokens)), 1),
			"inputTotalPct":      roundDelta(shared.PercentDelta(floatPtr(oc.totals.InputTotal), floatPtr(cmd.totals.InputTotal)), 1),
			"costUsdPct":         roundDelta(shared.PercentDelta(&oc.cost, &cmd.cost), 1),
			"totalSpendUsdPct":   roundDelta(shared.PercentDelta(&oc.totalSpend, &cmd.totalSpend), 1),
			"cacheHitRatePp":     roundDelta(shared.PointDelta(ocHit, cmdHit), 2),
		},
	}
}

func totalsCell(agg *toolAggregate) map[string]any {
	hit := shared.R4(shared.Rate(agg.totals.CacheRead, agg.totals.InputTotal))
	spendBasis := "full-report"
	if agg.window.Mode == "overlap" {
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
			"nPct":        roundDelta(shared.PercentDelta(floatPtr(ocSummary.N), floatPtr(cmdSummary.N)), 1),
			"tokensPct":   roundDelta(shared.PercentDelta(floatPtr(ocSummary.Tokens), floatPtr(cmdSummary.Tokens)), 1),
			"secondsPct":  roundDelta(shared.PercentDelta(&ocSummary.Seconds, &cmdSummary.Seconds), 1),
			"weightedPct": roundDelta(shared.PercentDelta(ocSummary.Weighted, cmdSummary.Weighted), 1),
			"medianPct":   roundDelta(shared.PercentDelta(ocSummary.Median, cmdSummary.Median), 1),
			"p90Pct":      roundDelta(shared.PercentDelta(ocSummary.P90, cmdSummary.P90), 1),
		},
		"winner": map[string]any{
			"weighted": shared.Winner(ocSummary.Weighted, cmdSummary.Weighted, true),
			"median":   shared.Winner(ocSummary.Median, cmdSummary.Median, true),
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
	return map[string]any{
		"oc":  ocCost,
		"cmd": cmdCost,
		"delta": map[string]any{
			"recordedPct":            roundDelta(shared.PercentDelta(floatPtrFloat(oc.cost), floatPtrFloat(cmd.cost)), 1),
			"totalSpendUsdPct":       roundDelta(shared.PercentDelta(&oc.totalSpend, &cmd.totalSpend), 1),
			"perStepPct":             roundDelta(shared.PercentDelta(optFloat(ocCost, "perStep"), optFloat(cmdCost, "perStep")), 1),
			"perMillionGeneratedPct": roundDelta(shared.PercentDelta(optFloat(ocCost, "perMillionGenerated"), optFloat(cmdCost, "perMillionGenerated")), 1),
			"per1kGeneratedPct":      roundDelta(shared.PercentDelta(ocPer1k, cmdPer1k), 1),
			"costPerSessionPct":      roundDelta(shared.PercentDelta(optFloat(ocCost, "costPerSession"), optFloat(cmdCost, "costPerSession")), 1),
		},
		"winner": map[string]any{
			"per1kGenerated": shared.Winner(ocPer1k, cmdPer1k, false),
			"totalSpendUsd":  shared.Winner(&oc.totalSpend, &cmd.totalSpend, false),
		},
	}
}

func costCell(agg *toolAggregate) map[string]any {
	perStep := shared.R4(shared.DivPtr(agg.cost, float64(agg.totals.Steps)))
	perMillion := shared.R2(shared.PerMillion(agg.cost, agg.totals.GeneratedTokens))
	per1k := shared.R4(shared.PerKThousand(agg.cost, agg.totals.GeneratedTokens))
	perSession := shared.R4(shared.DivPtr(agg.cost, float64(agg.totals.Sessions)))
	return map[string]any{
		"recorded":            shared.Round(agg.cost, 6),
		"errorCostUsd":        shared.Round(maxFloat(agg.errorCost, 0), 6),
		"totalSpendUsd":       shared.Round(agg.totalSpend, 6),
		"perStep":             anyOrNil(perStep),
		"perMillionGenerated": anyOrNil(perMillion),
		"per1kGenerated":      anyOrNil(per1k),
		"costPerSession":      anyOrNil(perSession),
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
			"hitRatePp":    roundDelta(shared.PointDelta(ocHit, cmdHit), 2),
			"writeSharePp": roundDelta(shared.PointDelta(ocWrite, cmdWrite), 2),
			"savingsPct":   roundDelta(shared.PercentDelta(floatPtrFloat(oc.report.Cache.SavingsUsd), floatPtrFloat(cmd.report.Cache.SavingsUsd)), 1),
		},
		"winner": map[string]any{
			"hitRate": shared.Winner(ocHit, cmdHit, true),
		},
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
	for name := range categoryNames {
		ocCall := oc.category[name]
		cmdCall := cmd.category[name]
		ocShare := callShare(ocCall, ocTotal)
		cmdShare := callShare(cmdCall, cmdTotal)
		categories = append(categories, map[string]any{
			"category": name,
			"oc":       callCell(ocCall, ocTotal),
			"cmd":      callCell(cmdCall, cmdTotal),
			"delta": map[string]any{
				"sharePp":      roundDelta(shared.PointDelta(ocShare, cmdShare), 2),
				"callsPct":     roundDelta(shared.PercentDelta(callCallsPtr(ocCall), callCallsPtr(cmdCall)), 1),
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
			"oc":       ocTotal,
			"cmd":      cmdTotal,
			"deltaPct": roundDelta(shared.PercentDelta(floatPtr(ocTotal), floatPtr(cmdTotal)), 1),
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
	families := make([]any, 0, len(oc.families)+len(cmd.families))
	for _, family := range sortedFamilies(oc, cmd) {
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
		families = append(families, map[string]any{
			"family": family,
			"shared": sharedFamily,
			"oc":     ocCell,
			"cmd":    cmdCell,
			"delta": map[string]any{
				"stepsPct":           roundDelta(shared.PercentDelta(optFloat(ocCell, "steps"), optFloat(cmdCell, "steps")), 1),
				"generatedTokensPct": roundDelta(shared.PercentDelta(optFloat(ocCell, "generatedTokens"), optFloat(cmdCell, "generatedTokens")), 1),
				"weightedPct":        roundDelta(shared.PercentDelta(ocWeighted, cmdWeighted), 1),
				"medianPct":          roundDelta(shared.PercentDelta(ocMedian, cmdMedian), 1),
				"p90Pct":             roundDelta(shared.PercentDelta(optFloat(ocCell, "p90"), optFloat(cmdCell, "p90")), 1),
				"per1kGeneratedPct":  roundDelta(shared.PercentDelta(ocPer1k, cmdPer1k), 1),
				"cacheHitRatePp":     roundDelta(shared.PointDelta(optFloat(ocCell, "cacheHitRate"), optFloat(cmdCell, "cacheHitRate")), 2),
			},
			"winner": map[string]any{
				"weighted":       shared.Winner(ocWeighted, cmdWeighted, true),
				"median":         shared.Winner(ocMedian, cmdMedian, true),
				"per1kGenerated": shared.Winner(ocPer1k, cmdPer1k, false),
			},
		})
	}
	return map[string]any{
		"oc":       modelCells(oc),
		"cmd":      modelCells(cmd),
		"families": families,
	}
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
			"p90":              anyOrNil(summary.P90),
			"per1kGenerated":   anyOrNil(per1k),
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
			"timedN":          int64(0),
			"weighted":        nil,
			"median":          nil,
			"p90":             nil,
		}
	}
	hit := shared.R4(shared.Rate(agg.cacheRead, agg.inputTotal))
	per1k := shared.R4(shared.PerKThousand(agg.cost, agg.generated))
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
		"timedN":          summary.N,
		"weighted":        anyOrNil(summary.Weighted),
		"median":          anyOrNil(summary.Median),
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
				"weightedPct": roundDelta(shared.PercentDelta(optFloat(ocCell, "weighted"), optFloat(cmdCell, "weighted")), 1),
				"medianPct":   roundDelta(shared.PercentDelta(optFloat(ocCell, "median"), optFloat(cmdCell, "median")), 1),
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
					"weightedPct": deltaWeighted,
					"medianPct":   roundDelta(shared.PercentDelta(optFloat(ocCell, "median"), optFloat(cmdCell, "median")), 1),
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

// ---- verdicts

func verdictsSection(oc, cmd *toolAggregate) []any {
	ocTimed := oc.timed.summary()
	cmdTimed := cmd.timed.summary()
	ocPer1k := shared.R4(shared.PerKThousand(oc.cost, oc.totals.GeneratedTokens))
	cmdPer1k := shared.R4(shared.PerKThousand(cmd.cost, cmd.totals.GeneratedTokens))
	ocHit := shared.R4(shared.Rate(oc.totals.CacheRead, oc.totals.InputTotal))
	cmdHit := shared.R4(shared.Rate(cmd.totals.CacheRead, cmd.totals.InputTotal))
	ocPerSession := shared.R1(shared.DivPtr(float64(oc.totals.GeneratedTokens), float64(oc.totals.Sessions)))
	cmdPerSession := shared.R1(shared.DivPtr(float64(cmd.totals.GeneratedTokens), float64(cmd.totals.Sessions)))

	verdicts := []any{
		verdict("generation speed (weighted)", "tok/s", anyOrNil(ocTimed.Weighted), anyOrNil(cmdTimed.Weighted),
			roundDelta(shared.PercentDelta(ocTimed.Weighted, cmdTimed.Weighted), 1), nil,
			shared.Winner(ocTimed.Weighted, cmdTimed.Weighted, true), true,
			"Canonical generatedTokens divided by plausible timed LLM seconds"),
		verdict("generation speed (median step)", "tok/s", anyOrNil(ocTimed.Median), anyOrNil(cmdTimed.Median),
			roundDelta(shared.PercentDelta(ocTimed.Median, cmdTimed.Median), 1), nil,
			shared.Winner(ocTimed.Median, cmdTimed.Median, true), true,
			"Median per-step generation rate; resistant to long tail steps"),
		verdict("cost efficiency", "$/1k generated", anyOrNil(ocPer1k), anyOrNil(cmdPer1k),
			roundDelta(shared.PercentDelta(ocPer1k, cmdPer1k), 1), nil,
			shared.Winner(ocPer1k, cmdPer1k, false), false,
			"Recorded cost per 1,000 generated (visible + reasoning) tokens"),
		verdict("cache hit rate", "% of input", anyOrNil(ocHit), anyOrNil(cmdHit),
			nil, roundDelta(shared.PointDelta(ocHit, cmdHit), 2),
			shared.Winner(ocHit, cmdHit, true), true,
			"cacheReadTokens / inputTotal; percentage-point delta"),
		verdict("total spend", "USD", shared.Round(oc.totalSpend, 6), shared.Round(cmd.totalSpend, 6),
			roundDelta(shared.PercentDelta(&oc.totalSpend, &cmd.totalSpend), 1), nil,
			shared.Winner(&oc.totalSpend, &cmd.totalSpend, false), false,
			"Recorded cost plus skipped/error ledger cost"),
		verdict("generated tokens per session", "tokens", anyOrNil(ocPerSession), anyOrNil(cmdPerSession),
			roundDelta(shared.PercentDelta(ocPerSession, cmdPerSession), 1), nil,
			shared.Winner(ocPerSession, cmdPerSession, true), true,
			"Session-scale output; independent of throughput"),
	}
	return verdicts
}

func verdict(metric, unit string, oc, cmd any, deltaPct, deltaPp any, winner string, higherBetter bool, note string) map[string]any {
	return map[string]any{
		"metric":       metric,
		"unit":         unit,
		"oc":           oc,
		"cmd":          cmd,
		"deltaPct":     deltaPct,
		"deltaPp":      deltaPp,
		"winner":       winner,
		"higherBetter": higherBetter,
		"note":         note,
	}
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

func validateComparison(doc map[string]any, oc, cmd *toolAggregate) []string {
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
