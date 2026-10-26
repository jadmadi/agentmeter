// SPDX-License-Identifier: AGPL-3.0-only

package ocreport

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jadmadi/agentmeter/internal/contract"
	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
	"github.com/jadmadi/agentmeter/internal/stats"
)

// fitKey identifies one per-model regression.
type fitKey struct {
	model string
	kind  string
}

// priceFit is the fitted $/1M rate set for one model.
type priceFit struct {
	InputRate      float64
	CacheReadRate  float64
	CacheWriteRate float64
	OutputRate     float64
	R2             *float64
	N              int
}

// baselineInfo is meta.paceBaseline.
type baselineInfo struct {
	From     string
	To       string
	PaceBase *float64
	N        int
}

// builder holds the state shared by the section builders.
type builder struct {
	opts Options
	th   Thresholds
	now  time.Time

	loaded *ocsource.Loaded
	rows   []*ocsource.Row
	ledger []*ocsource.LedgerEntry

	timed     []*ocsource.Row
	text      []*ocsource.Row
	toolTimed []*ocsource.Row

	models        []string
	modelShort    map[string]string
	modelProvider map[string]string

	fits        map[fitKey]*stats.Fit
	globalFits  map[string]*stats.Fit
	regressions []map[string]any

	projectLabels map[string]string
	projectPaths  []string

	paceBase     *float64
	paceBaseline *baselineInfo

	timingCounts map[string]int

	modelLedger   map[string][]*ocsource.LedgerEntry
	projectLedger map[string][]*ocsource.LedgerEntry
}

// Build reconstructs the full schema v3 OpenCode report.
//
// The report is validated with the shared contract before it is returned; a
// violation is an error and no partial data escapes (fail closed, like the
// Python builder's SystemExit).
func Build(opts Options) (map[string]any, error) {
	b, err := newBuilder(opts)
	if err != nil {
		return nil, err
	}
	data := b.report()
	if issues := contract.ValidateOC(data); len(issues) > 0 {
		return nil, fmt.Errorf("telemetry invariant failure:\n- %s", strings.Join(issues, "\n- "))
	}
	return data, nil
}

func newBuilder(opts Options) (*builder, error) {
	th := opts.effectiveThresholds()
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	b := &builder{
		opts:          opts,
		th:            th,
		now:           now,
		modelShort:    map[string]string{},
		modelProvider: map[string]string{},
		fits:          map[fitKey]*stats.Fit{},
		globalFits:    map[string]*stats.Fit{},
		modelLedger:   map[string][]*ocsource.LedgerEntry{},
		projectLedger: map[string][]*ocsource.LedgerEntry{},
	}

	baselineFrom, baselineTo, err := parseBaseline(opts.Baseline)
	if err != nil {
		return nil, err
	}

	loaded, err := ocsource.Load(ocsource.LoadOptions{
		DBPath:        opts.DBPath,
		ModelFilters:  opts.ModelFilters,
		Since:         opts.Since,
		Until:         opts.Until,
		IncludeTitles: opts.IncludeTitles,
		FullPaths:     opts.FullPaths,
	})
	if err != nil {
		return nil, err
	}
	b.loaded = loaded

	b.rows = make([]*ocsource.Row, len(loaded.Rows))
	for i := range loaded.Rows {
		b.rows[i] = &loaded.Rows[i]
	}
	b.ledger = make([]*ocsource.LedgerEntry, len(loaded.Ledger))
	for i := range loaded.Ledger {
		b.ledger[i] = &loaded.Ledger[i]
	}

	// timing buckets are assigned before anything reads them.
	for _, row := range b.rows {
		row.TimingBucket = th.Bucket(row)
	}
	b.timed = make([]*ocsource.Row, 0, len(b.rows))
	b.text = make([]*ocsource.Row, 0, len(b.rows))
	b.toolTimed = make([]*ocsource.Row, 0, len(b.rows))
	for _, row := range b.rows {
		if !th.IsTimed(row) {
			continue
		}
		b.timed = append(b.timed, row)
		if row.Tool {
			b.toolTimed = append(b.toolTimed, row)
		} else {
			b.text = append(b.text, row)
		}
	}

	b.buildProjectLabels()

	for _, entry := range b.ledger {
		b.modelLedger[entry.Model] = append(b.modelLedger[entry.Model], entry)
		b.projectLedger[entry.Project] = append(b.projectLedger[entry.Project], entry)
	}

	b.buildModels()

	// Per-model regressions, then the global fallbacks.
	for _, model := range b.models {
		rs := filterRows(b.timed, func(row *ocsource.Row) bool { return row.Model == model })
		fitAll := fitRate(rs)
		fitText := fitRate(filterRows(rs, func(row *ocsource.Row) bool { return !row.Tool }))
		fitTool := fitRate(filterRows(rs, func(row *ocsource.Row) bool { return row.Tool }))
		if fitAll != nil {
			b.fits[fitKey{model, "all"}] = fitAll
		}
		if fitText != nil {
			b.fits[fitKey{model, "text"}] = fitText
		}
		if fitTool != nil {
			b.fits[fitKey{model, "tool"}] = fitTool
		}
		b.regressions = append(b.regressions, obj(
			"model", model,
			"short", b.modelShort[model],
			"all", fitMap(fitAll),
			"text", fitMap(fitText),
			"tool", fitMap(fitTool),
		))
	}
	b.globalFits["text"] = fitRate(b.text)
	b.globalFits["tool"] = fitRate(b.toolTimed)

	for _, row := range b.timed {
		row.Ratio = b.expectedRatio(row)
	}

	// Pace baseline: the selected timed rows unless --baseline pins a window.
	baseSource := b.timed
	var baselineRows []*ocsource.Row
	if baselineFrom != "" && baselineTo != "" {
		for _, row := range b.timed {
			day := row.Ts.Format("2006-01-02")
			if baselineFrom <= day && day <= baselineTo {
				baselineRows = append(baselineRows, row)
			}
		}
		baseSource = baselineRows
	}
	ratioValues := make([]float64, 0, len(baseSource))
	for _, row := range baseSource {
		if row.Ratio != nil {
			ratioValues = append(ratioValues, *row.Ratio)
		}
	}
	if value, ok := stats.Median(ratioValues); ok {
		b.paceBase = &value
	}
	if baselineFrom != "" && baselineTo != "" {
		b.paceBaseline = &baselineInfo{
			From:     baselineFrom,
			To:       baselineTo,
			PaceBase: b.paceBase,
			N:        len(ratioValues),
		}
	}

	b.timingCounts = map[string]int{}
	for _, row := range b.rows {
		b.timingCounts[row.TimingBucket]++
	}
	return b, nil
}

