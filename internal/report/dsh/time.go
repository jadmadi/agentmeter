// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"sort"
	"time"

	sourcedsh "github.com/jadmadi/agentmeter/internal/source/dsh"
)

// ledgerIndexes groups ledger entries the way the builder does for the
// all-step spend series.
type ledgerIndexes struct {
	byDay      map[string][]*sourcedsh.LedgerEntry
	byHour     map[int][]*sourcedsh.LedgerEntry
	byModelDay map[string][]*sourcedsh.LedgerEntry
}

func buildLedgerIndexes(ledger []*sourcedsh.LedgerEntry) *ledgerIndexes {
	indexes := &ledgerIndexes{
		byDay:      make(map[string][]*sourcedsh.LedgerEntry),
		byHour:     make(map[int][]*sourcedsh.LedgerEntry),
		byModelDay: make(map[string][]*sourcedsh.LedgerEntry),
	}
	for _, entry := range ledger {
		if entry.TS == nil || entry.Reason == sourcedsh.ReasonDuplicate {
			continue
		}
		local := time.Unix(entry.TS.Unix(), int64(entry.TS.Nanosecond())).In(time.Local)
		day := local.Format("2006-01-02")
		indexes.byDay[day] = append(indexes.byDay[day], entry)
		indexes.byHour[local.Hour()] = append(indexes.byHour[local.Hour()], entry)
		key := entry.Model + "\x00" + day
		indexes.byModelDay[key] = append(indexes.byModelDay[key], entry)
	}
	return indexes
}

// paceIndex ports pace_index().
func paceIndex(values []*float64, paceBase *float64) any {
	if paceBase == nil || *paceBase == 0 {
		return nil
	}
	collected := make([]float64, 0, len(values))
	for _, value := range values {
		if value != nil {
			collected = append(collected, *value)
		}
	}
	if len(collected) == 0 {
		return nil
	}
	return pyRound(median(collected)/(*paceBase)*100, 1)
}

