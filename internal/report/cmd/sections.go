// SPDX-License-Identifier: AGPL-3.0-only

package cmd

import (
	"sort"

	sourcecmd "github.com/jadmadi/agentmeter/internal/source/cmd"
)

// orderedGroups keeps first-appearance order for grouped rows, which Python
// gets for free from dict insertion order.
type orderedGroups struct {
	order  []string
	values map[string][]*sourcecmd.Row
}

func newOrderedGroups() *orderedGroups {
	return &orderedGroups{values: make(map[string][]*sourcecmd.Row)}
}

func (groups *orderedGroups) add(key string, row *sourcecmd.Row) {
	if _, exists := groups.values[key]; !exists {
		groups.order = append(groups.order, key)
	}
	groups.values[key] = append(groups.values[key], row)
}

// buildByModel ports the byModel loop plus the standardization pass.
func buildByModel(
	rows []*sourcecmd.Row,
	timed []*sourcecmd.Row,
	models []string,
	fits map[string]map[string]map[string]any,
	semantics map[string]inputSemantics,
	freshInput func(*sourcecmd.Row) int64,
) []any {
	byModel := make([]any, 0, len(models))
	for _, model := range models {
		rs := make([]*sourcecmd.Row, 0, len(rows))
		ts := make([]*sourcecmd.Row, 0, len(timed))
		modelTiming := make(map[string]int64, len(sourcecmd.TimingReasons))
		for _, reason := range sourcecmd.TimingReasons {
			modelTiming[reason] = 0
		}
		for _, row := range rows {
			if row.Model != model {
				continue
			}
			rs = append(rs, row)
			modelTiming[row.Timing]++
		}
		for _, row := range timed {
			if row.Model == model {
				ts = append(ts, row)
			}
		}
		summary := summarize(ts)
		var fit map[string]any
		if perModel, ok := fits[model]; ok {
			fit = perModel["text"]
			if fit == nil {
				fit = perModel["all"]
			}
		}
		if fit == nil {
			fit = map[string]any{}
		}
		semantic := semantics[model]
		var cacheRead, cacheWrite, fresh, rawInput, output int64
		var cost float64
		for _, row := range rs {
			cacheRead += row.CacheRead
			cacheWrite += row.CacheWrite
			fresh += freshInput(row)
			rawInput += row.Inp
			output += row.Out
			cost += row.Cost
		}
		totalIn := fresh + cacheRead + cacheWrite
		rates := make([]float64, 0, len(ts))
		for _, row := range ts {
			if row.TPS != nil {
				rates = append(rates, *row.TPS)
			}
		}
		item := map[string]any{
			"model":                model,
			"short":                modelShort(model),
			"provider":             modelProvider(model),
			"steps":                int64(len(rs)),
			"toolSteps":            countIf(rs, func(row *sourcecmd.Row) bool { return row.Tool }),
			"textSteps":            countIf(rs, func(row *sourcecmd.Row) bool { return !row.Tool }),
			"outputTokens":         output,
			"generatedTokens":      output,
			"visibleOutputTokens":  nil,
			"inputTokens":          totalIn,
			"inputRawTokens":       rawInput,
			"freshInputTokens":     fresh,
			"inputTotal":           totalIn,
			"cacheReadTokens":      cacheRead,
			"cacheWriteTokens":     cacheWrite,
			"cacheHitRate":         rateOrNil(cacheRead, totalIn),
			"inputIncludesCache":   semantic.includesCache,
			"inputSemanticsSource": semantic.source,
			"costUsd":              pyRound(cost, 6),
			"timedN":               summary["n"],
			"rated":                len(ts) >= 30,
			"lowSample":            len(ts) < 30,
			"medianCI":             bootstrapMedianCI(rates, 200, stableSeed(model), 0.05),
			"weighted":             summary["weighted"],
			"median":               summary["median"],
			"p90":                  summary["p90"],
			"pureRate":             optMapValue(fit, "pureRate"),
			"fixedSeconds":         optMapValue(fit, "fixedSeconds"),
			"r2":                   optMapValue(fit, "r2"),
			"fitUsable":            mapTruthy(fit, "usable"),
			"timing":               timingMap(modelTiming),
			"rejected":             int64(len(rs)) - mapInt(summary, "n"),
		}
		byModel = append(byModel, item)
	}

	// Direct standardization over turn type x output-size strata.
	sizeDefs := []sizeBucket{
		{"20-100", 20, 100},
		{"100-300", 100, 300},
		{"300-1k", 300, 1000},
		{"1k-5k", 1000, 5000},
		{"5k+", 5000, 1_000_000_000_000},
	}
	sizeBucketFor := func(value int64) (string, bool) {
		for _, definition := range sizeDefs {
			if definition.low <= value && value < definition.high {
				return definition.label, true
			}
		}
		return "", false
	}
	globalStrata := make(map[string]int64, 10)
	for _, row := range timed {
		if bucket, ok := sizeBucketFor(row.Out); ok {
			globalStrata[stratumKey(row.Tool, bucket)]++
		}
	}
	for _, entry := range byModel {
		item, _ := entry.(map[string]any)
		model, _ := item["model"].(string)
		strata := make(map[string][]float64, 10)
		strataOrder := make([]string, 0, 10)
		for _, row := range timed {
			if row.Model != model {
				continue
			}
			bucket, ok := sizeBucketFor(row.Out)
			if !ok {
				continue
			}
			key := stratumKey(row.Tool, bucket)
			if _, exists := strata[key]; !exists {
				strataOrder = append(strataOrder, key)
			}
			strata[key] = append(strata[key], *row.TPS)
		}
		medians := make(map[string]float64, len(strataOrder))
		medianOrder := make([]string, 0, len(strataOrder))
		standardizedN := int64(0)
		for _, key := range strataOrder {
			values := strata[key]
			if len(values) < 5 {
				continue
			}
			medians[key] = median(values)
			medianOrder = append(medianOrder, key)
			standardizedN += int64(len(values))
		}
		value, covered := standardize(medians, medianOrder, globalStrata)
		if covered == 0 {
			item["standardizedRate"] = nil
		} else {
			item["standardizedRate"] = r1(value)
		}
		item["standardizedCoverage"] = covered
		item["standardizedN"] = standardizedN
	}
	return byModel
}