// parseBaseline splits --baseline and rejects a malformed window with the same
// message as the Python builder's SystemExit.
func parseBaseline(baseline string) (string, string, error) {
	if baseline == "" {
		return "", "", nil
	}
	if !strings.Contains(baseline, "..") {
		return "", "", fmt.Errorf("--baseline must be YYYY-MM-DD..YYYY-MM-DD")
	}
	parts := strings.SplitN(baseline, "..", 2)
	return parts[0], parts[1], nil
}

// buildProjectLabels resolves the disambiguated display labels used by every
// project-facing section.
func (b *builder) buildProjectLabels() {
	seenRows := map[string]bool{}
	rowPaths := make([]string, 0, len(b.rows))
	seenAll := map[string]bool{}
	allPaths := make([]string, 0, len(b.rows)+len(b.ledger))
	for _, row := range b.rows {
		if !seenRows[row.Project] {
			seenRows[row.Project] = true
			rowPaths = append(rowPaths, row.Project)
		}
		if !seenAll[row.Project] {
			seenAll[row.Project] = true
			allPaths = append(allPaths, row.Project)
		}
	}
	for _, entry := range b.ledger {
		if !seenAll[entry.Project] {
			seenAll[entry.Project] = true
			allPaths = append(allPaths, entry.Project)
		}
	}
	sort.Strings(rowPaths)
	sort.Strings(allPaths)
	b.projectPaths = rowPaths

	baseCounts := map[string]int{}
	for _, path := range allPaths {
		baseCounts[pyBasename(path)]++
	}
	b.projectLabels = make(map[string]string, len(allPaths))
	used := map[string]bool{}
	for _, path := range allPaths {
		base := pyBasename(path)
		var label string
		switch {
		case b.opts.FullPaths:
			label = path
		case baseCounts[base] == 1:
			label = base
		default:
			parent := pyBasename(pyDirname(strings.TrimRight(path, "/")))
			if parent != "" {
				label = base + " · " + parent
			} else {
				label = base
			}
		}
		if used[label] {
			label = path
		}
		used[label] = true
		b.projectLabels[path] = label
	}
	for _, row := range b.rows {
		row.ProjectLabel = b.projectLabels[row.Project]
	}
	for _, entry := range b.ledger {
		entry.ProjectLabel = b.projectLabels[entry.Project]
	}
}

