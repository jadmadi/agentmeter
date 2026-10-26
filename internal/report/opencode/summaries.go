// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"sort"

	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
	"github.com/jadmadi/agentmeter/internal/stats"
)

// summarize mirrors telemetry.py:summarize.
func summarize(rows []*ocsource.Row) map[string]any {
	if len(rows) == 0 {
		return obj(
			"n", 0,
			"tokens", int64(0),
			"visible", int64(0),
			"reasoning", int64(0),
			"seconds", 0,
			"weighted", nil,
			"visibleWeighted", nil,
			"median", nil,
			"visibleMedian", nil,
			"p10", nil,
			"p25", nil,
			"p75", nil,
			"p90", nil,
			"p99", nil,
			"max", nil,
			"ttftMedian", nil,
			"ttftN", 0,
			"decodeMedian", nil,
			"decodeN", 0,
		)
	}
	rates := make([]float64, len(rows))
	visibleRates := make([]float64, len(rows))
	secondsValues := make([]float64, len(rows))
	var tokens, visible, reasoning int64
	ttfts := make([]float64, 0, len(rows))
	decodes := make([]float64, 0, len(rows))
	for i, row := range rows {
		rates[i] = float64(row.Gen) / *row.DT
		visibleRates[i] = float64(row.Out) / *row.DT
		secondsValues[i] = *row.DT
		tokens += row.Gen
		visible += row.Out
		reasoning += row.Reas
		if row.TTFT != nil {
			ttfts = append(ttfts, *row.TTFT)
		}
		if row.DecodeRate != nil {
			decodes = append(decodes, *row.DecodeRate)
		}
	}
	sort.Float64s(rates)
	sort.Float64s(visibleRates)
	seconds := sumFloats(secondsValues)
	var weighted, visibleWeighted any
	if seconds != 0 {
		weighted = stats.PyRound(float64(tokens)/seconds, 1)
		visibleWeighted = stats.PyRound(float64(visible)/seconds, 1)
	}
	return obj(
		"n", len(rows),
		"tokens", tokens,
		"visible", visible,
		"reasoning", reasoning,
		"seconds", frac(seconds, 3),
		"ttftMedian", medianR1(ttfts),
		"ttftN", len(ttfts),
		"decodeMedian", medianR1(decodes),
		"decodeN", len(decodes),
		"weighted", weighted,
		"visibleWeighted", visibleWeighted,
		"median", medianR1(rates),
		"visibleMedian", medianR1(visibleRates),
		"p10", percentileAny(rates, 10),
		"p25", percentileAny(rates, 25),
		"p75", percentileAny(rates, 75),
		"p90", percentileAny(rates, 90),
		"p99", percentileAny(rates, 99),
		"max", stats.R1(rates[len(rates)-1]),
	)
}

// reasoningSummary mirrors telemetry.py:reasoning_summary (timed rows only).
func (b *builder) reasoningSummary(rows []*ocsource.Row) map[string]any {
	timed := make([]*ocsource.Row, 0, len(rows))
	for _, row := range rows {
		if b.th.IsTimed(row) {
			timed = append(timed, row)
		}
	}
	withReasoning := make([]*ocsource.Row, 0, len(timed))
	var timedGen, timedReas int64
	for _, row := range timed {
		timedGen += row.Gen
		timedReas += row.Reas
		if row.Reas > 0 {
			withReasoning = append(withReasoning, row)
		}
	}
	var shareSteps, tokenShare any
	if len(timed) != 0 {
		shareSteps = frac(float64(len(withReasoning))/float64(len(timed)), 4)
	}
	if timedGen != 0 {
		tokenShare = frac(float64(timedReas)/float64(timedGen), 4)
	}
	reasonValues := make([]float64, len(withReasoning))
	outputValues := make([]float64, len(withReasoning))
	for i, row := range withReasoning {
		reasonValues[i] = float64(row.Reas)
		outputValues[i] = float64(row.Out)
	}
	return obj(
		"steps", len(withReasoning),
		"shareSteps", shareSteps,
		"tokenShare", tokenShare,
		"medianReasonTokens", medianR1(reasonValues),
		"medianOutputTokens", medianR1(outputValues),
	)
}

