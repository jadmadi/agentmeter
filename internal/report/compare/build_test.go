// SPDX-License-Identifier: AGPL-3.0-only

package compare

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	shared "github.com/jadmadi/agentmeter/internal/compare"
	"github.com/jadmadi/agentmeter/internal/stats"
)

// ---- synthetic schema v3 reports -------------------------------------------

type synthModel struct {
	id            string
	short         string
	provider      string
	includesCache bool
}

type synthCall struct {
	tool string
}

type synthStep struct {
	day   int // 2026-01-01 + day-1
	model int // index into models (sorted before use)
	out   int64
	reas  int64
	inp   int64
	cr    int64
	cw    int64
	cost  float64
	dur   float64 // zero means no timing
	calls []synthCall
}

type synthSpec struct {
	tool   string
	models []synthModel
	steps  []synthStep
}

func syntheticReport(t *testing.T, tool string) (*shared.Report, map[string]any) {
	t.Helper()
	spec := synthSpec{
		tool: tool,
		models: []synthModel{
			{id: "prov/shared-model", short: "shared-model", provider: "prov", includesCache: tool == shared.ToolCMD},
			{id: "prov/unique-" + tool, short: "unique-" + tool, provider: "prov"},
		},
	}
	shell, read, write, final := "shell_command", "read_file", "write_file", "(final answer)"
	if tool == shared.ToolOC {
		shell, read, write = "shell", "read", "write"
	}
	if tool == shared.ToolOC {
		spec.steps = []synthStep{
			{day: 1, model: 0, out: 75, reas: 25, inp: 50, cost: 0.01, dur: 2.0, calls: []synthCall{{shell}}},
			{day: 2, model: 0, out: 300, reas: 100, inp: 100, cr: 50, cw: 10, cost: 0.02, dur: 2.0, calls: []synthCall{{read}}},
			{day: 3, model: 1, out: 1200, reas: 300, inp: 200, cr: 100, cost: 0.03, dur: 3.0, calls: []synthCall{{write}}},
			{day: 3, model: 0, out: 50, inp: 25, cost: 0.005},
		}
	} else {
		spec.steps = []synthStep{
			{day: 2, model: 0, out: 100, inp: 50, cost: 0.01, dur: 2.0, calls: []synthCall{{shell}}},
			{day: 3, model: 0, out: 400, inp: 100, cr: 50, cw: 10, cost: 0.02, dur: 2.0, calls: []synthCall{{read}}},
			{day: 4, model: 1, out: 1500, inp: 200, cr: 100, cost: 0.03, dur: 3.0, calls: []synthCall{{write}}},
			{day: 4, model: 0, out: 50, inp: 25, cost: 0.005},
		}
	}
	data := buildSynthReport(t, spec, final)
	report, err := shared.Parse(tool, shared.Input{
		Tool: tool, Kind: shared.KindReport, Path: "/tmp/" + tool, Label: tool + " fixture",
	}, data)
	if err != nil {
		t.Fatalf("parse %s fixture: %v", tool, err)
	}
	return report, data
}