// buildModels collects the sorted model list and the first-seen short name and
// provider, exactly like model_meta.setdefault.
func (b *builder) buildModels() {
	seen := map[string]bool{}
	for _, row := range b.rows {
		if seen[row.Model] {
			continue
		}
		seen[row.Model] = true
		b.models = append(b.models, row.Model)
	}
	sort.Strings(b.models)
	for _, row := range b.rows {
		if _, ok := b.modelShort[row.Model]; !ok {
			b.modelShort[row.Model] = row.Short
			b.modelProvider[row.Model] = row.Provider
		}
	}
}

// filterRows keeps rows matching keep, preserving order.
func filterRows(rows []*ocsource.Row, keep func(*ocsource.Row) bool) []*ocsource.Row {
	out := make([]*ocsource.Row, 0, len(rows))
	for _, row := range rows {
		if keep(row) {
			out = append(out, row)
		}
	}
	return out
}

// fitRate mirrors telemetry.py:fit_rate, including the usable flag.
func fitRate(rows []*ocsource.Row) *stats.Fit {
	xs := make([]float64, len(rows))
	ys := make([]float64, len(rows))
	for i, row := range rows {
		xs[i] = float64(row.Gen)
		ys[i] = *row.DT
	}
	fit, ok := stats.LinearFit(xs, ys, 30)
	if !ok {
		return nil
	}
	return &fit
}

// fitR2 is `fit["r2"] or 0`.
func fitR2(fit *stats.Fit) float64 {
	if fit == nil || !fit.HasR2 {
		return 0
	}
	return fit.R2
}

// fitUsable is `n >= 30 and (r2 or 0) >= 0.50`.
func fitUsable(fit *stats.Fit) bool {
	return fit != nil && fit.N >= 30 && fitR2(fit) >= 0.50
}

// fitMap renders a fit exactly like linear_fit's dictionary plus `usable`.
func fitMap(fit *stats.Fit) any {
	if fit == nil {
		return nil
	}
	var r2 any
	if fit.HasR2 {
		r2 = fit.R2
	}
	return obj(
		"fixedSeconds", fit.FixedSeconds,
		"pureRate", fit.PureRate,
		"r2", r2,
		"n", fit.N,
		"interceptConstrained", fit.InterceptConstrained,
		"usable", fitUsable(fit),
	)
}

// expectedRatio mirrors expected_ratio: the first usable fit in
// model/kind, model/all, global/kind order.
func (b *builder) expectedRatio(row *ocsource.Row) *float64 {
	kind := "text"
	if row.Tool {
		kind = "tool"
	}
	candidates := []*stats.Fit{
		b.fits[fitKey{row.Model, kind}],
		b.fits[fitKey{row.Model, "all"}],
		b.globalFits[kind],
	}
	var fit *stats.Fit
	for _, candidate := range candidates {
		if candidate != nil && fitUsable(candidate) {
			fit = candidate
			break
		}
	}
	if fit == nil || row.DT == nil {
		return nil
	}
	expected := fit.FixedSeconds + float64(row.Gen)/fit.PureRate
	if expected <= 0 {
		return nil
	}
	value := expected / *row.DT
	return &value
}

// paceIndexValues is pace_index over a sample that may contain missing values.
func (b *builder) paceIndexValues(values []float64) any {
	if len(values) == 0 || b.paceBase == nil || *b.paceBase == 0 {
		return nil
	}
	value, ok := stats.Median(values)
	if !ok {
		return nil
	}
	return stats.PyRound(value / *b.paceBase * 100, 1)
}

// ratiosOf collects the non-nil ratios of rows.
func ratiosOf(rows []*ocsource.Row) []float64 {
	out := make([]float64, 0, len(rows))
	for _, row := range rows {
		if row.Ratio != nil {
			out = append(out, *row.Ratio)
		}
	}
	return out
}

// pyBasename is os.path.basename(p.rstrip("/")).
func pyBasename(p string) string {
	trimmed := strings.TrimRight(p, "/")
	if index := strings.LastIndexByte(trimmed, '/'); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}

// pyDirname is os.path.dirname.
func pyDirname(p string) string {
	index := strings.LastIndexByte(p, '/')
	if index < 0 {
		return ""
	}
	head := strings.TrimRight(p[:index], "/")
	if head == "" {
		return "/"
	}
	return head
}

// localNowLabel is local_tz_label(): "UTC±HH:MM" for the current offset.
func localNowLabel(now time.Time) string {
	_, offset := now.Zone()
	minutes := int(math.Floor(float64(offset) / 60.0))
	sign := "+"
	if minutes < 0 {
		sign = "-"
		minutes = -minutes
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, minutes/60, minutes%60)
}

// dbLabel mirrors `db_path.replace(os.path.expanduser("~"), "~")`.
func dbLabel(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	return strings.ReplaceAll(path, home, "~")
}