// runtimeSummary mirrors telemetry.py:runtime_summary.
func runtimeSummary(rows []*ocsource.Row) map[string]any {
	withTools := make([]*ocsource.Row, 0, len(rows))
	for _, row := range rows {
		if row.ToolDT == nil || row.TotalDT == nil {
			continue
		}
		if 0 <= *row.ToolDT && *row.ToolDT <= *row.TotalDT && *row.TotalDT <= 3600 {
			withTools = append(withTools, row)
		}
	}
	if len(withTools) == 0 {
		return obj(
			"steps", 0,
			"medianToolSeconds", nil,
			"p90ToolSeconds", nil,
			"medianStepSeconds", nil,
			"medianShare", nil,
			"weightedShare", nil,
			"modelTimeShare", nil,
			"medianOverheadSeconds", nil,
			"p90OverheadSeconds", nil,
			"overheadShare", nil,
			"medianGapSeconds", nil,
			"p90GapSeconds", nil,
			"byTool", []any{},
		)
	}

	toolSeconds := make([]float64, len(withTools))
	stepSeconds := make([]float64, len(withTools))
	shares := make([]float64, 0, len(withTools))
	stepTotalValues := make([]float64, len(withTools))
	complete := make([]*ocsource.Row, 0, len(withTools))
	overheads := make([]float64, 0, len(withTools))
	gaps := make([]float64, 0, len(withTools))
	for i, row := range withTools {
		toolSeconds[i] = *row.ToolDT
		stepSeconds[i] = *row.TotalDT
		stepTotalValues[i] = *row.TotalDT
		if *row.TotalDT > 0 {
			shares = append(shares, *row.ToolDT / *row.TotalDT)
		}
		if row.DT != nil {
			complete = append(complete, row)
			if row.OverheadDT != nil {
				overheads = append(overheads, *row.OverheadDT)
			}
		}
		if row.ToolGapDT != nil {
			gaps = append(gaps, *row.ToolGapDT)
		}
	}
	sort.Float64s(toolSeconds)
	sort.Float64s(overheads)
	sort.Float64s(gaps)

	toolStepTime := sumFloats(stepTotalValues)

	type callRow struct {
		Name        string
		Runtime     float64
		Allocated   float64
		StepSeconds float64
		StepID      string
	}
	callRows := make([]callRow, 0, len(withTools))
	for _, row := range withTools {
		for _, call := range row.ToolDetails {
			if call.Runtime == nil {
				continue
			}
			callRows = append(callRows, callRow{
				Name:        call.Name,
				Runtime:     *call.Runtime,
				Allocated:   call.Allocated,
				StepSeconds: *row.TotalDT,
				StepID:      row.ID,
			})
		}
	}
	names := make([]string, 0, len(callRows))
	seen := map[string]bool{}
	for _, item := range callRows {
		if !seen[item.Name] {
			seen[item.Name] = true
			names = append(names, item.Name)
		}
	}
	sort.Strings(names)
	byTool := make([]map[string]any, 0, len(names))
	for _, name := range names {
		rs := make([]callRow, 0, 4)
		for _, item := range callRows {
			if item.Name == name {
				rs = append(rs, item)
			}
		}
		runtimes := make([]float64, 0, len(rs))
		allocatedValues := make([]float64, 0, len(rs))
		stepTotals := map[string]float64{}
		var stepOrder []string
		for _, item := range rs {
			if item.Runtime > 0 {
				runtimes = append(runtimes, item.Runtime)
			}
			allocatedValues = append(allocatedValues, item.Allocated)
			if _, ok := stepTotals[item.StepID]; !ok {
				stepOrder = append(stepOrder, item.StepID)
			}
			stepTotals[item.StepID] = item.StepSeconds
		}
		sort.Float64s(runtimes)
		allocated := sumFloats(allocatedValues)
		stepValues := make([]float64, len(stepOrder))
		for i, id := range stepOrder {
			stepValues[i] = stepTotals[id]
		}
		var weightedShare any
		if toolStepTime != 0 {
			weightedShare = allocated / toolStepTime
		}
		byTool = append(byTool, obj(
			"tool", name,
			"calls", len(rs),
			"medianToolSeconds", medianR2(runtimes),
			"p90ToolSeconds", percentileR2(runtimes, 90),
			"medianStepSeconds", medianR2(stepValues),
			"allocatedSeconds", frac(allocated, 3),
			"weightedShare", weightedShare,
		))
	}
	sortStableOrders(byTool, func(item map[string]any) float64 { return float64(item["calls"].(int)) }, true)

	completeToolValues := make([]float64, len(complete))
	completeStepValues := make([]float64, len(complete))
	completeModelValues := make([]float64, len(complete))
	for i, row := range complete {
		completeToolValues[i] = *row.ToolDT
		completeStepValues[i] = *row.TotalDT
		completeModelValues[i] = *row.DT
	}
	shareValues := make([]float64, 0, len(byTool))
	for _, item := range byTool {
		if value, ok := item["weightedShare"].(float64); ok {
			shareValues = append(shareValues, value)
		}
	}
	overheadValues := make([]float64, len(overheads))
	copy(overheadValues, overheads)

	var weightedShare, modelTimeShare, overheadShare, medianShare any
	if len(complete) != 0 {
		weightedShare = frac(sumFloats(completeToolValues)/sumFloats(completeStepValues), 4)
		modelTimeShare = frac(sumFloats(completeModelValues)/sumFloats(completeStepValues), 4)
	}
	if len(complete) != 0 && len(overheads) != 0 {
		overheadShare = frac(sumFloats(overheadValues)/sumFloats(completeStepValues), 4)
	}
	if len(shares) != 0 {
		if value, ok := stats.Median(shares); ok {
			medianShare = frac(value, 4)
		}
	}
	return obj(
		"steps", len(withTools),
		"medianToolSeconds", medianR2(toolSeconds),
		"p90ToolSeconds", percentileR2(toolSeconds, 90),
		"medianStepSeconds", medianR2(stepSeconds),
		"medianShare", medianShare,
		"weightedShare", weightedShare,
		"modelTimeShare", modelTimeShare,
		"medianOverheadSeconds", medianR2(overheads),
		"p90OverheadSeconds", percentileR2(overheads, 90),
		"overheadShare", overheadShare,
		"medianGapSeconds", medianR2(gaps),
		"p90GapSeconds", percentileR2(gaps, 90),
		"toolStepSeconds", frac(toolStepTime, 3),
		"byToolShareSum", frac(sumFloats(shareValues), 6),
		"byTool", mapSlice(byTool),
	)
}