func stratumKey(tool bool, bucket string) string {
	kind := "text"
	if tool {
		kind = "tool"
	}
	return kind + "|" + bucket
}

func timingMap(counts map[string]int64) map[string]any {
	result := make(map[string]any, len(sourcecmd.TimingReasons))
	for _, reason := range sourcecmd.TimingReasons {
		result[reason] = counts[reason]
	}
	return result
}

// buildTools ports the tool attribution loop and its sort.
func buildTools(rows []*sourcecmd.Row) ([]any, int64) {
	type toolStat struct {
		name           string
		calls          int64
		steps          map[string]struct{}
		generated      float64
		cost           float64
		tokensPerCall  []float64
		rates          []float64
		timedCalls     int64
		timedGenerated float64
		timedSeconds   float64
	}
	stats := make(map[string]*toolStat, 16)
	order := make([]string, 0, 16)
	var totalCalls int64
	for _, row := range rows {
		calls := row.ToolDetails
		if len(calls) == 0 {
			calls = []sourcecmd.ToolDetail{{Name: "(final answer)"}}
		}
		n := int64(len(calls))
		totalCalls += n
		timed := row.Timing == "plausible"
		for _, call := range calls {
			stat, exists := stats[call.Name]
			if !exists {
				stat = &toolStat{name: call.Name, steps: make(map[string]struct{})}
				stats[call.Name] = stat
				order = append(order, call.Name)
			}
			stat.calls++
			stat.steps[row.ID] = struct{}{}
			stat.generated += float64(row.Out) / float64(n)
			stat.cost += row.Cost / float64(n)
			stat.tokensPerCall = append(stat.tokensPerCall, float64(row.Out)/float64(n))
			if timed {
				stat.timedCalls++
				stat.timedGenerated += float64(row.Out) / float64(n)
				stat.timedSeconds += *row.DT / float64(n)
				stat.rates = append(stat.rates, *row.TPS)
			}
		}
	}
	tools := make([]any, 0, len(order))
	for _, name := range order {
		stat := stats[name]
		item := map[string]any{
			"tool":                name,
			"calls":               stat.calls,
			"stepsWithTool":       int64(len(stat.steps)),
			"shareCalls":          nil,
			"generatedTokens":     pyRound(stat.generated, 1),
			"visibleOutputTokens": nil,
			"medianTokensPerCall": nil,
			"timedCalls":          stat.timedCalls,
			"weighted":            nil,
			"medianTps":           nil,
			"errors":              int64(0),
			"errRate":             nil,
			"costUsd":             pyRound(stat.cost, 6),
			"costPerCall":         nil,
			"timeShare":           nil,
			"attribution":         "equal-share",
			"runtime":             "unavailable",
		}
		if totalCalls != 0 {
			item["shareCalls"] = float64(stat.calls) / float64(totalCalls)
		}
		if len(stat.tokensPerCall) > 0 {
			item["medianTokensPerCall"] = r1(median(stat.tokensPerCall))
		}
		if stat.timedSeconds != 0 {
			item["weighted"] = pyRound(stat.timedGenerated/stat.timedSeconds, 1)
		}
		if len(stat.rates) > 0 {
			item["medianTps"] = r1(median(stat.rates))
		}
		if stat.calls != 0 {
			item["costPerCall"] = pyRound(stat.cost/float64(stat.calls), 6)
		}
		tools = append(tools, item)
	}
	sort.SliceStable(tools, func(i, j int) bool {
		return mapInt(tools[i].(map[string]any), "calls") > mapInt(tools[j].(map[string]any), "calls")
	})
	return tools, totalCalls
}

