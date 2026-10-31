// SPDX-License-Identifier: AGPL-3.0-only

// Package cli is the `agentmeter compare` front end: flag parsing, input
// resolution, the build/write cycle, the terminal summary, and the optional
// local server. All aggregation lives in internal/report/compare.
package cli

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
	"time"

	shared "github.com/jadmadi/agentmeter/internal/compare"
	compareweb "github.com/jadmadi/agentmeter/internal/compare/web"
	"github.com/jadmadi/agentmeter/internal/emit"
	"github.com/jadmadi/agentmeter/internal/pyjson"
	reportcompare "github.com/jadmadi/agentmeter/internal/report/compare"
	"github.com/jadmadi/agentmeter/internal/terminal"
)

// DefaultOutDir is CWD-relative, like the per-tool defaults.
const DefaultOutDir = "./data/compare"

// GlobalName is the JS bundle global the comparison renderer reads.
const GlobalName = "COMPARISON_TELEMETRY"

const usage = `agentmeter compare: side-by-side OpenCode vs Command Code analytics.

Inputs are pre-built schema v3 reports, report directories, or the tools'
own live sources (an OpenCode SQLite store / a Command Code session root).
--oc and --cmd accept an optional value; omit it to use that tool's default.

Usage:
  agentmeter compare [flags]
  agentmeter compare [<oc-source>] [<cmd-source>]
  agentmeter compare oc-cmd

Examples:
  agentmeter compare                          compare default live sources
  agentmeter compare oc-cmd                   same, explicit pair alias
  agentmeter compare data/oc data/cmd         compare two report directories
  agentmeter compare --oc data/oc --cmd data/cmd
  agentmeter compare --oc --cmd               use both defaults
  agentmeter compare --serve

Flags:
`

// pairAliases are accepted as a single positional token meaning "use both
// defaults". A real path with the same name wins over the alias.
var pairAliases = map[string]struct{}{
	"oc-cmd":    {},
	"oc-vs-cmd": {},
	"oc_vs_cmd": {},
	"oc+cmd":    {},
	"both":      {},
}

// optionalValueFlags may be given without a value and fall back to the tool
// default; requiredValueFlags must carry one.
var optionalValueFlags = map[string]struct{}{"oc": {}, "cmd": {}}

var requiredValueFlags = map[string]struct{}{"out": {}, "port": {}}

