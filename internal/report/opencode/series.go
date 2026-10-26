// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"sort"
	"time"

	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
	"github.com/jadmadi/agentmeter/internal/stats"
)

// localDate is ts.astimezone().date().isoformat().
func localDate(row *ocsource.Row) string { return row.Ts.Format("2006-01-02") }

// dailySection builds the daily series, its 7-day moving averages and the
// robust anomaly ledger.
func (b *builder) dailySection(rows []*ocsource.Row) ([]any, []any) {
	ledgerByDay := map[string][]*ocsource.LedgerEntry{}
	for _, entry := range b.ledger {
		if entry.CreatedMs == 0 {
			continue
		}
		ledgerByDay[entry.Ts.Format("2006-01-02")] = append(ledgerByDay[entry.Ts.Format("2006-01-02")], entry)
	}

	byDay := map[string][]*ocsource.Row{}
	for _, row := range rows {
		day := localDate(row)
		byDay[day] = append(byDay[day], row)
	}
	dates := make([]string, 0, len(byDay))
	for day := range byDay {
		dates = append(dates, day)
	}
	sort.Strings(dates)

	daily := make([]map[string]any, 0, len(dates))
	for _, date := range dates {
		rs := byDay[date]
		rsTimed := make([]*ocsource.Row, 0, len(rs))
		for _, row := range rs {
			if b.th.IsTimed(row) {
				rsTimed = append(rsTimed, row)
			}
		}
		textRows := make([]*ocsource.Row, 0, len(rsTimed))
		for _, row := range rsTimed {
			if !row.Tool {
				textRows = append(textRows, row)
			}
		}
		var generatedAll, timedTokens, visible, reasoning, cacheRead, totalIn int64
		timedSecondsValues := make([]float64, 0, len(rsTimed))
		rates := make([]float64, 0, len(rsTimed))
		textRates := make([]float64, 0, len(textRows))
		costs := make([]float64, len(rs))
		sessions := map[string]bool{}
		for i, row := range rs {
			generatedAll += row.Gen
			visible += row.Out
			reasoning += row.Reas
			cacheRead += row.CacheRead
			totalIn += row.InpTotal()
			costs[i] = row.Cost
			sessions[row.SessionID] = true
		}
		for _, row := range rsTimed {
			timedTokens += row.Gen
			timedSecondsValues = append(timedSecondsValues, *row.DT)
			rates = append(rates, float64(row.Gen) / *row.DT)
		}
		for _, row := range textRows {
			textRates = append(textRates, float64(row.Gen) / *row.DT)
		}
		timedSeconds := sumFloats(timedSecondsValues)
		dayLedger := ledgerByDay[date]
		errorRows := filterLedger(dayLedger, "error")
		emptyRows := filterLedger(dayLedger, "empty")
		skippedCost := fsumLedger(dayLedger)
		errorCost := fsumLedger(errorRows)
		ratioValues := ratiosOf(rsTimed)
		var ratio *float64
		if value, ok := stats.Median(ratioValues); ok {
			ratio = &value
		}
		var cacheHitRate, weighted, medianRate, p90, ratioValue, ratioPct, index, textMedian, textIndex, errorRate, textN any
		textN = len(textRows)
		if totalIn != 0 {
			cacheHitRate = frac(float64(cacheRead)/float64(totalIn), 4)
		}
		if timedSeconds != 0 {
			weighted = frac(float64(timedTokens)/timedSeconds, 1)
		}
		if len(rates) != 0 {
			medianRate = medianR1(rates)
			p90 = percentileAny(sortedFloats(rates), 90)
		}
		if ratio != nil {
			ratioValue = frac(*ratio, 6)
			ratioPct = frac(*ratio*100, 2)
		}
		index = b.paceIndexValues(ratioValues)
		if len(textRates) != 0 {
			textMedian = medianR1(textRates)
		}
		textIndex = b.paceIndexValues(ratiosOf(textRows))
		if len(rs)+len(errorRows) != 0 {
			errorRate = frac(float64(len(errorRows))/float64(len(rs)+len(errorRows)), 4)
		}
		daily = append(daily, obj(
			"date", date,
			"nAll", len(rs),
			"nTimed", len(rsTimed),
			"n", len(rsTimed),
			"tokens", timedTokens,
			"generatedTokens", generatedAll,
			"visibleOutputTokens", visible,
			"reasoningTokens", reasoning,
			"timedTokens", timedTokens,
			"timedSeconds", timedSeconds,
			"inputTotal", totalIn,
			"cacheReadTokens", cacheRead,
			"cacheHitRate", cacheHitRate,
			"costUsd", frac(fsumFloats(costs), 6),
			"errorCostUsd", frac(errorCost, 6),
			"skippedCostUsd", frac(skippedCost, 6),
			"totalSpendUsd", frac(fsumFloats(costs)+skippedCost, 6),
			"errorSteps", len(errorRows),
			"emptySteps", len(emptyRows),
			"ledgerSteps", len(dayLedger),
			"errorRate", errorRate,
			"sessions", len(sessions),
			"weighted", weighted,
			"median", medianRate,
			"p90", p90,
			"ratio", ratioValue,
			"ratioPct", ratioPct,
			"ratioN", len(ratioValues),
			"index", index,
			"textN", textN,
			"textMedian", textMedian,
			"textIndex", textIndex,
		))
	}

	for i, entry := range daily {
		window := window7d(daily, i)
		tokenValues := make([]float64, len(window))
		secondValues := make([]float64, len(window))
		spendValues := make([]float64, len(window))
		indexValues := make([]float64, 0, len(window))
		for j, item := range window {
			tokenValues[j] = numeric(item["timedTokens"])
			secondValues[j] = numeric(item["timedSeconds"])
			spendValues[j] = numeric(item["totalSpendUsd"])
			if item["index"] != nil {
				indexValues = append(indexValues, numeric(item["index"]))
			}
		}
		tokens := sumFloats(tokenValues)
		seconds := sumFloats(secondValues)
		var weighted7d, cost7d, index7d any
		if seconds != 0 {
			weighted7d = frac(tokens/seconds, 1)
		}
		if len(window) != 0 {
			cost7d = frac(sumFloats(spendValues)/float64(len(window)), 6)
		}
		if len(indexValues) != 0 {
			if value, ok := stats.Median(indexValues); ok {
				index7d = stats.R1(value)
			}
		}
		entry["weighted7d"] = weighted7d
		entry["cost7d"] = cost7d
		entry["spend7dTotal"] = frac(sumFloats(spendValues), 6)
		entry["index7d"] = index7d
	}

	anomalies := make([]any, 0)
	for _, metric := range []string{"totalSpendUsd", "weighted", "index", "cacheHitRate", "errorRate"} {
		values := make([]float64, 0, len(daily))
		for _, entry := range daily {
			if entry[metric] == nil {
				continue
			}
			values = append(values, numeric(entry[metric]))
		}
		for _, entry := range daily {
			if entry[metric] == nil {
				continue
			}
			value := numeric(entry[metric])
			z, ok := stats.RobustZ(values, value)
			if !ok || mathAbs(z) < 3.5 {
				continue
			}
			direction := "low"
			if z > 0 {
				direction = "high"
			}
			anomalies = append(anomalies, obj(
				"date", entry["date"],
				"metric", metric,
				"value", entry[metric],
				"z", frac(z, 2),
				"direction", direction,
			))
		}
	}
	return mapSlice(daily), anomalies
}