func buildSynthReport(t *testing.T, spec synthSpec, final string) map[string]any {
	t.Helper()
	models := append([]synthModel(nil), spec.models...)
	sort.Slice(models, func(i, j int) bool { return models[i].id < models[j].id })
	modelIndex := map[string]int{}
	for index, model := range models {
		modelIndex[model.id] = index
	}

	toolSet := map[string]struct{}{final: {}}
	for _, step := range spec.steps {
		for _, call := range step.calls {
			toolSet[call.tool] = struct{}{}
		}
	}
	tools := make([]string, 0, len(toolSet))
	for name := range toolSet {
		tools = append(tools, name)
	}
	sort.Strings(tools)
	toolIndex := map[string]int{}
	for index, name := range tools {
		toolIndex[name] = index
	}

	sessionSet := map[string]struct{}{}
	modelStats := make([]synthModelStat, len(models))
	for index, model := range models {
		modelStats[index].model = model
	}
	var totalGen, totalInput, totalCR, totalCW int64
	var totalCost float64
	var totalCalls, toolSteps int64
	timedRates := make([]float64, 0, len(spec.steps))
	var timedGen int64
	var timedSeconds float64
	stepTools := make(map[int]map[string]struct{})
	callStats := map[string]*synthCallStat{}
	for stepIndex, step := range spec.steps {
		settleSession := func(day int, index int) string {
			if (day+index)%2 == 0 {
				return "s1"
			}
			return "s2"
		}
		session := settleSession(step.day, stepIndex)
		sessionSet[session] = struct{}{}

		generated := step.out + step.reas
		inputTotal := step.inp + step.cr + step.cw
		if spec.tool == shared.ToolCMD && models[step.model].includesCache {
			fresh := step.inp - step.cr - step.cw
			if fresh < 0 {
				fresh = 0
			}
			inputTotal = fresh + step.cr + step.cw
		}
		totalGen += generated
		totalInput += inputTotal
		totalCR += step.cr
		totalCW += step.cw
		totalCost += step.cost
		if len(step.calls) > 0 {
			toolSteps++
		}
		totalCalls += int64(maxInt(len(step.calls), 1))

		model := &modelStats[step.model]
		model.steps++
		model.generated += generated
		model.input += inputTotal
		model.cr += step.cr
		model.cw += step.cw
		model.cost += step.cost
		if step.dur > 0 {
			model.timedN++
			model.timedGen += generated
			model.timedSeconds += step.dur
			model.rates = append(model.rates, float64(generated)/step.dur)
		}

		if step.dur > 0 {
			timedGen += generated
			timedSeconds += step.dur
			timedRates = append(timedRates, float64(generated)/step.dur)
		}

		if stepTools[stepIndex] == nil {
			stepTools[stepIndex] = make(map[string]struct{})
		}
		calls := step.calls
		if len(calls) == 0 {
			calls = []synthCall{{final}}
		}
		for _, call := range calls {
			stepTools[stepIndex][call.tool] = struct{}{}
			stat := callStats[call.tool]
			if stat == nil {
				stat = &synthCallStat{tool: call.tool}
				callStats[call.tool] = stat
			}
			stat.calls++
			stat.generated += float64(generated) / float64(len(calls))
			stat.cost += step.cost / float64(len(calls))
			if step.dur > 0 {
				stat.timedCalls++
				stat.timedGen += float64(generated) / float64(len(calls))
				stat.timedSeconds += step.dur / float64(len(calls))
			}
		}
	}
	sessionIDs := make([]string, 0, len(sessionSet))
	for id := range sessionSet {
		sessionIDs = append(sessionIDs, id)
	}
	sort.Strings(sessionIDs)
	sessionIndex := map[string]int{}
	for index, id := range sessionIDs {
		sessionIndex[id] = index
	}

	stepTuples := make([]any, 0, len(spec.steps))
	type sessionAgg struct {
		steps int64
		gen   int64
		cost  float64
		first int64
		last  int64
	}
	sessionAggs := map[string]*sessionAgg{}
	type dayAgg struct {
		date      string
		nAll      int64
		nTimed    int64
		gen       int64
		input     int64
		cr        int64
		cost      float64
		timedGen  int64
		timedSecs float64
		rates     []float64
	}
	days := map[string]*dayAgg{}
	for stepIndex, step := range spec.steps {
		ts := int64(time.Date(2026, 1, step.day, 12, 0, 0, 0, time.UTC).Unix())
		generated := step.out + step.reas
		inputTotal := step.inp + step.cr + step.cw
		if spec.tool == shared.ToolCMD && models[step.model].includesCache {
			fresh := step.inp - step.cr - step.cw
			if fresh < 0 {
				fresh = 0
			}
			inputTotal = fresh + step.cr + step.cw
		}
		session := sessionIDs[(step.day+stepIndex)%len(sessionIDs)]
		primary := 0
		calls := step.calls
		if len(calls) == 0 {
			calls = []synthCall{{final}}
		}
		callIndexes := make([]any, 0, len(calls))
		for _, call := range calls {
			callIndexes = append(callIndexes, int64(toolIndex[call.tool]))
		}
		timingIndex := int64(1)
		if step.dur > 0 {
			timingIndex = 0
		}
		if spec.tool == shared.ToolOC {
			callsValue := make([]any, 0, len(calls))
			for _, call := range calls {
				runtime := float64(-1)
				if step.dur > 0 {
					runtime = step.dur
				}
				callsValue = append(callsValue, []any{int64(toolIndex[call.tool]), runtime, int64(0), float64(0)})
			}
			totalDT := float64(-1)
			toolDT := float64(-1)
			overhead := float64(-1)
			if step.dur > 0 {
				totalDT = step.dur + 0.5
				toolDT = 0.25
				overhead = 0.5
			}
			tuple := []any{
				ts, int64(modelIndex[models[step.model].id]), int64(primary), step.out,
				durationValue(step.dur), step.inp, step.cr, step.cw, step.cost,
				int64(0), int64(0), int64(0), int64(0), int64(sessionIndex[session]),
				int64(len(calls)), int64(0), int64(0), step.reas,
				totalDT, toolDT, inputTotal, int64(0), int64(0),
				float64(-1), float64(-1), float64(-1), overhead, float64(-1), float64(-1),
				callsValue, float64(-1), timingIndex,
			}
			stepTuples = append(stepTuples, tuple)
		} else {
			tuple := []any{
				ts, int64(modelIndex[models[step.model].id]), int64(primary), step.out,
				durationValue(step.dur), step.inp, step.cr, step.cw, step.cost,
				int64(0), int64(0), int64(0), int64(0), int64(sessionIndex[session]),
				int64(len(calls)), int64(0), int64(0), int64(-1), timingIndex, callIndexes,
			}
			stepTuples = append(stepTuples, tuple)
		}

		agg := sessionAggs[session]
		if agg == nil {
			agg = &sessionAgg{first: ts, last: ts}
			sessionAggs[session] = agg
		}
		if ts < agg.first {
			agg.first = ts
		}
		if ts > agg.last {
			agg.last = ts
		}
		agg.steps++
		agg.gen += generated
		agg.cost += step.cost

		date := time.Unix(ts, 0).UTC().Format("2006-01-02")
		day := days[date]
		if day == nil {
			day = &dayAgg{date: date}
			days[date] = day
		}
		day.nAll++
		day.gen += generated
		day.input += inputTotal
		day.cr += step.cr
		day.cost += step.cost
		if step.dur > 0 {
			day.nTimed++
			day.timedGen += generated
			day.timedSecs += step.dur
			day.rates = append(day.rates, float64(generated)/step.dur)
		}
	}

	// ---- models
	byModel := make([]any, 0, len(modelStats))
	for _, model := range modelStats {
		hitRate := float64OrNil(model.cr, model.input)
		byModel = append(byModel, map[string]any{
			"model": model.model.id, "short": model.model.short, "provider": model.model.provider,
			"steps": model.steps, "generatedTokens": model.generated,
			"inputTotal": model.input, "cacheReadTokens": model.cr, "cacheWriteTokens": model.cw,
			"costUsd": stats.PyRound(model.cost, 6), "timedN": model.timedN,
			"weighted": weightedOrNil(model.timedGen, model.timedSeconds),
			"median":   medianOrNilPtr(model.rates), "p90": percentileOrNilPtr(model.rates, 90),
			"cacheHitRate": hitRate, "inputIncludesCache": model.model.includesCache,
		})
	}

	// ---- tools
	toolList := make([]synthCallStat, 0, len(callStats))
	for _, stat := range callStats {
		toolList = append(toolList, *stat)
	}
	sort.Slice(toolList, func(i, j int) bool {
		if toolList[i].calls != toolList[j].calls {
			return toolList[i].calls > toolList[j].calls
		}
		return toolList[i].tool < toolList[j].tool
	})
	toolsOut := make([]any, 0, len(toolList))
	for _, stat := range toolList {
		calls := int64(0)
		for stepIndex := range stepTools {
			if _, ok := stepTools[stepIndex][stat.tool]; ok {
				calls++
			}
		}
		var share any
		if totalCalls > 0 {
			share = float64(stat.calls) / float64(totalCalls)
		}
		runtime := "unavailable"
		var timeShare any
		if spec.tool == shared.ToolOC {
			runtime = ""
			timeShare = nil
		}
		toolsOut = append(toolsOut, map[string]any{
			"tool": stat.tool, "calls": stat.calls, "stepsWithTool": calls,
			"shareCalls": share, "generatedTokens": float64(stat.generated),
			"costUsd": stats.PyRound(stat.cost, 6), "timedCalls": stat.timedCalls,
			"weighted":  weightedOrNil(int64(stat.timedGen), stat.timedSeconds),
			"medianTps": nil, "timeShare": timeShare, "runtime": runtime,
		})
	}

	// ---- sessions
	sessionsOut := make([]any, 0, len(sessionIDs))
	for _, id := range sessionIDs {
		agg := sessionAggs[id]
		span := 0.0
		if agg != nil {
			span = float64(agg.last-agg.first) / 60
		}
		var steps, gen int64
		var cost float64
		if agg != nil {
			steps, gen, cost = agg.steps, agg.gen, agg.cost
		}
		sessionsOut = append(sessionsOut, map[string]any{
			"sessionId": id, "steps": steps, "generatedTokens": gen,
			"costUsd": stats.PyRound(cost, 6), "spanMinutes": stats.PyRound(span, 1),
		})
	}

	// ---- daily
	dates := make([]string, 0, len(days))
	for date := range days {
		dates = append(dates, date)
	}
	sort.Strings(dates)
	dailyOut := make([]any, 0, len(dates))
	for _, date := range dates {
		day := days[date]
		dailyOut = append(dailyOut, map[string]any{
			"date": date, "nAll": day.nAll, "nTimed": day.nTimed,
			"generatedTokens": day.gen, "inputTotal": day.input,
			"cacheReadTokens": day.cr, "costUsd": stats.PyRound(day.cost, 6),
			"totalSpendUsd": stats.PyRound(day.cost, 6),
			"cacheHitRate":  float64OrNil(day.cr, day.input),
			"weighted":      weightedOrNil(day.timedGen, day.timedSecs),
			"median":        medianOrNilPtr(day.rates),
		})
	}

	sortedRates := append([]float64(nil), timedRates...)
	sort.Float64s(sortedRates)
	from := dates[0]
	to := dates[len(dates)-1]
	modelValues := make([]any, 0, len(models))
	for _, model := range models {
		modelValues = append(modelValues, map[string]any{"id": model.id, "short": model.short, "provider": model.provider})
	}
	timingReasons := []any{"plausible", "noTiming", "tooShort", "tooShortDuration", "tooLong", "slowRate", "fastRate"}

	return map[string]any{
		"schemaVersion": int64(3),
		"meta": map[string]any{
			"generatedAt": "2026-01-05T00:00:00+00:00", "timezone": "UTC",
			"dateFrom": from, "dateTo": to,
			"models": int64(len(models)), "sessions": int64(len(sessionIDs)),
		},
		"totals": map[string]any{
			"steps": int64(len(spec.steps)), "toolSteps": toolSteps, "toolCalls": totalCalls,
			"generatedTokens": totalGen, "inputTotal": totalInput,
			"cacheReadTokens": totalCR, "cacheWriteTokens": totalCW,
			"costUsd": stats.PyRound(totalCost, 6), "totalSpendUsd": stats.PyRound(totalCost, 6),
			"models": int64(len(models)),
		},
		"timed": map[string]any{"all": map[string]any{
			"n": int64(len(timedRates)), "tokens": timedGen,
			"seconds":  stats.PyRound(timedSeconds, 3),
			"weighted": weightedOrNil(timedGen, timedSeconds),
			"median":   medianOrNilPtr(sortedRates), "p10": percentileOrNilPtr(sortedRates, 10),
			"p25": percentileOrNilPtr(sortedRates, 25), "p75": percentileOrNilPtr(sortedRates, 75),
			"p90": percentileOrNilPtr(sortedRates, 90), "p99": percentileOrNilPtr(sortedRates, 99),
			"max": maxOrNil(sortedRates),
		}},
		"cache": map[string]any{
			"inputTokens": totalInput, "cacheReadTokens": totalCR, "cacheWriteTokens": totalCW,
			"hitRate": float64OrNil(totalCR, totalInput), "writeShare": float64OrNil(totalCW, totalInput),
			"savings": 0.0, "fittedModels": int64(0),
		},
		"cost": map[string]any{
			"recorded": stats.PyRound(totalCost, 6), "errorCostUsd": 0.0,
			"totalSpendUsd":       stats.PyRound(totalCost, 6),
			"perStep":             divisionOrNil(totalCost, float64(len(spec.steps))),
			"perMillionGenerated": millionOrNil(totalCost, totalGen),
		},
		"byModel": byModel,
		"lookups": map[string]any{
			"models": modelValues, "tools": stringValues(tools), "timingReasons": timingReasons,
		},
		"tools":    toolsOut,
		"steps":    stepTuples,
		"sessions": sessionsOut,
		"daily":    dailyOut,
	}
}