// Run executes the compare subcommand and returns its exit code.
func Run(args []string) int {
	return run(args, os.Stdout, os.Stderr)
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), usage)
		fs.PrintDefaults()
	}

	ocPath := fs.String("oc", "", "OpenCode report file/dir or SQLite store (default: autodetected store)")
	cmdPath := fs.String("cmd", reportcompare.DefaultCMDPath, "Command Code report file/dir or session root")
	outDir := fs.String("out", DefaultOutDir, "output directory")
	serve := fs.Bool("serve", false, "build once, serve the comparison, and open it")
	watch := fs.Bool("watch", false, "watch both sources, rebuild, and serve (implies --serve)")
	port := fs.Int("port", 8000, "port for --serve")
	noOpen := fs.Bool("no-open", false, "do not open a browser with --serve")
	allModels := fs.Bool("all-models", false, "use all models for the headline and verdicts (default: shared families)")
	jsonOut := fs.Bool("json", false, "print comparison-data.json to stdout instead of the human summary")
	noColor := fs.Bool("no-color", false, "disable ANSI color")

	normalized, err := normalizeArgs(args)
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		fs.Usage()
		return 2
	}
	if err := fs.Parse(normalized); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		fmt.Fprintln(stderr, "agentmeter compare: invalid flags")
		fs.Usage()
		return 2
	}
	set := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if err := applyPositionals(fs.Args(), ocPath, cmdPath, set); err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		fs.Usage()
		return 2
	}
	if *watch {
		*serve = true
	}

	ocInput, err := reportcompare.Detect(shared.ToolOC, *ocPath)
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		return 1
	}
	cmdInput, err := reportcompare.Detect(shared.ToolCMD, *cmdPath)
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		return 1
	}

	buildReport := func() (map[string]any, string, string, string, error) {
		ocReport, err := reportcompare.Load(ocInput)
		if err != nil {
			return nil, "", "", "", err
		}
		cmdReport, err := reportcompare.Load(cmdInput)
		if err != nil {
			return nil, "", "", "", err
		}
		doc, err := reportcompare.Build(ocReport, cmdReport, time.Now())
		if err != nil {
			return nil, "", "", "", err
		}
		jsonPath, jsPath, htmlPath, err := emit.WriteReportFiles(*outDir, emit.ComparisonFiles, doc, compareweb.IndexHTML, GlobalName)
		return doc, jsonPath, jsPath, htmlPath, err
	}

	doc, jsonPath, jsPath, htmlPath, err := buildReport()
	if err != nil {
		fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
		return 1
	}
	if *noColor {
		previous := os.Getenv("NO_COLOR")
		_ = os.Setenv("NO_COLOR", "1")
		defer func() {
			if previous == "" {
				_ = os.Unsetenv("NO_COLOR")
			} else {
				_ = os.Setenv("NO_COLOR", previous)
			}
		}()
	}
	if *jsonOut {
		raw, err := pyjson.JSONBytes(doc)
		if err != nil {
			fmt.Fprintf(stderr, "agentmeter compare: encode JSON: %v\n", err)
			return 1
		}
		if _, err := stdout.Write(raw); err != nil {
			return 1
		}
		if _, err := stdout.Write([]byte("\n")); err != nil {
			return 1
		}
	} else {
		scope := "shared"
		if *allModels || !likeForLikeAvailable(doc) {
			scope = "all"
		}
		printSummary(stdout, doc, jsonPath, jsPath, htmlPath, scope)
	}

	if *watch {
		err := emit.WatchFiles(
			filepath.Dir(htmlPath),
			emit.ComparisonFiles,
			*port,
			!*noOpen,
			0,
			func() string {
				return reportcompare.Fingerprint(ocInput) + "\x00" + reportcompare.Fingerprint(cmdInput)
			},
			func() error {
				_, _, _, _, err := buildReport()
				return err
			},
		)
		if err != nil {
			fmt.Fprintf(stderr, "agentmeter compare: watch: %v\n", err)
			return 1
		}
		return 0
	}
	if *serve {
		if err := emit.ServeFiles(filepath.Dir(htmlPath), emit.ComparisonFiles, *port, !*noOpen); err != nil {
			fmt.Fprintf(stderr, "agentmeter compare: %v\n", err)
			return 1
		}
		return 0
	}
	if !*jsonOut {
		fmt.Fprintln(stdout, "Hint: re-run with --serve to open it.")
	}
	return 0
}

func likeForLikeAvailable(doc map[string]any) bool {
	likeForLike, ok := doc["likeForLike"].(map[string]any)
	if !ok {
		return false
	}
	available, _ := likeForLike["available"].(bool)
	return available
}

// normalizeArgs makes --oc/--cmd values optional and rejects a missing value
// on the flags that truly require one. Go's flag package stops at the first
// positional, so flags are collected first and positionals are moved behind a
// "--" terminator. This lets both `compare data/oc data/cmd --serve` and
// `compare --oc --cmd` work.
func normalizeArgs(args []string) ([]string, error) {
	flags := make([]string, 0, len(args))
	positionals := make([]string, 0, 2)
	afterTerminator := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if afterTerminator {
			positionals = append(positionals, arg)
			continue
		}
		if arg == "--" {
			afterTerminator = true
			continue
		}
		name, _, hasValue := splitFlag(arg)
		canonical := strings.TrimLeft(name, "-")
		if _, optional := optionalValueFlags[canonical]; optional {
			if hasValue {
				flags = append(flags, arg)
				continue
			}
			if i+1 < len(args) && !looksLikeFlag(args[i+1]) {
				flags = append(flags, arg, args[i+1])
				i++
				continue
			}
			flags = append(flags, name+"=")
			continue
		}
		if _, required := requiredValueFlags[canonical]; required {
			if hasValue {
				flags = append(flags, arg)
				continue
			}
			if i+1 >= len(args) || looksLikeFlag(args[i+1]) {
				return nil, fmt.Errorf("%s requires a value (use %s=<value>)", displayFlag(name), displayFlag(name))
			}
			flags = append(flags, arg, args[i+1])
			i++
			continue
		}
		if looksLikeFlag(arg) {
			flags = append(flags, arg)
			continue
		}
		positionals = append(positionals, arg)
	}
	if len(positionals) > 0 {
		flags = append(flags, "--")
		flags = append(flags, positionals...)
	}
	return flags, nil
}

func splitFlag(arg string) (name, value string, hasValue bool) {
	if len(arg) < 2 || arg[0] != '-' {
		return arg, "", false
	}
	if index := strings.IndexByte(arg, '='); index >= 0 {
		return arg[:index], arg[index+1:], true
	}
	return arg, "", false
}