// window7d mirrors telemetry.py:window_7d.
func window7d(entries []map[string]any, index int) []map[string]any {
	parsed, err := time.Parse("2006-01-02", entries[index]["date"].(string))
	if err != nil {
		return entries[:index+1]
	}
	start := parsed.AddDate(0, 0, -6).Format("2006-01-02")
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries[:index+1] {
		if entry["date"].(string) >= start {
			out = append(out, entry)
		}
	}
	return out
}

// numeric extracts a float from a schema number.
func numeric(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	default:
		return 0
	}
}

// mathAbs is math.Abs without the import noise.
func mathAbs(value float64) float64 {
	if value < 0 {
		return -value
	}
	return value
}

// dailyByModelSection builds the per-model daily series.
func (b *builder) dailyByModelSection() []any {
	ledgerByModelDay := map[string][]*ocsource.LedgerEntry{}
	for _, entry := range b.ledger {
		if entry.CreatedMs == 0 {
			continue
		}
		key := entry.Model + "\x00" + entry.Ts.Format("2006-01-02")
		ledgerByModelDay[key] = append(ledgerByModelDay[key], entry)
	}
	out := make([]any, 0, len(b.models))
	for _, model := range b.models {
		rs := filterRows(b.rows, func(row *ocsource.Row) bool { return row.Model == model })
		byDate := map[string][]*ocsource.Row{}
		dates := make([]string, 0)
		for _, row := range rs {
			day := localDate(row)
			if _, ok := byDate[day]; !ok {
				dates = append(dates, day)
			}
			byDate[day] = append(byDate[day], row)
		}
		sort.Strings(dates)
		series := make([]any, 0, len(dates))
		for _, date := range dates {
			dayRows := byDate[date]
			dayTimed := make([]*ocsource.Row, 0, len(dayRows))
			var generated, visible, reasoning, cacheRead, totalIn int64
			costs := make([]float64, len(dayRows))
			for i, row := range dayRows {
				generated += row.Gen
				visible += row.Out
				reasoning += row.Reas
				cacheRead += row.CacheRead
				totalIn += row.InpTotal()
				costs[i] = row.Cost
			}
			for _, row := range dayRows {
				if b.th.IsTimed(row) {
					dayTimed = append(dayTimed, row)
				}
			}
			modelLedger := ledgerByModelDay[model+"\x00"+date]
			errorLedgerRows := filterLedger(modelLedger, "error")
			skippedCost := fsumLedger(modelLedger)
			dayCost := fsumFloats(costs)
			cacheHitRate := any(nil)
			if totalIn != 0 {
				cacheHitRate = frac(float64(cacheRead)/float64(totalIn), 4)
			}
			var weighted, medianRate any
			if len(dayTimed) != 0 {
				timedGen := make([]float64, len(dayTimed))
				timedSeconds := make([]float64, len(dayTimed))
				rates := make([]float64, len(dayTimed))
				for i, row := range dayTimed {
					timedGen[i] = float64(row.Gen)
					timedSeconds[i] = *row.DT
					rates[i] = float64(row.Gen) / *row.DT
				}
				weighted = frac(sumFloats(timedGen)/sumFloats(timedSeconds), 1)
				medianRate = medianR1(rates)
			}
			ratioValues := ratiosOf(dayTimed)
			var ratio *float64
			if value, ok := stats.Median(ratioValues); ok {
				ratio = &value
			}
			var ratioValue, ratioPct any
			if ratio != nil {
				ratioValue = frac(*ratio, 6)
				ratioPct = frac(*ratio*100, 2)
			}
			series = append(series, obj(
				"date", date,
				"nAll", len(dayRows),
				"nTimed", len(dayTimed),
				"generatedTokens", generated,
				"visibleOutputTokens", visible,
				"reasoningTokens", reasoning,
				"costUsd", frac(dayCost, 6),
				"errorCostUsd", frac(fsumLedger(errorLedgerRows), 6),
				"skippedCostUsd", frac(skippedCost, 6),
				"totalSpendUsd", frac(dayCost+skippedCost, 6),
				"cacheHitRate", cacheHitRate,
				"weighted", weighted,
				"median", medianRate,
				"ratio", ratioValue,
				"ratioPct", ratioPct,
				"ratioN", len(ratioValues),
				"index", b.paceIndexValues(ratioValues),
			))
		}
		out = append(out, obj("model", model, "series", series))
	}
	return out
}

