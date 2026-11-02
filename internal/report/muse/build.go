// SPDX-License-Identifier: AGPL-3.0-only

// Package muse builds the Muse schema v3 report.
//
// The report shares the Command Code section layout, rounding and sentinels,
// adapted to Muse semantics: model_completed events carry usage with a
// reasoning split and measured durations, input_tokens includes cache, tools
// link by response id, and no cost data exists (spend views read zero).
// There is no Python oracle yet; the Go builder is validated by
// contract.ValidateMuse and the fixture tests.
package muse

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jadmadi/agentmeter/internal/contract"
	sourcemuse "github.com/jadmadi/agentmeter/internal/source/muse"
)

// SchemaVersion is the report schema emitted by both builders.
const SchemaVersion = 3

// Options carries the builder flags that affect the report content.
type Options struct {
	// Root is the raw --root value; it is only used for meta.rootLabel.
	Root string
	// ModelFilters are the parsed --models terms.
	ModelFilters  []string
	Since         string
	Until         string
	IncludeTitles bool
	FullPaths     bool
	// Now is meta.generatedAt; zero means time.Now().
	Now time.Time
}

// Build assembles the full report and validates the shared invariants. It
// returns an error instead of writing a report that violates the contract,
// matching the Python builder's fail-closed behavior.
func Build(source *sourcemuse.Source, options Options) (map[string]any, error) {
	rows := source.Rows
	files := source.Files
	ledger := source.Ledger
	counts := source.Counts
	timingCounts := source.Timing
	events := source.Events
	duplicates := counts.Duplicates

	projectLabels := buildProjectLabels(rows, options.FullPaths)
	for _, row := range rows {
		label := projectLabels[row.ProjectPath]
		row.ProjectLabel = &label
	}

	timed := make([]*sourcemuse.Row, 0, len(rows))
	text := make([]*sourcemuse.Row, 0, len(rows))
	for _, row := range rows {
		if row.Timing != "plausible" {
			continue
		}
		timed = append(timed, row)
		if !row.Tool {
			text = append(text, row)
		}
	}

	models := make([]string, 0, 16)
	for _, row := range rows {
		models = append(models, row.Model)
	}
	models = sortedUnique(models)

	fits := make(map[string]map[string]map[string]any, len(models))
	globalFits := map[string]map[string]any{
		"text": fitRate(text),
		"tool": fitRate(filterRows(timed, func(row *sourcemuse.Row) bool { return row.Tool })),
	}
	for _, model := range models {
		modelTimed := filterRows(timed, func(row *sourcemuse.Row) bool { return row.Model == model })
		perModel := map[string]map[string]any{
			"all":  fitRate(modelTimed),
			"text": fitRate(filterRows(modelTimed, func(row *sourcemuse.Row) bool { return !row.Tool })),
			"tool": fitRate(filterRows(modelTimed, func(row *sourcemuse.Row) bool { return row.Tool })),
		}
		modelFits := make(map[string]map[string]any, 3)
		for _, kind := range []string{"all", "text", "tool"} {
			if perModel[kind] != nil {
				modelFits[kind] = perModel[kind]
			}
		}
		fits[model] = modelFits
	}
	for _, row := range timed {
		row.Ratio = expectedRatio(row, fits, globalFits)
	}
	ratioValues := make([]float64, 0, len(timed))
	for _, row := range timed {
		if row.Ratio != nil {
			ratioValues = append(ratioValues, *row.Ratio)
		}
	}
	var paceBase *float64
	if len(ratioValues) > 0 {
		value := median(ratioValues)
		paceBase = &value
	}

	// Price fits and canonical input semantics.
	priceFits := make(map[string]map[string]any, len(models))
	for _, model := range models {
		fit := priceFit(filterRows(rows, func(row *sourcemuse.Row) bool { return row.Model == model }))
		if fit != nil {
			priceFits[model] = fit
		}
	}
	semantics := make(map[string]inputSemantics, len(models))
	for _, model := range models {
		semantics[model] = inferSemantics()
	}
	freshInput := func(row *sourcemuse.Row) int64 {
		return maxInt64(0, row.Inp-row.CacheRead-row.CacheWrite)
	}
	inputTotal := func(row *sourcemuse.Row) int64 {
		return freshInput(row) + row.CacheRead + row.CacheWrite
	}

	byModel := buildByModel(rows, timed, models, fits, semantics, freshInput)

	costByModel := make([]any, 0, len(models))
	for _, model := range models {
		priceFit := priceFits[model]
		semantic := semantics[model]
		costByModel = append(costByModel, costSummary(
			model,
			filterRows(rows, func(row *sourcemuse.Row) bool { return row.Model == model }),
			priceFit,
			semantic.includesCache,
			semantic.source,
		))
	}

	// Totals.
	var totalInput, totalRawInput, totalFreshInput, totalCacheRead, totalCacheWrite, totalOutput, totalReasoning int64
	costAcc := sumAcc{}
	for _, entry := range costByModel {
		item := entry.(map[string]any)
		totalInput += mapInt(item, "inputTotal")
		totalRawInput += mapInt(item, "inputRawTokens")
		totalFreshInput += mapInt(item, "freshInputTokens")
	}
	for _, row := range rows {
		totalCacheRead += row.CacheRead
		totalCacheWrite += row.CacheWrite
		totalOutput += row.Out
		totalReasoning += row.Reas
		costAcc.add(row.Cost)
	}
	totalsCost := costAcc.value()
	errorAcc := sumAcc{}
	undatedAcc := sumAcc{}
	ledgerAcc := sumAcc{}
	for _, entry := range ledger {
		if entry.Reason == sourcemuse.ReasonDuplicate {
			continue
		}
		errorAcc.add(entry.CostUSD)
		ledgerAcc.add(entry.CostUSD)
		if entry.TS == nil {
			undatedAcc.add(entry.CostUSD)
		}
	}
	errorCost := errorAcc.value()
	undatedErrorCost := undatedAcc.value()
	ledgerCost := ledgerAcc.value()
	totalSpend := totalsCost + errorCost
	var fittedCacheRead int64
	fittedModels := int64(0)
	for _, entry := range costByModel {
		item := entry.(map[string]any)
		rate, ok := numericValue(item["freshInputRate"])
		if ok && rate > 0 {
			fittedCacheRead += mapInt(item, "cacheReadTokens")
			fittedModels++
		}
	}
	cache := map[string]any{
		"inputTokens":      totalInput,
		"inputRawTokens":   totalRawInput,
		"freshInputTokens": totalFreshInput,
		"cacheReadTokens":  totalCacheRead,
		"cacheWriteTokens": totalCacheWrite,
		"generatedTokens":  totalOutput,
		"hitRate":          rateOrNil(totalCacheRead, totalInput),
		"writeShare":       rateOrNil(totalCacheWrite, totalInput),
		"savings":          pyRound(sumOptional(costByModel, "cacheSavings"), 6),
		"writeCost":        pyRound(sumOptional(costByModel, "cacheWriteCost"), 6),
		"fittedModels":     fittedModels,
		"unfittedModels":   int64(len(models)) - fittedModels,
		"savingsCoverage":  nil,
	}
	if totalCacheRead != 0 {
		cache["savingsCoverage"] = pyRound(float64(fittedCacheRead)/float64(totalCacheRead), 4)
	}
	cost := map[string]any{
		"recorded":            pyRound(totalsCost, 6),
		"errorCostUsd":        pyRound(errorCost, 6),
		"undatedErrorCostUsd": pyRound(undatedErrorCost, 6),
		"totalSpendUsd":       pyRound(totalSpend, 6),
		"tokenCost":           pyRound(sumRequired(costByModel, "tokenCost"), 6),
		"freshCost":           pyRound(sumOptional(costByModel, "freshCost"), 6),
		"cacheReadCost":       pyRound(sumOptional(costByModel, "cacheReadCost"), 6),
		"cacheWriteCost":      pyRound(sumOptional(costByModel, "cacheWriteCost"), 6),
		"outputCost":          pyRound(sumOptional(costByModel, "outputCost"), 6),
		"perStep":             nil,
		"perMillionGenerated": nil,
		"perMillionOutput":    nil,
		"byModel":             costByModel,
	}
	if len(rows) > 0 {
		cost["perStep"] = pyRound(totalsCost/float64(len(rows)), 4)
	}
	if totalOutput != 0 {
		perMillion := pyRound(totalsCost/float64(totalOutput)*1e6, 2)
		cost["perMillionGenerated"] = perMillion
		cost["perMillionOutput"] = perMillion
	}

	tools, totalCalls := buildTools(rows)
	thinking := buildThinking(rows)
	efforts := buildEfforts(rows)
	entrypoints := buildEntrypoints(rows)
	projects := buildProjects(rows, projectLabels, inputTotal)
	sessionHealth := buildSessionHealth(source, totalCalls, timingCounts, ledgerCost)
	toolWindowCount := int64(0)
	if windows, ok := sessionHealth["toolWindows"].(map[string]any); ok {
		toolWindowCount = mapInt(windows, "count")
	}
	coverage := buildCoverage(source, rows, toolWindowCount)

	indexes := buildLookupIndexes(rows, models, projectLabels)
	steps := buildSteps(rows, indexes)

	ledgerBySession := make(map[string][]*sourcemuse.LedgerEntry, 64)
	for _, entry := range ledger {
		ledgerBySession[entry.SessionID] = append(ledgerBySession[entry.SessionID], entry)
	}
	sessions := buildSessions(rows, indexes, ledgerBySession, source.DuplicateSessions, freshInput, inputTotal, source.Outcomes)

	regressions := buildRegression(models, fits)
	fitsOut := buildFits(models, fits)
	prices := buildPrices(models, costByModel, semantics)

	edges := []float64{0, 10, 20, 30, 40, 50, 66, 80, 100, 120, 150, 200, 401}
	labels := []any{
		"0-10", "10-20", "20-30", "30-40", "40-50", "50-66",
		"66-80", "80-100", "100-120", "120-150", "150-200", "200+",
	}
	histogramSection := map[string]any{
		"labels": labels,
		"all":    intSlice(histogram(timed, edges)),
		"text":   intSlice(histogram(text, edges)),
	}
	sizeBuckets := bucketize(timed, []sizeBucket{
		{"20-100", 20, 100},
		{"100-300", 100, 300},
		{"300-600", 300, 600},
		{"600-1k", 600, 1000},
		{"1k-2k", 1000, 2000},
		{"2k-5k", 2000, 5000},
		{"5k+", 5000, 1_000_000_000},
	})

	ledgerIndex := buildLedgerIndexes(ledger)
	daily := buildDaily(rows, ledgerIndex, inputTotal, paceBase)
	anomalies := buildAnomalies(daily)
	dailyByModel := buildDailyByModel(rows, models, ledgerIndex, inputTotal, paceBase)
	compaction := compactionImpact(rows, events.CompactionEvents, inputTotal)
	hourly := buildHourly(rows, ledgerIndex, inputTotal, paceBase)
	contextSection := buildContextBuckets(timed, inputTotal, paceBase)
	contextText := buildContextBuckets(text, inputTotal, paceBase)

	extremesResult := buildExtremes(rows, timed, inputTotal)
	timedAll := summarize(timed)
	timedAll["maxIncluded"] = timedAll["max"]
	timedAll["maxObserved"] = extremesResult.maxObserved
	timedAll["maxRejected"] = extremesResult.maxRejected
	timedAll["maxRejectedReason"] = extremesResult.maxRejectedReason
	timedAll["minIncluded"] = extremesResult.minIncluded
	timedAll["maxDurationIncluded"] = extremesResult.maxDurationIncluded
	timedAll["maxDurationObserved"] = extremesResult.maxDurationObserved
	timedAll["maxDurationAny"] = extremesResult.maxDurationAny
	timedAll["upperCensored"] = timingCounts["fastRate"]
	timedAll["lowerCensored"] = timingCounts["slowRate"]
	timedAll["durationCensoredHigh"] = timingCounts["tooLong"]
	timedAll["durationCensoredLow"] = timingCounts["tooShortDuration"]

	var earliest, latest *time.Time
	for _, row := range rows {
		stamp := row.TS
		if earliest == nil || stamp.Before(*earliest) {
			earliest = &stamp
		}
		if latest == nil || stamp.After(*latest) {
			latest = &stamp
		}
	}
	var dateFrom, dateTo any
	if earliest != nil {
		dateFrom = sourcemuse.LocalDate(*earliest)
		dateTo = sourcemuse.LocalDate(*latest)
	}

	var maxTS any
	if source.MaxTS != nil {
		maxTS = sourcemuse.UnixFloat(*source.MaxTS)
	}
	metaTiming := map[string]any{
		"files":            counts.Files,
		"scanned":          counts.Scanned,
		"considered":       counts.Considered,
		"rows":             counts.Rows,
		"plausible":        timingCounts["plausible"],
		"noTiming":         timingCounts["noTiming"],
		"tooShort":         timingCounts["tooShort"],
		"tooShortDuration": timingCounts["tooShortDuration"],
		"tooLong":          timingCounts["tooLong"],
		"slowRate":         timingCounts["slowRate"],
		"fastRate":         timingCounts["fastRate"],
		"duplicates":       counts.Duplicates,
		"errors":           counts.Errors,
		"empty":            counts.Empty,
		"incomplete":       counts.Incomplete,
		"noTimestamp":      counts.NoTimestamp,
		"filteredModel":    counts.FilteredModel,
		"filteredDate":     counts.FilteredDate,
		"thresholds": map[string]any{
			"minSeconds": 0.5,
			"maxSeconds": 180.0,
			"minTokens":  int64(20),
			"minRate":    3.0,
			"maxRate":    400.0,
		},
	}
	countsMap := map[string]any{
		"files":         counts.Files,
		"scanned":       counts.Scanned,
		"considered":    counts.Considered,
		"rows":          counts.Rows,
		"duplicates":    counts.Duplicates,
		"errors":        counts.Errors,
		"empty":         counts.Empty,
		"incomplete":    counts.Incomplete,
		"noTimestamp":   counts.NoTimestamp,
		"filteredModel": counts.FilteredModel,
		"filteredDate":  counts.FilteredDate,
		"subagents":     counts.Subagents,
		"omissions":     counts.Omissions,
	}

	generatedAt := options.Now
	if generatedAt.IsZero() {
		generatedAt = time.Now()
	}
	var paceBaseOut, paceBasePct any
	if paceBase != nil && *paceBase != 0 {
		paceBaseOut = pyRound(*paceBase, 6)
		paceBasePct = pyRound(*paceBase*100, 1)
	}
	modelFilters := make([]string, 0, len(options.ModelFilters))
	modelFilters = append(modelFilters, options.ModelFilters...)

	data := map[string]any{
		"schemaVersion": SchemaVersion,
		"meta": map[string]any{
			"generatedAt":       generatedAt.UTC().Format("2006-01-02T15:04:05") + "+00:00",
			"rootLabel":         rootLabel(options.Root),
			"modelFilters":      modelFilters,
			"models":            int64(len(models)),
			"filesScanned":      int64(len(files)),
			"sessions":          int64(len(source.Sessions)),
			"duplicatesSkipped": duplicates,
			"skippedSteps":      int64(len(ledger)),
			"emptySteps":        counts.Empty,
			"includeTitles":     options.IncludeTitles,
			"fullPaths":         options.FullPaths,
			"since":             optionalString(options.Since),
			"until":             optionalString(options.Until),
			"timezone":          sourcemuse.LocalTZLabel(),
			"paceBase":          paceBaseOut,
			"paceBasePct":       paceBasePct,
			"timing":            metaTiming,
			"counts":            countsMap,
			"sourceWatermark": map[string]any{
				"maxTs":   maxTS,
				"files":   int64(len(files)),
				"scanned": counts.Scanned,
				"rows":    int64(len(rows)),
			},
			"dateFrom": dateFrom,
			"dateTo":   dateTo,
		},
		"totals": map[string]any{
			"steps":               int64(len(rows)),
			"toolSteps":           countIf(rows, func(row *sourcemuse.Row) bool { return row.Tool }),
			"textSteps":           countIf(rows, func(row *sourcemuse.Row) bool { return !row.Tool }),
			"toolCalls":           totalCalls,
			"outputTokens":        totalOutput,
			"generatedTokens":     totalOutput,
			"visibleOutputTokens": totalOutput - totalReasoning,
			"reasoningTokens":     totalReasoning,
			"inputTokens":         totalInput,
			"inputRawTokens":      totalRawInput,
			"freshInputTokens":    totalFreshInput,
			"inputTotal":          totalInput,
			"cacheReadTokens":     totalCacheRead,
			"cacheWriteTokens":    totalCacheWrite,
			"costUsd":             pyRound(totalsCost, 6),
			"errorCostUsd":        pyRound(errorCost, 6),
			"totalSpendUsd":       pyRound(totalSpend, 6),
			"models":              int64(len(models)),
		},
		"timed": map[string]any{
			"all":  timedAll,
			"text": summarize(text),
			"tool": summarize(filterRows(timed, func(row *sourcemuse.Row) bool { return row.Tool })),
		},
		"byModel":          byModel,
		"cache":            cache,
		"cost":             cost,
		"sessionHealth":    sessionHealth,
		"coverage":         coverage,
		"tools":            tools,
		"thinking":         thinking,
		"efforts":          efforts,
		"entrypoints":      entrypoints,
		"projects":         projects,
		"projectsTotal":    int64(len(projects)),
		"lookups":          buildLookups(indexes),
		"fits":             fitsOut,
		"prices":           prices,
		"steps":            steps,
		"sessions":         sessions,
		"regression":       regressions,
		"histogram":        histogramSection,
		"sizeBuckets":      sizeBuckets,
		"daily":            daily,
		"dailyByModel":     dailyByModel,
		"hourly":           hourly,
		"context":          contextSection,
		"contextText":      contextText,
		"peaks":            extremesResult.peaks,
		"slowest":          extremesResult.slowest,
		"lowestRate":       extremesResult.lowestRate,
		"rejectedExtremes": extremesResult.rejectedExtremes,
		"anomalies":        anomalies,
		"skipLedger":       buildSkipLedger(ledger),
		"fork": map[string]any{
			"duplicateSteps":         counts.Duplicates,
			"duplicateTokens":        source.DuplicateTokens,
			"duplicateCostUsd":       pyRound(source.DuplicateCost, 6),
			"sessionsWithDuplicates": int64(len(source.DuplicateSessions)),
		},
		"compactionImpact":  compaction,
		"compactionEvents":  buildCompactionEvents(events.CompactionEvents),
		"modelChangeEvents": []any{},
	}

	if errors := contract.ValidateMuse(data); len(errors) > 0 {
		return nil, fmt.Errorf("telemetry invariant failure:\n- %s", strings.Join(errors, "\n- "))
	}
	return data, nil
}

