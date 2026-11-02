// SPDX-License-Identifier: AGPL-3.0-only

// Package compare holds the cross-tool comparison model: schema v3 report
// parsing, canonical metric normalization, model-family matching, output-size
// strata and differential statistics shared by the builder and the CLI.
//
// Nothing here reads a tool's raw store; a Report is always derived from a
// finished schema v3 report, whether that report was loaded from
// telemetry-data.json or built live from the tool's own source.
package compare

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jadmadi/agentmeter/internal/pyjson"
)

// SchemaVersion is the input report schema this package consumes.
const SchemaVersion = 3

// ComparisonVersion is the emitted comparison document schema.
const ComparisonVersion = 1

// Tool identifiers used everywhere in the comparison document.
const (
	ToolOC   = "oc"
	ToolCMD  = "cmd"
	ToolMuse = "muse"
)

// CanonicalTools is the tool order used to arrange a pair: the earlier tool
// is always side A, so ratios read A / B deterministically.
var CanonicalTools = []string{ToolOC, ToolCMD, ToolMuse}

// DisplayName is the human-readable tool label for terminals and notes.
func DisplayName(tool string) string {
	switch tool {
	case ToolOC:
		return "OpenCode"
	case ToolCMD:
		return "Command Code"
	case ToolMuse:
		return "Muse"
	default:
		return tool
	}
}

// OrderPair arranges two tool ids in canonical order (side A first).
func OrderPair(first, second string) (string, string) {
	rank := map[string]int{ToolOC: 0, ToolCMD: 1, ToolMuse: 2}
	if rank[first] > rank[second] {
		return second, first
	}
	return first, second
}

// Input kinds.
const (
	KindReport = "report"
	KindLive   = "live"
)

// Strata buckets used for cross-tool generation-speed comparison.
var StrataBuckets = []struct {
	Label string
	Low   int64
	High  int64
}{
	{"20-100", 20, 100},
	{"100-300", 100, 300},
	{"300-1k", 300, 1000},
	{"1k-5k", 1000, 5000},
	{"5k+", 5000, 1_000_000_000_000},
}

// Input describes a resolved telemetry source.
type Input struct {
	Tool  string
	Kind  string
	Path  string
	Label string
}

// Totals is the canonical per-tool total block.
type Totals struct {
	Steps           int64
	ToolSteps       int64
	ToolCalls       int64
	GeneratedTokens int64
	InputTotal      int64
	CacheRead       int64
	CacheWrite      int64
	CostUsd         float64
	TotalSpendUsd   float64
	Models          int64
	Sessions        int64
}

// TimedSummary is the canonical timed throughput block.
type TimedSummary struct {
	N        int64
	Tokens   int64
	Seconds  float64
	Weighted *float64
	Median   *float64
	P10      *float64
	P25      *float64
	P75      *float64
	P90      *float64
	P99      *float64
	Max      *float64
}

// CacheSummary is the canonical cache block.
type CacheSummary struct {
	InputTotal  int64
	CacheRead   int64
	CacheWrite  int64
	HitRate     *float64
	WriteShare  *float64
	SavingsUsd  float64
	FittedModel int64
}

// CostSummary is the canonical cost block.
type CostSummary struct {
	Recorded            float64
	ErrorCostUsd        float64
	UndatedErrorCostUsd float64
	TotalSpendUsd       float64
	PerStep             *float64
	PerMillionGenerated *float64
}

// ModelStat is one byModel entry, reduced to the fields the comparison needs.
type ModelStat struct {
	Model              string
	Short              string
	Provider           string
	Family             string
	Steps              int64
	GeneratedTokens    int64
	InputTotal         int64
	CacheRead          int64
	CacheWrite         int64
	CostUsd            float64
	TimedN             int64
	Weighted           *float64
	Median             *float64
	P90                *float64
	CacheHitRate       *float64
	InputIncludesCache bool
}

// ToolStat is one tools[] entry.
type ToolStat struct {
	Tool            string
	Calls           int64
	StepsWithTool   int64
	ShareCalls      *float64
	GeneratedTokens float64
	CostUsd         float64
	TimedCalls      int64
	Weighted        *float64
	MedianTps       *float64
	TimeShare       *float64
	Runtime         string
}

// Call is one tool call attached to a step.
type Call struct {
	Name      string
	Category  string
	Runtime   *float64
	Error     bool
	Allocated float64
}