// buildDaily ports the daily series, including the 7-day windows.
func buildDaily(
	rows []*sourcedsh.Row,
	ledgerIndex *ledgerIndexes,
	inputTotal func(*sourcedsh.Row) int64,
	paceBase *float64,
) []any {
	groups := newOrderedGroups()
	for _, row := range rows {
		groups.add(sourcedsh.LocalDate(row.TS), row)
	}
	dateSet := make(map[string]struct{}, len(groups.values)+len(ledgerIndex.byDay))
	for date := range groups.values {
		dateSet[date] = struct{}{}
	}
	for date := range ledgerIndex.byDay {
		dateSet[date] = struct{}{}
	}
	dates := make([]string, 0, len(dateSet))
	for date := range dateSet {
		dates = append(dates, date)
	}
	sort.Strings(dates)

	daily := make([]any, 0, len(dates))
	for _, date := range dates {
		rs := groups.values[date]
		rsTimed := make([]*sourcedsh.Row, 0, len(rs))
		textRows := make([]*sourcedsh.Row, 0, len(rs))
		var tokens, timedTokens, cacheRead, totalIn int64
		secondsAcc := sumAcc{}
		costAcc := sumAcc{}
		sessions := make(map[string]struct{}, 4)
		ratios := make([]*float64, 0, len(rs))
		for _, row := range rs {
			tokens += row.Out
			cacheRead += row.CacheRead
			totalIn += inputTotal(row)
			costAcc.add(row.Cost)
			sessions[row.SessionID] = struct{}{}
			if row.Timing != "plausible" {
				continue
			}
			rsTimed = append(rsTimed, row)
			timedTokens += row.Out
			secondsAcc.add(*row.DT)
			ratios = append(ratios, row.Ratio)
			if !row.Tool {
				textRows = append(textRows, row)
			}
		}
		dayLedger := ledgerIndex.byDay[date]
		errorSteps := int64(len(dayLedger))
		errorAcc := sumAcc{}
		for _, entry := range dayLedger {
			errorAcc.add(entry.CostUSD)
		}
		timedSeconds := secondsAcc.value()
		cost := costAcc.value()
		errorCost := errorAcc.value()
		ratioValues := nonNil(ratios)
		ratio := medianOrNil(ratioValues)
		item := map[string]any{
			"date":            date,
			"nAll":            int64(len(rs)),
			"nTimed":          int64(len(rsTimed)),
			"n":               int64(len(rsTimed)),
			"tokens":          timedTokens,
			"generatedTokens": tokens,
			"timedTokens":     timedTokens,
			"timedSeconds":    timedSeconds,
			"inputTotal":      totalIn,
			"cacheReadTokens": cacheRead,
			"cacheHitRate":    rateOrNil(cacheRead, totalIn),
			"costUsd":         pyRound(cost, 6),
			"errorCostUsd":    pyRound(errorCost, 6),
			"totalSpendUsd":   pyRound(cost+errorCost, 6),
			"errorSteps":      errorSteps,
			"errorRate":       nil,
			"sessions":        int64(len(sessions)),
			"weighted":        nil,
			"median":          nil,
			"p90":             nil,
			"ratio":           nil,
			"ratioPct":        nil,
			"ratioN":          int64(len(ratioValues)),
			"index":           paceIndex(ratios, paceBase),
			"textN":           int64(len(textRows)),
			"textMedian":      nil,
			"textIndex":       paceIndex(rowRatios(textRows), paceBase),
		}
		denominator := int64(len(rs)) + errorSteps
		if denominator != 0 {
			item["errorRate"] = pyRound(float64(errorSteps)/float64(denominator), 4)
		}
		if timedSeconds != 0 {
			item["weighted"] = pyRound(float64(timedTokens)/timedSeconds, 1)
		}
		if len(rsTimed) > 0 {
			item["median"] = r1(median(rowTPS(rsTimed)))
			item["p90"] = r1(percentile(sortedTPS(rsTimed), 90))
		}
		if ratio != nil {
			item["ratio"] = pyRound(*ratio, 6)
			item["ratioPct"] = pyRound(*ratio*100, 2)
		}
		if len(textRows) > 0 {
			item["textMedian"] = r1(median(rowTPS(textRows)))
		}
		daily = append(daily, item)
	}

	// 7-day trailing windows.
	for index, entry := range daily {
		item := entry.(map[string]any)
		date, _ := item["date"].(string)
		start := parseISODate(date).AddDate(0, 0, -6)
		window := make([]map[string]any, 0, 8)
		for _, candidate := range daily[:index+1] {
			candidateItem := candidate.(map[string]any)
			candidateDate, _ := candidateItem["date"].(string)
			if !parseISODate(candidateDate).Before(start) {
				window = append(window, candidateItem)
			}
		}
		var tokens int64
		secondsAcc := sumAcc{}
		spendAcc := sumAcc{}
		indexes := make([]float64, 0, len(window))
		for _, member := range window {
			tokens += mapInt(member, "timedTokens")
			secondsAcc.add(mapFloat(member, "timedSeconds"))
			spendAcc.add(mapFloat(member, "totalSpendUsd"))
			if member["index"] != nil {
				indexes = append(indexes, mapFloat(member, "index"))
			}
		}
		seconds := secondsAcc.value()
		spend := spendAcc.value()
		if seconds != 0 {
			item["weighted7d"] = pyRound(float64(tokens)/seconds, 1)
		} else {
			item["weighted7d"] = nil
		}
		if len(window) > 0 {
			item["cost7d"] = pyRound(spend/float64(len(window)), 6)
		} else {
			item["cost7d"] = nil
		}
		item["spend7dTotal"] = pyRound(spend, 6)
		if len(indexes) > 0 {
			item["index7d"] = r1(median(indexes))
		} else {
			item["index7d"] = nil
		}
	}
	return daily
}

// buildAnomalies ports the robust-z anomaly ledger.
func buildAnomalies(daily []any) []any {
	anomalies := make([]any, 0)
	for _, metric := range []string{"totalSpendUsd", "weighted", "index", "cacheHitRate", "errorRate"} {
		values := make([]float64, 0, len(daily))
		for _, entry := range daily {
			item := entry.(map[string]any)
			if value, ok := numericValue(item[metric]); ok {
				values = append(values, value)
			}
		}
		for _, entry := range daily {
			item := entry.(map[string]any)
			value, hasValue := numericValue(item[metric])
			z, ok := robustZ(values, value, hasValue)
			if !ok {
				continue
			}
			if z >= 3.5 || z <= -3.5 {
				direction := "low"
				if z > 0 {
					direction = "high"
				}
				var valueOut any
				if hasValue {
					valueOut = item[metric]
				}
				anomalies = append(anomalies, map[string]any{
					"date":      item["date"],
					"metric":    metric,
					"value":     valueOut,
					"z":         pyRound(z, 2),
					"direction": direction,
				})
			}
		}
	}
	return anomalies
}