// buildThinking ports the thinking slice pair.
func buildThinking(rows []*sourcecmd.Row) map[string]any {
	return map[string]any{
		"with":    behaviorSlice(filterRows(rows, func(row *sourcecmd.Row) bool { return row.Thinking }), len(rows)),
		"without": behaviorSlice(filterRows(rows, func(row *sourcecmd.Row) bool { return !row.Thinking }), len(rows)),
	}
}

// buildEfforts ports the effort groups.
func buildEfforts(rows []*sourcecmd.Row) []any {
	groups := newOrderedGroups()
	for _, row := range rows {
		groups.add(row.Effort, row)
	}
	efforts := make([]any, 0, len(groups.order))
	for _, name := range groups.order {
		item := behaviorSlice(groups.values[name], len(rows))
		item["effort"] = name
		efforts = append(efforts, item)
	}
	sort.SliceStable(efforts, func(i, j int) bool {
		return mapInt(efforts[i].(map[string]any), "steps") > mapInt(efforts[j].(map[string]any), "steps")
	})
	return efforts
}

// buildEntrypoints ports the entrypoint groups.
func buildEntrypoints(rows []*sourcecmd.Row) []any {
	groups := newOrderedGroups()
	for _, row := range rows {
		groups.add(row.Entrypoint, row)
	}
	entrypoints := make([]any, 0, len(groups.order))
	for _, name := range groups.order {
		group := groups.values[name]
		item := behaviorSlice(group, len(rows))
		item["entrypoint"] = name
		sessions := make(map[string]struct{}, 8)
		for _, row := range group {
			sessions[row.SessionID] = struct{}{}
		}
		item["sessions"] = int64(len(sessions))
		entrypoints = append(entrypoints, item)
	}
	sort.SliceStable(entrypoints, func(i, j int) bool {
		return mapInt(entrypoints[i].(map[string]any), "steps") > mapInt(entrypoints[j].(map[string]any), "steps")
	})
	return entrypoints
}