// behaviorSlice mirrors telemetry.py:behavior_slice.
func (b *builder) behaviorSlice(rows []*ocsource.Row, total int) map[string]any {
	timed := make([]*ocsource.Row, 0, len(rows))
	for _, row := range rows {
		if b.th.IsTimed(row) {
			timed = append(timed, row)
		}
	}
	var share, medianOut, weighted, medianTps any
	if total != 0 {
		share = frac(float64(len(rows))/float64(total), 4)
	}
	genValues := make([]float64, len(rows))
	for i, row := range rows {
		genValues[i] = float64(row.Gen)
	}
	if len(rows) != 0 {
		medianOut = medianR1(genValues)
	}
	if len(timed) != 0 {
		timedGen := make([]float64, len(timed))
		timedSeconds := make([]float64, len(timed))
		rates := make([]float64, len(timed))
		for i, row := range timed {
			timedGen[i] = float64(row.Gen)
			timedSeconds[i] = *row.DT
			rates[i] = float64(row.Gen) / *row.DT
		}
		weighted = frac(sumFloats(timedGen)/sumFloats(timedSeconds), 1)
		medianTps = medianR1(rates)
	}
	return obj(
		"steps", len(rows),
		"share", share,
		"generatedTokens", sumField(rows, func(row *ocsource.Row) int64 { return row.Gen }),
		"medianOut", medianOut,
		"timedN", len(timed),
		"weighted", weighted,
		"medianTps", medianTps,
	)
}

// sliceSection builds the agents/variants lists: first-appearance grouping,
// then a stable descending sort by step count.
func (b *builder) sliceSection(rows []*ocsource.Row, total int, keyName string, key func(*ocsource.Row) string) []any {
	var order []string
	groups := map[string][]*ocsource.Row{}
	for _, row := range rows {
		name := key(row)
		if _, ok := groups[name]; !ok {
			order = append(order, name)
		}
		groups[name] = append(groups[name], row)
	}
	out := make([]map[string]any, 0, len(order))
	for _, name := range order {
		item := b.behaviorSlice(groups[name], total)
		item[keyName] = name
		out = append(out, item)
	}
	sortStableOrders(out, func(item map[string]any) float64 { return float64(item["steps"].(int)) }, true)
	return mapSlice(out)
}

