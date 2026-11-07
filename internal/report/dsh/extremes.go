// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"sort"

	sourcedsh "github.com/jadmadi/agentmeter/internal/source/dsh"
)

// peakRow ports peak_row().
func peakRow(row *sourcedsh.Row, inputTotal func(*sourcedsh.Row) int64) map[string]any {
	result := map[string]any{
		"tps":        nil,
		"out":        row.Out,
		"dt":         nil,
		"model":      modelShort(row.Model),
		"at":         sourcedsh.FormatMinute(row.TS),
		"tool":       row.Tool,
		"inputTotal": inputTotal(row),
		"reason":     row.Timing,
		"sessionId":  row.SessionID,
		"project":    nil,
	}
	if row.TPS != nil {
		result["tps"] = r1(*row.TPS)
	}
	if row.DT != nil {
		result["dt"] = r1(*row.DT)
	}
	if row.ProjectLabel != nil {
		result["project"] = *row.ProjectLabel
	}
	return result
}

// extremes ports peaks/slowest/lowestRate/rejectedExtremes.
type extremes struct {
	peaks               []any
	slowest             []any
	lowestRate          []any
	rejectedExtremes    []any
	maxObserved         any
	maxRejected         any
	maxRejectedReason   any
	minIncluded         any
	maxDurationIncluded any
	maxDurationObserved any
	maxDurationAny      any
}

func buildExtremes(rows []*sourcedsh.Row, timed []*sourcedsh.Row, inputTotal func(*sourcedsh.Row) int64) extremes {
	validRate := make([]*sourcedsh.Row, 0, len(rows))
	fastRows := make([]*sourcedsh.Row, 0, len(rows))
	slowRows := make([]*sourcedsh.Row, 0, len(rows))
	durationRows := make([]*sourcedsh.Row, 0, len(rows))
	invalidRate := make([]*sourcedsh.Row, 0, len(rows))
	for _, row := range rows {
		if row.DT != nil && *row.DT >= 0.5 && row.Out > 0 {
			switch row.Timing {
			case "plausible", "fastRate", "slowRate":
				validRate = append(validRate, row)
				if row.Timing != "slowRate" {
					fastRows = append(fastRows, row)
				}
				if row.Timing != "fastRate" {
					slowRows = append(slowRows, row)
				}
			}
		}
		if row.DT != nil && *row.DT > 0 {
			durationRows = append(durationRows, row)
		}
		if row.DT != nil && row.Out > 0 {
			switch row.Timing {
			case "tooShort", "tooShortDuration", "noTiming":
				invalidRate = append(invalidRate, row)
			}
		}
	}

	byRateDesc := func(items []*sourcedsh.Row) {
		sort.SliceStable(items, func(i, j int) bool { return *items[i].TPS > *items[j].TPS })
	}
	byDurationDesc := func(items []*sourcedsh.Row) {
		sort.SliceStable(items, func(i, j int) bool { return *items[i].DT > *items[j].DT })
	}
	byRateAsc := func(items []*sourcedsh.Row) {
		sort.SliceStable(items, func(i, j int) bool { return *items[i].TPS < *items[j].TPS })
	}

	peakRows := make([]*sourcedsh.Row, len(fastRows))
	copy(peakRows, fastRows)
	byRateDesc(peakRows)
	if len(peakRows) > 10 {
		peakRows = peakRows[:10]
	}
	slowestRows := make([]*sourcedsh.Row, len(durationRows))
	copy(slowestRows, durationRows)
	byDurationDesc(slowestRows)
	if len(slowestRows) > 8 {
		slowestRows = slowestRows[:8]
	}
	lowestRows := make([]*sourcedsh.Row, len(slowRows))
	copy(lowestRows, slowRows)
	byRateAsc(lowestRows)
	if len(lowestRows) > 8 {
		lowestRows = lowestRows[:8]
	}
	rejectedRows := make([]*sourcedsh.Row, len(invalidRate))
	copy(rejectedRows, invalidRate)
	byRateDesc(rejectedRows)
	if len(rejectedRows) > 8 {
		rejectedRows = rejectedRows[:8]
	}

	result := extremes{
		peaks:            mapPeakRows(peakRows, inputTotal),
		slowest:          mapPeakRows(slowestRows, inputTotal),
		lowestRate:       mapPeakRows(lowestRows, inputTotal),
		rejectedExtremes: mapPeakRows(rejectedRows, inputTotal),
	}
	if len(validRate) > 0 {
		rates := make([]float64, 0, len(validRate))
		durations := make([]float64, 0, len(validRate))
		for _, row := range validRate {
			rates = append(rates, *row.TPS)
			durations = append(durations, *row.DT)
		}
		sort.Float64s(rates)
		sort.Float64s(durations)
		result.maxObserved = r1(rates[len(rates)-1])
		result.maxDurationObserved = r1(durations[len(durations)-1])
	}
	if len(invalidRate) > 0 {
		rates := make([]float64, 0, len(invalidRate))
		for _, row := range invalidRate {
			rates = append(rates, *row.TPS)
		}
		sort.Float64s(rates)
		result.maxRejected = r1(rates[len(rates)-1])
		// Python's max() keeps the first maximal item.
		best := invalidRate[0]
		for _, row := range invalidRate[1:] {
			if *row.TPS > *best.TPS {
				best = row
			}
		}
		result.maxRejectedReason = best.Timing
	}
	if len(timed) > 0 {
		minRate := *timed[0].TPS
		maxDuration := *timed[0].DT
		for _, row := range timed[1:] {
			if *row.TPS < minRate {
				minRate = *row.TPS
			}
			if *row.DT > maxDuration {
				maxDuration = *row.DT
			}
		}
		result.minIncluded = r1(minRate)
		result.maxDurationIncluded = r1(maxDuration)
	}
	if len(durationRows) > 0 {
		durations := make([]float64, 0, len(durationRows))
		for _, row := range durationRows {
			durations = append(durations, *row.DT)
		}
		sort.Float64s(durations)
		result.maxDurationAny = r1(durations[len(durations)-1])
	}
	return result
}

func mapPeakRows(rows []*sourcedsh.Row, inputTotal func(*sourcedsh.Row) int64) []any {
	result := make([]any, 0, len(rows))
	for _, row := range rows {
		result = append(result, peakRow(row, inputTotal))
	}
	return result
}
