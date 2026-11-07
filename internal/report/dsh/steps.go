// SPDX-License-Identifier: AGPL-3.0-only

package dsh

import (
	"sort"
	"time"

	sourcedsh "github.com/jadmadi/agentmeter/internal/source/dsh"
)

// lookupIndexes mirrors the builder's ordered lookup tables and their
// reverse indexes.
type lookupIndexes struct {
	models        []string
	tools         []string
	efforts       []string
	projects      []string
	projectLabels []string
	entrypoints   []string
	sessionIDs    []string

	modelIndex      map[string]int
	toolIndex       map[string]int
	effortIndex     map[string]int
	projectIndex    map[string]int
	entrypointIndex map[string]int
	sessionIndex    map[string]int
	timingIndex     map[string]int
}

func buildLookupIndexes(rows []*sourcedsh.Row, models []string, labels map[string]string) *lookupIndexes {
	toolSet := map[string]struct{}{"(final answer)": {}}
	effortSet := map[string]struct{}{}
	projectSet := map[string]struct{}{}
	entrypointSet := map[string]struct{}{}
	sessionSet := map[string]struct{}{}
	for _, row := range rows {
		for _, detail := range row.ToolDetails {
			toolSet[detail.Name] = struct{}{}
		}
		effortSet[row.Effort] = struct{}{}
		projectSet[row.ProjectPath] = struct{}{}
		entrypointSet[row.Entrypoint] = struct{}{}
		sessionSet[row.SessionID] = struct{}{}
	}
	indexes := &lookupIndexes{
		models:          models,
		tools:           setToSortedSlice(toolSet),
		efforts:         setToSortedSlice(effortSet),
		projects:        setToSortedSlice(projectSet),
		entrypoints:     setToSortedSlice(entrypointSet),
		sessionIDs:      setToSortedSlice(sessionSet),
		modelIndex:      make(map[string]int, len(models)),
		toolIndex:       make(map[string]int, len(toolSet)),
		effortIndex:     make(map[string]int, len(effortSet)),
		projectIndex:    make(map[string]int, len(projectSet)),
		entrypointIndex: make(map[string]int, len(entrypointSet)),
		sessionIndex:    make(map[string]int, len(sessionSet)),
		timingIndex:     make(map[string]int, len(sourcedsh.TimingReasons)),
	}
	indexes.projectLabels = make([]string, 0, len(indexes.projects))
	for _, path := range indexes.projects {
		label := path
		if value, ok := labels[path]; ok {
			label = value
		}
		indexes.projectLabels = append(indexes.projectLabels, label)
	}
	for index, value := range models {
		indexes.modelIndex[value] = index
	}
	for index, value := range indexes.tools {
		indexes.toolIndex[value] = index
	}
	for index, value := range indexes.efforts {
		indexes.effortIndex[value] = index
	}
	for index, value := range indexes.projects {
		indexes.projectIndex[value] = index
	}
	for index, value := range indexes.entrypoints {
		indexes.entrypointIndex[value] = index
	}
	for index, value := range indexes.sessionIDs {
		indexes.sessionIndex[value] = index
	}
	for index, value := range sourcedsh.TimingReasons {
		indexes.timingIndex[value] = index
	}
	return indexes
}

