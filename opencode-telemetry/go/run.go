// SPDX-License-Identifier: AGPL-3.0-only

// Package octelemetry is the OpenCode command-line front end.
//
// It owns flag parsing, the build call, the report writer handoff, the summary
// text and the optional local server. All schema math lives in
// internal/report/opencode and all source reading in
// internal/source/opencode, so this package never touches a metric.
package octelemetry

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/jadmadi/agentmeter/internal/emit"
	ocreport "github.com/jadmadi/agentmeter/internal/report/opencode"
	ocsource "github.com/jadmadi/agentmeter/internal/source/opencode"
	"github.com/jadmadi/agentmeter/internal/terminal"
	ocweb "github.com/jadmadi/agentmeter/opencode-telemetry/web"
)

// DefaultOutDir is CWD-relative because a binary cannot assume a source tree.
const DefaultOutDir = "./data/oc"

// GlobalName is the JS bundle global the renderer reads.
const GlobalName = "OPENCODE_TELEMETRY"

// usage is printed for --help and for a malformed invocation.
const usage = `OpenCode telemetry: build a speed, token and cost report from the
OpenCode v2 SQLite store (session_v2 / session_message).

Usage:
  agentmeter oc [flags]

Flags:
`

// Run executes the OpenCode telemetry command and returns its process exit
// code: 0 on success, 1 on a build or write failure, 2 on a flag error.
func Run(args []string) int {
	return run(args, os.Stdout, os.Stderr)
}

// run is Run with injectable streams so the CLI is testable.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("oc", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}

	dbPath := fs.String("db", ocsource.DefaultDBPath(), "OpenCode database")
	models := fs.String("models", "", "comma-separated model filters")
	since := fs.String("since", "", "earliest local date, YYYY-MM-DD")
	until := fs.String("until", "", "latest local date, YYYY-MM-DD")
	outDir := fs.String("out", DefaultOutDir, "output directory")
	noTitles := fs.Bool("no-titles", false, "omit session titles")
	fullPaths := fs.Bool("full-paths", false, "keep full project paths")
	baseline := fs.String("baseline", "", "pace-index baseline window, YYYY-MM-DD..YYYY-MM-DD")
	minSeconds := fs.Float64("min-seconds", 0.5, "minimum LLM seconds")
	maxSeconds := fs.Float64("max-seconds", 300.0, "maximum LLM seconds")
	minTokens := fs.Int64("min-tokens", 20, "minimum generated tokens")
	minRate := fs.Float64("min-rate", 3.0, "minimum generated tokens/s")
	maxRate := fs.Float64("max-rate", 400.0, "maximum generated tokens/s")
	serve := fs.Bool("serve", false, "build once, serve the report, and open it")
	watch := fs.Bool("watch", false, "watch the store, rebuild, and serve (implies --serve)")
	port := fs.Int("port", 8000, "port for --serve")
	noOpen := fs.Bool("no-open", false, "do not open a browser with --serve")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "agentmeter oc: invalid flags")
		fs.Usage()
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "agentmeter oc: unexpected argument %q\n", fs.Arg(0))
		fs.Usage()
		return 2
	}
	if *watch {
		*serve = true
	}

	thresholds := ocreport.Thresholds{
		MinSeconds: *minSeconds,
		MaxSeconds: *maxSeconds,
		MinTokens:  *minTokens,
		MinRate:    *minRate,
		MaxRate:    *maxRate,
	}
	buildReport := func() (map[string]any, string, string, string, error) {
		data, err := ocreport.Build(ocreport.Options{
			DBPath:        *dbPath,
			ModelFilters:  splitModels(*models),
			Since:         *since,
			Until:         *until,
			IncludeTitles: !*noTitles,
			FullPaths:     *fullPaths,
			Thresholds:    &thresholds,
			Baseline:      *baseline,
		})
		if err != nil {
			return nil, "", "", "", err
		}
		jsonPath, jsPath, htmlPath, err := emit.WriteReport(*outDir, data, ocweb.IndexHTML, GlobalName)
		return data, jsonPath, jsPath, htmlPath, err
	}

	data, jsonPath, jsPath, htmlPath, err := buildReport()
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	printSummary(stdout, data, jsonPath, jsPath, htmlPath)

	if *watch {
		err := emit.Watch(
			filepath.Dir(htmlPath),
			*port,
			!*noOpen,
			0,
			func() string { return ocsource.Fingerprint(*dbPath) },
			func() error {
				_, _, _, _, err := buildReport()
				return err
			},
		)
		if err != nil {
			fmt.Fprintf(stderr, "agentmeter oc: watch: %v\n", err)
			return 1
		}
		return 0
	}
	if *serve {
		if err := emit.Serve(filepath.Dir(htmlPath), *port, !*noOpen); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "Open %s in a browser, or re-run with --serve.\n", htmlPath)
	return 0
}