// hourlySection builds the fixed 24-hour series.
func (b *builder) hourlySection(rows []*ocsource.Row) []any {
	byHourAll := map[int][]*ocsource.Row{}
	for _, row := range rows {
		hour := row.Ts.Hour()
		byHourAll[hour] = append(byHourAll[hour], row)
	}
	ledgerByHour := map[int][]*ocsource.LedgerEntry{}
	for _, entry := range b.ledger {
		if entry.CreatedMs == 0 {
			continue
		}
		hour := entry.Ts.Hour()
		ledgerByHour[hour] = append(ledgerByHour[hour], entry)
	}

	hourly := make([]any, 0, 24)
	for hour := 0; hour < 24; hour++ {
		rs := byHourAll[hour]
		rsTimed := make([]*ocsource.Row, 0, len(rs))
		for _, row := range rs {
			if b.th.IsTimed(row) {
				rsTimed = append(rsTimed, row)
			}
		}
		textRows := make([]*ocsource.Row, 0, len(rsTimed))
		for _, row := range rsTimed {
			if !row.Tool {
				textRows = append(textRows, row)
			}
		}
		var generatedAll, timedTokens, visible, reasoning, cacheRead, totalIn int64
		timedSecondsValues := make([]float64, 0, len(rsTimed))
		rates := make([]float64, 0, len(rsTimed))
		textRates := make([]float64, 0, len(textRows))
		costs := make([]float64, len(rs))
		sessions := map[string]bool{}
		for i, row := range rs {
			generatedAll += row.Gen
			visible += row.Out
			reasoning += row.Reas
			cacheRead += row.CacheRead
			totalIn += row.InpTotal()
			costs[i] = row.Cost
			sessions[row.SessionID] = true
		}
		for _, row := range rsTimed {
			timedTokens += row.Gen
			timedSecondsValues = append(timedSecondsValues, *row.DT)
			rates = append(rates, float64(row.Gen) / *row.DT)
		}
		for _, row := range textRows {
			textRates = append(textRates, float64(row.Gen) / *row.DT)
		}
		timedSeconds := sumFloats(timedSecondsValues)
		hourLedger := ledgerByHour[hour]
		errorRows := filterLedger(hourLedger, "error")
		skippedCost := fsumLedger(hourLedger)
		errorCost := fsumLedger(errorRows)
		ratioValues := ratiosOf(rsTimed)
		textRatioValues := ratiosOf(textRows)
		var ratio *float64
		if value, ok := stats.Median(ratioValues); ok {
			ratio = &value
		}
		var cacheHitRate, weighted, medianRate, p90, ratioValue, ratioPct, index, textMedian, textRatio, textIndex any
		if totalIn != 0 {
			cacheHitRate = frac(float64(cacheRead)/float64(totalIn), 4)
		}
		if timedSeconds != 0 {
			weighted = frac(float64(timedTokens)/timedSeconds, 1)
		}
		if len(rates) != 0 {
			medianRate = medianR1(rates)
			p90 = percentileAny(sortedFloats(rates), 90)
		}
		if ratio != nil {
			ratioValue = frac(*ratio, 6)
			ratioPct = frac(*ratio*100, 2)
		}
		index = b.paceIndexValues(ratioValues)
		if len(textRates) != 0 {
			textMedian = medianR1(textRates)
		}
		if value, ok := stats.Median(textRatioValues); ok {
			textRatio = frac(value, 6)
		}
		textIndex = b.paceIndexValues(textRatioValues)
		hourly = append(hourly, obj(
			"hour", hour,
			"nAll", len(rs),
			"nTimed", len(rsTimed),
			"n", len(rsTimed),
			"tokens", timedTokens,
			"generatedTokens", generatedAll,
			"visibleOutputTokens", visible,
			"reasoningTokens", reasoning,
			"inputTotal", totalIn,
			"cacheReadTokens", cacheRead,
			"cacheHitRate", cacheHitRate,
			"costUsd", frac(fsumFloats(costs), 6),
			"errorCostUsd", frac(errorCost, 6),
			"skippedCostUsd", frac(skippedCost, 6),
			"totalSpendUsd", frac(fsumFloats(costs)+skippedCost, 6),
			"errorSteps", len(errorRows),
			"sessions", len(sessions),
			"weighted", weighted,
			"median", medianRate,
			"p90", p90,
			"ratio", ratioValue,
			"ratioPct", ratioPct,
			"ratioN", len(ratioValues),
			"index", index,
			"textN", len(textRows),
			"textMedian", textMedian,
			"textRatio", textRatio,
			"textIndex", textIndex,
		))
	}
	return hourly
}