// Step is the canonical per-step row reconstructed from the compact tuple.
type Step struct {
	TS           int64
	ModelIndex   int
	Model        string
	Family       string
	SessionIndex int
	SessionID    string
	Visible      int64 // -1 when the source cannot separate visible output
	Reasoning    int64
	Generated    int64
	InputTotal   int64
	CacheRead    int64
	CacheWrite   int64
	CostUsd      float64
	Duration     *float64
	TotalDT      *float64 // OpenCode only
	Timed        bool
	Tool         bool
	Calls        []Call
}

// SessionRow is a session metadata row aligned with the step session index.
type SessionRow struct {
	SessionID   string
	Steps       int64
	Generated   int64
	CostUsd     float64
	SpanMinutes *float64
}

// DailyRow is one day of the report's daily series.
type DailyRow struct {
	Date            string
	Steps           int64
	TimedN          int64
	GeneratedTokens int64
	InputTotal      int64
	CacheRead       int64
	CostUsd         float64
	TotalSpendUsd   float64
	CacheHitRate    *float64
	Weighted        *float64
	Median          *float64
}

// RuntimeSummary is the OpenCode runtime block (absent for Command Code).
type RuntimeSummary struct {
	Steps                 int64
	MedianOverheadSeconds *float64
	P90OverheadSeconds    *float64
	OverheadShare         *float64
	MedianStepSeconds     *float64
}

// Report is the canonical, tool-neutral view of one schema v3 report.
type Report struct {
	Tool      string
	Input     Input
	Generated string
	Timezone  string
	Location  *time.Location
	DateFrom  string
	DateTo    string
	ModelsN   int64
	SessionsN int64
	Totals    Totals
	Timed     TimedSummary
	Cache     CacheSummary
	Cost      CostSummary
	Models    []ModelStat
	Tools     []ToolStat
	Steps     []Step
	Sessions  []SessionRow
	Daily     []DailyRow
	Runtime   *RuntimeSummary
}

