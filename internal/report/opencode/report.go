// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"math"
	"sort"

	"github.com/jadmadi/agentmeter/internal/contract"

	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
	"github.com/jadmadi/agentmeter/internal/stats"
)

// costModel is one cost.byModel entry plus the typed values the aggregate
// sections need.
type costModel struct {
	Model      string
	Short      string
	Steps      int
	Visible    int64
	Reasoning  int64
	Generated  int64
	Inp        int64
	CacheRead  int64
	CacheWrite int64
	InputTotal int64

	Recorded                float64
	TokenCost               float64
	FreshCost               *float64
	CacheReadCost           *float64
	CacheWriteCost          *float64
	OutputCost              *float64
	UnattributedCost        float64
	CacheSavings            *float64
	CacheReadSavings        *float64
	CacheWritePenalty       *float64
	EffectiveInputPrice     *float64
	Fit                     *priceFit
	CostPerStep             *float64
	CostPerMillionGenerated *float64
}

// Map renders the cost.byModel schema entry.
func (c *costModel) Map() map[string]any {
	var reasoningShare any
	if c.Generated != 0 {
		reasoningShare = frac(float64(c.Reasoning)/float64(c.Generated), 4)
	}
	var cacheHitRate, cacheWriteShare any
	if c.InputTotal != 0 {
		cacheHitRate = frac(float64(c.CacheRead)/float64(c.InputTotal), 4)
		cacheWriteShare = frac(float64(c.CacheWrite)/float64(c.InputTotal), 4)
	}
	var freshRate, cacheRate, cacheWriteRate, outputRate, priceR2 any
	if c.Fit != nil {
		freshRate = c.Fit.InputRate
		cacheRate = c.Fit.CacheReadRate
		cacheWriteRate = c.Fit.CacheWriteRate
		outputRate = c.Fit.OutputRate
		if c.Fit.R2 != nil {
			priceR2 = *c.Fit.R2
		}
	}
	return obj(
		"model", c.Model,
		"short", c.Short,
		"steps", c.Steps,
		"outputTokens", c.Visible,
		"visibleOutputTokens", c.Visible,
		"reasoningTokens", c.Reasoning,
		"generatedTokens", c.Generated,
		"reasoningShare", reasoningShare,
		"inputTokens", c.InputTotal,
		"inputRawTokens", c.Inp,
		"freshInputTokens", c.Inp,
		"inputTotal", c.InputTotal,
		"cacheReadTokens", c.CacheRead,
		"cacheWriteTokens", c.CacheWrite,
		"cacheHitRate", cacheHitRate,
		"cacheWriteShare", cacheWriteShare,
		"inputIncludesCache", false,
		"inputSemanticsSource", "normalized",
		"recordedCost", frac(c.Recorded, 6),
		"tokenCost", frac(c.TokenCost, 6),
		"freshCost", optFrac(c.FreshCost, 6),
		"cacheReadCost", optFrac(c.CacheReadCost, 6),
		"cacheWriteCost", optFrac(c.CacheWriteCost, 6),
		"outputCost", optFrac(c.OutputCost, 6),
		"unattributedCost", c.UnattributedCost,
		"cacheSavings", optFrac(c.CacheSavings, 6),
		"cacheReadSavings", optFrac(c.CacheReadSavings, 6),
		"cacheWritePenalty", optFrac(c.CacheWritePenalty, 6),
		"effectiveInputPrice", optFrac(c.EffectiveInputPrice, 4),
		"freshInputRate", freshRate,
		"cacheRate", cacheRate,
		"cacheWriteRate", cacheWriteRate,
		"outputRate", outputRate,
		"priceR2", priceR2,
		"costPerStep", optFrac(c.CostPerStep, 4),
		"costPerMillionGenerated", optFrac(c.CostPerMillionGenerated, 2),
		"costPerMillionOutput", optFrac(c.CostPerMillionGenerated, 2),
	)
}

