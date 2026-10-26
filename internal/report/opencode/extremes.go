// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"sort"

	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
	"github.com/jadmadi/agentmeter/internal/stats"
)

// extremesResult carries the peak tables and the censoring facts the timed
// summary needs.
type extremesResult struct {
	peaks               []any
	slowest             []any
	lowestRate          []any
	rejectedExtremes    []any
	observedRates       []float64
	rejectedRates       []float64
	maxRejectedReason   any
	minIncluded         any
	maxDurationIncluded any
	maxDurationObserved any
	maxDurationAny      any
}

// peakRow mirrors telemetry.py:peak_row.
func peakRow(row *ocsource.Row) map[string]any {
	var tps, dt any
	if row.DT != nil {
		if *row.DT != 0 {
			tps = stats.R1(float64(row.Gen) / *row.DT)
		}
		dt = stats.R1(*row.DT)
	}
	return obj(
		"tps", tps,
		"out", row.Gen,
		"visible", row.Out,
		"reasoning", row.Reas,
		"dt", dt,
		"model", row.Short,
		"at", row.Ts.Format("2006-01-02 15:04"),
		"tool", row.Tool,
		"inputTotal", row.InpTotal(),
		"reason", row.TimingBucket,
		"sessionId", row.SessionID,
		"project", row.ProjectLabel,
	)
}

// extremesSection builds peaks, slowest, lowestRate, rejectedExtremes and the
// censoring bounds.
func (b *builder) extremesSection() extremesResult {
	rows := b.rows
	validRate := make([]*ocsource.Row, 0, len(rows))
	fastRows := make([]*ocsource.Row, 0, len(rows))
	slowRows := make([]*ocsource.Row, 0, len(rows))
	durationRows := make([]*ocsource.Row, 0, len(rows))
	invalidRate := make([]*ocsource.Row, 0, len(rows))
	for _, row := range rows {
		if row.DT != nil && *row.DT > 0 {
			durationRows = append(durationRows, row)
		}
		if row.DT != nil && *row.DT >= 0.5 && row.Gen > 0 {
			switch row.TimingBucket {
			case "plausible", "fastRate", "slowRate":
				validRate = append(validRate, row)
			}
		}
		if row.DT != nil && row.Gen > 0 {
			switch row.TimingBucket {
			case "tooShort", "tooShortDuration", "noTiming":
				invalidRate = append(invalidRate, row)
			}
		}
		if row.DT != nil && *row.DT >= 0.5 && row.Gen > 0 {
			switch row.TimingBucket {
			case "plausible", "fastRate":
				fastRows = append(fastRows, row)
			}
			switch row.TimingBucket {
			case "plausible", "slowRate":
				slowRows = append(slowRows, row)
			}
		}
	}

	rateOfRow := func(row *ocsource.Row) float64 { return float64(row.Gen) / *row.DT }

	fastSorted := make([]*ocsource.Row, len(fastRows))
	copy(fastSorted, fastRows)
	sort.SliceStable(fastSorted, func(i, j int) bool { return rateOfRow(fastSorted[i]) > rateOfRow(fastSorted[j]) })
	slowSorted := make([]*ocsource.Row, len(slowRows))
	copy(slowSorted, slowRows)
	sort.SliceStable(slowSorted, func(i, j int) bool { return rateOfRow(slowSorted[i]) < rateOfRow(slowSorted[j]) })
	durationSorted := make([]*ocsource.Row, len(durationRows))
	copy(durationSorted, durationRows)
	sort.SliceStable(durationSorted, func(i, j int) bool { return *durationSorted[i].DT > *durationSorted[j].DT })
	invalidSorted := make([]*ocsource.Row, len(invalidRate))
	copy(invalidSorted, invalidRate)
	sort.SliceStable(invalidSorted, func(i, j int) bool { return rateOfRow(invalidSorted[i]) > rateOfRow(invalidSorted[j]) })

	out := extremesResult{
		peaks:            peakTable(fastSorted, 10),
		slowest:          peakTable(durationSorted, 8),
		lowestRate:       peakTable(slowSorted, 8),
		rejectedExtremes: peakTable(invalidSorted, 8),
	}
	for _, row := range validRate {
		out.observedRates = append(out.observedRates, rateOfRow(row))
	}
	sort.Float64s(out.observedRates)
	for _, row := range invalidRate {
		out.rejectedRates = append(out.rejectedRates, rateOfRow(row))
	}
	sort.Float64s(out.rejectedRates)

	if len(invalidRate) > 0 {
		// Python's max() keeps the first maximal element.
		best := invalidRate[0]
		for _, row := range invalidRate[1:] {
			if rateOfRow(row) > rateOfRow(best) {
				best = row
			}
		}
		out.maxRejectedReason = best.TimingBucket
	}
	if len(b.timed) > 0 {
		rates := make([]float64, len(b.timed))
		durations := make([]float64, len(b.timed))
		for i, row := range b.timed {
			rates[i] = float64(row.Gen) / *row.DT
			durations[i] = *row.DT
		}
		if smallest := pyMin(rates); smallest != nil {
			out.minIncluded = stats.R1(*smallest)
		}
		if largest := pyMax(durations); largest != nil {
			out.maxDurationIncluded = stats.R1(*largest)
		}
	}
	if len(validRate) > 0 {
		durations := make([]float64, len(validRate))
		for i, row := range validRate {
			durations[i] = *row.DT
		}
		sort.Float64s(durations)
		out.maxDurationObserved = stats.R1(durations[len(durations)-1])
	}
	if len(durationRows) > 0 {
		durations := make([]float64, len(durationRows))
		for i, row := range durationRows {
			durations[i] = *row.DT
		}
		sort.Float64s(durations)
		out.maxDurationAny = stats.R1(durations[len(durations)-1])
	}
	return out
}

