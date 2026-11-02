// SPDX-License-Identifier: AGPL-3.0-only

package muse

import (
	"sort"

	sourcemuse "github.com/jadmadi/agentmeter/internal/source/muse"
)

// summarize ports telemetry.py:summarize().
func summarize(rows []*sourcemuse.Row) map[string]any {
	if len(rows) == 0 {
		return map[string]any{
			"n":        int64(0),
			"tokens":   int64(0),
			"seconds":  int64(0),
			"weighted": nil,
			"median":   nil,
			"p10":      nil,
			"p25":      nil,
			"p75":      nil,
			"p90":      nil,
			"p99":      nil,
			"max":      nil,
		}
	}
	tps := make([]float64, 0, len(rows))
	var tokens int64
	secondsAcc := sumAcc{}
	for _, row := range rows {
		tps = append(tps, *row.TPS)
		tokens += row.Out
		secondsAcc.add(*row.DT)
	}
	sort.Float64s(tps)
	secondsFloat := secondsAcc.value()
	result := map[string]any{
		"n":       int64(len(rows)),
		"tokens":  tokens,
		"seconds": pyRound(secondsFloat, 1),
	}
	if secondsFloat != 0 {
		result["weighted"] = pyRound(float64(tokens)/secondsFloat, 1)
	} else {
		result["weighted"] = nil
	}
	result["median"] = r1(median(tps))
	result["p10"] = r1(percentile(tps, 10))
	result["p25"] = r1(percentile(tps, 25))
	result["p75"] = r1(percentile(tps, 75))
	result["p90"] = r1(percentile(tps, 90))
	result["p99"] = r1(percentile(tps, 99))
	result["max"] = r1(tps[len(tps)-1])
	return result
}

// fitRate ports fit_rate(): linear_fit with min_n=30 plus the usability flag.
func fitRate(rows []*sourcemuse.Row) map[string]any {
	xs := make([]float64, 0, len(rows))
	ys := make([]float64, 0, len(rows))
	for _, row := range rows {
		xs = append(xs, float64(row.Out))
		ys = append(ys, *row.DT)
	}
	result := linearFit(xs, ys, 30)
	if result == nil {
		return nil
	}
	result.usable = result.n >= 30 && result.r2 != nil && *result.r2 >= 0.50
	return fitMap(result)
}

func fitMap(result *linearFitResult) map[string]any {
	var r2Value any
	if result.r2 != nil {
		r2Value = *result.r2
	}
	return map[string]any{
		"fixedSeconds":         result.fixedSeconds,
		"pureRate":             result.pureRate,
		"r2":                   r2Value,
		"n":                    result.n,
		"interceptConstrained": result.interceptConstrained,
		"usable":               result.usable,
	}
}

// priceCandidate is one attempt() result from fit_prices().
type priceCandidate struct {
	rates    [4]float64
	r2       *float64
	subtract bool
}