// contextBuckets mirrors telemetry.py:context_buckets.
func (b *builder) contextBuckets(rows []*ocsource.Row) []any {
	type bucketDef struct {
		Label string
		Low   int64
		High  int64
	}
	defs := []bucketDef{
		{"<50k", 0, 50_000},
		{"50-150k", 50_000, 150_000},
		{"150-400k", 150_000, 400_000},
		{"400k+", 400_000, 1_000_000_000_000},
	}
	out := make([]any, 0, len(defs))
	for _, def := range defs {
		rs := make([]*ocsource.Row, 0, len(rows))
		for _, row := range rows {
			totalIn := row.InpTotal()
			if def.Low <= totalIn && totalIn < def.High {
				rs = append(rs, row)
			}
		}
		if len(rs) == 0 {
			continue
		}
		ratioValues := ratiosOf(rs)
		var ratio *float64
		if value, ok := stats.Median(ratioValues); ok {
			ratio = &value
		}
		rates := make([]float64, len(rs))
		genValues := make([]float64, len(rs))
		secondValues := make([]float64, len(rs))
		for i, row := range rs {
			rates[i] = float64(row.Gen) / *row.DT
			genValues[i] = float64(row.Gen)
			secondValues[i] = *row.DT
		}
		var ratioValue, ratioPct any
		if ratio != nil {
			ratioValue = frac(*ratio, 6)
			ratioPct = frac(*ratio*100, 2)
		}
		out = append(out, obj(
			"label", def.Label,
			"n", len(rs),
			"median", medianR1(rates),
			"weighted", frac(sumFloats(genValues)/sumFloats(secondValues), 1),
			"ratio", ratioValue,
			"ratioPct", ratioPct,
			"ratioN", len(ratioValues),
			"index", b.paceIndexValues(ratioValues),
		))
	}
	return out
}

