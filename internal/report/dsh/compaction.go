// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"sort"

	sourcedsh "github.com/jadmadi/agentmeter/internal/source/dsh"
)

// compactionImpact ports compaction_impact(): pair the nearest timed step
// before and after each compaction event in the same session.
func compactionImpact(
	rows []*sourcedsh.Row,
	events []sourcedsh.CompactionEvent,
	inputTotal func(*sourcedsh.Row) int64,
) map[string]any {
	type pair struct {
		contextBefore int64
		contextAfter  int64
		rateBefore    *float64
		rateAfter     *float64
		costBefore    float64
		costAfter     float64
	}
	bySession := make(map[string][]*sourcedsh.Row, 64)
	for _, row := range rows {
		bySession[row.SessionID] = append(bySession[row.SessionID], row)
	}
	for _, sessionRows := range bySession {
		sort.SliceStable(sessionRows, func(i, j int) bool { return sessionRows[i].TS.Before(sessionRows[j].TS) })
	}

	pairs := make([]pair, 0, len(events))
	for _, event := range events {
		if event.SessionID == nil || *event.SessionID == "" || event.TS == nil {
			continue
		}
		sessionRows := bySession[*event.SessionID]
		var before, after *sourcedsh.Row
		for _, row := range sessionRows {
			if row.Timing != "plausible" {
				continue
			}
			if row.TS.Before(*event.TS) {
				before = row
			}
		}
		for _, row := range sessionRows {
			if row.Timing != "plausible" {
				continue
			}
			if row.TS.After(*event.TS) {
				after = row
				break
			}
		}
		if before == nil || after == nil {
			continue
		}
		pairs = append(pairs, pair{
			contextBefore: inputTotal(before),
			contextAfter:  inputTotal(after),
			rateBefore:    before.TPS,
			rateAfter:     after.TPS,
			costBefore:    before.Cost,
			costAfter:     after.Cost,
		})
	}

	medianOf := func(values []float64) any {
		if len(values) == 0 {
			return nil
		}
		return r2(median(values))
	}
	contextBefore := make([]float64, 0, len(pairs))
	contextAfter := make([]float64, 0, len(pairs))
	rateBefore := make([]float64, 0, len(pairs))
	rateAfter := make([]float64, 0, len(pairs))
	costBefore := make([]float64, 0, len(pairs))
	costAfter := make([]float64, 0, len(pairs))
	contextDeltas := make([]float64, 0, len(pairs))
	rateDeltas := make([]float64, 0, len(pairs))
	for _, item := range pairs {
		contextBefore = append(contextBefore, float64(item.contextBefore))
		contextAfter = append(contextAfter, float64(item.contextAfter))
		costBefore = append(costBefore, item.costBefore)
		costAfter = append(costAfter, item.costAfter)
		contextDeltas = append(contextDeltas, float64(item.contextAfter-item.contextBefore))
		if item.rateBefore != nil {
			rateBefore = append(rateBefore, *item.rateBefore)
		}
		if item.rateAfter != nil {
			rateAfter = append(rateAfter, *item.rateAfter)
		}
		if item.rateBefore != nil && *item.rateBefore != 0 && item.rateAfter != nil {
			rateDeltas = append(rateDeltas, (*item.rateAfter-*item.rateBefore) / *item.rateBefore * 100)
		}
	}

	var medianContextDelta, medianRateDelta any
	if len(contextDeltas) > 0 {
		medianContextDelta = r2(median(contextDeltas))
	}
	if len(rateDeltas) > 0 {
		medianRateDelta = r1(median(rateDeltas))
	}
	return map[string]any{
		"events":              int64(len(events)),
		"paired":              int64(len(pairs)),
		"medianContextBefore": medianOf(contextBefore),
		"medianContextAfter":  medianOf(contextAfter),
		"medianContextDelta":  medianContextDelta,
		"medianRateBefore":    medianOf(rateBefore),
		"medianRateAfter":     medianOf(rateAfter),
		"medianRateDeltaPct":  medianRateDelta,
		"medianCostBefore":    medianOf(costBefore),
		"medianCostAfter":     medianOf(costAfter),
	}
}