// report assembles every schema v3 section.
func (b *builder) report() map[string]any {
	rows := b.rows
	ledger := b.ledger
	models := b.models

	// ---- per model
	byModel := make([]map[string]any, 0, len(models))
	for _, model := range models {
		rs := filterRows(rows, func(row *ocsource.Row) bool { return row.Model == model })
		ts := filterRows(b.timed, func(row *ocsource.Row) bool { return row.Model == model })
		summary := summarize(ts)
		fit := b.fits[fitKey{model, "all"}]
		if fit == nil {
			fit = b.fits[fitKey{model, "text"}]
		}
		reasoning := b.reasoningSummary(rs)
		var modelGen, modelReas, cacheRead, cacheWrite, fresh int64
		for _, row := range rs {
			modelGen += row.Gen
			modelReas += row.Reas
			cacheRead += row.CacheRead
			cacheWrite += row.CacheWrite
			fresh += row.Inp
		}
		totalIn := fresh + cacheRead + cacheWrite
		modelTiming := map[string]int{}
		for _, row := range rs {
			modelTiming[row.TimingBucket]++
		}
		modelLedger := b.modelLedger[model]
		errorSteps := countLedger(modelLedger, "error")
		emptySteps := countLedger(modelLedger, "empty")
		errorCost := ledgerCost(modelLedger, "error")
		skippedCost := fsumLedger(modelLedger)
		rates := make([]float64, len(ts))
		for i, row := range ts {
			rates[i] = float64(row.Gen) / *row.DT
		}
		var medianCI any
		if ci, ok := stats.BootstrapMedianCI(rates, 200, stats.StableSeed(model), 0.05); ok {
			medianCI = []any{ci[0], ci[1]}
		}
		var pureRate, fixedSeconds, fitR2Value any
		if fit != nil {
			pureRate = fit.PureRate
			fixedSeconds = fit.FixedSeconds
			if fit.HasR2 {
				fitR2Value = fit.R2
			}
		}
		var reasoningShare, cacheHitRate, errorRate any
		if modelGen != 0 {
			reasoningShare = frac(float64(modelReas)/float64(modelGen), 4)
		}
		if totalIn != 0 {
			cacheHitRate = frac(float64(cacheRead)/float64(totalIn), 4)
		}
		if len(rs)+errorSteps != 0 {
			errorRate = frac(float64(errorSteps)/float64(len(rs)+errorSteps), 4)
		}
		var costUsd float64
		costs := make([]float64, len(rs))
		for i, row := range rs {
			costs[i] = row.Cost
		}
		costUsd = fsumFloats(costs)

		timing := map[string]any{}
		for _, key := range ocsource.TimingReasons {
			timing[key] = modelTiming[key]
		}
		byModel = append(byModel, obj(
			"model", model,
			"short", b.modelShort[model],
			"provider", b.modelProvider[model],
			"steps", len(rs),
			"toolSteps", countRows(rs, func(row *ocsource.Row) bool { return row.Tool }),
			"textSteps", countRows(rs, func(row *ocsource.Row) bool { return !row.Tool }),
			"outputTokens", int64(sumField(rs, func(row *ocsource.Row) int64 { return row.Out })),
			"visibleOutputTokens", int64(sumField(rs, func(row *ocsource.Row) int64 { return row.Out })),
			"reasoningTokens", modelReas,
			"generatedTokens", modelGen,
			"reasoningShare", reasoningShare,
			"reasoningSteps", reasoning["steps"],
			"inputTokens", totalIn,
			"inputRawTokens", fresh,
			"freshInputTokens", fresh,
			"inputTotal", totalIn,
			"cacheReadTokens", cacheRead,
			"cacheWriteTokens", cacheWrite,
			"cacheHitRate", cacheHitRate,
			"inputIncludesCache", false,
			"inputSemanticsSource", "normalized",
			"costUsd", frac(costUsd, 6),
			"timedN", summary["n"],
			"rated", len(ts) >= 30,
			"lowSample", len(ts) < 30,
			"medianCI", medianCI,
			"weighted", summary["weighted"],
			"median", summary["median"],
			"visibleMedian", summary["visibleMedian"],
			"p90", summary["p90"],
			"ttftMedian", summary["ttftMedian"],
			"ttftN", summary["ttftN"],
			"decodeMedian", summary["decodeMedian"],
			"decodeN", summary["decodeN"],
			"pureRate", pureRate,
			"fixedSeconds", fixedSeconds,
			"r2", fitR2Value,
			"fitUsable", fitUsable(fit),
			"timing", timing,
			"rejected", len(rs)-modelTiming["plausible"],
			"errorSteps", errorSteps,
			"emptySteps", emptySteps,
			"ledgerSteps", len(modelLedger),
			"errorCostUsd", frac(errorCost, 6),
			"skippedCostUsd", frac(skippedCost, 6),
			"totalSpendUsd", frac(costUsd+skippedCost, 6),
			"errorRate", errorRate,
		))
	}

	// ---- direct standardization over turn type x output-size strata
	sizeDefs := []struct {
		Label string
		Low   int64
		High  int64
	}{
		{"20-100", 20, 100},
		{"100-300", 100, 300},
		{"300-1k", 300, 1000},
		{"1k-5k", 1000, 5000},
		{"5k+", 5000, 1_000_000_000_000},
	}
	sizeBucket := func(value int64) (string, bool) {
		for _, def := range sizeDefs {
			if def.Low <= value && value < def.High {
				return def.Label, true
			}
		}
		return "", false
	}
	globalStrata := map[string]int{}
	for _, row := range b.timed {
		label, ok := sizeBucket(row.Gen)
		if !ok {
			continue
		}
		globalStrata[stratumKey(row.Tool, label)]++
	}
	for _, item := range byModel {
		model := item["model"].(string)
		strata := map[string][]float64{}
		var order []string
		for _, row := range b.timed {
			if row.Model != model {
				continue
			}
			label, ok := sizeBucket(row.Gen)
			if !ok {
				continue
			}
			key := stratumKey(row.Tool, label)
			if _, seen := strata[key]; !seen {
				order = append(order, key)
			}
			strata[key] = append(strata[key], float64(row.Gen) / *row.DT)
		}
		ordered := make([]stats.Stratum, 0, len(order))
		standardizedN := 0
		for _, key := range order {
			values := strata[key]
			if len(values) < 5 {
				continue
			}
			medianValue, ok := stats.Median(values)
			if !ok {
				continue
			}
			ordered = append(ordered, stats.Stratum{Key: key, Value: medianValue})
			standardizedN += len(values)
		}
		if value, covered, ok := stats.StandardizeOrdered(ordered, globalStrata); ok {
			item["standardizedRate"] = stats.R1(value)
			item["standardizedCoverage"] = covered
			item["standardizedN"] = standardizedN
		} else {
			item["standardizedRate"] = nil
			item["standardizedCoverage"] = 0
			item["standardizedN"] = standardizedN
		}
	}

	// ---- cache & price
	priceFits := map[string]*priceFit{}
	for _, model := range models {
		if fit := fitPrices(filterRows(rows, func(row *ocsource.Row) bool { return row.Model == model })); fit != nil {
			priceFits[model] = fit
		}
	}
	costByModel := make([]*costModel, 0, len(models))
	for _, model := range models {
		costByModel = append(costByModel, costSummary(model, b.modelShort[model],
			filterRows(rows, func(row *ocsource.Row) bool { return row.Model == model }),
			priceFits[model]))
	}
	var (
		totalInput      int64
		totalRawInput   int64
		totalFreshInput int64
		totalCacheRead  int64
		totalCacheWrite int64
		totalOutput     int64
		totalReasoning  int64
		totalGenerated  int64
	)
	for _, item := range costByModel {
		totalInput += item.InputTotal
		totalRawInput += item.Inp
		totalFreshInput += item.Inp
		totalCacheRead += item.CacheRead
		totalCacheWrite += item.CacheWrite
		totalOutput += item.Visible
		totalReasoning += item.Reasoning
		totalGenerated += item.Generated
	}
	costs := make([]float64, len(rows))
	for i, row := range rows {
		costs[i] = row.Cost
	}
	totalCost := fsumFloats(costs)
	ledgerCosts := make([]float64, len(ledger))
	undatedLedgerCosts := make([]float64, 0, len(ledger))
	for i, entry := range ledger {
		ledgerCosts[i] = entry.Cost
		if entry.CreatedMs == 0 {
			undatedLedgerCosts = append(undatedLedgerCosts, entry.Cost)
		}
	}
	errorCost := fsumFloats(ledgerCosts)
	undatedErrorCost := fsumFloats(undatedLedgerCosts)
	totalSpend := totalCost + errorCost

	unattributedValues := make([]float64, 0, len(costByModel))
	var fittedCacheRead int64
	fittedModels := 0
	for _, item := range costByModel {
		if item.FreshCost == nil {
			unattributedValues = append(unattributedValues, frac(item.Recorded, 6))
		}
		if item.Fit != nil && item.Fit.InputRate > 0 {
			fittedCacheRead += item.CacheRead
			fittedModels++
		}
	}
	unattributedCost := fsumFloats(unattributedValues)

	cacheSavingsValues := make([]float64, 0, len(costByModel))
	cacheReadSavingsValues := make([]float64, 0, len(costByModel))
	cacheWritePenaltyValues := make([]float64, 0, len(costByModel))
	cacheReadCostValues := make([]float64, 0, len(costByModel))
	cacheWriteCostValues := make([]float64, 0, len(costByModel))
	tokenCostValues := make([]float64, 0, len(costByModel))
	freshCostValues := make([]float64, 0, len(costByModel))
	outputCostValues := make([]float64, 0, len(costByModel))
	for _, item := range costByModel {
		cacheSavingsValues = append(cacheSavingsValues, frac(orZero(item.CacheSavings), 6))
		cacheReadSavingsValues = append(cacheReadSavingsValues, frac(orZero(item.CacheReadSavings), 6))
		cacheWritePenaltyValues = append(cacheWritePenaltyValues, frac(orZero(item.CacheWritePenalty), 6))
		cacheReadCostValues = append(cacheReadCostValues, frac(orZero(item.CacheReadCost), 6))
		cacheWriteCostValues = append(cacheWriteCostValues, frac(orZero(item.CacheWriteCost), 6))
		tokenCostValues = append(tokenCostValues, frac(item.TokenCost, 6))
		freshCostValues = append(freshCostValues, frac(orZero(item.FreshCost), 6))
		outputCostValues = append(outputCostValues, frac(orZero(item.OutputCost), 6))
	}

	var hitRate, writeShare, savingsCoverage any
	if totalInput != 0 {
		hitRate = frac(float64(totalCacheRead)/float64(totalInput), 4)
		writeShare = frac(float64(totalCacheWrite)/float64(totalInput), 4)
	}
	if totalCacheRead != 0 {
		savingsCoverage = frac(float64(fittedCacheRead)/float64(totalCacheRead), 4)
	}
	cacheSection := obj(
		"inputTokens", totalInput,
		"inputRawTokens", totalRawInput,
		"freshInputTokens", totalFreshInput,
		"cacheReadTokens", totalCacheRead,
		"cacheWriteTokens", totalCacheWrite,
		"outputTokens", totalOutput,
		"visibleOutputTokens", totalOutput,
		"reasoningTokens", totalReasoning,
		"generatedTokens", totalGenerated,
		"hitRate", hitRate,
		"writeShare", writeShare,
		"savings", frac(fsumFloats(cacheSavingsValues), 6),
		"readSavings", frac(fsumFloats(cacheReadSavingsValues), 6),
		"writePenalty", frac(fsumFloats(cacheWritePenaltyValues), 6),
		"readCost", frac(fsumFloats(cacheReadCostValues), 6),
		"writeCost", frac(fsumFloats(cacheWriteCostValues), 6),
		"tokenCost", frac(fsumFloats(tokenCostValues), 6),
		"fittedModels", fittedModels,
		"unfittedModels", len(models)-fittedModels,
		"savingsCoverage", savingsCoverage,
	)
	var perStep, perMillionGenerated any
	if len(rows) != 0 {
		perStep = frac(totalCost/float64(len(rows)), 4)
	}
	if totalGenerated != 0 {
		perMillionGenerated = frac(totalCost/float64(totalGenerated)*1e6, 2)
	}
	costByModelOut := make([]any, len(costByModel))
	for i, item := range costByModel {
		costByModelOut[i] = item.Map()
	}
	costSection := obj(
		"recorded", frac(totalCost, 6),
		"errorCostUsd", frac(errorCost, 6),
		"undatedErrorCostUsd", frac(undatedErrorCost, 6),
		"totalSpendUsd", frac(totalSpend, 6),
		"tokenCost", frac(fsumFloats(tokenCostValues), 6),
		"freshCost", frac(fsumFloats(freshCostValues), 6),
		"cacheReadCost", frac(fsumFloats(cacheReadCostValues), 6),
		"cacheWriteCost", frac(fsumFloats(cacheWriteCostValues), 6),
		"outputCost", frac(fsumFloats(outputCostValues), 6),
		"unattributedCost", frac(unattributedCost, 6),
		"perStep", perStep,
		"perMillionGenerated", perMillionGenerated,
		"perMillionOutput", perMillionGenerated,
		"byModel", costByModelOut,
	)

	// ---- tool mix: attribute every call and allocate non-overlapping time
	for _, row := range rows {
		allocations := ocsource.AllocateToolTime(row.ToolDetails)
		for i := range row.ToolDetails {
			row.ToolDetails[i].Allocated = allocations[i]
		}
	}
	toolStepTime := 0.0
	{
		values := make([]float64, 0, len(rows))
		for _, row := range rows {
			if len(row.ToolDetails) > 0 && row.TotalDT != nil {
				values = append(values, *row.TotalDT)
			}
		}
		toolStepTime = sumFloats(values)
	}
	tools, totalCalls := toolsSection(rows, b.th, toolStepTime)

	// ---- behavior slices
	thinking := obj(
		"with", b.behaviorSlice(filterRows(rows, func(row *ocsource.Row) bool { return row.Thinking }), len(rows)),
		"without", b.behaviorSlice(filterRows(rows, func(row *ocsource.Row) bool { return !row.Thinking }), len(rows)),
	)
	agents := b.sliceSection(rows, len(rows), "agent", func(row *ocsource.Row) string { return row.Agent })
	variants := b.sliceSection(rows, len(rows), "variant", func(row *ocsource.Row) string { return row.Variant })
	entrypoints := b.entrypointSection(rows, len(rows))
	projects := b.projectSection(rows)

	runtimeSection := runtimeSummary(rows)

	// ---- health / coverage
	errorSeconds := 0.0
	{
		values := make([]float64, len(ledger))
		for i, entry := range ledger {
			values[i] = orZero(entry.TotalDT)
		}
		errorSeconds = fsumFloats(values)
	}
	timingCounts := b.timingCounts
	healthTiming := map[string]any{}
	for _, key := range ocsource.TimingReasons {
		healthTiming[key] = timingCounts[key]
	}
	sessionHealth := obj(
		"events", counterList(b.loaded.Events.Types.MostCommon(), "type", "count"),
		"finishes", counterList(b.loaded.Events.Finishes.MostCommon(), "finish", "count"),
		"toolErrors", counterList(b.loaded.Events.ToolErrors.MostCommon(), "tool", "count"),
		"toolCalls", b.loaded.Events.ToolCalls.Total(),
		"errors", b.loaded.Events.Errors,
		"noStream", b.loaded.Events.NoStream,
		"compactions", b.loaded.Events.Compactions,
		"modelSwitches", b.loaded.Events.ModelSwitches,
		"agentSwitches", b.loaded.Events.AgentSwitches,
		"idle", b.loaded.Events.Idle,
		"shell", b.loaded.Events.Shell,
		"sessionsTotal", b.loaded.SessionsTotal,
		"sessionsRoot", b.loaded.SessionsRoot,
		"sessionsSubagent", b.loaded.SessionsSubagent,
		"sessionsInWindow", len(b.loaded.Sessions),
		"errorSteps", b.loaded.Events.Errors,
		"emptySteps", b.loaded.EmptySteps,
		"ledgerSteps", len(ledger),
		"errorCostUsd", frac(errorCost, 6),
		"errorSeconds", frac(errorSeconds, 3),
		"skippedCostUsd", frac(errorCost, 6),
		"duplicateSteps", b.loaded.Duplicates,
		"duplicateCostUsd", frac(b.loaded.DuplicateCost, 6),
		"duplicateTokens", b.loaded.DuplicateTokens,
		"negativeOverhead", b.loaded.Events.NegativeOverhead,
		"timing", healthTiming,
		"versions", counterList(b.loaded.Events.Versions.MostCommon(), "version", "sessions"),
	)
	coverage := obj(
		"timing", timingCounts["plausible"] > 0,
		"timingBreakdown", true,
		"streamed", countRows(rows, func(row *ocsource.Row) bool { return row.DT != nil }) > 0,
		"completed", countRows(rows, func(row *ocsource.Row) bool { return row.TotalDT != nil }) > 0,
		"toolRuntime", countRows(rows, func(row *ocsource.Row) bool { return row.ToolDT != nil }) > 0,
		"toolTimeAllocation", true,
		"reasoning", totalReasoning > 0,
		"cacheRead", totalCacheRead > 0,
		"cacheWrite", totalCacheWrite > 0,
		"titles", countRows(rows, func(row *ocsource.Row) bool { return row.SessionTitle != nil }) > 0,
		"parentSessions", b.loaded.SessionsSubagent > 0,
		"cost", totalCost > 0,
		"variants", countRows(rows, func(row *ocsource.Row) bool { return row.Variant != "default" }) > 0,
		"errors", b.loaded.Events.Errors > 0,
		"errorLedger", len(ledger) > 0,
		"skipLedger", len(ledger) > 0,
		"ttft", countRows(rows, func(row *ocsource.Row) bool { return row.TTFT != nil }) > 0,
		"overhead", countRows(rows, func(row *ocsource.Row) bool { return row.OverheadDT != nil }) > 0,
		"projectPaths", true,
		"inputSemantics", true,
	)
	timingInfo := obj(
		"scanned", b.loaded.Scanned,
		"considered", b.loaded.Considered,
		"noCreated", b.loaded.NoCreated,
		"empty", b.loaded.EmptySteps,
		"errorSteps", b.loaded.ErrorSteps,
		"ledger", len(ledger),
		"rows", len(rows),
		"plausible", timingCounts["plausible"],
		"noTiming", timingCounts["noTiming"],
		"tooShort", timingCounts["tooShort"],
		"tooShortDuration", timingCounts["tooShortDuration"],
		"tooLong", timingCounts["tooLong"],
		"stalled", timingCounts["tooLong"],
		"slowRate", timingCounts["slowRate"],
		"fastRate", timingCounts["fastRate"],
		"outOfRange", timingCounts["tooShortDuration"]+timingCounts["tooLong"],
		"implausibleRate", timingCounts["slowRate"]+timingCounts["fastRate"],
		"duplicates", b.loaded.Duplicates,
		"fingerprintCollisions", b.loaded.Collisions,
		"thresholds", b.th.Map(),
	)

	// ---- compact per-step rows and index lookups
	indexes := newLookups(b, rows)
	steps := b.stepsSection(rows, indexes)
	sessions := b.sessionsSection(rows, indexes)
	lookups := indexes.Map()

	fitsOut := make([]any, len(models))
	prices := make([]any, len(models))
	for i, model := range models {
		fitsOut[i] = obj(
			"all", fitMap(b.fits[fitKey{model, "all"}]),
			"text", fitMap(b.fits[fitKey{model, "text"}]),
			"tool", fitMap(b.fits[fitKey{model, "tool"}]),
		)
		var freshRate, cacheRate, cacheWriteRate, outputRate any
		if fit := priceFits[model]; fit != nil {
			freshRate = fit.InputRate
			cacheRate = fit.CacheReadRate
			cacheWriteRate = fit.CacheWriteRate
			outputRate = fit.OutputRate
		}
		prices[i] = obj(
			"fresh", freshRate,
			"cache", cacheRate,
			"cacheWrite", cacheWriteRate,
			"output", outputRate,
			"inputIncludesCache", false,
			"inputSemanticsSource", "normalized",
		)
	}

	// ---- distribution
	edges := []float64{0, 10, 20, 30, 40, 50, 66, 80, 100, 120, 150, 200, 250, 300, 400}
	labels := []any{
		"0-10", "10-20", "20-30", "30-40", "40-50", "50-66", "66-80",
		"80-100", "100-120", "120-150", "150-200", "200-250", "250-300", "300+",
	}
	histogram := obj(
		"labels", labels,
		"all", histogramCounts(b.timed, edges),
		"text", histogramCounts(b.text, edges),
	)
	sizeBuckets := bucketize(b.timed, []struct {
		Label string
		Low   int64
		High  int64
	}{
		{"20-100", 20, 100},
		{"100-300", 100, 300},
		{"300-600", 300, 600},
		{"600-1k", 600, 1000},
		{"1k-2k", 1000, 2000},
		{"2k-5k", 2000, 5000},
		{"5k+", 5000, 1_000_000_000},
	})

	// ---- daily, hourly, per-model daily
	daily, anomalies := b.dailySection(rows)
	dailyByModel := b.dailyByModelSection()
	hourly := b.hourlySection(rows)
	context := b.contextBuckets(b.timed)
	contextText := b.contextBuckets(b.text)
	compactionSection := b.compactionImpact()

	// ---- peaks / slowest / lowest rate
	extremes := b.extremesSection()

	// ---- timed summaries (the all summary carries the censoring fields)
	timedAll := summarize(b.timed)
	timedAll["maxIncluded"] = timedAll["max"]
	timedAll["maxObserved"] = r1OfSorted(extremes.observedRates)
	timedAll["maxRejected"] = r1OfSorted(extremes.rejectedRates)
	timedAll["maxRejectedReason"] = extremes.maxRejectedReason
	timedAll["minIncluded"] = extremes.minIncluded
	timedAll["maxDurationIncluded"] = extremes.maxDurationIncluded
	timedAll["maxDurationObserved"] = extremes.maxDurationObserved
	timedAll["maxDurationAny"] = extremes.maxDurationAny
	timedAll["upperCensored"] = timingInfo["fastRate"]
	timedAll["lowerCensored"] = timingInfo["slowRate"]
	timedAll["durationCensoredHigh"] = timingInfo["tooLong"]
	timedAll["durationCensoredLow"] = timingInfo["tooShortDuration"]

	timedSection := obj(
		"all", timedAll,
		"text", summarize(b.text),
		"tool", summarize(b.toolTimed),
	)

	// ---- totals
	var dateFrom, dateTo any
	if len(rows) > 0 {
		first := rows[0].Ts
		last := rows[0].Ts
		for _, row := range rows[1:] {
			if row.Ts.Before(first) {
				first = row.Ts
			}
			if row.Ts.After(last) {
				last = row.Ts
			}
		}
		dateFrom = first.Format("2006-01-02")
		dateTo = last.Format("2006-01-02")
	}
	allReasoning := b.reasoningSummary(rows)
	errorLedger := b.errorLedgerSection()

	var reasoningShare, reasoningTokenShare any
	if totalGenerated != 0 {
		reasoningShare = frac(float64(totalReasoning)/float64(totalGenerated), 4)
	}
	if v, ok := allReasoning["tokenShare"]; ok {
		reasoningTokenShare = v
	}
	_ = reasoningTokenShare
	totals := obj(
		"steps", len(rows),
		"toolSteps", countRows(rows, func(row *ocsource.Row) bool { return row.Tool }),
		"textSteps", countRows(rows, func(row *ocsource.Row) bool { return !row.Tool }),
		"toolCalls", totalCalls,
		"outputTokens", totalOutput,
		"visibleOutputTokens", totalOutput,
		"reasoningTokens", totalReasoning,
		"generatedTokens", totalGenerated,
		"reasoningShare", reasoningShare,
		"inputTokens", totalInput,
		"inputRawTokens", totalRawInput,
		"freshInputTokens", totalFreshInput,
		"inputTotal", totalInput,
		"cacheReadTokens", totalCacheRead,
		"cacheWriteTokens", totalCacheWrite,
		"costUsd", frac(totalCost, 6),
		"errorCostUsd", frac(errorCost, 6),
		"totalSpendUsd", frac(totalSpend, 6),
		"models", len(models),
	)

	// runtime.negativeOverhead is added after the runtime summary.
	runtimeSection["negativeOverhead"] = b.loaded.Events.NegativeOverhead

	// ---- meta
	var paceBaseValue, paceBasePct any
	if b.paceBase != nil && *b.paceBase != 0 {
		paceBaseValue = frac(*b.paceBase, 6)
		paceBasePct = frac(*b.paceBase*100, 1)
	}
	var paceBaseline any
	if b.paceBaseline != nil {
		paceBaseline = obj(
			"from", b.paceBaseline.From,
			"to", b.paceBaseline.To,
			"paceBase", paceBaseValue,
			"paceBasePct", paceBasePct,
			"n", b.paceBaseline.N,
		)
	}
	meta := obj(
		"generatedAt", b.now.UTC().Format("2006-01-02T15:04:05+00:00"),
		"source", "OpenCode SQLite",
		"dbLabel", dbLabel(b.opts.DBPath),
		"modelFilters", toAnyStrings(b.opts.ModelFilters),
		"models", len(models),
		"sessions", len(b.loaded.Sessions),
		"sessionsTotal", b.loaded.SessionsTotal,
		"sessionsRoot", b.loaded.SessionsRoot,
		"sessionsSubagent", b.loaded.SessionsSubagent,
		"stepsScanned", b.loaded.Scanned,
		"emptyStepsSkipped", b.loaded.EmptySteps,
		"errorSteps", b.loaded.ErrorSteps,
		"errorCostUsd", frac(errorCost, 6),
		"errorSeconds", frac(errorSeconds, 3),
		"duplicatesSkipped", b.loaded.Duplicates,
		"fingerprintCollisions", b.loaded.Collisions,
		"timing", timingInfo,
		"sourceWatermark", obj(
			"maxCreatedMs", b.loaded.MaxCreatedMs,
			"scanned", b.loaded.Scanned,
			"rows", len(rows),
			"duplicates", b.loaded.Duplicates,
		),
		"includeTitles", b.opts.IncludeTitles,
		"fullPaths", b.opts.FullPaths,
		"since", optionalString(b.opts.Since),
		"until", optionalString(b.opts.Until),
		"timezone", localNowLabel(b.now),
		"paceBase", paceBaseValue,
		"paceBasePct", paceBasePct,
		"paceBaseline", paceBaseline,
		"dateFrom", dateFrom,
		"dateTo", dateTo,
	)

	return obj(
		"schemaVersion", contract.SchemaVersion,
		"meta", meta,
		"totals", totals,
		"timed", timedSection,
		"byModel", mapSlice(byModel),
		"cache", cacheSection,
		"cost", costSection,
		"reasoning", allReasoning,
		"runtime", runtimeSection,
		"sessionHealth", sessionHealth,
		"coverage", coverage,
		"tools", tools,
		"thinking", thinking,
		"agents", agents,
		"variants", variants,
		"entrypoints", entrypoints,
		"projects", projects,
		"projectsTotal", len(projects),
		"errorLedger", errorLedger,
		"lookups", lookups,
		"fits", fitsOut,
		"fitsGlobal", obj(
			"text", fitMap(b.globalFits["text"]),
			"tool", fitMap(b.globalFits["tool"]),
		),
		"prices", prices,
		"steps", steps,
		"sessions", sessions,
		"regression", mapSlice(b.regressions),
		"histogram", histogram,
		"sizeBuckets", sizeBuckets,
		"daily", daily,
		"dailyByModel", dailyByModel,
		"hourly", hourly,
		"context", context,
		"contextText", contextText,
		"peaks", extremes.peaks,
		"slowest", extremes.slowest,
		"lowestRate", extremes.lowestRate,
		"rejectedExtremes", extremes.rejectedExtremes,
		"anomalies", anomalies,
		"fork", obj(
			"duplicateSteps", b.loaded.Duplicates,
			"duplicateTokens", b.loaded.DuplicateTokens,
			"duplicateCostUsd", frac(b.loaded.DuplicateCost, 6),
			"sessionsWithDuplicates", len(b.loaded.DuplicateSessions),
			"fingerprintCollisions", b.loaded.Collisions,
		),
		"compactionImpact", compactionSection,
		"compactionEvents", b.compactionEventsSection(),
		"modelChangeEvents", b.modelChangeEventsSection(),
	)
}