// compactionImpact mirrors telemetry.py:compaction_impact.
func (b *builder) compactionImpact() map[string]any {
	bySession := map[string][]*ocsource.Row{}
	for _, row := range b.rows {
		bySession[row.SessionID] = append(bySession[row.SessionID], row)
	}
	for sid := range bySession {
		rows := bySession[sid]
		sort.SliceStable(rows, func(i, j int) bool { return rows[i].Ts.Before(rows[j].Ts) })
	}

	type pair struct {
		contextBefore int64
		contextAfter  int64
		rateBefore    *float64
		rateAfter     *float64
		costBefore    float64
		costAfter     float64
	}
	pairs := make([]pair, 0, len(b.loaded.Events.CompactionEvents))
	for _, event := range b.loaded.Events.CompactionEvents {
		if event.SessionID == "" || event.Ts == nil {
			continue
		}
		sessionRows := bySession[event.SessionID]
		var before, after []*ocsource.Row
		for _, row := range sessionRows {
			if b.th.IsTimed(row) && row.Ts.Before(*event.Ts) {
				before = append(before, row)
			}
		}
		for _, row := range sessionRows {
			if b.th.IsTimed(row) && row.Ts.After(*event.Ts) {
				after = append(after, row)
			}
		}
		if len(before) == 0 || len(after) == 0 {
			continue
		}
		old := before[len(before)-1]
		fresh := after[0]
		pairs = append(pairs, pair{
			contextBefore: old.InpTotal(),
			contextAfter:  fresh.InpTotal(),
			rateBefore:    rateOf(old),
			rateAfter:     rateOf(fresh),
			costBefore:    old.Cost,
			costAfter:     fresh.Cost,
		})
	}

	contextBeforeValues := make([]float64, len(pairs))
	contextAfterValues := make([]float64, len(pairs))
	contextDeltaValues := make([]float64, len(pairs))
	rateBeforeValues := make([]float64, 0, len(pairs))
	rateAfterValues := make([]float64, 0, len(pairs))
	rateDeltaValues := make([]float64, 0, len(pairs))
	costBeforeValues := make([]float64, len(pairs))
	costAfterValues := make([]float64, len(pairs))
	for i, item := range pairs {
		contextBeforeValues[i] = float64(item.contextBefore)
		contextAfterValues[i] = float64(item.contextAfter)
		contextDeltaValues[i] = float64(item.contextAfter - item.contextBefore)
		costBeforeValues[i] = item.costBefore
		costAfterValues[i] = item.costAfter
		if item.rateBefore != nil {
			rateBeforeValues = append(rateBeforeValues, *item.rateBefore)
		}
		if item.rateAfter != nil {
			rateAfterValues = append(rateAfterValues, *item.rateAfter)
		}
		if item.rateBefore != nil && *item.rateBefore != 0 && item.rateAfter != nil {
			rateDeltaValues = append(rateDeltaValues, (*item.rateAfter-*item.rateBefore) / *item.rateBefore * 100)
		}
	}
	return obj(
		"events", len(b.loaded.Events.CompactionEvents),
		"paired", len(pairs),
		"medianContextBefore", medianR2(contextBeforeValues),
		"medianContextAfter", medianR2(contextAfterValues),
		"medianContextDelta", medianR2(contextDeltaValues),
		"medianRateBefore", medianR2(rateBeforeValues),
		"medianRateAfter", medianR2(rateAfterValues),
		"medianRateDeltaPct", medianR1(rateDeltaValues),
		"medianCostBefore", medianR2(costBeforeValues),
		"medianCostAfter", medianR2(costAfterValues),
	)
}

