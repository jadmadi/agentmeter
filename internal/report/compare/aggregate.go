// SPDX-License-Identifier: AGPL-3.0-only

package compare

import (
	"sort"
	"time"

	shared "github.com/jadmadi/agentmeter/internal/compare"
)

// comparisonWindow is the shared date window the comparison runs over.
// Overlap means both tools have dated telemetry and the window is their
// intersection; every other mode compares each tool's full range. Sides are
// in canonical pair order.
type comparisonWindow struct {
	Mode  string
	From  string
	To    string
	Days  int
	FromA string
	ToA   string
	FromB string
	ToB   string
}

func resolveWindow(a, b *shared.Report) comparisonWindow {
	window := comparisonWindow{
		Mode:  "full",
		FromA: reportRangeFrom(a),
		ToA:   reportRangeTo(a),
		FromB: reportRangeFrom(b),
		ToB:   reportRangeTo(b),
	}
	aFrom, aTo := window.FromA, window.ToA
	bFrom, bTo := window.FromB, window.ToB
	switch {
	case aFrom != "" && aTo != "" && bFrom != "" && bTo != "":
		from := maxString(aFrom, bFrom)
		to := minString(aTo, bTo)
		if from <= to {
			window.Mode = "overlap"
			window.From = from
			window.To = to
		} else {
			window.Mode = "disjoint"
			window.From = minString(aFrom, bFrom)
			window.To = maxString(aTo, bTo)
		}
	case aFrom != "" && aTo != "":
		window.Mode = "single"
		window.From = aFrom
		window.To = aTo
	case bFrom != "" && bTo != "":
		window.Mode = "single"
		window.From = bFrom
		window.To = bTo
	default:
		window.Mode = "empty"
	}
	if window.From != "" && window.To != "" {
		window.Days = inclusiveDays(window.From, window.To)
	}
	return window
}

func reportRangeFrom(report *shared.Report) string {
	if report.DateFrom != "" {
		return report.DateFrom
	}
	if len(report.Daily) > 0 {
		return report.Daily[0].Date
	}
	return ""
}

func reportRangeTo(report *shared.Report) string {
	if report.DateTo != "" {
		return report.DateTo
	}
	if len(report.Daily) > 0 {
		return report.Daily[len(report.Daily)-1].Date
	}
	return ""
}

func inclusiveDays(from, to string) int {
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return 0
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil || end.Before(start) {
		return 0
	}
	return int(end.Sub(start).Hours()/24) + 1
}

func maxString(a, b string) string {
	if a > b {
		return a
	}
	return b
}

func minString(a, b string) string {
	if a < b {
		return a
	}
	return b
}

// windowSteps filters a report's steps to the comparison window. A missing
// timestamp (0) is never filtered out; schema v3 steps always carry one.
func windowSteps(report *shared.Report, window comparisonWindow) []shared.Step {
	if window.Mode != "overlap" {
		return report.Steps
	}
	steps := make([]shared.Step, 0, len(report.Steps))
	for _, step := range report.Steps {
		date := report.LocalDate(step.TS)
		if date >= window.From && date <= window.To {
			steps = append(steps, step)
		}
	}
	return steps
}

// timedSample accumulates generation throughput for a set of timed steps.
type timedSample struct {
	n       int64
	tokens  int64
	seconds float64
	rates   []float64
}

func (sample *timedSample) add(generated int64, seconds float64) {
	if seconds <= 0 {
		return
	}
	sample.n++
	sample.tokens += generated
	sample.seconds += seconds
	sample.rates = append(sample.rates, float64(generated)/seconds)
}

func (sample *timedSample) summary() shared.TimedSummary {
	sort.Float64s(sample.rates)
	return shared.TimedSummary{
		N:        sample.n,
		Tokens:   sample.tokens,
		Seconds:  shared.Round(sample.seconds, 3),
		Weighted: shared.R1(shared.WeightedRate(sample.tokens, sample.seconds)),
		Median:   shared.R1(shared.MedianOf(sample.rates)),
		P10:      shared.R1(shared.PercentileOf(sample.rates, 10)),
		P25:      shared.R1(shared.PercentileOf(sample.rates, 25)),
		P75:      shared.R1(shared.PercentileOf(sample.rates, 75)),
		P90:      shared.R1(shared.PercentileOf(sample.rates, 90)),
		P99:      shared.R1(shared.PercentileOf(sample.rates, 99)),
		Max:      maxRate(sample.rates),
	}
}