// stratumKey encodes the (turn type, output-size bucket) stratum.
func stratumKey(isTool bool, label string) string {
	if isTool {
		return "tool\x00" + label
	}
	return "text\x00" + label
}

// sumField sums an int64 row field.
func sumField(rows []*ocsource.Row, get func(*ocsource.Row) int64) int64 {
	var total int64
	for _, row := range rows {
		total += get(row)
	}
	return total
}

// countRows counts matching rows.
func countRows(rows []*ocsource.Row, keep func(*ocsource.Row) bool) int {
	count := 0
	for _, row := range rows {
		if keep(row) {
			count++
		}
	}
	return count
}

// countLedger counts ledger entries whose reason includes the flag.
func countLedger(entries []*ocsource.LedgerEntry, flag string) int {
	count := 0
	for _, entry := range entries {
		if entry.Reason == flag || entry.Reason == "both" {
			count++
		}
	}
	return count
}

// ledgerCost sums the cost of ledger entries whose reason includes the flag.
func ledgerCost(entries []*ocsource.LedgerEntry, flag string) float64 {
	values := make([]float64, 0, len(entries))
	for _, entry := range entries {
		if entry.Reason == flag || entry.Reason == "both" {
			values = append(values, entry.Cost)
		}
	}
	return sumFloats(values)
}

// r1OfSorted returns r1 of the largest element, or null.
func r1OfSorted(values []float64) any {
	if len(values) == 0 {
		return nil
	}
	return stats.R1(values[len(values)-1])
}

// mapSlice converts a typed slice to []any.
func mapSlice(items []map[string]any) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

// toAnyStrings converts []string to []any for the JSON encoder.
func toAnyStrings(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// optionalString renders "" as null.
func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

// counterList renders Counter.most_common() as [{"key": key, "count": n}, ...].
func counterList(items []ocsource.CounterItem, key, count string) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = obj(key, item.Key, count, item.Count)
	}
	return out
}

// sortStableOrders sorts items by a descending numeric key, preserving the
// original order for ties (Python's stable sorted(key=...)).
func sortStableOrders[T any](items []T, key func(T) float64, descending bool) {
	sort.SliceStable(items, func(i, j int) bool {
		if descending {
			return key(items[i]) > key(items[j])
		}
		return key(items[i]) < key(items[j])
	})
}

// float64Or translates an optional float into a sample value.
func float64Or(value *float64, fallback float64) float64 {
	if value == nil {
		return fallback
	}
	return *value
}

// finiteOrZero guards against a NaN slipping into a schema number.
func finiteOrZero(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}