// Parse decodes a schema v3 report into the canonical comparison view and
// fails closed on any structural or arithmetic inconsistency.
func Parse(tool string, input Input, data map[string]any) (*Report, error) {
	if tool != ToolOC && tool != ToolCMD && tool != ToolMuse {
		return nil, fmt.Errorf("compare: unknown tool %q", tool)
	}
	version, ok := pyjson.QueryInt(data, "schemaVersion")
	if !ok {
		return nil, fmt.Errorf("compare: %s report has no schemaVersion", tool)
	}
	if version != SchemaVersion {
		return nil, fmt.Errorf("compare: %s report schemaVersion %d, want %d", tool, version, SchemaVersion)
	}

	report := &Report{
		Tool:      tool,
		Input:     input,
		Generated: stringAt(data, "meta", "generatedAt"),
		Timezone:  stringAt(data, "meta", "timezone"),
	}
	report.Location = ParseTimezone(report.Timezone)

	for _, path := range [][]string{
		{"meta"}, {"totals"}, {"timed", "all"}, {"cache"}, {"cost"},
		{"byModel"}, {"tools"}, {"steps"}, {"lookups"}, {"sessions"}, {"daily"},
	} {
		value, ok := pyjson.Query(data, path...)
		if !ok || value == nil {
			return nil, fmt.Errorf("compare: %s report is missing %s", tool, strings.Join(path, "."))
		}
	}

	totals := objectAt(data, "totals")
	report.Totals = Totals{
		Steps:           intAt(totals, "steps"),
		ToolSteps:       intAt(totals, "toolSteps"),
		ToolCalls:       intAt(totals, "toolCalls"),
		GeneratedTokens: intAt(totals, "generatedTokens"),
		InputTotal:      intAt(totals, "inputTotal"),
		CacheRead:       intAt(totals, "cacheReadTokens"),
		CacheWrite:      intAt(totals, "cacheWriteTokens"),
		CostUsd:         floatAt(totals, "costUsd"),
		TotalSpendUsd:   floatAt(totals, "totalSpendUsd"),
		Models:          intAt(totals, "models"),
	}

	timed := objectAt(data, "timed", "all")
	report.Timed = TimedSummary{
		N:        intAt(timed, "n"),
		Tokens:   intAt(timed, "tokens"),
		Seconds:  floatAt(timed, "seconds"),
		Weighted: optFloatAt(timed, "weighted"),
		Median:   optFloatAt(timed, "median"),
		P10:      optFloatAt(timed, "p10"),
		P25:      optFloatAt(timed, "p25"),
		P75:      optFloatAt(timed, "p75"),
		P90:      optFloatAt(timed, "p90"),
		P99:      optFloatAt(timed, "p99"),
		Max:      optFloatAt(timed, "max"),
	}

	cache := objectAt(data, "cache")
	report.Cache = CacheSummary{
		InputTotal:  intAt(cache, "inputTokens"),
		CacheRead:   intAt(cache, "cacheReadTokens"),
		CacheWrite:  intAt(cache, "cacheWriteTokens"),
		HitRate:     optFloatAt(cache, "hitRate"),
		WriteShare:  optFloatAt(cache, "writeShare"),
		SavingsUsd:  floatAt(cache, "savings"),
		FittedModel: intAt(cache, "fittedModels"),
	}

	cost := objectAt(data, "cost")
	report.Cost = CostSummary{
		Recorded:            floatAt(cost, "recorded"),
		ErrorCostUsd:        floatAt(cost, "errorCostUsd"),
		UndatedErrorCostUsd: floatAt(cost, "undatedErrorCostUsd"),
		TotalSpendUsd:       floatAt(cost, "totalSpendUsd"),
		PerStep:             optFloatAt(cost, "perStep"),
		PerMillionGenerated: optFloatAt(cost, "perMillionGenerated"),
	}

	report.DateFrom = stringAt(data, "meta", "dateFrom")
	report.DateTo = stringAt(data, "meta", "dateTo")
	report.ModelsN = intAt(objectAt(data, "meta"), "models")
	report.SessionsN = intAt(objectAt(data, "meta"), "sessions")

	models, err := parseModels(data, tool)
	if err != nil {
		return nil, err
	}
	report.Models = models

	tools, err := parseTools(data)
	if err != nil {
		return nil, err
	}
	report.Tools = tools

	sessions, err := parseSessions(data)
	if err != nil {
		return nil, err
	}
	report.Sessions = sessions

	daily, err := parseDaily(data)
	if err != nil {
		return nil, err
	}
	report.Daily = daily

	steps, err := parseSteps(tool, data, models, sessions)
	if err != nil {
		return nil, err
	}
	report.Steps = steps

	if runtime, ok := pyjson.QueryMap(data, "runtime"); ok {
		report.Runtime = &RuntimeSummary{
			Steps:                 intAt(runtime, "steps"),
			MedianOverheadSeconds: optFloatAt(runtime, "medianOverheadSeconds"),
			P90OverheadSeconds:    optFloatAt(runtime, "p90OverheadSeconds"),
			OverheadShare:         optFloatAt(runtime, "overheadShare"),
			MedianStepSeconds:     optFloatAt(runtime, "medianStepSeconds"),
		}
	}

	if warnings := validateReport(report); len(warnings) > 0 {
		return nil, fmt.Errorf("compare: %s report failed validation:\n- %s", tool, strings.Join(warnings, "\n- "))
	}
	return report, nil
}

func parseModels(data map[string]any, tool string) ([]ModelStat, error) {
	lookupModels, ok := pyjson.QuerySlice(data, "lookups", "models")
	if !ok {
		return nil, fmt.Errorf("compare: %s report is missing lookups.models", tool)
	}
	rows, ok := pyjson.QuerySlice(data, "byModel")
	if !ok {
		return nil, fmt.Errorf("compare: %s report is missing byModel", tool)
	}
	if len(lookupModels) != len(rows) {
		return nil, fmt.Errorf("compare: %s lookups.models has %d entries, byModel has %d", tool, len(lookupModels), len(rows))
	}
	models := make([]ModelStat, len(rows))
	for index, entry := range rows {
		row, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("compare: %s byModel[%d] is not an object", tool, index)
		}
		lookup, ok := lookupModels[index].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("compare: %s lookups.models[%d] is not an object", tool, index)
		}
		id := stringAt(lookup, "id")
		short := stringAt(lookup, "short")
		if model := stringAt(row, "model"); model != id {
			return nil, fmt.Errorf("compare: %s byModel[%d].model = %q, lookups.models[%d].id = %q", tool, index, model, index, id)
		}
		models[index] = ModelStat{
			Model:              id,
			Short:              short,
			Provider:           stringAt(lookup, "provider"),
			Family:             NormalizeFamily(id, short),
			Steps:              intAt(row, "steps"),
			GeneratedTokens:    intAt(row, "generatedTokens"),
			InputTotal:         intAt(row, "inputTotal"),
			CacheRead:          intAt(row, "cacheReadTokens"),
			CacheWrite:         intAt(row, "cacheWriteTokens"),
			CostUsd:            floatAt(row, "costUsd"),
			TimedN:             intAt(row, "timedN"),
			Weighted:           optFloatAt(row, "weighted"),
			Median:             optFloatAt(row, "median"),
			P90:                optFloatAt(row, "p90"),
			CacheHitRate:       optFloatAt(row, "cacheHitRate"),
			InputIncludesCache: boolAt(row, "inputIncludesCache"),
		}
	}
	return models, nil
}

