// SPDX-License-Identifier: AGPL-3.0-only

package ocsource

import (
	"sort"

	"github.com/jadmadi/agentmeter/internal/stats"
)

// Interval is one millisecond [Start, End] window.
type Interval struct {
	Start int64
	End   int64
}

// MergeIntervals returns (unionSeconds, spanSeconds) for millisecond
// intervals: telemetry.py:merge_intervals. Overlapping or parallel tool calls
// are counted once; the span is first start to last end.
func MergeIntervals(intervals []Interval) (unionSeconds float64, spanSeconds float64) {
	if len(intervals) == 0 {
		return 0, 0
	}
	ordered := make([]Interval, len(intervals))
	copy(ordered, intervals)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Start != ordered[j].Start {
			return ordered[i].Start < ordered[j].Start
		}
		return ordered[i].End < ordered[j].End
	})
	var total int64
	curStart, curEnd := ordered[0].Start, ordered[0].End
	for _, item := range ordered[1:] {
		if item.Start <= curEnd {
			if item.End > curEnd {
				curEnd = item.End
			}
		} else {
			total += curEnd - curStart
			curStart, curEnd = item.Start, item.End
		}
	}
	total += curEnd - curStart
	minStart := ordered[0].Start
	maxEnd := ordered[0].End
	for _, item := range ordered[1:] {
		if item.Start < minStart {
			minStart = item.Start
		}
		if item.End > maxEnd {
			maxEnd = item.End
		}
	}
	return float64(total) / 1000.0, float64(maxEnd-minStart) / 1000.0
}

// AllocateToolTime allocates non-overlapping tool time so per-tool shares are
// additive: telemetry.py:allocate_tool_time.
//
// Non-overlapping calls keep their own runtime. When calls overlap (parallel
// tools), the union duration is split proportionally to each call's runtime,
// so the allocations for a step sum to the step's tool union, never more.
func AllocateToolTime(calls []ToolCall) []float64 {
	allocations := make([]float64, len(calls))
	intervals := make([]Interval, 0, len(calls))
	durations := make([]float64, 0, len(calls))
	indexes := make([]int, 0, len(calls))
	for index, call := range calls {
		ran, done := call.Ran, call.Completed
		if ran != 0 && done != 0 && done > ran {
			intervals = append(intervals, Interval{Start: ran, End: done})
			durations = append(durations, float64(done-ran)/1000.0)
			indexes = append(indexes, index)
		}
	}
	if len(intervals) == 0 {
		return allocations
	}
	union, _ := MergeIntervals(intervals)
	totalRuntime := stats.Sum(durations)
	if totalRuntime <= 0 {
		return allocations
	}
	scale := 1.0
	if ratio := union / totalRuntime; ratio < 1.0 {
		scale = ratio
	}
	for i, index := range indexes {
		allocations[index] = durations[i] * scale
	}
	return allocations
}