// inferSemantics is fixed: Muse input_tokens tracks the full prompt estimate,
// so it includes cache; fresh input is input minus cache read and write.
func inferSemantics() inputSemantics {
	return inputSemantics{includesCache: true, source: "normalized"}
}

func buildProjectLabels(rows []*sourcemuse.Row, fullPaths bool) map[string]string {
	paths := make([]string, 0, 16)
	for _, row := range rows {
		paths = append(paths, row.ProjectPath)
	}
	return projectLabelsForPaths(paths, fullPaths)
}

// projectLabelsForPaths ports the label disambiguation loop: basenames when
// unique, "base · parent" when ambiguous, and the full path when the label is
// already taken.
func projectLabelsForPaths(paths []string, fullPaths bool) map[string]string {
	paths = sortedUnique(paths)
	baseCounts := make(map[string]int, len(paths))
	for _, path := range paths {
		baseCounts[projectBase(path)]++
	}
	labels := make(map[string]string, len(paths))
	used := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		base := projectBase(path)
		var label string
		switch {
		case fullPaths:
			label = path
		case baseCounts[base] == 1:
			label = base
		default:
			parent := projectParent(path)
			if parent != "" {
				label = base + " · " + parent
			} else {
				label = base
			}
		}
		if _, exists := used[label]; exists {
			label = path
		}
		used[label] = struct{}{}
		labels[path] = label
	}
	return labels
}