// priceFit ports fit_prices(): solve recorded cost against four token
// categories, trying both input decompositions.
func priceFit(rows []*sourcemuse.Row) map[string]any {
	nonWeb := make([]*sourcemuse.Row, 0, len(rows))
	for _, row := range rows {
		if !row.Web {
			nonWeb = append(nonWeb, row)
		}
	}
	if len(nonWeb) < 30 {
		return nil
	}

	attempt := func(subtract bool) *priceCandidate {
		sample := make([][4]float64, 0, len(nonWeb))
		for _, row := range nonWeb {
			nonCached := float64(row.Inp)
			if subtract {
				nonCached = float64(maxInt64(0, row.Inp-row.CacheRead-row.CacheWrite))
			}
			sample = append(sample, [4]float64{
				nonCached / 1e6,
				float64(row.CacheRead) / 1e6,
				float64(row.CacheWrite) / 1e6,
				float64(row.Out) / 1e6,
			})
		}
		active := make([]int, 0, 4)
		for index := 0; index < 4; index++ {
			for _, features := range sample {
				if features[index] > 0 {
					active = append(active, index)
					break
				}
			}
		}
		if len(active) == 0 {
			return nil
		}
		size := len(active)
		matrix := make([][]float64, size)
		for row := range matrix {
			matrix[row] = make([]float64, size)
		}
		rhs := make([]float64, size)
		for sampleIndex, features := range sample {
			cost := nonWeb[sampleIndex].Cost
			for i := 0; i < size; i++ {
				rhs[i] += features[active[i]] * cost
				for j := 0; j < size; j++ {
					matrix[i][j] += features[active[i]] * features[active[j]]
				}
			}
		}
		coefficients := solveLinear(matrix, rhs)
		if coefficients == nil {
			return nil
		}
		var rates [4]float64
		for index, column := range active {
			rates[column] = coefficients[index]
		}
		for _, rate := range rates {
			if rate < -0.01 {
				return nil
			}
		}
		for index := range rates {
			if rates[index] < 0 {
				rates[index] = 0
			}
		}
		costAcc := sumAcc{}
		for _, row := range nonWeb {
			costAcc.add(row.Cost)
		}
		mean := costAcc.value() / float64(len(nonWeb))
		var ssRes, ssTot float64
		for index, features := range sample {
			predictedAcc := sumAcc{}
			for column := 0; column < 4; column++ {
				predictedAcc.add(features[column] * rates[column])
			}
			predicted := predictedAcc.value()
			residual := nonWeb[index].Cost - predicted
			ssRes += residual * residual
			delta := nonWeb[index].Cost - mean
			ssTot += delta * delta
		}
		var rSquared *float64
		if ssTot != 0 {
			value := 1 - ssRes/ssTot
			rSquared = &value
		}
		return &priceCandidate{rates: rates, r2: rSquared, subtract: subtract}
	}

	plausible := func(candidate *priceCandidate) bool {
		inputRate, cacheRate, writeRate := candidate.rates[0], candidate.rates[1], candidate.rates[2]
		if inputRate <= 0 {
			for _, rate := range candidate.rates {
				if !(rate > -1e-9 && rate < 1e-9) {
					return false
				}
			}
			return true
		}
		if cacheRate > inputRate*1.001 {
			return false
		}
		limit := inputRate * 3
		if limit < 0.01 {
			limit = 0.01
		}
		if writeRate > limit {
			return false
		}
		return true
	}

	// Without recorded cost there is no price signal to fit.
	recorded := 0.0
	for _, row := range rows {
		recorded += row.Cost
	}
	if recorded == 0 {
		return nil
	}
	candidates := make([]*priceCandidate, 0, 2)
	// Muse input includes cache by construction, so only the subtracting
	// decomposition is fitted; inputIncludesCache is always true.
	for _, subtract := range []bool{true} {
		candidate := attempt(subtract)
		if candidate != nil && plausible(candidate) {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	type scored struct {
		candidate *priceCandidate
		r2        float64
		cacheRate float64
		order     int
	}
	scores := make([]scored, 0, len(candidates))
	for index, candidate := range candidates {
		r2Value := 0.0
		if candidate.r2 != nil {
			r2Value = pyRound(*candidate.r2, 4)
		}
		cacheRatio := 0.0
		if candidate.rates[0] > 0 {
			cacheRatio = candidate.rates[1] / candidate.rates[0]
		}
		scores = append(scores, scored{candidate: candidate, r2: r2Value, cacheRate: cacheRatio, order: index})
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].r2 != scores[j].r2 {
			return scores[i].r2 > scores[j].r2
		}
		if scores[i].cacheRate != scores[j].cacheRate {
			return scores[i].cacheRate < scores[j].cacheRate
		}
		return scores[i].candidate.subtract && !scores[j].candidate.subtract
	})
	best := scores[0].candidate
	if best.r2 != nil && *best.r2 < 0.85 {
		return nil
	}
	var r2Value any
	if best.r2 != nil {
		r2Value = pyRound(*best.r2, 4)
	}
	return map[string]any{
		"inputRate":          pyRound(best.rates[0], 4),
		"cacheReadRate":      pyRound(best.rates[1], 4),
		"cacheWriteRate":     pyRound(best.rates[2], 4),
		"outputRate":         pyRound(best.rates[3], 4),
		"r2":                 r2Value,
		"n":                  int64(len(nonWeb)),
		"inputIncludesCache": best.subtract,
	}
}