// buildDailyByModel ports the per-model daily series.
func buildDailyByModel(
	rows []*sourcedsh.Row,
	models []string,
	ledgerIndex *ledgerIndexes,
	inputTotal func(*sourcedsh.Row) int64,
	paceBase *float64,
) []any {
	result := make([]any, 0, len(models))
	for _, model := range models {
		groups := newOrderedGroups()
		for _, row := range rows {
			if row.Model != model {
				continue
			}
			groups.add(sourcedsh.LocalDate(row.TS), row)
		}
		dateSet := make(map[string]struct{}, len(groups.values))
		for date := range groups.values {
			dateSet[date] = struct{}{}
		}
		for date := range ledgerIndex.byDay {
			if len(ledgerIndex.byModelDay[model+"\x00"+date]) > 0 {
				dateSet[date] = struct{}{}
			}
		}
		dates := make([]string, 0, len(dateSet))
		for date := range dateSet {
			dates = append(dates, date)
		}
		sort.Strings(dates)
		series := make([]any, 0, len(dates))
		for _, date := range dates {
			dayRows := groups.values[date]
			dayTimed := make([]*sourcedsh.Row, 0, len(dayRows))
			var generated, cacheRead, totalIn int64
			costAcc := sumAcc{}
			for _, row := range dayRows {
				generated += row.Out
				cacheRead += row.CacheRead
				totalIn += inputTotal(row)
				costAcc.add(row.Cost)
				if row.Timing == "plausible" {
					dayTimed = append(dayTimed, row)
				}
			}
			cost := costAcc.value()
			modelLedger := ledgerIndex.byModelDay[model+"\x00"+date]
			errorAcc := sumAcc{}
			for _, entry := range modelLedger {
				if entry.Reason != sourcedsh.ReasonDuplicate {
					errorAcc.add(entry.CostUSD)
				}
			}
			errorCost := errorAcc.value()
			ratioValues := nonNil(rowRatios(dayTimed))
			ratio := medianOrNil(ratioValues)
			item := map[string]any{
				"date":            date,
				"nAll":            int64(len(dayRows)),
				"nTimed":          int64(len(dayTimed)),
				"generatedTokens": generated,
				"costUsd":         pyRound(cost, 6),
				"errorCostUsd":    pyRound(errorCost, 6),
				"cacheHitRate":    rateOrNil(cacheRead, totalIn),
				"weighted":        nil,
				"median":          nil,
				"ratio":           nil,
				"ratioPct":        nil,
				"ratioN":          int64(len(ratioValues)),
				"index":           paceIndex(rowRatios(dayTimed), paceBase),
			}
			if len(dayTimed) > 0 {
				timedTokensAcc := sumAcc{}
				timedSecondsAcc := sumAcc{}
				for _, row := range dayTimed {
					timedTokensAcc.add(float64(row.Out))
					timedSecondsAcc.add(*row.DT)
				}
				item["weighted"] = pyRound(timedTokensAcc.value()/timedSecondsAcc.value(), 1)
				item["median"] = r1(median(rowTPS(dayTimed)))
			}
			if ratio != nil {
				item["ratio"] = pyRound(*ratio, 6)
				item["ratioPct"] = pyRound(*ratio*100, 2)
			}
			series = append(series, item)
		}
		result = append(result, map[string]any{"model": model, "series": series})
	}
	return result
}