// buildProjects ports the project groups.
func buildProjects(rows []*sourcecmd.Row, labels map[string]string, inputTotal func(*sourcecmd.Row) int64) []any {
	groups := newOrderedGroups()
	for _, row := range rows {
		groups.add(row.ProjectPath, row)
	}
	projects := make([]any, 0, len(groups.order))
	for _, path := range groups.order {
		group := groups.values[path]
		var cacheRead, totalIn, output int64
		costAcc := sumAcc{}
		sessions := make(map[string]struct{}, 8)
		for _, row := range group {
			cacheRead += row.CacheRead
			totalIn += inputTotal(row)
			output += row.Out
			costAcc.add(row.Cost)
			sessions[row.SessionID] = struct{}{}
		}
		cost := costAcc.value()
		name := path
		if label, ok := labels[path]; ok {
			name = label
		}
		projects = append(projects, map[string]any{
			"name":            name,
			"path":            path,
			"steps":           int64(len(group)),
			"sessions":        int64(len(sessions)),
			"generatedTokens": output,
			"outputTokens":    output,
			"inputTotal":      totalIn,
			"cacheReadTokens": cacheRead,
			"cacheHitRate":    rateOrNil(cacheRead, totalIn),
			"costUsd":         pyRound(cost, 6),
		})
	}
	sort.SliceStable(projects, func(i, j int) bool {
		return mapInt(projects[i].(map[string]any), "generatedTokens") > mapInt(projects[j].(map[string]any), "generatedTokens")
	})
	return projects
}

// buildSessionHealth ports hooks, tool windows and the event counters.
func buildSessionHealth(source *sourcecmd.Source, totalCalls int64, timingCounts map[string]int64, ledgerCost float64) map[string]any {
	type hookGroup struct {
		name  string
		event string
		items []sourcecmd.Hook
	}
	groups := make(map[string]*hookGroup, 8)
	order := make([]string, 0, 8)
	for index := range source.Events.Hooks {
		hook := source.Events.Hooks[index]
		name := "(unnamed)"
		if hook.HookName != nil && *hook.HookName != "" {
			name = *hook.HookName
		}
		eventName := "(event)"
		if hook.EventName != nil && *hook.EventName != "" {
			eventName = *hook.EventName
		}
		key := name + "\x00" + eventName
		group, exists := groups[key]
		if !exists {
			group = &hookGroup{name: name, event: eventName}
			groups[key] = group
			order = append(order, key)
		}
		group.items = append(group.items, hook)
	}
	hooks := make([]any, 0, len(order))
	for _, key := range order {
		group := groups[key]
		durations := make([]float64, 0, len(group.items))
		var failures, unknownExit, blocked int64
		for _, hook := range group.items {
			if hook.Duration != nil {
				durations = append(durations, *hook.Duration)
			}
			if hook.ExitCode == nil {
				unknownExit++
			} else if *hook.ExitCode != 0 {
				failures++
			}
			if hook.Blocked {
				blocked++
			}
		}
		sort.Float64s(durations)
		var medianMs, p90Ms any
		if len(durations) > 0 {
			medianMs = r1(median(durations))
			p90Ms = r1(percentile(durations, 90))
		}
		hooks = append(hooks, map[string]any{
			"hookName":    group.name,
			"eventName":   group.event,
			"count":       int64(len(group.items)),
			"failures":    failures,
			"unknownExit": unknownExit,
			"blocked":     blocked,
			"medianMs":    medianMs,
			"p90Ms":       p90Ms,
		})
	}
	sort.SliceStable(hooks, func(i, j int) bool {
		return mapInt(hooks[i].(map[string]any), "count") > mapInt(hooks[j].(map[string]any), "count")
	})

	windows := make([]float64, 0, len(source.Events.ToolWindows))
	for _, window := range source.Events.ToolWindows {
		if window.Seconds != 0 {
			windows = append(windows, window.Seconds)
		}
	}
	sort.Float64s(windows)

	var toolWindowMedian, toolWindowP90 any
	if len(windows) > 0 {
		toolWindowMedian = r2(median(windows))
		toolWindowP90 = r2(percentile(windows, 90))
	}
	return map[string]any{
		"hooks":      hooks,
		"hookEvents": int64(len(source.Events.Hooks)),
		"toolWindows": map[string]any{
			"count":         int64(len(windows)),
			"medianSeconds": toolWindowMedian,
			"p90Seconds":    toolWindowP90,
		},
		"compactions": map[string]any{
			"count":        source.Events.Compactions,
			"tokensBefore": source.Events.CompactionTokens,
		},
		"modelChanges":     map[string]any{"count": source.Events.ModelChanges},
		"fileSnapshots":    map[string]any{"count": source.Events.FileSnapshots},
		"branches":         sortedKeys(source.Events.Branches),
		"timing":           timingMap(timingCounts),
		"toolCalls":        totalCalls,
		"skippedSteps":     int64(len(source.Ledger)),
		"skippedCostUsd":   pyRound(ledgerCost, 6),
		"emptySteps":       source.Counts.NoOutput,
		"noTimestampSteps": source.Counts.NoTimestamp,
		"duplicateSteps":   source.Counts.Duplicates,
		"duplicateCostUsd": pyRound(source.DuplicateCost, 6),
		"duplicateTokens":  source.DuplicateTokens,
	}
}