func parseTools(data map[string]any) ([]ToolStat, error) {
	rows, ok := pyjson.QuerySlice(data, "tools")
	if !ok {
		return nil, fmt.Errorf("compare: report is missing tools")
	}
	tools := make([]ToolStat, 0, len(rows))
	for index, entry := range rows {
		row, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("compare: tools[%d] is not an object", index)
		}
		tools = append(tools, ToolStat{
			Tool:            stringAt(row, "tool"),
			Calls:           intAt(row, "calls"),
			StepsWithTool:   intAt(row, "stepsWithTool"),
			ShareCalls:      optFloatAt(row, "shareCalls"),
			GeneratedTokens: floatAt(row, "generatedTokens"),
			CostUsd:         floatAt(row, "costUsd"),
			TimedCalls:      intAt(row, "timedCalls"),
			Weighted:        optFloatAt(row, "weighted"),
			MedianTps:       optFloatAt(row, "medianTps"),
			TimeShare:       optFloatAt(row, "timeShare"),
			Runtime:         stringAt(row, "runtime"),
		})
	}
	return tools, nil
}

func parseSessions(data map[string]any) ([]SessionRow, error) {
	rows, ok := pyjson.QuerySlice(data, "sessions")
	if !ok {
		return nil, fmt.Errorf("compare: report is missing sessions")
	}
	sessions := make([]SessionRow, len(rows))
	for index, entry := range rows {
		row, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("compare: sessions[%d] is not an object", index)
		}
		sessions[index] = SessionRow{
			SessionID:   stringAt(row, "sessionId"),
			Steps:       intAt(row, "steps"),
			Generated:   intAt(row, "generatedTokens"),
			CostUsd:     floatAt(row, "costUsd"),
			SpanMinutes: optFloatAt(row, "spanMinutes"),
		}
	}
	return sessions, nil
}

func parseDaily(data map[string]any) ([]DailyRow, error) {
	rows, ok := pyjson.QuerySlice(data, "daily")
	if !ok {
		return nil, fmt.Errorf("compare: report is missing daily")
	}
	daily := make([]DailyRow, 0, len(rows))
	for index, entry := range rows {
		row, ok := entry.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("compare: daily[%d] is not an object", index)
		}
		daily = append(daily, DailyRow{
			Date:            stringAt(row, "date"),
			Steps:           intAt(row, "nAll"),
			TimedN:          intAt(row, "nTimed"),
			GeneratedTokens: intAt(row, "generatedTokens"),
			InputTotal:      intAt(row, "inputTotal"),
			CacheRead:       intAt(row, "cacheReadTokens"),
			CostUsd:         floatAt(row, "costUsd"),
			TotalSpendUsd:   floatAt(row, "totalSpendUsd"),
			CacheHitRate:    optFloatAt(row, "cacheHitRate"),
			Weighted:        optFloatAt(row, "weighted"),
			Median:          optFloatAt(row, "median"),
		})
	}
	return daily, nil
}