func looksLikeFlag(arg string) bool {
	return len(arg) > 1 && arg[0] == '-'
}

func displayFlag(name string) string {
	return "--" + strings.TrimLeft(name, "-")
}

// applyPositionals resolves the optional positional inputs:
//
//	compare                         both defaults
//	compare oc-cmd                  both defaults (pair alias; a real path wins)
//	compare <oc>                    OC source + default cmd
//	compare <oc> <cmd>              both positional sources
func applyPositionals(args []string, ocPath, cmdPath *string, set map[string]bool) error {
	switch len(args) {
	case 0:
		return nil
	case 1:
		arg := args[0]
		if _, alias := pairAliases[strings.ToLower(arg)]; alias && !pathExists(arg) {
			if set["oc"] || set["cmd"] {
				return fmt.Errorf("pair alias %q cannot be combined with --oc/--cmd", arg)
			}
			return nil
		}
		if set["oc"] {
			return fmt.Errorf("OpenCode input given twice (positional %q and --oc)", arg)
		}
		*ocPath = arg
		return nil
	case 2:
		if set["oc"] || set["cmd"] {
			return fmt.Errorf("positional inputs cannot be combined with --oc/--cmd")
		}
		*ocPath, *cmdPath = args[0], args[1]
		return nil
	default:
		return fmt.Errorf("accepts at most two positional inputs (OpenCode, Command Code)")
	}
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// printSummary renders the compact terminal comparison. The headline comes
// from the like-for-like (shared-family) block by default; --all-models shows
// the confounded aggregate instead.
func printSummary(w io.Writer, doc map[string]any, jsonPath, jsPath, htmlPath, scope string) {
	meta := object(doc, "meta")
	tools := object(meta, "tools")
	window := object(meta, "window")
	ocInfo := object(tools, "oc")
	cmdInfo := object(tools, "cmd")

	evidence := doc
	scopeLine := "all models · confounded by model and workload mix"
	if scope == "shared" {
		if likeForLike, ok := doc["likeForLike"].(map[string]any); ok && truthy(likeForLike["available"]) {
			evidence = likeForLike
			totals := object(likeForLike, "totals")
			scopeLine = fmt.Sprintf("like-for-like · %s shared families · %s shared steps",
				count(len(list(likeForLike, "families"))),
				count(int64Value(object(totals, "oc")["steps"])+int64Value(object(totals, "cmd")["steps"])))
		}
	}

	terminal.Title(w, "AgentMeter · OpenCode vs Command Code")
	fmt.Fprintln(w)
	terminal.Line(w, "SOURCE", "oc  "+text(ocInfo["label"]))
	terminal.Line(w, "", "cmd "+text(cmdInfo["label"]))
	mode := text(window["mode"])
	from := text(window["from"])
	to := text(window["to"])
	if from == "-" || to == "-" {
		terminal.Line(w, "WINDOW", fmt.Sprintf("%s · %s days", mode, count(window["days"])))
	} else {
		terminal.Line(w, "WINDOW", fmt.Sprintf("%s · %s → %s · %s days", mode, from, to, count(window["days"])))
	}
	terminal.Line(w, "SCOPE", scopeLine)

	totals := object(doc, "totals")
	ocTotals := object(totals, "oc")
	cmdTotals := object(totals, "cmd")
	terminal.Line(w, "SCALE", fmt.Sprintf("oc %s steps · %s sessions · %s windowed models",
		count(ocTotals["steps"]), count(ocTotals["sessions"]), count(ocTotals["models"])))
	terminal.Line(w, "", fmt.Sprintf("cmd %s steps · %s sessions · %s windowed models",
		count(cmdTotals["steps"]), count(cmdTotals["sessions"]), count(cmdTotals["models"])))

	throughput := object(evidence, "throughput")
	ocSpeed := object(throughput, "oc")
	cmdSpeed := object(throughput, "cmd")
	speedDelta := object(throughput, "delta")
	speedWinner := object(throughput, "winner")
	terminal.Line(w, "SPEED", fmt.Sprintf("weighted oc %s · cmd %s tok/s · %s · %s",
		number(ocSpeed["weighted"]), number(cmdSpeed["weighted"]),
		ratioText(speedDelta["weightedRatio"]), winnerText(speedWinner["weighted"])))
	terminal.Line(w, "", fmt.Sprintf("median   oc %s · cmd %s tok/s · %s · %s",
		number(ocSpeed["median"]), number(cmdSpeed["median"]),
		ratioText(speedDelta["medianRatio"]), winnerText(speedWinner["median"])))

	cost := object(evidence, "cost")
	ocCost := object(cost, "oc")
	cmdCost := object(cost, "cmd")
	costDelta := object(cost, "delta")
	costWinner := object(cost, "winner")
	terminal.Line(w, "COST", fmt.Sprintf("$/1k gen oc $%s · cmd $%s · %s · %s",
		money(ocCost["per1kGenerated"]), money(cmdCost["per1kGenerated"]),
		ratioText(costDelta["per1kGeneratedRatio"]), winnerText(costWinner["per1kGenerated"])))
	terminal.Line(w, "", fmt.Sprintf("$/M input oc $%s · cmd $%s · %s · %s",
		money(ocCost["perMillionInput"]), money(cmdCost["perMillionInput"]),
		ratioText(costDelta["perMillionInputRatio"]), winnerText(costWinner["perMillionInput"])))
	terminal.Line(w, "", fmt.Sprintf("$/step   oc $%s · cmd $%s · %s · %s",
		money(ocCost["perStep"]), money(cmdCost["perStep"]),
		ratioText(costDelta["perStepRatio"]), winnerText(costWinner["perStep"])))
	spendCost := object(doc, "cost")
	ocSpend := object(spendCost, "oc")
	cmdSpend := object(spendCost, "cmd")
	terminal.Line(w, "SPEND", fmt.Sprintf("all-model /session oc $%s · cmd $%s · %s · %s",
		money(ocSpend["totalSpendPerSession"]), money(cmdSpend["totalSpendPerSession"]),
		ratioText(object(spendCost, "delta")["totalSpendPerSessionRatio"]),
		winnerText(object(spendCost, "winner")["totalSpendPerSession"])))

	cache := object(evidence, "cache")
	ocCache := object(cache, "oc")
	cmdCache := object(cache, "cmd")
	terminal.Line(w, "CACHE", fmt.Sprintf("hit oc %s%% · cmd %s%% · %s pp · %s",
		percent2(ocCache["hitRate"]), percent2(cmdCache["hitRate"]),
		signedPp(object(cache, "delta")["hitRatePp"]), winnerText(winner(cache, "hitRate"))))

	families := object(meta, "families")
	models := object(meta, "models")
	terminal.Line(w, "FAMILIES", fmt.Sprintf("%s shared · %s oc-only · %s cmd-only",
		count(families["shared"]), count(families["ocOnly"]), count(families["cmdOnly"])))
	terminal.Line(w, "", fmt.Sprintf("%s oc source models · %s cmd source models · shared families hold %s oc / %s cmd source models",
		count(models["oc"]), count(models["cmd"]), count(models["sharedOC"]), count(models["sharedCMD"])))

	printFamilyTable(w, doc, scope)
	printToolTable(w, evidence)
	printVerdicts(w, doc, scope)

	terminal.Section(w, "REPORT")
	fmt.Fprintf(w, "  %s\n", htmlPath)
	fmt.Fprintf(w, "  %s\n", jsonPath)
	fmt.Fprintf(w, "  %s\n", jsPath)
}

func printFamilyTable(w io.Writer, doc map[string]any, scope string) {
	families := list(object(doc, "models"), "families")
	rows := make([][]string, 0, 8)
	for _, entry := range families {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		sharedFamily := truthy(item["shared"])
		if scope == "shared" && !sharedFamily {
			continue
		}
		oc := object(item, "oc")
		cmd := object(item, "cmd")
		n := object(item, "n")
		delta := object(item, "delta")
		flags := item["flags"]
		flagText := confidenceText(item["confidence"])
		if list, ok := flags.([]any); ok && len(list) > 0 {
			parts := make([]string, 0, len(list))
			for _, flag := range list {
				parts = append(parts, shortFlag(flag))
			}
			flagText = strings.Join(parts, ",")
		}
		rows = append(rows, []string{
			text(item["family"]),
			compactCount(n["oc"]) + "/" + compactCount(n["cmd"]),
			number(oc["weighted"]),
			number(cmd["weighted"]),
			ratioText(delta["weightedRatio"]),
			ratioText(delta["per1kGeneratedRatio"]),
			flagText,
		})
		if len(rows) == 8 {
			break
		}
	}
	if len(rows) == 0 {
		return
	}
	title := "SHARED MODEL FAMILIES (like-for-like)"
	if scope == "all" {
		title = "MODEL FAMILIES (shared first; one-sided marked)"
	}
	terminal.Section(w, title)
	terminal.Table(w,
		[]string{"FAMILY", "N OC/CMD", "OC", "CMD", "SPEED×", "$1K×", "FLAGS"},
		rows,
		[]bool{false, true, true, true, true, true, false},
		[]int{22, 11, 7, 7, 7, 6, 20})
	terminal.Note(w, "N = timed steps (oc/cmd) · OC/CMD = weighted tok/s")
	terminal.Note(w, "ratio = OpenCode / Command Code · speed >1 favors oc · cost >1 favors cmd")
}

func compactCount(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return terminal.Compact(number)
}

func shortFlag(value any) string {
	switch text(value) {
	case "low-sample":
		return "low-n"
	case "cost-outlier":
		return "cost-out"
	case "speed-outlier":
		return "speed-out"
	case "one-sided":
		return "one-side"
	default:
		return text(value)
	}
}

func printToolTable(w io.Writer, evidence map[string]any) {
	toolSection := object(evidence, "tools")
	categories := list(toolSection, "normalized")
	rows, ocCoverage, cmdCoverage := toolShareRows(categories, 6)
	if len(rows) == 0 {
		return
	}
	terminal.Section(w, "TOOL MIX (share of calls)")
	terminal.Table(w,
		[]string{"CATEGORY", "OC %", "CMD %", "Δ pp"},
		rows,
		[]bool{false, true, true, true},
		[]int{28, 8, 8, 8})
	terminal.Note(w, fmt.Sprintf("coverage: oc %.1f%% · cmd %.1f%% (residual shown as other)",
		ocCoverage*100, cmdCoverage*100))
}

func toolShareRows(categories []any, limit int) ([][]string, float64, float64) {
	type entry struct {
		name     string
		ocShare  *float64
		cmdShare *float64
	}
	entries := make([]entry, 0, len(categories))
	var ocCoverage, cmdCoverage float64
	for _, raw := range categories {
		item, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		oc := object(item, "oc")
		cmd := object(item, "cmd")
		ocShare := optFloatValue(oc["shareCalls"])
		cmdShare := optFloatValue(cmd["shareCalls"])
		if ocShare != nil {
			ocCoverage += *ocShare
		}
		if cmdShare != nil {
			cmdCoverage += *cmdShare
		}
		entries = append(entries, entry{name: text(item["category"]), ocShare: ocShare, cmdShare: cmdShare})
	}
	// The normalized list is already ordered by calls, but sort defensively by
	// the larger of the two shares so the folded remainder is genuinely tail.
	sort.SliceStable(entries, func(i, j int) bool {
		return maxShare(entries[i].ocShare, entries[i].cmdShare) > maxShare(entries[j].ocShare, entries[j].cmdShare)
	})
	take := limit
	if len(entries) <= limit+1 {
		take = len(entries)
	}
	rows := make([][]string, 0, take+1)
	var otherOC, otherCMD float64
	otherCount := 0
	for index, item := range entries {
		if index < take {
			ocText := "-"
			if item.ocShare != nil {
				ocText = fmt.Sprintf("%.1f", *item.ocShare*100)
			}
			cmdText := "-"
			if item.cmdShare != nil {
				cmdText = fmt.Sprintf("%.1f", *item.cmdShare*100)
			}
			rows = append(rows, []string{
				item.name,
				ocText,
				cmdText,
				signedPp(shareDiff(item.ocShare, item.cmdShare)),
			})
			continue
		}
		if item.ocShare != nil {
			otherOC += *item.ocShare
		}
		if item.cmdShare != nil {
			otherCMD += *item.cmdShare
		}
		otherCount++
	}
	if otherCount > 0 {
		label := "other"
		if otherCount > 1 {
			label = fmt.Sprintf("other (%d)", otherCount)
		}
		rows = append(rows, []string{label, fmt.Sprintf("%.1f", otherOC*100), fmt.Sprintf("%.1f", otherCMD*100), signedPp((otherOC - otherCMD) * 100)})
	}
	return rows, ocCoverage, cmdCoverage
}

func maxShare(a, b *float64) float64 {
	value := 0.0
	if a != nil {
		value = *a
	}
	if b != nil && *b > value {
		value = *b
	}
	return value
}

func shareDiff(a, b *float64) any {
	if a == nil || b == nil {
		return nil
	}
	return (*a - *b) * 100
}

func printVerdicts(w io.Writer, doc map[string]any, scope string) {
	verdictsByScope, _ := doc["verdicts"].(map[string]any)
	verdicts, _ := verdictsByScope[scope].([]any)
	if len(verdicts) == 0 {
		return
	}
	terminal.Section(w, "VERDICTS ("+scope+")")
	rows := make([][]string, 0, len(verdicts))
	for _, entry := range verdicts {
		item, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			text(item["metric"]),
			verdictValue(item),
			winnerOf(item),
			confidenceText(item["confidence"]),
			count(item["n"]),
		})
	}
	terminal.Table(w,
		[]string{"METRIC", "OC/CMD", "WINNER", "CONFIDENCE", "N"},
		rows,
		[]bool{false, true, false, false, true},
		[]int{30, 9, 9, 12, 8})
	terminal.Note(w, "ratio = OpenCode / Command Code · cost ratios <1 favor oc · n is the smaller side")
}

