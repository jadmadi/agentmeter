// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
	"github.com/jadmadi/agentmeter/internal/stats"
)

// directStats is the per-session direct (subagent-exclusive) aggregate used by
// the rollup columns.
type directStats struct {
	steps        int
	generated    int64
	cost         float64
	skippedCost  float64
	errorSteps   int
	emptySteps   int
	ledgerSteps  int
	errorCost    float64
	errorSeconds float64
	inputTotal   int64
	cacheRead    int64
}

// sessionsSection mirrors the Python session list and its rollups.
func (b *builder) sessionsSection(rows []*ocsource.Row, l *lookups) []any {
	sessionRows := map[string][]*ocsource.Row{}
	for _, row := range rows {
		sessionRows[row.SessionID] = append(sessionRows[row.SessionID], row)
	}
	ledgerBySession := map[string][]*ocsource.LedgerEntry{}
	for _, entry := range b.ledger {
		ledgerBySession[entry.SessionID] = append(ledgerBySession[entry.SessionID], entry)
	}

	direct := map[string]*directStats{}
	for _, sid := range l.sessionIDs {
		rs := sessionRows[sid]
		ledgerRows := ledgerBySession[sid]
		errorRows := filterLedger(ledgerRows, "error")
		emptyRows := filterLedger(ledgerRows, "empty")
		stat := &directStats{
			steps:        len(rs),
			skippedCost:  fsumLedger(ledgerRows),
			errorSteps:   len(errorRows),
			emptySteps:   len(emptyRows),
			ledgerSteps:  len(ledgerRows),
			errorCost:    fsumLedger(errorRows),
			errorSeconds: fsumLedgerSeconds(errorRows),
		}
		for _, row := range rs {
			stat.generated += row.Gen
			stat.inputTotal += row.InpTotal()
			stat.cacheRead += row.CacheRead
		}
		stat.cost = fsumRowCosts(rs)
		direct[sid] = stat
	}
	for sid, ledgerRows := range ledgerBySession {
		if _, ok := direct[sid]; ok {
			continue
		}
		errorRows := filterLedger(ledgerRows, "error")
		direct[sid] = &directStats{
			steps:        0,
			generated:    0,
			cost:         0,
			skippedCost:  fsumLedger(ledgerRows),
			errorSteps:   len(errorRows),
			emptySteps:   len(ledgerRows) - len(errorRows),
			ledgerSteps:  len(ledgerRows),
			errorCost:    fsumLedger(errorRows),
			errorSeconds: fsumLedgerSeconds(errorRows),
			inputTotal:   0,
			cacheRead:    0,
		}
	}

	children := map[string][]string{}
	for _, id := range b.loaded.SessionMetaOrder {
		meta := b.loaded.SessionsMeta[id]
		if meta.Parent != nil && *meta.Parent != "" {
			children[*meta.Parent] = append(children[*meta.Parent], id)
		}
	}
	descendantCache := map[string][]string{}
	var descendants func(string) []string
	descendants = func(sid string) []string {
		if found, ok := descendantCache[sid]; ok {
			return found
		}
		found := make([]string, 0, 4)
		seen := map[string]bool{}
		stack := append([]string{}, children[sid]...)
		for len(stack) > 0 {
			child := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if seen[child] {
				continue
			}
			seen[child] = true
			found = append(found, child)
			stack = append(stack, children[child]...)
		}
		descendantCache[sid] = found
		return found
	}

	compactionBySession := map[string]int{}
	for _, event := range b.loaded.Events.CompactionEvents {
		if event.SessionID != "" {
			compactionBySession[event.SessionID]++
		}
	}

	out := make([]any, 0, len(l.sessionIDs))
	for _, sid := range l.sessionIDs {
		rs := sessionRows[sid]
		stamps := make([]int64, len(rs))
		for i, row := range rs {
			stamps[i] = row.EpochSeconds()
		}
		firstTs := minInt64(stamps)
		lastTs := maxInt64(stamps)
		wallMinutes := float64(lastTs-firstTs) / 60.0
		activeValues := make([]float64, 0, len(rs))
		for _, row := range rs {
			if row.TotalDT != nil {
				activeValues = append(activeValues, *row.TotalDT)
			}
		}
		activeMinutes := sumFloats(activeValues) / 60.0
		idleMinutes := wallMinutes - activeMinutes
		if idleMinutes < 0 {
			idleMinutes = 0
		}
		timedRows := make([]*ocsource.Row, 0, len(rs))
		for _, row := range rs {
			if b.th.IsTimed(row) {
				timedRows = append(timedRows, row)
			}
		}
		var genTokens, cacheRead, totalIn int64
		for _, row := range rs {
			genTokens += row.Gen
			cacheRead += row.CacheRead
			totalIn += row.InpTotal()
		}
		projectPath := rs[0].Project
		projectLabel := b.projectLabels[projectPath]
		ledgerRows := ledgerBySession[sid]
		errorRows := filterLedger(ledgerRows, "error")
		emptyRows := filterLedger(ledgerRows, "empty")
		skippedCost := fsumLedger(ledgerRows)
		errorCost := fsumLedger(errorRows)
		errorSeconds := fsumLedgerSeconds(errorRows)

		childrenIDs := descendants(sid)
		rollupIDs := append([]string{sid}, childrenIDs...)
		rollupSteps := 0
		var rollupGenerated int64
		rollupCostValues := make([]float64, 0, len(rollupIDs))
		rollupSkippedValues := make([]float64, 0, len(rollupIDs))
		for _, item := range rollupIDs {
			if stat, ok := direct[item]; ok {
				rollupSteps += stat.steps
				rollupGenerated += stat.generated
				rollupCostValues = append(rollupCostValues, stat.cost)
				rollupSkippedValues = append(rollupSkippedValues, stat.skippedCost)
			} else {
				rollupCostValues = append(rollupCostValues, 0)
				rollupSkippedValues = append(rollupSkippedValues, 0)
			}
		}
		subagentCostValues := make([]float64, 0, len(childrenIDs))
		for _, item := range childrenIDs {
			stat, ok := direct[item]
			if !ok {
				subagentCostValues = append(subagentCostValues, 0)
				continue
			}
			subagentCostValues = append(subagentCostValues, stat.cost+stat.skippedCost)
		}
		compactions := compactionBySession[sid]
		for _, item := range childrenIDs {
			compactions += compactionBySession[item]
		}

		outcome := "incomplete"
		hasError := false
		if stat, ok := direct[sid]; ok && stat.errorSteps > 0 {
			hasError = true
		}
		for _, item := range childrenIDs {
			if stat, ok := direct[item]; ok && stat.errorSteps > 0 {
				hasError = true
			}
		}
		switch {
		case hasError:
			outcome = "error"
		case compactions > 0:
			outcome = "compacted"
		case len(rs) > 0 && rs[len(rs)-1].Finish == "stop":
			outcome = "completed"
		}

		displayID := truncateRunes(sid, 16)
		title := displayID
		if rs[0].SessionTitle != nil && *rs[0].SessionTitle != "" {
			title = *rs[0].SessionTitle
		}
		models := map[string]bool{}
		for _, row := range rs {
			models[row.Short] = true
		}
		var cacheHitRate, weighted, outputPerMinute, wallOutputPerMinute any
		if totalIn != 0 {
			cacheHitRate = frac(float64(cacheRead)/float64(totalIn), 4)
		}
		if len(timedRows) != 0 {
			timedGen := make([]float64, len(timedRows))
			timedSeconds := make([]float64, len(timedRows))
			for i, row := range timedRows {
				timedGen[i] = float64(row.Gen)
				timedSeconds[i] = *row.DT
			}
			weighted = frac(sumFloats(timedGen)/sumFloats(timedSeconds), 1)
		}
		if activeMinutes >= 0.5 {
			outputPerMinute = frac(float64(genTokens)/activeMinutes, 1)
		}
		if wallMinutes >= 0.5 {
			wallOutputPerMinute = frac(float64(genTokens)/wallMinutes, 1)
		}
		costs := make([]float64, len(rs))
		for i, row := range rs {
			costs[i] = row.Cost
		}

		out = append(out, obj(
			"id", displayID,
			"sessionId", sid,
			"title", title,
			"project", projectLabel,
			"projectPath", projectPath,
			"entrypoint", rs[0].Entry,
			"agent", rs[0].Agent,
			"models", toAnyStrings(sortedSet(models)),
			"steps", len(rs),
			"toolSteps", countRows(rs, func(row *ocsource.Row) bool { return row.Tool }),
			"textSteps", countRows(rs, func(row *ocsource.Row) bool { return !row.Tool }),
			"outputTokens", int64(sumField(rs, func(row *ocsource.Row) int64 { return row.Out })),
			"visibleOutputTokens", int64(sumField(rs, func(row *ocsource.Row) int64 { return row.Out })),
			"reasoningTokens", sumField(rs, func(row *ocsource.Row) int64 { return row.Reas }),
			"generatedTokens", genTokens,
			"inputTokens", totalIn,
			"inputRawTokens", sumField(rs, func(row *ocsource.Row) int64 { return row.Inp }),
			"freshInputTokens", sumField(rs, func(row *ocsource.Row) int64 { return row.Inp }),
			"inputTotal", totalIn,
			"cacheReadTokens", cacheRead,
			"cacheWriteTokens", sumField(rs, func(row *ocsource.Row) int64 { return row.CacheWrite }),
			"cacheHitRate", cacheHitRate,
			"costUsd", frac(fsumFloats(costs), 6),
			"skippedCostUsd", frac(skippedCost, 6),
			"totalSpendUsd", frac(fsumFloats(costs)+skippedCost, 6),
			"errorSteps", len(errorRows),
			"emptySteps", len(emptyRows),
			"ledgerSteps", len(ledgerRows),
			"duplicateSteps", b.loaded.DuplicateSessions[sid],
			"errorCostUsd", frac(errorCost, 6),
			"errorSeconds", frac(errorSeconds, 3),
			"timedN", len(timedRows),
			"lowSample", len(timedRows) < 30,
			"weighted", weighted,
			"firstTs", firstTs,
			"lastTs", lastTs,
			"spanMinutes", frac(wallMinutes, 1),
			"activeMinutes", frac(activeMinutes, 1),
			"idleMinutes", frac(idleMinutes, 1),
			"outputPerMinute", outputPerMinute,
			"wallOutputPerMinute", wallOutputPerMinute,
			"compactions", compactions,
			"outcome", outcome,
			"subagentSessions", len(childrenIDs),
			"subagentCostUsd", frac(sumFloats(subagentCostValues), 6),
			"rollupSteps", rollupSteps,
			"rollupGeneratedTokens", int64(rollupGenerated),
			"rollupCostUsd", frac(sumFloats(rollupCostValues), 6),
			"rollupTotalSpendUsd", frac(sumFloats(rollupCostValues)+sumFloats(rollupSkippedValues), 6),
		))
	}
	return out
}