func parseSteps(tool string, data map[string]any, models []ModelStat, sessions []SessionRow) ([]Step, error) {
	rawSteps, ok := pyjson.QuerySlice(data, "steps")
	if !ok {
		return nil, fmt.Errorf("compare: %s report is missing steps", tool)
	}
	toolNamesValue, ok := pyjson.Query(data, "lookups", "tools")
	if !ok {
		return nil, fmt.Errorf("compare: %s report is missing lookups.tools", tool)
	}
	names, ok := stringList(toolNamesValue)
	if !ok {
		return nil, fmt.Errorf("compare: %s lookups.tools is not a string array", tool)
	}
	timingValue, ok := pyjson.Query(data, "lookups", "timingReasons")
	if !ok {
		return nil, fmt.Errorf("compare: %s report is missing lookups.timingReasons", tool)
	}
	reasons, ok := stringList(timingValue)
	if !ok {
		return nil, fmt.Errorf("compare: %s lookups.timingReasons is not a string array", tool)
	}

	steps := make([]Step, 0, len(rawSteps))
	for index, entry := range rawSteps {
		tuple, ok := entry.([]any)
		if !ok {
			return nil, fmt.Errorf("compare: %s steps[%d] is not an array", tool, index)
		}
		want := 20
		if tool == ToolOC {
			want = 32
		}
		if len(tuple) != want {
			return nil, fmt.Errorf("compare: %s steps[%d] has %d slots, want %d", tool, index, len(tuple), want)
		}
		modelIndex, err := tupleIndex(tuple, 1, len(models), tool, index)
		if err != nil {
			return nil, err
		}
		sessionIndex, err := tupleIndex(tuple, 13, len(sessions), tool, index)
		if err != nil {
			return nil, err
		}
		model := models[modelIndex]
		step := Step{
			TS:           int64(tupleNum(tuple, 0)),
			ModelIndex:   modelIndex,
			Model:        model.Model,
			Family:       model.Family,
			SessionIndex: sessionIndex,
			SessionID:    sessions[sessionIndex].SessionID,
			InputTotal:   0,
			CostUsd:      tupleNum(tuple, 8),
			CacheRead:    int64(tupleNum(tuple, 6)),
			CacheWrite:   int64(tupleNum(tuple, 7)),
		}
		duration := tupleNum(tuple, 4)
		if duration > 0 {
			step.Duration = &duration
		}
		rawInput := int64(tupleNum(tuple, 5))
		if tool == ToolOC {
			step.Visible = int64(tupleNum(tuple, 3))
			step.Reasoning = int64(tupleNum(tuple, 17))
			step.Generated = step.Visible + step.Reasoning
			step.InputTotal = rawInput + step.CacheRead + step.CacheWrite
			totalDT := tupleNum(tuple, 18)
			if totalDT > 0 {
				step.TotalDT = &totalDT
			}
			timingIndex, err := tupleIndex(tuple, 31, len(reasons), tool, index)
			if err != nil {
				return nil, err
			}
			step.Timed = reasons[timingIndex] == "plausible" && step.Duration != nil
			calls, err := parseOCCalls(tuple[29], names, tool, index)
			if err != nil {
				return nil, err
			}
			step.Calls = calls
		} else {
			step.Visible = -1
			step.Generated = int64(tupleNum(tuple, 3))
			if model.InputIncludesCache {
				fresh := rawInput - step.CacheRead - step.CacheWrite
				if fresh < 0 {
					fresh = 0
				}
				step.InputTotal = fresh + step.CacheRead + step.CacheWrite
			} else {
				step.InputTotal = rawInput + step.CacheRead + step.CacheWrite
			}
			timingIndex, err := tupleIndex(tuple, 18, len(reasons), tool, index)
			if err != nil {
				return nil, err
			}
			step.Timed = reasons[timingIndex] == "plausible" && step.Duration != nil
			calls, err := parseCMDCalls(tuple[19], names, tool, index)
			if err != nil {
				return nil, err
			}
			step.Calls = calls
		}
		step.Tool = int64(tupleNum(tuple, 14)) > 0
		if len(step.Calls) == 0 {
			step.Calls = []Call{{Name: "(final answer)", Category: "(final answer)"}}
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func parseOCCalls(value any, names []string, tool string, stepIndex int) ([]Call, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("compare: %s steps[%d] tool calls are not an array", tool, stepIndex)
	}
	calls := make([]Call, 0, len(list))
	for i, entry := range list {
		tuple, ok := entry.([]any)
		if !ok || len(tuple) < 4 {
			return nil, fmt.Errorf("compare: %s steps[%d] call %d is malformed", tool, stepIndex, i)
		}
		index := int(tupleNum(tuple, 0))
		if index < 0 || index >= len(names) {
			return nil, fmt.Errorf("compare: %s steps[%d] call %d tool index %d out of range", tool, stepIndex, i, index)
		}
		call := Call{Name: names[index], Category: NormalizeTool(names[index])}
		if runtime := tupleNum(tuple, 1); runtime >= 0 {
			call.Runtime = &runtime
		}
		call.Error = tupleNum(tuple, 2) != 0
		call.Allocated = tupleNum(tuple, 3)
		calls = append(calls, call)
	}
	return calls, nil
}

func parseCMDCalls(value any, names []string, tool string, stepIndex int) ([]Call, error) {
	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("compare: %s steps[%d] tool indexes are not an array", tool, stepIndex)
	}
	calls := make([]Call, 0, len(list))
	for i := range list {
		index := int(tupleNum(list, i))
		if index < 0 || index >= len(names) {
			return nil, fmt.Errorf("compare: %s steps[%d] tool index %d out of range", tool, stepIndex, index)
		}
		calls = append(calls, Call{Name: names[index], Category: NormalizeTool(names[index])})
	}
	return calls, nil
}

// validateReport enforces the schema v3 arithmetic before the comparison
// trusts the report. Every violation is listed so a broken input produces an
// actionable error rather than a silently wrong dashboard.
func validateReport(report *Report) []string {
	var issues []string
	add := func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) }

	var modelSteps, modelGenerated, modelInput int64
	for _, model := range report.Models {
		modelSteps += model.Steps
		modelGenerated += model.GeneratedTokens
		modelInput += model.InputTotal
	}
	if modelSteps != report.Totals.Steps {
		add("sum(byModel.steps) = %d, totals.steps = %d", modelSteps, report.Totals.Steps)
	}
	if modelGenerated != report.Totals.GeneratedTokens {
		add("sum(byModel.generatedTokens) = %d, totals.generatedTokens = %d", modelGenerated, report.Totals.GeneratedTokens)
	}
	if modelInput != report.Totals.InputTotal {
		add("sum(byModel.inputTotal) = %d, totals.inputTotal = %d", modelInput, report.Totals.InputTotal)
	}

	var stepGenerated, stepInput, stepCacheRead, stepCacheWrite, toolCalls int64
	var stepCost float64
	var timedN int64
	for _, step := range report.Steps {
		stepGenerated += step.Generated
		stepInput += step.InputTotal
		stepCacheRead += step.CacheRead
		stepCacheWrite += step.CacheWrite
		stepCost += step.CostUsd
		toolCalls += int64(len(step.Calls))
		if step.Timed {
			timedN++
		}
	}
	if int64(len(report.Steps)) != report.Totals.Steps {
		add("steps rows = %d, totals.steps = %d", len(report.Steps), report.Totals.Steps)
	}
	if stepGenerated != report.Totals.GeneratedTokens {
		add("sum(step.generatedTokens) = %d, totals.generatedTokens = %d", stepGenerated, report.Totals.GeneratedTokens)
	}
	if stepInput != report.Totals.InputTotal {
		add("sum(step.inputTotal) = %d, totals.inputTotal = %d", stepInput, report.Totals.InputTotal)
	}
	if stepCacheRead != report.Totals.CacheRead {
		add("sum(step.cacheReadTokens) = %d, totals.cacheReadTokens = %d", stepCacheRead, report.Totals.CacheRead)
	}
	if stepCacheWrite != report.Totals.CacheWrite {
		add("sum(step.cacheWriteTokens) = %d, totals.cacheWriteTokens = %d", stepCacheWrite, report.Totals.CacheWrite)
	}
	if !closeEnough(stepCost, report.Totals.CostUsd, 1e-3) {
		add("sum(step.costUsd) = %.6f, totals.costUsd = %.6f", stepCost, report.Totals.CostUsd)
	}
	if toolCalls != report.Totals.ToolCalls {
		add("sum(step.calls) = %d, totals.toolCalls = %d", toolCalls, report.Totals.ToolCalls)
	}
	if timedN != report.Timed.N {
		add("timed steps = %d, timed.all.n = %d", timedN, report.Timed.N)
	}

	var toolCallsList int64
	for _, tool := range report.Tools {
		toolCallsList += tool.Calls
	}
	if toolCallsList != report.Totals.ToolCalls {
		add("sum(tools.calls) = %d, totals.toolCalls = %d", toolCallsList, report.Totals.ToolCalls)
	}

	var dailyGenerated int64
	var dailyCost float64
	for _, day := range report.Daily {
		dailyGenerated += day.GeneratedTokens
		dailyCost += day.CostUsd
	}
	if dailyGenerated != report.Totals.GeneratedTokens {
		add("sum(daily.generatedTokens) = %d, totals.generatedTokens = %d", dailyGenerated, report.Totals.GeneratedTokens)
	}
	if !closeEnough(dailyCost, report.Totals.CostUsd, 1e-3) {
		add("sum(daily.costUsd) = %.6f, totals.costUsd = %.6f", dailyCost, report.Totals.CostUsd)
	}

	if report.Cache.InputTotal != report.Totals.InputTotal {
		add("cache.inputTokens = %d, totals.inputTotal = %d", report.Cache.InputTotal, report.Totals.InputTotal)
	}
	if rate, ok := Ratio(float64(report.Cache.CacheRead), float64(report.Cache.InputTotal)); ok {
		if report.Cache.HitRate == nil || !closeEnough(*report.Cache.HitRate, rate, 0.001) {
			add("cache.hitRate = %v, cacheRead/inputTotal = %.4f", report.Cache.HitRate, rate)
		}
	}
	return issues
}