func buildCoverage(source *sourcecmd.Source, rows []*sourcecmd.Row, toolWindows int64) map[string]any {
	var cacheRead, cacheWrite, thinking, titles, entrypoints, timing bool
	for _, row := range rows {
		if row.CacheRead != 0 {
			cacheRead = true
		}
		if row.CacheWrite != 0 {
			cacheWrite = true
		}
		if row.Thinking {
			thinking = true
		}
		if row.SessionTitle != nil {
			titles = true
		}
		if row.EntrypointRaw != nil {
			entrypoints = true
		}
		if row.DT != nil {
			timing = true
		}
	}
	return map[string]any{
		"cacheRead":       cacheRead,
		"cacheWrite":      cacheWrite,
		"thinking":        thinking,
		"hooks":           len(source.Events.Hooks) > 0,
		"toolWindows":     toolWindows > 0,
		"compactions":     source.Events.Compactions > 0,
		"modelChanges":    source.Events.ModelChanges > 0,
		"fileSnapshots":   source.Events.FileSnapshots > 0,
		"branches":        len(source.Events.Branches) > 0,
		"titles":          titles,
		"entrypoints":     entrypoints,
		"timing":          timing,
		"timingBreakdown": true,
		"toolCalls":       true,
		"skipLedger":      len(source.Ledger) > 0,
		"projectPaths":    true,
		"inputSemantics":  true,
	}
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func modelShort(model string) string {
	if index := lastIndexByte(model, '/'); index >= 0 {
		return model[index+1:]
	}
	return model
}

func modelProvider(model string) string {
	if index := lastIndexByte(model, '/'); index >= 0 {
		return model[:index]
	}
	return "unknown"
}

func countIf(rows []*sourcecmd.Row, predicate func(*sourcecmd.Row) bool) int64 {
	var total int64
	for _, row := range rows {
		if predicate(row) {
			total++
		}
	}
	return total
}

func filterRows(rows []*sourcecmd.Row, predicate func(*sourcecmd.Row) bool) []*sourcecmd.Row {
	result := make([]*sourcecmd.Row, 0, len(rows))
	for _, row := range rows {
		if predicate(row) {
			result = append(result, row)
		}
	}
	return result
}

type inputSemantics struct {
	includesCache bool
	source        string
}