// filterLedger keeps the entries whose reason includes flag.
func filterLedger(entries []*ocsource.LedgerEntry, flag string) []*ocsource.LedgerEntry {
	out := make([]*ocsource.LedgerEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Reason == flag || entry.Reason == "both" {
			out = append(out, entry)
		}
	}
	return out
}

// fsumLedger is math.fsum over ledger costs.
func fsumLedger(entries []*ocsource.LedgerEntry) float64 {
	values := make([]float64, len(entries))
	for i, entry := range entries {
		values[i] = entry.Cost
	}
	return fsumFloats(values)
}

// fsumLedgerSeconds is math.fsum(entry.totalDt or 0).
func fsumLedgerSeconds(entries []*ocsource.LedgerEntry) float64 {
	values := make([]float64, len(entries))
	for i, entry := range entries {
		values[i] = orZero(entry.TotalDT)
	}
	return fsumFloats(values)
}

// fsumRowCosts is math.fsum over row costs.
func fsumRowCosts(rows []*ocsource.Row) float64 {
	values := make([]float64, len(rows))
	for i, row := range rows {
		values[i] = row.Cost
	}
	return stats.Fsum(values)
}

func minInt64(values []int64) int64 {
	smallest := values[0]
	for _, value := range values[1:] {
		if value < smallest {
			smallest = value
		}
	}
	return smallest
}

func maxInt64(values []int64) int64 {
	largest := values[0]
	for _, value := range values[1:] {
		if value > largest {
			largest = value
		}
	}
	return largest
}

// truncateRunes mirrors Python's sid[:16] (code points, not bytes).
func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