// entrypointSection is the entrypoints variant of sliceSection (plus sessions).
func (b *builder) entrypointSection(rows []*ocsource.Row, total int) []any {
	var order []string
	groups := map[string][]*ocsource.Row{}
	for _, row := range rows {
		if _, ok := groups[row.Entry]; !ok {
			order = append(order, row.Entry)
		}
		groups[row.Entry] = append(groups[row.Entry], row)
	}
	out := make([]map[string]any, 0, len(order))
	for _, name := range order {
		item := b.behaviorSlice(groups[name], total)
		sessions := map[string]bool{}
		for _, row := range groups[name] {
			sessions[row.SessionID] = true
		}
		item["entrypoint"] = name
		item["sessions"] = len(sessions)
		out = append(out, item)
	}
	sortStableOrders(out, func(item map[string]any) float64 { return float64(item["steps"].(int)) }, true)
	return mapSlice(out)
}

// projectSection mirrors the Python projects block.
func (b *builder) projectSection(rows []*ocsource.Row) []any {
	var order []string
	groups := map[string][]*ocsource.Row{}
	for _, row := range rows {
		if _, ok := groups[row.Project]; !ok {
			order = append(order, row.Project)
		}
		groups[row.Project] = append(groups[row.Project], row)
	}
	out := make([]map[string]any, 0, len(order))
	for _, path := range order {
		rs := groups[path]
		var cacheRead, totalIn, generated, output int64
		costs := make([]float64, len(rs))
		sessions := map[string]bool{}
		for i, row := range rs {
			cacheRead += row.CacheRead
			totalIn += row.InpTotal()
			generated += row.Gen
			output += row.Out
			costs[i] = row.Cost
			sessions[row.SessionID] = true
		}
		projectLedger := b.projectLedger[path]
		errorSteps := countLedger(projectLedger, "error")
		errorCost := ledgerCost(projectLedger, "error")
		skippedCost := fsumLedger(projectLedger)
		projectCost := fsumFloats(costs)
		var cacheHitRate, errorRate any
		if totalIn != 0 {
			cacheHitRate = frac(float64(cacheRead)/float64(totalIn), 4)
		}
		if len(rs)+errorSteps != 0 {
			errorRate = frac(float64(errorSteps)/float64(len(rs)+errorSteps), 4)
		}
		out = append(out, obj(
			"name", b.projectLabels[path],
			"path", path,
			"steps", len(rs),
			"sessions", len(sessions),
			"generatedTokens", generated,
			"outputTokens", output,
			"inputTotal", totalIn,
			"cacheReadTokens", cacheRead,
			"cacheHitRate", cacheHitRate,
			"costUsd", frac(projectCost, 6),
			"errorSteps", errorSteps,
			"errorCostUsd", frac(errorCost, 6),
			"skippedCostUsd", frac(skippedCost, 6),
			"totalSpendUsd", frac(projectCost+skippedCost, 6),
			"errorRate", errorRate,
		))
	}
	sortStableOrders(out, func(item map[string]any) float64 { return float64(item["generatedTokens"].(int64)) }, true)
	return mapSlice(out)
}