type synthModelStat struct {
	model        synthModel
	steps        int64
	generated    int64
	input        int64
	cr           int64
	cw           int64
	cost         float64
	timedN       int64
	timedGen     int64
	timedSeconds float64
	rates        []float64
}

type synthCallStat struct {
	tool         string
	calls        int64
	generated    float64
	cost         float64
	timedCalls   int64
	timedGen     float64
	timedSeconds float64
}

func durationValue(dur float64) any {
	if dur <= 0 {
		return int64(-1)
	}
	return dur
}

func stringValues(values []string) []any {
	out := make([]any, len(values))
	for index, value := range values {
		out[index] = value
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func divisionOrNil(numerator, denominator float64) any {
	if denominator == 0 {
		return nil
	}
	return stats.PyRound(numerator/denominator, 4)
}

func millionOrNil(cost float64, generated int64) any {
	if generated == 0 {
		return nil
	}
	return stats.PyRound(cost/float64(generated)*1e6, 2)
}

func float64OrNil(numerator, denominator int64) any {
	if denominator == 0 {
		return nil
	}
	return stats.PyRound(float64(numerator)/float64(denominator), 4)
}

func weightedOrNil(tokens int64, seconds float64) any {
	if seconds == 0 {
		return nil
	}
	return stats.R1(float64(tokens) / seconds)
}

func medianOrNilPtr(values []float64) any {
	value, ok := stats.Median(values)
	if !ok {
		return nil
	}
	return stats.R1(value)
}

func percentileOrNilPtr(values []float64, p float64) any {
	if len(values) == 0 {
		return nil
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	value, ok := stats.Percentile(sorted, p)
	if !ok {
		return nil
	}
	return stats.R1(value)
}

func maxOrNil(values []float64) any {
	if len(values) == 0 {
		return nil
	}
	return stats.R1(values[len(values)-1])
}

// ---- tests -----------------------------------------------------------------

func TestBuildSyntheticComparison(t *testing.T) {
	oc, _ := syntheticReport(t, shared.ToolOC)
	cmd, _ := syntheticReport(t, shared.ToolCMD)
	now := time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	doc, err := Build(oc, cmd, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if doc["kind"] != "comparison" {
		t.Errorf("kind = %v", doc["kind"])
	}

	window := doc["meta"].(map[string]any)["window"].(map[string]any)
	if window["mode"] != "overlap" || window["from"] != "2026-01-02" || window["to"] != "2026-01-03" {
		t.Errorf("window = %v", window)
	}

	totals := doc["totals"].(map[string]any)
	ocTotals := totals["oc"].(map[string]any)
	cmdTotals := totals["cmd"].(map[string]any)
	if ocTotals["steps"].(int64) != 3 {
		t.Errorf("oc window steps = %v, want 3 (Jan 1 is outside the overlap)", ocTotals["steps"])
	}
	if cmdTotals["steps"].(int64) != 2 {
		t.Errorf("cmd window steps = %v, want 2 (Jan 4 is outside the overlap)", cmdTotals["steps"])
	}
	if ocTotals["generatedTokens"].(int64) != 1950 {
		t.Errorf("oc generated = %v, want 1950", ocTotals["generatedTokens"])
	}
	if cmdTotals["generatedTokens"].(int64) != 500 {
		t.Errorf("cmd generated = %v, want 500", cmdTotals["generatedTokens"])
	}

	throughput := doc["throughput"].(map[string]any)
	ocSpeed := throughput["oc"].(map[string]any)
	cmdSpeed := throughput["cmd"].(map[string]any)
	if ocSpeed["n"].(int64) != 2 || cmdSpeed["n"].(int64) != 2 {
		t.Errorf("timed n = %v/%v, want 2/2", ocSpeed["n"], cmdSpeed["n"])
	}
	// oc: (400+1500)/(2+3) = 380, cmd: (100+400)/(2+2) = 125.
	if value, _ := ocSpeed["weighted"].(float64); value != 380 {
		t.Errorf("oc weighted = %v, want 380", ocSpeed["weighted"])
	}
	if value, _ := cmdSpeed["weighted"].(float64); value != 125 {
		t.Errorf("cmd weighted = %v, want 125", cmdSpeed["weighted"])
	}
	if throughput["winner"].(map[string]any)["weighted"] != shared.ToolOC {
		t.Error("weighted speed winner should be oc")
	}

	families := doc["models"].(map[string]any)["families"].([]any)
	sharedFamily := findFamily(t, families, "shared-model")
	if sharedFamily["shared"] != true {
		t.Error("shared-model should be a shared family")
	}
	if sharedFamily["modelMatch"] != "1:1" {
		t.Errorf("shared-model modelMatch = %v, want 1:1", sharedFamily["modelMatch"])
	}
	ocFamily := sharedFamily["oc"].(map[string]any)
	cmdFamily := sharedFamily["cmd"].(map[string]any)
	if ocFamily["steps"].(int64) != 2 || cmdFamily["steps"].(int64) != 2 {
		t.Errorf("shared family steps = %v/%v, want 2/2", ocFamily["steps"], cmdFamily["steps"])
	}
	if ocFamily["weighted"] == nil || cmdFamily["weighted"] == nil {
		t.Error("shared family should have weighted speed on both sides")
	}

	strata := doc["strata"].(map[string]any)
	pooled := strata["pooled"].([]any)
	bucket300 := findBucket(t, pooled, "300-1k")
	ocBucket := bucket300["oc"].(map[string]any)
	cmdBucket := bucket300["cmd"].(map[string]any)
	if ocBucket["n"].(int64) != 1 || cmdBucket["n"].(int64) != 1 {
		t.Errorf("300-1k n = %v/%v, want 1/1", ocBucket["n"], cmdBucket["n"])
	}
	if delta, ok := bucket300["delta"].(map[string]any)["weightedPct"].(float64); !ok || delta != 0 {
		t.Errorf("300-1k weighted delta = %v, want 0 (both 200 tok/s)", bucket300["delta"])
	}
	familyStrata := strata["families"].([]any)
	if len(familyStrata) != 1 || familyStrata[0].(map[string]any)["family"] != "shared-model" {
		t.Errorf("strata families = %v, want only shared-model", familyStrata)
	}

	toolMix := doc["tools"].(map[string]any)
	categories := toolMix["normalized"].([]any)
	readCategory := findCategory(t, categories, "read")
	readOC := readCategory["oc"].(map[string]any)
	readCMD := readCategory["cmd"].(map[string]any)
	if readOC["calls"].(int64) != 1 || readCMD["calls"].(int64) != 1 {
		t.Errorf("read calls = %v/%v, want 1/1", readOC["calls"], readCMD["calls"])
	}

	sessions := doc["sessions"].(map[string]any)
	ocSessions := sessions["oc"].(map[string]any)
	cmdSessions := sessions["cmd"].(map[string]any)
	if ocSessions["runtimeAvailable"] != true {
		t.Error("oc should expose runtime metrics")
	}
	if cmdSessions["runtimeAvailable"] != false || cmdSessions["activeMinutesMedian"] != nil {
		t.Error("cmd must report runtime as unavailable")
	}
	partial := doc["meta"].(map[string]any)["partialMetrics"].([]any)
	if !containsString(partial, "Command Code records no per-step wall clock") {
		t.Errorf("partialMetrics = %v", partial)
	}

	likeForLike := doc["likeForLike"].(map[string]any)
	if likeForLike["available"] != true {
		t.Error("like-for-like scope should be available with a shared family")
	}
	sharedTotals := likeForLike["totals"].(map[string]any)
	if sharedTotals["oc"].(map[string]any)["steps"].(int64) != 2 ||
		sharedTotals["cmd"].(map[string]any)["steps"].(int64) != 2 {
		t.Errorf("shared steps = %v/%v, want 2/2", sharedTotals["oc"], sharedTotals["cmd"])
	}

	verdictsByScope := doc["verdicts"].(map[string]any)
	sharedVerdicts, _ := verdictsByScope["shared"].([]any)
	allVerdicts, _ := verdictsByScope["all"].([]any)
	if len(sharedVerdicts) != 6 || len(allVerdicts) != 6 {
		t.Fatalf("verdicts = shared %d / all %d, want 6/6", len(sharedVerdicts), len(allVerdicts))
	}
	if sharedVerdicts[0].(map[string]any)["winner"] != shared.ToolOC {
		t.Errorf("shared speed verdict winner = %v", sharedVerdicts[0])
	}
	if sharedVerdicts[2].(map[string]any)["winner"] != shared.ToolOC {
		t.Errorf("shared cost verdict winner = %v", sharedVerdicts[2])
	}
	if sharedVerdicts[0].(map[string]any)["confidence"] != "low-sample" {
		t.Errorf("shared speed confidence = %v, want low-sample (one timed step)", sharedVerdicts[0])
	}
	spendVerdict := sharedVerdicts[4].(map[string]any)
	if spendVerdict["scope"] != "all" {
		t.Errorf("spend verdict should be all-model: %v", spendVerdict)
	}
	if spendVerdict["confidence"] != "low-sample" {
		t.Errorf("spend verdict confidence = %v, want low-sample (one session)", spendVerdict)
	}

	timeline := doc["timeline"].(map[string]any)
	days := timeline["days"].([]any)
	if len(days) != 2 {
		t.Errorf("timeline days = %d, want 2", len(days))
	}
	cumulative := timeline["cumulative"].([]any)
	last := cumulative[len(cumulative)-1].(map[string]any)
	if last["oc"].(map[string]any)["generatedTokens"].(int64) != 1950 {
		t.Errorf("cumulative oc generated = %v", last["oc"])
	}
}

func TestFamilyFlagsAndConfidence(t *testing.T) {
	low, median, high := 0.0009, 0.0025, 0.010
	if !isOutlier(&low, &median, 2.0) {
		t.Error("0.0009 against a 0.0025 median should be a cost outlier")
	}
	if isOutlier(&high, &median, 2.0) != true {
		t.Error("0.010 against a 0.0025 median should be a cost outlier")
	}
	if isOutlier(&median, &median, 2.0) {
		t.Error("a value equal to the median is not an outlier")
	}

	oc, cmd := 103.0, 100.0
	if got := familyConfidence(true, &oc, &cmd, 100, 100); got != "tie" {
		t.Errorf("3%% difference confidence = %q, want tie", got)
	}
	oc = 120
	if got := familyConfidence(true, &oc, &cmd, 10, 10); got != "low-sample" {
		t.Errorf("low-n confidence = %q, want low-sample", got)
	}
	oc = 115
	if got := familyConfidence(true, &oc, &cmd, 100, 100); got != "weak" {
		t.Errorf("15%% difference confidence = %q, want weak", got)
	}
	oc = 150
	if got := familyConfidence(true, &oc, &cmd, 100, 100); got != "clear" {
		t.Errorf("50%% difference confidence = %q, want clear", got)
	}
	if got := familyConfidence(false, &oc, &cmd, 100, 100); got != "one-sided" {
		t.Errorf("one-sided confidence = %q", got)
	}
}

func TestBuildDeterministic(t *testing.T) {
	oc, _ := syntheticReport(t, shared.ToolOC)
	cmd, _ := syntheticReport(t, shared.ToolCMD)
	now := time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)
	first, err := Build(oc, cmd, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	second, err := Build(oc, cmd, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal first: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal second: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("Build is not deterministic")
	}
}

func TestBuildRejectsNil(t *testing.T) {
	if _, err := Build(nil, nil, time.Now()); err == nil {
		t.Fatal("Build(nil, nil) should fail")
	}
}

func TestBuildNoOverlap(t *testing.T) {
	oc, _ := syntheticReport(t, shared.ToolOC)
	cmd, cmdData := syntheticReport(t, shared.ToolCMD)
	// Push cmd two months later so the ranges are disjoint.
	shift := func(data map[string]any, days int) {
		delta := int64(days * 24 * 3600)
		for _, entry := range data["steps"].([]any) {
			tuple := entry.([]any)
			tuple[0] = tuple[0].(int64) + delta
		}
		for _, entry := range data["daily"].([]any) {
			day := entry.(map[string]any)
			parsed, _ := time.Parse("2006-01-02", day["date"].(string))
			day["date"] = parsed.AddDate(0, 0, days).Format("2006-01-02")
		}
		meta := data["meta"].(map[string]any)
		meta["dateFrom"] = shiftDate(meta["dateFrom"].(string), days)
		meta["dateTo"] = shiftDate(meta["dateTo"].(string), days)
	}
	shift(cmdData, 60)
	cmd, err := shared.Parse(shared.ToolCMD, cmd.Input, cmdData)
	if err != nil {
		t.Fatalf("reparse cmd: %v", err)
	}
	doc, err := Build(oc, cmd, time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Build disjoint: %v", err)
	}
	window := doc["meta"].(map[string]any)["window"].(map[string]any)
	if window["mode"] != "disjoint" {
		t.Errorf("window mode = %v, want disjoint", window["mode"])
	}
	partial := doc["meta"].(map[string]any)["partialMetrics"].([]any)
	if !containsString(partial, "no overlapping dates") {
		t.Errorf("partialMetrics = %v", partial)
	}
}

func shiftDate(value string, days int) string {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	return parsed.AddDate(0, 0, days).Format("2006-01-02")
}

func TestDetectInputs(t *testing.T) {
	root := t.TempDir()

	// Command Code: a plain directory is a live session root; a directory with
	// telemetry-data.json is a report.
	cmdRoot := filepath.Join(root, "cmd-sessions")
	if err := os.MkdirAll(cmdRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	input, err := Detect(shared.ToolCMD, cmdRoot)
	if err != nil {
		t.Fatalf("detect cmd root: %v", err)
	}
	if input.Kind != shared.KindLive {
		t.Errorf("cmd root kind = %q, want live", input.Kind)
	}
	reportDir := filepath.Join(root, "cmd-report")
	if err := os.MkdirAll(reportDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(reportDir, ReportName), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, err = Detect(shared.ToolCMD, reportDir)
	if err != nil {
		t.Fatalf("detect cmd report dir: %v", err)
	}
	if input.Kind != shared.KindReport {
		t.Errorf("cmd report dir kind = %q, want report", input.Kind)
	}

	// OpenCode: a JSON file is a report; a SQLite-header file is a live store;
	// an empty directory is an error.
	reportFile := filepath.Join(root, "oc.json")
	if err := os.WriteFile(reportFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	input, err = Detect(shared.ToolOC, reportFile)
	if err != nil || input.Kind != shared.KindReport {
		t.Fatalf("detect oc report = %+v, %v", input, err)
	}
	dbFile := filepath.Join(root, "opencode.db")
	if err := os.WriteFile(dbFile, append([]byte("SQLite format 3\x00"), make([]byte, 64)...), 0o644); err != nil {
		t.Fatal(err)
	}
	input, err = Detect(shared.ToolOC, dbFile)
	if err != nil || input.Kind != shared.KindLive {
		t.Fatalf("detect oc db = %+v, %v", input, err)
	}
	emptyDir := filepath.Join(root, "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(shared.ToolOC, emptyDir); err == nil {
		t.Error("detect oc empty dir should fail")
	}
	if _, err := Detect(shared.ToolOC, filepath.Join(root, "missing.db")); err == nil {
		t.Error("detect oc missing path should fail")
	}
}

func TestFingerprintReportChanges(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "telemetry-data.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	input := shared.Input{Tool: shared.ToolOC, Kind: shared.KindReport, Path: path}
	first := Fingerprint(input)
	if err := os.WriteFile(path, []byte("{\"schemaVersion\":3}"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := Fingerprint(input)
	if first == second {
		t.Error("report fingerprint did not change after the file changed")
	}
}

func findFamily(t *testing.T, families []any, name string) map[string]any {
	t.Helper()
	for _, entry := range families {
		family := entry.(map[string]any)
		if family["family"] == name {
			return family
		}
	}
	t.Fatalf("family %q not found", name)
	return nil
}

func findBucket(t *testing.T, buckets []any, label string) map[string]any {
	t.Helper()
	for _, entry := range buckets {
		bucket := entry.(map[string]any)
		if bucket["bucket"] == label {
			return bucket
		}
	}
	t.Fatalf("bucket %q not found", label)
	return nil
}

func findCategory(t *testing.T, categories []any, name string) map[string]any {
	t.Helper()
	for _, entry := range categories {
		category := entry.(map[string]any)
		if category["category"] == name {
			return category
		}
	}
	t.Fatalf("category %q not found", name)
	return nil
}

func containsString(values []any, needle string) bool {
	for _, value := range values {
		if text, ok := value.(string); ok && strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