// buildHourly ports the 24-slot hourly series.
func buildHourly(
	rows []*sourcedsh.Row,
	ledgerIndex *ledgerIndexes,
	inputTotal func(*sourcedsh.Row) int64,
	paceBase *float64,
) []any {
	groups := make([][]*sourcedsh.Row, 24)
	for _, row := range rows {
		hour := sourcedsh.LocalHour(row.TS)
		groups[hour] = append(groups[hour], row)
	}
	hourly := make([]any, 0, 24)
	for hour := 0; hour < 24; hour++ {
		rs := groups[hour]
		rsTimed := make([]*sourcedsh.Row, 0, len(rs))
		textRows := make([]*sourcedsh.Row, 0, len(rs))
		var tokens, timedTokens, cacheRead, totalIn int64
		secondsAcc := sumAcc{}
		costAcc := sumAcc{}
		sessions := make(map[string]struct{}, 4)
		for _, row := range rs {
			tokens += row.Out
			cacheRead += row.CacheRead
			totalIn += inputTotal(row)
			costAcc.add(row.Cost)
			sessions[row.SessionID] = struct{}{}
			if row.Timing != "plausible" {
				continue
			}
			rsTimed = append(rsTimed, row)
			timedTokens += row.Out
			secondsAcc.add(*row.DT)
			if !row.Tool {
				textRows = append(textRows, row)
			}
		}
		hourLedger := ledgerIndex.byHour[hour]
		errorSteps := int64(len(hourLedger))
		errorAcc := sumAcc{}
		for _, entry := range hourLedger {
			errorAcc.add(entry.CostUSD)
		}
		timedSeconds := secondsAcc.value()
		cost := costAcc.value()
		errorCost := errorAcc.value()
		ratioValues := nonNil(rowRatios(rsTimed))
		ratio := medianOrNil(ratioValues)
		textRatio := medianOrNil(nonNil(rowRatios(textRows)))
		item := map[string]any{
			"hour":            int64(hour),
			"nAll":            int64(len(rs)),
			"nTimed":          int64(len(rsTimed)),
			"n":               int64(len(rsTimed)),
			"tokens":          timedTokens,
			"generatedTokens": tokens,
			"inputTotal":      totalIn,
			"cacheReadTokens": cacheRead,
			"cacheHitRate":    rateOrNil(cacheRead, totalIn),
			"costUsd":         pyRound(cost, 6),
			"errorCostUsd":    pyRound(errorCost, 6),
			"totalSpendUsd":   pyRound(cost+errorCost, 6),
			"errorSteps":      errorSteps,
			"sessions":        int64(len(sessions)),
			"weighted":        nil,
			"median":          nil,
			"p90":             nil,
			"ratio":           nil,
			"ratioPct":        nil,
			"ratioN":          int64(len(ratioValues)),
			"index":           paceIndex(rowRatios(rsTimed), paceBase),
			"textN":           int64(len(textRows)),
			"textMedian":      nil,
			"textRatio":       nil,
			"textIndex":       paceIndex(rowRatios(textRows), paceBase),
		}
		if timedSeconds != 0 {
			item["weighted"] = pyRound(float64(timedTokens)/timedSeconds, 1)
		}
		if len(rsTimed) > 0 {
			item["median"] = r1(median(rowTPS(rsTimed)))
			item["p90"] = r1(percentile(sortedTPS(rsTimed), 90))
		}
		if ratio != nil {
			item["ratio"] = pyRound(*ratio, 6)
			item["ratioPct"] = pyRound(*ratio*100, 2)
		}
		if len(textRows) > 0 {
			item["textMedian"] = r1(median(rowTPS(textRows)))
		}
		if textRatio != nil {
			item["textRatio"] = pyRound(*textRatio, 6)
		}
		hourly = append(hourly, item)
	}
	return hourly
}

// buildContextBuckets ports context_buckets().
func buildContextBuckets(rows []*sourcedsh.Row, inputTotal func(*sourcedsh.Row) int64, paceBase *float64) []any {
	definitions := []sizeBucket{
		{"<50k", 0, 50_000},
		{"50-150k", 50_000, 150_000},
		{"150-400k", 150_000, 400_000},
		{"400k+", 400_000, 1_000_000_000_000},
	}
	result := make([]any, 0, len(definitions))
	for _, definition := range definitions {
		selected := make([]*sourcedsh.Row, 0, len(rows))
		for _, row := range rows {
			total := inputTotal(row)
			if definition.low <= total && total < definition.high {
				selected = append(selected, row)
			}
		}
		if len(selected) == 0 {
			continue
		}
		ratioValues := nonNil(rowRatios(selected))
		ratio := medianOrNil(ratioValues)
		tokensAcc := sumAcc{}
		secondsAcc := sumAcc{}
		for _, row := range selected {
			tokensAcc.add(float64(row.Out))
			secondsAcc.add(*row.DT)
		}
		tokens := tokensAcc.value()
		seconds := secondsAcc.value()
		item := map[string]any{
			"label":    definition.label,
			"n":        int64(len(selected)),
			"median":   r1(median(rowTPS(selected))),
			"weighted": pyRound(tokens/seconds, 1),
			"ratio":    nil,
			"ratioPct": nil,
			"ratioN":   int64(len(ratioValues)),
			"index":    paceIndex(rowRatios(selected), paceBase),
		}
		if ratio != nil {
			item["ratio"] = pyRound(*ratio, 6)
			item["ratioPct"] = pyRound(*ratio*100, 2)
		}
		result = append(result, item)
	}
	return result
}

func rowTPS(rows []*sourcedsh.Row) []float64 {
	values := make([]float64, 0, len(rows))
	for _, row := range rows {
		values = append(values, *row.TPS)
	}
	return values
}

func sortedTPS(rows []*sourcedsh.Row) []float64 {
	values := rowTPS(rows)
	sort.Float64s(values)
	return values
}

func rowRatios(rows []*sourcedsh.Row) []*float64 {
	values := make([]*float64, 0, len(rows))
	for _, row := range rows {
		values = append(values, row.Ratio)
	}
	return values
}

func nonNil(values []*float64) []*float64 {
	result := make([]*float64, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, value)
		}
	}
	return result
}

func medianOrNil(values []*float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	flattened := make([]float64, 0, len(values))
	for _, value := range values {
		flattened = append(flattened, *value)
	}
	result := median(flattened)
	return &result
}

func numericValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int64:
		return float64(typed), true
	default:
		return 0, false
	}
}

func parseISODate(text string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02", text, time.Local)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