// splitModels parses the comma-separated --models filter.
func splitModels(list string) []string {
	parts := strings.Split(list, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// printSummary renders a compact, unit-explicit terminal summary.
func printSummary(w io.Writer, data map[string]any, jsonPath, jsPath, htmlPath string) {
	meta := object(data, "meta")
	totals := object(data, "totals")
	timed := object(data, "timed")
	cache := object(data, "cache")
	cost := object(data, "cost")
	timing := object(meta, "timing")

	count := func(value any) string { return terminal.Count(int64(numberOf(value))) }
	compact := func(value any) string { return terminal.Compact(numberOf(value)) }

	steps := numberOf(totals["steps"])
	timedN := numberOf(object(timed, "all")["n"])
	percent := 0.0
	if steps > 0 {
		percent = timedN / steps * 100
	}
	terminal.Title(w, "AgentMeter · OpenCode")
	fmt.Fprintln(w)
	terminal.Line(w, "SOURCE", fmt.Sprintf("%s assistant steps · %s sessions · %s models",
		count(meta["stepsScanned"]), count(meta["sessions"]), count(meta["models"])))
	rows := fmt.Sprintf("%s steps · %s timed (%.1f%%) · %s empty · %s fork copies",
		count(totals["steps"]), count(timedN), percent, count(timing["empty"]), count(meta["duplicatesSkipped"]))
	if collisions := int64(numberOf(meta["fingerprintCollisions"])); collisions > 0 {
		rows += fmt.Sprintf(" · %s collisions", terminal.Count(collisions))
	}
	terminal.Line(w, "ROWS", rows)

	reasoningShare, _ := floatOf(totals["reasoningShare"])
	terminal.Line(w, "TOKENS", fmt.Sprintf("%s generated · %s visible · %s reasoning (%.1f%%)",
		compact(totals["generatedTokens"]), compact(totals["outputTokens"]),
		compact(totals["reasoningTokens"]), reasoningShare*100))

	timedAll := object(timed, "all")
	if timedN > 0 {
		terminal.Line(w, "SPEED", fmt.Sprintf("%s tok/s weighted · %s tok/s median · %s tok/s p90 · %s tok/s visible median",
			text(timedAll["weighted"]), text(timedAll["median"]), text(timedAll["p90"]), text(timedAll["visibleMedian"])))
	}
	timedText := object(timed, "text")
	if numberOf(timedText["n"]) > 0 {
		terminal.Line(w, "FINAL", fmt.Sprintf("%s tok/s weighted · %s replies · %s tok/s median · %s tok/s p90",
			text(timedText["weighted"]), count(timedText["n"]),
			text(timedText["median"]), text(timedText["p90"])))
	}
	runtimeSection := object(data, "runtime")
	if numberOf(runtimeSection["steps"]) > 0 {
		modelShare, _ := floatOf(runtimeSection["modelTimeShare"])
		toolShare, _ := floatOf(runtimeSection["weightedShare"])
		terminal.Line(w, "CLOCK", fmt.Sprintf("model %.0f%% · tools %.0f%% · tool p90 %ss",
			modelShare*100, toolShare*100, text(runtimeSection["p90ToolSeconds"])))
	}
	terminal.Line(w, "COST", fmt.Sprintf("$%s recorded · ~$%s cache savings · $%s/step",
		money(numberOf(cost["recorded"])), money(numberOf(cache["savings"])),
		text(cost["perStep"])))
	if hitRate, ok := floatOf(cache["hitRate"]); ok {
		terminal.Line(w, "CACHE", fmt.Sprintf("%.1f%% read · %.2f%% write",
			hitRate*100, numberOf(cache["writeShare"])*100))
	}

	models := topModels(data, 6)
	if len(models) > 0 {
		terminal.Section(w, fmt.Sprintf("MODELS (top %d of %s, by steps)",
			len(models), count(meta["models"])))
		tableRows := make([][]string, 0, len(models))
		for _, item := range models {
			fixed := text(item["fixedSeconds"])
			if fixed != "-" {
				fixed += "s"
			}
			reas := "-"
			if share, ok := floatOf(item["reasoningShare"]); ok {
				reas = fmt.Sprintf("%.0f%%", share*100)
			}
			tableRows = append(tableRows, []string{
				modelName(item),
				count(item["steps"]),
				text(item["weighted"]),
				text(item["median"]),
				text(item["p90"]),
				text(item["visibleMedian"]),
				text(item["pureRate"]),
				fixed,
				reas,
			})
		}
		terminal.Table(w,
			[]string{"MODEL", "STEPS", "GEN/S", "MED", "P90", "VIS", "PURE", "FIXED", "REAS"},
			tableRows,
			[]bool{false, true, true, true, true, true, true, true, true},
			[]int{28, 7, 6, 6, 6, 5, 6, 7, 5})
		terminal.Note(w, "gen/s weighted tok/s · med/p90 generated tok/s")
		terminal.Note(w, "vis visible median · pure marginal tok/s · fixed model seconds")
		terminal.Note(w, "reas reasoning share of generated tokens")
	}

	terminal.Section(w, "REPORT")
	fmt.Fprintf(w, "  %s\n", htmlPath)
	fmt.Fprintf(w, "  %s\n", jsonPath)
	fmt.Fprintf(w, "  %s\n", jsPath)
}

// topModels returns the byModel entries with the most steps, first six.
func modelName(item map[string]any) string {
	if name := stringOf(item["short"]); name != "" {
		return name
	}
	if name := stringOf(item["model"]); name != "" {
		return name
	}
	return "(unknown)"
}

func topModels(data map[string]any, limit int) []map[string]any {
	entries := list(data, "byModel")
	out := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		if object, ok := entry.(map[string]any); ok {
			out = append(out, object)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return numberOf(out[i]["steps"]) > numberOf(out[j]["steps"])
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// object returns a nested object, tolerating a missing or mistyped section.
func object(data map[string]any, key string) map[string]any {
	if value, ok := data[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

// list returns a nested list, tolerating a missing section.
func list(data map[string]any, key string) []any {
	if value, ok := data[key].([]any); ok {
		return value
	}
	return nil
}

// numberOf converts a schema number to float64 (0 when missing).
func numberOf(value any) float64 {
	converted, _ := floatOf(value)
	return converted
}

// floatOf converts a schema number to float64.
func floatOf(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case int32:
		return float64(v), true
	case uint64:
		return float64(v), true
	default:
		return 0, false
	}
}

// number renders a schema number without a trailing ".0".
func number(value any) string {
	switch v := value.(type) {
	case nil:
		return "0"
	case int:
		return fmt.Sprintf("%d", v)
	case int64:
		return fmt.Sprintf("%d", v)
	case int32:
		return fmt.Sprintf("%d", v)
	case uint64:
		return fmt.Sprintf("%d", v)
	case float64:
		return trimFloat(v)
	case float32:
		return trimFloat(float64(v))
	case string:
		return v
	default:
		return fmt.Sprintf("%v", v)
	}
}

// text renders an optional number the way Python prints it, with "-" for null.
func text(value any) string {
	if value == nil {
		return "-"
	}
	return number(value)
}

// grouped renders a count with thousands separators (Python's `,`).
func grouped(value any) string {
	return groupThousands(number(value))
}

// groupThousands inserts "," separators into the integer part of a number.
func groupThousands(raw string) string {
	negative := strings.HasPrefix(raw, "-")
	if negative {
		raw = raw[1:]
	}
	dot := strings.IndexByte(raw, '.')
	whole := raw
	fraction := ""
	if dot >= 0 {
		whole = raw[:dot]
		fraction = raw[dot:]
	}
	var b strings.Builder
	for i, digit := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(digit)
	}
	out := b.String() + fraction
	if negative {
		return "-" + out
	}
	return out
}

// money renders a cost with two decimals and thousands separators, like
// Python's f"${value:,.2f}".
func money(value float64) string {
	return groupThousands(fmt.Sprintf("%.2f", value))
}

// trimFloat prints a float the way Python's str() prints a summary value.
func trimFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// stringOf renders a string field.
func stringOf(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

// truncate keeps the first limit characters (the Python `model[:44]` slice).
func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