// ParseTimezone converts a report timezone label into a location. Both
// builders emit "UTC±HH:MM"; anything unrecognised falls back to UTC so the
// comparison stays deterministic.
func ParseTimezone(label string) *time.Location {
	if label == "" || label == "UTC" || label == "UTC+00:00" {
		return time.UTC
	}
	if strings.HasPrefix(label, "UTC") && len(label) >= 9 {
		rest := label[3:]
		sign := rest[0]
		if sign == '+' || sign == '-' {
			body := rest[1:]
			body = strings.Replace(body, ":", "", 1)
			if len(body) == 4 {
				hours := int(body[0]-'0')*10 + int(body[1]-'0')
				minutes := int(body[2]-'0')*10 + int(body[3]-'0')
				offset := (hours*60 + minutes) * 60
				if sign == '-' {
					offset = -offset
				}
				return time.FixedZone(label, offset)
			}
		}
	}
	if location, err := time.LoadLocation(label); err == nil {
		return location
	}
	return time.UTC
}

// LocalDate renders an epoch-second timestamp in the report's own timezone.
func (r *Report) LocalDate(ts int64) string {
	return time.Unix(ts, 0).In(r.Location).Format("2006-01-02")
}

func closeEnough(a, b, tolerance float64) bool {
	return math.Abs(a-b) <= tolerance+1e-9*math.Abs(b)
}