func maxRate(sorted []float64) *float64 {
	if len(sorted) == 0 {
		return nil
	}
	value := shared.R1(&sorted[len(sorted)-1])
	return value
}

// modelAggregate is the windowed per-model aggregate.
type modelAggregate struct {
	model      shared.ModelStat
	steps      int64
	toolSteps  int64
	generated  int64
	inputTotal int64
	cacheRead  int64
	cacheWrite int64
	cost       float64
	timed      timedSample
}

// familyAggregate is the windowed per-family aggregate.
type familyAggregate struct {
	family     string
	models     map[string]struct{}
	steps      int64
	toolSteps  int64
	generated  int64
	inputTotal int64
	cacheRead  int64
	cacheWrite int64
	cost       float64
	timed      timedSample
}

func (aggregate *familyAggregate) addModel(model shared.ModelStat) {
	if aggregate.models == nil {
		aggregate.models = make(map[string]struct{})
	}
	aggregate.models[model.Model] = struct{}{}
}

// callAggregate mirrors the report builders' equal-share attribution.
type callAggregate struct {
	name           string
	calls          int64
	steps          map[string]struct{}
	generated      float64
	cost           float64
	timedCalls     int64
	timedGenerated float64
	timedSeconds   float64
	rates          []float64
	allocated      float64
	errors         int64
}

func newCallAggregate(name string) *callAggregate {
	return &callAggregate{name: name, steps: make(map[string]struct{})}
}

// bucketAggregate is one output-size stratum.
type bucketAggregate struct {
	label   string
	n       int64
	tokens  int64
	seconds float64
	rates   []float64
}

func (bucket *bucketAggregate) add(generated int64, seconds float64) {
	bucket.n++
	bucket.tokens += generated
	bucket.seconds += seconds
	if seconds > 0 {
		bucket.rates = append(bucket.rates, float64(generated)/seconds)
	}
}

func (bucket *bucketAggregate) summary() map[string]any {
	sort.Float64s(bucket.rates)
	return map[string]any{
		"n":        bucket.n,
		"tokens":   bucket.tokens,
		"seconds":  shared.Round(bucket.seconds, 3),
		"weighted": anyOrNil(shared.R1(shared.WeightedRate(bucket.tokens, bucket.seconds))),
		"median":   anyOrNil(shared.R1(shared.MedianOf(bucket.rates))),
		"p10":      anyOrNil(shared.R1(shared.PercentileOf(bucket.rates, 10))),
		"p90":      anyOrNil(shared.R1(shared.PercentileOf(bucket.rates, 90))),
	}
}

// sessionAggregate is the windowed per-session rollup.
type sessionAggregate struct {
	id            string
	steps         int64
	generated     int64
	cost          float64
	firstTs       int64
	lastTs        int64
	seen          bool
	activeSeconds float64
	hasTotal      bool
}

func (session *sessionAggregate) add(step shared.Step) {
	if !session.seen {
		session.firstTs = step.TS
		session.lastTs = step.TS
		session.seen = true
	}
	if step.TS < session.firstTs {
		session.firstTs = step.TS
	}
	if step.TS > session.lastTs {
		session.lastTs = step.TS
	}
	session.steps++
	session.generated += step.Generated
	session.cost += step.CostUsd
	if step.TotalDT != nil {
		session.activeSeconds += *step.TotalDT
		session.hasTotal = true
	}
}

func (session *sessionAggregate) spanMinutes() float64 {
	if !session.seen {
		return 0
	}
	return float64(session.lastTs-session.firstTs) / 60
}

func (session *sessionAggregate) activeMinutes() *float64 {
	if !session.hasTotal {
		return nil
	}
	value := session.activeSeconds / 60
	return &value
}

func (session *sessionAggregate) idleMinutes() *float64 {
	active := session.activeMinutes()
	if active == nil {
		return nil
	}
	value := session.spanMinutes() - *active
	if value < 0 {
		value = 0
	}
	return &value
}

// toolAggregate is one tool's windowed comparison view.
type toolAggregate struct {
	report *shared.Report
	window comparisonWindow
	steps  []shared.Step

	totals        shared.Totals
	timed         timedSample
	cost          float64
	totalSpend    float64
	errorCost     float64
	spendComplete bool

	models   map[string]*modelAggregate
	families map[string]*familyAggregate
	tools    map[string]*callAggregate
	category map[string]*callAggregate
	sessions map[string]*sessionAggregate
	pooled   map[string]*bucketAggregate
}