func setToSortedSlice(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

// buildSteps ports the compact step table: 20 positional slots, sorted by
// epoch seconds with a stable sort.
func buildSteps(rows []*sourcedsh.Row, indexes *lookupIndexes) []any {
	type step struct {
		ts    int64
		tuple []any
	}
	steps := make([]step, 0, len(rows))
	for _, row := range rows {
		toolName := "(final answer)"
		if row.PrimaryTool != nil && *row.PrimaryTool != "" {
			toolName = *row.PrimaryTool
		}
		var duration any = int64(-1)
		if row.DT != nil {
			duration = pyRound(*row.DT, 2)
		}
		var ratio any = int64(-1)
		if row.Ratio != nil {
			ratio = pyRound(*row.Ratio, 4)
		}
		thinking := int64(0)
		if row.Thinking {
			thinking = 1
		}
		toolIndexes := make([]any, 0, len(row.Tools))
		for _, name := range row.Tools {
			toolIndexes = append(toolIndexes, indexes.toolIndex[name])
		}
		tuple := []any{
			sourcedsh.UnixSeconds(row.TS),
			indexes.modelIndex[row.Model],
			indexes.toolIndex[toolName],
			row.Out,
			duration,
			row.Inp,
			row.CacheRead,
			row.CacheWrite,
			pyRound(row.Cost, 6),
			indexes.effortIndex[row.Effort],
			thinking,
			indexes.projectIndex[row.ProjectPath],
			indexes.entrypointIndex[row.Entrypoint],
			indexes.sessionIndex[row.SessionID],
			row.NTools,
			int64(0), // Muse has no content blocks; Thinking comes from reasoning tokens
			int64(0),
			ratio,
			indexes.timingIndex[row.Timing],
			toolIndexes,
		}
		steps = append(steps, step{ts: sourcedsh.UnixSeconds(row.TS), tuple: tuple})
	}
	sort.SliceStable(steps, func(i, j int) bool { return steps[i].ts < steps[j].ts })
	result := make([]any, 0, len(steps))
	for _, item := range steps {
		result = append(result, item.tuple)
	}
	return result
}

// buildSessions ports the session rollup, aligned with session_idx.
func buildSessions(
	rows []*sourcedsh.Row,
	indexes *lookupIndexes,
	ledgerBySession map[string][]*sourcedsh.LedgerEntry,
	duplicateSessions map[string]int64,
	freshInput func(*sourcedsh.Row) int64,
	inputTotal func(*sourcedsh.Row) int64,
	outcomes map[string]string,
) []any {
	groups := newOrderedGroups()
	for _, row := range rows {
		groups.add(row.SessionID, row)
	}
	sessions := make([]any, 0, len(indexes.sessionIDs))
	for _, sessionID := range indexes.sessionIDs {
		rs := groups.values[sessionID]
		var firstTS, lastTS *timeValue
		for _, row := range rs {
			stamp := row.TS
			if firstTS == nil || stamp.Before(firstTS.at) {
				firstTS = &timeValue{at: stamp}
			}
			if lastTS == nil || stamp.After(lastTS.at) {
				lastTS = &timeValue{at: stamp}
			}
		}
		spanMinutes := 0.0
		if firstTS != nil && lastTS != nil {
			spanMinutes = sourcedsh.DeltaSeconds(lastTS.at.Sub(firstTS.at)) / 60
		}
		var output, reasoning, cacheRead, totalIn, rawInput, fresh int64
		costAcc := sumAcc{}
		timedRows := 0
		models := map[string]struct{}{}
		toolSteps := int64(0)
		subagentSteps := int64(0)
		for _, row := range rs {
			output += row.Out
			reasoning += row.Reas
			cacheRead += row.CacheRead
			totalIn += inputTotal(row)
			rawInput += row.Inp
			fresh += freshInput(row)
			costAcc.add(row.Cost)
			if row.Timing == "plausible" {
				timedRows++
			}
			models[modelShort(row.Model)] = struct{}{}
			if row.Tool {
				toolSteps++
			}
			if row.Subagent {
				subagentSteps++
			}
		}
		cost := costAcc.value()
		skipped := ledgerBySession[sessionID]
		skippedAcc := sumAcc{}
		for _, entry := range skipped {
			if entry.Reason != sourcedsh.ReasonDuplicate {
				skippedAcc.add(entry.CostUSD)
			}
		}
		skippedCost := skippedAcc.value()
		title := ""
		if rs[0].SessionTitle != nil && *rs[0].SessionTitle != "" {
			title = *rs[0].SessionTitle
		} else {
			title = slicePrefix(sessionID, 8)
		}
		project := rs[0].ProjectPath
		if rs[0].ProjectLabel != nil && *rs[0].ProjectLabel != "" {
			project = *rs[0].ProjectLabel
		}
		entrypoint := rs[0].Entrypoint
		if entrypoint == "" {
			entrypoint = "interactive"
		}
		item := map[string]any{
			"id":                  slicePrefix(sessionID, 8),
			"sessionId":           sessionID,
			"title":               title,
			"project":             project,
			"projectPath":         rs[0].ProjectPath,
			"entrypoint":          entrypoint,
			"branch":              optString(rs[0].Branch),
			"models":              setToSortedSlice(models),
			"steps":               int64(len(rs)),
			"toolSteps":           toolSteps,
			"textSteps":           int64(len(rs)) - toolSteps,
			"subagentSteps":       subagentSteps,
			"outputTokens":        output,
			"generatedTokens":     output,
			"visibleOutputTokens": output - reasoning,
			"reasoningTokens":     reasoning,
			"inputTokens":         totalIn,
			"inputRawTokens":      rawInput,
			"freshInputTokens":    fresh,
			"inputTotal":          totalIn,
			"cacheReadTokens":     cacheRead,
			"cacheWriteTokens":    cacheWriteSum(rs),
			"cacheHitRate":        rateOrNil(cacheRead, totalIn),
			"costUsd":             pyRound(cost, 6),
			"skippedSteps":        int64(len(skipped)),
			"skippedCostUsd":      pyRound(skippedCost, 6),
			"totalSpendUsd":       pyRound(cost+skippedCost, 6),
			"duplicateSteps":      duplicateSessions[sessionID],
			"timedN":              int64(timedRows),
			"weighted":            sessionWeighted(rs),
			"firstTs":             firstTS.unixOrNil(),
			"lastTs":              lastTS.unixOrNil(),
			"spanMinutes":         pyRound(spanMinutes, 1),
			"activeMinutes":       nil,
			"idleMinutes":         nil,
			"outputPerMinute":     perMinute(output, spanMinutes),
			"wallOutputPerMinute": perMinute(output, spanMinutes),
			"outcome":             sessionOutcome(sessionID, outcomes),
		}
		sessions = append(sessions, item)
	}
	return sessions
}

type timeValue struct {
	at time.Time
}

func (value *timeValue) unixOrNil() any {
	if value == nil {
		return nil
	}
	return sourcedsh.UnixSeconds(value.at)
}

func cacheWriteSum(rows []*sourcedsh.Row) int64 {
	var total int64
	for _, row := range rows {
		total += row.CacheWrite
	}
	return total
}

func sessionWeighted(rows []*sourcedsh.Row) any {
	tokens := sumAcc{}
	seconds := sumAcc{}
	counted := false
	for _, row := range rows {
		if row.Timing != "plausible" {
			continue
		}
		counted = true
		tokens.add(float64(row.Out))
		seconds.add(*row.DT)
	}
	if !counted {
		return nil
	}
	return pyRound(tokens.value()/seconds.value(), 1)
}

// sessionOutcome returns the session_end exit reason verbatim, or "unknown"
// when the main log recorded no session_end.
func sessionOutcome(sessionID string, outcomes map[string]string) string {
	if exit, ok := outcomes[sessionID]; ok && exit != "" {
		return exit
	}
	return "unknown"
}

func perMinute(output int64, spanMinutes float64) any {
	if spanMinutes < 0.5 {
		return nil
	}
	return pyRound(float64(output)/spanMinutes, 1)
}

func optFitValue(fits map[string]map[string]any, kind string) any {
	fit, ok := fits[kind]
	if !ok {
		return nil
	}
	return fit
}

func optString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

// slicePrefix is Python's `text[:n]` for the ASCII session ids in practice,
// rune-safe for anything else.
func slicePrefix(text string, size int) string {
	runes := []rune(text)
	if len(runes) <= size {
		return text
	}
	return string(runes[:size])
}

// buildRegression ports the regression list.
func buildRegression(models []string, fits map[string]map[string]map[string]any) []any {
	regressions := make([]any, 0, len(models))
	for _, model := range models {
		perModel := fits[model]
		regressions = append(regressions, map[string]any{
			"model": model,
			"short": modelShort(model),
			"all":   optFitValue(perModel, "all"),
			"text":  optFitValue(perModel, "text"),
			"tool":  optFitValue(perModel, "tool"),
		})
	}
	return regressions
}

// buildFits ports the fits array: per model, the three fit dicts.
func buildFits(models []string, fits map[string]map[string]map[string]any) []any {
	result := make([]any, 0, len(models))
	for _, model := range models {
		perModel := fits[model]
		result = append(result, map[string]any{
			"all":  optFitValue(perModel, "all"),
			"text": optFitValue(perModel, "text"),
			"tool": optFitValue(perModel, "tool"),
		})
	}
	return result
}

// buildPrices ports the prices array (six keys, including the semantics
// source).
func buildPrices(models []string, costByModel []any, semantics map[string]inputSemantics) []any {
	byModel := make(map[string]map[string]any, len(costByModel))
	for _, entry := range costByModel {
		item, _ := entry.(map[string]any)
		model, _ := item["model"].(string)
		byModel[model] = item
	}
	prices := make([]any, 0, len(models))
	for _, model := range models {
		item := byModel[model]
		semantic := semantics[model]
		prices = append(prices, map[string]any{
			"fresh":                optMapValue(item, "freshInputRate"),
			"cache":                optMapValue(item, "cacheRate"),
			"cacheWrite":           optMapValue(item, "cacheWriteRate"),
			"output":               optMapValue(item, "outputRate"),
			"inputIncludesCache":   semantic.includesCache,
			"inputSemanticsSource": semantic.source,
		})
	}
	return prices
}

// buildLookups ports the lookups block.
func buildLookups(indexes *lookupIndexes) map[string]any {
	models := make([]any, 0, len(indexes.models))
	for _, model := range indexes.models {
		models = append(models, map[string]any{
			"id":       model,
			"short":    modelShort(model),
			"provider": modelProvider(model),
		})
	}
	timingReasons := make([]any, 0, len(sourcedsh.TimingReasons))
	for _, reason := range sourcedsh.TimingReasons {
		timingReasons = append(timingReasons, reason)
	}
	return map[string]any{
		"models":        models,
		"tools":         indexes.tools,
		"efforts":       indexes.efforts,
		"projects":      indexes.projects,
		"projectLabels": indexes.projectLabels,
		"entrypoints":   indexes.entrypoints,
		"timingReasons": timingReasons,
	}
}