func tupleNum(tuple []any, index int) float64 {
	if index < 0 || index >= len(tuple) {
		return 0
	}
	value, _ := pyjson.Float(tuple[index])
	return value
}

func tupleIndex(tuple []any, index, limit int, tool string, stepIndex int) (int, error) {
	value := tupleNum(tuple, index)
	if value != math.Trunc(value) || value < 0 || int(value) >= limit {
		return 0, fmt.Errorf("compare: %s steps[%d] index %g out of range 0..%d", tool, stepIndex, value, limit-1)
	}
	return int(value), nil
}

func stringList(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return typed, true
	case []any:
		out := make([]string, len(typed))
		for index, item := range typed {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out[index] = text
		}
		return out, true
	}
	return nil, false
}

func objectAt(data map[string]any, path ...string) map[string]any {
	object, _ := pyjson.QueryMap(data, path...)
	return object
}

func stringAt(data map[string]any, path ...string) string {
	value, _ := pyjson.QueryString(data, path...)
	return value
}

func intAt(data map[string]any, key string) int64 {
	value, _ := pyjson.Int(data[key])
	return value
}

func floatAt(data map[string]any, key string) float64 {
	value, _ := pyjson.Float(data[key])
	return value
}

func optFloatAt(data map[string]any, key string) *float64 {
	value := data[key]
	if value == nil {
		return nil
	}
	number, ok := pyjson.Float(value)
	if !ok {
		return nil
	}
	return &number
}

func boolAt(data map[string]any, key string) bool {
	value, _ := data[key].(bool)
	return value
}