// toolsSection mirrors the per-call tool attribution block.
func toolsSection(rows []*ocsource.Row, th Thresholds, toolStepTime float64) ([]any, int) {
	type toolStat struct {
		calls          int
		steps          map[string]bool
		generated      float64
		visible        float64
		reasoning      float64
		cost           float64
		tokensPerCall  []float64
		runtimes       []float64
		errors         int
		timedCalls     int
		timedGenerated float64
		timedSeconds   float64
		rates          []float64
		timeAllocated  float64
	}
	toolStats := map[string]*toolStat{}
	var order []string
	totalCalls := 0
	for _, row := range rows {
		type callView struct {
			name      string
			runtime   *float64
			isError   bool
			allocated float64
		}
		calls := make([]callView, 0, len(row.ToolDetails))
		if len(row.ToolDetails) > 0 {
			for _, call := range row.ToolDetails {
				calls = append(calls, callView{name: call.Name, runtime: call.Runtime, isError: call.Error, allocated: call.Allocated})
			}
		} else {
			calls = append(calls, callView{name: "(final answer)"})
		}
		n := len(calls)
		totalCalls += n
		rowTimed := th.IsTimed(row)
		for _, call := range calls {
			stat, ok := toolStats[call.name]
			if !ok {
				stat = &toolStat{steps: map[string]bool{}}
				toolStats[call.name] = stat
				order = append(order, call.name)
			}
			stat.calls++
			stat.steps[row.ID] = true
			stat.generated += float64(row.Gen) / float64(n)
			stat.visible += float64(row.Out) / float64(n)
			stat.reasoning += float64(row.Reas) / float64(n)
			stat.cost += row.Cost / float64(n)
			stat.tokensPerCall = append(stat.tokensPerCall, float64(row.Gen)/float64(n))
			stat.timeAllocated += call.allocated
			if call.runtime != nil {
				stat.runtimes = append(stat.runtimes, *call.runtime)
			}
			if call.isError {
				stat.errors++
			}
			if rowTimed {
				stat.timedCalls++
				stat.timedGenerated += float64(row.Gen) / float64(n)
				stat.timedSeconds += *row.DT / float64(n)
				stat.rates = append(stat.rates, float64(row.Gen) / *row.DT)
			}
		}
	}
	// Python iterates the defaultdict in first-insertion order.
	out := make([]map[string]any, 0, len(order))
	for _, name := range order {
		stat := toolStats[name]
		runtimes := sortedFloats(stat.runtimes)
		var shareCalls, weighted, medianTps, medianRuntime, p90Runtime, timeShare, errRate, costPerCall any
		if totalCalls != 0 {
			shareCalls = float64(stat.calls) / float64(totalCalls)
		}
		if stat.timedSeconds != 0 {
			weighted = frac(stat.timedGenerated/stat.timedSeconds, 1)
		}
		if len(stat.rates) != 0 {
			medianTps = medianR1(stat.rates)
		}
		if len(runtimes) != 0 {
			medianRuntime = medianR2(runtimes)
			p90Runtime = percentileR2(runtimes, 90)
		}
		if toolStepTime != 0 {
			timeShare = stat.timeAllocated / toolStepTime
		}
		if stat.calls != 0 {
			errRate = frac(float64(stat.errors)/float64(stat.calls), 4)
			costPerCall = frac(stat.cost/float64(stat.calls), 4)
		}
		out = append(out, obj(
			"tool", name,
			"calls", stat.calls,
			"stepsWithTool", len(stat.steps),
			"shareCalls", shareCalls,
			"generatedTokens", frac(stat.generated, 1),
			"visibleTokens", frac(stat.visible, 1),
			"visibleOutputTokens", frac(stat.visible, 1),
			"reasoningTokens", frac(stat.reasoning, 1),
			"medianTokensPerCall", medianR1(stat.tokensPerCall),
			"timedCalls", stat.timedCalls,
			"weighted", weighted,
			"medianTps", medianTps,
			"medianRuntime", medianRuntime,
			"p90Runtime", p90Runtime,
			"timeAllocatedSeconds", frac(stat.timeAllocated, 3),
			"timeShare", timeShare,
			"timeShareBasis", "tool-step-time",
			"errors", stat.errors,
			"errRate", errRate,
			"costUsd", frac(stat.cost, 6),
			"costPerCall", costPerCall,
			"attribution", "equal-share",
		))
	}
	sortStableOrders(out, func(item map[string]any) float64 { return float64(item["calls"].(int)) }, true)
	return mapSlice(out), totalCalls
}

// costSummary mirrors telemetry.py:cost_summary.
func costSummary(model, short string, rows []*ocsource.Row, fit *priceFit) *costModel {
	out := &costModel{Model: model, Short: short, Steps: len(rows), Fit: fit}
	for _, row := range rows {
		out.Inp += row.Inp
		out.CacheRead += row.CacheRead
		out.CacheWrite += row.CacheWrite
		out.Visible += row.Out
		out.Generated += row.Gen
		out.Reasoning += row.Reas
	}
	costs := make([]float64, len(rows))
	for i, row := range rows {
		costs[i] = row.Cost
	}
	recorded := fsumFloats(costs)
	out.Recorded = recorded
	out.InputTotal = out.Inp + out.CacheRead + out.CacheWrite

	if fit != nil {
		freshCost := float64(out.Inp) / 1e6 * fit.InputRate
		cacheReadCost := float64(out.CacheRead) / 1e6 * fit.CacheReadRate
		cacheWriteCost := float64(out.CacheWrite) / 1e6 * fit.CacheWriteRate
		outputCost := float64(out.Generated) / 1e6 * fit.OutputRate
		out.TokenCost = freshCost + cacheReadCost + cacheWriteCost + outputCost
		readSavings := float64(out.CacheRead) * (fit.InputRate - fit.CacheReadRate) / 1e6
		writePenalty := float64(out.CacheWrite) * (fit.CacheWriteRate - fit.InputRate) / 1e6
		savings := readSavings - writePenalty
		out.FreshCost = &freshCost
		out.CacheReadCost = &cacheReadCost
		out.CacheWriteCost = &cacheWriteCost
		out.OutputCost = &outputCost
		out.CacheReadSavings = &readSavings
		out.CacheWritePenalty = &writePenalty
		out.CacheSavings = &savings
		out.UnattributedCost = 0.0
		if out.InputTotal != 0 {
			effective := (freshCost + cacheReadCost + cacheWriteCost) / (float64(out.InputTotal) / 1e6)
			out.EffectiveInputPrice = &effective
		}
	} else {
		out.TokenCost = recorded
		out.UnattributedCost = stats.PyRound(recorded, 6)
	}
	if len(rows) != 0 {
		perStep := stats.PyRound(recorded/float64(len(rows)), 4)
		out.CostPerStep = &perStep
	}
	if out.Generated != 0 {
		perMillion := stats.PyRound(recorded/float64(out.Generated)*1e6, 2)
		out.CostPerMillionGenerated = &perMillion
	}
	return out
}