func projectBase(path string) string {
	trimmed := strings.TrimRight(path, "/")
	if trimmed == "" {
		return path
	}
	if index := strings.LastIndexByte(trimmed, '/'); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

func projectParent(path string) string {
	trimmed := strings.TrimRight(path, "/")
	index := strings.LastIndexByte(trimmed, '/')
	if index < 0 {
		return ""
	}
	return projectBase(trimmed[:index])
}

func sortedUnique(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	sort.Strings(values)
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func rootLabel(root string) string {
	home := expandUserHome()
	if home == "" {
		return root
	}
	return strings.ReplaceAll(root, home, "~")
}

func expandUserHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func optionalString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func intSlice(values []int64) []any {
	result := make([]any, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

// sumOptional is `sum(item[key] or 0 ...)` with CPython's compensated float
// sum; nil/missing values contribute nothing.
func sumOptional(rows []any, key string) float64 {
	acc := sumAcc{}
	for _, entry := range rows {
		item := entry.(map[string]any)
		if value, ok := numericValue(item[key]); ok {
			acc.add(value)
		}
	}
	return acc.value()
}

// sumRequired is `sum(item[key] ...)` where every item has a float value.
func sumRequired(rows []any, key string) float64 {
	acc := sumAcc{}
	for _, entry := range rows {
		item := entry.(map[string]any)
		acc.add(mapFloat(item, key))
	}
	return acc.value()
}

func buildSkipLedger(ledger []*sourcemuse.LedgerEntry) []any {
	result := make([]any, 0, len(ledger))
	for _, entry := range ledger {
		var timestamp any
		if entry.TS != nil {
			timestamp = sourcemuse.UnixSeconds(*entry.TS)
		}
		result = append(result, map[string]any{
			"id":               entry.ID,
			"reason":           entry.Reason,
			"sessionId":        entry.SessionID,
			"projectPath":      entry.ProjectPath,
			"model":            entry.Model,
			"ts":               timestamp,
			"outputTokens":     entry.OutputTokens,
			"inputTokens":      entry.InputTokens,
			"cacheReadTokens":  entry.CacheReadTokens,
			"cacheWriteTokens": entry.CacheWriteTokens,
			"costUsd":          pyRound(entry.CostUSD, 6),
		})
	}
	return result
}

func buildCompactionEvents(events []sourcemuse.CompactionEvent) []any {
	result := make([]any, 0, len(events))
	for _, event := range events {
		var timestamp any
		if event.TS != nil {
			timestamp = sourcemuse.UnixSeconds(*event.TS)
		}
		var sessionID any
		if event.SessionID != nil {
			sessionID = *event.SessionID
		}
		result = append(result, map[string]any{
			"ts":           timestamp,
			"sessionId":    sessionID,
			"tokensBefore": event.TokensBefore,
		})
	}
	return result
}