// costSummary ports cost_summary(). The returned map is emitted verbatim as
// one cost.byModel row.
func costSummary(model string, rows []*sourcemuse.Row, priceFit map[string]any, includesCache bool, semanticsSource string) map[string]any {
	var rawInput, cacheRead, cacheWrite, fresh, out, reasoning int64
	recordedAcc := sumAcc{}
	for _, row := range rows {
		rawInput += row.Inp
		cacheRead += row.CacheRead
		cacheWrite += row.CacheWrite
		if includesCache {
			fresh += maxInt64(0, row.Inp-row.CacheRead-row.CacheWrite)
		} else {
			fresh += row.Inp
		}
		out += row.Out
		reasoning += row.Reas
		recordedAcc.add(row.Cost)
	}
	recorded := recordedAcc.value()
	totalInput := fresh + cacheRead + cacheWrite

	var freshCost, cacheReadCost, cacheWriteCost, outputCost float64
	var tokenCost float64
	var cacheSavings *float64
	var effectiveInput *float64
	if priceFit != nil {
		freshRate := mapFloat(priceFit, "inputRate")
		cacheRate := mapFloat(priceFit, "cacheReadRate")
		cacheWriteRate := mapFloat(priceFit, "cacheWriteRate")
		outputRate := mapFloat(priceFit, "outputRate")
		freshCost = float64(fresh) / 1e6 * freshRate
		cacheReadCost = float64(cacheRead) / 1e6 * cacheRate
		cacheWriteCost = float64(cacheWrite) / 1e6 * cacheWriteRate
		outputCost = float64(out) / 1e6 * outputRate
		tokenCost = freshCost + cacheReadCost + cacheWriteCost + outputCost
		savings := float64(cacheRead) * (freshRate - cacheRate) / 1e6
		if savings < 0 {
			savings = 0
		}
		cacheSavings = &savings
		inputCost := freshCost + cacheReadCost + cacheWriteCost
		if totalInput != 0 {
			value := inputCost / (float64(totalInput) / 1e6)
			effectiveInput = &value
		}
	} else {
		tokenCost = recorded
	}

	short := model
	if index := lastIndexByte(model, '/'); index >= 0 {
		short = model[index+1:]
	}

	result := map[string]any{
		"model":                model,
		"short":                short,
		"steps":                int64(len(rows)),
		"outputTokens":         out,
		"generatedTokens":      out,
		"visibleOutputTokens":  out - reasoning,
		"reasoningTokens":      reasoning,
		"inputTokens":          totalInput,
		"inputRawTokens":       rawInput,
		"freshInputTokens":     fresh,
		"inputTotal":           totalInput,
		"cacheReadTokens":      cacheRead,
		"cacheWriteTokens":     cacheWrite,
		"cacheHitRate":         rateOrNil(cacheRead, totalInput),
		"cacheWriteShare":      rateOrNil(cacheWrite, totalInput),
		"inputIncludesCache":   includesCache,
		"inputSemanticsSource": semanticsSource,
		"recordedCost":         pyRound(recorded, 6),
		"tokenCost":            pyRound(tokenCost, 6),
		"freshCost":            optionalRound(freshCost, priceFit != nil, 6),
		"cacheReadCost":        optionalRound(cacheReadCost, priceFit != nil, 6),
		"cacheWriteCost":       optionalRound(cacheWriteCost, priceFit != nil, 6),
		"outputCost":           optionalRound(outputCost, priceFit != nil, 6),
		"cacheSavings":         optionalRoundPtr(cacheSavings, 6),
		"effectiveInputPrice":  optionalRoundPtr(effectiveInput, 4),
		"freshInputRate":       optMapFloat(priceFit, "inputRate"),
		"cacheRate":            optMapFloat(priceFit, "cacheReadRate"),
		"cacheWriteRate":       optMapFloat(priceFit, "cacheWriteRate"),
		"outputRate":           optMapFloat(priceFit, "outputRate"),
		"priceR2":              optMapValue(priceFit, "r2"),
	}
	if len(rows) > 0 {
		result["costPerStep"] = pyRound(recorded/float64(len(rows)), 4)
	} else {
		result["costPerStep"] = nil
	}
	if out != 0 {
		result["costPerMillionGenerated"] = pyRound(recorded/float64(out)*1e6, 2)
		result["costPerMillionOutput"] = pyRound(recorded/float64(out)*1e6, 2)
	} else {
		result["costPerMillionGenerated"] = nil
		result["costPerMillionOutput"] = nil
	}
	return result
}

// expectedRatio ports expected_ratio(): 1.0 means faster than the fit predicts.
func expectedRatio(row *sourcemuse.Row, fits map[string]map[string]map[string]any, globalFits map[string]map[string]any) *float64 {
	kind := "text"
	if row.Tool {
		kind = "tool"
	}
	candidates := []map[string]any{}
	if perModel, ok := fits[row.Model]; ok {
		candidates = append(candidates, perModel[kind], perModel["all"])
	} else {
		candidates = append(candidates, nil, nil)
	}
	candidates = append(candidates, globalFits[kind])
	var chosen map[string]any
	for _, candidate := range candidates {
		if candidate != nil && mapTruthy(candidate, "usable") {
			chosen = candidate
			break
		}
	}
	if chosen == nil || row.DT == nil || *row.DT == 0 {
		return nil
	}
	expected := mapFloat(chosen, "fixedSeconds") + float64(row.Out)/mapFloat(chosen, "pureRate")
	if expected <= 0 {
		return nil
	}
	value := expected / *row.DT
	return &value
}