// rateOf is `gen / dt` for a timed row.
func rateOf(row *ocsource.Row) *float64 {
	if row.DT == nil || *row.DT == 0 {
		return nil
	}
	value := float64(row.Gen) / *row.DT
	return &value
}

// compactionEventsSection renders the compaction event ledger.
func (b *builder) compactionEventsSection() []any {
	out := make([]any, len(b.loaded.Events.CompactionEvents))
	for i, event := range b.loaded.Events.CompactionEvents {
		var ts any
		if event.Ts != nil {
			ts = event.Ts.Unix()
		}
		out[i] = obj(
			"ts", ts,
			"sessionId", event.SessionID,
			"tokensBefore", event.TokensBefore,
		)
	}
	return out
}

// modelChangeEventsSection renders the model-switch event ledger.
func (b *builder) modelChangeEventsSection() []any {
	out := make([]any, len(b.loaded.Events.ModelSwitchEvents))
	for i, event := range b.loaded.Events.ModelSwitchEvents {
		var ts any
		if event.Ts != nil {
			ts = event.Ts.Unix()
		}
		out[i] = obj(
			"ts", ts,
			"sessionId", event.SessionID,
			"model", event.Model,
		)
	}
	return out
}

// errorLedgerSection renders the error/empty ledger rows.
func (b *builder) errorLedgerSection() []any {
	out := make([]any, 0, len(b.ledger))
	for _, entry := range b.ledger {
		var ts any
		if entry.CreatedMs != 0 {
			ts = entry.EpochSeconds()
		}
		project := entry.ProjectLabel
		if label, ok := b.projectLabels[entry.Project]; ok {
			project = label
		}
		out = append(out, obj(
			"ts", ts,
			"sessionId", entry.SessionID,
			"project", project,
			"projectPath", entry.Project,
			"entry", entry.Entry,
			"model", entry.Model,
			"provider", entry.Provider,
			"short", entry.Short,
			"agent", entry.Agent,
			"variant", entry.Variant,
			"finish", entry.Finish,
			"reason", entry.Reason,
			"outputTokens", entry.Out,
			"reasoningTokens", entry.Reas,
			"generatedTokens", entry.Gen,
			"freshInputTokens", entry.Inp,
			"cacheReadTokens", entry.CacheRead,
			"cacheWriteTokens", entry.CacheWrite,
			"costUsd", frac(entry.Cost, 6),
			"llmSeconds", optFrac(entry.DT, 3),
			"totalSeconds", optFrac(entry.TotalDT, 3),
			"toolSeconds", optFrac(entry.ToolDT, 3),
			"toolCalls", entry.NTools,
			"empty", entry.Empty,
			"error", entry.Error,
		))
	}
	return out
}