func aggregate(report *shared.Report, window comparisonWindow) *toolAggregate {
	return aggregateFamilies(report, window, nil)
}

// aggregateFamilies is aggregate restricted to a family set. A non-nil filter
// is used for the like-for-like view; its spend is recorded-only because
// skipped ledger cost cannot be attributed to a model family.
func aggregateFamilies(report *shared.Report, window comparisonWindow, families map[string]struct{}) *toolAggregate {
	steps := windowSteps(report, window)
	agg := &toolAggregate{
		report:        report,
		window:        window,
		steps:         steps,
		models:        make(map[string]*modelAggregate),
		families:      make(map[string]*familyAggregate),
		tools:         make(map[string]*callAggregate),
		category:      make(map[string]*callAggregate),
		sessions:      make(map[string]*sessionAggregate),
		pooled:        make(map[string]*bucketAggregate),
		spendComplete: families == nil,
	}
	sessionSeen := make(map[string]struct{})
	modelSeen := make(map[string]struct{})
	for stepIndex, step := range steps {
		if families != nil {
			if _, ok := families[step.Family]; !ok {
				continue
			}
		}
		agg.totals.Steps++
		if step.Tool {
			agg.totals.ToolSteps++
		}
		agg.totals.GeneratedTokens += step.Generated
		agg.totals.InputTotal += step.InputTotal
		agg.totals.CacheRead += step.CacheRead
		agg.totals.CacheWrite += step.CacheWrite
		agg.totals.ToolCalls += int64(len(step.Calls))
		agg.cost += step.CostUsd
		if step.Timed && step.Duration != nil {
			agg.timed.add(step.Generated, *step.Duration)
		}
		if _, seen := modelSeen[step.Model]; !seen {
			modelSeen[step.Model] = struct{}{}
			agg.totals.Models++
		}
		if _, seen := sessionSeen[step.SessionID]; !seen {
			sessionSeen[step.SessionID] = struct{}{}
			agg.totals.Sessions++
		}

		model := agg.models[step.Model]
		if model == nil {
			stat := shared.ModelStat{Model: step.Model, Family: step.Family}
			for _, candidate := range report.Models {
				if candidate.Model == step.Model {
					stat = candidate
					break
				}
			}
			model = &modelAggregate{model: stat}
			agg.models[step.Model] = model
		}
		model.steps++
		model.generated += step.Generated
		model.inputTotal += step.InputTotal
		model.cacheRead += step.CacheRead
		model.cacheWrite += step.CacheWrite
		model.cost += step.CostUsd
		if step.Tool {
			model.toolSteps++
		}
		if step.Timed && step.Duration != nil {
			model.timed.add(step.Generated, *step.Duration)
		}

		family := agg.families[step.Family]
		if family == nil {
			family = &familyAggregate{family: step.Family, models: make(map[string]struct{})}
			agg.families[step.Family] = family
		}
		family.models[step.Model] = struct{}{}
		family.steps++
		family.generated += step.Generated
		family.inputTotal += step.InputTotal
		family.cacheRead += step.CacheRead
		family.cacheWrite += step.CacheWrite
		family.cost += step.CostUsd
		if step.Tool {
			family.toolSteps++
		}
		if step.Timed && step.Duration != nil {
			family.timed.add(step.Generated, *step.Duration)
		}

		if label, ok := shared.BucketFor(step.Generated); ok && step.Timed && step.Duration != nil {
			bucket := agg.pooled[label]
			if bucket == nil {
				bucket = &bucketAggregate{label: label}
				agg.pooled[label] = bucket
			}
			bucket.add(step.Generated, *step.Duration)
		}

		callCount := len(step.Calls)
		if callCount == 0 {
			callCount = 1
		}
		for _, call := range step.Calls {
			addCall(agg.tools, call.Name, step, callCount, stepIndex)
			addCall(agg.category, call.Category, step, callCount, stepIndex)
		}

		session := agg.sessions[step.SessionID]
		if session == nil {
			session = &sessionAggregate{id: step.SessionID}
			agg.sessions[step.SessionID] = session
		}
		session.add(step)
	}

	// Spend includes skipped/error ledger cost, which is only available per
	// day and cannot be split by model family. The like-for-like scope is
	// therefore recorded-only. An overlap window uses the daily series; a
	// full-range comparison can use the report's exact total, which also
	// carries undated ledger cost.
	if families == nil {
		for _, day := range report.Daily {
			if window.Mode == "overlap" && (day.Date < window.From || day.Date > window.To) {
				continue
			}
			agg.totalSpend += day.TotalSpendUsd
		}
		if window.Mode != "overlap" {
			agg.totalSpend = report.Totals.TotalSpendUsd
		}
		if len(report.Daily) == 0 {
			agg.totalSpend = report.Totals.TotalSpendUsd
		}
	} else {
		agg.totalSpend = agg.cost
	}
	agg.errorCost = agg.totalSpend - agg.cost
	if agg.errorCost < 0 && agg.errorCost > -1e-4 {
		agg.errorCost = 0
	}
	agg.totals.CostUsd = agg.cost
	agg.totals.TotalSpendUsd = agg.totalSpend
	return agg
}