// peakTable renders up to limit peak rows.
func peakTable(rows []*ocsource.Row, limit int) []any {
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]any, len(rows))
	for i, row := range rows {
		out[i] = peakRow(row)
	}
	return out
}

// histogramCounts mirrors telemetry.py:histogram (bisect_right - 1, clamped).
func histogramCounts(rows []*ocsource.Row, edges []float64) []any {
	counts := make([]int, len(edges)-1)
	for _, row := range rows {
		value := float64(row.Gen) / *row.DT
		index := sort.Search(len(edges), func(i int) bool { return edges[i] > value }) - 1
		if index < 0 {
			index = 0
		}
		if index > len(counts)-1 {
			index = len(counts) - 1
		}
		counts[index]++
	}
	out := make([]any, len(counts))
	for i, count := range counts {
		out[i] = count
	}
	return out
}

// bucketize mirrors telemetry.py:bucketize.
func bucketize(rows []*ocsource.Row, defs []struct {
	Label string
	Low   int64
	High  int64
}) []any {
	out := make([]any, 0, len(defs))
	for _, def := range defs {
		rs := make([]*ocsource.Row, 0, len(rows))
		for _, row := range rows {
			if def.Low <= row.Gen && row.Gen < def.High {
				rs = append(rs, row)
			}
		}
		if len(rs) == 0 {
			continue
		}
		rates := make([]float64, len(rs))
		genValues := make([]float64, len(rs))
		secondValues := make([]float64, len(rs))
		var tokens int64
		for i, row := range rs {
			rates[i] = float64(row.Gen) / *row.DT
			genValues[i] = float64(row.Gen)
			secondValues[i] = *row.DT
			tokens += row.Gen
		}
		sort.Float64s(rates)
		out = append(out, obj(
			"label", def.Label,
			"n", len(rs),
			"tokens", tokens,
			"weighted", frac(sumFloats(genValues)/sumFloats(secondValues), 1),
			"median", medianR1(rates),
			"p90", percentileAny(rates, 90),
		))
	}
	return out
}