func verdictValue(item map[string]any) string {
	if item["unit"] == "% of input" {
		return signedPp(item["deltaPp"]) + " pp"
	}
	if item["ratio"] != nil {
		return ratioText(item["ratio"])
	}
	return "-"
}

// ---- document readers

func object(data map[string]any, key string) map[string]any {
	if value, ok := data[key].(map[string]any); ok {
		return value
	}
	return map[string]any{}
}

func list(data map[string]any, key string) []any {
	if value, ok := data[key].([]any); ok {
		return value
	}
	return nil
}

func winner(container map[string]any, key string) string {
	winners := object(container, "winner")
	value, _ := winners[key].(string)
	return value
}

func winnerOf(item map[string]any) string {
	switch value, _ := item["winner"].(string); value {
	case shared.ToolOC:
		return "oc wins"
	case shared.ToolCMD:
		return "cmd wins"
	case "tie":
		return "tie"
	default:
		return "n/a"
	}
}

func truthy(value any) bool {
	flag, _ := value.(bool)
	return flag
}

func text(value any) string {
	if value == nil {
		return "-"
	}
	if text, ok := value.(string); ok {
		if text == "" {
			return "-"
		}
		return text
	}
	return fmt.Sprintf("%v", value)
}

func count(value any) string {
	if value == nil {
		return "-"
	}
	number, ok := pyjson.Int(value)
	if !ok {
		return text(value)
	}
	return terminal.Count(number)
}

