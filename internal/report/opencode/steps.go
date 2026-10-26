// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"sort"

	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
)

// sentinelFrac renders round(value, ndigits) or the -1 sentinel.
func sentinelFrac(value *float64, ndigits int) any {
	if value == nil {
		return int64(-1)
	}
	return frac(*value, ndigits)
}

// stepsSection builds the 32-slot compact step tuples, sorted by epoch.
func (b *builder) stepsSection(rows []*ocsource.Row, l *lookups) []any {
	out := make([]any, 0, len(rows))
	for _, row := range rows {
		calls := make([]any, 0, len(row.ToolDetails))
		for _, call := range row.ToolDetails {
			calls = append(calls, []any{
				l.toolIndex[call.Name],
				sentinelFrac(call.Runtime, 3),
				boolToInt(call.Error),
				frac(call.Allocated, 3),
			})
		}
		tuple := []any{
			row.EpochSeconds(),
			l.modelIndex[row.Model],
			l.toolIndex[row.PrimaryTool],
			row.Out,
			sentinelFrac(row.DT, 3),
			row.Inp,
			row.CacheRead,
			row.CacheWrite,
			row.Cost,
			l.agentIndex[row.Agent],
			boolToInt(row.Thinking),
			l.projectIndex[row.Project],
			l.entrypointIndex[row.Entry],
			l.sessionIndex[row.SessionID],
			row.NTools,
			row.NThinking,
			row.NText,
			row.Reas,
			sentinelFrac(row.TotalDT, 3),
			sentinelFrac(row.ToolDT, 3),
			row.InpTotal(),
			l.variantIndex[row.Variant],
			l.finishIndex[row.Finish],
			sentinelFrac(row.TTFT, 3),
			sentinelFrac(row.DecodeDT, 3),
			sentinelFrac(row.DecodeRate, 2),
			sentinelFrac(row.OverheadDT, 3),
			sentinelFrac(row.ToolSpanDT, 3),
			sentinelFrac(row.ToolGapDT, 3),
			calls,
			sentinelFrac(row.Ratio, 4),
			l.timingIndex[row.TimingBucket],
		}
		out = append(out, tuple)
	}
	// Python sorts the tuples by their epoch only; the sort is stable.
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].([]any)[0].(int64) < out[j].([]any)[0].(int64)
	})
	return out
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