// bucketize ports bucketize(): fixed output-token buckets over timed rows.
func bucketize(rows []*sourcemuse.Row, buckets []sizeBucket) []any {
	result := make([]any, 0, len(buckets))
	for _, bucket := range buckets {
		selected := make([]*sourcemuse.Row, 0, len(rows))
		for _, row := range rows {
			if bucket.low <= row.Out && row.Out < bucket.high {
				selected = append(selected, row)
			}
		}
		if len(selected) == 0 {
			continue
		}
		tps := make([]float64, 0, len(selected))
		var tokens int64
		secondsAcc := sumAcc{}
		for _, row := range selected {
			tps = append(tps, *row.TPS)
			tokens += row.Out
			secondsAcc.add(*row.DT)
		}
		seconds := secondsAcc.value()
		sort.Float64s(tps)
		result = append(result, map[string]any{
			"label":    bucket.label,
			"n":        int64(len(selected)),
			"tokens":   tokens,
			"weighted": pyRound(float64(tokens)/seconds, 1),
			"median":   r1(median(tps)),
			"p90":      r1(percentile(tps, 90)),
		})
	}
	return result
}

// histogram ports histogram(): bisect_right over the edges, clamped.
func histogram(rows []*sourcemuse.Row, edges []float64) []int64 {
	counts := make([]int64, len(edges)-1)
	for _, row := range rows {
		index := sort.Search(len(edges), func(i int) bool { return edges[i] > *row.TPS }) - 1
		if index < 0 {
			index = 0
		}
		if index > len(counts)-1 {
			index = len(counts) - 1
		}
		counts[index]++
	}
	return counts
}

// behaviorSlice ports behavior_slice().
func behaviorSlice(rows []*sourcemuse.Row, totalRows int) map[string]any {
	timedRows := make([]*sourcemuse.Row, 0, len(rows))
	for _, row := range rows {
		if row.Timing == "plausible" {
			timedRows = append(timedRows, row)
		}
	}
	result := map[string]any{
		"steps":        int64(len(rows)),
		"share":        nil,
		"outputTokens": int64(0),
		"medianOut":    nil,
		"timedN":       int64(len(timedRows)),
		"weighted":     nil,
		"medianTps":    nil,
	}
	if totalRows > 0 {
		result["share"] = pyRound(float64(len(rows))/float64(totalRows), 4)
	}
	var outputTokens int64
	outs := make([]float64, 0, len(rows))
	for _, row := range rows {
		outputTokens += row.Out
		outs = append(outs, float64(row.Out))
	}
	result["outputTokens"] = outputTokens
	if len(outs) > 0 {
		result["medianOut"] = r1(median(outs))
	}
	if len(timedRows) > 0 {
		timedTokensAcc := sumAcc{}
		timedSecondsAcc := sumAcc{}
		rates := make([]float64, 0, len(timedRows))
		for _, row := range timedRows {
			timedTokensAcc.add(float64(row.Out))
			timedSecondsAcc.add(*row.DT)
			rates = append(rates, *row.TPS)
		}
		result["weighted"] = pyRound(timedTokensAcc.value()/timedSecondsAcc.value(), 1)
		result["medianTps"] = r1(median(rates))
	}
	return result
}

type sizeBucket struct {
	label string
	low   int64
	high  int64
}

func rateOrNil(numerator, denominator int64) any {
	if denominator == 0 {
		return nil
	}
	return pyRound(float64(numerator)/float64(denominator), 4)
}

func optionalRound(value float64, present bool, ndigits int) any {
	if !present {
		return nil
	}
	return pyRound(value, ndigits)
}

func optionalRoundPtr(value *float64, ndigits int) any {
	if value == nil {
		return nil
	}
	return pyRound(*value, ndigits)
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func lastIndexByte(text string, target byte) int {
	for index := len(text) - 1; index >= 0; index-- {
		if text[index] == target {
			return index
		}
	}
	return -1
}

// mapFloat reads a numeric report field, treating a nil/missing value as 0 the
// way Python's `or 0` does.
func mapFloat(object map[string]any, key string) float64 {
	if object == nil {
		return 0
	}
	value, _ := object[key].(float64)
	return value
}

// optMapFloat reads a numeric report field, preserving nil.
func optMapFloat(object map[string]any, key string) any {
	if object == nil {
		return nil
	}
	return object[key]
}

func optMapValue(object map[string]any, key string) any {
	if object == nil {
		return nil
	}
	return object[key]
}

func mapTruthy(object map[string]any, key string) bool {
	value, ok := object[key]
	if !ok {
		return false
	}
	boolean, _ := value.(bool)
	return boolean
}

func mapInt(object map[string]any, key string) int64 {
	if object == nil {
		return 0
	}
	switch typed := object[key].(type) {
	case int64:
		return typed
	case float64:
		return int64(typed)
	default:
		return 0
	}
}