func number(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return trimFloat(number)
}

func numberWithUnit(value any, unit any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	unitText := text(unit)
	switch {
	case unitText == "USD":
		return fmt.Sprintf("$%.2f", number)
	case strings.HasPrefix(unitText, "$/"):
		return fmt.Sprintf("$%.4f", number)
	case unitText == "% of input":
		return trimFloat(number*100) + "%"
	case unitText == "tokens":
		return terminal.Compact(number)
	}
	return trimFloat(number)
}

func money(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.4f", number)
}

func percent(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.1f", number*100)
}

func signed(value any) string {
	entry, ok := value.(map[string]any)
	if !ok {
		return "-"
	}
	return signedPct(entry["weightedPct"])
}

func signedPct(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%+.1f%%", number)
}

func signedPp(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%+.2f", number)
}

func percent2(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.2f", number*100)
}

func int64Value(value any) int64 {
	number, ok := pyjson.Int(value)
	if !ok {
		return 0
	}
	return number
}

func optFloatValue(value any) *float64 {
	number, ok := pyjson.Float(value)
	if !ok {
		return nil
	}
	return &number
}

func ratioText(value any) string {
	number, ok := pyjson.Float(value)
	if !ok {
		return "-"
	}
	return fmt.Sprintf("%.2f×", number)
}

func winnerText(value any) string {
	switch value {
	case shared.ToolOC:
		return "oc wins"
	case shared.ToolCMD:
		return "cmd wins"
	case "tie":
		return "tie"
	default:
		return "n/a"
	}
}

func confidenceText(value any) string {
	text, _ := value.(string)
	if text == "" {
		return "-"
	}
	return text
}

func delta(value any, ocBetter, cmdBetter, side string) string {
	formatted := signedPct(value)
	if formatted == "-" {
		return "n/a"
	}
	switch side {
	case shared.ToolOC:
		return formatted + " (" + ocBetter + ")"
	case shared.ToolCMD:
		return formatted + " (" + cmdBetter + ")"
	case "tie":
		return formatted + " (tie)"
	default:
		return formatted
	}
}

func trimFloat(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}