func addCall(index map[string]*callAggregate, name string, step shared.Step, callCount, stepIndex int) {
	stat := index[name]
	if stat == nil {
		stat = newCallAggregate(name)
		index[name] = stat
	}
	stat.calls++
	stat.steps[itoa(int64(stepIndex))] = struct{}{}
	stat.generated += float64(step.Generated) / float64(callCount)
	stat.cost += step.CostUsd / float64(callCount)
	if step.Timed && step.Duration != nil {
		stat.timedCalls++
		stat.timedGenerated += float64(step.Generated) / float64(callCount)
		stat.timedSeconds += *step.Duration / float64(callCount)
		stat.rates = append(stat.rates, float64(step.Generated) / *step.Duration)
	}
}

// sortedCallAggregates returns the call aggregates ordered by calls desc, then
// name for determinism.
func sortedCallAggregates(index map[string]*callAggregate) []*callAggregate {
	items := make([]*callAggregate, 0, len(index))
	for _, stat := range index {
		items = append(items, stat)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].calls != items[j].calls {
			return items[i].calls > items[j].calls
		}
		return items[i].name < items[j].name
	})
	return items
}

// sortedFamilies returns windowed families with shared families first and
// combined generated tokens descending.
func sortedFamilies(oc, cmd *toolAggregate) []string {
	set := make(map[string]struct{})
	for family := range oc.families {
		set[family] = struct{}{}
	}
	for family := range cmd.families {
		set[family] = struct{}{}
	}
	families := make([]string, 0, len(set))
	for family := range set {
		families = append(families, family)
	}
	sort.SliceStable(families, func(i, j int) bool {
		iShared := familyShared(oc, cmd, families[i])
		jShared := familyShared(oc, cmd, families[j])
		if iShared != jShared {
			return iShared
		}
		iTokens := familyGenerated(oc, families[i]) + familyGenerated(cmd, families[i])
		jTokens := familyGenerated(oc, families[j]) + familyGenerated(cmd, families[j])
		if iTokens != jTokens {
			return iTokens > jTokens
		}
		return families[i] < families[j]
	})
	return families
}

func familyShared(oc, cmd *toolAggregate, family string) bool {
	return len(aggModelIDs(oc, family)) > 0 && len(aggModelIDs(cmd, family)) > 0
}

func familyGenerated(agg *toolAggregate, family string) int64 {
	if item := agg.families[family]; item != nil {
		return item.generated
	}
	return 0
}

// aggModelIDs returns the sorted windowed model ids of a family in agg.
func aggModelIDs(agg *toolAggregate, family string) []string {
	item := agg.families[family]
	if item == nil || len(item.models) == 0 {
		return []string{}
	}
	ids := make([]string, 0, len(item.models))
	for id := range item.models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// modelIDs returns the sorted model ids of a family in a report.
func modelIDs(report *shared.Report, family string) []string {
	ids := make([]string, 0, 2)
	for _, model := range report.Models {
		if model.Family == family {
			ids = append(ids, model.Model)
		}
	}
	sort.Strings(ids)
	return ids
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	buffer := make([]byte, 0, 20)
	for value > 0 {
		buffer = append(buffer, byte('0'+value%10))
		value /= 10
	}
	for i, j := 0, len(buffer)-1; i < j; i, j = i+1, j-1 {
		buffer[i], buffer[j] = buffer[j], buffer[i]
	}
	if negative {
		return "-" + string(buffer)
	}
	return string(buffer)
}