// fitPrices mirrors telemetry.py:fit_prices.
//
// Recorded cost is fitted against fresh input, cache read, cache write and
// generated tokens ($/1M). The recorded cost fits output+reasoning at the
// output rate, so the last feature is the generated total.
func fitPrices(rows []*ocsource.Row) *priceFit {
	rs := make([]*ocsource.Row, 0, len(rows))
	for _, row := range rows {
		if row.Cost > 0 {
			rs = append(rs, row)
		}
	}
	if len(rs) < 30 {
		return nil
	}
	sample := make([][4]float64, len(rs))
	for i, row := range rs {
		sample[i] = [4]float64{
			float64(row.Inp) / 1e6,
			float64(row.CacheRead) / 1e6,
			float64(row.CacheWrite) / 1e6,
			float64(row.Gen) / 1e6,
		}
	}
	active := make([]int, 0, 4)
	for i := 0; i < 4; i++ {
		for _, feats := range sample {
			if feats[i] > 0 {
				active = append(active, i)
				break
			}
		}
	}
	if len(active) == 0 {
		return nil
	}
	n := len(active)
	matrix := make([][]float64, n)
	for i := range matrix {
		matrix[i] = make([]float64, n)
	}
	rhs := make([]float64, n)
	for i, feats := range sample {
		vals := make([]float64, n)
		for j, index := range active {
			vals[j] = feats[index]
		}
		for a := 0; a < n; a++ {
			rhs[a] += vals[a] * rs[i].Cost
			for b := 0; b < n; b++ {
				matrix[a][b] += vals[a] * vals[b]
			}
		}
	}
	coeffs, ok := stats.SolveLinear(matrix, rhs)
	if !ok {
		return nil
	}
	rates := [4]float64{}
	for i, index := range active {
		rates[index] = coeffs[i]
	}
	for _, value := range rates {
		if value < -0.01 {
			return nil
		}
	}
	for i := range rates {
		if rates[i] < 0 {
			rates[i] = 0
		}
	}
	if rates[1] > rates[0]*1.001 {
		return nil
	}
	if rates[2] > mathMax(rates[0]*3, 0.01) {
		return nil
	}
	costValues := make([]float64, len(rs))
	for i, row := range rs {
		costValues[i] = row.Cost
	}
	mean := sumFloats(costValues) / float64(len(rs))
	residuals := make([]float64, len(rs))
	for i, feats := range sample {
		predicted := sumFloats([]float64{
			feats[0] * rates[0],
			feats[1] * rates[1],
			feats[2] * rates[2],
			feats[3] * rates[3],
		})
		delta := rs[i].Cost - predicted
		residuals[i] = delta * delta
	}
	ssRes := sumFloats(residuals)
	deviations := make([]float64, len(rs))
	for i, row := range rs {
		delta := row.Cost - mean
		deviations[i] = delta * delta
	}
	ssTot := sumFloats(deviations)
	var rSquared *float64
	if ssTot != 0 {
		value := 1 - ssRes/ssTot
		rSquared = &value
	}
	if rSquared != nil && *rSquared < 0.85 {
		return nil
	}
	out := &priceFit{
		InputRate:      stats.PyRound(rates[0], 4),
		CacheReadRate:  stats.PyRound(rates[1], 4),
		CacheWriteRate: stats.PyRound(rates[2], 4),
		OutputRate:     stats.PyRound(rates[3], 4),
		N:              len(rs),
	}
	if rSquared != nil {
		rounded := stats.PyRound(*rSquared, 4)
		out.R2 = &rounded
	}
	return out
}

// mathMax is math.max.
func mathMax(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
